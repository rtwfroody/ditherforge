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
   to a canonical bar.
3. **Identifies & orients** — clusters the twelve endpoint patches into four
   filaments and matches them to the manifest palette by *robust z-scored Lab
   structure* (not absolute hex distance — the measured colors are expected to
   differ from nominal); the endpoint labels resolve each plate's pair and its
   180° flip.
4. **Fits (γ, ℓ)** — jointly across all six pairs, in manufacturer-anchored
   space, by forward-simulating the pipeline's own neighbor-blend model
   (`voxel.EffectiveCellColors`: β = 10^(−ℓ/TD), two Jacobi passes, 26-connected
   area-weighted blend — ported faithfully) run in the **power-law domain**
   `f(c)=c^γ` on the known Bayer pattern at each section's **realized** coverage.
   Reports the global `(γ, ℓ)`, the additive `γ=1` best-ℓ baseline for comparison,
   per-pair residuals under the global fit, and a per-pair ℓ refit at the global γ.

Outputs a JSON (global & per-pair fit, additive baseline, manufacturer hexes,
photo-absolute endpoints as a labeled diagnostic with per-plate values, per-plate
per-section measured colors) and a `ramp_fits.png` of anchored measured vs modeled
curves, into the output directory.

### Manufacturer-anchored fit (`--anchor mfg`, default)

Per plate, per channel, the measured linear section values are affine-mapped so
the plate's own measured endpoints (sections 0 and 8) land exactly on the
manufacturer palette hexes:

    x' = A_mfg + (x − mA) · (B_mfg − A_mfg) / (mB − mA)

This cancels white balance, flare, paper color and per-plate black gloss (each
plate's black maps to manufacturer black regardless of sheen), leaving only the
relative mixing shape. A channel whose endpoints barely differ (`|mB − mA|` below
a contrast threshold — e.g. Cold White + Orange in red) would divide by ~0, so it
is dropped from that plate's residual. `--anchor photo` restores the older
photo-absolute behavior (trusting the phone's colors).

### Power-law mixing domain (γ)

Spatial averaging of the fine speckle by camera/eye is additive in linear light,
so the **section average stays linear**. The subtractive behavior lives in the
per-cell effective colors: the neighbor Jacobi blend (same β, same self-ref-C0)
runs in `f(c)=c^γ`, then `f⁻¹` per cell, then cells are averaged linearly.
`γ=1` is the exact additive model (regression-guarded bit-for-bit); `γ→0`
approaches geometric / optical-density mixing; `γ<0` (allowed in the extended
fit) biases a mix toward its darker member. `c` is floored at `1e-4` before `f`
so `γ≤0` stays finite.

### Model adequacy (open question)

On the real print the subtractive domain **helps but does not fully fit**: the
global γ rails to the lower bound (the data want the most-subtractive end; the
extended fit lands at γ<0), the residual improves ~25 % over additive, and all
six pairs now participate — but the two bright + bright pairs (Cold White +
Orange especially) still misfit, and the per-pair ℓ refit at the global γ spreads
widely. A single global `(γ, ℓ)` is therefore an improvement, not the final word;
the residual pattern points at a per-pair or TD-dependent γ.

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
colors through the *same* forward model at a known injected `(γ, ℓ)`, applies a
per-plate per-channel affine distortion (WB/gloss) that anchoring must invert,
plus random pose on off-white paper (with a Cold White plate **brighter** than
the paper), a lighting gradient, blur and sensor noise. It then asserts: the
plates are detected/identified/oriented, `γ` and `ℓ` are recovered within
tolerance, the subtractive residual beats the additive baseline, a
brighter-than-paper value survives unclamped, and — as a regression guard — the
`γ=1` path reproduces the additive forward model bit-for-bit. Set
`SWATCHPHOTO_DEBUG=1` to print per-component detection diagnostics to stderr.
