package cellslicer

import (
	"testing"
)

// TestMarkOuterEdgesTwoAdjacentSquares: two cells sharing one edge.
// All non-shared edges (3 per cell) are on the partition outer
// boundary; the shared one is inner. Footprint covers exactly the
// union of both cells.
func TestMarkOuterEdgesTwoAdjacentSquares(t *testing.T) {
	// CCW unit square at [0,1]x[0,1] and an adjacent one at [1,2]x[0,1].
	cells := []Cell{
		{Outer: []Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}}},
		{Outer: []Point2{{1, 0}, {2, 0}, {2, 1}, {1, 1}}},
	}
	fp := &Footprint{Loops: []FootprintLoop{
		newLoop([]Point2{{0, 0}, {2, 0}, {2, 1}, {0, 1}}),
	}}
	MarkOuterEdges(cells, fp)
	// cell 0 edges: (0,0)→(1,0) outer; (1,0)→(1,1) INNER (shared);
	// (1,1)→(0,1) outer; (0,1)→(0,0) outer.
	wantA := []bool{true, false, true, true}
	if got := cells[0].OuterEdgeOnBoundary; !boolSliceEq(got, wantA) {
		t.Errorf("cell A flags = %v, want %v", got, wantA)
	}
	// cell 1 edges: (1,0)→(2,0) outer; (2,0)→(2,1) outer;
	// (2,1)→(1,1) outer; (1,1)→(1,0) INNER (shared reverse of A's).
	wantB := []bool{true, true, true, false}
	if got := cells[1].OuterEdgeOnBoundary; !boolSliceEq(got, wantB) {
		t.Errorf("cell B flags = %v, want %v", got, wantB)
	}
}

// TestMarkOuterEdgesPartitionGap: two cells separated by a 1mm gap.
// Footprint covers both cells AND the gap. The cells' inward-facing
// edges (toward the gap) are "no-mate" edges (no neighbour shares
// them) but they face INTO the footprint — the safety check should
// catch this and keep them clipping, since open-ending would let
// both cells double-claim the gap.
func TestMarkOuterEdgesPartitionGap(t *testing.T) {
	cells := []Cell{
		{Outer: []Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}}},
		{Outer: []Point2{{2, 0}, {3, 0}, {3, 1}, {2, 1}}},
	}
	// Footprint spans x=[0,3], y=[0,1] — covers cells and the gap.
	fp := &Footprint{Loops: []FootprintLoop{
		newLoop([]Point2{{0, 0}, {3, 0}, {3, 1}, {0, 1}}),
	}}
	MarkOuterEdges(cells, fp)
	// cell 0 edge (1,0)→(1,1) faces the gap — no mate but inside fp,
	// so safety check fires: NOT outer.
	wantA := []bool{true, false, true, true}
	if got := cells[0].OuterEdgeOnBoundary; !boolSliceEq(got, wantA) {
		t.Errorf("cell A flags = %v, want %v (gap-facing edge should NOT be flagged)", got, wantA)
	}
	// cell 1 edge (2,1)→(2,0) is the gap-facing direction equivalent...
	// wait, cell B has order (2,0)→(3,0)→(3,1)→(2,1), so edge (2,1)→(2,0)
	// is the LAST edge. It faces the gap — no mate but inside fp,
	// so safety check fires: NOT outer.
	wantB := []bool{true, true, true, false}
	if got := cells[1].OuterEdgeOnBoundary; !boolSliceEq(got, wantB) {
		t.Errorf("cell B flags = %v, want %v (gap-facing edge should NOT be flagged)", got, wantB)
	}
}

// TestMarkOuterEdgesSingleCell: one cell on its own. Every edge is
// outer (no possible neighbours), and they all face outside the
// footprint (which is exactly the cell).
func TestMarkOuterEdgesSingleCell(t *testing.T) {
	cells := []Cell{
		{Outer: []Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}}},
	}
	fp := &Footprint{Loops: []FootprintLoop{
		newLoop([]Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}}),
	}}
	MarkOuterEdges(cells, fp)
	want := []bool{true, true, true, true}
	if got := cells[0].OuterEdgeOnBoundary; !boolSliceEq(got, want) {
		t.Errorf("single-cell flags = %v, want %v", got, want)
	}
}

// TestMarkOuterEdgesNilFootprint: nil footprint disables the safety
// check. All no-mate edges are flagged outer regardless of geometry.
func TestMarkOuterEdgesNilFootprint(t *testing.T) {
	cells := []Cell{
		{Outer: []Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}}},
		{Outer: []Point2{{2, 0}, {3, 0}, {3, 1}, {2, 1}}},
	}
	MarkOuterEdges(cells, nil)
	// Without the safety check the gap-facing edges ARE flagged.
	wantA := []bool{true, true, true, true}
	wantB := []bool{true, true, true, true}
	if got := cells[0].OuterEdgeOnBoundary; !boolSliceEq(got, wantA) {
		t.Errorf("cell A flags = %v, want %v", got, wantA)
	}
	if got := cells[1].OuterEdgeOnBoundary; !boolSliceEq(got, wantB) {
		t.Errorf("cell B flags = %v, want %v", got, wantB)
	}
}

func boolSliceEq(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// newLoop builds a FootprintLoop with bbox precomputed.
func newLoop(pts []Point2) FootprintLoop {
	lp := FootprintLoop{Points: pts}
	lp.computeBbox()
	return lp
}
