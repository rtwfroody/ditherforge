package cellslicer

import (
	"math"
	"testing"
)

// squareCell returns a cell that is an axis-aligned square of the given
// side, so its polygon area is side².
func squareCell(side float32) Cell {
	return Cell{Outer: []Point2{{0, 0}, {side, 0}, {side, side}, {0, side}}}
}

func TestMedianCellAreaMM2(t *testing.T) {
	// Odd count: areas 1, 4, 9 → median 4.
	odd := Slab{Cells: []Cell{squareCell(1), squareCell(2), squareCell(3)}}
	if got := odd.MedianCellAreaMM2(); math.Abs(float64(got-4)) > 1e-4 {
		t.Errorf("odd median = %.4f, want 4", got)
	}

	// Even count: areas 1, 4 → median (1+4)/2 = 2.5. Unsorted input must
	// still sort first.
	even := Slab{Cells: []Cell{squareCell(2), squareCell(1)}}
	if got := even.MedianCellAreaMM2(); math.Abs(float64(got-2.5)) > 1e-4 {
		t.Errorf("even median = %.4f, want 2.5", got)
	}

	// Degenerate polygons (<3 points) are skipped, not counted as zero.
	mixed := Slab{Cells: []Cell{
		squareCell(3),
		{Outer: []Point2{{0, 0}, {1, 0}}}, // 2 points: ignored
	}}
	if got := mixed.MedianCellAreaMM2(); math.Abs(float64(got-9)) > 1e-4 {
		t.Errorf("median with a degenerate cell = %.4f, want 9", got)
	}

	// No cells with a valid polygon → 0.
	var empty Slab
	if got := empty.MedianCellAreaMM2(); got != 0 {
		t.Errorf("empty slab median = %.4f, want 0", got)
	}
}
