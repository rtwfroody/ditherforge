#!/usr/bin/env bash
# calibration/tune.sh — coordinate-descent tuner for the palette-selection scorer.
#
# The single entry point for (re)tuning the fast scorer's weights against the
# frozen ground-truth tables in calibration/groundtruth/. It never re-runs a
# ground-truth sweep (that is sweep.sh's job); it only replays the production
# fast scorer via `palettesearch --regret-table`, whose env overrides
# (DITHERFORGE_SELECT_WASH/MU/NU/SPREAD/MIXSAT) select the weight vector.
#
# Objective per candidate vector: sum of the 4 fixtures' REGRET deltas (rankKey
# gap between the production pick and the ground-truth winner). We also track the
# worst single-fixture delta. A candidate replaces the best-so-far only if the
# total strictly improves AND no single fixture regresses past its phase-1
# baseline by more than NOREGRESS_SLACK.
#
# Every evaluation appends a row to calibration/tune_log.csv and is served from
# that cache on re-run (keyed by the exact weight tuple), so the descent is
# idempotent and cheap to resume. FORCE=1 ignores the cache and re-evaluates.
#
#   calibration/tune.sh           # run / resume the descent
#   FORCE=1 calibration/tune.sh   # ignore cached rows, re-evaluate everything
#
# The first regret run per fixture pays the one-time voxelize (served from the
# pipeline disk cache thereafter); the descent's ~100+ evals are cache-hot.
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

INV=calibration/fixtures/panchroma_curated20.txt
LOG=calibration/tune_log.csv
BIN=$(mktemp -d)/palettesearch
trap 'rm -rf "$(dirname "$BIN")"' EXIT

echo "== building cmd/palettesearch"
go build -o "$BIN" ./cmd/palettesearch || exit 1

# Fixtures, in the fixed CSV column order d_orzelwp,d_orzelaf,d_earth,d_benchy.
FIX_NAMES=(orzel_white_pinned orzel_all_free earth_all_free benchy_all_free)
# Phase-1 regret baselines (per fixture, same order). The no-regress guard
# rejects any candidate whose fixture delta exceeds baseline + NOREGRESS_SLACK.
BASELINE=(1.866 2.105 3.003 13.285)
NOREGRESS_SLACK=0.25

# Value ladders (geometric around the current defaults). MIXSAT's last rung is
# effectively "off" (linear mix-spread). CHROMA_FALLOFF is left fixed.
LADDER_MIXSAT=(10 15 22 30 45 70 120 1000000)
LADDER_MU=(0 0.05 0.10 0.15 0.22 0.30)
LADDER_WASH=(0.2 0.4 0.6 0.9 1.2)
LADDER_NU=(0 0.04 0.08 0.15)
LADDER_SPREAD=(0.15 0.3 0.5)

# Start point = current defaults (all rungs of their own ladders).
CUR_WASH=0.6
CUR_MU=0.15
CUR_NU=0.08
CUR_SPREAD=0.3
CUR_MIXSAT=30

# eval WASH MU NU SPREAD MIXSAT
# Prints one CSV row: wash,mu,nu,spread,mixsat,d_orzelwp,d_orzelaf,d_earth,d_benchy,total,max
# Served from LOG when the tuple is already recorded (unless FORCE=1). Exits the
# whole script if any regret run fails or its delta can't be parsed — a bogus row
# must never be recorded.
eval_vec() {
    local wash=$1 mu=$2 nu=$3 spread=$4 mixsat=$5
    local key="$wash,$mu,$nu,$spread,$mixsat"

    if [ -z "${FORCE:-}" ] && [ -f "$LOG" ]; then
        local cached
        # Anchor on the exact 5-field prefix; escape dots so grep treats them
        # literally. -m1 stops at the first (only) match.
        cached=$(grep -m1 -E "^${key//./\\.}," "$LOG" || true)
        if [ -n "$cached" ]; then
            printf '%s\n' "$cached"
            return 0
        fi
    fi

    local deltas=() i name table out rc d
    for i in 0 1 2 3; do
        name=${FIX_NAMES[$i]}
        table="calibration/groundtruth/$name/results.csv"
        if [ ! -f "$table" ]; then
            echo "== $name: missing ground-truth table $table (run sweep.sh first)" >&2
            exit 1
        fi
        out=$(DITHERFORGE_SELECT_WASH="$wash" DITHERFORGE_SELECT_MU="$mu" \
            DITHERFORGE_SELECT_NU="$nu" DITHERFORGE_SELECT_SPREAD="$spread" \
            DITHERFORGE_SELECT_MIXSAT="$mixsat" \
            "$BIN" --quiet --inventory "$INV" --regret-table "$table" \
            "calibration/fixtures/$name.json" 2>&1)
        rc=$?
        if [ $rc -ne 0 ]; then
            echo "== regret run FAILED for $name (rc=$rc) at $key" >&2
            printf '%s\n' "$out" >&2
            exit 1
        fi
        # The delta is the last "delta N.NNN" token in the REGRET report.
        d=$(printf '%s\n' "$out" | grep -oE 'delta [0-9]+\.[0-9]+' | tail -1 | awk '{print $2}')
        if [ -z "$d" ]; then
            echo "== could not parse REGRET delta for $name at $key" >&2
            printf '%s\n' "$out" >&2
            exit 1
        fi
        deltas[$i]=$d
    done

    local total max
    read -r total max < <(awk -v a="${deltas[0]}" -v b="${deltas[1]}" \
        -v c="${deltas[2]}" -v e="${deltas[3]}" \
        'BEGIN{t=a+b+c+e; m=a; if(b>m)m=b; if(c>m)m=c; if(e>m)m=e; printf "%.4f %.4f", t, m}')

    local row="$key,${deltas[0]},${deltas[1]},${deltas[2]},${deltas[3]},$total,$max"
    if [ ! -f "$LOG" ]; then
        echo "wash,mu,nu,spread,mixsat,d_orzelwp,d_orzelaf,d_earth,d_benchy,total,max" >"$LOG"
    fi
    printf '%s\n' "$row" >>"$LOG"
    printf '%s\n' "$row"
}

