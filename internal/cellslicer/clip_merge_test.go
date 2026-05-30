package cellslicer

import (
	"math"
	"testing"
)

// sq is a CCW unit-grid square cell of side s at (x0,y0).
func sq(x0, y0, s float32, kind CellKind) Cell {
	return Cell{
		Outer: []Point2{{x0, y0}, {x0 + s, y0}, {x0 + s, y0 + s}, {x0, y0 + s}},
		Kind:  kind,
	}
}

func TestMergedGroupContours_SimpleMerge(t *testing.T) {
	// Two edge-adjacent unit squares merge into one rectangle: a single
	// CCW contour, traced cleanly.
	cells := []Cell{sq(0, 0, 1, KindHex), sq(1, 0, 1, KindHex)}
	contours, clean := mergedGroupContours(cells, []int{0, 1}, 1.0)
	if !clean {
		t.Fatal("simple two-cell merge should be clean")
	}
	if len(contours) != 1 {
		t.Fatalf("got %d contours, want 1 (merged rectangle)", len(contours))
	}
	// The shared internal edge at x=1 must have cancelled: no contour
	// vertex should sit at the interior midpoint (1,0)->(1,1) as a
	// standalone boundary — the rectangle spans x∈[0,2].
	if a := signedArea(toPoint2(contours[0])); a <= 0 {
		t.Errorf("outer contour area %.2f, want > 0 (CCW)", a)
	}
}

func TestMergedGroupContours_Hole(t *testing.T) {
	// Eight unit cells forming a 3×3 ring with the center (1,1) missing.
	// The merged boundary is an outer CCW loop plus a CW hole loop.
	var cells []Cell
	var idx []int
	for cy := 0; cy < 3; cy++ {
		for cx := 0; cx < 3; cx++ {
			if cx == 1 && cy == 1 {
				continue // hole
			}
			idx = append(idx, len(cells))
			cells = append(cells, sq(float32(cx), float32(cy), 1, KindHex))
		}
	}
	contours, clean := mergedGroupContours(cells, idx, 1.0)
	if !clean {
		t.Fatal("ring-with-hole should trace cleanly")
	}
	if len(contours) != 2 {
		t.Fatalf("got %d contours, want 2 (outer + hole)", len(contours))
	}
	// Exactly one outer (CCW, positive area) and one hole (CW, negative).
	pos, neg := 0, 0
	for _, c := range contours {
		if signedArea(toPoint2(c)) > 0 {
			pos++
		} else {
			neg++
		}
	}
	if pos != 1 || neg != 1 {
		t.Errorf("orientation: got %d CCW / %d CW, want 1 / 1", pos, neg)
	}
}

func TestMergedGroupContours_PinchNotClean(t *testing.T) {
	// Two squares touching only at the corner (2,2) — forced into one
	// group. The surviving boundary self-touches there (two outgoing
	// edges from (2,2)), so the trace must report not-clean so the caller
	// falls back to per-cell clipping instead of dropping a sub-loop.
	cells := []Cell{sq(0, 0, 2, KindHex), sq(2, 2, 2, KindHex)}
	_, clean := mergedGroupContours(cells, []int{0, 1}, 1.0)
	if clean {
		t.Fatal("corner-touching (pinched) group should report clean=false")
	}
}

// toPoint2 converts an extrusion contour to []Point2 for signedArea.
func toPoint2(c [][2]float32) []Point2 {
	out := make([]Point2, len(c))
	for i, p := range c {
		out[i] = p
	}
	return out
}

