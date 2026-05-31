package cellslicer

import (
	"math"
	"testing"
)

// TestCoverTargetExcludesHiddenInterior guards the surface-only contract
// as seen by the coverage diagnostic: PartitionSlabAnalytic's CoverTarget
// is the shell the cells actually tile (band ∪ innerCap), NOT the full
// footprint. A solid middle slab (same footprint above and below) must
// have a CoverTarget that is only the cellSize-wide wall band — its
// hidden interior must be excluded, or the HighlightUncovered overlay
// would redden the whole interior as a false gap (the bug this fixes).
func TestCoverTargetExcludesHiddenInterior(t *testing.T) {
	const cellSize float32 = 1.0
	square := []Loop{
		makeLoop([]Point2{{0, 0}, {20, 0}, {20, 20}, {0, 20}}, false),
	}
	fp := ComputeFootprint(square, square)
	fpArea := footprintArea(fp)

	t.Run("middle_slab", func(t *testing.T) {
		// Solid neighbours on both sides: nothing is a cap surface, so the
		// cover target is just the wall band, far smaller than the footprint.
		_, cover, _ := PartitionSlabAnalytic(fp, fp, fp, cellSize)
		if cover == nil {
			t.Fatal("nil cover target")
		}
		coverArea := footprintArea(cover)

		band := footprintArea(FootprintDifference(fp, OffsetFootprint(fp, -cellSize)))
		if rel := math.Abs(coverArea-band) / band; rel > 0.005 {
			t.Errorf("cover target %.3f mm² != wall band %.3f mm² (%.2f%% off)",
				coverArea, band, 100*rel)
		}
		// The interior (footprint minus band) must NOT be in the cover
		// target. For a 20×20 square with cellSize=1 the band is ~76 mm²
		// and the footprint 400 mm², so this is a wide, robust margin.
		if coverArea > 0.5*fpArea {
			t.Errorf("cover target %.3f mm² includes hidden interior (footprint %.3f mm²)",
				coverArea, fpArea)
		}
	})

	t.Run("cap_slab", func(t *testing.T) {
		// No neighbour above (top of the model): the whole face is a cap,
		// so the cover target is the entire footprint and the diagnostic
		// correctly expects full coverage there.
		_, cover, _ := PartitionSlabAnalytic(fp, fp, nil, cellSize)
		if cover == nil {
			t.Fatal("nil cover target")
		}
		coverArea := footprintArea(cover)
		if rel := math.Abs(coverArea-fpArea) / fpArea; rel > 0.005 {
			t.Errorf("cap-slab cover target %.3f mm² != footprint %.3f mm² (%.2f%% off)",
				coverArea, fpArea, 100*rel)
		}
	})
}
