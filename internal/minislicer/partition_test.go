package minislicer

import (
	"math"
	"testing"
)

func TestPartitionUnitSquare(t *testing.T) {
	// Unit square at z=0.
	loop := Loop{
		Points: []Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}},
		Z:      0,
	}
	loop.RefreshDerived()
	layers := []Layer{{Z: 0, LayerIdx: 0, Loops: []Loop{loop}}}

	cases := []struct {
		cellSize  float32
		wantNSec  int
		wantStep  float32
		minLength float32
	}{
		{cellSize: 0.25, wantNSec: 16, wantStep: 0.25, minLength: 0.25},
		{cellSize: 1.0, wantNSec: 4, wantStep: 1.0, minLength: 1.0},
		{cellSize: 0.4, wantNSec: 10, wantStep: 0.4, minLength: 0.4},
		// Perimeter 4, cell 1.5: floor(4/1.5)=2; step=2.0 (≥ 1.5).
		{cellSize: 1.5, wantNSec: 2, wantStep: 2.0, minLength: 1.5},
		// Cell larger than perimeter: 1 section, sub-cellSize.
		{cellSize: 10, wantNSec: 1, wantStep: 4.0, minLength: 0},
	}
	for _, tc := range cases {
		secs := PartitionLoops(layers, tc.cellSize)
		if len(secs) != tc.wantNSec {
			t.Errorf("cellSize=%g: got %d sections, want %d", tc.cellSize, len(secs), tc.wantNSec)
			continue
		}
		for _, s := range secs {
			if math.Abs(float64(s.Length-tc.wantStep)) > 1e-5 {
				t.Errorf("cellSize=%g: section %d Length=%g, want %g", tc.cellSize, s.Index, s.Length, tc.wantStep)
			}
			if s.Length < tc.minLength-1e-5 {
				t.Errorf("cellSize=%g: section %d Length=%g < min %g", tc.cellSize, s.Index, s.Length, tc.minLength)
			}
		}
	}
}

func TestPartitionMidpoints(t *testing.T) {
	// Unit square; cellSize=1 → 4 sections, mid of each = edge midpoints.
	loop := Loop{
		Points: []Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}},
		Z:      0,
	}
	loop.RefreshDerived()
	layers := []Layer{{Z: 0, LayerIdx: 0, Loops: []Loop{loop}}}
	secs := PartitionLoops(layers, 1.0)
	wantMids := []Point2{{0.5, 0}, {1, 0.5}, {0.5, 1}, {0, 0.5}}
	if len(secs) != 4 {
		t.Fatalf("got %d sections, want 4", len(secs))
	}
	for i, s := range secs {
		if math.Abs(float64(s.Mid[0]-wantMids[i][0])) > 1e-5 ||
			math.Abs(float64(s.Mid[1]-wantMids[i][1])) > 1e-5 {
			t.Errorf("section %d midpoint = %v, want %v", i, s.Mid, wantMids[i])
		}
	}
}
