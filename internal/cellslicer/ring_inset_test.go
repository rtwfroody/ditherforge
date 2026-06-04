package cellslicer

import (
	"math"
	"testing"
)

// circleLoop builds an n-gon of radius r centered at origin (CCW).
func circleLoop(n int, r float32) Loop {
	pts := make([]Point2, n)
	for i := range n {
		a := 2 * math.Pi * float64(i) / float64(n)
		pts[i] = Point2{r * float32(math.Cos(a)), r * float32(math.Sin(a))}
	}
	return makeLoop(pts, false)
}

// TestRingSeedsInsetKeepsCellsLarge locks the ring-seed inset: ring
// (boundary) seeds are placed cellSize/2 inside the perimeter, not on
// it, so the clipped Voronoi cell lands fully inside the footprint and
// is ~full-size instead of a half-size sliver the slicer would drop.
//
// On a cylinder cross-section (regular 20-gon) the on-perimeter
// placement yields ring cells averaging ~0.66 mm² (cellSize²=1); the
// inset lifts that to ~0.90 mm² and raises the SMALLEST ring cell well
// clear of one dither pixel. A regression back to perimeter seeds drops
// the mean below the threshold here and fails this test.
func TestRingSeedsInsetKeepsCellsLarge(t *testing.T) {
	const cellSize float32 = 1.0
	loop := circleLoop(20, 10) // cylinder cross-section: regular 20-gon, r=10mm
	fp := ComputeFootprint([]Loop{loop}, []Loop{loop})

	cells, _, _ := PartitionSlabAnalytic(fp, nil, nil, cellSize)

	px := (cellSize / 4) * (cellSize / 4) // one dither pixel
	var sum, min float32
	min = math.MaxFloat32
	n := 0
	for i := range cells {
		if cells[i].Kind != KindRing {
			continue
		}
		a := absf32(signedArea(cells[i].Outer))
		sum += a
		if a < min {
			min = a
		}
		n++
		if a < px {
			t.Errorf("ring cell %d area %.4f mm² is below one pixel (%.4f) — inset failed to keep it large", i, a, px)
		}
	}
	if n == 0 {
		t.Fatal("no ring cells produced")
	}
	mean := sum / float32(n)

	// On-perimeter placement gives mean ~0.66; the inset gives ~0.90. A
	// threshold of 0.80 cleanly separates the two: dropping back to
	// perimeter seeds trips this.
	const wantMean float32 = 0.80
	if mean < wantMean {
		t.Errorf("mean ring cell area %.4f mm² < %.2f — ring seeds appear to sit on the perimeter, not inset", mean, wantMean)
	}
	// The smallest ring cell must stay comfortably above a pixel (4x here).
	if min < 4*px {
		t.Errorf("smallest ring cell %.4f mm² is within 4 pixels (%.4f) of the drop threshold", min, 4*px)
	}
}

// TestRingSeedsInsetHandlesHoles confirms decision #2: hole-loop ring
// seeds inset INTO the solid band (away from the void), not toward the
// hole. If inwardNormal's sign were wrong for CW holes the seeds would
// land in the void, fail the Contains gate, and fall back onto the hole
// perimeter — yielding the same half-size sliver cells around holes the
// inset is meant to eliminate. We assert the hole-band ring cells are large.
func TestRingSeedsInsetHandlesHoles(t *testing.T) {
	const cellSize float32 = 1.0
	// Square and hole both centered on the origin so the hole is cleanly
	// interior. makeLoop(..., true) + loopToClipperPath force the hole CW,
	// so Clipper's non-zero union subtracts it (no manual reversal needed).
	outer := makeLoop([]Point2{{-15, -15}, {15, -15}, {15, 15}, {-15, 15}}, false)
	hole := makeLoop(circleLoop(20, 6).Points, true) // r=6 hole at origin
	fp := ComputeFootprint([]Loop{outer, hole}, []Loop{outer, hole})

	cells, _, _ := PartitionSlabAnalytic(fp, nil, nil, cellSize)

	px := (cellSize / 4) * (cellSize / 4)
	// Ring cells whose seed came from the hole sit in the annular band
	// just outside r=6 (centered on origin). Require none to be sliver-sized.
	holeRing := 0
	for i := range cells {
		if cells[i].Kind != KindRing {
			continue
		}
		cx, cy, _ := polyCentroid(cells[i].Outer)
		r := math.Hypot(float64(cx), float64(cy))
		if r < 5 || r > 8.5 {
			continue
		}
		holeRing++
		if a := absf32(signedArea(cells[i].Outer)); a < 4*px {
			t.Errorf("hole-band ring cell %d (r=%.2f) area %.4f mm² is sliver-sized — hole seeds did not inset into the band", i, r, a)
		}
	}
	if holeRing == 0 {
		t.Fatal("no hole-band ring cells found; the hole loop produced no ring seeds")
	}
}

