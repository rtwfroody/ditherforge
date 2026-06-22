# Investigation: white holes in flat surfaces (Nord Stage 4 model-thicker)

Status: **IN PROGRESS (2026-06-21 session 3).** ROOT CAUSE CORRECTED: the holes
are NOT inverted/flipped faces (clip preserves winding to 0.024%). They are
DROPPED EXTERIOR surface fragments — the clip drops the front-facing exterior
face of thin single-wall panels, exposing the correctly-wound back-facing
interior face, which reads as a white hole. See "CORRECTED (session 3)" below.
The fix is in clip COVERAGE, not winding. Both headless-repro blockers (A
voxelize determinism, B export key mismatch) are FIXED.

## Symptom

Loading `~/Documents/3d_print/objects/Nord+Stage+4+-+88+High+detailed+5.3/model-thicker.json`
(the settings file is right there next to the model; size 100, snapmaker_u1, layer
0.08, locked Steel Grey #616469 from Panchroma Basic, dizzy-corrected,
`colorAwareCells: true`, contrast 20, alpha-wrap, split Z@0.5125-frac) and viewing
the **positive-Z, top-down** Output Model: the **bottom of the keyboard (a black
rectangle)** shows **white triangular/wedge gaps in the middle of the flat
horizontal surface** (zoom in to see them). The GUI reported ~828k triangles.

The user has confirmed:
- Color/warp pins do NOT matter for this issue.
- It's a **top-down view of a horizontal surface** (not vertical walls, not a grazing artifact).
- The reverted `reorientShellToSource` workaround did **not** fix it.

## Blockers, now diagnosed (session 2)

### Blocker A — dither panic `index out of range` (the "color-aware CLI crash") — FIXED 2026-06-21

NOT a neighbor-graph off-by-one. It was a **cache-desync caused by non-deterministic
voxelize**, and the non-determinism lived in `colorGrid.pickMergeVictim`
(internal/cellslicer/colorcut.go, the color-aware partition): it tie-broke the merge
victim AND target by Go's randomized map iteration order, so the merge sequence and
final cell count varied run to run. FIXED (commit 5dcae57) with deterministic
tie-breaks: victim = smallest area then smallest label; target = most adjacency then
SMALLER-area neighbour then smallest label (the smaller-area rule is load-bearing —
plain smallest-label sweeps a gradient into one region, breaking
TestColorRegionsGradientNotCut). Repeated fresh runs now give identical cell counts;
the partial-bust crash repro completes; whole-dir cache wipes no longer needed.

Original mechanism (for reference):

- `dizzy-corrected` dither does `DitherCorrected(cells = po.Cells, neighbors = vo.Neighbors)`
  in `run.go` runDither. `po.Cells` flows Voxelize→ColorAdjust→ColorWarp→**Palette**;
  `vo.Neighbors` flows Voxelize→**Dither** directly. Both descend from Voxelize.
- **Voxelize / cellslicer is non-deterministic**: the visible-cell count varies
  run to run (observed 146507 / 146555 / 146572 / 146574 for the *same* settings).
  Likely a parallel (workers=12) tie/ordering race in the cellslicer or adjacency.
- Stage cache keys are content-independent (folded settings hashes only). So a
  cached descendant (ColorWarp/Palette, written from voxelize generation A) can be
  paired with a freshly-recomputed Voxelize (generation B) whose neighbor indices
  run past the older `po.Cells` length → out-of-range in dither.
- Repro: bust `voxelize` (and downstream) but leave `coloradjust`/`colorwarp`
  intact → crash. A **fully clean cache run completes** (voxelize runs once,
  memoized within the single pipelineRun `r`, so both paths see one generation).
- The GUI doesn't normally hit it because a normal run computes voxelize once.

Proper fix = **make voxelize deterministic**. Workaround for repro = `rm -rf
~/.cache/ditherforge` (whole dir) before each CLI run, never a partial bust.

