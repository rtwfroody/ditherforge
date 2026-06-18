package voxel

import (
	"context"
	"math"
	"testing"
)

// TestAlphaFromTDSanitizesGarbage guards the single chokepoint that protects
// the dither from a hand-authored inventory file: TD ≤ 0, NaN, or ±Inf must
// become opaque (1), and a finite-but-huge TD must floor above 0 so it never
// zeroes a weighted-average denominator. Every returned alpha must be finite
// and in (0,1].
func TestAlphaFromTDSanitizesGarbage(t *testing.T) {
	nan := float32(math.NaN())
	inf := float32(math.Inf(1))
	for _, tc := range []struct {
		td   float32
		want float32 // exact expected value where it matters
	}{
		{0, 1}, {-5, 1}, {nan, 1}, {inf, 1},
	} {
		if got := AlphaFromTD(tc.td); got != tc.want {
			t.Errorf("AlphaFromTD(%v) = %v, want %v", tc.td, got, tc.want)
		}
	}
	// Huge but finite TD: alpha tiny but strictly positive and finite.
	huge := AlphaFromTD(1e9)
	if huge <= 0 || huge > 1 || math.IsNaN(float64(huge)) || math.IsInf(float64(huge), 0) {
		t.Errorf("AlphaFromTD(1e9) = %v, want a small finite value in (0,1]", huge)
	}
}

// TestPaletteAlphasUniformIsNil locks in that a uniform TD slice collapses to
// nil (the kernels' exact identity path), so the default all-opaque pipeline
// is bit-identical to the pre-TD dither.
func TestPaletteAlphasUniformIsNil(t *testing.T) {
	if a := PaletteAlphas([]float32{1, 1, 1, 1}); a != nil {
		t.Errorf("uniform TDs gave %v, want nil", a)
	}
	if a := PaletteAlphas([]float32{4.3, 4.3}); a != nil {
		t.Errorf("uniform non-default TDs gave %v, want nil", a)
	}
	if a := PaletteAlphas([]float32{1, 4.3}); a == nil {
		t.Errorf("mixed TDs gave nil, want a real slice")
	}
}

// gridCells builds a w×h grid of cells all set to target color `col`,
// with a 4-neighbor adjacency graph (face weight 1.0). Area = 1 each.
func gridCells(w, h int, col [3]uint8) ([]ActiveCell, [][]Neighbor) {
	n := w * h
	cells := make([]ActiveCell, n)
	neighbors := make([][]Neighbor, n)
	at := func(x, y int) int { return y*w + x }
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := at(x, y)
			cells[i] = ActiveCell{Col: x, Row: y, Cx: float32(x), Cy: float32(y), Color: col, Area: 1}
			var nb []Neighbor
			if x > 0 {
				nb = append(nb, Neighbor{Idx: at(x-1, y), Weight: 1})
			}
			if x < w-1 {
				nb = append(nb, Neighbor{Idx: at(x+1, y), Weight: 1})
			}
			if y > 0 {
				nb = append(nb, Neighbor{Idx: at(x, y-1), Weight: 1})
			}
			if y < h-1 {
				nb = append(nb, Neighbor{Idx: at(x, y+1), Weight: 1})
			}
			neighbors[i] = nb
		}
	}
	return cells, neighbors
}

func fracAssignedTo(assigns []int32, idx int32) float64 {
	c := 0
	for _, a := range assigns {
		if a == idx {
			c++
		}
	}
	return float64(c) / float64(len(assigns))
}

