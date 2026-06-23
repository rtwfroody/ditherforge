# Investigation: white holes in flat surfaces (Nord Stage 4 model-thicker)

Status: **RESOLVED 2026-06-23.** Fixed in commit `1e352af`
("fix: close flat-top white holes via cell-gen↔clip slab-assignment alignment").
Top-down holes dropped 1.269% → 0.020%.

> Note: this file is a working investigation record kept in the checkout for
> convenience; it is not meant to be a durable repo artifact. Everything below
> reflects the final, instrument-confirmed conclusion. Earlier session-by-session
> theories that turned out to be **wrong** are listed under "Refuted hypotheses"
> so they are not re-chased — do not treat them as current.

## Symptom

Loading `~/Documents/3d_print/objects/Nord+Stage+4+-+88+High+detailed+5.3/model-thicker.json`
(the settings file sits next to the model; size 100, snapmaker_u1, layer 0.08,
locked Steel Grey #616469, `colorAwareCells: true`, alpha-wrap, split enabled)
and viewing the **positive-Z, top-down** Output Model: the flat horizontal bottom
face of the keyboard showed **white triangular/wedge/sliver gaps** in the middle
of the surface. Confirmed by the user to be a top-down view of a horizontal
surface (not vertical walls, not a grazing artifact).

## Root cause (confirmed)

A **cell-generation ↔ clip slab-assignment desync** — NOT a CSG/boolean defect.

- Cell generation assigns each surface triangle to a slab by its **true,
  un-quantized Z**: `SlabSurfaceFootprints` → `slabIndexForZ` on `model.Vertices`
  (`internal/cellslicer/slice.go`).
- The clip's per-slab split assigns the **1 µm-quantized** geometry:
  `buildSlabSrc` → `DedupVertsByPosition` (snap to `clipperScale = 1000`) →
  `SplitByPlane` (`internal/cellslicer/clip_manifold.go`).
- Slab planes are **off-grid**: `SlabBoundaryPlanesFirst` (slice.go) adds a sub-µm
  per-plane offset, and on a split half `zMin` is an arbitrary float. So at a plane
  `P = K + f` µm, any near-horizontal surface whose true Z lands in the
  `~|f − 0.5|` µm band where rounding crosses `P` gets its **cells in one slab**
  (true Z) and its **surface in the other** (quantized Z). The slab that owns the
  surface after the split has no cell there; the slab with the cells does not own
  the surface → the surface is clipped by **neither** → white hole.

### Evidence

- `DITHERFORGE_SLAB_COVER_PROBE` (`internal/cellslicer/clip_slab_cover_probe.go`):
  per slab, up-facing srcID cap area whose centroid lies inside **no** group
  contour of its own slab. On the real model **95–99.5 % of that uncovered area
  is covered by an *adjacent* slab's contour** — the desync signature (cells in
  the neighbour, surface here).
- `DITHERFORGE_CLIP_COVER_PROBE` (`internal/cellslicer/clip_cover_probe.go`):
  in-contour CSG drop = **0.000 %** — the intersection never drops surface that
  lies inside a cell's own contour.
- `internal/manifoldbool/onplane_repro_test.go`: `SplitByPlane` **conserves** both
  a flat top and a straddling (tent) top — surface area and `srcID` survive on the
  correct side (loss ≈ 0). This directly refutes the "quantization → coincident
  face → discarded cut cap" theory.

## The fix

In `sliceSampleHalf` (`internal/pipeline/run.go`), before slicing, snap a **clone**
of `geom.Vertices` to the same 1 µm grid the clip uses (`cellslicer.Snap` =
`Dequantize(Quantize(v))`). Both stages then bin identical Z and agree on slab
ownership. The clone keeps the shared cached geometry pristine; the clip snaps its
own copy to bit-identical positions. Covers split (`geom = so.Halves[h]`) and
non-split (`geom = lo.Model`) because cell-gen and clip share the same mesh in
both. Cache salt `snap-preslice-v1` (`hashVoxelizeSettings`,
`internal/pipeline/stepcache.go`) invalidates stale voxelize caches.

Snap-only (no vertex merge / degenerate-face drop) is sufficient: `Snap` collapses
same-bucket vertices to identical positions, so a face the clip would drop as
degenerate becomes zero-area in cell-gen too and contributes no footprint. The only
possible direction is "cell with no surface" (harmless), never "surface with no
cell" (a hole).

### Results

| view | before | after |
|---|---|---|
| top (flat-top holes) | 12637 px — 1.269 % | 202 px — 0.020 % (63×) |
| perspective | 4484 px — 0.462 % | 571 px — 0.059 % |
| bottom | 649 px — 0.065 % | 781 px — 0.078 % (noise) |

Measured with `--debug-stages-dir <dir>` (`top_holes.png` etc.); `go test ./...`
green.

### Why earlier fix attempts did nothing

`prism-grow` (growing a cell prism's Z extent) was a bit-identical no-op because
each cell's prism is `Intersection()`'d with **only its own slab's** pre-split
manifold (`ss.slabManifold(g.slabIdx)`, `clip_merge.go`). The leaked surface lives
in the **neighbour** slab's manifold, unreachable by any prism Z-extent.
Vertex-nudging was blunt (moved shared wall vertices, net-negative).

## Residual (second mechanism, not fixed)

~0.02 % of the top remains as a model-wide hairline at slab boundaries: a
near-horizontal triangle that **straddles** a boundary gets a thin sliver in each
slab (area conserved), but cell-gen's `triBandXYPath` aspect/thinness reject drops
the thin straddle band, so that slab gets no cell over its sliver. Snapping does not
help (snapped vertices still straddle). Below visibility. A future fix would align
the binning *rule* (split partition ↔ cell-gen band) or assign straddle slivers
deterministically to one slab in both stages. Related:
`project_vertical_wall_slivers`, `project_apollo_holes_root_cause`.

## Refuted hypotheses (do not re-chase)

All were investigated and disproved by the probes/tests above:

- **Inverted/flipped output faces** (session 2). Clip preserves winding to 0.024 %;
  the white reading was a measurement artifact.
- **Footprint under-coverage / partition buried-exclusion** (sessions 3–5). The
  `FOOTPRINT_GROW` oracle moved the number only because growing a prism reaches into
  the neighbour slab's surface — a *symptom* of the desync, not under-reach within
  the correct slab.
- **Inter-group prism seams** (session 6). In-contour CSG drop is 0.000 %.
- **1 µm quantization → coincident face discarded as a cut cap** (session 8). The
  `manifoldbool` repro shows the split keeps both flat and straddling tops with
  `srcID`; the face is not discarded by the boolean — it simply lands in a slab that
  has no cells.

## Diagnostic tooling (env-gated, zero cost when off)

- `DITHERFORGE_SLAB_COVER_PROBE` — per-slab uncovered cap + adjacent-slab desync %.
- `DITHERFORGE_CLIP_COVER_PROBE` — in-contour CSG drop (mechanism (b) test).
- `DITHERFORGE_HOLE_REPORT` — `CheckWatertight` boundary/non-manifold counts per
  stage (note: clip-output boundary edges are dominated by legitimate per-cell
  perimeter edges, so this is **not** a white-hole metric — use the cover probes or
  the `top_holes.png` overlay).
- `--debug-stages-dir <dir>` — output-mesh hole overlays (`*_holes.png`).
- `internal/manifoldbool/onplane_repro_test.go` — `SplitByPlane` conservation tests.

## Related context

Headless-repro blockers fixed earlier and unrelated to the root cause: voxelize
non-determinism (`pickMergeVictim` tie-breaks, `colorcut.go`) and the export
fraction-key mismatch.
