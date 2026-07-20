package voxel

import (
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/palette"
)

// srgbToLinearLUT / linearToSrgbByte are exercised indirectly; these tests
// only compare bytes and coarse color-channel trends.

const (
	tdTestLayerH = float32(0.2)
	tdTestShell  = float32(0.84)
)

var (
	tdWhite = [3]uint8{255, 255, 255}
	tdRed   = [3]uint8{200, 40, 40}
)

// TestEffectivePaletteGarbageTDIdentity: a TD of 0, negative, NaN, or ±Inf is
// treated as fully opaque, so the entry's bytes come back untouched.
func TestEffectivePaletteGarbageTDIdentity(t *testing.T) {
	pal := [][3]uint8{tdRed, tdRed, tdRed, tdRed, tdRed}
	tds := []float32{
		0,
		-1,
		float32(math.NaN()),
		float32(math.Inf(1)),
		float32(math.Inf(-1)),
	}
	out := EffectivePalette(pal, tds, tdTestLayerH, tdTestShell, tdWhite)
	for i := range pal {
		if out[i] != pal[i] {
			t.Errorf("garbage TD %v: entry %d = %v, want identity %v", tds[i], i, out[i], pal[i])
		}
	}
}

// TestEffectivePaletteOpaqueIdentity: a genuinely opaque filament leaks a
// negligible amount of infill (L < 1/1024), so the short-circuit returns the
// entry's bytes exactly. TD=0.3 mm at h=0.2, s=0.84 gives L≈1e-4.
//
// NB: the spec's suggested "0.6 mm → ≤1 byte-step" example does NOT hold under
// this model — 0.6 mm leaks ~1% infill (≈10-byte shift on dark channels),
// consistent with the design doc's own figure that an "opaque" TD=0.8 filament
// still leaks ~4%. Only sub-~0.4 mm TDs trip the identity short-circuit here.
func TestEffectivePaletteOpaqueIdentity(t *testing.T) {
	pal := [][3]uint8{tdRed}
	tds := []float32{0.3}
	out := EffectivePalette(pal, tds, tdTestLayerH, tdTestShell, tdWhite)
	if out[0] != pal[0] {
		t.Errorf("opaque TD=0.3: got %v, want byte-identical %v", out[0], pal[0])
	}
}

// TestEffectivePaletteTranslucentTowardInfill: a translucent red over white
// infill gets lighter (higher luminance) and less saturated (channels pulled
// together toward white).
func TestEffectivePaletteTranslucentTowardInfill(t *testing.T) {
	pal := [][3]uint8{tdRed}
	tds := []float32{6}
	out := EffectivePalette(pal, tds, tdTestLayerH, tdTestShell, tdWhite)
	got := out[0]

	if got == tdRed {
		t.Fatalf("translucent TD=6 should transform, got identity %v", got)
	}
	// Every channel moves toward white (255): each ≥ the original.
	for ch := 0; ch < 3; ch++ {
		if got[ch] < tdRed[ch] {
			t.Errorf("ch %d moved away from white infill: %d < %d", ch, got[ch], tdRed[ch])
		}
	}
	// Less saturated: the green/blue channels rise more than red (which is
	// already near-white in the source), so the max-min spread shrinks.
	spreadIn := int(tdRed[0]) - int(tdRed[1])
	spreadOut := int(got[0]) - int(got[1])
	if spreadOut >= spreadIn {
		t.Errorf("saturation did not drop: spread out %d >= in %d", spreadOut, spreadIn)
	}
}

// TestEffectivePaletteMonotonic: larger TD shifts further toward infill, and a
// thicker shell shifts less.
func TestEffectivePaletteMonotonic(t *testing.T) {
	// Distance from red toward white infill along the red (0) channel.
	shift := func(td, shell float32) int {
		out := EffectivePalette([][3]uint8{tdRed}, []float32{td}, tdTestLayerH, shell, tdWhite)
		return int(out[0][1]) - int(tdRed[1]) // green channel rises toward 255
	}

	s4 := shift(4, tdTestShell)
	s6 := shift(6, tdTestShell)
	s10 := shift(10, tdTestShell)
	if !(s4 < s6 && s6 < s10) {
		t.Errorf("shift not monotonic in TD: TD4=%d TD6=%d TD10=%d", s4, s6, s10)
	}

	thin := shift(6, 0.4)
	thick := shift(6, 1.6)
	if !(thick < thin) {
		t.Errorf("thicker shell should shift less: thin=%d thick=%d", thin, thick)
	}
}

