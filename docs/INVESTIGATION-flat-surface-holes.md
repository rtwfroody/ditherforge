# Investigation: white holes in flat surfaces (Nord Stage 4 model-thicker)

Status: **FIX NOT YET LANDED (2026-06-22 session 5).** Session 5 OVERTURNED the
slabCoverRegions buried-exclusion theory entirely: the `DITHERFORGE_HOLE_PROBE` ground-truth
(strict slab attribution) shows the partition-dropped region is buried at EVERY slab —
EXPOSED=0 — so the buried exclusion drops no exposed surface. Also EXONERATED: the
surface-projection sliver-reject (`KEEP_HORIZ_SLIVERS` → byte-identical 1.269%). The holes
are thin diagonal slivers on the flat top; the only oracle that moves the number is
`FOOTPRINT_GROW` (grows the CLIP prism outward), so the gap is at the merged-group clip
prism boundary, NOT partition coverage. See "Session 5" below. Next: back-project a specific
sliver to its slab/XY and inspect the clip prism there. Earlier session status:

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

## Session 5 (2026-06-22) — TWO theories overturned; holes are thin diagonal slivers

Built `DITHERFORGE_HOLE_PROBE` (run.go, committed): for every slab it samples the dropped
footprint (`Footprint − CoverTarget`) and **ray-casts the source `geom`** (same partition
frame — no cross-frame mapping) to decide, per sample, whether the TOPMOST source surface
over that XY lies **in this slab** (an exposed cap wrongly dropped) or **above it**
(correctly buried), plus footprint membership (cap/surface/interior/neighborBoth) and the
producing triangle's `|nz|` / plane-span.

**Result with STRICT slab attribution (`slabIndexForZ(topZ) == i`): EXPOSED = 0 across
EVERY slab in BOTH halves.** Every partition-dropped region is genuinely buried (always real
surface above it). An earlier `tol=thick` version mis-attributed the slab-*above*'s cap to
the slab below and produced false "EXPOSED=442" counts — corrected to strict.

Consequences:
1. **The `slabCoverRegions` buried exclusion (`neighborBoth = capFps[i-1] ∩ capFps[i+1]`)
   is EXONERATED** — it drops no exposed surface. OVERTURNS the session-4 pin and the
   `surfaceFps`-union direction. (`NO_BURIED` only "helped" by brute-force tiling ~2250
   mm²/slab of genuinely-hidden body interior whose tall prisms incidentally re-cover the
   cap — not a real fix.)
2. **Visual ground truth** (`--debug-stages-dir top_holes.png`, clean cache = 1.269%): holes
   are **long thin DIAGONAL SLIVERS** across the flat top panel + a couple larger wedges;
   they look like coarse triangle edges / radiating lines.
3. **Surface-projection sliver-reject EXONERATED.** `DITHERFORGE_KEEP_HORIZ_SLIVERS` (keep
   the thin in-band strip of near-horizontal cap triangles past the `triBandXYPath` aspect
   reject) → top holes **byte-identical 1.269% / 12637 px**. So the sliver *shape* is not
   surfaceFps dropping near-horizontal strips. (Reverted; confirms prior `NO_SLIVER_REJECT`.)
4. Built-in **zero-face clip cells are all RING (wall) cells** at slabs 1,2,3,959,802,1235…
   — NOT the flat-top cap slabs (47/48 half0, 1189 half1). So the flat-top holes are
   **neither a partition drop NOR a zero-face clip cell**.

**Where it leaves it:** the only oracle that ever moved the number is `FOOTPRINT_GROW`
(grows each CLIP prism footprint OUTWARD, in `clipOneGroupManifold`). With partition-drop all
buried and clip-prism-grow the thing that helps, the missing surface lies **just OUTSIDE the
merged-group prism footprint** — a CLIP-stage coverage gap at cell/group footprint
boundaries (cf. residual "ill-defined Clipper tie-break at coverTarget coincident edges",
[[project_footprint_bucket_clip]]), NOT a partition coverTarget gap. keepHorizSlivers not
helping says the gap is NOT near-horizontal surface added to fpCur.