// TestOpacityWeightingRaisesTranslucentArea is the core check for the
// red+yellow→orange problem: an opaque red (low TD) and a translucent
// yellow (high TD) dithered toward orange must spend MORE area on the
// yellow when opacity weighting is on, because each translucent yellow
// cell contributes less to the perceived mix.
func TestOpacityWeightingRaisesTranslucentArea(t *testing.T) {
	orange := [3]uint8{255, 140, 0}
	pal := [][3]uint8{{255, 0, 0}, {255, 255, 0}} // red (idx 0), yellow (idx 1)
	cells, neighbors := gridCells(48, 48, orange)

	// Baseline: opaque (nil alpha) — historical area-weighted behavior.
	base, err := DitherWithNeighbors(context.Background(), cells, pal, nil, neighbors, nil)
	if err != nil {
		t.Fatalf("baseline dither: %v", err)
	}
	baseYellow := fracAssignedTo(base, 1)

	// Opacity-weighted: red opaque (TD 1), yellow translucent (TD 4.3).
	palAlpha := PaletteAlphas([]float32{1.0, 4.3})
	weighted, err := DitherWithNeighbors(context.Background(), cells, pal, palAlpha, neighbors, nil)
	if err != nil {
		t.Fatalf("weighted dither: %v", err)
	}
	weightedYellow := fracAssignedTo(weighted, 1)

	t.Logf("yellow area fraction: opaque=%.3f  opacity-weighted=%.3f (alpha red=%.3f yellow=%.3f)",
		baseYellow, weightedYellow, AlphaFromTD(1.0), AlphaFromTD(4.3))

	if weightedYellow <= baseYellow {
		t.Errorf("opacity weighting did not raise translucent-yellow area: opaque=%.3f weighted=%.3f",
			baseYellow, weightedYellow)
	}
	// Both colors should still be in play (sanity: we're actually dithering).
	if baseYellow <= 0 || baseYellow >= 1 {
		t.Errorf("baseline yellow fraction %.3f is degenerate; test fixture not dithering", baseYellow)
	}
}

// TestAllModesRespondToTD verifies every production dither mode spends more
// area on the translucent yellow when opacity weighting is on — i.e. TD is
// actually wired into each mode, not just the dizzy family.
func TestAllModesRespondToTD(t *testing.T) {
	orange := [3]uint8{255, 140, 0}
	pal := [][3]uint8{{255, 0, 0}, {255, 255, 0}} // red (0), yellow (1)
	cells, neighbors := gridCells(48, 48, orange)
	mixed := PaletteAlphas([]float32{1.0, 4.3}) // red opaque, yellow translucent
	ctx := context.Background()

	modes := []struct {
		name string
		run  func(palAlpha []float32) ([]int32, error)
	}{
		{"floyd-steinberg", func(a []float32) ([]int32, error) {
			return FloydSteinberg(ctx, cells, pal, a, neighbors, nil)
		}},
		{"dizzy-corrected", func(a []float32) ([]int32, error) {
			return DitherCorrected(ctx, cells, pal, a, neighbors, nil)
		}},
		{"riemersma", func(a []float32) ([]int32, error) {
			return Riemersma(ctx, cells, pal, a, neighbors, RiemersmaInputBiasDefault, nil)
		}},
		{"riemersma-pair", func(a []float32) ([]int32, error) {
			return RiemersmaPair(ctx, cells, pal, a, neighbors, RiemersmaPairCancellationDefault, RiemersmaInputBiasDefault, nil)
		}},
		{"blue-noise", func(a []float32) ([]int32, error) {
			return BlueNoiseAdaptive(ctx, cells, pal, a, neighbors, BlueNoiseAdaptiveTolDefault, nil)
		}},
	}
	for _, m := range modes {
		base, err := m.run(nil)
		if err != nil {
			t.Fatalf("%s base: %v", m.name, err)
		}
		weighted, err := m.run(mixed)
		if err != nil {
			t.Fatalf("%s weighted: %v", m.name, err)
		}
		by, wy := fracAssignedTo(base, 1), fracAssignedTo(weighted, 1)
		t.Logf("%-16s yellow area: opaque=%.3f opacity-weighted=%.3f", m.name, by, wy)
		if wy <= by {
			t.Errorf("%s: opacity weighting did not raise yellow area (opaque=%.3f weighted=%.3f)", m.name, by, wy)
		}
		if by <= 0 || by >= 1 {
			t.Errorf("%s: baseline yellow fraction %.3f degenerate (not dithering)", m.name, by)
		}
	}
}