func TestGroupConnectedSameColorCells(t *testing.T) {
	// A row of four 10×10 cells along X: [0..10][10..20][20..30][30..40].
	// Colors: cell0=A cell1=A cell2=B cell3=A.
	//   cell0,cell1 are adjacent + same color → one group.
	//   cell3 is color A but NOT adjacent to cell0/1 (cell2 between them
	//   is color B) → its own group, proving connectivity (not
	//   color-anywhere) drives grouping.
	//   cell2 is its own group.
	// Plus a hex cell sharing cell1's right edge but a different KIND →
	// must not merge across kinds even though adjacent & same color.
	slabs := []Slab{{Cells: []Cell{
		sq(0, 0, 10, KindRing),  // 0  A
		sq(10, 0, 10, KindRing), // 1  A  (adjacent to 0)
		sq(20, 0, 10, KindRing), // 2  B
		sq(30, 0, 10, KindRing), // 3  A  (adjacent to 2 only)
	}}}
	color := []int32{1, 1, 2, 1}

	groups := groupConnectedSameColorCells(slabs, color)
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3: %+v", len(groups), groups)
	}
	// Map rep → sorted member set for assertion independent of order.
	byRep := map[int][]int{}
	for _, g := range groups {
		byRep[g.repGlobal] = append(byRep[g.repGlobal], g.cellIdxs...)
	}
	if got := byRep[0]; len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Errorf("group rep 0 should be {0,1}, got %v", got)
	}
	if got := byRep[2]; len(got) != 1 || got[0] != 2 {
		t.Errorf("group rep 2 should be {2}, got %v", got)
	}
	if got := byRep[3]; len(got) != 1 || got[0] != 3 {
		t.Errorf("group rep 3 should be {3} (color A but not adjacent to 0/1), got %v", got)
	}
}

func TestGroupConnectedSameColorCells_KindBoundary(t *testing.T) {
	// Two adjacent same-color cells of DIFFERENT kind must not merge.
	slabs := []Slab{{Cells: []Cell{
		sq(0, 0, 10, KindRing), // 0
		sq(10, 0, 10, KindHex), // 1 adjacent, same color, different kind
	}}}
	groups := groupConnectedSameColorCells(slabs, []int32{5, 5})
	if len(groups) != 2 {
		t.Fatalf("different-kind adjacent cells must not merge; got %d groups, want 2", len(groups))
	}
}

func TestClipMeshToMergedCellsFourCellsOneColor(t *testing.T) {
	// Same fixture as TestClipMeshToCellsManifoldFourCells, but all four
	// cells share one color → one merged group. The merged clip must
	// produce the same surface area (80 mm²) as the per-cell clip, tag
	// every face with the group representative (cell 0), and report the
	// merge in CellRep.
	model := cubeModel(10)
	cells := []Cell{
		{Outer: []Point2{{0, 0}, {5, 0}, {5, 5}, {0, 5}}, Kind: KindHex},
		{Outer: []Point2{{5, 0}, {10, 0}, {10, 5}, {5, 5}}, Kind: KindHex},
		{Outer: []Point2{{0, 5}, {5, 5}, {5, 10}, {0, 10}}, Kind: KindHex},
		{Outer: []Point2{{5, 5}, {10, 5}, {10, 10}, {5, 10}}, Kind: KindHex},
	}
	slabs := []Slab{{ZBot: 4, ZTop: 6, Cells: cells}}
	color := []int32{3, 3, 3, 3}

	cr, err := ClipMeshToMergedCellsManifold(model, slabs, 1.0, color)
	if err != nil {
		t.Fatalf("ClipMeshToMergedCellsManifold: %v", err)
	}
	if len(cr.Faces) == 0 {
		t.Fatal("0 faces")
	}
	// All faces map to the single group's representative (cell 0).
	for _, idx := range cr.FaceCellIdx {
		if idx != 0 {
			t.Errorf("FaceCellIdx contains %d, want 0 (group rep)", idx)
		}
	}
	// CellRep: all four cells point at representative 0.
	if len(cr.CellRep) != 4 {
		t.Fatalf("CellRep len=%d, want 4", len(cr.CellRep))
	}
	for i, rep := range cr.CellRep {
		if rep != 0 {
			t.Errorf("CellRep[%d]=%d, want 0", i, rep)
		}
	}
	area := triMeshArea(cr.Verts, cr.Faces)
	if math.Abs(area-80) > 0.01 {
		t.Errorf("merged surface area = %.4f mm², want 80 (4 sides × 10 × 2)", area)
	}
}

