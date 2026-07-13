package swatch

import (
	"math"
	"testing"
)

// TestClipTriToRect checks the triangle/rectangle clip area on simple cases.
func TestClipTriToRect(t *testing.T) {
	// Right triangle (0,0)(10,0)(0,10), area 50, fully inside a big rect.
	tri := [3][2]float64{{0, 0}, {10, 0}, {0, 10}}
	if a := clipTriToRect(tri, -1, 11, -1, 11); math.Abs(a-50) > 1e-9 {
		t.Errorf("full area = %v, want 50", a)
	}
	// Clip that triangle to the unit square [0,1]x[0,1]: the triangle covers the
	// whole square except the corner cut by the hypotenuse x+z=10, which does
	// not intersect the unit square, so the whole square (area 1) is inside.
	if a := clipTriToRect(tri, 0, 1, 0, 1); math.Abs(a-1) > 1e-9 {
		t.Errorf("unit-square clip = %v, want 1", a)
	}
	// Degenerate rectangle -> 0.
	if a := clipTriToRect(tri, 5, 5, 0, 10); a != 0 {
		t.Errorf("degenerate clip = %v, want 0", a)
	}
	// Non-overlapping rectangle -> 0.
	if a := clipTriToRect(tri, 20, 30, 20, 30); a != 0 {
		t.Errorf("disjoint clip = %v, want 0", a)
	}
}

// TestMeasureCoverageSynthetic builds a synthetic single-plate output mesh whose
// front face is entirely filament B and back face entirely filament A, and
// verifies MeasureCoverage reports front=1, back=0 for every section.
func TestMeasureCoverageSynthetic(t *testing.T) {
	pal := []Filament{{Hex: "#000000"}, {Hex: "#FFFFFF"}}
	plan := BuildPlan(pal, 1.0) // one plate, A=0 B=1, N=30
	plate := plan.Plates[0]

	// Two quads at the plate's front (Y=0, normal -Y = B) and back
	// (Y=ThickMM, normal +Y = A) planes, each spanning the full 30x30 face.
	y0 := float32(plate.YOffsetMM)
	y1 := y0 + float32(ThickMM)
	verts := [][3]float32{
		{0, y0, 0}, {PlateMM, y0, 0}, {PlateMM, y0, PlateMM}, {0, y0, PlateMM}, // front 0..3
		{0, y1, 0}, {PlateMM, y1, 0}, {PlateMM, y1, PlateMM}, {0, y1, PlateMM}, // back 4..7
	}
	// Front winding for outward -Y normal: (0,1,2),(0,2,3) gives -Y (see BuildMesh).
	// Back winding for +Y normal: reverse.
	faces := [][3]uint32{
		{0, 1, 2}, {0, 2, 3}, // front, B
		{4, 6, 5}, {4, 7, 6}, // back, A
	}
	assign := []int32{int32(plate.B), int32(plate.B), int32(plate.A), int32(plate.A)}

	front, back := MeasureCoverage(plan, verts, faces, assign)
	for s := 0; s < Sections; s++ {
		if math.Abs(front[0][s]-1) > 1e-9 {
			t.Errorf("front section %d coverage = %v, want 1", s, front[0][s])
		}
		if math.Abs(back[0][s]-0) > 1e-9 {
			t.Errorf("back section %d coverage = %v, want 0", s, back[0][s])
		}
	}
}
