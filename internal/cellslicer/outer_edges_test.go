package cellslicer

import (
	"sync/atomic"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
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
	if got := cells[0].OuterEdgeOpen; !boolSliceEq(got, wantA) {
		t.Errorf("cell A flags = %v, want %v", got, wantA)
	}
	// cell 1 edges: (1,0)→(2,0) outer; (2,0)→(2,1) outer;
	// (2,1)→(1,1) outer; (1,1)→(1,0) INNER (shared reverse of A's).
	wantB := []bool{true, true, true, false}
	if got := cells[1].OuterEdgeOpen; !boolSliceEq(got, wantB) {
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
	if got := cells[0].OuterEdgeOpen; !boolSliceEq(got, wantA) {
		t.Errorf("cell A flags = %v, want %v (gap-facing edge should NOT be flagged)", got, wantA)
	}
	// cell 1 edge (2,1)→(2,0) is the gap-facing direction equivalent...
	// wait, cell B has order (2,0)→(3,0)→(3,1)→(2,1), so edge (2,1)→(2,0)
	// is the LAST edge. It faces the gap — no mate but inside fp,
	// so safety check fires: NOT outer.
	wantB := []bool{true, true, true, false}
	if got := cells[1].OuterEdgeOpen; !boolSliceEq(got, wantB) {
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
	if got := cells[0].OuterEdgeOpen; !boolSliceEq(got, want) {
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
	if got := cells[0].OuterEdgeOpen; !boolSliceEq(got, wantA) {
		t.Errorf("cell A flags = %v, want %v", got, wantA)
	}
	if got := cells[1].OuterEdgeOpen; !boolSliceEq(got, wantB) {
		t.Errorf("cell B flags = %v, want %v", got, wantB)
	}
}

// TestMarkOuterEdgesConvexPartitionCorner: two cells share one edge
// and meet at a convex corner of the partition outline. Both cells
// have one OPEN edge each that faces the partition exterior on that
// corner side. This setup is what the code review flagged as a
// potential geometric-duplication hazard: at clip time a slabPoly
// straddling the corner with vertices past both cells' open edges
// could be emitted twice (one per cell). The post-clip face dedup
// in ClipMeshToCells2D handles the resulting duplicates; this test
// pins the upstream tagging behaviour so a future Voronoi-owner
// fix changes the assertion here (fewer "open" tags at corner-
// adjacent edges) rather than failing silently.
func TestMarkOuterEdgesConvexPartitionCorner(t *testing.T) {
	// Two adjacent unit squares, [0,1]x[0,1] and [1,2]x[0,1].
	// Shared edge is x=1 between them. Footprint is exactly the
	// union — a [0,2]x[0,1] rectangle — so all non-shared edges
	// face true exterior (not a partition gap).
	cells := []Cell{
		{Outer: []Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}}},
		{Outer: []Point2{{1, 0}, {2, 0}, {2, 1}, {1, 1}}},
	}
	fp := &Footprint{Loops: []FootprintLoop{
		newLoop([]Point2{{0, 0}, {2, 0}, {2, 1}, {0, 1}}),
	}}
	MarkOuterEdges(cells, fp)
	// Cell A edges: 0=(0,0)→(1,0) open (y<0 exterior);
	//               1=(1,0)→(1,1) INNER (shared);
	//               2=(1,1)→(0,1) open (y>1 exterior);
	//               3=(0,1)→(0,0) open (x<0 exterior).
	wantA := []bool{true, false, true, true}
	if got := cells[0].OuterEdgeOpen; !boolSliceEq(got, wantA) {
		t.Errorf("cell A flags = %v, want %v", got, wantA)
	}
	// Cell B edges: 0=(1,0)→(2,0) open;
	//               1=(2,0)→(2,1) open;
	//               2=(2,1)→(1,1) open;
	//               3=(1,1)→(1,0) INNER (shared with A).
	wantB := []bool{true, true, true, false}
	if got := cells[1].OuterEdgeOpen; !boolSliceEq(got, wantB) {
		t.Errorf("cell B flags = %v, want %v", got, wantB)
	}
	// At the (1,0) convex corner of the partition outline cell A's
	// edge 0 and cell B's edge 0 both face -y exterior. A slabPoly
	// past (1, -ε) is in both half-spaces. Without the post-clip
	// face dedup these would produce duplicate emitted triangles —
	// captured here for the record so the dedup invariant has a
	// test attached to it.
	if !cells[0].OuterEdgeOpen[0] || !cells[1].OuterEdgeOpen[0] {
		t.Errorf("expected (1,0) convex-corner edges open on both cells; need post-clip face dedup to handle the duplication that follows")
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

// TestVerticalPathRiskCountResetsPerInvocation pins the fix for the
// reviewer-found bug: verticalPathRiskCount is a package-level
// atomic, so a stale value from a previous ClipMeshToCells2D call
// (or stray test in the same binary) would persist into the next
// call and the end-of-clip WARNING would no longer truthfully mean
// "this run tripped it". The reset must happen at the start of every
// invocation.
func TestVerticalPathRiskCountResetsPerInvocation(t *testing.T) {
	// Seed a stale counter value.
	atomic.StoreUint64(&verticalPathRiskCount, 42)
	// Call ClipMeshToCells2D on an empty model + zero slabs. The
	// body does almost nothing (no faces, no slabs) but its very
	// first action should reset the counter.
	emptyModel := &loader.LoadedModel{}
	_, err := ClipMeshToCells2D(emptyModel, nil, nil)
	if err != nil {
		t.Fatalf("ClipMeshToCells2D on empty input returned %v", err)
	}
	if got := atomic.LoadUint64(&verticalPathRiskCount); got != 0 {
		t.Errorf("verticalPathRiskCount = %d after empty ClipMeshToCells2D, want 0 (must reset at start of every call)", got)
	}
}
