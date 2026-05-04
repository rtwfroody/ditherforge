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
	outVerts, outFaces, err := Decimate(context.Background(), verts, faces, 100, 1.0, 0, nil)
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
	_, outFaces, err := Decimate(context.Background(), verts, faces, 8, 1.0, 0, nil)
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

	_, outFaces, err := Decimate(context.Background(), verts, faces, 4, 10.0, 0, nil)
	if err != nil {
		t.Fatalf("Decimate: %v", err)
	}
	if len(outFaces) >= len(faces) {
		t.Errorf("expected fewer faces than %d, got %d", len(faces), len(outFaces))
	}
}

// TestDecimate_NonManifoldEdgePreserved guards against a regression where
// edges shared by 3+ faces would get collapsed by the cost-budget loop,
// dropping every face on the edge at once and turning the surviving
// wing-edges into open boundaries. Real-world STLs (e.g. 3DBenchy) carry
// hundreds of non-manifold edges; the symptom was holes on the hull
// where surrounding triangles disappeared.
//
// Construction: two tetrahedra glued along edge (v0, v1). Both tets are
// closed in their own right, so every edge in the mesh has count==2
// EXCEPT (v0, v1) which has count==4 (two faces from each tet). The
// pre-fix `count==1` rule would not have locked v0/v1 since none of
// their other edges are open boundaries — only the new `count!=2` rule
// catches this.
func TestDecimate_NonManifoldEdgePreserved(t *testing.T) {
	verts := [][3]float32{
		{0, 0, 0},       // v0 — shared edge endpoint
		{1, 0, 0},       // v1 — shared edge endpoint
		{0.5, 1, 0.1},   // v2 — Tet A apex
		{0.5, 0.5, 1},   // v3 — Tet A apex
		{0.5, -1, -0.1}, // v4 — Tet B apex
		{0.5, -0.5, 1},  // v5 — Tet B apex
	}
	// Tet A: outward normals via right-hand rule.
	// Tet B: mirror across the y=0 plane (winding flipped).
	faces := [][3]uint32{
		// Tet A
		{0, 1, 2},
		{0, 2, 3},
		{0, 3, 1},
		{1, 3, 2},
		// Tet B
		{0, 4, 1},
		{0, 5, 4},
		{0, 1, 5},
		{1, 4, 5},
	}

	// Sanity: pre-decimation, edge (v0, v1) should be non-manifold (4
	// faces) and every other edge should be manifold (2 faces).
	wr := CheckWatertight(faces)
	if len(wr.BoundaryEdges) != 0 {
		t.Fatalf("test setup wrong: input has %d boundary edges, want 0", len(wr.BoundaryEdges))
	}
	if len(wr.NonManifoldEdges) == 0 {
		t.Fatalf("test setup wrong: input has no non-manifold edges; expected (v0, v1) to be non-manifold")
	}

	// Aggressive errorBudget so the cost-budget loop has license to
	// collapse anything topology allows. Without the non-manifold lock,
	// the (v0, v1) edge is short and would be popped early; collapsing
	// it drops all 4 faces touching the edge at once and the wing-edges
	// become open boundaries.
	_, outFaces, err := Decimate(context.Background(), verts, faces, 1, 10.0, 100.0, nil)
	if err != nil {
		t.Fatalf("Decimate: %v", err)
	}
	if len(outFaces) == 0 {
		// Without the lock, an unconstrained heap walk can chase the
		// non-manifold collapse and the cascading degeneracies until
		// every face is gone — also a "no holes" result, but for the
		// wrong reason. Catch it explicitly.
		t.Fatal("decimation produced empty mesh; non-manifold collapse cascade likely")
	}
	wr = CheckWatertight(outFaces)
	if len(wr.BoundaryEdges) != 0 {
		t.Errorf("decimation introduced %d boundary edges (non-manifold edge was collapsed): %s",
			len(wr.BoundaryEdges), wr)
	}
}

func TestDecimate_SingleTriangle(t *testing.T) {
	verts := [][3]float32{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}}
	faces := [][3]uint32{{0, 1, 2}}
	outVerts, outFaces, err := Decimate(context.Background(), verts, faces, 0, 1.0, 0, nil)
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