### Blocker B — `ExportFile`: "pipeline has not been run yet" (v0.9.6 regression)

The fraction-of-extent refactor (committed today) made `RunCached` call
`r.resolveFractionalOptions()`, which mutates `r.opts` (`Split.Offset *= ext`,
`BaseColorMaterialXTileMM *= ext`, sticker center/scale). `Split.Offset` is in the
split-stage cache key, so every downstream stage's disk blob is keyed under the
**resolved** opts. But `ExportFile(cache, opts, …)` is called from both `main.go`
and `app.go` with the **original fractional** opts → `stageKey` differs →
`getLoad/getPalette/getMerge` all miss → "pipeline has not been run yet".
Affects CLI export and (by inspection of `app.go:346`, exports `*last`) the GUI
"Save Output" too. Fix = resolve the fractional opts in `ExportFile` as well
(extract a shared helper keyed off cached Preload's `ScaledMaxExtentMM`).

## What we measured earlier (still valid; on the real exported 3mf, 836k tris)

Two independent methods agreed there ARE inverted faces, but they're RARE:
- **Straight-down ray-cast**: topmost surface correctly outward on ~99.85% of area,
  **inverted on ~0.15%**, those inverted spots **single-layer** (true white holes),
  `withCloseSecondSurface = 0`.
- **Culled-vs-unculled render diff** (`debugrender.RenderInputCulled`, mimics the
  GUI's THREE.FrontSide back-face culling): culled-away pixels form thin triangular
  wedges matching the screenshot.

Ray-cast: body is a **hollow single-wall case** (2 hits/ray) → NOT thin-double z-fight.
Topology: where faces share vertex indices, 1,084,391/1,084,501 edges winding-consistent
(110 bad), 331,905 boundary edges → T-junctioned patchwork; flipped faces are
**disconnected cells** whose whole-patch normal points wrong (each patch internally
consistent). Topology can't catch them.

## Ruled OUT (with evidence)

- Coverage/zero-face wall slivers at the rim (real, but wall rings; visible defect is mid-surface).
- Vertical/sloped-wall under-coverage. Grazing-angle magnification. Z-fighting (single layer).
- Flipped normals as the WHOLE story / a topological winding error (reverted fix proves
  re-orienting to nearest source normal is insufficient).
- The cut cap itself (slab 766 tiles fully, 0 zero-face cells).

## The reverted attempt and WHY it failed

`reorientShellToSource` (in `runClip`) flipped any output face whose normal opposed the
nearest source-surface (alpha-wrap) normal: 153/129 faces flipped, zero regression — but
the user reports it did not fix the holes. Hypotheses: (1) the holes are NOT simple
inverted faces but **near-tangent or genuinely missing geometry** (dot≈0, never flips);
(2) my CLI reproductions only had inverted faces over *backed* regions (not single-layer
white), so the metric never moved. **Settle missing-vs-inverted on the REAL mesh first.**

## CORRECTED (session 3): the holes are DROPPED EXTERIOR faces, not winding flips

The session-2 "inverted faces" conclusion was a **measurement artifact** and is now
overturned. New instrumentation (`DITHERFORGE_FLIP_REPORT=1`, in `internal/pipeline/run.go`)
compares every clip-output face normal against the nearest source-surface normal in the
SAME bed-space frame (per split half), matched by perpendicular distance to the source
triangle's plane (so thin walls don't mis-match to the opposite face). Result on the
user's exact `model-thicker.json` (split, full clean cache):

- **Only 0.024% of clip-output faces are actually inverted** relative to their source
  (146 / 611,672), evenly spread across orientation and both halves. So **clip preserves
  winding faithfully — it does NOT flip faces.**
- The flag also dumps the clip INPUT (the split halves assembled in bed space) as
  `pr.DebugSourceMesh`; `--debug-stages-dir` renders it under `<dir>/source/`. **The clip
  INPUT has 0.000% top-down holes** (both the exterior top face and the interior wall face
  are present, exterior in front). **The clip OUTPUT has 1.269%.**

Reconciliation (this is the real mechanism): the keyboard bottom is a thin single-wall
panel with an exterior (front-facing, toward the top camera in the reoriented half) face
AND an interior (back-facing, into the cavity) face. **Clip drops the EXTERIOR fragment in
the wedge regions; the correctly-wound INTERIOR fragment survives** and becomes the
front-most surface top-down → it is back-facing → culled → reads as a white hole. The
session-2 unculled render showed "full coverage" only because the surviving interior face
fills the pixel — masking that the exterior face was dropped. So it IS missing geometry
(the exterior fragment), exposed as a back-face. NOT a winding error.

### Where in clip the exterior face is dropped (narrowed, not yet pinned)

- Ruled OUT: per-cell prism cap coplanarity. `DITHERFORGE_PRISM_ZEPS=0.003` extrudes each
  cell prism's Z range ±3µm so a boundary-coincident horizontal face is strictly interior
  to the prism — **zero change** (1.269% → 1.269%, identical zero-face counts). So the
  exterior face is dropped BEFORE the per-cell intersect, not at its Z caps.
- Two phenomena, by cell size:
  1. **Tiny ring cells go zero-face** (clip cell→face: ring 1px=747/1629=46%,
     2-4px=4490/6605=68% produce NO faces). Scattered rim/speckle holes.
  2. **Big diagonal wedges in NON-zero-face cells** (large cells are ~never zero-face:
     17-64px ring=28/68065). The cell keeps its interior face but lacks the exterior →
     exposed back-face. The wedges are diagonal slivers, consistent with a slightly-TILTED
     flat panel crossing slab boundaries along diagonal XY lines.
- Remaining suspects (both in the clip coverage domain, both heavily tuned — apollo /
  slivers / walls): (A) the per-slab pre-split (`splitSrcBySlabs`, Manifold `SplitByPlane`)
  drops the exterior fragment where it is coincident with / straddles a slab boundary
  plane; (B) the upper slab's cell footprint (color-aware cells segment per slab, so cell
  boundaries differ slab-to-slab) does not cover the XY where the exterior fragment landed.
  Next decisive (but slow / RAM-heavy) test: force the no-split-source path so every cell
  intersects the full `src` (no `SplitByPlane`); if the wedges vanish → (A).

