package voxel

import (
	"math"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// EdgeKey is a canonical (undirected) edge, with A <= B.
type EdgeKey struct{ A, B uint32 }

// MakeEdgeKey creates a canonical edge key.
func MakeEdgeKey(a, b uint32) EdgeKey {
	if a > b {
		a, b = b, a
	}
	return EdgeKey{a, b}
}

// BuildBoundaryEdges returns the set of edges that belong to only one face.
func BuildBoundaryEdges(model *loader.LoadedModel) map[EdgeKey]struct{} {
	edgeCount := make(map[EdgeKey]int, len(model.Faces)*3)
	for _, f := range model.Faces {
		edgeCount[MakeEdgeKey(f[0], f[1])]++
		edgeCount[MakeEdgeKey(f[1], f[2])]++
		edgeCount[MakeEdgeKey(f[2], f[0])]++
	}
	boundary := make(map[EdgeKey]struct{})
	for e, count := range edgeCount {
		if count == 1 {
			boundary[e] = struct{}{}
		}
	}
	return boundary
}

// ComputeSDF computes the signed distance field value at point p.
// Uses closest-surface-normal for sign determination. Points within
// shellThickness of the surface are classified as inside, unless the
// closest point is on a boundary edge and the normal says "outside".
func ComputeSDF(p [3]float32, model *loader.LoadedModel, si *SpatialIndex, searchRadius float32, shellThickness float32, boundaryEdges map[EdgeKey]struct{}, modelMin, modelMax [3]float32, buf *SearchBuf) float32 {
	for i := 0; i < 3; i++ {
		if p[i] < modelMin[i] || p[i] > modelMax[i] {
			cands := si.CandidatesRadiusZ(p[0], p[1], searchRadius, p[2], searchRadius, buf)
			bestDistSq := float32(math.MaxFloat32)
			for _, ti := range cands {
				f := model.Faces[ti]
				_, dSq := ClosestPointOnTriangle3D(p, model.Vertices[f[0]], model.Vertices[f[1]], model.Vertices[f[2]])
				if dSq < bestDistSq {
					bestDistSq = dSq
				}
			}
			return float32(math.Sqrt(float64(bestDistSq)))
		}
	}
	cands := si.CandidatesRadiusZ(p[0], p[1], searchRadius, p[2], searchRadius, buf)
	bestDistSq := float32(math.MaxFloat32)
	var bestClosest [3]float32
	bestTri := int32(-1)
	var bestRegion ClosestRegion
	for _, ti := range cands {
		f := model.Faces[ti]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]
		cp, dSq, region := ClosestPointOnTriangle3DEx(p, v0, v1, v2)
		if dSq < bestDistSq {
			bestDistSq = dSq
			bestClosest = cp
			bestTri = ti
			bestRegion = region
		}
	}
	dist := float32(math.Sqrt(float64(bestDistSq)))

	if bestTri < 0 {
		return dist
	}

	f := model.Faces[bestTri]
	v0 := model.Vertices[f[0]]
	v1 := model.Vertices[f[1]]
	v2 := model.Vertices[f[2]]
	e1 := [3]float32{v1[0] - v0[0], v1[1] - v0[1], v1[2] - v0[2]}
	e2 := [3]float32{v2[0] - v0[0], v2[1] - v0[1], v2[2] - v0[2]}
	normal := [3]float32{
		e1[1]*e2[2] - e1[2]*e2[1],
		e1[2]*e2[0] - e1[0]*e2[2],
		e1[0]*e2[1] - e1[1]*e2[0],
	}

	dx := p[0] - bestClosest[0]
	dy := p[1] - bestClosest[1]
	dz := p[2] - bestClosest[2]
	dot := dx*normal[0] + dy*normal[1] + dz*normal[2]

	if dist < shellThickness {
		if dot <= 0 {
			return -dist
		}
		onBoundary := false
		if bestRegion == RegionEdge01 || bestRegion == RegionVertex0 || bestRegion == RegionVertex1 {
			_, onBoundary = boundaryEdges[MakeEdgeKey(f[0], f[1])]
		}
		if !onBoundary && (bestRegion == RegionEdge12 || bestRegion == RegionVertex1 || bestRegion == RegionVertex2) {
			_, onBoundary = boundaryEdges[MakeEdgeKey(f[1], f[2])]
		}
		if !onBoundary && (bestRegion == RegionEdge20 || bestRegion == RegionVertex2 || bestRegion == RegionVertex0) {
			_, onBoundary = boundaryEdges[MakeEdgeKey(f[2], f[0])]
		}
		if onBoundary {
			return dist
		}
		return -dist
	}

	if dot < 0 {
		return -dist
	}
	return dist
}
