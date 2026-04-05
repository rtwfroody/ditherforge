package voxel

import (
	"context"
	"testing"
)

// A closed box: 8 vertices, 12 triangles (2 per face).
func makeBox() ([][3]float32, [][3]uint32) {
	verts := [][3]float32{
		{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0}, // bottom
		{0, 0, 1}, {1, 0, 1}, {1, 1, 1}, {0, 1, 1}, // top
	}
	faces := [][3]uint32{
		// bottom (-Z)
		{0, 2, 1}, {0, 3, 2},
		// top (+Z)
		{4, 5, 6}, {4, 6, 7},
		// front (-Y)
		{0, 1, 5}, {0, 5, 4},
		// back (+Y)
		{2, 3, 7}, {2, 7, 6},
		// left (-X)
		{0, 4, 7}, {0, 7, 3},
		// right (+X)
		{1, 2, 6}, {1, 6, 5},
	}
	return verts, faces
}

func TestDecimate_TargetAboveInput(t *testing.T) {
	verts, faces := makeBox()
	outVerts, outFaces, err := Decimate(context.Background(), verts, faces, 100, 1.0)
	if err != nil {
		t.Fatalf("Decimate: %v", err)
	}
	if len(outFaces) != len(faces) {
		t.Errorf("expected %d faces (unchanged), got %d", len(faces), len(outFaces))
	}
	if len(outVerts) != len(verts) {
		t.Errorf("expected %d verts (unchanged), got %d", len(verts), len(outVerts))
	}
}

func TestDecimate_PreservesWatertight(t *testing.T) {
	verts, faces := makeBox()
	r := CheckWatertight(faces)
	if !r.IsWatertight() {
		t.Fatalf("input box not watertight: %s", r)
	}

	// Decimate to fewer faces.
	_, outFaces, err := Decimate(context.Background(), verts, faces, 8, 1.0)
	if err != nil {
		t.Fatalf("Decimate: %v", err)
	}
	r = CheckWatertight(outFaces)
	if !r.IsWatertight() {
		t.Errorf("decimated box not watertight: %s", r)
	}
	if len(outFaces) > 12 {
		t.Errorf("expected <= 12 faces, got %d", len(outFaces))
	}
}

func TestDecimate_ReducesFaces(t *testing.T) {
	// Build a subdivided plane that can be aggressively decimated.
	// 4x4 grid of quads = 32 triangles.
	var verts [][3]float32
	n := 5
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			verts = append(verts, [3]float32{float32(x), float32(y), 0})
		}
	}
	var faces [][3]uint32
	for y := 0; y < n-1; y++ {
		for x := 0; x < n-1; x++ {
			i := uint32(y*n + x)
			faces = append(faces, [3]uint32{i, i + 1, i + uint32(n) + 1})
			faces = append(faces, [3]uint32{i, i + uint32(n) + 1, i + uint32(n)})
		}
	}

	_, outFaces, err := Decimate(context.Background(), verts, faces, 4, 10.0)
	if err != nil {
		t.Fatalf("Decimate: %v", err)
	}
	if len(outFaces) >= len(faces) {
		t.Errorf("expected fewer faces than %d, got %d", len(faces), len(outFaces))
	}
}

func TestDecimate_SingleTriangle(t *testing.T) {
	verts := [][3]float32{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}}
	faces := [][3]uint32{{0, 1, 2}}
	outVerts, outFaces, err := Decimate(context.Background(), verts, faces, 0, 1.0)
	if err != nil {
		t.Fatalf("Decimate: %v", err)
	}
	// Can't decimate below the minimum — should return something valid.
	if len(outFaces) == 0 && len(outVerts) == 0 {
		// Acceptable: fully collapsed.
		return
	}
	if len(outFaces) > 1 {
		t.Errorf("expected <= 1 face, got %d", len(outFaces))
	}
}
