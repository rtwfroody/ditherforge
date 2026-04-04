package voxel

import (
	"math"
	"testing"
)

func approxEqual(a, b [3]float32, eps float32) bool {
	for i := 0; i < 3; i++ {
		if float32(math.Abs(float64(a[i]-b[i]))) > eps {
			return false
		}
	}
	return true
}

func polyContains(poly [][3]float32, target [3]float32) bool {
	for _, v := range poly {
		if approxEqual(v, target, 1e-5) {
			return true
		}
	}
	return false
}

func TestClipPolygon_AllNeg(t *testing.T) {
	tri := [][3]float32{{-1, 0, 0}, {-2, 1, 0}, {-2, -1, 0}}
	neg, pos := ClipPolygonByPlane(tri, 0, 0)
	if len(neg) != 3 {
		t.Errorf("expected 3 neg vertices, got %d", len(neg))
	}
	if len(pos) != 0 {
		t.Errorf("expected 0 pos vertices, got %d", len(pos))
	}
}

func TestClipPolygon_AllPos(t *testing.T) {
	tri := [][3]float32{{1, 0, 0}, {2, 1, 0}, {2, -1, 0}}
	neg, pos := ClipPolygonByPlane(tri, 0, 0)
	if len(neg) != 0 {
		t.Errorf("expected 0 neg vertices, got %d", len(neg))
	}
	if len(pos) != 3 {
		t.Errorf("expected 3 pos vertices, got %d", len(pos))
	}
}

func TestClipPolygon_VertexOnPlane(t *testing.T) {
	// One vertex exactly on the plane (treated as neg side).
	tri := [][3]float32{{0, 0, 0}, {-1, 1, 0}, {-1, -1, 0}}
	neg, pos := ClipPolygonByPlane(tri, 0, 0)
	if len(neg) != 3 {
		t.Errorf("expected 3 neg vertices (vertex on plane is neg), got %d", len(neg))
	}
	if len(pos) != 0 {
		t.Errorf("expected 0 pos vertices, got %d", len(pos))
	}
}

func TestClipPolygon_Split(t *testing.T) {
	// Triangle straddling X=0: A(-1,0,0), B(1,1,0), C(1,-1,0)
	// Neg side should be a triangle (A, m1, m2), pos side a quad (m1, B, C, m2).
	tri := [][3]float32{{-1, 0, 0}, {1, 1, 0}, {1, -1, 0}}
	neg, pos := ClipPolygonByPlane(tri, 0, 0)
	if len(neg) != 3 {
		t.Errorf("expected 3 neg vertices, got %d", len(neg))
	}
	if len(pos) != 4 {
		t.Errorf("expected 4 pos vertices, got %d", len(pos))
	}
	// The neg polygon should contain A and two intersection points at x=0.
	if !polyContains(neg, [3]float32{-1, 0, 0}) {
		t.Error("neg should contain original vertex A")
	}
	for _, v := range neg {
		if v[0] > 0.001 {
			t.Errorf("neg vertex has x=%f > 0", v[0])
		}
	}
	for _, v := range pos {
		if v[0] < -0.001 {
			t.Errorf("pos vertex has x=%f < 0", v[0])
		}
	}
}

func TestClipPolygon_Quad(t *testing.T) {
	// Clip a quad (4 vertices) to verify beyond-triangle input.
	quad := [][3]float32{{-1, -1, 0}, {1, -1, 0}, {1, 1, 0}, {-1, 1, 0}}
	neg, pos := ClipPolygonByPlane(quad, 0, 0)
	if len(neg) != 4 {
		t.Errorf("expected 4 neg vertices, got %d", len(neg))
	}
	if len(pos) != 4 {
		t.Errorf("expected 4 pos vertices, got %d", len(pos))
	}
}

func TestClipPolygon_Degenerate(t *testing.T) {
	// 2 vertices — degenerate, should not panic.
	line := [][3]float32{{-1, 0, 0}, {1, 0, 0}}
	neg, pos := ClipPolygonByPlane(line, 0, 0)
	_ = neg
	_ = pos // just verify no panic
}

func TestClipPolygon_WindingPreserved(t *testing.T) {
	// Verify that the winding order is preserved by checking the cross product
	// sign is consistent between input and output.
	tri := [][3]float32{{-1, 0, 0}, {1, 1, 0}, {1, -1, 0}}
	neg, pos := ClipPolygonByPlane(tri, 0, 0)

	crossZ := func(poly [][3]float32) float64 {
		if len(poly) < 3 {
			return 0
		}
		var sum float64
		for i := 0; i < len(poly); i++ {
			a := poly[i]
			b := poly[(i+1)%len(poly)]
			sum += float64(a[0]*b[1] - b[0]*a[1])
		}
		return sum
	}

	inSign := crossZ(tri)
	negSign := crossZ(neg)
	posSign := crossZ(pos)

	if (inSign > 0) != (negSign > 0) {
		t.Error("neg polygon winding flipped")
	}
	if (inSign > 0) != (posSign > 0) {
		t.Error("pos polygon winding flipped")
	}
}
