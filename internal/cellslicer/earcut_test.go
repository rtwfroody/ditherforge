package cellslicer

import (
	"math"
	"testing"
)

// triArea returns twice the signed area of the triangle with the
// given vertex array indices into verts.
func triArea(verts []Point2, t [3]uint32) float64 {
	a := verts[t[0]]
	b := verts[t[1]]
	c := verts[t[2]]
	return float64((b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0]))
}

func sumTriArea(verts []Point2, tris [][3]uint32) float64 {
	var s float64
	for _, t := range tris {
		s += triArea(verts, t)
	}
	return s
}

// TestEarcutSquare triangulates a unit square. Expected: 2
// triangles, combined area = 1.
func TestEarcutSquare(t *testing.T) {
	outer := []Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	verts, tris := Earcut(outer, nil)
	if len(tris) != 2 {
		t.Fatalf("got %d triangles, want 2", len(tris))
	}
	got := sumTriArea(verts, tris) / 2
	if math.Abs(got-1.0) > 1e-6 {
		t.Errorf("total area = %g, want 1.0", got)
	}
}

// TestEarcutConcave triangulates an L-shaped polygon.
func TestEarcutConcave(t *testing.T) {
	// L-shape: 6 vertices, area = 3.
	outer := []Point2{{0, 0}, {2, 0}, {2, 1}, {1, 1}, {1, 2}, {0, 2}}
	verts, tris := Earcut(outer, nil)
	for i, tr := range tris {
		t.Logf("  tri %d: %v %v %v  triArea=%g",
			i, verts[tr[0]], verts[tr[1]], verts[tr[2]],
			triArea(verts, tr))
	}
	if len(tris) != 4 {
		t.Errorf("got %d triangles, want 4 (n-2 for 6-vertex polygon)", len(tris))
	}
	got := sumTriArea(verts, tris) / 2
	if math.Abs(got-3.0) > 1e-6 {
		t.Errorf("L area = %g, want 3.0", got)
	}
}

// TestEarcutRectWithHole: a 10x10 rect with a 4x4 hole in the
// middle. Expected area = 100 - 16 = 84.
func TestEarcutRectWithHole(t *testing.T) {
	outer := []Point2{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	hole := []Point2{{3, 3}, {3, 7}, {7, 7}, {7, 3}} // CW
	verts, tris := Earcut(outer, [][]Point2{hole})
	if len(tris) == 0 {
		t.Fatalf("got 0 triangles")
	}
	got := sumTriArea(verts, tris) / 2
	if math.Abs(got-84.0) > 1e-6 {
		t.Errorf("rect-minus-hole area = %g, want 84.0 (tris=%d)", got, len(tris))
	}
}

// TestEarcutTwoHoles: a 20x20 rect with two 2x2 holes.
// Expected area = 400 - 4 - 4 = 392.
func TestEarcutTwoHoles(t *testing.T) {
	outer := []Point2{{0, 0}, {20, 0}, {20, 20}, {0, 20}}
	hole1 := []Point2{{4, 4}, {4, 6}, {6, 6}, {6, 4}} // CW
	hole2 := []Point2{{14, 14}, {14, 16}, {16, 16}, {16, 14}} // CW
	verts, tris := Earcut(outer, [][]Point2{hole1, hole2})
	got := sumTriArea(verts, tris) / 2
	if math.Abs(got-392.0) > 0.01 {
		t.Errorf("two-hole area = %g, want 392.0 (tris=%d)", got, len(tris))
	}
}

// TestEarcutHoleConvertedOrientation: outer passed CW, hole passed
// CCW. Earcut should normalize internally and still produce a
// correct triangulation.
func TestEarcutOrientationNormalize(t *testing.T) {
	outerCW := []Point2{{0, 0}, {0, 10}, {10, 10}, {10, 0}}
	holeCCW := []Point2{{3, 3}, {7, 3}, {7, 7}, {3, 7}}
	verts, tris := Earcut(outerCW, [][]Point2{holeCCW})
	got := sumTriArea(verts, tris) / 2
	if math.Abs(got-84.0) > 1e-6 {
		t.Errorf("normalized area = %g, want 84.0", got)
	}
}
