# swatchphoto

Recover **in-context filament colors** and a **neighbor-bleed path length ℓ**
from a top-down photo of the printed DitherForge swatch plates plus the
`.swatch.json` manifest emitted next to the exported 3MF.

Each of the six calibration plates (one per unordered pair of a four-filament
palette) is a 90×10×2 mm bar with nine 10 mm sections at B-fraction
`p = 0, 1/8, … , 1`, the A/B mix rendered as a fine Bayer speckle
(`internal/swatch/swatch.go`). This tool:

1. **Normalizes lighting** — decodes sRGB→linear and divides out a smooth
   paper-illumination map (correcting color cast *and* a lighting/vignette
   gradient). The map is a per-channel low-order polynomial fit *robustly* (IRLS
   with outlier rejection), so it follows the paper and rejects the plates — a
   plate **brighter than the paper** (Cold White often is) neither contaminates
   the estimate nor gets clamped: it simply normalizes to a linear value **above
   1.0**, which is carried unclamped through sampling and the fit (only the
   *display* hex saturates, and the JSON flags which channels clipped).
2. **Detects & rectifies plates** — segments non-paper regions by deviation from
   white, fits rotated rectangles (the 9:1 aspect is unambiguous), and warps each
   to a canonical bar.
3. **Identifies & orients** — clusters the twelve endpoint patches into four
   filaments and matches them to the manifest palette by *robust z-scored Lab
   structure* (not absolute hex distance — the measured colors are expected to
   differ from nominal); the endpoint labels resolve each plate's pair and its
   180° flip.
4. **Fits** — takes each filament's pure endpoints across the plates it appears
   on to a **corrected hex** — the per-channel **median** (robust to a single
   glossy plate; the per-plate endpoint hexes and the spread are all reported so
   disagreement is visible, not smeared) — then fits **ℓ per pair and a robust
   global ℓ** by forward-simulating the pipeline's own neighbor-blend model
   (`voxel.EffectiveCellColors`: β = 10^(−ℓ/TD), two Jacobi passes, 26-connected
   area-weighted blend in linear light — ported faithfully) on the known Bayer
   pattern at each section's **realized** coverage and least-squares matching the
   measured section colors. The global ℓ rejects per-pair outliers (MAD) and
   reports which pairs it excluded and how many it used.

Outputs a JSON (corrected hexes + linear + clipped-channel flags + per-plate
endpoints + spreads, per-pair & robust global ℓ + residuals, per-plate
per-section measured colors) and a `ramp_fits.png` of measured vs modeled curves,
into the output directory.

### Model limitation (bright + bright pairs)

The pipeline's blend is **additive** in linear RGB (a cell moves toward the
area-weighted mean of its neighbors). Real printed speckle mixes more
**subtractively**: a 50/50 mix of two *bright* filaments (e.g. Cold White +
Orange) photographs *darker* than the additive model can ever produce, so those
pairs' per-pair ℓ rails to a bound and their residual stays high. Pairs with a
**dark** filament (Black + anything) fit well, because the translucent partner
bleeding toward black *is* a darkening the additive model captures. The robust
global ℓ therefore leans on the well-constrained pairs and excludes the railing
ones — surfaced in `global_ell.excluded_pairs`. This is a property of the model
being fit, not a photo defect.

## Invocation

Runs via [uv](https://docs.astral.sh/uv/) with PEP 723 inline dependencies
(numpy, opencv-python-headless, scipy, matplotlib) — no venv setup needed:

```sh
# analyze a real photo
uv run tools/swatchphoto/swatchphoto.py PHOTO.jpg MANIFEST.swatch.json [--out DIR] [--anisotropic]

# synthetic round-trip self-test (no real photo needed — build/verify the chain)
uv run tools/swatchphoto/swatchphoto.py --selftest [--anisotropic] [--ell 0.35] [--seed 0]
```

`--anisotropic` fits separate `ℓx` (in-column, X) and `ℓz` (in-row, Z) path
lengths. Cells are ~0.53 mm wide but only ~0.08 mm tall (one print layer), so the
layer-tall rows were designed to probe vertical bleed the single-ℓ model can't
distinguish; the anisotropic mode collapses to the isotropic Go model when
`ℓx == ℓz`. Whether real data can actually separate `ℓx` from `ℓz` is the open
question the mode exists to explore.

## Self-test

`--selftest` renders synthetic "photos" of all six plates from known filament
colors through the *same* forward model at a known injected ℓ (random rotations
and positions on white paper, a lighting gradient, blur and sensor noise), then
runs the full analysis and asserts the plates are detected, identified and
oriented, the hexes and ℓ are recovered within tolerance. Set
`SWATCHPHOTO_DEBUG=1` to print per-component detection diagnostics to stderr.
