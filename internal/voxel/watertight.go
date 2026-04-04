package voxel

import "fmt"

// HalfEdge represents a directed edge from vertex A to vertex B.
type HalfEdge struct {
	A, B uint32
}

// WatertightResult contains the results of a watertight mesh check.
type WatertightResult struct {
	// BoundaryEdges are half-edges with no matching reverse half-edge.
	BoundaryEdges []HalfEdge
	// NonManifoldEdges are half-edges shared by more than 2 faces.
	NonManifoldEdges []HalfEdge
}

// IsWatertight returns true if the mesh has no boundary or non-manifold edges.
func (r *WatertightResult) IsWatertight() bool {
	return len(r.BoundaryEdges) == 0 && len(r.NonManifoldEdges) == 0
}

func (r *WatertightResult) String() string {
	if r.IsWatertight() {
		return "watertight"
	}
	return fmt.Sprintf("%d boundary edges, %d non-manifold edges",
		len(r.BoundaryEdges), len(r.NonManifoldEdges))
}

// CheckWatertight checks whether a triangle mesh is watertight.
// A mesh is watertight if every directed half-edge (A→B) has exactly one
// matching reverse half-edge (B→A), meaning every edge is shared by exactly
// 2 faces with opposite winding.
func CheckWatertight(faces [][3]uint32) *WatertightResult {
	// Count directed half-edges.
	type edge = HalfEdge
	edgeCount := make(map[edge]int, len(faces)*3)
	for _, f := range faces {
		edgeCount[edge{f[0], f[1]}]++
		edgeCount[edge{f[1], f[2]}]++
		edgeCount[edge{f[2], f[0]}]++
	}

	result := &WatertightResult{}
	for e, count := range edgeCount {
		rev := edge{e.B, e.A}
		revCount := edgeCount[rev]
		if revCount == 0 {
			result.BoundaryEdges = append(result.BoundaryEdges, e)
		}
		if count > 1 || revCount > 1 {
			// Only report once per undirected edge (from the A<B side).
			if e.A < e.B {
				result.NonManifoldEdges = append(result.NonManifoldEdges, e)
			}
		}
	}
	return result
}