**Corrected next step (ground-truth, don't theorize):** back-project a specific magenta
sliver from `top_holes.png` (bed space top-down: `pixelX=-worldY·s+cx`, `pixelY=-worldX·s+cy`
from `render.ProjectedBounds(az=0,el=90)`; Z by ray-casting the OUTPUT mesh at that XY), map
bed→partition frame for its half (split reorient inverse), find its slab, and check directly
whether that XY is in `footprints`/`coverTarget`/a cell and why the merged-group clip prism
fails to cover it. Pins partition-gap vs clip-prism-gap vs Clipper-tie-break.

Diagnostic committed this session: `DITHERFORGE_HOLE_PROBE` (run.go).

## Useful tooling notes

- CLI now takes the settings JSON directly: `/tmp/ditherforge-cli <settings.json>`.
  The user's exact config is `…/Nord+Stage+4…5.3/model-thicker.json`.
- `go build -o /tmp/ditherforge-cli ./cmd/ditherforge`.
- ALWAYS `rm -rf ~/.cache/ditherforge` (whole dir) before a CLI run until Blocker A is
  fixed — a partial bust desyncs voxelize and panics in dither.
- `debugrender.LoadInputMesh(path,&size)` loads glb/3mf w/ face colors; `LoadAnyModel` raw.
- debugrender views: az/el `{0,90}` is true top-down (depth=-Z); `{0,0}` ("front") looks along X.
- Cull check: `debugrender.RenderInputCulledWithBounds` vs unculled `render.RenderColor`.

## Session 6 (2026-06-22) — HOLES PINNED TO INTER-GROUP PRISM SEAMS (geometric gap)

Three probes + a controlled discriminator. All committed (clip_cover_probe.go,
probeSurfaceBeyondFootprint in run.go, env-gated seam/open bloat in clip_merge.go).

1. **Inside-contour coverage** (`DITHERFORGE_CLIP_COVER_PROBE`): per merged group, rasterize
   the slab's up-facing top-cap SOURCE surface, the clip OUTPUT (up-facing only — a white hole
   is the up exterior dropped while the down interior survives at the same XY, so plain XY
   coverage falsely reads "covered"), and the prism contour. Inside a group's OWN contour the
   up-facing cap is **99.999% covered** (222 dropped px / 29.16M). → NOT a CSG crack WITHIN a
   contour. Refutes the within-contour-(b) hypothesis.
2. **Beyond the outer footprint** (`probeSurfaceBeyondFootprint`, run.go, HOLE_PROBE-gated):
   only ~1.3 mm² of exposed up-facing cap projects OUTSIDE footprints[i], clustered at a low
   ledge (Z≈3.5–4 mm), NOT the visible flat top. → NOT a simple outer-footprint under-reach.
