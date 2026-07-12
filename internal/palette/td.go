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
// washes toward its surroundings. Calibrated to 0.3 mm against a physical print
// (the print reads between the 0.2 and 0.35 renders). The pipeline overrides it
// from DITHERFORGE_SIM_NEIGHBOR_PATH_MM.
const DefaultNeighborPathMM = 0.3

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
// their neighbor-effective colors — each filament composited toward the
// area-weighted mean of the target (cell) colors by its lateral leak β (see
// NeighborLeak) — rather than nominal filament colors, so a translucent
// filament isn't picked as a chroma carrier it can't actually deliver on the
// print. This is the selection-time analogue of the per-cell print simulation
// (voxel.EffectiveCellColors): individual cell placement is unknown when the
// palette is chosen, so the global target mean is the tractable stand-in for
// what a translucent cell's neighbors will statistically look like.
//
// Enabled is a request, not a guarantee: SelectFromInventory downgrades to
// bit-identical nominal scoring when the palette's normalized TDs are uniform
// (a common shift toward the same mean can't reorder the subsets being
// compared) or all filaments are effectively opaque (β = 0 everywhere).
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
}

// weightedMeanLinear returns the cellWeights-weighted mean of cellColors in
// linear-light RGB — the stand-in "neighborhood" every translucent cell washes
// toward under TD-aware selection (see TDParams). cellWeights, when nil, weights
// every color equally; a zero total weight yields black, which only arises when
// there are no cells (in which case selection is already degenerate).
func weightedMeanLinear(cellColors [][3]uint8, cellWeights []float32) [3]float64 {
	var mean [3]float64
	var wsum float64
	for i, c := range cellColors {
		w := 1.0
		if cellWeights != nil && i < len(cellWeights) {
			w = float64(cellWeights[i])
		}
		lin := linearOf(c)
		mean[0] += w * lin[0]
		mean[1] += w * lin[1]
		mean[2] += w * lin[2]
		wsum += w
	}
	if wsum > 0 {
		mean[0] /= wsum
		mean[1] /= wsum
		mean[2] /= wsum
	}
	return mean
}

// neighborEffLab composites one filament's linear-RGB color toward the target
// mean by its lateral leak β and returns the result in CIELAB, ready for
// hull/nearest scoring. Callers skip it for β = 0 (opaque) and keep the
// filament's nominal Lab, so opaque entries stay bit-identical to the nominal
// scoring path.
func neighborEffLab(lin [3]float64, beta float64, meanTargetLin [3]float64) [3]float64 {
	r := (1-beta)*lin[0] + beta*meanTargetLin[0]
	g := (1-beta)*lin[1] + beta*meanTargetLin[1]
	b := (1-beta)*lin[2] + beta*meanTargetLin[2]
	l, aa, bb := colorful.LinearRgb(r, g, b).Lab()
	return [3]float64{l, aa, bb}
}