# row_field ROW N -> the Nth comma-separated field.
row_field() { printf '%s\n' "$1" | cut -d, -f"$2"; }

# passes_guard ROW -> 0 (true) if no fixture delta exceeds baseline + slack.
passes_guard() {
    awk -F, -v b0="${BASELINE[0]}" -v b1="${BASELINE[1]}" -v b2="${BASELINE[2]}" \
        -v b3="${BASELINE[3]}" -v g="$NOREGRESS_SLACK" \
        '{ if ($6<=b0+g && $7<=b1+g && $8<=b2+g && $9<=b3+g) exit 0; else exit 1 }' <<<"$1"
}

# less_than X Y -> 0 (true) if X < Y (float).
less_than() { awk -v x="$1" -v y="$2" 'BEGIN{exit !(x<y)}'; }

echo "== evaluating start point (defaults)"
BEST_ROW=$(eval_vec "$CUR_WASH" "$CUR_MU" "$CUR_NU" "$CUR_SPREAD" "$CUR_MIXSAT")
BEST_TOTAL=$(row_field "$BEST_ROW" 10)
echo "   start: $BEST_ROW"

# Coordinate descent: 2 full passes, params in order MIXSAT, MU, WASH, NU, SPREAD.
for pass in 1 2; do
    echo "== pass $pass"
    for param in MIXSAT MU WASH NU SPREAD; do
        ladder_name="LADDER_$param[@]"
        ladder=("${!ladder_name}")
        cur_name="CUR_$param"
        best_val=${!cur_name}
        improved=0
        for val in "${ladder[@]}"; do
            # Build the candidate vector: current best with $param set to $val.
            local_wash=$CUR_WASH local_mu=$CUR_MU local_nu=$CUR_NU
            local_spread=$CUR_SPREAD local_mixsat=$CUR_MIXSAT
            case $param in
                MIXSAT) local_mixsat=$val ;;
                MU) local_mu=$val ;;
                WASH) local_wash=$val ;;
                NU) local_nu=$val ;;
                SPREAD) local_spread=$val ;;
            esac
            row=$(eval_vec "$local_wash" "$local_mu" "$local_nu" "$local_spread" "$local_mixsat")
            total=$(row_field "$row" 10)
            mark=""
            if less_than "$total" "$BEST_TOTAL" && passes_guard "$row"; then
                BEST_TOTAL=$total
                best_val=$val
                BEST_ROW=$row
                improved=1
                mark=" <= new best"
            fi
            printf '   %-6s=%-8s total=%-9s max=%-8s%s\n' \
                "$param" "$val" "$total" "$(row_field "$row" 11)" "$mark"
        done
        printf -v "$cur_name" '%s' "$best_val"
        if [ "$improved" -eq 1 ]; then
            echo "   -> $param := $best_val (total $BEST_TOTAL)"
        fi
    done
done

echo
echo "BEST: wash=$CUR_WASH mu=$CUR_MU nu=$CUR_NU spread=$CUR_SPREAD mixsat=$CUR_MIXSAT"
echo "BEST: total=$(row_field "$BEST_ROW" 10) max=$(row_field "$BEST_ROW" 11)"
echo "BEST: d_orzelwp=$(row_field "$BEST_ROW" 6) d_orzelaf=$(row_field "$BEST_ROW" 7) d_earth=$(row_field "$BEST_ROW" 8) d_benchy=$(row_field "$BEST_ROW" 9)"
