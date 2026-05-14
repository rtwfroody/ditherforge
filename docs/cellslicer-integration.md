# Cellslicer integration plan

## Goal

Replace the mini-slicer mesh-emission pipeline with a cellslicer-based
pipeline that colors the **original input mesh** instead of a staircase
remesh. Cells (small 2D polygons per slab) define color regions; the
original mesh is Boolean-cut along cell volumes so each output triangle
fragment inherits its enclosing cell's dithered color.

The cellslicer prototype (`cmd/cellslicer-prototype`) already produces
the cell polygons. This plan covers wiring it into the pipeline.

## End state

- `internal/cellslicer` package owns slab partition, cell polygon
  generation, and adjacency. Replaces `internal/minislicer` for
  partition and mesh emission.
- Voxelize stage runs cellslicer; per-cell colors sampled via existing
  `voxel.SampleByTriangle`.
- Dither stage unchanged structurally — runs over a cell adjacency
  graph instead of a section graph.
- Clip stage runs CGAL Boolean intersection: original mesh ∩ each
  cell's 3D prism → colored fragments.
- `internal/minislicer` deleted.
- VersionSemver bumped (cache invalidation).

## Phased work

### Phase 1: Package extraction

Move the prototype's cell-polygon machinery into `internal/cellslicer`:

- `footprint.go` — slab footprint (Clipper union of bot + top contours).
- `partition.go` — `walkLoopAtCellSize`, `generateRingCells`,
  `generateHexCells`, hex tessellation.
- `merge.go` — small-cell merge (operates on rasterized adjacency or
  on polygon graph; prototype uses raster, integrated version should
  switch to polygon-graph for accuracy).
- `cell.go` — `Cell`, `CellKind`, `Slab` types.
- `slice.go` — driver: `PartitionModel(model, layerH, cellSize) []Slab`.

The prototype's PNG renderer stays in `cmd/cellslicer-prototype` as a
debug tool. The library produces polygons, not pixels.

### Phase 2: Voxelize stage

Replace the Voxelize stage body in `internal/pipeline` to call
`cellslicer.PartitionModel`. Replace its existing per-layer contour
section output with cell output. Adapt the `voxelizeOutput` struct.

Color sampling: for each cell, sample the model at a small set of 3D
points inside the cell's prism (slab Z range × cell XY polygon) using
`voxel.SampleByTriangle` (already texture- and material-aware). Cell
RGB = mean or area-weighted average of samples.

### Phase 3: Dither stage

Build a cell adjacency graph from the partition:

- Within a slab: cells sharing a polygon edge are 4-neighbors-ish.
  Easiest derivation is pixel-rasterize at moderate resolution and
  read adjacency from the cellID grid (as the prototype's merge step
  does), then carry the adjacency forward.
- Across slabs: cell C in slab k is adjacent to cell C' in slab k+1
  if their XY polygons overlap. Use Clipper intersection area > 0
  as the test.

Run existing `voxel.DitherWithNeighbors` over the graph — its input is
already a generic adjacency graph, not a grid.

### Phase 4: Clip stage

Replace the current mesh emission with CGAL Boolean intersection:

- For each cell: build a 3D prism = cell polygon extruded over the
  slab Z range, with caps top and bottom.
- Boolean-intersect prism ∩ original mesh → output fragments.
- Each fragment is assigned the cell's dithered palette color.
- Aggregate all fragments into a single output mesh with per-face
  colors.

Use CGAL EPECK kernel via the existing `cgalbool` / `cgalclip` infra.
Robustness: input mesh may have minor issues; use `cgalclip`'s
existing repair path before the Boolean.

Performance: per-cell Boolean. With spatial indexing of model
triangles, each prism only intersects nearby triangles; expected
runtime is alpha-wrap-league rather than current Clip-stage-league.
Worth benchmarking early.

### Phase 5: Pipeline wiring

- Update `internal/pipeline/run.go` to call the cellslicer-based stages.
- Update step cache keys (`stepcache.go`) so the cellslicer pipeline
  has its own cache identity.
- Bump `VersionSemver` to invalidate prior disk-cache entries.

### Phase 6: Mini-slicer removal

After Phase 5 produces working output on the test models:

- Delete `internal/minislicer/`.
- Delete mini-slicer-specific tests.
- Remove mini-slicer references from `pipeline/`, `voxel/`,
  documentation.
- Decimate / Sticker / Split paths that fed into mini-slicer:
  re-evaluate whether they still apply. Sticker and Split are
  output-format concerns and probably stay; Decimate is moot once we
  emit per-fragment colors on the original mesh.

### Phase 7: Tests

Existing reference tests (`tests/sampled_match_input_test.go`) compare
against fixed PNGs. The new pipeline's output should be visually
similar but won't be pixel-identical:

- First pass: run tests as-is and inspect mismatches.
- If thresholds are too tight: widen them, or generate new
  references after manual visual approval.
- Add cellslicer-specific tests for: footprint correctness, cell
  count vs. model area, no-sliver invariant.

## Open questions / risks

1. **Boolean performance.** CGAL EPECK is the right kernel for
   robustness but it's slow. Need to benchmark on a real model
   (e.g., `low_poly_building`) early in Phase 4. If too slow, options:
   spatial pre-filter, batched Boolean, or fallback to approximate
   library (Manifold) with careful input sanitization.

2. **Mesh hygiene.** Some input models have non-manifold edges or
   tiny gaps. CGAL needs clean input. The existing `cgalclip` repair
   path handles this for alpha-wrap; reuse it.

3. **Per-face color limits.** Output meshes can have millions of
   tiny fragments. 3MF supports per-face colors but slicers may
   choke on extreme triangle counts. Plan: post-merge co-planar
   triangles of the same color before export (extend existing
   `voxel/merge.go` to work on cell-fragment output).

4. **Cell stability across slabs.** Currently each slab generates
   cells independently. For dither continuity along Z this may
   produce visible seams. Mitigation: align hex grid origin across
   slabs (use a global anchor), or accept the noise as a feature
   (it breaks up Z-line moiré).

5. **Sticker integration.** Stickers (decals on surface) currently
   compose into voxel.SampleByTriangle. They should keep working
   without changes since cellslicer samples the same way.

6. **Holes in the footprint.** Slabs with internal cavities produce
   footprints with hole loops. Cellslicer must handle these — outer
   loops generate ring cells, hole loops generate ring cells inside
   the cavity. Already supported in prototype via Clipper PolyTree.

## Order of execution

Phases are ordered for incremental verifiability:

1. Phase 1 (extract package): isolated, no runtime change.
2. Phase 2 (Voxelize): produces colored cells; can render via debug
   tooling without Phase 4.
3. Phase 3 (Dither): completes the cell-coloring chain.
4. Phase 4 (Clip): the big one. Can be tested independently with a
   trivial fixed-color cell partition before integrating with the
   real partition.
5. Phase 5 (wiring): hook everything up behind a flag.
6. Phase 7 (tests): validate before phase 6.
7. Phase 6 (mini-slicer deletion): only after the tests pass.

Phases 1–3 are low-risk and small. Phase 4 is the bulk of the
engineering work and the main unknown. Phase 5 onward depends on
phase 4 succeeding.
