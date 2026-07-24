#!/usr/bin/env bash
# calibration/sweep.sh — run every ground-truth fixture sweep, sequentially.
#
# The single entry point for (re)building the calibration ground-truth
# tables. For each settings JSON in calibration/fixtures/ it runs
# cmd/palettesearch into calibration/groundtruth/<fixture>/, teeing the
# harness output (including the REGRET line) to sweep.log there.
#
# A fixture is SKIPPED when its results.csv is newer than both the fixture
# settings and the inventory file. That check does not see harness code
# changes — after changing cmd/palettesearch or internal/pipeline, rerun
# with FORCE=1:
#
#   FORCE=1 calibration/sweep.sh            # redo everything
#   calibration/sweep.sh orzel_all_free ... # only the named fixtures
#
# Sweeps saturate the machine; never run two instances concurrently.
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

INV=calibration/fixtures/panchroma_curated20.txt
RENDER_TOP=5
BIN=$(mktemp -d)/palettesearch
trap 'rm -rf "$(dirname "$BIN")"' EXIT

echo "== building cmd/palettesearch"
go build -o "$BIN" ./cmd/palettesearch || exit 1

if [ $# -gt 0 ]; then
    fixtures=("$@")
else
    fixtures=()
    for f in calibration/fixtures/*.json; do
        fixtures+=("$(basename "$f" .json)")
    done
fi

failed=()
for name in "${fixtures[@]}"; do
    fx="calibration/fixtures/$name.json"
    out="calibration/groundtruth/$name"
    if [ ! -f "$fx" ]; then
        echo "== $name: no such fixture ($fx)" >&2
        failed+=("$name")
        continue
    fi
    if [ -z "${FORCE:-}" ] && [ -f "$out/results.csv" ] &&
        [ "$out/results.csv" -nt "$fx" ] && [ "$out/results.csv" -nt "$INV" ]; then
        echo "== $name: up to date, skipping (FORCE=1 to redo)"
        continue
    fi
    echo "== $name: sweeping -> $out"
    mkdir -p "$out"
    if ! "$BIN" --quiet --inventory "$INV" --render-top "$RENDER_TOP" \
        --out "$out" "$fx" 2>&1 | tee "$out/sweep.log"; then
        echo "== $name: FAILED (see $out/sweep.log)" >&2
        rm -f "$out/results.csv" # don't let a partial table look up-to-date
        failed+=("$name")
    fi
done

if [ ${#failed[@]} -gt 0 ]; then
    echo "== FAILED fixtures: ${failed[*]}" >&2
    exit 1
fi
echo "== all fixtures up to date"
