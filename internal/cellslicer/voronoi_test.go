package cellslicer

import (
	"math"
	"testing"
)

// makeLoop builds a Loop with SignedArea/IsHole populated so
// loopToClipperPath orients it correctly.
func makeLoop(pts []Point2, isHole bool) Loop {
	return Loop{Points: pts, SignedArea: signedArea(pts), IsHole: isHole}
}

// footprintArea sums the signed areas of a footprint's loops (CCW outer
// positive, CW holes negative), giving the net covered area in mm².
func footprintArea(fp *Footprint) float64 {
	var a float64
	for i := range fp.Loops {
		a += float64(signedArea(fp.Loops[i].Points))
	}
	if a < 0 {
		a = -a
	}
	return a
}

// TestVoronoiBandCellsTilesExactly is the correctness guard for the
// local-box optimisation in voronoiBandCells: if the local box is ever
// too small, a cell gets truncated and the band is left with a gap, so
// the cells' total area drops below the band's. It also asserts the
// cells never overlap. Together these mean the cells tile the band
// exactly — the property the whole Voronoi partition relies on.
func TestVoronoiBandCellsTilesExactly(t *testing.T) {
	const cellSize float32 = 1.0
	cases := []struct {
		name  string
		loops []Loop
	}{
		{
			// Pure wall case (the one the global-box bug broke): a plain
			// square, band = full cellSize ring, no interior seeds.
			name: "square",
			loops: []Loop{
				makeLoop([]Point2{{0, 0}, {20, 0}, {20, 20}, {0, 20}}, false),
			},
		},
		{
			// Square with a square hole: exercises a second (hole) loop
			// and the band around it.
			name: "square_with_hole",
			loops: []Loop{
				makeLoop([]Point2{{0, 0}, {20, 0}, {20, 20}, {0, 20}}, false),
				makeLoop([]Point2{{8, 8}, {8, 12}, {12, 12}, {12, 8}}, true),
			},
		},
		{
			// Thin rectangle (width 3*cellSize): band from both long
			// edges nearly fills it, stressing how far a cell reaches.
			name: "thin_strip",
			loops: []Loop{
				makeLoop([]Point2{{0, 0}, {30, 0}, {30, 3}, {0, 3}}, false),
			},
		},
		{
			// L-shape: one reflex (270°) corner. The inner offset miters
			// the band wider there, the worst case for how far a boundary
			// cell reaches from its seed — the real stress on localHalf.
			name: "L_shape",
			loops: []Loop{
				makeLoop([]Point2{{0, 0}, {20, 0}, {20, 10}, {10, 10}, {10, 20}, {0, 20}}, false),
			},
		},
		{
			// Plus/cross: four reflex corners.
			name: "plus",
			loops: []Loop{
				makeLoop([]Point2{
					{10, 0}, {20, 0}, {20, 10}, {30, 10}, {30, 20}, {20, 20},
					{20, 30}, {10, 30}, {10, 20}, {0, 20}, {0, 10}, {10, 10},
				}, false),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := ComputeFootprint(tc.loops, tc.loops)
			inner := OffsetFootprint(fp, -cellSize)
			band := FootprintDifference(fp, inner)
			if band == nil || len(band.Loops) == 0 {
				t.Fatal("empty band")
			}

			var seeds []Point2
			for i := range fp.Loops {
				for _, m := range walkLoopAtCellSize(&fp.Loops[i], cellSize) {
					seeds = append(seeds, m.point)
				}
			}
			pxArea := (cellSize / 4) * (cellSize / 4)
			cells := voronoiBandCells(seeds, band, cellSize, pxArea, KindRing)
			if len(cells) == 0 {
				t.Fatal("no cells produced")
			}

			// Coverage: the cells must tile the whole band. A truncated
			// cell (local box too small) would drop area below the band.
			bandArea := footprintArea(band)
			var cellArea float64
			for i := range cells {
				cellArea += math.Abs(float64(signedArea(cells[i].Outer)))
			}
			rel := math.Abs(cellArea-bandArea) / bandArea
			if rel > 0.005 {
				t.Errorf("cells cover %.4f mm² but band is %.4f mm² (%.2f%% off) — gap or truncation",
					cellArea, bandArea, 100*rel)
			}

			// No overlaps (tolerance matches the pipeline reporter).
			if ov := DetectCellOverlaps(cells, 0.05*cellSize*cellSize); len(ov) != 0 {
				t.Errorf("%d overlapping cell pairs, worst %.4f mm²", len(ov), ov[0].AreaMM2)
			}
		})
	}
}
