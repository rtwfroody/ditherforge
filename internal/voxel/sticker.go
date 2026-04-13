package voxel

import (
	"image"
	"math"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// StickerDecal holds the pre-computed mapping from mesh triangles to sticker
// texture UVs. Built by flood-filling from a seed triangle across the mesh
// adjacency, projecting each vertex onto the sticker's tangent plane.
type StickerDecal struct {
	Image  image.Image
	TriUVs map[int32][3][2]float32 // triangle index → per-vertex sticker UVs
}

// FindSeedTriangle returns the index of the triangle closest to the given
// world-space point, or -1 if none is found.
func FindSeedTriangle(point [3]float64, model *loader.LoadedModel, si *SpatialIndex) int32 {
	p := [3]float32{float32(point[0]), float32(point[1]), float32(point[2])}

	// Search in expanding radii until we find a triangle.
	buf := NewSearchBuf(len(model.Faces))
	for _, radius := range []float32{1, 5, 20, 100, 500} {
		cands := si.CandidatesRadiusZ(p[0], p[1], radius, p[2], radius, buf)
		bestDistSq := float32(math.MaxFloat32)
		bestTri := int32(-1)
		for _, ti := range cands {
			f := model.Faces[ti]
			r := ClosestPointOnTriangle(p,
				model.Vertices[f[0]], model.Vertices[f[1]], model.Vertices[f[2]])
			if r.DistSq < bestDistSq {
				bestDistSq = r.DistSq
				bestTri = ti
			}
		}
		if bestTri >= 0 {
			return bestTri
		}
	}
	return -1
}

// Edge vertex indices within a triangle's face.
// Edge 0: vertices 0,1; Edge 1: vertices 1,2; Edge 2: vertices 2,0.
var edgeVertIdx = [3][2]int{{0, 1}, {1, 2}, {2, 0}}
var edgeOtherIdx = [3]int{2, 0, 1} // the vertex NOT on the edge

// bfsEntry carries a triangle and its computed sticker UVs through the BFS.
type bfsEntry struct {
	tri int32
	uvs [3][2]float32
}

// BuildStickerDecal creates a decal by flood-filling from a seed triangle
// across the mesh adjacency. The seed triangle's UVs are computed by planar
// projection. Subsequent triangles get their UVs by geodesic unfolding:
// shared edge vertices keep their UVs from the parent, and the new vertex
// is placed by preserving the 3D triangle's shape in UV space. This makes
// the sticker follow the surface rather than projecting through corners.
func BuildStickerDecal(
	model *loader.LoadedModel,
	adj *TriAdjacency,
	img image.Image,
	seedTri int32,
	center [3]float64,
	normal [3]float64,
	up [3]float64,
	scale float64,
	rotationDeg float64,
) *StickerDecal {
	// Build orthonormal tangent frame from normal and up.
	n := normalize3(normal)
	u := normalize3(up)

	// If up is nearly parallel to normal, substitute a world axis.
	cross := cross3(u, n)
	crossLen := math.Sqrt(cross[0]*cross[0] + cross[1]*cross[1] + cross[2]*cross[2])
	if crossLen < 0.1 {
		if math.Abs(n[0]) < 0.9 {
			u = [3]float64{1, 0, 0}
		} else {
			u = [3]float64{0, 1, 0}
		}
	}

	// T (tangent/right) = normalize(cross(up, normal))
	t := normalize3(cross3(u, n))
	// B (bitangent/up on surface) = normalize(cross(normal, T))
	b := normalize3(cross3(n, t))

	// Apply rotation around the normal.
	if rotationDeg != 0 {
		rad := rotationDeg * math.Pi / 180
		cosR := math.Cos(rad)
		sinR := math.Sin(rad)
		newT := [3]float64{
			cosR*t[0] + sinR*b[0],
			cosR*t[1] + sinR*b[1],
			cosR*t[2] + sinR*b[2],
		}
		newB := [3]float64{
			-sinR*t[0] + cosR*b[0],
			-sinR*t[1] + cosR*b[1],
			-sinR*t[2] + cosR*b[2],
		}
		t = newT
		b = newB
	}

	// Sticker dimensions in world units.
	imgBounds := img.Bounds()
	aspect := float64(imgBounds.Dy()) / float64(imgBounds.Dx())
	halfW := scale / 2
	halfH := (scale * aspect) / 2

	// planarUV projects a vertex onto the tangent plane for the seed triangle.
	planarUV := func(pos [3]float32) [2]float32 {
		dx := float64(pos[0]) - center[0]
		dy := float64(pos[1]) - center[1]
		dz := float64(pos[2]) - center[2]
		projT := dx*t[0] + dy*t[1] + dz*t[2]
		projB := dx*b[0] + dy*b[1] + dz*b[2]
		return [2]float32{
			float32((projT/halfW + 1) / 2),
			float32((projB/halfH + 1) / 2),
		}
	}

	// Compute seed triangle UVs by planar projection.
	seedFace := model.Faces[seedTri]
	seedUVs := [3][2]float32{
		planarUV(model.Vertices[seedFace[0]]),
		planarUV(model.Vertices[seedFace[1]]),
		planarUV(model.Vertices[seedFace[2]]),
	}

	decal := &StickerDecal{
		Image:  img,
		TriUVs: make(map[int32][3][2]float32),
	}

	// BFS flood-fill from seed triangle.
	visited := make([]bool, len(model.Faces))
	queue := []bfsEntry{{tri: seedTri, uvs: seedUVs}}
	visited[seedTri] = true

	for len(queue) > 0 {
		entry := queue[0]
		queue = queue[1:]

		if !triOverlapsUVBounds(entry.uvs) {
			continue
		}

		decal.TriUVs[entry.tri] = entry.uvs

		// Expand to neighbors through each edge.
		for ei, ni := range adj.Neighbors[entry.tri] {
			if ni < 0 || visited[ni] {
				continue
			}
			visited[ni] = true

			// Find which edge of the neighbor connects back.
			nej := -1
			for e := 0; e < 3; e++ {
				if adj.Neighbors[ni][e] == entry.tri {
					nej = e
					break
				}
			}
			if nej < 0 {
				continue // shouldn't happen in a well-formed adjacency
			}

			// Shared edge: vertices of current tri's edge ei.
			curFace := model.Faces[entry.tri]
			i0, i1 := edgeVertIdx[ei][0], edgeVertIdx[ei][1]
			uvA := entry.uvs[i0]
			uvB := entry.uvs[i1]
			posA := model.Vertices[curFace[i0]]

			// The current triangle's third vertex (not on the shared edge).
			uvCurrent := entry.uvs[edgeOtherIdx[ei]]

			// Neighbor's vertices: shared edge is at edgeVertIdx[nej],
			// and the new vertex is at edgeOtherIdx[nej].
			nFace := model.Faces[ni]
			nj0, nj1 := edgeVertIdx[nej][0], edgeVertIdx[nej][1]
			posD := model.Vertices[nFace[nj0]]
			posE := model.Vertices[nFace[nj1]]
			posNew := model.Vertices[nFace[edgeOtherIdx[nej]]]

			// Match neighbor's shared edge vertices to our UVs.
			// The shared edge may be traversed in reverse order.
			var uvD, uvE [2]float32
			snapA := SnapPos(posA)
			snapD := SnapPos(posD)
			if snapA == snapD {
				uvD, uvE = uvA, uvB
			} else {
				uvD, uvE = uvB, uvA
			}

			// Unfold the new vertex into UV space.
			uvNew := unfoldVertex(posD, posE, posNew, uvD, uvE, uvCurrent)

			// Build the neighbor's full UV array in face-vertex order.
			var nUVs [3][2]float32
			nUVs[nj0] = uvD
			nUVs[nj1] = uvE
			nUVs[edgeOtherIdx[nej]] = uvNew

			queue = append(queue, bfsEntry{tri: ni, uvs: nUVs})
		}
	}

	return decal
}

// unfoldVertex computes the UV of a new vertex C by unfolding the 3D triangle
// (A, B, C) across the shared edge A-B into UV space. The shared edge has
// known UVs (uvA, uvB). uvCurrent is the UV of the parent triangle's third
// vertex, used to determine which side of the edge the new vertex goes on
// (it must go on the opposite side).
func unfoldVertex(posA, posB, posC [3]float32, uvA, uvB, uvCurrent [2]float32) [2]float32 {
	// UV edge vector and length.
	uvEdge := [2]float32{uvB[0] - uvA[0], uvB[1] - uvA[1]}
	uvEdgeLen := float32(math.Sqrt(float64(uvEdge[0]*uvEdge[0] + uvEdge[1]*uvEdge[1])))
	if uvEdgeLen < 1e-10 {
		return uvA
	}
	uvDir := [2]float32{uvEdge[0] / uvEdgeLen, uvEdge[1] / uvEdgeLen}
	uvPerp := [2]float32{-uvDir[1], uvDir[0]} // 90° CCW rotation

	// 3D vectors from A.
	ab := [3]float32{posB[0] - posA[0], posB[1] - posA[1], posB[2] - posA[2]}
	ac := [3]float32{posC[0] - posA[0], posC[1] - posA[1], posC[2] - posA[2]}
	lenAB := float32(math.Sqrt(float64(ab[0]*ab[0] + ab[1]*ab[1] + ab[2]*ab[2])))
	lenAC := float32(math.Sqrt(float64(ac[0]*ac[0] + ac[1]*ac[1] + ac[2]*ac[2])))
	if lenAB < 1e-10 || lenAC < 1e-10 {
		return uvA
	}

	// Angle at A via dot product.
	cosA := (ab[0]*ac[0] + ab[1]*ac[1] + ab[2]*ac[2]) / (lenAB * lenAC)
	if cosA > 1 {
		cosA = 1
	}
	if cosA < -1 {
		cosA = -1
	}
	sinA := float32(math.Sqrt(float64(1 - cosA*cosA)))

	// Scale from 3D to UV.
	uvScale := uvEdgeLen / lenAB

	// The new vertex must be on the opposite side of the edge from uvCurrent.
	toCurrent := [2]float32{uvCurrent[0] - uvA[0], uvCurrent[1] - uvA[1]}
	crossSign := uvEdge[0]*toCurrent[1] - uvEdge[1]*toCurrent[0]
	sign := float32(1)
	if crossSign > 0 {
		sign = -1
	}

	r := lenAC * uvScale
	return [2]float32{
		uvA[0] + r*(cosA*uvDir[0]+sign*sinA*uvPerp[0]),
		uvA[1] + r*(cosA*uvDir[1]+sign*sinA*uvPerp[1]),
	}
}

// triOverlapsUVBounds returns true if the triangle (in sticker UV space)
// overlaps the [0,1]x[0,1] sticker region. This uses a bounding-box test:
// the triangle's AABB must overlap [0,1]x[0,1]. This is conservative (may
// include some triangles that don't actually overlap) but never misses.
func triOverlapsUVBounds(uvs [3][2]float32) bool {
	minU := min(uvs[0][0], min(uvs[1][0], uvs[2][0]))
	maxU := max(uvs[0][0], max(uvs[1][0], uvs[2][0]))
	minV := min(uvs[0][1], min(uvs[1][1], uvs[2][1]))
	maxV := max(uvs[0][1], max(uvs[1][1], uvs[2][1]))
	return maxU >= 0 && minU <= 1 && maxV >= 0 && minV <= 1
}

// CompositeStickerColor samples all decals for the given triangle at the given
// barycentric coordinates and alpha-composites over the base color. Returns the
// composited RGBA.
func CompositeStickerColor(base [4]uint8, triIdx int32, bary [3]float32, decals []*StickerDecal) [4]uint8 {
	result := base
	for _, d := range decals {
		uvs, ok := d.TriUVs[triIdx]
		if !ok {
			continue
		}

		// Interpolate sticker UV from barycentric coordinates.
		u := bary[0]*uvs[0][0] + bary[1]*uvs[1][0] + bary[2]*uvs[2][0]
		v := bary[0]*uvs[0][1] + bary[1]*uvs[1][1] + bary[2]*uvs[2][1]

		// Skip if interpolated UV is outside [0,1].
		if u < 0 || u > 1 || v < 0 || v > 1 {
			continue
		}

		// Sample sticker image. V is flipped (image y=0 is top).
		bounds := d.Image.Bounds()
		imgW := bounds.Dx()
		imgH := bounds.Dy()
		px := int(u*float32(imgW-1)) + bounds.Min.X
		py := int((1-v)*float32(imgH-1)) + bounds.Min.Y

		r, g, b, a := d.Image.At(px, py).RGBA()
		if a < 0x0100 {
			continue
		}

		// Un-premultiply and convert to 8-bit.
		sr := uint8(r * 0xFF / a)
		sg := uint8(g * 0xFF / a)
		sb := uint8(b * 0xFF / a)
		sa := uint8(a >> 8)

		// Alpha-composite sticker over result.
		alpha := float32(sa) / 255
		invA := 1 - alpha
		result = [4]uint8{
			uint8(float32(sr)*alpha + float32(result[0])*invA),
			uint8(float32(sg)*alpha + float32(result[1])*invA),
			uint8(float32(sb)*alpha + float32(result[2])*invA),
			result[3], // preserve base alpha
		}
	}
	return result
}

// Vector math helpers.

func cross3(a, b [3]float64) [3]float64 {
	return [3]float64{
		a[1]*b[2] - a[2]*b[1],
		a[2]*b[0] - a[0]*b[2],
		a[0]*b[1] - a[1]*b[0],
	}
}

func normalize3(v [3]float64) [3]float64 {
	l := math.Sqrt(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])
	if l < 1e-12 {
		return [3]float64{0, 0, 1}
	}
	return [3]float64{v[0] / l, v[1] / l, v[2] / l}
}
