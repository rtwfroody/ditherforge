# ditherbench

Informational benchmark for dither algorithm quality. Loads the
checked-in PNG fixtures under `tests/testdata/color/` plus two
synthetic uniform-color fixtures, runs each dither mode, and prints
a table of metrics per (fixture, mode).

There is no pass/fail and no Go-test integration. The tool exists to
support algorithm experimentation — comparing scrambling permutation
subsets, tuning neighbor weights, evaluating a new mode against the
existing baselines.

## Run

From the repo root:

```sh
go run ./tests/ditherbench                 # all fixtures, all modes
go run ./tests/ditherbench --fixture uniform   # synthetic only
go run ./tests/ditherbench --mode floyd     # one mode (substring)
go run ./tests/ditherbench --out /tmp/dith  # also dump output PNGs
```

## Metrics

Per (fixture, mode) row:

- **drift_ΔE** — global Lab ΔE between avg(input) and avg(output).
  Captures the chroma bias the dither failed to correct. FS-class
  scores ~0.3, dizzy ~7-8, no-dither up to ~15. Lower is better.

- **p50_ΔE / p99_ΔE** — per-cell ΔE between cell color and its
  assigned palette color. Median and 99th percentile of the local
  quantization error. Bounded by ~half the palette quantization
  step regardless of mode; dither pays a small per-cell cost to buy
  global accuracy. Comparable across modes; not the headline metric.

- **blockvar(S=2,4,8,16,32)** — variance of the per-S×S-block mean
  error vector (with the global mean subtracted first), normalized
  by per-pixel error variance. White noise decays as 1/S²; blue
  noise decays faster; banding at scale B keeps the ratio close to
  1 at S=B. The shape across scales tells you where isotropic
  structure lives. Square-block averaging is direction-blind, so
  this metric misses diagonal-stripe patterns — see `maxdircorr`
  for the directional companion.

- **maxdircorr(d=1, d=4)** — max |autocorrelation| of the error
  vector field (mean-subtracted) over 6 directions: (d,0), (0,d),
  (d,d), (d,-d), (d,2d), (2d,d). 0 = uncorrelated (good blue-noise
  signature), 1 = perfectly correlated (banding at that distance
  and direction). Catches the failure mode `blockvar` misses:
  Floyd-Steinberg's diagonal scanline structure is invisible to
  square-block averaging but obvious to directional autocorrelation.
  d=1 catches local stripes; d=4 catches Bayer-like periodicity.
  The (1,2)/(2,1) directions cover ~26.5° / ~63.5° lattice angles
  that pure axis+diagonal sampling misses.

Both spatial-structure metrics subtract the global mean error
vector before measuring, so a high-drift mode doesn't get a
"bandy" penalty just because |μ|² leaks into the variance/
correlation. Drift is reported separately by `drift_ΔE`.

## Synthetic fixtures

Three 512×512 single-color images:

- `uniform_terracotta` (#B37D67) — between palette entries; cleanest
  test of dither structure on a uniform region.
- `uniform_neutral_grey` (#808080) — same idea, neutral hue.
- `uniform_saturated_magenta` (#FF00FF) — out-of-gamut input
  no Panchroma 4-color palette can reach. Catches the
  "candidate dithers nicely toward the wrong color" failure: a
  buggy algorithm that breaks energy conservation will look
  blue-noise here while drift_ΔE blows up.

These have no input texture to hide patterns behind, so any
structure in the output is dither artifact. Compare the
`maxdircorr` column across modes here to evaluate whether a
candidate algorithm produces blue-noise output or directional
banding.

## Reading the table

A "good" dither mode should achieve:

- `drift_ΔE` near 0 (energy-conserving)
- `p50_ΔE` close to the no-dither baseline (tight palette match
  for typical cells; dither shouldn't ruin per-cell accuracy)
- `blockvar` decaying faster than 1/S² (high-frequency error
  cancels in larger blocks — blue-noise property)
- `maxdircorr` near 0 (no directional structure)

Today nothing achieves all four simultaneously. FS gets the first
three; dizzy gets the fourth. The motivating use case for this
tool is evaluating Z-order-with-Owen-scrambling variants that
might get all four.

## Output PNGs

With `--out DIR`, writes one PNG per (fixture, mode) showing the
final palette assignments. Useful for visual inspection on the
synthetic uniform fixtures, where any structure in the output is
unambiguously dither artifact.
