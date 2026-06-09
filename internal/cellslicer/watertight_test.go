package cellslicer

// Watertight invariant helpers for the cellslicer clip output.
//
// countHoleEdges counts boundary / non-manifold edges keyed by 1µm
// quantised 3D position so coincident-position vertices that haven't
// been deduplicated still match. TestCountHoleEdges exercises the
// helper on synthetic meshes so the assertion logic has guaranteed
// coverage and is ready to be wired into future per-fixture
// regression tests for ClipMeshToCellsManifold.

import (
	"testing"
)

func TestCountHoleEdges(t *testing.T) {
	// A single closed tetrahedron: 4 faces, 6 edges, each shared by
	// exactly 2 triangles. Zero boundary, zero non-manifold.
	tetVerts := [][3]float32{
		{0, 0, 0},
		{1, 0, 0},
		{0, 1, 0},
		{0, 0, 1},
	}
	tetFaces := [][3]uint32{
		{0, 2, 1}, // base z=0 (CW from above so the outward normal points down)
		{0, 1, 3}, // y=0 wall
		{1, 2, 3}, // slanted wall
		{0, 3, 2}, // x=0 wall
	}
	b, nm := countHoleEdges(tetVerts, tetFaces)
	if b != 0 || nm != 0 {
		t.Errorf("closed tetrahedron: boundary=%d nonManifold=%d, want 0,0", b, nm)
	}

	// Drop one face — the tetrahedron now has a hole. The three edges
	// of the dropped face become single-use boundary edges.
	openFaces := tetFaces[:3]
	b, nm = countHoleEdges(tetVerts, openFaces)
	if b != 3 || nm != 0 {
		t.Errorf("open tetrahedron: boundary=%d nonManifold=%d, want 3,0", b, nm)
	}

	// Two coplanar triangles sharing an edge, plus a third triangle
	// duplicating one of those edges — the shared edge appears in
	// three faces, i.e. non-manifold.
	dupVerts := [][3]float32{
		{0, 0, 0},
		{1, 0, 0},
		{0, 1, 0},
		{1, 1, 0},
	}
	dupFaces := [][3]uint32{
		{0, 1, 2}, // edge 1→2
		{1, 3, 2}, // edge 1→2 reverse (manifold pair so far)
		{2, 1, 0}, // edge 1→2 again (third use → non-manifold)
	}
	_, nm = countHoleEdges(dupVerts, dupFaces)
	if nm == 0 {
		t.Errorf("triple-edge mesh: nonManifold=%d, want >0", nm)
	}
}

// countHoleEdges returns (boundary, nonManifold) edge counts keyed by
// 1µm quantised 3D position. Coincident-position vertices that didn't
// share an index still collapse to one edge.
func countHoleEdges(verts [][3]float32, faces [][3]uint32) (boundary, nonManifold int) {
	type ek struct{ A, B int3D }
	mk := func(a, b int3D) ek {
		if a.X > b.X || (a.X == b.X && a.Y > b.Y) || (a.X == b.X && a.Y == b.Y && a.Z > b.Z) {
			a, b = b, a
		}
		return ek{a, b}
	}
	counts := make(map[ek]int, len(faces)*2)
	for _, f := range faces {
		va := Quantize(verts[f[0]])
		vb := Quantize(verts[f[1]])
		vc := Quantize(verts[f[2]])
		if va != vb {
			counts[mk(va, vb)]++
		}
		if vb != vc {
			counts[mk(vb, vc)]++
		}
		if vc != va {
			counts[mk(vc, va)]++
		}
	}
	for _, c := range counts {
		switch {
		case c == 1:
			boundary++
		case c == 2:
			// manifold edge — expected
		default:
			nonManifold++
		}
	}
	return
}
