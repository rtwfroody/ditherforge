# Dithering research log

Running notes for the cross-fixture dithering search. Newest entries on top
within each section.

## Goal

Find a dithering algorithm that scores well *across the board* on:

- drift_ΔE (Lab)
- wander_ΔE (Lab — chosen vs nearest-input palette; new metric)
- maxdircorr (no directional structure)
- p50_ΔE / p99_ΔE (per-cell quality)
- blockvar @ S=2,4 (low local error amplitude)

Bricks_benchy is the hardest fixture (Riemersma clumps; FS scanlines; dizzy
drifts). Other fixtures shouldn't suffer much.

User constraints:
- Acceptable cost ≤10× FS, up to 20× if results are excellent.
- No new user knobs (or at most a single knob that *is* the UI).
- No need for the GUI to track research modes — backend bench only.

## Baseline results (commit `bbcf445`-ish, current main)

bricks_benchy:
  riemersma:        drift 0.00, wander 32.35, p50 32.5, p99 62.0, bv2/4 0.194/0.037, mdc1/4 0.104/0.007
  floyd-steinberg:  drift 0.34, wander 36.46, p50 33.2, p99 63.5, bv2/4 0.056/0.011, mdc1/4 0.603/0.107
  dizzy-corrected:  drift 7.45, wander 43.41, ...
  dizzy:            drift 8.81, wander 44.58, ...

uniform_neutral_grey:
  riemersma:        drift 0.02, wander 24.61, p50 8.1, p99 86.8 — BAD wander
  floyd-steinberg:  drift 0.97, wander 14.76
  dizzy:            drift 1.91, wander 8.99 — best wander on uniforms

Headline insight: Riemersma's clumpy/oscillatory look on near-flat regions is
captured numerically by wander_ΔE on the uniform fixtures. On real-fixture
average it's actually *low* (because most cells genuinely pick nearest), but
when residual swings push it past the gap to the next palette, it picks
visibly-far entries.

## Plan

Phase 0: instrument. Add spatial autocorrelation of the wander signal
(captures clumping that mean-wander misses). Add a smooth-gradient and a
near-flat-with-faint-texture fixture.

Phase 1: candidate algorithms. Try in order of increasing complexity:
  1. Residual-clipped Riemersma — cap window accumulator magnitude so the
     palette pick must be one of the K nearest to input. Trades a tiny drift
     for big wander reduction.
  2. Riemersma + local-swap refinement post-pass — couple of cleanup passes
     that swap to closer palette where regional residual permits.
  3. Ostromoukhov-style variable-coefficient diffusion on the Riemersma
     tour — different feedback coefficients per input level.
  4. Blue-noise threshold mask (void-and-cluster) — deterministic per-cell
     thresholding, zero memory. Risk: single-color banding.
  5. Hybrid: Riemersma + ordered/blue-noise base — combine.

Phase 2: pick the winner. Wire in production. Drop research-only modes
from the menu.

## Log (newest first)

### Round 1 — K-nearest and residual-clipped

Tried: r-knearest-{2,3}, r-clip-{30,60}, r-knr-{2,3}-p2 (K-nearest + 2 refinement passes, λ=0.1).

Highlights:

- **r-knearest-3** on bricks_benchy: drift 0.00, wander 31.12 (down from 32.35 baseline), but blockvar S=2 jumped 0.194→0.359 and mdc1 doubled (0.104→0.160). The wander gain isn't worth the local-amplitude regression on textured inputs.
- **r-knearest-3** on uniform_neutral_grey: drift 1.42, wander 9.76 — basically matches dizzy's 1.91 / 8.99, much better than Riemersma's 0.02 / 24.61. So K-nearest works on flat regions but hurts texture.
- **r-clip-{30,60}** sacrifice too much drift on textured fixtures (bricks 14.79 / 9.80 ΔE drift) — clipping kills error diffusion on real images.
- **r-knr-*-p2** refinement collapses checkerboard to no-dither (drift 22.40, wander 0). Refinement objective is too greedy; reverts to nearest in any locally-balanced region. Drop the refinement approach as currently formulated.

Diagnosis of why Riemersma wanders on uniform_neutral_grey: nearest palette is ~21 RGB away (20.8 in Lab) but next-nearest is ~31 (≥ RiemersmaInputBiasRange of 30). α-bias drops to ~0.26 there, so the window can still push target far enough to pick a *very* far palette (white/black at ~220 RGB). p99 = 86.8 ΔE confirms — 1% of cells get the far-palette swing, and that's what reads as the "white/black approximating grey" artifact.

The fix shape: trade a small drift (<2 ΔE — invisible) for bounded wander. K-nearest at fixed K hurts texture; need *adaptive* K based on palette geometry.

