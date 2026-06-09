# Split in the cellslicer pipeline — design

Status: implemented on `mini-slicer-prototype` (2026-05-29, v0.9.28).
Owner: tim
Last updated: 2026-05-29

> **Implementation note (2026-05-29).** Landed as designed: Voxelize
> runs the cellslicer chain per half via `sliceSampleHalf`, `SampleSlab`
> gained a `colorXform` (`split.Transform.ApplyInverse`), Clip runs
> per-half via `clipPerHalf` and tags `ShellHalfIdx`, and export3mf's
> `splitModelByMesh` emits one `<object>` per half. Verified on the
> earth model (Z- and X-cuts) — both halves correctly textured and laid
> out — and by `TestSplitCellslicer_TwoHalves`. The unsplit path is
> bit-identical (the `building/top` sampled-match failure is
> pre-existing and unrelated). CLI split flags added: `--split`,
> `--split-axis`, `--split-offset`, `--split-connector`.

This doc describes how the Split feature (cut a model into two halves
that print separately and glue together) should be re-integrated now
that Voxelize/Clip have been rebuilt around `internal/cellslicer`
instead of the voxel-grid (`TwoGrid`) pipeline. It assumes you've read
`docs/SPLIT.md` (the user-facing design and the cut/connector/layout
geometry, all of which still apply unchanged).

## TL;DR

- The **geometry cut is already correct and already in the right place.**
  `StageSplit` runs after `StageLoad` and produces two watertight,
  bed-laid-out half meshes plus per-half transforms. None of that
  changes.
- What changed is **how the slicer consumes the halves.** On `main` the
  voxel grid iterated the two halves *inside* Voxelize and tagged each
  cell with `HalfIdx`, sampling color by inverse-transforming the cell
  centroid back to original-mesh coords. The cellslicer can't do that:
  its color sampling is welded to the slab coordinate frame.
- The fix is to run the **entire cellslicer chain (slice → footprint →
  partition → sample → clip) once per half**, with the half's bed-space
  geometry as the slicer input, and to give `SampleSlab` an inverse
  transform so color still comes from the untouched original-coords
  color model. `HalfIdx` then threads to the output as two `<object>`
  entries.
- This is what the "move it into the stage before voxelization"
  intuition resolves to: the **cut geometry must become the slicer's
  input mesh**, replacing `lo.Model`, rather than being a late per-cell
  tag applied during voxelization.

## How it worked on `main`

Pipeline order (both branches share these `StageID`s):

```
Parse → Load → Split → Decimate → Sticker → Voxelize
      → ColorAdjust → ColorWarp → Palette → Dither → Clip → Merge
```

On `main`, with the voxel-grid Voxelize:

1. **Split** (`run.go` `Split()`) cuts `lo.Model` on the plane, bakes
   connectors, and lays the two halves out side-by-side on the bed.
   `split.Layout` (`internal/split/layout.go:62`) **rewrites the half
   vertices in place to bed coords** and returns `Xform[h]`
   (original→bed; `Xform[h].ApplyInverse` is bed→original). Output is
   `splitOutput{Enabled, Halves[2], Xform[2], CutNormal, CutPlaneD}`.
2. **Decimate** ran once per half on `splitOutput.Halves[h]`.
3. **Voxelize** iterated the two half meshes. Each voxel cell recorded
   a `HalfIdx byte` (= which half mesh produced it). The geometry lived
   in **bed coords**, so the voxel layers were the real print layers.
   Color was sampled by mapping each cell centroid **back to
   original-mesh coords** via `Xform[halfIdx].ApplyInverse`, where the
   untouched `ColorModel` / `SampleModel` / sticker decals still lived.
   The color meshes were never cut or moved.
4. **Clip** ran per half (`clipSplit` → `ClipMeshByPatchesTwoGrid` once
   per half, with the decimated half mesh), then concatenated the two
   results with a vertex offset and produced a parallel
   `ShellHalfIdx []byte` tagging each output face's source half.
