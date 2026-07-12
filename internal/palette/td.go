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

// TDParams carries the transmission-distance context for TD-aware palette
// selection. When Enabled, SelectFromInventory scores candidate subsets on
// their TD-effective colors — each filament composited toward the subset's
// infill through the printed shell (see TDLeak) — rather than nominal filament
// colors, so a translucent filament isn't picked as a chroma carrier it can't
// actually deliver on the print.
//
// Enabled is a request, not a guarantee: SelectFromInventory downgrades to
// bit-identical nominal scoring when the palette's normalized TDs are uniform
// or all filaments are effectively opaque, since a uniform shift toward infill
// (or no shift at all) can't change which subset scores best.
type TDParams struct {
	Enabled          bool
	LayerHeightMM    float32
	ShellThicknessMM float32
}

// tdScorer scores a candidate subset on TD-effective colors. It captures the
// per-entry linear-RGB colors and normalized TDs computed once by
// SelectFromInventory; the only per-subset work is designating the subset's
// infill, compositing each member toward it, and converting to Lab (≤ ~8
// colors — cheap next to the sample-scoring loop that dominates). It is safe
// for concurrent use: score allocates its own vertex slice and mutates no
// shared state, so the parallel exhaustive search can call it freely.
type tdScorer struct {
	invLin     [][3]float64 // linear RGB per inventory entry
	invNorm    []float64    // normalized TD per inventory entry (garbage→0)
	lockedLin  [][3]float64 // linear RGB per locked entry
	lockedNorm []float64    // normalized TD per locked entry
	layer      float64      // layer height (mm)
	shell      float64      // shell thickness (mm)
	// vertScore is the geometric scoring core (hull when dithering, nearest
	// otherwise), applied to the composited effective-color vertices.
	vertScore vertScoreFunc
}

// score matches scoreFunc. The precomputed invLab/fixedLab are ignored: for
// TD-aware scoring the vertices depend on the subset's infill, so they are
// composited fresh per call.
func (s *tdScorer) score(indices []int, _ [][3]float64, _ [][3]float64, samples []WeightedLabSample) float64 {
	return s.vertScore(s.effVerts(indices), samples)
}

// effVerts composites the subset (all locked entries plus the chosen inventory
// indices) toward its designated infill and returns the effective colors in
// CIELAB, ready for hull/nearest scoring.
func (s *tdScorer) effVerts(indices []int) [][3]float64 {
	nLocked := len(s.lockedLin)

	// Designate the subset's infill: the lowest normalized TD (most opaque).
	// Ties resolve to locked-before-inventory then lowest index, which falls
	// out of a strict-less scan over locked entries first, then the (ascending
	// in the exhaustive search) inventory indices. This mirrors the pipeline's
	// designateInfill rule minus the most-used tie-break, which can't be
	// evaluated at selection time. Ties don't matter for the effective colors:
	// tied-lowest entries are all near-opaque, so which one is nominally the
	// infill barely moves the composite.
	infillTD := math.Inf(1)
	var infillLin [3]float64
	for i := 0; i < nLocked; i++ {
		if s.lockedNorm[i] < infillTD {
			infillTD = s.lockedNorm[i]
			infillLin = s.lockedLin[i]
		}
	}
	for _, idx := range indices {
		if s.invNorm[idx] < infillTD {
			infillTD = s.invNorm[idx]
			infillLin = s.invLin[idx]
		}
	}

	verts := make([][3]float64, nLocked+len(indices))
	for i := 0; i < nLocked; i++ {
		verts[i] = effLab(s.lockedLin[i], s.lockedNorm[i], infillLin, s.layer, s.shell)
	}
	for j, idx := range indices {
		verts[nLocked+j] = effLab(s.invLin[idx], s.invNorm[idx], infillLin, s.layer, s.shell)
	}
	return verts
}

// effLab composites one filament's linear-RGB color toward the infill through
// the printed shell (leak fraction from TDLeak) and returns the result in
// CIELAB. An opaque filament (leak 0) short-circuits to its own color, so its
// vertex is unaffected by the infill choice.
func effLab(lin [3]float64, normTD float64, infillLin [3]float64, layer, shell float64) [3]float64 {
	leak := TDLeak(normTD, layer, shell)
	r, g, b := lin[0], lin[1], lin[2]
	if leak > 0 {
		r = (1-leak)*lin[0] + leak*infillLin[0]
		g = (1-leak)*lin[1] + leak*infillLin[1]
		b = (1-leak)*lin[2] + leak*infillLin[2]
	}
	l, a, bb := colorful.LinearRgb(r, g, b).Lab()
	return [3]float64{l, a, bb}
}