## SUPERSEDED (session 2): "the holes are INVERTED FACES, not missing geometry"

Built `--debug-stages-dir` into the CLI (renders `pr.OutputMesh` top-down/bottom/persp:
`<view>_unculled.png`, `<view>_culled.png` = GUI FrontSide cull, `<view>_holes.png` =
culled render with every culled-away surface pixel painted magenta). Ran on the user's
exact `model-thicker.json` (fully-clean cache).

- Top-down output mesh, **unculled**: the flat keyboard-bottom panel is fully covered,
  NO blank gaps inside it. **culled**: thin diagonal wedges + a few larger patches go
  blank. `<view>_holes.png`: those go **magenta**. ⇒ surface is present but its front
  face points away from the top camera ⇒ **inverted (back-facing) triangles**. There is
  **no missing geometry** (no spot blank in BOTH renders inside the panel).
- Hole pixel fraction, top-down: **1.269%** of surface pixels (12,640 px) with split.
- This kills the "near-tangent / genuinely missing" hypothesis — the bad faces are
  squarely back-facing, large-dot-negative. So why didn't `reorientShellToSource` fix
  it? Prime suspect: it compared the output face normal (in split BED-space) against the
  source normal (in ORIGINAL-mesh space) without applying the per-half colorXform, so
  the comparison was garbage for the reoriented half and it flipped the wrong/too-few
  faces. (cf. [[project_split_cellslicer]]: SampleSlab needs ApplyInverse colorXform.)

### Split is the major contributor, but there is a baseline

