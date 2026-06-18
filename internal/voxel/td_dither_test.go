package voxel

import (
	"context"
	"testing"
)

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
			cells[i] = ActiveCell{Col: x, Row: y, Color: col, Area: 1}
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
