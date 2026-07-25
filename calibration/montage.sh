#!/usr/bin/env bash
# calibration/montage.sh — rebuild a fixture's winner-vs-pick comparison image.
#
#   calibration/montage.sh [fixture]        # default: benchy_all_free
#
# Writes calibration/groundtruth/<fixture>/compare_tuned_vs_winner.png: three
# perspective panels, sampled target | ground-truth winner | production pick.
#
# Two guards, both learned the hard way:
#   - Refuses to run on renders older than the fixture JSON. Stale renders from
#     a previous texture scale look perfectly valid and are the classic trap.
#   - Reads the panel labels out of sweep.log rather than hardcoding them, so a
#     re-swept table can never be captioned with the previous run's palettes.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

name=${1:-benchy_all_free}
dir="calibration/groundtruth/$name"
fixture="calibration/fixtures/$name.json"
log="$dir/sweep.log"
out="$dir/compare_tuned_vs_winner.png"

for f in "$fixture" "$log"; do
    [ -f "$f" ] || { echo "== no such file: $f" >&2; exit 1; }
done

for f in target_persp.png top1_persp.png prodpick_persp.png; do
    if [ ! "$dir/$f" -nt "$fixture" ]; then
        echo "== STALE: $dir/$f is not newer than $fixture" >&2
        echo "==        re-run: FORCE=1 calibration/sweep.sh $name" >&2
        exit 1
    fi
done

# names_of LINE -> "A|B|C|D" from the "#RRGGBB(Color Name)" tokens on the line.
names_of() { grep -oE '\([^)]+\)' <<<"$1" | tr -d '()' | paste -sd'|' -; }

# Winner: the rank-1 row of the ranking table ("1  15.050  50.256  #...").
win_row=$(grep -m1 -E '^1 +[0-9]+\.[0-9]+ ' "$log" || true)
[ -n "$win_row" ] || { echo "== could not find rank-1 row in $log" >&2; exit 1; }
win_key=$(awk '{print $2}' <<<"$win_row")
win_pal=$(names_of "$win_row")

# Production pick: the REGRET block (palette on one line, scores on the next).
pick_row=$(grep -m1 'production fast scorer picked' "$log" || true)
[ -n "$pick_row" ] || { echo "== could not find REGRET line in $log" >&2; exit 1; }
pick_pal=$(names_of "$pick_row")
score_row=$(grep -m1 -E 'its rank [0-9]+/[0-9]+' "$log")
pick_rank=$(grep -oE 'its rank [0-9]+/[0-9]+' <<<"$score_row" | awk '{print $3}')
pick_key=$(grep -oE 'rankKey [0-9]+\.[0-9]+' <<<"$score_row" | head -1 | awk '{print $2}')
pick_delta=$(grep -oE 'delta [0-9]+\.[0-9]+' <<<"$score_row" | tail -1 | awk '{print $2}')

montage \
    -label 'sampled target' "$dir/target_persp.png" \
    -label "winner: $win_pal  (rankKey $win_key)" "$dir/top1_persp.png" \
    -label "prod pick: $pick_pal  (rank $pick_rank, rankKey $pick_key, delta $pick_delta)" \
        "$dir/prodpick_persp.png" \
    -tile 3x1 -geometry +8+8 -background white "$out"

echo "wrote $out"
magick identify "$out"