5. **Merge / Export** emitted **two `<object>` entries** in the 3MF —
   one per `HalfIdx` — which is what slicers expect for a multi-part
   print.

The load-bearing idea: **geometry in bed coords, color sampled in
original coords, reconciled per-cell by `ApplyInverse`.** `HalfIdx` was
*implicit* — just the source-mesh loop index — never a per-triangle
attribute until Clip stamped `ShellHalfIdx` for export.

## Why that doesn't port to the cellslicer as-is

The branch's Voxelize (`run.go:565`) does not iterate geometry the way
the grid did. It:

1. Slices **one** mesh — `geomModel := lo.Model` (`run.go:609`) — into
   horizontal Z-slabs (`SliceMesh`, `run.go:623`).
2. Computes a per-slab footprint, then partitions each slab into
   ring/hex cell polygons (`PartitionSlabAnalytic`, `run.go:683`).
3. Samples each cell's color in `SampleSlab`
   (`internal/cellslicer/sample.go:68`) by reading `lo.ColorModel`
   **directly at the cell's `(cx, cy, midZ)`** — the cell polygon's XY
   centroid and the slab's mid-Z (`sample.go:81`, `sample.go:96`).

Step 3 is the problem. **The slab partition, the cell polygons, the
sample points, and the color model all share one coordinate frame.**
There is no per-cell inverse-transform seam like the grid had —
`SampleSlab` assumes the geometry it sliced and the color model it
samples are in the *same* space.

So the grid's trick ("voxelize in bed coords, sample in original
coords") has nowhere to live. And Clip is monolithic too:
`ClipMeshToCellsManifold(lo.Model, vo.CellSlabs, vo.CellSize)`
(`run.go:1127`, `internal/cellslicer/clip_manifold.go:75`) clips the
*whole* model against *all* cells, with no half awareness — the result
carries no `ShellHalfIdx`, so Merge/Export can't separate the halves.

Today the branch papers over this: `Voxelize` and `Clip` call
`r.Split()` only so the stub caches, then **ignore the output**
(`run.go:577`, `run.go:1104`). Split is dead weight on this branch.

## The design

Run the **whole cellslicer chain once per half**, in the half's
bed-space geometry, and give color sampling an inverse transform so it
still reads the original-coords color model. Concretely, the per-half
unit of work is everything Voxelize+Clip currently do to `lo.Model`,
applied instead to each `splitOutput.Halves[h]`.

This keeps the cellslicer's single-frame assumption intact (per half,
geometry and slabs agree), and it reproduces `main`'s exact color
semantics — geometry in bed coords, color sampled in original coords —
by relocating the `ApplyInverse` from the grid traversal into
`SampleSlab`.

### Coordinate frames (the critical part)

Because `split.Layout` already rewrote the halves into **bed coords**:

- **Slice / footprint / partition / clip** run in bed coords per half.
  Slabs come out horizontal in *bed* orientation, i.e. they are the
  real print layers — exactly as on `main`, and correct for an X- or
  Y-axis cut whose layout rotates the cut face down (the half is
  reoriented *before* slicing, so the slabs follow the print, not the
  authored axis). This is the main reason to slice the laid-out half
  rather than the authored one.
- **Color sampling** must map each sample point back to original-mesh
  coords before touching `ColorModel`. `SampleSlab` gains an optional
  `colorXform *split.Transform` (or a plain `func([3]float32)
  [3]float32`); when non-nil it applies `colorXform.ApplyInverse(p)` to
  every sample point before `SampleNearestColor`
  (`sample.go:96`). `ColorModel`, the spatial index, and sticker decals
  stay in original coords, untouched and unrebuilt — same as `main`.
- **Clip output** is bed-coords fragments per half. No final transform
  needed; just concatenate the two halves (offsetting half 1's vertex
  indices) and stamp `ShellHalfIdx`.