// TestEffectivePaletteUniformStillTransforms: unlike PaletteAlphas (which
// returns nil for a uniform-TD palette because a uniform opacity cancels in
// the renormalized mix), a uniformly translucent palette really does wash
// toward the infill and must transform.
func TestEffectivePaletteUniformStillTransforms(t *testing.T) {
	pal := [][3]uint8{tdRed, {40, 200, 40}, {40, 40, 200}}
	tds := []float32{6, 6, 6}
	out := EffectivePalette(pal, tds, tdTestLayerH, tdTestShell, tdWhite)
	for i := range pal {
		if out[i] == pal[i] {
			t.Errorf("uniform translucent entry %d unchanged: %v", i, out[i])
		}
	}
}

// TestEffectivePaletteNoMutation: the input palette slice is not modified.
func TestEffectivePaletteNoMutation(t *testing.T) {
	pal := [][3]uint8{tdRed, {40, 200, 40}}
	orig := [][3]uint8{pal[0], pal[1]}
	_ = EffectivePalette(pal, []float32{6, 6}, tdTestLayerH, tdTestShell, tdWhite)
	for i := range pal {
		if pal[i] != orig[i] {
			t.Errorf("input mutated at %d: %v != %v", i, pal[i], orig[i])
		}
	}
}

// TestEffectivePaletteMissingTDOpaque: fewer TDs than palette entries — the
// missing entries are opaque (identity).
func TestEffectivePaletteMissingTDOpaque(t *testing.T) {
	pal := [][3]uint8{tdRed, tdRed}
	out := EffectivePalette(pal, []float32{6}, tdTestLayerH, tdTestShell, tdWhite)
	if out[0] == pal[0] {
		t.Errorf("entry 0 (TD=6) should transform")
	}
	if out[1] != pal[1] {
		t.Errorf("entry 1 (no TD) should be identity, got %v", out[1])
	}
}

// --- EffectiveCellColors ---

// Palette shared by the per-cell tests: opaque black (TD 0.1) and translucent
// orange (TD 3.3), mirroring a printed orange speckle in a grey/black field.
var (
	cellBlack   = [3]uint8{0, 0, 0}
	cellOrange  = [3]uint8{230, 120, 20}
	cellPalette = [][3]uint8{cellBlack, cellOrange}
	cellTDs     = []float32{0.1, 3.3}
)

const (
	idxBlack  int32 = 0
	idxOrange int32 = 1
)

func lum(c [3]uint8) int {
	return int(c[0]) + int(c[1]) + int(c[2])
}

// unitCells builds n ActiveCells, all unit area (equal neighbor weights).
func unitCells(n int) []ActiveCell {
	cells := make([]ActiveCell, n)
	for i := range cells {
		cells[i].Area = 1
	}
	return cells
}

// TestEffectiveCellColorsLineBlend: an orange (translucent) cell between two
// black (opaque) cells darkens toward black, while the black cells — being
// opaque — come back byte-identical.
func TestEffectiveCellColorsLineBlend(t *testing.T) {
	cells := unitCells(3)
	assign := []int32{idxBlack, idxOrange, idxBlack}
	neigh := [][]Neighbor{
		{{Idx: 1, Weight: 1}},
		{{Idx: 0, Weight: 1}, {Idx: 2, Weight: 1}},
		{{Idx: 1, Weight: 1}},
	}
	out := EffectiveCellColors(cells, assign, cellPalette, cellTDs, neigh, 1.0, 2, 0)

	if out[0] != cellBlack || out[2] != cellBlack {
		t.Errorf("opaque black cells changed: %v %v", out[0], out[2])
	}
	if lum(out[1]) >= lum(cellOrange) {
		t.Errorf("translucent center did not darken toward black neighbors: %v (lum %d) vs orange lum %d",
			out[1], lum(out[1]), lum(cellOrange))
	}
	// Every channel moves toward the black (0) neighbors: none brighter.
	for ch := 0; ch < 3; ch++ {
		if out[1][ch] > cellOrange[ch] {
			t.Errorf("ch %d brightened toward black neighbors: %d > %d", ch, out[1][ch], cellOrange[ch])
		}
	}
}

