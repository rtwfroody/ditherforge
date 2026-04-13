package voxel

import (
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

func TestBuildTriAdjacencyTwoTriangles(t *testing.T) {
	// Two triangles sharing edge v1-v2:
	//   tri 0: v0, v1, v2
	//   tri 1: v1, v3, v2
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 0}, // v0
			{1, 0, 0}, // v1
			{0, 1, 0}, // v2
			{1, 1, 0}, // v3
		},
		Faces: [][3]uint32{
			{0, 1, 2},
			{1, 3, 2},
		},
	}

	adj := BuildTriAdjacency(model)

	if len(adj.Neighbors) != 2 {
		t.Fatalf("expected 2 triangles, got %d", len(adj.Neighbors))
	}

	// Tri 0 edge 1 (v1-v2) should neighbor tri 1.
	found01 := false
	for _, n := range adj.Neighbors[0] {
		if n == 1 {
			found01 = true
		}
	}
	if !found01 {
		t.Errorf("tri 0 should neighbor tri 1, got %v", adj.Neighbors[0])
	}

	// Tri 1 should neighbor tri 0.
	found10 := false
	for _, n := range adj.Neighbors[1] {
		if n == 0 {
			found10 = true
		}
	}
	if !found10 {
		t.Errorf("tri 1 should neighbor tri 0, got %v", adj.Neighbors[1])
	}
}

func TestBuildTriAdjacencyDuplicatedVertices(t *testing.T) {
	// Same geometry as above but vertices are duplicated (no shared indices),
	// simulating UV seams. Adjacency should still be found via snapped positions.
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 0}, // tri0 v0
			{1, 0, 0}, // tri0 v1
			{0, 1, 0}, // tri0 v2
			{1, 0, 0}, // tri1 v0 (same position as tri0 v1)
			{1, 1, 0}, // tri1 v1
			{0, 1, 0}, // tri1 v2 (same position as tri0 v2)
		},
		Faces: [][3]uint32{
			{0, 1, 2},
			{3, 4, 5},
		},
	}

	adj := BuildTriAdjacency(model)

	found01 := false
	for _, n := range adj.Neighbors[0] {
		if n == 1 {
			found01 = true
		}
	}
	if !found01 {
		t.Errorf("tri 0 should neighbor tri 1 despite duplicated vertices, got %v", adj.Neighbors[0])
	}
}

func TestBuildTriAdjacencyIsolatedTriangle(t *testing.T) {
	// Single triangle — all neighbors should be -1.
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 0},
			{1, 0, 0},
			{0, 1, 0},
		},
		Faces: [][3]uint32{
			{0, 1, 2},
		},
	}

	adj := BuildTriAdjacency(model)

	for ei, n := range adj.Neighbors[0] {
		if n != -1 {
			t.Errorf("edge %d should have no neighbor, got %d", ei, n)
		}
	}
}

func TestBuildTriAdjacencyTetrahedron(t *testing.T) {
	// Tetrahedron: 4 triangles, each sharing edges with the other 3.
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 0},
			{1, 0, 0},
			{0.5, 1, 0},
			{0.5, 0.5, 1},
		},
		Faces: [][3]uint32{
			{0, 1, 2},
			{0, 1, 3},
			{1, 2, 3},
			{0, 2, 3},
		},
	}

	adj := BuildTriAdjacency(model)

	// Every triangle should have exactly 3 neighbors (no -1 entries).
	for fi, n := range adj.Neighbors {
		for ei, neighbor := range n {
			if neighbor == -1 {
				t.Errorf("tri %d edge %d should have a neighbor", fi, ei)
			}
		}
	}
}
