package cellslicer

import (
	"testing"
)

// stripFootprint builds an axis-aligned rectangle [0,w]×[0,h] as a
// coverTarget. A width == cellSize models the cube's one-cell-thick
// wall band; a large w,h models a fat cap region.
func stripFootprint(w, h float32) *Footprint {
	lp := Loop{Points: []Point2{{0, 0}, {w, 0}, {w, h}, {0, h}}}
	lp.RefreshDerived()
	return ComputeFootprint([]Loop{lp}, nil)
}

func cellAreaSum(cells []Cell) float64 {
	var sum float64
	for i := range cells {
		a := signedArea(cells[i].Outer)
		if a < 0 {
			a = -a
		}
		sum += float64(a)
	}
	return sum
}

// TestTileColorRegionNoOverlap is the regression guard for the thin-band
// overlap bug: ringSeeds on a region only cellSize wide inset both long
// edges onto the same centreline, emitting coincident seeds whose Voronoi
// cells overlapped (~2× the region area). tileColorRegion thins the ring
// seeds, so the tiled cells must partition the region — total cell area
// equals the region area (no overlap, no gap) — not exceed it.
func TestTileColorRegionNoOverlap(t *testing.T) {
	const cellSize float32 = 1.0
	pxArea := (cellSize / 4) * (cellSize / 4)

	cases := []struct {
		name string
		w, h float32
	}{
		{"thin-band (1 cell wide)", cellSize, 12},
		{"medium (2 cells wide)", 2 * cellSize, 12},
		{"fat cap", 12, 12},
	}
	for _, c := range cases {
		region := stripFootprint(c.w, c.h)
		// A real cap region fills its interior, so give the fat/medium
		// cases a cap mask equal to the region; the thin band has none.
		capMask := &Footprint{}
		if c.w >= 2*cellSize {
			capMask = region
		}
		var stats PartitionStats
		cells := tileColorRegion(region, capMask, cellSize, pxArea, &stats)
		got := cellAreaSum(cells)
		want := footprintArea(region)
		// Allow 1% for Clipper integer rounding at cell edges.
		if d := (got - want) / want; d > 0.01 || d < -0.01 {
			t.Errorf("%s: cell area sum %.3f != region area %.3f (%.1f%% off) — overlap or gap",
				c.name, got, want, d*100)
		}
	}
}