### Round 10 — biasMax / biasRange tuning

Tried r-bias-100-50 and r-bias-100-80 (biasMax=1.0, range=50/80 vs default 0.85/30):

- bricks: 0.00 / 31.20 (vs baseline 32.35) — tiny wander gain.
- delorean: 0.39 / 6.26 (vs 0.35 / 7.79) — wander win, drift cost trivial.
- uniform_neutral_grey: 0.06 / 24.24 (vs 0.02 / 24.61) — essentially unchanged. The hard case is unfixed by α tuning.
- uniform_terracotta: 2.14 / 18.60 (unchanged; nearDist > range).
- faint_texture_grey: drift 0.00, wander 13.82 — wander unchanged but **wclump shot up** (0.071 → 0.301). Strong α makes the wander more spatially clustered.

Conclusion: tuning α gives marginal improvements on textured fixtures and nothing on uniform_grey (the original complaint case).

### Round 9 — texture-aware (variance gating)

Restrict K to flatK in low-input-variance regions; full palette in textured regions.

r-tex-3-10 (flatK=3, threshold=10):
- bricks: matches Riemersma plus marginal mdc1 win.
- uniform_neutral_grey: drift 1.42 / wander 9.76 (great).
- gradient_warm: drift 2.58 / **wander 14.16** (vs baseline 25.54) but mdc1 0.273 (vs 0.123) — gradient triggers flat detection because local 16-cell windows have small variance, so K=3 kicks in and creates directional structure.
- faint_texture_grey: drift 2.81 / wander 7.29 (good).

Variance gating can't separate "smooth gradient" from "uniform color" — both are locally flat. Texture-aware fails on gradients.

### Round 8 — post-clamp wander

Run Riemersma, then replace any cell whose chosen-vs-nearest-input ΔE exceeds budget with the nearest-input palette.

All budget settings degrade: low budget reverts to no-dither (drift = quantization), high budget doesn't trigger. There's no useful middle.

### Rounds 5-7 — leak / wander-penalty / dynamic-K

- **leak (per-step window decay)** trades wander for drift. leak=0.05 hurts bricks badly (drift 10.00); higher leak loses error diffusion. Bricks regression makes leak unsuitable as a global default.
- **wander-penalty** (β·dist²(p, p_nearest) added to score) stays close to baseline even at β=10 — penalty insufficient against large window swings on uniform regions.
- **dynamic-K** (K = minK + floor(|residual|/step)) collapses to base Riemersma because residual reaches the threshold quickly in flat regions, so K expands to full and wander stays high.

### Rounds 2-4 — K-nearest variants

- **r-mk3-r2.0** (minK=3, ratio=2.0) is the most promising uniform/bricks improvement:
  - bricks: 0.00 / 31.14 (vs 32.35) — strict win on wander+wclump+mdc1
  - uniform_neutral_grey: 1.42 / 9.76 (vs 0.02 / 24.61) — DRAMATIC win
  - golden_pheasant: 0.00 / 31.24 (vs 30.60) — minor regression
  - gradient_warm: 2.25 / 15.64 (vs 0.02 / 25.54) — wander win, drift cost
  - **delorean: drift 0.46 / wander 6.43 / mdc1 0.431 (vs 0.100)** — mdc1 4× worse
  - **earth: drift 2.28 / wander 25.57 / mdc1 0.585 (vs 0.024)** — mdc1 24× worse, blockvar 2.7× worse
  - uniform_terracotta: drift **18.03** (vs 2.14) — palette doesn't bracket within K=3.

The earth and delorean blockvar/mdc1 regressions are real visual artifacts: K=3 forces "wrong" palette picks repeatedly in sequences, producing directional stripes.

## Conclusion

**No algorithm I tested strictly improves base Riemersma.** Every improvement on uniform-region wander comes with a regression in directional structure (mdc1) or blockvar amplitude on real-image fixtures.

The fundamental issue: with 4 palettes spanning a wide gamut, the bracketing pair for a flat-grey or flat-terracotta input may be (nearest, far_palette). Riemersma trades wander for zero drift; algorithms that cap wander pay drift on those palette geometries.

**Best candidates ordered by improvement-to-regression ratio:**

1. **base Riemersma** — robust, hard to beat strictly. Loses on uniform wander but those occasional far-palette picks are how it preserves chroma.
2. **r-bias-100-80** (biasMax=1.0, range=80) — marginal improvement everywhere, no regressions on real fixtures. Modest wander gains on textured fixtures (~10%), no help on uniform_grey.
3. **r-mk3-r2.0** — best on bricks/golden_pheasant/uniforms, but introduces directional structure on diverse-color real fixtures (delorean, earth). Not safe as default.

