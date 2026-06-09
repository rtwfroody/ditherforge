package cellslicer

import (
	"math"
	"testing"
)

// diskTopCapFootprints builds the region inputs for a top-cap slab: a disk
// cross-section that is solid below and open above, so neighborBoth is empty
// and innerCap == inner (the whole interior is visible cap surface).
func diskTopCapFootprints(r float32, cellSize float32) (fp, innerCap *Footprint) {
	loop := circleLoop(96, r)
	fp = ComputeFootprint([]Loop{loop}, []Loop{loop})
	innerCap = OffsetFootprint(fp, -cellSize) // neighborBoth empty ⇒ innerCap = inner
	return fp, innerCap
}

// TestConcentricCapSeedsUniform locks the concentric-offset cap seeding: on a
// flat disk cap the cells are nearly all one size (contour-parallel rings, no
// axis-aligned grid seam), every cell stays clear of a dither pixel, and the
// mean cell area sits near cellSize². A regression to the old hex lattice (or
// a broken drop filter) shows up as a much larger area spread or sub-pixel
// cells at the ring/cap interface.
func TestConcentricCapSeedsUniform(t *testing.T) {
	const cs float32 = 1.0
	loop := circleLoop(96, 15)
	fp := ComputeFootprint([]Loop{loop}, []Loop{loop})

	// fpAbove nil ⇒ open top ⇒ innerCap = inner = full interior.
	cells, _, stats := PartitionSlabAnalytic(fp, fp, nil, cs)
	if stats.RawHex == 0 {
		t.Fatal("no cap seeds produced for a flat disk cap")
	}

	px := (cs / 4) * (cs / 4)
	var sum, mn, mx float32
	mn = math.MaxFloat32
	for i := range cells {
		a := absf32(signedArea(cells[i].Outer))
		if a < px {
			t.Errorf("cell %d area %.4f mm² is below one pixel (%.4f)", i, a, px)
		}
		sum += a
		if a < mn {
			mn = a
		}
		if a > mx {
			mx = a
		}
	}
	mean := sum / float32(len(cells))

	// Cells tile a cellSize circle, so each is ~cellSize² in area.
	if mean < 0.85 || mean > 1.15 {
		t.Errorf("mean cell area %.4f mm² is not near cellSize²=1; cap seeding density is off", mean)
	}
	// Uniformity: the whole point of contour-parallel seeding. Measured
	// max/min ≈ 1.14 on this disk (deterministic for a fixed disk+cellSize);
	// 1.35 leaves margin while still catching a regression to the grid, whose
	// clipped interface cells spread the ratio to several-fold.
	if mx/mn > 1.35 {
		t.Errorf("cell area max/min = %.2f (max=%.3f min=%.3f) — cells are not roughly equal", mx/mn, mx, mn)
	}
}

// TestConcentricCapSeedsMinSpacing checks the circle-fit constraint: the drop
// filter keeps cap seeds at least ~cellSize apart across rings, so each cell
// can hold a cellSize-diameter circle (a Voronoi cell's in-circle radius is
// half the nearest-neighbour distance). The lone exception is the cap centre,
// where the innermost ring's equal-arc points sit on a short chord; the 0.8
// floor admits that singularity while still catching a broken filter that
// would crowd seeds to a fraction of cellSize.
func TestConcentricCapSeedsMinSpacing(t *testing.T) {
	const cs float32 = 1.0
	fp, innerCap := diskTopCapFootprints(15, cs)
	ring := ringSeeds(fp, cs)
	cap := concentricCapSeeds(fp, innerCap, cs, ring)
	if len(cap) == 0 {
		t.Fatal("no cap seeds produced")
	}

	all := append(append([]Point2(nil), ring...), cap...)
	minD := float32(math.MaxFloat32)
	for i := range all {
		for j := i + 1; j < len(all); j++ {
			dx := all[i][0] - all[j][0]
			dy := all[i][1] - all[j][1]
			d := float32(math.Hypot(float64(dx), float64(dy)))
			if d < minD {
				minD = d
			}
		}
	}
	if minD < 0.8*cs {
		t.Errorf("min seed spacing %.4f < %.2f — drop filter let seeds crowd; cells may not hold a cellSize circle", minD, 0.8*cs)
	}
}

// TestConcentricCapSeedsSurfaceOnly guards surface-only behaviour: a slab
// whose interior is buried (empty innerCap, e.g. a vertical wall) must get NO
// cap seeds, so the slicer never tiles invisible volume.
func TestConcentricCapSeedsSurfaceOnly(t *testing.T) {
	const cs float32 = 1.0
	fp, _ := diskTopCapFootprints(15, cs)
	if got := concentricCapSeeds(fp, &Footprint{}, cs, nil); got != nil {
		t.Errorf("empty innerCap produced %d cap seeds; want 0 (surface-only)", len(got))
	}
	if got := concentricCapSeeds(fp, nil, cs, nil); got != nil {
		t.Errorf("nil innerCap produced %d cap seeds; want 0", len(got))
	}
}
