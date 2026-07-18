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
	plan := BuildPlan(pal, 1.0, 0.25, 0.20) // one plate, A=0 B=1
	plate := plan.Plates[0]

	// Two quads at the plate's front (Y=0, normal -Y = B) and back
	// (Y=ThickMM, normal +Y = A) planes, each spanning the full 90x10 face.
	const w, h = PlateWidthMM, PlateHeightMM
	y0 := float32(plate.YOffsetMM)
	y1 := y0 + float32(ThickMM)
	verts := [][3]float32{
		{0, y0, 0}, {w, y0, 0}, {w, y0, h}, {0, y0, h}, // front 0..3
		{0, y1, 0}, {w, y1, 0}, {w, y1, h}, {0, y1, h}, // back 4..7
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
		if math.Abs(front[0][s].B-1) > 1e-9 {
			t.Errorf("front section %d B = %v, want 1", s, front[0][s].B)
		}
		if math.Abs(back[0][s].B-0) > 1e-9 {
			t.Errorf("back section %d B = %v, want 0", s, back[0][s].B)
		}
		if front[0][s].Foreign != 0 || back[0][s].Foreign != 0 {
			t.Errorf("section %d foreign = (%v,%v), want 0", s, front[0][s].Foreign, back[0][s].Foreign)
		}
	}
}

// TestForeignDetectionAndSnap builds a synthetic plate whose front face is
// painted a THIRD color (not the plate's A or B), asserts MeasureCoverage
// surfaces it as foreign, and asserts SnapToPairs rewrites it to a pair member
// (removing the foreign coverage). This is the detector the manifest relies on
// and the fix for the real White+Orange->Beige contamination.
func TestForeignDetectionAndSnap(t *testing.T) {
	// Palette engineered like the repro: a plate of ColdWhite(A)+Orange(B) whose
	// blend lands on Beige, a third palette entry.
	pal := []Filament{
		{Hex: "#080A0D", Label: "Black"},
		{Hex: "#C2AB72", Label: "Beige"},
		{Hex: "#D9DFE5", Label: "ColdWhite"},
		{Hex: "#F67405", Label: "Orange"},
	}
	plan := BuildPlan(pal, 1.0, 0.25, 0.20)
	// Use the ColdWhite(2)+Orange(3) plate.
	var plate Plate
	var plateIdx int
	for i, pl := range plan.Plates {
		if pl.A == 2 && pl.B == 3 {
			plate, plateIdx = pl, i
		}
	}
	const w, h = PlateWidthMM, PlateHeightMM
	y0 := float32(plate.YOffsetMM)
	y1 := y0 + float32(ThickMM)
	verts := [][3]float32{
		{0, y0, 0}, {w, y0, 0}, {w, y0, h}, {0, y0, h}, // front 0..3
		{0, y1, 0}, {w, y1, 0}, {w, y1, h}, {0, y1, h}, // back 4..7
	}
	faces := [][3]uint32{
		{0, 1, 2}, {0, 2, 3}, // front -> Beige (foreign, index 1)
		{4, 6, 5}, {4, 7, 6}, // back  -> Orange (B, index 3)
	}
	const beige = int32(1)
	assign := []int32{beige, beige, int32(plate.B), int32(plate.B)}

	front, _ := MeasureCoverage(plan, verts, faces, assign)
	for s := 0; s < Sections; s++ {
		if math.Abs(front[plateIdx][s].Foreign-1) > 1e-9 {
			t.Fatalf("section %d foreign = %v, want 1 (front is all Beige)", s, front[plateIdx][s].Foreign)
		}
	}

	// SnapToPairs must rewrite the Beige front faces to a pair member so no
	// foreign remains.
	snapped := SnapToPairs(plan, verts, faces, assign)
	for i, a := range snapped {
		if a != int32(plate.A) && a != int32(plate.B) {
			t.Errorf("face %d still foreign after snap: %d", i, a)
		}
	}
	frontS, _ := MeasureCoverage(plan, verts, faces, snapped)
	for s := 0; s < Sections; s++ {
		if frontS[plateIdx][s].Foreign != 0 {
			t.Errorf("section %d foreign after snap = %v, want 0", s, frontS[plateIdx][s].Foreign)
		}
	}
}
