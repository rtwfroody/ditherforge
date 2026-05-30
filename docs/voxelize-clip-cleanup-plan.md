# Option A — Voxelize & Clip Cleanup Plan

The plan stages independent fixes in dependency order, with each step gated
by tests so regressions surface immediately.

## Step 0 — Promote hole-report to a CI invariant

**What.** Take the existing `reportHolesByPos` / `DumpFirstBoundaryEdge`
infrastructure in `clip2d.go` and wire it into a Go test that runs
`Voxelize` + `ClipMeshToCells2D` against a small fixture set (cube,
sphere, low_poly_building, lekythos vase) and asserts
`boundary == 0 && nonManifold == 0`.

**Why.** Every subsequent step touches load-bearing geometry. Without an
invariant test, we'll be re-discovering bugs the same way we did last
month.

**Files.** New `internal/cellslicer/watertight_test.go`; reuse fixtures
from `tests/objects/`.

**Done when.** `go test ./internal/cellslicer/...` fails if any of the
four fixtures produces a boundary or non-manifold edge. Mark known
failures with `t.Skip` + bug ref rather than relaxing thresholds.

**Risk.** Low. Worst case it documents the current bug pressure.

## Step 1 — Single canonical quantisation API

**What.** Add `internal/cellslicer/quant.go` exposing
`Quantize([3]float32) → int3D` and `Dequantize(int3D) → [3]float32`,
both using the existing `clipperScale = 1000` (1 µm) bucket. Replace
every ad-hoc `int3DOf`, the `lerpAtZ` verbatim-Z write, the Clipper
round trip, and the splice's float cross-product tolerance with calls
to this API.

**Why.** Today float-tolerance splice exists *only* because Clipper's
int snap and `lerpAtZ`'s float Z disagree about where the same point
is. Once quantisation is single-source, equality on `int3D` is exact
and the tolerance machinery is dead code.

**Files.** `clip2d.go` (~150 lines around `int3DOf`, slabZSet
construction), `clip2d_subdivide.go` (most of the splice —
cross-product tolerance, dedup), `clipper2d.go` (round-trip helpers),
`partition.go` (footprint round-trip if any).

**Done when.** Step 0 invariant still green; `tolAb2`, `cross2`, and
the `outInt`/`polyKey` machinery in `clip2d_subdivide.go` removed;
splice file shrinks by ≥30%.

**Risk.** Medium. Splice has a lot of subtle behaviour. Keep the
existing `splice_diag_test.go` running as a safety net during the
change; it's there exactly for this.

## Step 2 — Kill the cell-polygon raster round-trip

**What.** Replace `CellOutlineFromRaster`, `splitDisconnectedCells`,
and the `OuterEdgeOpen` flag with directly-emitted ring and hex
polygons. Generate `Cell.Outer` analytically from
`generateRingCellsRaw` / `generateHexCellsRaw`, then intersect each
with `fpCur` via Clipper. Pixel rasterisation stays only for the
`Pixels` histogram counter, not as a source of truth for the outline.

**Why.** This is the single biggest source of latent bugs. It causes
polyomino-shaped cells, disconnected-cell splits that perturb global
indexing, the open-ended outer-edge flag, the corner-duplication
"first worker wins" dedup, and the multi-pass backfill ribbon. All
four of those go away.

**Files.** `partition.go` (most of `PartitionSlabRaster`,
`backfillAnalyticCap`, `CellOutlineFromRaster`,
`splitDisconnectedCells` — net delete), `raster.go` (stamp helpers
become test-only), `cell.go` (remove `OuterEdgeOpen`), `clip2d.go`
(delete the open-ended-edge branches at lines ~442–448, ~479, ~691–708;
cross-piece dedup simplifies).

**Done when.** Step 0 invariant still green on all four fixtures;
`OuterEdgeOpen` symbol gone; cross-piece dedup no longer arbitrates
corner duplicates because they no longer exist; `partition.go`
shrinks by ~40%.

**Risk.** Medium-high. This changes what cells *look like*, which
means downstream colour-sampling tolerance and adjacency may need
re-tuning. Run the full pipeline against the regression objects in
`tests/objects/` and visually diff outputs.

## Step 3 — Unify cap and vertical-scan into one 3D-prism clip