3. **The hole image** (`top_holes.png`, this build): holes are **long thin straight diagonal
   slivers tracing interior lines across the panel** + a few wedges = cell/group SEAM lines,
   not the outer edge. (`geom` is confirmed in BED coords — "Each half's geometry is in bed
   coords" — so partition +Z = bed-top; the up-facing probes are valid, no flip caveat.)

By elimination the drops are at the **seams BETWEEN adjacent group prisms** (neither probe
targeted that region: one looked strictly inside a contour, the other strictly outside the
footprint). Confirmed by the discriminator below.

**Discriminator** (`DITHERFORGE_SEAM_BLOAT` grows non-open/seam edges; `DITHERFORGE_OPEN_BLOAT`
grows open/footprint-boundary edges; clip_cover_probe.go). Baseline 1.269%:

| bloat | open (outer boundary) | seam (between groups) |
|-------|-----------------------|-----------------------|
| 0.2mm | 1.095% | 0.971% |
| 0.5mm | 0.943% | 0.711% |
| both 0.2mm (uniform) | 0.718% (≈ old FOOTPRINT_GROW=0.2 0.685% → discriminator is faithful) |

- **Seam is the dominant lever (~1.7× the boundary effect).**
- Seam sweep is a **smooth proportional ramp** (0.02→1.174, 0.05→1.079, 0.1→1.023, 0.2→0.971,
  0.5→0.711), NOT a step. A sub-µm numerical coincidence crack would close with a ~10µm offset
  then flatten; this scales with distance to 0.5mm → the seam gap has **real geometric width
  (tenths of a mm)**, not an FP crack.

**Conclusion:** white holes = geometric coverage gap at the seams between adjacent merged-group
clip prisms (dominant) + a smaller share at the outer footprint boundary. Adjacent
different-color group prisms are each Intersection()'d with the source independently and don't
reliably meet at their shared wall, leaving a thin uncovered band → dropped exterior face →
back-facing interior shows → white hole. Growing prisms so neighbors OVERLAP at seams closes it
(why FOOTPRINT_GROW was the only lever). Refines, not contradicts, "footprint under-coverage":
the under-coverage is specifically at INTER-GROUP SEAMS, not the outer silhouette.

**Next:** experimental fix = default seam bloat on non-open contour edges (overlap adjacent
prisms). Watch for duplicate coincident faces / non-manifold growth from the overlap.

---

## Session 7 (2026-06-22) — ROOT CAUSE: flat top straddles a slab boundary; per-slab clip handoff

Visualized the clip INPUT mesh and the CELLS together, per slab (committed tooling:
`--debug-stages-dir` with `DITHERFORGE_FLIP_REPORT=1` writes `cells_overlay`,
`cells_over_holes`, `cells_toplayer[_byslab]`, and `perslab/slab_NNNN`).

Findings:
- **Clip input is solid** where the holes are (gray, 0.000% top-down holes); cells **do** tile
  the hole region (blue grid runs through the magenta in `cells_over_holes`). So neither a bad
  mesh nor a missing cell.
- `cells_toplayer_byslab` (topmost cell per pixel, filled by slab index): the nominally-flat top
  is a **patchwork of ADJACENT slab indices** in coarse-triangle regions; **every hole sits on a
  slab/slab boundary**.
- Per-slab cell counts: the top surface median Z = **4.042**, sitting **right on the slab 48/49
  boundary (4.043)**. Half0 concentrates **6424 cells in slab 48**; coarse-triangle patches drift
  into slab 49 (1550) and 50–53 (~30–50). Half1's top is gently tilted → ~100 cells each across
  slabs 683–693. (Two split halves, each contributing part of the top at Z≈4.)
- `perslab/slab_0048`: green = source surface in slab 48's Z-band (prism captures it), gray =
  surface drifted into slab 49 (no slab-48 cells there), **magenta holes on the green↔gray
  seam**.

**Mechanism (confirmed):** because the flat top lies essentially ON a slab plane, neighbouring
surface patches split between slab N and N+1. Each slab's cells are extruded into a prism over
ONLY that slab's 0.08mm band and Intersection()'d with the source independently. Along the
boundary line between an N-patch and an (N+1)-patch the two prisms' XY footprints abut on the
exact surface/plane crossing; the independent CSG at that coincident line drops a thin sliver →
exterior face gone → back-facing interior shows → white hole.

**Comparison-semantics check (does `<` vs `<=` cause it?):** No, not directly. `triBandXYPath`
clips the per-slab footprint with `clipPolyZHalf(zBot, keep z>=zBot)` AND `(zTop, keep z<=zTop)`
— a **CLOSED [zBot,zTop]** band on both ends, so adjacent slabs' footprints **already OVERLAP**
on the shared plane (not a strict-inequality gap). `slabIndexForZ` is half-open `[lo,hi)` — a
mild inconsistency, but the footprints overlapping means a simple `<`→`<=` change won't close the
holes. Consistent with: `FOOTPRINT_GROW`/seam-bloat (XY outward) is the only lever; `PRISM_ZEPS`
(Z growth) does nothing → the dropped surface is at the correct Z but its XY lands on the exact
coincident seam where the independent per-slab CSG is ill-conditioned.

**Open design question (for the fix):** the surface straddling a slab plane is a DEGENERATE
coincidence, not random rounding. Cleanest fixes to weigh: (a) snap surface vertices within ε of
a slab plane onto it + assign each triangle wholly to one slab (no straddle, no coincident-CSG),
(b) make adjacent slabs' clip coverage provably overlap at shared-slab-boundary seams (targeted
bloat only where neighbouring cells differ in slab index; watch non-manifold growth), or (c) cut
the source once by the slab planes into watertight caps and assign faces to slabs, instead of
independent per-slab prism intersections that can disagree at the boundary.
