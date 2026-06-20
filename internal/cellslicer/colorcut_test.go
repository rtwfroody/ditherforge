package cellslicer

import (
	"math"
	"testing"
)

// squareFootprint builds a single CCW square coverTarget [0,size]².
func squareFootprint(size float32) *Footprint {
	lp := Loop{Points: []Point2{{0, 0}, {size, 0}, {size, size}, {0, size}}}
	lp.RefreshDerived()
	return ComputeFootprint([]Loop{lp}, nil)
}

// TestColorRegionsCheckerboard: a checkerboard with squares larger than
// a cell segments into many distinct monochrome regions, and those
// regions still tile the whole coverTarget.
func TestColorRegionsCheckerboard(t *testing.T) {
	const size = 8.0
	const cellSize = 1.0
	const checker = 2.0 // ≥ cellSize, so every square is honourable

	cover := squareFootprint(size)
	sample := func(x, y float32) ([3]uint8, bool) {
		cx := int(math.Floor(float64(x / checker)))
		cy := int(math.Floor(float64(y / checker)))
		if (cx+cy)%2 == 0 {
			return [3]uint8{0, 0, 0}, true
		}
		return [3]uint8{255, 255, 255}, true
	}

	regions := ColorRegions(cover, cellSize, 30, sample)
	if len(regions) < 4 {
		t.Fatalf("expected many checker regions, got %d", len(regions))
	}

	// Regions tile coverTarget: total area ≈ cover area.
	var total float64
	for _, r := range regions {
		total += footprintArea(r)
	}
	want := footprintArea(cover)
	if d := math.Abs(total-want) / want; d > 0.05 {
		t.Fatalf("regions do not tile coverTarget: total=%.3f want=%.3f (%.1f%% off)", total, want, d*100)
	}
}

// TestColorRegionsGradientNotCut: a smooth black→white gradient has no
// sharp edge, so above a modest ΔE threshold it must stay ONE region
// (ColorRegions returns nil) rather than over-segmenting into bands.
func TestColorRegionsGradientNotCut(t *testing.T) {
	const size = 8.0
	const cellSize = 1.0

	cover := squareFootprint(size)
	// Linear ramp across x: adjacent grid nodes differ by a tiny ΔE.
	sample := func(x, y float32) ([3]uint8, bool) {
		v := uint8(x / size * 255)
		return [3]uint8{v, v, v}, true
	}

	if regions := ColorRegions(cover, cellSize, 20, sample); regions != nil {
		t.Fatalf("smooth gradient should not be cut at ΔE=20, got %d regions", len(regions))
	}
	// A low threshold WILL start cutting the ramp into bands.
	if regions := ColorRegions(cover, cellSize, 1, sample); len(regions) < 2 {
		t.Fatalf("at ΔE=1 the ramp should over-segment, got %d regions", len(regions))
	}
}

// TestColorRegionsIsolatedIslandCovered: a small disconnected coverTarget
// island of a distinct colour must be covered by some region, never left
// in no region — an uncovered island is a hole in the printed shell. (An
// isolated island is "deep" by isDeep's definition, so it survives via the
// keep path; the enforceMinSize freeze path is the additional safety net
// for the narrower non-deep, no-mergeable-neighbour case.) Locks the
// disjoint-union==coverTarget invariant against regressions in either path.
func TestColorRegionsIsolatedIslandCovered(t *testing.T) {
	const cellSize = 1.0

	// Two disconnected components: a fat 6×6 square at the origin and a
	// tiny 0.5mm island far away. Different colours, so the grid segments
	// them; the island is sub-cell with no neighbour.
	big := Loop{Points: []Point2{{0, 0}, {6, 0}, {6, 6}, {0, 6}}}
	big.RefreshDerived()
	speck := Loop{Points: []Point2{{20, 20}, {20.5, 20}, {20.5, 20.5}, {20, 20.5}}}
	speck.RefreshDerived()
	cover := ComputeFootprint([]Loop{big, speck}, nil)

	sample := func(x, y float32) ([3]uint8, bool) {
		if x > 10 {
			return [3]uint8{0, 0, 0}, true // the speck
		}
		return [3]uint8{255, 255, 255}, true // the big square
	}

	regions := ColorRegions(cover, cellSize, 30, sample)
	if len(regions) < 2 {
		t.Fatalf("expected the island kept as its own region, got %d regions", len(regions))
	}
	// The island's location must be covered by some region — the bug
	// dropped the island, leaving (20.25,20.25) in no region at all.
	covered := false
	for _, r := range regions {
		if r.Contains(20.25, 20.25) {
			covered = true
			break
		}
	}
	if !covered {
		t.Fatalf("isolated sub-cell island was dropped — its location is in no region (hole in shell)")
	}
}

// TestColorRegionsSubCellSpeckMerged: a colour feature smaller than a
// cell must NOT become its own region — it is merged into its
// neighbour, leaving a single colour and thus no cut (nil result).
func TestColorRegionsSubCellSpeckMerged(t *testing.T) {
	const size = 8.0
	const cellSize = 1.0

	cover := squareFootprint(size)
	// A 0.4mm black speck (< cellSize) centred at (4,4) on white.
	sample := func(x, y float32) ([3]uint8, bool) {
		if x >= 3.8 && x <= 4.2 && y >= 3.8 && y <= 4.2 {
			return [3]uint8{0, 0, 0}, true
		}
		return [3]uint8{255, 255, 255}, true
	}

	regions := ColorRegions(cover, cellSize, 30, sample)
	if regions != nil {
		t.Fatalf("sub-cell speck should merge away (nil regions), got %d regions", len(regions))
	}
}

// TestColorRegionsHalfSplit: a single high-contrast edge between two
// large regions is honoured — exactly two regions, splitting cover in
// half along the colour boundary.
func TestColorRegionsHalfSplit(t *testing.T) {
	const size = 8.0
	const cellSize = 1.0

	cover := squareFootprint(size)
	sample := func(x, y float32) ([3]uint8, bool) {
		if x < 4.0 {
			return [3]uint8{0, 0, 0}, true
		}
		return [3]uint8{255, 255, 255}, true
	}

	regions := ColorRegions(cover, cellSize, 30, sample)
	if len(regions) != 2 {
		t.Fatalf("expected 2 regions across one edge, got %d", len(regions))
	}
	// Each half ≈ 32mm².
	for i, r := range regions {
		a := footprintArea(r)
		if a < 24 || a > 40 {
			t.Errorf("region %d area %.2f not ≈ half of 64", i, a)
		}
	}
}