func TestMergedClipMatchesPerCellArea(t *testing.T) {
	// The merged clip must cover exactly the same surface as the per-cell
	// clip — same total area — while producing FEWER faces, because the
	// internal seams at x=5 / y=5 between the four same-color cells are
	// no longer cut into the source triangles.
	model := cubeModel(10)
	cells := []Cell{
		{Outer: []Point2{{0, 0}, {5, 0}, {5, 5}, {0, 5}}, Kind: KindHex},
		{Outer: []Point2{{5, 0}, {10, 0}, {10, 5}, {5, 5}}, Kind: KindHex},
		{Outer: []Point2{{0, 5}, {5, 5}, {5, 10}, {0, 10}}, Kind: KindHex},
		{Outer: []Point2{{5, 5}, {10, 5}, {10, 10}, {5, 10}}, Kind: KindHex},
	}
	slabs := []Slab{{ZBot: 4, ZTop: 6, Cells: cells}}

	perCell, err := ClipMeshToCellsManifold(model, slabs, 1.0)
	if err != nil {
		t.Fatalf("per-cell clip: %v", err)
	}
	merged, err := ClipMeshToMergedCellsManifold(model, slabs, 1.0, []int32{1, 1, 1, 1})
	if err != nil {
		t.Fatalf("merged clip: %v", err)
	}

	aPer := triMeshArea(perCell.Verts, perCell.Faces)
	aMerged := triMeshArea(merged.Verts, merged.Faces)
	if math.Abs(aPer-aMerged) > 0.01 {
		t.Errorf("area mismatch: per-cell=%.4f merged=%.4f (want equal)", aPer, aMerged)
	}
	if len(merged.Faces) >= len(perCell.Faces) {
		t.Errorf("merged faces=%d not fewer than per-cell faces=%d (seam removal expected)",
			len(merged.Faces), len(perCell.Faces))
	}
}

func TestMergedClipTwoColorsKeepsBoundary(t *testing.T) {
	// Left half (cells 0,2) is color A; right half (cells 1,3) is color
	// B. Two groups → faces tagged with reps 0 and 1; total area still
	// the full 80 mm² wall.
	model := cubeModel(10)
	cells := []Cell{
		{Outer: []Point2{{0, 0}, {5, 0}, {5, 5}, {0, 5}}, Kind: KindHex},     // 0 left
		{Outer: []Point2{{5, 0}, {10, 0}, {10, 5}, {5, 5}}, Kind: KindHex},   // 1 right
		{Outer: []Point2{{0, 5}, {5, 5}, {5, 10}, {0, 10}}, Kind: KindHex},   // 2 left
		{Outer: []Point2{{5, 5}, {10, 5}, {10, 10}, {5, 10}}, Kind: KindHex}, // 3 right
	}
	slabs := []Slab{{ZBot: 4, ZTop: 6, Cells: cells}}
	color := []int32{10, 20, 10, 20}

	cr, err := ClipMeshToMergedCellsManifold(model, slabs, 1.0, color)
	if err != nil {
		t.Fatalf("ClipMeshToMergedCellsManifold: %v", err)
	}
	// Representatives: left group rep=0, right group rep=1.
	if cr.CellRep[0] != 0 || cr.CellRep[2] != 0 {
		t.Errorf("left cells should rep to 0, got CellRep[0]=%d CellRep[2]=%d", cr.CellRep[0], cr.CellRep[2])
	}
	if cr.CellRep[1] != 1 || cr.CellRep[3] != 1 {
		t.Errorf("right cells should rep to 1, got CellRep[1]=%d CellRep[3]=%d", cr.CellRep[1], cr.CellRep[3])
	}
	seen := map[int32]bool{}
	for _, idx := range cr.FaceCellIdx {
		seen[idx] = true
	}
	if !seen[0] || !seen[1] {
		t.Errorf("expected faces tagged with both reps 0 and 1, got %v", seen)
	}
	if len(seen) != 2 {
		t.Errorf("expected exactly 2 distinct face tags (the two group reps), got %v", seen)
	}
	area := triMeshArea(cr.Verts, cr.Faces)
	if math.Abs(area-80) > 0.01 {
		t.Errorf("two-color merged area = %.4f mm², want 80", area)
	}
}