// TestEffectiveCellColorsAllOpaqueIdentity: an all-opaque palette returns the
// nominal cell colors byte-for-byte (fast path).
func TestEffectiveCellColorsAllOpaqueIdentity(t *testing.T) {
	cells := unitCells(3)
	assign := []int32{idxBlack, idxBlack, idxBlack}
	pal := [][3]uint8{{10, 20, 30}}
	tds := []float32{0.1} // opaque
	assignOpaque := []int32{0, 0, 0}
	neigh := [][]Neighbor{
		{{Idx: 1, Weight: 1}},
		{{Idx: 0, Weight: 1}, {Idx: 2, Weight: 1}},
		{{Idx: 1, Weight: 1}},
	}
	_ = assign
	out := EffectiveCellColors(cells, assignOpaque, pal, tds, neigh, 1.0, 2, 0)
	for i := range out {
		if out[i] != pal[0] {
			t.Errorf("opaque cell %d changed: %v want %v", i, out[i], pal[0])
		}
	}
}

// TestEffectiveCellColorsOutOfRangeAssignment: an out-of-range or negative
// assignment yields a gray placeholder and never panics, and neither poisons a
// translucent neighbor's blend (it's excluded as a source).
func TestEffectiveCellColorsOutOfRangeAssignment(t *testing.T) {
	cells := unitCells(3)
	assign := []int32{99, idxOrange, -1} // both ends invalid
	neigh := [][]Neighbor{
		{{Idx: 1, Weight: 1}},
		{{Idx: 0, Weight: 1}, {Idx: 2, Weight: 1}},
		{{Idx: 1, Weight: 1}},
	}
	out := EffectiveCellColors(cells, assign, cellPalette, cellTDs, neigh, 1.0, 2, 0)
	gray := [3]uint8{128, 128, 128}
	if out[0] != gray || out[2] != gray {
		t.Errorf("invalid cells not gray: %v %v", out[0], out[2])
	}
	// Center has no valid neighbors, so it keeps orange (round-trip through
	// linear-light must not drift more than a byte on any channel).
	for ch := 0; ch < 3; ch++ {
		d := int(out[1][ch]) - int(cellOrange[ch])
		if d < -1 || d > 1 {
			t.Errorf("center cell drifted with only-invalid neighbors: ch %d %d vs %d", ch, out[1][ch], cellOrange[ch])
		}
	}
}

// TestEffectiveCellColorsIterationsPropagate: on a 5-cell chain black-orange-
// orange-orange-black, a second Jacobi pass lets black reach the middle cell
// through the intervening orange cells (which darkened on pass 1), so the
// middle cell is strictly darker with 2 iterations than with 1.
func TestEffectiveCellColorsIterationsPropagate(t *testing.T) {
	cells := unitCells(5)
	assign := []int32{idxBlack, idxOrange, idxOrange, idxOrange, idxBlack}
	neigh := [][]Neighbor{
		{{Idx: 1, Weight: 1}},
		{{Idx: 0, Weight: 1}, {Idx: 2, Weight: 1}},
		{{Idx: 1, Weight: 1}, {Idx: 3, Weight: 1}},
		{{Idx: 2, Weight: 1}, {Idx: 4, Weight: 1}},
		{{Idx: 3, Weight: 1}},
	}
	out1 := EffectiveCellColors(cells, assign, cellPalette, cellTDs, neigh, 1.0, 1, 0)
	out2 := EffectiveCellColors(cells, assign, cellPalette, cellTDs, neigh, 1.0, 2, 0)

	// The middle cell (index 2) only "sees" black on the second pass.
	if lum(out2[2]) >= lum(out1[2]) {
		t.Errorf("second iteration did not darken the middle cell: iter1 lum %d, iter2 lum %d",
			lum(out1[2]), lum(out2[2]))
	}
}

// TestEffectiveCellColorsEmpty: no cells returns an empty (non-nil) slice.
func TestEffectiveCellColorsEmpty(t *testing.T) {
	out := EffectiveCellColors(nil, nil, cellPalette, cellTDs, nil, 1.0, 2, 0)
	if out == nil || len(out) != 0 {
		t.Errorf("empty input should return empty slice, got %v", out)
	}
}

