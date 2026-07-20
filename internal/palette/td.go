package palette

import (
	"math"

	colorful "github.com/lucasb-eyer/go-colorful"
)

// TDLeak returns the fraction of light that survives the printed shell and
// returns tinted by the infill filament, for a single palette entry with
// transmission distance td (mm). It is the scalar core of the "layered"
// filament-translucency model, extracted here so package palette (which
// package voxel imports) can compute effective colors for TD-aware palette
// selection without importing voxel. voxel.EffectivePalette calls it too, so
// there is one definition of the leak and both stages stay in lockstep.
//
// The model is the N-crossing Beer–Lambert recursion of
// docs/td-translucency-model.md:
//
//   - Per-crossing path length ℓ = h·√2, where h is the layer height (mm) and
//     √2 = 1/cos(45°) is a representative viewing angle.
//   - Per-crossing transmission T = 10^(−ℓ/td) (Beer–Lambert).
//   - Number of shell crossings N = s/h (shell thickness / layer height),
//     clamped to [1,64] as a float — not rounded — so N doesn't step
//     discontinuously as the shell/layer ratio slides.
//   - Leak L = T^N: the fraction that survives the whole shell.
//
// Identity/sanitization contract (matched byte-for-byte by
// voxel.EffectivePalette's identity paths):
//
//   - A garbage td (≤ 0, NaN, or ±Inf) is treated as fully opaque → 0. A
//     hand-authored --inventory file can carry garbage that must not poison
//     the color model.
//   - A leak below 1/1024 is treated as fully opaque → 0, so a purely-opaque
//     palette composites to itself exactly rather than merely round-trip-equal.
//   - Degenerate geometry falls back to defaults: h ≤ 0 or NaN → 0.2 mm;
//     s ≤ 0 or NaN → 0.84 mm.
func TDLeak(td, layerHeightMM, shellThicknessMM float64) float64 {
	// Geometry with defensive fallbacks. !(x > 0) catches both NaN and ≤ 0.
	h := layerHeightMM
	if !(h > 0) {
		h = 0.2
	}
	s := shellThicknessMM
	if !(s > 0) {
		s = 0.84
	}

	// !(td > 0) catches NaN and ≤ 0; IsInf catches ±Inf. Any → opaque.
	if !(td > 0) || math.IsInf(td, 0) {
		return 0
	}

	ell := h * math.Sqrt2

	// N = shell / layer, clamped to [1,64] (float, not rounded).
	n := s / h
	if n < 1 {
		n = 1
	} else if n > 64 {
		n = 64
	}

	t := math.Pow(10, -ell/td)
	leak := math.Pow(t, n)
	if leak < 1.0/1024.0 {
		// Effectively opaque.
		return 0
	}
	return leak
}

// normSelTD normalizes a filament TD for selection exactly as the pipeline's
// infill designation does (see normInfillTD in run.go): missing/non-positive/
// NaN/Inf all collapse to 0 (fully opaque), so garbage in a hand-authored
// inventory can't poison the effective-color model.
func normSelTD(td float32) float64 {
	d := float64(td)
	if !(d > 0) || math.IsInf(d, 0) {
		return 0
	}
	return d
}

// linearOf converts an sRGB byte color to linear-light RGB via go-colorful,
// the same conversion SelectFromInventory uses for its nominal Lab precompute,
// so the effective-color scoring can't drift relative to the uniform-TD path.
func linearOf(c [3]uint8) [3]float64 {
	r, g, b := colorful.Color{
		R: float64(c[0]) / 255.0,
		G: float64(c[1]) / 255.0,
		B: float64(c[2]) / 255.0,
	}.LinearRgb()
	return [3]float64{r, g, b}
}

// DefaultNeighborPathMM is the representative in-plane path length (mm) a
// translucent cell's own filament imposes before a neighbor's color shows
// through. It is the calibration knob (against physical test prints) shared by
// the per-cell print simulation (voxel.EffectiveCellColors) and TD-aware
// palette selection, so both models agree on how much a translucent filament
// washes toward its surroundings. Calibrated to 0.130 mm against a photographed
// swatch print (tools/swatchphoto) under the transmittance model.
//
// NOTE: ℓ is MODEL-SPECIFIC — this 0.130 was fit jointly WITH the transmittance
// filter TransmittanceKappa = 3.04; the two go together (the old additive model
// used 0.3). The pipeline overrides it from DITHERFORGE_SIM_NEIGHBOR_PATH_MM.
const DefaultNeighborPathMM = 0.130

// TransmittanceKappa is the neighbor-transmittance filter exponent κ of the
// print-simulation mixing model: returning neighbor light passes back through the
// translucent cell's body and is filtered by the cell's own per-channel hue
// T_c = (C_c / max_channel(C))^κ before it blends in. κ=0 is the plain additive
// neighbor blend (bit-for-bit). κ≈3 was fit against a photographed swatch print
// (tools/swatchphoto): it best predicts how a saturated filament next to a bright
// neighbor keeps its own hue instead of washing out (and, with a dark neighbor,
// dies). Shared by the per-cell sim and TD-aware selection so they can't drift;
// the pipeline overrides it from DITHERFORGE_SIM_KAPPA. See DefaultNeighborPathMM
// for why ℓ and κ are calibrated as a pair.
const TransmittanceKappa = 3.04

