# swatchphoto

Recover a **mixing law (γ, ℓ)** — and, as a diagnostic, in-context filament
colors — from a top-down photo of the printed DitherForge swatch plates plus the
`.swatch.json` manifest emitted next to the exported 3MF.

A phone JPEG's *absolute* colors are not trusted (auto white balance, HDR tone
mapping, a paper-as-white assumption). Its *relative curve shape* is. So by
default the tool anchors each plate to the **manufacturer** endpoint hexes and
fits only the mixing shape (see "Manufacturer-anchored fit" below).

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
   to a canonical bar. Closely spaced plates whose shared shadow bridges them
   into one blob are recovered by a parallel-bar split: rotate the blob upright
   and cut at valleys in the per-column occupancy profile (a bridge dips below
   ~50% of the bar length; in-plate speckle never drops below ~70%).
3. **Identifies & orients** — clusters the twelve endpoint patches into four
   filaments and matches them to the manifest palette by *robust z-scored Lab
   structure* (not absolute hex distance — the measured colors are expected to
   differ from nominal); the endpoint labels resolve each plate's pair and its
   180° flip.
4. **Fits (γ, ℓ)** — jointly across all six pairs, in manufacturer-anchored
   space, by forward-simulating the pipeline's own neighbor-blend model
   (`voxel.EffectiveCellColors`: β = 10^(−ℓ/TD), two Jacobi passes, 26-connected
   area-weighted blend — ported faithfully) on the known Bayer pattern at each
   section's **realized** coverage. Several **candidate mixing models** (below)
   are fit and compared by leave-one-pair-out cross-validation; the winning
   model, its parameters, per-pair residuals and the additive baseline are all
   reported.

Outputs a JSON (candidate comparison + winner, manufacturer hexes, photo-absolute
endpoints as a labeled diagnostic with per-plate values, per-plate per-section
measured colors) and a `ramp_fits.png` of the anchored measured vs the winning
model's curves, into the output directory.

### Manufacturer-anchored fit (`--anchor mfg`, default)

Per plate, per channel, the measured linear section values are affine-mapped so
the plate's own measured endpoints (sections 0 and 8) land exactly on the
manufacturer palette hexes:

    x' = A_mfg + (x − mA) · (B_mfg − A_mfg) / (mB − mA)

