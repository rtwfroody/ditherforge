package voxel

import (
	"math"
	"testing"
)

func areaApproxEqual(t *testing.T, name string, got, want float32) {
	t.Helper()
	tol := float32(1e-4)
	if math.Abs(float64(got-want)) > float64(tol) {
		t.Errorf("%s: got %.6f, want %.6f", name, got, want)
	}
}

// triangleAreaRef returns the unclipped area of triangle (a, b, c).
func triangleAreaRef(a, b, c [3]float32) float32 {
	ab := [3]float32{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
	ac := [3]float32{c[0] - a[0], c[1] - a[1], c[2] - a[2]}
	cx := ab[1]*ac[2] - ab[2]*ac[1]
	cy := ab[2]*ac[0] - ab[0]*ac[2]
	cz := ab[0]*ac[1] - ab[1]*ac[0]
	return 0.5 * float32(math.Sqrt(float64(cx*cx+cy*cy+cz*cz)))
}

func TestTriangleAABBClippedArea_FullyInside(t *testing.T) {
	// Small triangle fully inside a unit box.
	v0 := [3]float32{0.1, 0.1, 0.0}
	v1 := [3]float32{0.4, 0.1, 0.0}
	v2 := [3]float32{0.1, 0.5, 0.0}
	center := [3]float32{0, 0, 0}
	half := [3]float32{0.5, 0.5, 0.5}
	got := TriangleAABBClippedArea(v0, v1, v2, center, half)
	want := triangleAreaRef(v0, v1, v2)
	areaApproxEqual(t, "fully inside", got, want)
}

func TestTriangleAABBClippedArea_FullyOutside(t *testing.T) {
	v0 := [3]float32{2, 2, 0}
	v1 := [3]float32{3, 2, 0}
	v2 := [3]float32{2, 3, 0}
	center := [3]float32{0, 0, 0}
	half := [3]float32{0.5, 0.5, 0.5}
	got := TriangleAABBClippedArea(v0, v1, v2, center, half)
	areaApproxEqual(t, "fully outside", got, 0)
}

func TestTriangleAABBClippedArea_HalfClippedAxisAligned(t *testing.T) {
	// Right triangle in the z=0 plane with legs along +x and +y, total
	// area = 0.5 * 2 * 2 = 2. Box covers x in [-0.5, +0.5], y in [-0.5, +0.5]
	// (with origin at triangle's right-angle vertex), so clipped region is
	// the unit square cut by the hypotenuse y = -x + 2 — but the hypotenuse
	// runs from (2,0) to (0,2), well outside the box, so the clipped
	// region is the full half-unit square, area 0.25.
	v0 := [3]float32{0, 0, 0}
	v1 := [3]float32{2, 0, 0}
	v2 := [3]float32{0, 2, 0}
	center := [3]float32{0, 0, 0}
	half := [3]float32{0.5, 0.5, 0.5}
	got := TriangleAABBClippedArea(v0, v1, v2, center, half)
	// Clipped region: x in [0, 0.5], y in [0, 0.5] — wait, box covers
	// x in [-0.5, +0.5] (centered at 0). The triangle lives in x>=0,
	// y>=0, so the intersection is x in [0, 0.5], y in [0, 0.5]:
	// a 0.5×0.5 square = 0.25.
	areaApproxEqual(t, "axis-aligned clip", got, 0.25)
}

func TestTriangleAABBClippedArea_DiagonalClip(t *testing.T) {
	// Triangle in the z=0 plane: vertices at (-2, -2), (+2, -2), (0, +2).
	// Total area = 0.5 * 4 * 4 = 8.
	// Clipped to a unit box centered at origin (x,y in [-0.5, 0.5]):
	// the triangle covers all of y < -2 to y > 2; at y=-0.5 the triangle
	// spans x in [-1.5, 1.5] (truncated by box to [-0.5, 0.5]); at y=0.5
	// the triangle spans x in [-0.75, 0.75] (truncated to [-0.5, 0.5]).
	// So inside the box, the triangle covers the full box minus nothing
	// — no, wait. Let me reconsider.
	//
	// Triangle edges:
	//   left: from (-2,-2) to (0,2): x = -2 + (y+2)/2 (slope 1/2)
	//   right: from (2,-2) to (0,2): x = 2 - (y+2)/2 (slope -1/2)
	//   bottom: y = -2 (well below the box)
	//
	// At y in [-0.5, 0.5] the triangle's x-range is:
	//   left edge: x = -2 + (y+2)/2 = (y-2)/2 → at y=-0.5: -1.25, y=0.5: -0.75
	//   right edge: x = 2 - (y+2)/2 = (2-y)/2 → at y=-0.5: 1.25, y=0.5: 0.75
	// Both bounds are |x| > 0.5 throughout y in [-0.5, 0.5], so the triangle
	// entirely covers the box's y-strip. The clipped region is the full
	// 1×1 unit square = 1.0.
	v0 := [3]float32{-2, -2, 0}
	v1 := [3]float32{2, -2, 0}
	v2 := [3]float32{0, 2, 0}
	center := [3]float32{0, 0, 0}
	half := [3]float32{0.5, 0.5, 0.5}
	got := TriangleAABBClippedArea(v0, v1, v2, center, half)
	areaApproxEqual(t, "diagonal clip (box fully inside triangle)", got, 1.0)
}

func TestTriangleAABBClippedArea_TiltedTriangle(t *testing.T) {
	// Triangle tilted in 3D — lies in the plane z = x. Vertices:
	// (-1, -1, -1), (1, -1, 1), (0, 1, 0). Area is independent of
	// the box clip orientation.
	// We'll clip with a large box that fully contains the triangle —
	// result must equal the triangle's full area.
	v0 := [3]float32{-1, -1, -1}
	v1 := [3]float32{1, -1, 1}
	v2 := [3]float32{0, 1, 0}
	center := [3]float32{0, 0, 0}
	half := [3]float32{10, 10, 10}
	got := TriangleAABBClippedArea(v0, v1, v2, center, half)
	want := triangleAreaRef(v0, v1, v2)
	areaApproxEqual(t, "tilted, fully contained", got, want)
}

func TestTriangleAABBClippedArea_TiltedTriangleClipped(t *testing.T) {
	// Triangle in the plane z = x, vertices at (-2, -2, -2), (2, -2, 2),
	// (0, 2, 0). The plane intersects a unit box centered at the origin
	// in a tilted rectangle whose footprint in box-local (x, y) is
	// the full y-range [-0.5, 0.5] paired with the box-local x-range
	// where |x| ≤ 0.5 AND |z|=|x| ≤ 0.5 — i.e., x ∈ [-0.5, 0.5].
	//
	// The triangle's projection onto the (x, y) plane has edges:
	//   left:  from (-2, -2) to (0, 2): x = (y - 2) / 2
	//   right: from ( 2, -2) to (0, 2): x = (2 - y) / 2
	//   bottom: y = -2
	// Inside box y ∈ [-0.5, 0.5], the left edge x ranges from -1.25 to
	// -0.75 (all < -0.5), the right edge from 1.25 to 0.75 (all > 0.5).
	// So the clipped region in the (x, y) plane is the box-y-strip
	// rectangle [-0.5, 0.5] × [-0.5, 0.5].
	//
	// Because the triangle lies in the plane z = x, the clipped polygon
	// in 3D is that same (x, y) rectangle lifted onto z = x — a tilted
	// rectangle. Its sides are (1, 0, 1) (length √2) and (0, 1, 0)
	// (length 1), so area = √2.
	v0 := [3]float32{-2, -2, -2}
	v1 := [3]float32{2, -2, 2}
	v2 := [3]float32{0, 2, 0}
	center := [3]float32{0, 0, 0}
	half := [3]float32{0.5, 0.5, 0.5}
	got := TriangleAABBClippedArea(v0, v1, v2, center, half)
	want := float32(math.Sqrt2)
	areaApproxEqual(t, "tilted, clipped to tilted rectangle", got, want)
}

func TestTriangleAABBClippedArea_DegenerateOnFace(t *testing.T) {
	// Triangle lying flat on a box face (z = +halfExtent[2]).
	// Should report the full triangle area (the boundary-coincident
	// case is documented as double-counted but well-defined).
	v0 := [3]float32{0.0, 0.0, 0.5}
	v1 := [3]float32{0.3, 0.0, 0.5}
	v2 := [3]float32{0.0, 0.3, 0.5}
	center := [3]float32{0, 0, 0}
	half := [3]float32{0.5, 0.5, 0.5}
	got := TriangleAABBClippedArea(v0, v1, v2, center, half)
	want := triangleAreaRef(v0, v1, v2)
	areaApproxEqual(t, "triangle on face", got, want)
}

func TestTriangleAABBClippedArea_PointTouch(t *testing.T) {
	// Triangle with a single vertex grazing the box, rest outside.
	// Clipped polygon is degenerate (point or line), area = 0.
	v0 := [3]float32{0.5, 0.5, 0.5} // box corner
	v1 := [3]float32{2, 1, 0.5}
	v2 := [3]float32{1, 2, 0.5}
	center := [3]float32{0, 0, 0}
	half := [3]float32{0.5, 0.5, 0.5}
	got := TriangleAABBClippedArea(v0, v1, v2, center, half)
	areaApproxEqual(t, "point-touch", got, 0)
}

func TestTriangleAABBClippedArea_AgreesWithOverlap(t *testing.T) {
	// Quick consistency check: when overlap test says false, area
	// should be 0. When true, area >= 0 (could be 0 for degenerate
	// boundary contact, but typically positive).
	tris := [][3][3]float32{
		{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}},
		{{-3, -3, 0}, {-2, -3, 0}, {-3, -2, 0}},
		{{0.4, 0.4, 0.4}, {0.45, 0.4, 0.4}, {0.4, 0.45, 0.4}},
		{{-0.6, 0, 0}, {0.6, 0, 0}, {0, 0.6, 0}},
	}
	center := [3]float32{0, 0, 0}
	half := [3]float32{0.5, 0.5, 0.5}
	for i, tri := range tris {
		overlap := TriangleAABBOverlap(tri[0], tri[1], tri[2], center, half)
		area := TriangleAABBClippedArea(tri[0], tri[1], tri[2], center, half)
		if !overlap && area > 1e-5 {
			t.Errorf("tri %d: overlap=false but area=%.6f > 0", i, area)
		}
		if area < 0 {
			t.Errorf("tri %d: negative area %.6f", i, area)
		}
	}
}
