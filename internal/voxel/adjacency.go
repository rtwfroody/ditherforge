package voxel

import "github.com/rtwfroody/ditherforge/internal/loader"

// TriAdjacency stores per-triangle edge-neighbor information.
// Neighbors[i] holds up to 3 neighbor triangle indices for triangle i,
// one per edge. -1 means no neighbor on that edge.
type TriAdjacency struct {
	Neighbors [][3]int32
}

// snappedEdge is a pair of snapped vertex positions used as an edge key.
// Positions are ordered so that the smaller one comes first, making the
// key symmetric (edge A-B == edge B-A).
type snappedEdge struct {
	a, b [3]float32
}

func makeSnappedEdge(p0, p1 [3]float32) snappedEdge {
	a := SnapPos(p0)
	b := SnapPos(p1)
	if a[0] < b[0] || (a[0] == b[0] && a[1] < b[1]) || (a[0] == b[0] && a[1] == b[1] && a[2] < b[2]) {
		return snappedEdge{a, b}
	}
	return snappedEdge{b, a}
}

// edgeEntry records which triangle and which edge index (0,1,2) an edge belongs to.
type edgeEntry struct {
	tri  int32
	edge int // 0, 1, or 2
}

// BuildTriAdjacency builds an edge-based adjacency structure for the model's
// triangles. Vertices at the same snapped position are considered identical,
// so edges are matched even when vertices are duplicated (e.g. for UV seams).
func BuildTriAdjacency(model *loader.LoadedModel) *TriAdjacency {
	nTris := len(model.Faces)
	adj := &TriAdjacency{
		Neighbors: make([][3]int32, nTris),
	}
	// Initialize all neighbors to -1 (no neighbor).
	for i := range adj.Neighbors {
		adj.Neighbors[i] = [3]int32{-1, -1, -1}
	}

	// Map each edge to the first triangle that claimed it.
	edgeMap := make(map[snappedEdge]edgeEntry, nTris*3/2)

	for fi, f := range model.Faces {
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]

		edges := [3]snappedEdge{
			makeSnappedEdge(v0, v1), // edge 0: v0-v1
			makeSnappedEdge(v1, v2), // edge 1: v1-v2
			makeSnappedEdge(v2, v0), // edge 2: v2-v0
		}

		for ei, e := range edges {
			if prev, ok := edgeMap[e]; ok {
				// Link the two triangles sharing this edge.
				adj.Neighbors[fi][ei] = prev.tri
				adj.Neighbors[prev.tri][prev.edge] = int32(fi)
				// Remove from map so a third triangle on this edge
				// (non-manifold) doesn't overwrite. Each edge links at
				// most 2 triangles.
				delete(edgeMap, e)
			} else {
				edgeMap[e] = edgeEntry{tri: int32(fi), edge: ei}
			}
		}
	}

	return adj
}