This cancels white balance, flare, paper color and per-plate black gloss (each
plate's black maps to manufacturer black regardless of sheen), leaving only the
relative mixing shape. A channel whose endpoints barely differ (`|mB − mA|` below
a contrast threshold) would divide by ~0, so it is dropped from that plate's
residual. `--anchor photo` restores the older photo-absolute behavior.

### Candidate mixing models

All are **local per-cell rules** on the neighbor graph with **per-filament**
parameters only (no per-pair free parameters), so the winner ports to
`voxel.EffectiveCellColors`. Section averaging always stays linear; only the
per-cell blend changes:

| model | params | idea |
|---|---|---|
| `additive` | ℓ | the current Go model (β=10^(−ℓ/TD)). Regression baseline. |
| `global_gamma` | ℓ, γ | power-mean domain `f(c)=c^γ`, uniform γ. |
| `td_gamma` | ℓ, a, b | per-cell `γ_i = clamp(a + b·log10(TD_i))`. |
| `transmittance` | ℓ, κ | neighbor light filtered by the cell's own hue `T_i=(C0_i/max)^κ`, applied to the neighbor's *deviation* from C0 (κ=0 → additive). |
| `transmittance_shadow` | ℓ, κ, s | + interface micro-shadowing `×(1−s·u_i)`. |
| `td_gamma_shadow` | ℓ, a, b, s | + micro-shadowing on `td_gamma`. |

The **transmittance** filter deliberately acts on the neighbor's deviation from
the cell's own color (not the raw neighbor sum): a uniform patch then reads as its
own color, so the anchored endpoints stay on the manufacturer hexes — the literal
`(1−β)C + β·T∘nbAvg` form self-filters pure patches and its endpoint shift fights
the anchoring, pinning κ→0.

### Model selection & result

Candidates are ranked by **leave-one-pair-out cross-validation** (fit on 5 pairs,
evaluate the held-out one, all 6 rotations, mean held-out RMS — the primary
criterion on a 6-pair dataset), with a **parsimony rule**: among models within a
small LOO margin of the best, take the fewest parameters.

**2026-07 blue-noise recalibration** (Beige/Black/Brown/Cold White, Snapmaker
U1, one print each at 0.08 mm and 0.2 mm layer height):

| layer | winner | ℓ (mm) | κ | LOO RMS (additive / transmittance) |
|---|---|---|---|---|
| 0.08 mm | **additive** | 0.2053 | 0 | 0.044 / 0.044 |
| 0.20 mm | **additive** | 0.2075 | 0 | 0.053 / 0.065 |

**Winner at both heights: `additive` (κ = 0), with ℓ statistically identical
(≈0.206 mm)** — layer height does not measurably change the mixing behavior.
On the properly dithered plates the transmittance filter no longer earns its
parameter: at 0.2 mm it fits κ≈0.05 (i.e. additive) and *loses* LOO; at
0.08 mm it edges additive by less than the parsimony margin. The earlier fit
on Bayer-pattern swatches (**transmittance, ℓ≈0.13, κ≈3.0**, LOO 0.069 vs
additive 0.117) was to a large extent fitting the Bayer worm-banding artifacts,
not the filament optics; those numbers are superseded. The calibrated pairs
live in `palette.NeighborModelForLayer` (Go side), interpolated by layer
height.

## Invocation

Runs via [uv](https://docs.astral.sh/uv/) with PEP 723 inline dependencies
(numpy, opencv-python-headless, scipy, matplotlib) — no venv setup needed:

```sh
# analyze a real photo (manufacturer-anchored (γ, ℓ) fit by default)
uv run tools/swatchphoto/swatchphoto.py PHOTO.jpg MANIFEST.swatch.json [--out DIR] [--anchor mfg|photo] [--anisotropic]

# synthetic round-trip self-test (no real photo needed — build/verify the chain)
uv run tools/swatchphoto/swatchphoto.py --selftest [--anisotropic] [--ell 0.35] [--gamma 0.4] [--seed 0]
```

`--anisotropic` fits separate `ℓx` (in-column, X) and `ℓz` (in-row, Z) path
lengths at `γ=1` (additive). Cells are ~0.53 mm wide but only ~0.08 mm tall (one
print layer), so the layer-tall rows were designed to probe vertical bleed the
single-ℓ model can't distinguish; the anisotropic mode collapses to the isotropic
Go model when `ℓx == ℓz`. Whether real data can actually separate `ℓx` from `ℓz`
is the open question the mode exists to explore.

## Self-test

`--selftest` renders synthetic "photos" of all six plates from the *manufacturer*
colors through a *known* candidate model, applies a per-plate per-channel affine
distortion (WB/gloss) that anchoring must invert, plus random pose on off-white
paper (with a Cold White plate **brighter** than the paper), a lighting gradient,
blur and sensor noise. It runs two scenes and asserts, in each, that the plates
are detected/identified/oriented and the injected parameters are recovered:

- a `(γ, ℓ)` scene → `global_gamma` recovers `γ` and `ℓ`, and its residual beats
  the additive baseline; a brighter-than-paper value survives unclamped;
- a `(κ, ℓ)` scene → `transmittance` recovers `κ` and `ℓ` (this is why the
  synthetic generator is model-parameterized).

Plus a regression guard that the `γ=1` path reproduces the additive forward model
bit-for-bit against an independent reference. Set `SWATCHPHOTO_DEBUG=1` to print
per-component detection diagnostics to stderr.