// additiveReferenceBlend independently reimplements the pre-transmittance
// additive model — C_{t+1}(i) = (1−β)·C0(i) + β·nbAvg(i), self term always C0,
// 2 passes — so the κ=0 path can be checked bit-for-bit against it.
func additiveReferenceBlend(cells []ActiveCell, assign []int32, pal [][3]uint8, tds []float32, neigh [][]Neighbor, ell float32, iters int) [][3]uint8 {
	n := len(cells)
	out := make([][3]uint8, n)
	c0 := make([][3]float64, n)
	beta := make([]float64, n)
	valid := make([]bool, n)
	for i := range cells {
		k := int(assign[i])
		if k < 0 || k >= len(pal) {
			out[i] = [3]uint8{128, 128, 128}
			continue
		}
		valid[i] = true
		c := pal[k]
		out[i] = c
		c0[i] = [3]float64{float64(srgbToLinearLUT[c[0]]), float64(srgbToLinearLUT[c[1]]), float64(srgbToLinearLUT[c[2]])}
		beta[i] = palette.NeighborLeak(float64(tds[k]), float64(ell))
	}
	cur := make([][3]float64, n)
	copy(cur, c0)
	next := make([][3]float64, n)
	for it := 0; it < iters; it++ {
		for i := 0; i < n; i++ {
			if !valid[i] || beta[i] == 0 || len(neigh[i]) == 0 {
				next[i] = c0[i]
				continue
			}
			var sum [3]float64
			var ws float64
			for _, nb := range neigh[i] {
				j := nb.Idx
				if j < 0 || int(j) >= n || !valid[j] {
					continue
				}
				w := float64(nb.Weight) * math.Max(float64(cells[j].Area), 1e-6)
				ws += w
				sum[0] += w * cur[j][0]
				sum[1] += w * cur[j][1]
				sum[2] += w * cur[j][2]
			}
			if ws == 0 {
				next[i] = c0[i]
				continue
			}
			b := beta[i]
			for ch := 0; ch < 3; ch++ {
				next[i][ch] = (1-b)*c0[i][ch] + b*sum[ch]/ws
			}
		}
		cur, next = next, cur
	}
	for i := 0; i < n; i++ {
		if !valid[i] || beta[i] == 0 {
			continue
		}
		out[i] = [3]uint8{linearToSrgbByte(float32(cur[i][0])), linearToSrgbByte(float32(cur[i][1])), linearToSrgbByte(float32(cur[i][2]))}
	}
	return out
}

// TestEffectiveCellColorsKappaZeroBitIdentical: κ=0 must reproduce the pre-change
// additive model bit-for-bit. Compared to an independent reimplementation on a
// fixed mixed graph (translucent + opaque, varied weights).
func TestEffectiveCellColorsKappaZeroBitIdentical(t *testing.T) {
	cells := unitCells(4)
	pal := [][3]uint8{cellBlack, cellOrange, {240, 235, 250}}
	tds := []float32{0.1, 3.3, 0.5}
	assign := []int32{1, 0, 2, 1}
	neigh := [][]Neighbor{
		{{Idx: 1, Weight: 1}, {Idx: 2, Weight: 0.1}},
		{{Idx: 0, Weight: 1}, {Idx: 3, Weight: 1}},
		{{Idx: 0, Weight: 0.1}, {Idx: 3, Weight: 1}},
		{{Idx: 1, Weight: 1}, {Idx: 2, Weight: 1}},
	}
	got := EffectiveCellColors(cells, assign, pal, tds, neigh, 0.3, 2, 0)
	want := additiveReferenceBlend(cells, assign, pal, tds, neigh, 0.3, 2)
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("cell %d: κ=0 %v != additive reference %v", i, got[i], want[i])
		}
	}
}

// TestEffectiveCellColorsTransmittanceHue: under κ>0 a translucent orange cell
// next to a bright (white) neighbor keeps its saturated hue (low green/blue)
// where the additive blend washes those channels up toward white. This is the
// asymmetry the transmittance filter adds and the whole reason it was ported.
func TestEffectiveCellColorsTransmittanceHue(t *testing.T) {
	cells := unitCells(2)
	pal := [][3]uint8{cellOrange, {245, 245, 250}}
	tds := []float32{3.3, 0.1} // translucent orange, opaque near-white
	assign := []int32{0, 1}
	neigh := [][]Neighbor{{{Idx: 1, Weight: 1}}, {{Idx: 0, Weight: 1}}}
	add := EffectiveCellColors(cells, assign, pal, tds, neigh, 0.13, 2, 0)
	tm := EffectiveCellColors(cells, assign, pal, tds, neigh, 0.13, 2, 3.04)
	// Orange cell (index 0): transmittance keeps green/blue lower than additive.
	if !(tm[0][1] < add[0][1] && tm[0][2] < add[0][2]) {
		t.Errorf("transmittance did not keep orange saturated next to white: additive=%v transmittance=%v", add[0], tm[0])
	}
	// Orange's own bright channel (red) stays high under transmittance.
	if tm[0][0] < 150 {
		t.Errorf("orange lost its red under transmittance: %v", tm[0])
	}
}
