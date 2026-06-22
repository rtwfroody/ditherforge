# Investigation: white holes in flat surfaces (Nord Stage 4 model-thicker)

Status: **FIX NOT YET LANDED (2026-06-22 session 4).** The session-3 localization to the
slabCoverRegions buried exclusion was only PARTIALLY right: the proposed surfaceFps-union
fix was implemented and REVERTED — it changed nothing (holes 1.269%, buried area
byte-identical). The dropped panel surface is not in any surface-projection footprint at the
hole XY, so un-burying can't target it; NO_BURIED only "works" by tiling all interior. See
"TRIED AND FAILED (session 4)" below. Next: back-project a hole pixel to its slab/XY and
inspect the footprints there directly (no more guessing). Earlier session status:

Status: **ROOT CAUSE CONFIRMED (2026-06-21 session 3).** The holes are NOT
inverted/flipped faces (clip preserves winding to 0.024%). They are DROPPED
EXTERIOR surface fragments: the merged-group clip prism's footprint
UNDER-COVERS the in-band surface silhouette, so surface near the footprint
boundary projects outside the prism and is dropped, exposing the back-facing
interior face → white hole. Directly confirmed: dilating the footprint
(`DITHERFORGE_FOOTPRINT_GROW=0.5`) removes the big wedges (1.269% → 0.497%);
slab pre-split and prism Z-range are both ruled out. The fix is in per-slab
footprint coverage (NOT winding, NOT OpenEdgeBloatMM). Both headless-repro
blockers (A voxelize determinism, B export key mismatch) are FIXED.

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

### CONFIRMED (session 3): per-cell/group FOOTPRINT under-coverage drops the exterior face

The default clip path is the MERGED-cell path (`effectiveMergeCells` ON):
`clipPerHalfMerged` → `ClipMeshToMergedCellsManifold` → `clipOneGroupManifold`, which
extrudes ONE prism per same-color cell GROUP from the merged group contour. (NB: hooks
placed in `clipOneCellManifold` are DEAD on the real path — they only run in the rare
pinch fallback. Test in `clipOneGroupManifold`.)

Diagnostic experiments on the user's `model-thicker.json` (each: wipe cache, full run,
top-down `--debug-stages-dir` hole %):

| experiment | top-down holes | verdict |
|---|---|---|
| baseline (split) | 1.269% | — |
| `DITHERFORGE_NO_PRESPLIT=1` (every cell vs full `src`, no `SplitByPlane`) | 1.261% | **not the slab pre-split** |
| `DITHERFORGE_PRISM_ZEPS=0.1` on the group prism (±0.1mm Z, > slab height) | 1.269% | **not Z / cross-slab** |
| `DITHERFORGE_FOOTPRINT_GROW=0.2` (dilate group contour 0.2mm) | 0.685% | footprint coverage |
| `DITHERFORGE_FOOTPRINT_GROW=0.5` | 0.497% | footprint coverage |

At grow=0.5 the **big diagonal wedges are GONE** (top_holes.png) and zero-face cells are
nearly eliminated (ring 2-4px 4490→65, 5-16px 7975→2). So the dominant, user-visible
defect is **the merged-group prism footprint not covering the full source-surface
silhouette** — surface near the footprint boundary projects OUTSIDE the prism and the
intersection drops it, exposing the back-facing interior. Split triples it because the
reoriented half lays the panel near-horizontal, so its in-band silhouette within a thin
0.08mm slab is a wide diagonal band the footprint badly under-covers.

This DIRECTLY refutes the `OpenEdgeBloatMM` comment (clip_manifold.go ~L51-59): "the cell
footprint is the XY projection of the slab surface, so the surface never extends past the
footprint boundary… no distant geometry to reach out and grab." There IS — growing the
footprint reaches it. Cf. [[project_apollo_holes_root_cause]] (slab footprint used only
the 2 bounding-plane contours; mid-slab bulges projected outside it → SlabSurfaceFootprints
added). The fix belongs in how the per-slab cell footprint / `SlabSurfaceFootprints` covers
near-horizontal in-band surface, NOT in OpenEdgeBloatMM (a blunt global dilation regrows the
mesh and reintroduces skirt/merge asymmetry the 5µm margin was tuned to avoid).

Residual after grow=0.5 is ~0.36% small slivers = the separate, already-known baseline
sliver family ([[project_vertical_wall_slivers]]); NOT footprint-coverage.

### PINNED (session 4): the buried-interior exclusion in slabCoverRegions drops the surface

Narrowed the "footprint under-coverage" to its exact line. `coverTarget = band ∪ innerCap`
where `innerCap = inner − (fpBelow ∩ fpAbove)` (slabCoverRegions, partition.go). The
neighbour intersection `neighborBoth` marks a region "buried" (no exposed cap) when the
two neighbour slabs' BOUNDING-PLANE cross-sections are both solid there — but that test is
blind to surface exposed mid-slab, so for a near-horizontal panel it wrongly drops real
cap surface.

