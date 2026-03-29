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

// Pseudonormals holds precomputed angle-weighted vertex pseudonormals and
// edge pseudonormals for accurate SDF sign determination at mesh features.
// See Bærentzen & Aanæs, "Signed Distance Computation Using the Angle
// Weighted Pseudonormal" (2005).
type Pseudonormals struct {
	VertexNormals [][3]float32         // indexed by vertex index
	EdgeNormals   map[EdgeKey][3]float32
	BoundaryEdges map[EdgeKey]struct{}
}

// BuildPseudonormals precomputes pseudonormals for a mesh.
func BuildPseudonormals(model *loader.LoadedModel) *Pseudonormals {
	pn := &Pseudonormals{
		VertexNormals: make([][3]float32, len(model.Vertices)),
		EdgeNormals:   make(map[EdgeKey][3]float32, len(model.Faces)*3),
		BoundaryEdges: make(map[EdgeKey]struct{}),
	}

	edgeCount := make(map[EdgeKey]int, len(model.Faces)*3)

	for _, f := range model.Faces {
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]

		// Face normal (unnormalized for edge accumulation, normalized for vertex weighting).
		e01 := [3]float32{v1[0] - v0[0], v1[1] - v0[1], v1[2] - v0[2]}
		e02 := [3]float32{v2[0] - v0[0], v2[1] - v0[1], v2[2] - v0[2]}
		fn := Cross3f(e01, e02)
		fnLen := float32(math.Sqrt(float64(fn[0]*fn[0] + fn[1]*fn[1] + fn[2]*fn[2])))
		if fnLen < 1e-12 {
			continue
		}
		fnUnit := [3]float32{fn[0] / fnLen, fn[1] / fnLen, fn[2] / fnLen}

		// Angle at each vertex for angle-weighted vertex pseudonormals.
		edges := [3][3]float32{
			e01,
			{v2[0] - v1[0], v2[1] - v1[1], v2[2] - v1[2]},
			{v0[0] - v2[0], v0[1] - v2[1], v0[2] - v2[2]},
		}
		// Opposite edges for angle computation at each vertex.
		// Vertex 0: angle between e01 and e02
		// Vertex 1: angle between -e01 and edges[1]
		// Vertex 2: angle between -edges[1] and -e02
		pairs := [3][2][3]float32{
			{e01, e02},
			{{-e01[0], -e01[1], -e01[2]}, edges[1]},
			{{-edges[1][0], -edges[1][1], -edges[1][2]}, {-e02[0], -e02[1], -e02[2]}},
		}
		verts := [3]uint32{f[0], f[1], f[2]}
		for i := 0; i < 3; i++ {
			a := pairs[i][0]
			b := pairs[i][1]
			la := math.Sqrt(float64(a[0]*a[0] + a[1]*a[1] + a[2]*a[2]))
			lb := math.Sqrt(float64(b[0]*b[0] + b[1]*b[1] + b[2]*b[2]))
			if la < 1e-12 || lb < 1e-12 {
				continue
			}
			cosA := float64(a[0]*b[0]+a[1]*b[1]+a[2]*b[2]) / (la * lb)
			if cosA > 1 {
				cosA = 1
			} else if cosA < -1 {
				cosA = -1
			}
			angle := float32(math.Acos(cosA))
			vi := verts[i]
			pn.VertexNormals[vi][0] += fnUnit[0] * angle
			pn.VertexNormals[vi][1] += fnUnit[1] * angle
			pn.VertexNormals[vi][2] += fnUnit[2] * angle
		}

		// Edge pseudonormals: accumulate face normal for each edge.
		edgePairs := [3]EdgeKey{
			MakeEdgeKey(f[0], f[1]),
			MakeEdgeKey(f[1], f[2]),
			MakeEdgeKey(f[2], f[0]),
		}
		for _, ek := range edgePairs {
			en := pn.EdgeNormals[ek]
			en[0] += fnUnit[0]
			en[1] += fnUnit[1]
			en[2] += fnUnit[2]
			pn.EdgeNormals[ek] = en
			edgeCount[ek]++
		}
	}

	// Normalize vertex pseudonormals.
	for i := range pn.VertexNormals {
		n := pn.VertexNormals[i]
		l := float32(math.Sqrt(float64(n[0]*n[0] + n[1]*n[1] + n[2]*n[2])))
		if l > 1e-12 {
			pn.VertexNormals[i] = [3]float32{n[0] / l, n[1] / l, n[2] / l}
		}
	}

	// Normalize edge pseudonormals and identify boundary edges.
	for ek, en := range pn.EdgeNormals {
		l := float32(math.Sqrt(float64(en[0]*en[0] + en[1]*en[1] + en[2]*en[2])))
		if l > 1e-12 {
			pn.EdgeNormals[ek] = [3]float32{en[0] / l, en[1] / l, en[2] / l}
		}
		if edgeCount[ek] == 1 {
			pn.BoundaryEdges[ek] = struct{}{}
		}
	}

	return pn
}

// ComputeSDF computes the signed distance field value at point p.
// Uses angle-weighted pseudonormals for sign determination at vertices and
// edges. Points within shellThickness of the surface are classified as inside,
// unless the closest point is on a boundary edge and the normal says "outside".
func ComputeSDF(p [3]float32, model *loader.LoadedModel, si *SpatialIndex, searchRadius float32, shellThickness float32, pn *Pseudonormals, modelMin, modelMax [3]float32, buf *SearchBuf) float32 {
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
		// No triangles found within search radius. Since we passed the
		// bounding box check above, the point is likely deep inside the
		// model. Return a large negative value (inside).
		return -searchRadius
	}

	// Choose the appropriate pseudonormal based on the closest region.
	f := model.Faces[bestTri]
	var normal [3]float32
	switch bestRegion {
	case RegionVertex0:
		normal = pn.VertexNormals[f[0]]
	case RegionVertex1:
		normal = pn.VertexNormals[f[1]]
	case RegionVertex2:
		normal = pn.VertexNormals[f[2]]
	case RegionEdge01:
		normal = pn.EdgeNormals[MakeEdgeKey(f[0], f[1])]
	case RegionEdge12:
		normal = pn.EdgeNormals[MakeEdgeKey(f[1], f[2])]
	case RegionEdge20:
		normal = pn.EdgeNormals[MakeEdgeKey(f[2], f[0])]
	default: // RegionInterior — use face normal
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]
		e1 := [3]float32{v1[0] - v0[0], v1[1] - v0[1], v1[2] - v0[2]}
		e2 := [3]float32{v2[0] - v0[0], v2[1] - v0[1], v2[2] - v0[2]}
		normal = Cross3f(e1, e2)
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
			_, onBoundary = pn.BoundaryEdges[MakeEdgeKey(f[0], f[1])]
		}
		if !onBoundary && (bestRegion == RegionEdge12 || bestRegion == RegionVertex1 || bestRegion == RegionVertex2) {
			_, onBoundary = pn.BoundaryEdges[MakeEdgeKey(f[1], f[2])]
		}
		if !onBoundary && (bestRegion == RegionEdge20 || bestRegion == RegionVertex2 || bestRegion == RegionVertex0) {
			_, onBoundary = pn.BoundaryEdges[MakeEdgeKey(f[2], f[0])]
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