A subtlety inherited verbatim from `main`: at the cut plane the
inverse-transformed sample points of the two halves land on opposite
sides of the original surface, so dithering on the two caps is computed
independently — the accepted "small dither mismatch at the seam" from
`docs/SPLIT.md`. Cap faces carry no texture and fall through to the
base-color path, so this is cosmetic and already-blessed behavior.

### Stage-by-stage changes

**Split (`run.go:318`).** No change. Already produces bed-space
`Halves[2]` + `Xform[2]`. Connectors and layout are untouched.

**Voxelize (`run.go:565`).** Stop ignoring `r.Split()`. Factor the
current body — `SliceMesh` → `ComputeFootprint` → `PartitionSlabAnalytic`
→ `SampleSlab` → adjacency — into a helper:

```go
func (r *pipelineRun) voxelizeOne(
    geom *loader.LoadedModel,       // bed-space half (or lo.Model when unsplit)
    colorModel *loader.LoadedModel, // always lo.ColorModel, original coords
    colorXform *split.Transform,    // nil when unsplit / identity
    halfIdx byte,
) (perHalfVoxels, error)
```

- Unsplit (`!so.Enabled`): call it once with `geom = lo.Model`,
  `colorXform = nil`, `halfIdx = 0`. **Bit-identical to today.**
- Split: call it once per half with `geom = so.Halves[h]`,
  `colorXform = &so.Xform[h]`, `halfIdx = h`, then **concatenate** the
  two results into one `voxelizeOutput`, offsetting the second half's
  global cell indices so `CellSlabs`, `CellSamples`, `VisibleToCell`,
  and the adjacency graph stay globally consistent.

`SliceMesh` already takes an arbitrary `*loader.LoadedModel`, so it
slices `so.Halves[h]` with no change. The only cellslicer change is the
`colorXform` parameter on `SampleSlab` described above.

Add `HalfIdx byte` to `cellslicer.CellSample` (and carry it on the
`Slab`/cell record the clip consumes). When unsplit it stays 0.

**Adjacency / Dither (`run.go`, Dither stage).** Build the adjacency
graph **per half and never connect across halves** — the bed gap means
there is no real spatial adjacency between the two pieces, and error
diffusion must not flow across the seam (matches `main`'s
per-connected-component behavior). Mechanically this falls out for free:
concatenate the two per-half adjacency graphs with disjoint index
ranges and add no cross-half edges. Dither itself is unchanged — it
already runs over a generic adjacency graph.

**Clip (`run.go:1085`).** Stop ignoring `r.Split()`. Mirror Voxelize:

- Unsplit: one `ClipMeshToCellsManifold(lo.Model, vo.CellSlabs, ...)` as
  today; `ShellHalfIdx = nil`.
- Split: one `ClipMeshToCellsManifold(so.Halves[h], slabs_h, ...)` per
  half (the slabs for half `h`, i.e. the slice of `vo.CellSlabs` that
  came from that half), concatenate with a vertex-index offset, and
  build `ShellHalfIdx []byte` parallel to the faces. Per-face palette
  assignment plumbing (`faceAssign`, `run.go:1134`) is unchanged — it
  keys on the global cell index, which already accounts for both halves.