Evidence:
- `DITHERFORGE_COVER_REPORT` (run.go): per-slab `coverTarget` area vs Σ cell.Outer area.
  Cells FULLY tile coverTarget (deficit ≤0.014 mm², total ≈0) — so it is NOT a tiling
  failure. But `coverTarget` ≪ `fpCur` (slab 1171: fp=57.8 → cover=28.5; slab 38:
  fp=2370 → cover=125). The shrinkage is the buried subtraction.
- `DITHERFORGE_NO_BURIED` (partition.go): skip the `neighborBoth` subtraction →
  holes 1.269% → 0.701%. Half the holes are buried-excluded real surface. (Too blunt as
  a fix — it would also tile genuinely-hidden interior; cf. [[project_cellslicer_surface_only]].)
- `--debug-slab-svg 1171` → SVG: red=cells, orange=footprint, magenta=footprint-with-no-cells.
  A magenta WEDGE tapering to a point, matching the output holes. The footprint (= fpCur,
  the real in-band silhouette) includes the wedge; coverTarget excludes it; no cells tile
  it; clip drops the exterior face → hole. (CoverTarget is nil after the voxelize gob
  round-trip, so the SVG's HighlightUncovered falls back to Footprint and thus shows
  fpCur−cells = the buried region — which is exactly what we want to see.)
- Exonerated by A/B env gates: triBandXYPath sliver reject (`NO_SLIVER_REJECT`/
  `KEEP_HORIZ_SLICES` — no change), slab pre-split (`NO_PRESPLIT`), prism Z
  (`PRISM_ZEPS`), color-aware tiling (`colorAwareCells:false` — no change), winding.

TRIED AND FAILED (session 4): "union surfaceFps back into coverTarget." Threaded
`surfaceFps[i]` (and then `∪ interiorFps[i]`) into slabCoverRegions, subtracting it from
`neighborBoth` so the buried test can't drop exposed surface. Result: **holes unchanged
(1.269%)**, and `DITHERFORGE_COVER_REPORT` showed the panel slab's buried area UNCHANGED
(475.375 mm², byte-identical) — i.e. the in-band/interior surface footprints do NOT overlap
the buried-excluded wedge. Reverted.

WHY it failed / corrected mechanism: the buried exclusion is a PARTIAL RED HERRING.
- `DITHERFORGE_NO_BURIED` reduces holes (→0.701%) only by brute-force tiling the ENTIRE
  solid interior (body slabs have buried≈2250 mm² of genuinely-hidden interior); a
  body-interior cell's tall prism then incidentally re-captures the panel face. It is NOT
  covering the specific surface, so it is not a viable fix.
- The dropped panel surface is NOT present in `surfaceFps` or `interiorFps` at the hole
  XY. `fpCur` includes that area only via capFp's BOUNDING-PLANE cross-section (solid), so
  there is no surface marker to protect — the surface PROJECTION misses the panel there.
- Only `DITHERFORGE_FOOTPRINT_GROW` (grow cells past their footprint into a neighbour's
  coverage) reliably mitigates — consistent with "surface present, but no footprint marks
  its XY in that slab."

REVISED next step (do NOT guess again — instrument at a known hole): pick a magenta hole
pixel from `--debug-stages-dir top_holes.png`, back-project to a world XY + Z (the output
mesh is in bed space), find its slab, and directly inspect — at that exact XY — which of
fpCur / coverTarget / surfaceFps / interiorFps / capFp contain it, and whether the source
mesh has a face there. That pins whether the surface is (a) missing from fpCur entirely
(surface-projection bug — fix in SlabSurfaceFootprints/InteriorHorizontalFootprints) or
(b) in fpCur but buried AND not in any surface footprint (needs a different exposure test).
The session-4 attempt skipped this back-projection and guessed (b)+surfaceFps, which was
wrong. `--debug-slab-svg <idx>` already renders any slab; the missing piece is the
hole-pixel → slab/XY mapping.

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

The "fix the winding / reorient pass" plan is DEAD (clip does not flip faces). The cause is
CONFIRMED: **per-cell/group footprint under-coverage** drops the exterior fragment (see the
table above). The fix must make the per-slab cell footprint cover the full in-band surface
silhouette for near-horizontal surfaces — without a blunt global dilation.

1. Find where the per-slab cell footprint / `SlabSurfaceFootprints` is built and why it
   under-reaches for near-horizontal in-band surface (the in-band silhouette of a tilted
   panel in a 0.08mm slab is a wide diagonal band). Likely the same root as
   [[project_apollo_holes_root_cause]] (footprint from bounding-plane contours misses
   mid-slab surface) but for near-horizontal rather than bulging walls.
2. Validate any fix with `DITHERFORGE_FOOTPRINT_GROW` as the oracle (grow=0.5 ≈ the target:
   wedges gone, ~0.36% baseline residual) and the `--debug-stages-dir` top-down hole %.
   Do NOT just raise OpenEdgeBloatMM (regrows mesh, reintroduces skirt/merge asymmetry).
3. The ~0.36% baseline sliver residual is separate ([[project_vertical_wall_slivers]]).
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
