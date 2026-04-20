package voxel

import (
	"context"
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

// bfsEntry carries a triangle and its tangent-plane coordinates through the BFS.
// Tangent coords are in world units (isotropic); converted to UV when storing.
type bfsEntry struct {
	tri int32
	tc  [3][2]float32
}

// BuildStickerDecal creates a decal by flood-filling from a seed triangle
// across the mesh adjacency. The seed triangle's UVs are computed by planar
// projection. Subsequent triangles get their UVs by geodesic unfolding:
// shared edge vertices keep their UVs from the parent, and the new vertex
// is placed by preserving the 3D triangle's shape in UV space. This makes
// the sticker follow the surface rather than projecting through corners.
func BuildStickerDecal(
	ctx context.Context,
	model *loader.LoadedModel,
	adj *TriAdjacency,
	img image.Image,
	seedTri int32,
	center [3]float64,
	normal [3]float64,
	up [3]float64,
	scale float64,
	rotationDeg float64,
	maxAngleDeg float64,
) (*StickerDecal, error) {
	t, b, _ := buildStickerTangentFrame(normal, up, rotationDeg)

	// Sticker dimensions in world units.
	imgBounds := img.Bounds()
	aspect := float64(imgBounds.Dy()) / float64(imgBounds.Dx())
	halfW := scale / 2
	halfH := (scale * aspect) / 2

	// planarTangent projects a vertex onto the tangent plane, returning
	// coordinates in world units (isotropic). The BFS tracks these tangent
	// coordinates so that unfoldVertex works in an isotropic space. We
	// convert to UV only when storing in the decal or checking occupancy.
	planarTangent := func(pos [3]float32) [2]float32 {
		dx := float64(pos[0]) - center[0]
		dy := float64(pos[1]) - center[1]
		dz := float64(pos[2]) - center[2]
		return [2]float32{
			float32(dx*t[0] + dy*t[1] + dz*t[2]),
			float32(dx*b[0] + dy*b[1] + dz*b[2]),
		}
	}

	// tangentToUV converts tangent-plane coordinates to [0,1] UV.
	fHalfW := float32(halfW)
	fHalfH := float32(halfH)
	tangentToUV := func(tc [2]float32) [2]float32 {
		return [2]float32{
			(tc[0]/fHalfW + 1) / 2,
			(tc[1]/fHalfH + 1) / 2,
		}
	}
	tangentTrisToUV := func(tc [3][2]float32) [3][2]float32 {
		return [3][2]float32{tangentToUV(tc[0]), tangentToUV(tc[1]), tangentToUV(tc[2])}
	}

	// Compute seed triangle tangent coords.
	seedFace := model.Faces[seedTri]
	seedTangent := [3][2]float32{
		planarTangent(model.Vertices[seedFace[0]]),
		planarTangent(model.Vertices[seedFace[1]]),
		planarTangent(model.Vertices[seedFace[2]]),
	}

	decal := &StickerDecal{
		Image:  img,
		TriUVs: make(map[int32][3][2]float32),
	}

	// Precompute cosine threshold for max angle check.
	// A maxAngleDeg of 0 means no limit.
	cosMaxAngle := float32(-1) // accept all angles by default
	if maxAngleDeg > 0 && maxAngleDeg < 180 {
		cosMaxAngle = float32(math.Cos(maxAngleDeg * math.Pi / 180))
	}

	// UV-space occupancy bitmap. Tracks which sticker pixels have already
	// been claimed by a triangle. A new triangle is only accepted if it
	// covers at least one unclaimed pixel, preventing the sticker from
	// repeating when geodesic unfolding folds back on non-developable surfaces.
	occW := imgBounds.Dx()
	occH := imgBounds.Dy()
	occupancy := make([]bool, occW*occH)

	// triOverlapsTangentBounds checks if the triangle overlaps the sticker
	// area in tangent-plane coordinates: [-halfW,halfW] x [-halfH,halfH].
	triOverlapsTangentBounds := func(tc [3][2]float32) bool {
		minU := min(tc[0][0], min(tc[1][0], tc[2][0]))
		maxU := max(tc[0][0], max(tc[1][0], tc[2][0]))
		minV := min(tc[0][1], min(tc[1][1], tc[2][1]))
		maxV := max(tc[0][1], max(tc[1][1], tc[2][1]))
		return maxU >= -fHalfW && minU <= fHalfW && maxV >= -fHalfH && minV <= fHalfH
	}

	// BFS flood-fill from seed triangle. Coordinates are in tangent-plane
	// space (world units) so that unfoldVertex operates in an isotropic space.
	visited := make([]bool, len(model.Faces))
	queue := []bfsEntry{{tri: seedTri, tc: seedTangent}}
	visited[seedTri] = true

	iter := 0
	for len(queue) > 0 {
		if iter%1000 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		iter++
		entry := queue[0]
		queue = queue[1:]

		if !triOverlapsTangentBounds(entry.tc) {
			continue
		}

		// Convert to UV for occupancy check and decal storage.
		uvs := tangentTrisToUV(entry.tc)

		// Only accept this triangle if it covers at least one new pixel.
		if !occupancyClaimTriangle(uvs, occupancy, occW, occH) {
			continue
		}

		decal.TriUVs[entry.tri] = uvs

		// Expand to neighbors through each edge.
		var curNormal [3]float32
		if cosMaxAngle > -1 {
			curNormal = faceNormal32(model, entry.tri)
		}
		for ei, ni := range adj.Neighbors[entry.tri] {
			if ni < 0 || visited[ni] {
				continue
			}

			// Skip neighbor if the angle between face normals exceeds the limit.
			if cosMaxAngle > -1 {
				n2 := faceNormal32(model, ni)
				dot := curNormal[0]*n2[0] + curNormal[1]*n2[1] + curNormal[2]*n2[2]
				if dot < cosMaxAngle {
					continue
				}
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
			tcA := entry.tc[i0]
			tcB := entry.tc[i1]
			posA := model.Vertices[curFace[i0]]

			// The current triangle's third vertex (not on the shared edge).
			tcCurrent := entry.tc[edgeOtherIdx[ei]]

			// Neighbor's vertices: shared edge is at edgeVertIdx[nej],
			// and the new vertex is at edgeOtherIdx[nej].
			nFace := model.Faces[ni]
			nj0, nj1 := edgeVertIdx[nej][0], edgeVertIdx[nej][1]
			posD := model.Vertices[nFace[nj0]]
			posE := model.Vertices[nFace[nj1]]
			posNew := model.Vertices[nFace[edgeOtherIdx[nej]]]

			// Match neighbor's shared edge vertices to our tangent coords.
			// The shared edge may be traversed in reverse order.
			var tcD, tcE [2]float32
			snapA := SnapPos(posA)
			snapD := SnapPos(posD)
			if snapA == snapD {
				tcD, tcE = tcA, tcB
			} else {
				tcD, tcE = tcB, tcA
			}

			// Unfold the new vertex into tangent space (isotropic).
			tcNew := unfoldVertex(posD, posE, posNew, tcD, tcE, tcCurrent)

			// Build the neighbor's full tangent-coord array in face-vertex order.
			var nTCs [3][2]float32
			nTCs[nj0] = tcD
			nTCs[nj1] = tcE
			nTCs[edgeOtherIdx[nej]] = tcNew

			queue = append(queue, bfsEntry{tri: ni, tc: nTCs})
		}
	}

	return decal, nil
}

// unfoldVertex computes the 2D position of a new vertex C by unfolding the 3D
// triangle (A, B, C) across the shared edge A-B into a flat coordinate space.
// The shared edge has known 2D positions (flatA, flatB). flatCurrent is the 2D
// position of the parent triangle's third vertex, used to determine which side
// of the edge the new vertex goes on (it must go on the opposite side).
// The 2D space can be any isotropic coordinate system (e.g. tangent-plane coords).
func unfoldVertex(posA, posB, posC [3]float32, flatA, flatB, flatCurrent [2]float32) [2]float32 {
	// 2D edge vector and length.
	edgeVec := [2]float32{flatB[0] - flatA[0], flatB[1] - flatA[1]}
	edgeLen := float32(math.Sqrt(float64(edgeVec[0]*edgeVec[0] + edgeVec[1]*edgeVec[1])))
	if edgeLen < 1e-10 {
		return flatA
	}
	edgeDir := [2]float32{edgeVec[0] / edgeLen, edgeVec[1] / edgeLen}
	edgePerp := [2]float32{-edgeDir[1], edgeDir[0]} // 90° CCW rotation

	// 3D vectors from A.
	ab := [3]float32{posB[0] - posA[0], posB[1] - posA[1], posB[2] - posA[2]}
	ac := [3]float32{posC[0] - posA[0], posC[1] - posA[1], posC[2] - posA[2]}
	lenAB := float32(math.Sqrt(float64(ab[0]*ab[0] + ab[1]*ab[1] + ab[2]*ab[2])))
	lenAC := float32(math.Sqrt(float64(ac[0]*ac[0] + ac[1]*ac[1] + ac[2]*ac[2])))
	if lenAB < 1e-10 || lenAC < 1e-10 {
		return flatA
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

	// Scale from 3D to 2D.
	flatScale := edgeLen / lenAB

	// The new vertex must be on the opposite side of the edge from flatCurrent.
	toCurrent := [2]float32{flatCurrent[0] - flatA[0], flatCurrent[1] - flatA[1]}
	crossSign := edgeVec[0]*toCurrent[1] - edgeVec[1]*toCurrent[0]
	sign := float32(1)
	if crossSign > 0 {
		sign = -1
	}

	r := lenAC * flatScale
	return [2]float32{
		flatA[0] + r*(cosA*edgeDir[0]+sign*sinA*edgePerp[0]),
		flatA[1] + r*(cosA*edgeDir[1]+sign*sinA*edgePerp[1]),
	}
}

// occupancyClaimTriangle rasterizes a triangle in UV space into the occupancy
// bitmap. Returns true if at least one pixel was newly claimed (i.e., the
// triangle covers some previously unclaimed area). All pixels covered by the
// triangle are marked as claimed regardless of the return value.
func occupancyClaimTriangle(uvs [3][2]float32, occ []bool, w, h int) bool {
	// Compute AABB in pixel coordinates, clipped to bitmap.
	fw, fh := float32(w), float32(h)
	minPx := int(max(0, min(uvs[0][0], min(uvs[1][0], uvs[2][0]))*fw))
	maxPx := int(min(fw-1, max(uvs[0][0], max(uvs[1][0], uvs[2][0]))*fw))
	minPy := int(max(0, min(uvs[0][1], min(uvs[1][1], uvs[2][1]))*fh))
	maxPy := int(min(fh-1, max(uvs[0][1], max(uvs[1][1], uvs[2][1]))*fh))

	claimedNew := false
	for py := minPy; py <= maxPy; py++ {
		for px := minPx; px <= maxPx; px++ {
			// Sample point at pixel center.
			u := (float32(px) + 0.5) / fw
			v := (float32(py) + 0.5) / fh
			if !pointInTriangle2D(u, v, uvs) {
				continue
			}
			idx := py*w + px
			if !occ[idx] {
				claimedNew = true
			}
			occ[idx] = true
		}
	}
	return claimedNew
}

// pointInTriangle2D returns true if point (px,py) is inside the 2D triangle
// defined by uvs, using barycentric coordinate sign tests.
func pointInTriangle2D(px, py float32, uvs [3][2]float32) bool {
	d1 := (px-uvs[1][0])*(uvs[0][1]-uvs[1][1]) - (uvs[0][0]-uvs[1][0])*(py-uvs[1][1])
	d2 := (px-uvs[2][0])*(uvs[1][1]-uvs[2][1]) - (uvs[1][0]-uvs[2][0])*(py-uvs[2][1])
	d3 := (px-uvs[0][0])*(uvs[2][1]-uvs[0][1]) - (uvs[2][0]-uvs[0][0])*(py-uvs[0][1])
	hasNeg := (d1 < 0) || (d2 < 0) || (d3 < 0)
	hasPos := (d1 > 0) || (d2 > 0) || (d3 > 0)
	return !(hasNeg && hasPos)
}

// buildStickerTangentFrame returns (t, b, n): the sticker's tangent, bitangent,
// and normal in world coordinates. Matches the convention used by both decal
// builders and the frontend's floating-billboard preview: n along the surface
// normal, t across, b up on the surface; rotationDeg rotates (t,b) around n.
// If up is nearly parallel to normal, a world axis is substituted.
func buildStickerTangentFrame(normal, up [3]float64, rotationDeg float64) (t, b, n [3]float64) {
	n = normalize3(normal)
	u := normalize3(up)

	cross := cross3(u, n)
	crossLen := math.Sqrt(cross[0]*cross[0] + cross[1]*cross[1] + cross[2]*cross[2])
	if crossLen < 0.1 {
		if math.Abs(n[0]) < 0.9 {
			u = [3]float64{1, 0, 0}
		} else {
			u = [3]float64{0, 1, 0}
		}
	}

	t = normalize3(cross3(u, n))
	b = normalize3(cross3(n, t))

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
	return t, b, n
}

// Triangles whose face normal makes a larger angle with the sticker normal
// than this are rejected from projection mode. Above ~84° (cos ≈ 0.1) the
// projected footprint stretches so far that the resulting UV smear is never
// what the user wants.
const projectionMinCosAngle = float32(0.1)

// BuildStickerDecalProjection creates a decal by projecting the sticker image
// onto the mesh along the sticker normal. Unlike the unfolding builder, there
// is no flood fill or geodesic distortion — every front-facing triangle whose
// projected footprint overlaps the sticker rectangle becomes a candidate, and
// the candidate is kept only if it is the frontmost surface along the
// projection direction at its centroid (approximate occlusion test).
//
// Limitation: partially occluded triangles (centroid visible, some portion
// hidden) are kept whole, so the sticker can bleed through onto hidden
// regions. A fully correct implementation would require triangle clipping
// in UV space.
func BuildStickerDecalProjection(
	ctx context.Context,
	model *loader.LoadedModel,
	img image.Image,
	center [3]float64,
	normal [3]float64,
	up [3]float64,
	scale float64,
	rotationDeg float64,
) (*StickerDecal, error) {
	t, b, n := buildStickerTangentFrame(normal, up, rotationDeg)

	imgBounds := img.Bounds()
	aspect := float64(imgBounds.Dy()) / float64(imgBounds.Dx())
	halfW := scale / 2
	halfH := (scale * aspect) / 2
	fHalfW := float32(halfW)
	fHalfH := float32(halfH)

	// Per-vertex tangent projection: returns (tc_u, tc_v, depth_along_n).
	// depth_along_n is in world units; larger = closer to the (virtual)
	// projector at +n infinity.
	projectVertex := func(pos [3]float32) (float32, float32, float32) {
		dx := float64(pos[0]) - center[0]
		dy := float64(pos[1]) - center[1]
		dz := float64(pos[2]) - center[2]
		return float32(dx*t[0] + dy*t[1] + dz*t[2]),
			float32(dx*b[0] + dy*b[1] + dz*b[2]),
			float32(dx*n[0] + dy*n[1] + dz*n[2])
	}

	type candidate struct {
		tri    int32
		tcs    [3][2]float32 // tangent-plane coords per vertex
		depths [3]float32    // depth along +n per vertex
		cx, cy float32       // centroid tangent coords
		cdepth float32       // centroid depth
	}

	// Gather candidates: front-facing (well past edge-on) triangles whose
	// tangent-plane AABB overlaps the sticker rectangle. Zero-area faces are
	// skipped explicitly; we don't rely on faceNormal32's [0,0,1] fallback,
	// which would spuriously pass the front-face test when the sticker
	// normal happens to point +Z.
	var cands []candidate
	for fi := range model.Faces {
		if fi%1000 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		tri := int32(fi)
		f := model.Faces[tri]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]

		ax, ay, az := v1[0]-v0[0], v1[1]-v0[1], v1[2]-v0[2]
		bx, by, bz := v2[0]-v0[0], v2[1]-v0[1], v2[2]-v0[2]
		nx := ay*bz - az*by
		ny := az*bx - ax*bz
		nz := ax*by - ay*bx
		nLen := float32(math.Sqrt(float64(nx*nx + ny*ny + nz*nz)))
		if nLen < 1e-12 {
			continue
		}
		fnDot := float32(float64(nx/nLen)*n[0] + float64(ny/nLen)*n[1] + float64(nz/nLen)*n[2])
		if fnDot < projectionMinCosAngle {
			continue
		}

		u0, b0, d0 := projectVertex(v0)
		u1, b1, d1 := projectVertex(v1)
		u2, b2, d2 := projectVertex(v2)

		minU := min(u0, min(u1, u2))
		maxU := max(u0, max(u1, u2))
		minV := min(b0, min(b1, b2))
		maxV := max(b0, max(b1, b2))
		if maxU < -fHalfW || minU > fHalfW || maxV < -fHalfH || minV > fHalfH {
			continue
		}

		cands = append(cands, candidate{
			tri:    tri,
			tcs:    [3][2]float32{{u0, b0}, {u1, b1}, {u2, b2}},
			depths: [3]float32{d0, d1, d2},
			cx:     (u0 + u1 + u2) / 3,
			cy:     (b0 + b1 + b2) / 3,
			cdepth: (d0 + d1 + d2) / 3,
		})
	}

	decal := &StickerDecal{
		Image:  img,
		TriUVs: make(map[int32][3][2]float32),
	}
	if len(cands) == 0 {
		return decal, nil
	}

	// Occlusion test: for each candidate, check if any OTHER candidate is
	// closer to the projector at its centroid (t,b). If so, drop it.
	// O(n²) in candidates; acceptable for typical sticker sizes.
	//
	// depthEps scales with sticker size so coplanar surfaces within 0.01%
	// of the sticker's width are treated as ties rather than occluders.
	const baryEps = float32(1e-4)
	depthEps := float32(scale) * 1e-4
	for i, c := range cands {
		if i%1000 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		occluded := false
		for j, other := range cands {
			if i == j {
				continue
			}
			bary, ok := barycentric2D(c.cx, c.cy, other.tcs)
			if !ok {
				continue
			}
			if bary[0] < -baryEps || bary[1] < -baryEps || bary[2] < -baryEps {
				continue
			}
			otherDepth := bary[0]*other.depths[0] + bary[1]*other.depths[1] + bary[2]*other.depths[2]
			if otherDepth > c.cdepth+depthEps {
				occluded = true
				break
			}
		}
		if occluded {
			continue
		}
		decal.TriUVs[c.tri] = [3][2]float32{
			{(c.tcs[0][0]/fHalfW + 1) / 2, (c.tcs[0][1]/fHalfH + 1) / 2},
			{(c.tcs[1][0]/fHalfW + 1) / 2, (c.tcs[1][1]/fHalfH + 1) / 2},
			{(c.tcs[2][0]/fHalfW + 1) / 2, (c.tcs[2][1]/fHalfH + 1) / 2},
		}
	}

	return decal, nil
}

// barycentric2D returns barycentric coords of (px,py) relative to the 2D
// triangle. ok is false if the triangle is degenerate.
func barycentric2D(px, py float32, tri [3][2]float32) ([3]float32, bool) {
	x0, y0 := tri[0][0], tri[0][1]
	x1, y1 := tri[1][0], tri[1][1]
	x2, y2 := tri[2][0], tri[2][1]
	denom := (y1-y2)*(x0-x2) + (x2-x1)*(y0-y2)
	if denom == 0 {
		return [3]float32{}, false
	}
	w0 := ((y1-y2)*(px-x2) + (x2-x1)*(py-y2)) / denom
	w1 := ((y2-y0)*(px-x2) + (x0-x2)*(py-y2)) / denom
	return [3]float32{w0, w1, 1 - w0 - w1}, true
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

// faceNormal32 computes the unit normal of a triangle by cross product.
func faceNormal32(model *loader.LoadedModel, tri int32) [3]float32 {
	f := model.Faces[tri]
	v0 := model.Vertices[f[0]]
	v1 := model.Vertices[f[1]]
	v2 := model.Vertices[f[2]]
	ax, ay, az := v1[0]-v0[0], v1[1]-v0[1], v1[2]-v0[2]
	bx, by, bz := v2[0]-v0[0], v2[1]-v0[1], v2[2]-v0[2]
	nx := ay*bz - az*by
	ny := az*bx - ax*bz
	nz := ax*by - ay*bx
	l := float32(math.Sqrt(float64(nx*nx + ny*ny + nz*nz)))
	if l < 1e-12 {
		return [3]float32{0, 0, 1}
	}
	return [3]float32{nx / l, ny / l, nz / l}
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