Re-ran with `splitEnabled:false` (model-thicker-nosplit.json):
- top-down holes drop **1.269% → 0.364%** (4,209 px). The large magenta PATCHES are
  split-specific; what remains are **thin scattered slivers/chevrons** present without
  split. So TWO phenomena, both inverted faces:
  1. **Baseline sliver flips** (~0.36%, no split): occasional flipped sliver triangles —
     smells like the SlabSurfaceFootprint/`triBandXYPath` sliver family
     ([[project_vertical_wall_slivers]]) or per-cell clip winding.
  2. **Split-induced large patches** (the extra ~0.9%): big inverted wedges concentrated
     in the reoriented (z-down) half / near the split boundary.
- NOTE: the half-orientation matrices are *proper* rotations (det +1, asserted by
  `split.TestOrientationRotation`), so the rotation itself does NOT flip winding. The
  inversion is introduced upstream (cellslicer/clip emit some cells back-facing) and just
  becomes *visible* when the z-down half rotates the keyboard underside to face up. The
  unsplit "bottom" view has very few holes (0.026%), so it is NOT simply "all downward
  faces are inverted".

### Bisected: the inversions are emitted by CLIP, not Merge

Re-ran with `noMerge:true` (model-thicker-nomerge.json): top-down holes =
**12640 px / 1.269%**, byte-identical to the merge run. So **Merge neither adds nor
removes the inverted faces** — they are already present in the Clip output (the
`noMerge` output geometry IS the clip shell). The bug lives in the **Clip stage**
("Manifold merged-cell intersect, same-color cells per slab") or the cellslicer cells
feeding it — NOT in the coplanar merge. Combined with the split result, the split's
large patches come from clipping the reoriented (z-down) half in its bed-space frame.

## Concrete next steps (session 3 — corrected)

The "fix the winding / reorient pass" plan is DEAD (clip does not flip faces; reorienting
would do nothing). The real fix is **coverage: stop clip dropping the exterior fragment.**

1. Run the decisive (A)-vs-(B) test: force the full-`src` no-pre-split path and see if the
   big wedges vanish. (A) = `splitSrcBySlabs`/`SplitByPlane` boundary drop; (B) = per-slab
   footprint coverage gap. RAM/time-heavy — do single-threaded or on a smaller model.
2. If (A): make the per-slab `SplitByPlane` keep boundary-coincident / straddling exterior
   faces (perturb the cut plane off flat-surface Z, or overlap adjacent per-slab sources by
   an eps and rely on per-cell dedup). Cf. [[project_slab_grid_first_layer]],
   [[project_coincident_boundary_gray_walls]].
3. If (B): extend the upper slab's cell footprints to cover the exterior fragment XY (the
   `SlabSurfaceFootprints` / coverTarget family — [[project_apollo_holes_root_cause]],
   [[project_vertical_wall_slivers]]).
4. Blocker A (voxelize determinism) and Blocker B (export key mismatch) are FIXED; separate.

Diagnostics added this session (kept, env-gated, zero cost when off):
- `DITHERFORGE_FLIP_REPORT=1` → `[flip-report]` lines (output-vs-source winding) + populates
  `pr.DebugSourceMesh` (clip input) so `--debug-stages-dir` also renders `<dir>/source/`.

## Useful tooling notes

- CLI now takes the settings JSON directly: `/tmp/ditherforge-cli <settings.json>`.
  The user's exact config is `…/Nord+Stage+4…5.3/model-thicker.json`.
- `go build -o /tmp/ditherforge-cli ./cmd/ditherforge`.
- ALWAYS `rm -rf ~/.cache/ditherforge` (whole dir) before a CLI run until Blocker A is
  fixed — a partial bust desyncs voxelize and panics in dither.
- `debugrender.LoadInputMesh(path,&size)` loads glb/3mf w/ face colors; `LoadAnyModel` raw.
- debugrender views: az/el `{0,90}` is true top-down (depth=-Z); `{0,0}` ("front") looks along X.
- Cull check: `debugrender.RenderInputCulledWithBounds` vs unculled `render.RenderColor`.
