package voxel

import "testing"

func TestCheckWatertight_Tetrahedron(t *testing.T) {
	// A closed tetrahedron: 4 triangles, every edge shared by exactly 2 faces.
	faces := [][3]uint32{
		{0, 1, 2},
		{0, 3, 1},
		{0, 2, 3},
		{1, 3, 2},
	}
	r := CheckWatertight(faces)
	if !r.IsWatertight() {
		t.Errorf("expected watertight tetrahedron, got %s", r)
	}
}

func TestCheckWatertight_OpenTriangle(t *testing.T) {
	// A single triangle has 3 boundary edges (no matching reverse).
	faces := [][3]uint32{
		{0, 1, 2},
	}
	r := CheckWatertight(faces)
	if r.IsWatertight() {
		t.Error("expected non-watertight for single triangle")
	}
	if len(r.BoundaryEdges) != 3 {
		t.Errorf("expected 3 boundary edges, got %d", len(r.BoundaryEdges))
	}
	if len(r.NonManifoldEdges) != 0 {
		t.Errorf("expected 0 non-manifold edges, got %d", len(r.NonManifoldEdges))
	}
}

func TestCheckWatertight_DuplicateFace(t *testing.T) {
	// Two identical triangles: each directed half-edge appears twice,
	// but there is no reverse edge. Should have both boundary and non-manifold.
	faces := [][3]uint32{
		{0, 1, 2},
		{0, 1, 2},
	}
	r := CheckWatertight(faces)
	if r.IsWatertight() {
		t.Error("expected non-watertight for duplicate faces")
	}
	if len(r.BoundaryEdges) != 3 {
		t.Errorf("expected 3 boundary edges (no reverse), got %d", len(r.BoundaryEdges))
	}
	// Dedup reports each undirected edge once (from the A<B side).
	// Edges: 0→1 (0<1, reported), 1→2 (1<2, reported), 2→0 (2>0, skipped).
	if len(r.NonManifoldEdges) != 2 {
		t.Errorf("expected 2 non-manifold edges (deduped), got %d", len(r.NonManifoldEdges))
	}
}

func TestCheckWatertight_NonManifold(t *testing.T) {
	// A tetrahedron with one face duplicated (opposite winding).
	// Edge 0→1 appears in the original and also as 1→0 in the duplicate,
	// giving the reverse edge count > 1.
	faces := [][3]uint32{
		{0, 1, 2},
		{0, 3, 1},
		{0, 2, 3},
		{1, 3, 2},
		{2, 1, 0}, // duplicate of face 0 with reversed winding
	}
	r := CheckWatertight(faces)
	if r.IsWatertight() {
		t.Error("expected non-watertight for non-manifold mesh")
	}
	if len(r.BoundaryEdges) != 0 {
		t.Errorf("expected 0 boundary edges, got %d", len(r.BoundaryEdges))
	}
	if len(r.NonManifoldEdges) == 0 {
		t.Error("expected non-manifold edges, got 0")
	}
}