// TestRingSeedsInsetReflexCornersStayInBand guards the one real risk a
// cellSize/2 inward push could carry: at a reflex (concave) corner or a
// sharp spike the band could pinch, and a pushed seed could overshoot
// past `inner` into the hex-lattice (innerCap) region — landing a
// stray ring seed cellSize/2 from a hex seed and producing the degenerate
// sub-pixel cell the inset is meant to eliminate. Across an L-shape, a
// plus (4 reflex corners), an arrow (acute spike + sharp reflex notch),
// and a 5-point star (5 sharp reflex corners) we require BOTH:
//   - no ring seed lands inside `inner` (none crossed the band), and
//   - no ring cell is sub-pixel.
func TestRingSeedsInsetReflexCornersStayInBand(t *testing.T) {
	const cellSize float32 = 1.0
	star := make([]Point2, 0, 10)
	for i := range 10 {
		ang := 2 * math.Pi * float64(i) / 10
		r := float32(14)
		if i%2 == 1 {
			r = 5
		}
		star = append(star, Point2{r * float32(math.Cos(ang)), r * float32(math.Sin(ang))})
	}
	cases := []struct {
		name  string
		loops []Loop
	}{
		{"L_shape", []Loop{makeLoop([]Point2{{0, 0}, {20, 0}, {20, 10}, {10, 10}, {10, 20}, {0, 20}}, false)}},
		{"plus", []Loop{makeLoop([]Point2{
			{10, 0}, {20, 0}, {20, 10}, {30, 10}, {30, 20}, {20, 20},
			{20, 30}, {10, 30}, {10, 20}, {0, 20}, {0, 10}, {10, 10},
		}, false)}},
		{"arrow", []Loop{makeLoop([]Point2{{0, 0}, {30, 8}, {0, 16}, {8, 8}}, false)}},
		{"star", []Loop{makeLoop(star, false)}},
	}

	px := (cellSize / 4) * (cellSize / 4)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := ComputeFootprint(tc.loops, tc.loops)
			inner := OffsetFootprint(fp, -cellSize)
			cells, _, _ := PartitionSlabAnalytic(fp, nil, nil, cellSize)

			// No ring seed may cross the band into the hex (inner) region.
			ringInset := 0.5 * cellSize
			for i := range fp.Loops {
				lp := &fp.Loops[i]
				for _, m := range walkLoopAtCellSize(lp, cellSize) {
					nrm := inwardNormal(lp, m)
					cand := Point2{m.point[0] + ringInset*nrm[0], m.point[1] + ringInset*nrm[1]}
					if fp.Contains(cand[0], cand[1]) && inner.Contains(cand[0], cand[1]) {
						t.Errorf("ring seed at (%.2f,%.2f) crossed into inner/hex territory — inset overshot the band", cand[0], cand[1])
					}
				}
			}
			// No ring cell may be sub-pixel.
			nRing := 0
			for i := range cells {
				if cells[i].Kind != KindRing {
					continue
				}
				nRing++
				if a := absf32(signedArea(cells[i].Outer)); a < px {
					t.Errorf("ring cell %d area %.5f mm² is sub-pixel (px=%.5f) — inset produced a sliver", i, a, px)
				}
			}
			if nRing == 0 {
				t.Fatal("no ring cells produced")
			}
		})
	}
}
