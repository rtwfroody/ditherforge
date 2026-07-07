# Filament translucency (TD): why the current compensation falls short, and the corrected model

Status: step 2 of "Suggested order of work" implemented (2026-07-06). The
N-crossing + infill-terminator effective-color model ships as an opt-in
alternative selected by the settings `tdModel="layered"` (with `infillColor`).
The shell thickness the model integrates the infill leak over is **not** a
setting: it is derived from the selected printer's process profile
(`wall_loops` × wall line widths, via `export3mf.ShellThicknessMM`) — the same
profile ditherforge embeds into the exported 3MF, so it is the ground truth for
the print's actual walls. `voxel.EffectivePalette` builds the effective palette and
the dither quantizes/diffuses against it with plain area weighting. The legacy
area-compensation model (`tdModel=""`/`"area"`) remains the default pending the
two-swatch calibration/validation of steps 3-4. Steps 1, 3, and 4 are not yet
done.

## Current implementation

`internal/voxel/color.go` (`AlphaFromTD`, `PaletteAlphas`, opacity-weighted diffusion in
`DitherWithNeighbors` and the other kernels):

- Each palette color gets a scalar opacity `α = 1 − 10^(−t/TD)` with a single global
  `NominalOpticalThicknessMM = 1.0`.
- The nearest-color decision still compares against the **nominal** filament color in Lab.
- Error diffusion conserves the opacity-weighted area average `Σ(aᵢαₖcₖ)/Σ(aᵢαₖ)` instead of
  the plain area average, so a translucent filament counts less per unit area and the solver
  spends more area on it.

In practice this doesn't compensate nearly enough: translucent colors come out washed out.

## The key observation

The viewer is almost never looking edge-on along the XY plane. At any realistic viewing
angle, a light ray entering the outer extrusion travels only a fraction of a millimeter
through the translucent filament before crossing into the layer **above/below** — i.e. into
a *different dither cell*. So the "backing" of a translucent cell is, recursively, more
dithered surface — until the ray runs out of shell and hits infill.

We cannot control the backing: the slicer only honors surface colors, and the only global
knobs are (a) which filament prints the infill and (b) shell/wall thickness.

### The current model is the infinite-recursion fixed point of exactly this picture

If perceived color satisfies `C = Σ fₖ [αₖCₖ + (1−αₖ)C]` (each first-hit cell contributes its
own color at opacity αₖ, and passes the rest to more dithered surface), the fixed point is

```
C = Σ fₖαₖCₖ / Σ fₖαₖ
```

— exactly the quantity the renormalized diffusion conserves. So the model is structurally
right; it under-compensates for two quantitative reasons:

### (a) The recursion terminates in the infill — and that term is missing entirely

The shell is finite. Looking at a vertical wall from ~45° elevation, each layer crossing
advances ~one layer height inward, so a 2-wall shell (~0.84 mm) gives roughly **N ≈ 4**
dither-cell crossings before the ray is in infill. Per-crossing path ≈ layer_height/cos45°
≈ 0.28 mm at 0.2 mm layers.

- Translucent filament, TD = 4: per-crossing transmission `10^(−0.28/4) ≈ 0.85`; after 4
  crossings **~52% of the light reaches the infill** and comes back infill-colored.
- Opaque filament, TD = 0.8: per-crossing transmission ≈ 0.45; after 4 crossings ~4%.

So in translucent-heavy regions roughly *half* the perceived color is the infill filament,
while the current model attributes that light to the palette average. No amount of area
renormalization can fix a term that isn't in the objective. This is likely the dominant
cause of "doesn't compensate nearly enough."

### (b) The per-crossing optical thickness is ~3× overstated

`NominalOpticalThicknessMM = 1.0`, but the real per-crossing path in the angled-view regime
is ~0.2–0.4 mm. Because `α = 1 − 10^(−t/TD)` compresses toward 1 as t grows, overstating t
understates the *ratio* between opaque and translucent alphas — the thing that sets
compensation strength. Example, TD 0.8 vs TD 6:

| t (mm) | α_opaque | α_translucent | ratio |
|--------|----------|---------------|-------|
| 1.0    | 0.944    | 0.319         | 3.0   |
| 0.35   | 0.634    | 0.126         | 5.0   |
| 0.2    | 0.438    | 0.074         | 5.9   |

