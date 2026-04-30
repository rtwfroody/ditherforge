package export3mf

import (
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// TestSplitModelByMesh_SingleMeshReturnsNil — when NumMeshes is 0
// or 1 (the unsplit path), splitModelByMesh returns nil so the
// caller takes the unchanged single-object export path.
func TestSplitModelByMesh_SingleMeshReturnsNil(t *testing.T) {
	model := &loader.LoadedModel{
		Vertices:  [][3]float32{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}},
		Faces:     [][3]uint32{{0, 1, 2}},
		NumMeshes: 1,
	}
	if got := splitModelByMesh(model, []int32{0}); got != nil {
		t.Errorf("got %d parts for single-mesh model, want nil", len(got))
	}

	model.NumMeshes = 0
	if got := splitModelByMesh(model, []int32{0}); got != nil {
		t.Errorf("got %d parts for NumMeshes=0, want nil", len(got))
	}
}

// TestSplitModelByMesh_PartitionsAndCompactsVertices — two meshes
// referenced by FaceMeshIdx produce two parts, each with a compacted
// vertex table and remapped face indices. Verifies the load-bearing
// "vertex table is per-part" contract.
func TestSplitModelByMesh_PartitionsAndCompactsVertices(t *testing.T) {
	// 6 vertices: 0-2 used by mesh 0, 3-5 used by mesh 1.
	// 2 faces: face 0 in mesh 0, face 1 in mesh 1.
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 0}, {1, 0, 0}, {0, 1, 0}, // mesh 0 verts
			{10, 0, 0}, {11, 0, 0}, {10, 1, 0}, // mesh 1 verts
		},
		Faces:       [][3]uint32{{0, 1, 2}, {3, 4, 5}},
		FaceMeshIdx: []int32{0, 1},
		NumMeshes:   2,
	}
	assignments := []int32{7, 9}
	parts := splitModelByMesh(model, assignments)
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(parts))
	}
	// Each part has 3 vertices and 1 face.
	for i, p := range parts {
		if len(p.Vertices) != 3 {
			t.Errorf("part %d: %d vertices, want 3", i, len(p.Vertices))
		}
		if len(p.Faces) != 1 {
			t.Errorf("part %d: %d faces, want 1", i, len(p.Faces))
		}
		if len(p.Assignments) != 1 {
			t.Errorf("part %d: %d assignments, want 1", i, len(p.Assignments))
		}
		// Face indices remapped to part-local: 0, 1, 2.
		f := p.Faces[0]
		if f[0] != 0 || f[1] != 1 || f[2] != 2 {
			t.Errorf("part %d face %v: indices not compacted to {0,1,2}", i, f)
		}
	}
	// Mesh-0 vertices match the first 3 of the source.
	if parts[0].Vertices[0] != model.Vertices[0] {
		t.Errorf("part 0 first vertex %v, want %v", parts[0].Vertices[0], model.Vertices[0])
	}
	// Mesh-1 vertices match indices 3-5.
	if parts[1].Vertices[0] != model.Vertices[3] {
		t.Errorf("part 1 first vertex %v, want %v", parts[1].Vertices[0], model.Vertices[3])
	}
	// Assignments preserved per-face.
	if parts[0].Assignments[0] != 7 || parts[1].Assignments[0] != 9 {
		t.Errorf("assignments not preserved: parts[0]=%v parts[1]=%v", parts[0].Assignments, parts[1].Assignments)
	}
}

// TestSplitModelByMesh_SharedVerticesAreDuplicated — when a vertex
// is referenced by faces from different meshes, each part gets its
// own copy. This is the contract that makes per-part `<object>`
// emission self-contained.
func TestSplitModelByMesh_SharedVerticesAreDuplicated(t *testing.T) {
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 0}, {1, 0, 0}, {0, 1, 0}, {1, 1, 0},
		},
		// Two faces sharing vertex 1; one in mesh 0, one in mesh 1.
		Faces:       [][3]uint32{{0, 1, 2}, {1, 3, 2}},
		FaceMeshIdx: []int32{0, 1},
		NumMeshes:   2,
	}
	parts := splitModelByMesh(model, nil)
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(parts))
	}
	// Vertex 1 (1,0,0) and vertex 2 (0,1,0) are referenced by both
	// meshes; each part gets its own copy.
	if len(parts[0].Vertices) != 3 {
		t.Errorf("part 0: %d vertices, want 3 (verts 0,1,2 from mesh 0)", len(parts[0].Vertices))
	}
	if len(parts[1].Vertices) != 3 {
		t.Errorf("part 1: %d vertices, want 3 (verts 1,3,2 from mesh 1)", len(parts[1].Vertices))
	}
}