Populate `clipOutput.ShellHalfIdx` (the field is already in `main`'s
design; add it to the branch's `clipOutput`).

**Merge (`run.go:~1287`).** The per-half merge path already exists in
`main` (`mergeSplitFaces` when `ShellHalfIdx != nil`); port it. When
`ShellHalfIdx == nil` (unsplit) it merges the whole mesh as today.

**Output / Export.** `mergeOutput` gains `ShellHalfIdx []byte` (nil when
unsplit). `buildOutputModel` produces one `LoadedModel` when nil and two
(split by `HalfIdx`) when set; `export3mf` writes the two as sibling
`<object>` entries. This is exactly `main`'s design — reuse it.

### What stays untouched

- `internal/split` — the cut, connectors, polylabel, and layout are
  all geometry-only and frame-agnostic. No change.
- `lo.ColorModel`, `lo.SampleModel`, `so.Model`, the sticker spatial
  index, and decal `TriUVs` all stay at original coords, uncut and
  unrebuilt. Stickers keep working with no change because the inverse
  transform lands sample points back in the frame where decals live.
- `SliceMesh`, `ComputeFootprint`, `PartitionSlabAnalytic`,
  `ClipMeshToCellsManifold` — no signature change. They just get called
  per half on a different input mesh.

The **only** cellslicer API change is the optional `colorXform` on
`SampleSlab` plus the `HalfIdx` field on `CellSample` / the cell record.

## Caching

`stageKey` already cascades from Split through Decimate, Voxelize, Clip,
Merge, so any `SplitSettings` change invalidates exactly those stages
and nothing upstream. Two notes:

- Keep the disabled-Split hash collapsing to a fixed empty value (as
  `main` does) so toggling Split off re-hits the pre-split cache for
  downstream stages.
- Bump `VersionSemver` once when this lands, because the
  `voxelizeOutput` / `clipOutput` shapes change (`HalfIdx`,
  `ShellHalfIdx`) and old disk-cache entries are no longer
  shape-compatible.

## Phasing

1. **Per-half plumbing, identity color frame.** Add `voxelizeOne` and
   the per-half loop in Voxelize+Clip; thread `HalfIdx`,
   `ShellHalfIdx`, two-object export. **Skip the `colorXform` for now**
   — sample with `colorXform = nil`, accepting wrong colors on the
   reoriented half. Goal: prove the geometry, indexing, adjacency, and
   two-object 3MF are correct. Gate on a fixture (e.g. Z-cut cube)
   producing two watertight objects.
2. **Correct color frame.** Add `colorXform` to `SampleSlab`; pass
   `&so.Xform[h]`. Now colors are right on both halves. Verify against
   a Z-cut (where layout is a 180° flip, so colors should match the
   unsplit output closely) and an X-cut (90° reorientation, the case
   Phase 1 gets visibly wrong).
3. **Per-half merge + seam polish.** Port `mergeSplitFaces`; confirm the
   seam dither mismatch is within the `docs/SPLIT.md` tolerance; add a
   cellslicer split test (two watertight halves, correct per-object face
   counts, no cross-half adjacency edges).

Each phase is independently shippable behind the existing Split toggle;
with Split off, every phase is bit-identical to the current branch.

## Open questions / risks

1. **Per-half slab Z ranges.** Voxelize computes `modelZRange(geomModel)`
   (`run.go:618`) and `SlabBoundaryPlanes` from it. Per half this must
   use **that half's** bed-space Z range, so the two halves can have
   different slab counts. The global-cell-index offset bookkeeping must
   not assume equal counts. (Low risk, just careful indexing.)
2. **Adjacency across the seam.** Confirm no code path infers
   adjacency from global cell index contiguity rather than from the
   explicit graph — if it does, the concatenation could accidentally
   bridge the two halves. Audit the adjacency builder before relying on
   "disjoint ranges, no cross edges."
3. **Clip robustness per half.** The connector booleans and the cap can
   leave the half mesh with near-degenerate features at the seam;
   `ClipMeshToCellsManifold` must stay watertight on them. Add the
   split halves to whatever the cleanup plan's Step 0 watertight
   invariant test covers (`docs/voxelize-clip-cleanup-plan.md`).
4. **Interaction with the Option-A cleanup.** This work and the
   voxelize/clip cleanup both touch `SampleSlab` and the Voxelize/Clip
   bodies. The `colorXform` param is small and additive; sequence it
   *after* the cleanup's Step 4 (`sample.go` rewrite) lands to avoid a
   merge collision, or coordinate the two edits.