// TestDitherCorrectedNoTranslucentBleed is a regression guard for the bug
// where DitherCorrected weighted the OUTPUT mean by alpha but the INPUT mean
// plainly: a translucent color's alpha-discount then looked like a permanent
// global drift, so the corrector shifted every cell toward that hue and
// scattered it into solid regions of other colors (yellow speckles all over a
// pure-red 3DBenchy hull). A field that is half pure-red, half pure-yellow
// with a translucent yellow must keep the deep-red region essentially red.
func TestDitherCorrectedNoTranslucentBleed(t *testing.T) {
	w, h := 64, 64
	pal := [][3]uint8{{255, 0, 0}, {255, 255, 0}} // red (0), yellow (1)
	palAlpha := PaletteAlphas([]float32{1.0, 4.3})  // yellow translucent
	cells, neighbors := gridCells(w, h, [3]uint8{255, 0, 0})
	for y := 0; y < h; y++ {
		for x := w / 2; x < w; x++ {
			cells[y*w+x].Color = [3]uint8{255, 255, 0} // right half: yellow target
		}
	}
	assigns, err := DitherCorrected(context.Background(), cells, pal, palAlpha, neighbors, nil)
	if err != nil {
		t.Fatalf("DitherCorrected: %v", err)
	}
	// Deep-red quarter (x < w/4), far from the red/yellow boundary.
	yellow, total := 0, 0
	for y := 0; y < h; y++ {
		for x := 0; x < w/4; x++ {
			total++
			if assigns[y*w+x] == 1 {
				yellow++
			}
		}
	}
	frac := float64(yellow) / float64(total)
	t.Logf("yellow fraction in deep-red quarter: %.3f", frac)
	if frac > 0.02 {
		t.Errorf("translucent yellow bled into solid-red region: %.3f (want ~0)", frac)
	}
}

// TestUniformAlphaIsIdentity guarantees the backwards-compatibility
// property: a nil alpha and any uniform alpha must produce byte-identical
// assignments, since a constant opacity cancels in the renormalized mix.
func TestUniformAlphaIsIdentity(t *testing.T) {
	target := [3]uint8{90, 160, 70}
	pal := [][3]uint8{{0, 0, 0}, {255, 255, 255}, {0, 255, 0}, {255, 0, 0}}
	cells, neighbors := gridCells(40, 40, target)

	nilA, err := DitherWithNeighbors(context.Background(), cells, pal, nil, neighbors, nil)
	if err != nil {
		t.Fatalf("nil-alpha dither: %v", err)
	}
	uniform := []float32{0.5, 0.5, 0.5, 0.5}
	uniA, err := DitherWithNeighbors(context.Background(), cells, pal, uniform, neighbors, nil)
	if err != nil {
		t.Fatalf("uniform-alpha dither: %v", err)
	}
	for i := range nilA {
		if nilA[i] != uniA[i] {
			t.Fatalf("uniform alpha changed assignment at cell %d: nil=%d uniform=%d", i, nilA[i], uniA[i])
		}
	}
}

// TestDitherCorrectedUniformAlphaIsIdentity extends the identity guarantee
// to the production default mode (dizzy-corrected), whose drift correction
// also became opacity-weighted.
func TestDitherCorrectedUniformAlphaIsIdentity(t *testing.T) {
	target := [3]uint8{120, 90, 200}
	pal := [][3]uint8{{0, 0, 0}, {255, 255, 255}, {0, 0, 255}, {255, 0, 0}}
	cells, neighbors := gridCells(40, 40, target)

	nilA, err := DitherCorrected(context.Background(), cells, pal, nil, neighbors, nil)
	if err != nil {
		t.Fatalf("nil-alpha corrected: %v", err)
	}
	uniform := []float32{0.7, 0.7, 0.7, 0.7}
	uniA, err := DitherCorrected(context.Background(), cells, pal, uniform, neighbors, nil)
	if err != nil {
		t.Fatalf("uniform-alpha corrected: %v", err)
	}
	for i := range nilA {
		if nilA[i] != uniA[i] {
			t.Fatalf("uniform alpha changed corrected assignment at cell %d: nil=%d uniform=%d", i, nilA[i], uniA[i])
		}
	}
}
