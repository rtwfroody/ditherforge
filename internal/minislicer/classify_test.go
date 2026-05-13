package minislicer

import "testing"

// TestClassifyHolesConcaveOuter exercises the worst case: a
// crescent-shaped outer (concave) whose centroid falls inside its
// own cavity. The vertex-based classifier must still mark the
// crescent as outer (depth 0) and the cavity as a hole (depth 1).
func TestClassifyHolesConcaveOuter(t *testing.T) {
	// Outer: rectangle minus a notch on the right side. Its centroid
	// drifts toward the notch, but its vertices are unambiguous.
	outer := []Point2{
		{0, 0}, {10, 0}, {10, 4}, {6, 4}, {6, 6}, {10, 6}, {10, 10},
		{0, 10},
	}
	// Hole: a small square fully inside the outer.
	hole := []Point2{{2, 2}, {4, 2}, {4, 4}, {2, 4}}

	loops := []Loop{
		newTestLoop(outer),
		newTestLoop(hole),
	}
	classifyHoles(loops)
	if loops[0].IsHole {
		t.Errorf("outer loop misclassified as hole")
	}
	if !loops[1].IsHole {
		t.Errorf("hole loop misclassified as outer")
	}
}

// TestClassifyHolesTwoIslands confirms that two separate outer
// loops (e.g., the boat hull and the smokestack on the same Z
// slice) are both classified as outer, not as holes of each other.
func TestClassifyHolesTwoIslands(t *testing.T) {
	a := []Point2{{0, 0}, {2, 0}, {2, 2}, {0, 2}}
	b := []Point2{{10, 10}, {12, 10}, {12, 12}, {10, 12}}
	loops := []Loop{
		newTestLoop(a),
		newTestLoop(b),
	}
	classifyHoles(loops)
	if loops[0].IsHole || loops[1].IsHole {
		t.Errorf("two disjoint islands should both be outer; got %v %v",
			loops[0].IsHole, loops[1].IsHole)
	}
}

// TestClassifyHolesPerOuterChild verifies HasHoleChild: in a layer
// with two separate outers where only one contains a hole, that
// outer alone has HasHoleChild=true (so the smokestack-with-no-cavity
// outer next to a hull-with-cavity outer still emits its cap fan).
func TestClassifyHolesPerOuterChild(t *testing.T) {
	hullOuter := []Point2{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	hullHole := []Point2{{2, 2}, {8, 2}, {8, 8}, {2, 8}}
	smokestack := []Point2{{20, 0}, {22, 0}, {22, 2}, {20, 2}}
	loops := []Loop{
		newTestLoop(hullOuter),
		newTestLoop(hullHole),
		newTestLoop(smokestack),
	}
	classifyHoles(loops)
	if !loops[0].HasHoleChild {
		t.Errorf("hull outer should have HasHoleChild=true")
	}
	if loops[1].HasHoleChild {
		t.Errorf("hole shouldn't have HasHoleChild")
	}
	if loops[2].HasHoleChild {
		t.Errorf("smokestack outer (no cavity) should have HasHoleChild=false")
	}
}

// TestClassifyHolesNestedTwice verifies even-odd: outer (depth 0,
// outer) → hole (depth 1, hole) → island (depth 2, outer again).
func TestClassifyHolesNestedTwice(t *testing.T) {
	outer := []Point2{{0, 0}, {20, 0}, {20, 20}, {0, 20}}
	hole := []Point2{{4, 4}, {16, 4}, {16, 16}, {4, 16}}
	island := []Point2{{8, 8}, {12, 8}, {12, 12}, {8, 12}}
	loops := []Loop{
		newTestLoop(outer),
		newTestLoop(hole),
		newTestLoop(island),
	}
	classifyHoles(loops)
	if loops[0].IsHole {
		t.Errorf("outer should be outer; got hole")
	}
	if !loops[1].IsHole {
		t.Errorf("hole should be hole; got outer")
	}
	if loops[2].IsHole {
		t.Errorf("island (depth 2) should be outer; got hole")
	}
}

// newTestLoop builds a Loop for tests: sets Points + SignedArea +
// bbox so Loop.Contains works without a separate setup step.
func newTestLoop(pts []Point2) Loop {
	l := Loop{Points: pts}
	l.RefreshDerived()
	return l
}