**What.** Adopt the approach commit `21b7b25` reached for: store each
cell as a 3D prism (its outer polygon swept between `ZBot` and `ZTop`),
and clip every `slabPoly` against that prism via Sutherland-Hodgman
in 3D. Delete `clip2d_vertical.go`, `verticalPathRiskCount`,
`isPolyXYDegenerate`, and the cap/vertical dispatch in
`clipPolyToCells`.

**Why.** The dual-path dispatch is the *other* main source of
cross-path vertex mismatches. With analytic cell outlines from Step 2
and a single 3D clip path, the cap-vs-vertical edge-case zoo
(near-vertical sources, axis-aligned cube walls, the white-arc bug
class) collapses into one code path with one numerical regime.

**Files.** `clip2d.go` (large rewrite of `clipPolyToCells` /
`clipPolyToCellsCap` / `clipPolyToCellsVertical`), delete
`clip2d_vertical.go`, `clip_horizontal.go` becomes test-only.

**Done when.** Step 0 invariant still green; `DITHERFORGE_CLIP_MODE`
env var only honoured in tests; `verticalPathRiskCount` removed; the
splice in Step 1 no longer needs to handle the "two paths disagree"
case — it's pure cross-cell / cross-slab subdivision.

**Risk.** High in scope, low in surprise. The hard part — exact Z on
slab planes — is already solved by Step 1's canonical quantisation.
Performance should match or beat current cap path (no Clipper2
round-trip, no plane-equation Z-lift).

## Step 4 — Replace 5×5 bbox jitter with cell-interior colour sample

**What.** In `sample.go`, replace the `cellSize/2` bbox-radius 5×5
grid with rejection-sampled points *inside* `c.Outer`. Centroid + 4
inset corners is enough for most cells; for highly non-convex ones,
fall back to rejection sampling with a fixed budget.

**Why.** On thin or L-shaped cells (which become rarer after Step 2
but don't vanish — diagonal partition boundaries still produce them),
most of the 25 samples land outside the cell and pull adjacent
geometry's colour.

**Files.** `sample.go` only. ~60 lines.

**Done when.** Visual diff against current cake/charizard/earth
outputs is at-or-better; no regression on `tests/objects`
colour-fidelity test (or add one if missing).

**Risk.** Low. Localised change.

## Step 5 — Prune now-dead infrastructure

**What.** Sweep up what the prior steps obsoleted:

- `DITHERFORGE_HOLE_REPORT` becomes test-only (it's redundant with
  Step 0's CI invariant);
- the two package-level `debugHoles` globals in `clip2d.go` and
  `run.go` go away;
- `splice_diag_test.go`'s `runPhase1ForDiag` mirror can stop tracking
  production code (the warning at `clip2d.go:290–295` retires);
- `DITHERFORGE_CLIP_MODE=horizontal` and `clipSettings.HorizontalOnly`
  cache-key plumbing in `pipeline/stepcache.go` retire;
- atomic counters used only by hole-report retire.

**Why.** Each of these exists to debug a bug class that the earlier
steps deleted. Leaving them as zombie code costs reading time and
tempts future "this isn't covered" rationalisations.

**Files.** `clip2d.go`, `clip2d_subdivide.go`, `pipeline/run.go`,
`pipeline/stepcache.go`, `splice_diag_test.go`.

**Done when.** No `os.Getenv("DITHERFORGE_*")` in production paths;
no package-level `debugHoles` globals; `splice_diag_test.go` either
passes against the new code unchanged or is deleted with the bug it
was diagnosing.

**Risk.** Low. Pure deletion.

## Step 6 — Follow up with Option C

After Option A lands, the cellslicer codebase is in a much better
state to absorb Option C: replace the cap/splice clip pipeline with
[Manifold's](https://github.com/elalish/manifold)
`Boolean(mesh, prism, INTERSECT)` per cell (or `Decompose` per slab).
With Step 1's canonical quantisation, Step 2's analytic cells, and
Step 3's unified 3D clip already in place, the remaining work is just
CGO bindings and per-face tagging plumbing — `clip2d.go` and
`clip2d_subdivide.go` go away entirely, leaving cellslicer focused on
what it should be doing: emitting cells. We'll plan that separately
once Option A is green on all fixtures.

## Total scope

~2 weeks of focused work, gated by the Step 0 invariant at every
commit. Each step is independently shippable and reversible if a
regression bites.