Compensation is currently about half of what it should be, even before the infill term.

### (c) Smaller omissions (refinements, not the fix)

- **Exit re-tint**: light reflected from deeper layers passes back out through the
  translucent layers and gets tinted a second time (Beer–Lambert single-pass ignores this).
- **Scattering**: filament is a scattering medium, not tinted glass. A thin translucent
  layer over dark backing reads lighter than transmission math predicts (back-scatter), and
  colors over white read darker (double-pass absorption). Kubelka–Munk two-flux is the
  standard model (cf. Babaei et al. "Color Contoning", SIGGRAPH 2017; Elek/Sumin et al.
  scattering-aware 3D-print compensation, 2017/2019).
- **Lateral subsurface scattering**: a ~0.5–1 mm PSF blurs the dither pattern; the
  literature fix is sharpening/deconvolving the target before dithering. Independent of the
  compositing math.

## Why HueForge doesn't have this problem

HueForge outputs an STL/3MF plus filament-swap-at-layer instructions, not gcode. Its
geometry *is* the optical stack: a flat heightmap plate, every layer 100% solid coverage,
one filament per layer for the whole plate. The slicer is only trusted to print solid
layers and pause for swaps, so HueForge knows and chooses the entire per-pixel stack by
construction. The trick does not transfer to arbitrary 3D multi-color surfaces.

## Corrected model

Keep the fixed-point structure; add the terminator; fix the geometry:

1. **Per-crossing transmission** `Tₖ = 10^(−ℓ/TDₖ)` with `ℓ ≈ layer_height / cos(45°)`
   derived from layer height and a representative viewing angle, replacing the 1.0 mm
   global. (View angle is unknowable; 45° is the defensible statistical pick — straight-on
   is the easy case, grazing is hopeless anyway.)
2. **Number of crossings** `N ≈ shell_thickness / layer_height` (both known or exposable).
3. **Effective color = N-step recursion terminated at infill**: each step contributes
   `αₖCₖ` of the remaining light through the local dither mixture; whatever survives N
   steps returns `C_infill`. The infill filament becomes an explicit input to the color
   model — which it physically is.
4. **Quantize and diffuse against these effective colors with plain area weighting.** The
   composite already accounts for the loss, so the renormalization machinery is subsumed,
   and the residual carried by error diffusion is the true residual (today the error is
   computed against nominal colors, so unreachable saturation is silently dropped).

Practical corollary for the one backing knob we do have: **opaque white infill** is the
"paper" of the print and maximizes chroma headroom. Infill color should probably become a
setting once it's part of the color model.

## Calibration instead of trusting TD

The chain (published TD → α → recursion depth) compounds too many assumptions. Calibrate:
print a swatch plate with a full-coverage patch of each filament as a normal walled part,
once over white infill and once over black infill; photograph/measure both. Per filament,
the two measurements let you fit an effective per-crossing α (and an intrinsic
reflectance). For opaque filaments the two swatches match; for translucent ones the
over-white vs over-black gap directly measures the infill-leakage term and validates the
N-crossing model. Fitted alphas then replace the `AlphaFromTD` guess.

## Suggested order of work

1. **Cheap experiment first**: set `NominalOpticalThicknessMM` ≈ 0.3 and print something
   translucent-heavy. This isolates how much of the shortfall is (b) alone; the remaining
   gap is the infill term (a).
2. Implement the N-crossing + infill-terminator effective-color model (quantize + diffuse
   against effective colors, plain area weighting).
3. Two-swatch calibration prints; replace TD-derived alphas with fitted ones.
4. Refinements if still needed: Kubelka–Munk two-flux, target sharpening for lateral
   scattering.

## Rejected / non-options

- **Controlling the backing geometry** (extending each cell's filament inward to opaque
  depth): not possible — the slicer ignores interior colors; only surface colors plus the
  two global knobs (infill filament, shell thickness) exist.
- **Pure area compensation** (the current approach) has a hard ceiling at 100% coverage and
  mis-attributes the transmitted light; it cannot be fixed by tuning alone, though tuning
  (item b) recovers roughly half the missing compensation.