// TransmittanceColor returns the per-channel transmittance T of a filament whose
// LINEAR-light color is lin: T_c = (lin_c / max_c lin)^κ, in [0,1]. It is the hue
// filter the returning neighbor light passes through. A pure-black cell (max ≤ 0)
// or κ = 0 yields T = {1,1,1} (no filtering → additive).
func TransmittanceColor(lin [3]float64, kappa float64) [3]float64 {
	if kappa == 0 {
		return [3]float64{1, 1, 1}
	}
	maxc := lin[0]
	if lin[1] > maxc {
		maxc = lin[1]
	}
	if lin[2] > maxc {
		maxc = lin[2]
	}
	if !(maxc > 0) {
		return [3]float64{1, 1, 1}
	}
	return [3]float64{
		math.Pow(lin[0]/maxc, kappa),
		math.Pow(lin[1]/maxc, kappa),
		math.Pow(lin[2]/maxc, kappa),
	}
}

// NeighborLeak returns β = 10^(−neighborPathMM/td): the fraction of a
// neighbor's color that shows through a cell of a filament with transmission
// distance td (mm) over the representative in-plane path. It is the lateral
// analogue of TDLeak and the shared core of the neighbor translucency model —
// voxel.EffectiveCellColors calls it per cell and TD-aware selection calls it
// per inventory entry, so the simulation and the selection can't drift apart.
//
// Sanitization / clamping contract (matched by both callers):
//   - A garbage td (≤ 0, NaN, or ±Inf) is fully opaque → 0.
//   - A leak below 1/1024 is treated as fully opaque → 0, so an opaque cell
//     comes back byte-identical to its nominal color.
//   - Otherwise clamped to [0, 0.95] so a cell never fully dissolves into its
//     neighbors.
func NeighborLeak(td, neighborPathMM float64) float64 {
	if !(td > 0) || math.IsInf(td, 0) {
		return 0
	}
	b := math.Pow(10, -neighborPathMM/td)
	if b < 1.0/1024.0 {
		return 0
	}
	if b > 0.95 {
		b = 0.95
	}
	return b
}

// TDParams carries the transmission-distance context for TD-aware palette
// selection. When Enabled, SelectFromInventory scores candidate subsets on
// their PER-SAMPLE neighbor-effective colors — for each target sample, each
// filament composited toward THAT sample's color by its lateral leak β (see
// NeighborLeak) and hue filter T — rather than nominal filament colors, so a
// translucent filament isn't picked as a chroma carrier it can't actually
// deliver per cell on the print. This is the selection-time analogue of the
// per-cell print simulation (voxel.EffectiveCellColors): the per-sample vertex
// set mirrors the dither's own per-cell effective-color rule, so a saturated
// translucent filament can't fake-enclose interior body colors it physically
// can't render (see tdSelectState).
//
// Enabled is a request, not a guarantee: SelectFromInventory downgrades to
// bit-identical nominal scoring when the palette's normalized TDs are uniform
// (a common shift toward each sample can't reorder the subsets being compared)
// or all filaments are effectively opaque (β = 0 everywhere).
//
// NeighborPathMM is the in-plane path length (mm) driving β; when ≤ 0 it
// defaults to DefaultNeighborPathMM. LayerHeightMM and ShellThicknessMM are
// retained for callers and the TDLeak-based layered model; the neighbor scorer
// itself no longer uses them.
type TDParams struct {
	Enabled          bool
	LayerHeightMM    float32
	ShellThicknessMM float32
	NeighborPathMM   float32
	// Kappa is the transmittance filter exponent κ (see TransmittanceKappa).
	// 0 is the plain additive blend (bit-identical to the pre-transmittance
	// selection); the pipeline sets it to the calibrated TransmittanceKappa.
	Kappa float32
}

// neighborEffLab composites one filament's linear-RGB color toward a target
// under the transmittance model and returns the result in CIELAB, ready for
// hull/nearest scoring:
//
//	eff = own + β · T ∘ (target − own),  T = (own / max_channel(own))^κ
//
// so the neighbor light the filament washes toward is first filtered by the
// filament's own hue — a translucent orange scores as it actually prints
// (keeping its warmth next to warm targets, dying next to dark ones) rather than
// its vivid nominal color. TD-aware selection calls this per (filament, sample)
// with that sample's own color as the target, mirroring the dither's per-cell
// rule. κ = 0 collapses to the plain additive composite (1−β)·own + β·target,
// bit-for-bit. Callers skip this for β = 0 (opaque) and keep the filament's
// nominal Lab, so opaque entries stay bit-identical.
func neighborEffLab(lin [3]float64, beta float64, targetLin [3]float64, kappa float64) [3]float64 {
	var r, g, b float64
	if kappa == 0 {
		r = (1-beta)*lin[0] + beta*targetLin[0]
		g = (1-beta)*lin[1] + beta*targetLin[1]
		b = (1-beta)*lin[2] + beta*targetLin[2]
	} else {
		t := TransmittanceColor(lin, kappa)
		r = lin[0] + beta*t[0]*(targetLin[0]-lin[0])
		g = lin[1] + beta*t[1]*(targetLin[1]-lin[1])
		b = lin[2] + beta*t[2]*(targetLin[2]-lin[2])
	}
	l, aa, bb := colorful.LinearRgb(r, g, b).Lab()
	return [3]float64{l, aa, bb}
}