### Recommendation

Keep Riemersma as the default. The user's "white/black approximating grey" complaint is partly a palette-geometry symptom: when the Panchroma palette has only one near-grey entry, achieving zero drift forces using black/white. Mitigations available:

- **More palette colors** — moving from 4 to 6 colors would likely include a better-bracketing near-grey, reducing wander on grey naturally.
- **Optional snap-to-nearest pass** as a user-toggleable post-processing step (trades drift for wander).
- **Single user knob** trading drift vs wander: expose r-mk3-r2.0 as opt-in, with default Riemersma.

If a single-knob is desired: `--wander-cap N` where N=0 is base Riemersma (zero drift), N=∞ is snap-to-nearest. Implementation: r-mk3-r2.0 is the natural cap-2.0 variant; ratio is the knob.

### Round 13-15 — Blue-noise simplex variants

Implemented and tested:

- **bn-pair**: best K=2 pair per cell + LDS-driven choice. NO error feedback.
  - **Wins**: uniform_neutral_grey wander 9.01 (vs 24.61 base), faint_texture_grey 7.32 (vs 13.83), gradient_warm 17.29 (vs 25.54).
  - **Loses**: bricks drift 4.74, earth drift 8.47 (palette pair doesn't bracket all inputs).

- **bn-tri**: best K=3 triangle + LDS. Smaller drift than bn-pair (more bracketing options) but wider simplex → wander grows on textured fixtures.
  - delorean wander 22.09 (vs 7.79 base, 5.54 bn-pair) — big regression because triangle picks 3rd palette unnecessarily.
  - bricks 2.23/34.00 — strict but small improvements on bricks.

- **bn-simplex** (full K=4): drift basically 0 (barycentric over full palette), but wander ~ Riemersma. Doesn't help uniforms.

- **bn-adapt-{tol}**: pick smallest-K simplex with projection error ≤ tol, fall through K=1→2→3→4. Gives bn-pair behavior on well-bracketed inputs, escalates K only when needed.

- **bn-pair-d, bn-tri-d**: bn-pair/bn-tri with Riemersma-style sliding-window diffusion of projection error. Drift drops to ~0 BUT diffusion residual builds up in flat regions, forcing the algorithm to pick wider pairs → wander regression on uniforms (grey 25.04 vs bn-pair's 9.01). The diffusion **negates** the bn-pair wander gains on uniforms.

### Best candidate: bn-adapt-20

(tol = 20 RGB units between input and projection plane.)

| Fixture | Riemersma drift/wander | bn-adapt-20 drift/wander | Δ |
|---|---|---|---|
| bricks | 0.00 / 32.35 | 2.69 / 35.50 | drift +2.7, wander +3 (slight regression) |
| delorean | 0.35 / 7.79 | 3.18 / 5.20 | drift +2.8, **wander −33%** |
| earth | 0.00 / 26.62 | 5.70 / 22.90 | drift +5.7 (visible), wander −14% |
| golden_pheasant | 0.00 / 30.60 | 2.01 / 37.32 | drift +2, wander +22% (regression) |
| uniform_terracotta | 2.14 / 18.60 | 2.14 / 18.60 | identical |
| **uniform_neutral_grey** | 0.02 / 24.61 | 1.98 / 8.79 | drift +2, **wander −64%** |
| uniform_saturated_magenta | 66.90 / 50.09 | 91.07 / 67.36 | worse (out-of-gamut barycentric) |
| checkerboard | 6.06 / 21.89 | 4.36 / 23.85 | drift −28%, wander +9% |
| gradient_warm | 0.02 / 25.54 | 2.84 / 16.05 | drift +2.8, **wander −37%** |
| faint_texture_grey | 0.00 / 13.83 | 2.81 / 7.28 | drift +2.8, **wander −47%** |

**Trade-off summary**: ~2-5 ΔE drift cost on textured fixtures in exchange for 33-64% wander reduction on uniforms/gradients/faint-texture. The drift is visible on careful inspection but borderline. Out-of-gamut inputs (magenta) get worse — this is the K=4 fallback's weakness. earth's drift of 5.7 ΔE is the most concerning; bricks regresses on both metrics slightly.

The fundamental difference from Riemersma: bn-adapt is **memoryless ordered dither** (LDS threshold, no sliding window), so it can't redistribute drift. Each cell's choice is independent. Wander is bounded by the chosen simplex's diameter; drift accumulates per-cell.

### Next: DBS

The lit search's gold-standard recommendation is Direct Binary Search: iterative pixel-flip minimizing HVS-filtered Lab squared error. Should subsume drift+wander+structure as one objective.

### Round 16-17 — Direct Binary Search

Implemented and tested DBS with a 1-hop and 2-hop neighbor filter, initialized from Riemersma or bn-adapt-20.

DBS-8 (1-hop, 8 sweeps, init from Riemersma):
- **Big wander wins**: grey 24.61→6.13, delorean 7.79→3.49, magenta 50.09→18.76, faint_texture 13.83→7.38.
- **Big bv2 wins**: bricks 0.194→0.079, earth 0.242→0.135 — much smoother local error.
- **mdc1 disaster**: bricks 0.104→0.477, earth 0.024→0.281, golden_pheasant 0.015→0.285 — DBS converges to patterns with strong directional structure that the local 1-hop filter sees as low-error.

DBS-2hop-8: wider filter reduces mdc1 somewhat (bricks 0.477→0.325) but loses wander gains (grey 6.13→10.37). Net: worse trade-off than 1-hop DBS.

DBS-bn20-8: starting DBS from bn-adapt-20 instead of Riemersma. Similar to DBS-2hop, with marginally better magenta wander (61% vs Riemersma).

**DBS conclusion**: the wander/blockvar gains are real but mdc1 stripes make the output visually unacceptable on real-image fixtures. The fundamental issue is that DBS optimizes a filtered-error metric — the filter shape determines what "error" means, and on a non-2D-grid voxel surface our local neighborhood filters end up rewarding patterns with directional structure. Would need a much wider perceptually-uniform filter (5+ hops) to fix, but that gets expensive and may not converge cleanly.

## Final summary

After ~17 rounds across two research sessions, the Pareto frontier looks like:

| Algorithm | Drift on textured | Wander on uniforms | mdc1 (struct) | Notes |
|---|---|---|---|---|
| Riemersma (baseline) | 0 | high (24-30) | low | best on real fixtures |
| bn-tri | 0.4-4 | 9-37 | low-medium | delorean regression on wander |
| **bn-adapt-20** | 2-6 | 7-9 (uniforms) | low-medium | best balance — **recommended candidate** |
| DBS-8 | 3-6 | 6-7 (uniforms) | **bad** (0.3-0.5) | wander champion but mdc1 stripes |

**The fundamental constraint**: with a small palette (4 colors), the trade-off between drift, wander, and structure is genuinely a Pareto frontier — no algorithm dominates on all three.

**Recommendation**: ship `bn-adapt-20` (or expose it as a one-knob alternative). The user's "white/black on grey" complaint maps directly to Riemersma's high uniform-region wander; bn-adapt-20 reduces uniform_neutral_grey wander by 64% (24.61→8.79) at a 2-3 ΔE drift cost on textured fixtures (visible on careful inspection but borderline). It avoids DBS's mdc1 disaster.

If the drift cost matters: keep Riemersma. There's no clean strict-improvement available with these palette/fixture configurations.

### Round 11 plan — fundamentally different approaches

After lit search:

- **(A) Lab-space Riemersma** (Damera-Venkata & Evans, IEEE TIP 2003 + Haneishi 1996): drop-in replacement of RGB distances with Lab. Hypothesis: chrominance gets weighted differently and the white/black-on-grey case may shift balance toward near-grey palette since white is "only" 46 ΔE from grey in Lab vs 220 RGB. Cheap to implement.

- **(B) Direct Binary Search (Agar & Allebach, IEEE TIP 2005)**: iterative pixel-flip minimizing HVS-filtered Lab squared error. Each sweep is O(N·K·M) where M is filter footprint. For 500K cells, K=4, M~16 → 32M ops/sweep, ~5-10 sweeps → seconds. Subsumes wander/drift/structure as one principled objective.

- **(C) Blue-noise threshold mask with simplex barycentric** (Ulichney + folklore): for each cell, compute barycentric coords w.r.t. nearest K palette entries; threshold against per-cell blue-noise value. Zero error-diffusion memory; deterministic; bounded wander; drift = per-cell rounding.

- **(D) 3D blue noise on the voxel surface** (Brunton/Fraunhofer, TOG 2022 "Shape dithering"): generate a 3D blue-noise mask in cell-space; use it as the threshold for color choice.

Try in order: (A), (C) (simplest), (B) (most promising), and (D) if needed.

- **Adaptive-K Riemersma**: per-cell K = count of palettes within `ratio × nearest_dist` of input. If only one palette is "close," snap (K=1). If several bracket, mix among them. Try ratio ∈ {1.5, 2.0, 2.5}.
- **Bounded-wander Riemersma**: per-cell K = palettes within `nearest_dist + budget` (additive instead of relative). budget ∈ {15, 30}.
- Also: re-test plain Riemersma with biasMax 1.0 (force snap when α active) to see how close that gets us.
