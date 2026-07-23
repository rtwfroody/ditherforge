# Palette-scorer calibration suite

Ground-truth palette-search tables used to tune (and guard against overfitting)
the production **fast selection scorer** in `internal/palette` — the code that
picks a filament palette from an inventory without an exhaustive sweep.

## Why this exists

The scorer's `mixSpreadMu` / `mixSpreadNu` / wash penalties were originally
calibrated on **all-free orzel** alone. That over-fit: once White was locked the
scorer's pick fell to rank ~359/2925 (see `groundtruth/orzel_white_pinned`).
This suite holds ground-truth tables for several fixtures with *different*
models and *different* pin configurations, so a single tuning target can no
longer dominate.

## Layout

```
fixtures/            settings JSONs (same format ditherforge-cli consumes)
groundtruth/<fix>/   results.csv  results.json  manifest.md  [target/top PNGs]
```

Each `results.csv` is produced by the `cmd/palettesearch` harness: it voxelizes
the model once, then exhaustively dithers + TD-simulates + renders + scores
every candidate palette against a palette-independent sampled target, ranking by
mean blur-ΔE at σ∈{2,8}. See `internal/pipeline/palettesearch.go`.

## Fixtures

| fixture | model | pins | free / eligible | candidates |
| --- | --- | --- | --- | --- |
| `orzel_white_pinned` | orzel eagle (OBJ) | White locked | 3 / 27 | 2925 |
| `orzel_all_free` | orzel eagle (OBJ) | none | 4 / 28 | 20475 |
| `earth_all_free` | earth sphere (GLB, in-repo) | none | 4 / 28 | 20475 |
| `benchy_all_free` | 3DBenchy + rainbow MaterialX | none | 4 / 28 | 20475 |

`gray-eagle`, named in the scorer's calibration comment, is a **synthetic**
sample cloud in `internal/palette/td_select_test.go` (no model file), so it has
no sweep table here — it remains a fast unit-test fixture.

## Constraints on the fixture settings

The harness scores directly from cells, so a few settings are unsupported and
were normalized in these fixtures (noted per-fixture in each `manifest.md`):

- **`colorSnap` must be 0** (it mutates cell colors). earth's natural `5` and
  benchy's natural `7` were set to 0.
- **`tdModel` must be the default area model** (not `layered`). benchy's natural
  `layered` was changed to area.
- Models outside the repo are referenced by **absolute `inputFile` paths**, so
  those tables are reproducible only on the author's machine. The in-repo earth
  model uses a repo-relative path and is fully portable. Model files are not
  committed; only the settings + tables are.

## The tuning loop

Build a table once (slow, hours), then iterate the scorer against it in seconds:

```sh
# one-time ground truth
palettesearch calibration/fixtures/orzel_white_pinned.json \
  --quiet --out calibration/groundtruth/orzel_white_pinned

# fast regret readout — repeat per candidate μ/ν/wash
for MU in 0.10 0.15 0.20; do
  DITHERFORGE_SELECT_MU=$MU palettesearch \
    calibration/fixtures/orzel_white_pinned.json \
    --regret-table calibration/groundtruth/orzel_white_pinned/results.csv
done
```

`--regret-table` voxelizes + runs the production scorer in-process (so the
`DITHERFORGE_SELECT_WASH/MU/NU` env overrides apply) and prints the pick's rank
in the given table. A good tuning is one that lowers the regret delta across
*all* fixtures, not just one.
