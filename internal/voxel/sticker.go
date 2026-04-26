package voxel

import (
	"context"
	"image"
	"math"
	"sort"

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

// bfsEntry carries a triangle and its tangent-plane coordinates through the
// BFS. Each triangle gets its own per-vertex tc built by unfolding across
// the shared edge with its parent — this keeps the BFS numerically stable
// even when two BFS paths reach the same 3D vertex via different accumulated
// distortions. The function-local vertUV map is populated as a SIDE EFFECT
// of BFS (first-triangle-to-touch-a-vertex wins) to seed the ARAP initial
// layout without feeding the cache back into unfoldVertex, which would
// compound divergence.
type bfsEntry struct {
	tri int32
	tc  [3][2]float32
}

// BuildStickerDecal creates a decal by flood-filling from a seed triangle
// across the mesh adjacency and then relaxing the layout with ARAP. The
// initial layout is a Discrete Exponential Map (Schmidt 2006): the seed
// triangle's UVs come from planar projection onto the sticker's tangent
// plane, and every subsequent triangle is unfolded across its shared edge
// with the BFS parent, preserving 3D edge length and interior angle.
// Per-vertex UVs are memoized (first BFS visit wins) so every triangle that
// touches a vertex agrees on its UV, guaranteeing edge continuity even
// across BFS cross-edges.
//
// DEM on its own is great on flat/developable regions but compresses badly
// on curved surfaces because cross-edge vertex reuse feeds inconsistent
// edge lengths into downstream unfolds. The layout is therefore passed to
// arapRegion.Solve — a local/global ARAP parameterization (Liu/Zhou/Wang
// 2008) that pins the seed vertices and relaxes everything else to be
// as-rigid-as-possible with respect to the 3D geometry.
//
// Positive rotationDeg rotates the sticker clockwise when viewed from outside
// the surface (i.e. looking down the normal toward the mesh).
//
// KNOWN LIMITATION (closed/highly-curved meshes): BFS uses unfolded tc to
// bound expansion, with a planar tangent reset at tcRunaway to keep BFS
// from stalling on coarse meshes with very large triangles. On curved or
// closed shapes the reset can bridge geodesically-disjoint regions back
// into the rect, and ARAP — given that wider input — folds them on top
// of the local patch. Visually the sticker can appear stretched or
// repeat several times across the surface. The proper fix is to decouple
// region selection from parameterization: extract a true geodesic disk
// around the seed (heat method or fast marching for surface distance)
// and feed only that disk into ARAP. The current heuristic-based
// approach has been tuned against base.json (works well) but degrades
// on more curved meshes like top.json.
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
	voxelSize float64,
	onProgress func(float64),
) (*StickerDecal, error) {
	reportProgress := func(f float64) {
		if onProgress != nil {
			onProgress(f)
		}
	}
	reportProgress(0)
	// Guarantee the sticker's segment of the aggregate bar completes, even
	// on the early-return paths (empty accepted set, ctx cancel, etc).
	defer reportProgress(1.0)
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

	decal := &StickerDecal{
		Image:  img,
		TriUVs: make(map[int32][3][2]float32),
	}

	// Per-vertex UV map populated as a side effect of the BFS below (first
	// triangle to touch a vertex wins). Consumed by ARAP as its initial
	// per-vertex layout; never read back into the BFS unfold.
	vertUV := make(map[[3]float32][2]float32)
	seedFace := model.Faces[seedTri]

	// Precompute cosine threshold for max angle check.
	// A maxAngleDeg of 0 means no limit.
	cosMaxAngle := float32(-1) // accept all angles by default
	if maxAngleDeg > 0 && maxAngleDeg < 180 {
		cosMaxAngle = float32(math.Cos(maxAngleDeg * math.Pi / 180))
	}

	// triOverlapsTangentBounds checks if the triangle overlaps the sticker
	// area in tangent-plane coordinates: [-halfW,halfW] x [-halfH,halfH].
	triOverlapsTangentBounds := func(tc [3][2]float32) bool {
		minU := min(tc[0][0], min(tc[1][0], tc[2][0]))
		maxU := max(tc[0][0], max(tc[1][0], tc[2][0]))
		minV := min(tc[0][1], min(tc[1][1], tc[2][1]))
		maxV := max(tc[0][1], max(tc[1][1], tc[2][1]))
		return maxU >= -fHalfW && minU <= fHalfW && maxV >= -fHalfH && minV <= fHalfH
	}

	// DEM flood fill. Occupancy is NOT applied during BFS: compressed or
	// distorted DEM triangles would get rejected before ARAP has a chance
	// to fix them, pruning BFS and shrinking coverage. Instead we gate only
	// on triOverlapsTangentBounds (which stops BFS at the sticker edge) and
	// run occupancy later on the relaxed UVs.
	visited := make([]bool, len(model.Faces))
	seedTC := [3][2]float32{
		planarTangent(model.Vertices[seedFace[0]]),
		planarTangent(model.Vertices[seedFace[1]]),
		planarTangent(model.Vertices[seedFace[2]]),
	}
	queue := []bfsEntry{{tri: seedTri, tc: seedTC}}
	visited[seedTri] = true
	var acceptedTris []int32

	// Triangles larger than this in 3D are subdivided during BFS accept.
	// DEM preserves 3D edge length, so triangles much larger than the sticker
	// produce UVs that run far outside the sticker rect — the occupancy
	// rasterizer then rejects them in bulk, leaving "missing" patches on
	// coarsely-triangulated meshes. Splitting them into quarter-size children
	// keeps per-triangle UV spans bounded.
	subdivideThreshold := float32(math.Max(halfW, halfH))

	iter := 0
	totalFaces := len(model.Faces)
	for len(queue) > 0 {
		if iter%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if totalFaces > 0 {
				frac := float64(iter) / float64(totalFaces)
				if frac > 1 {
					frac = 1
				}
				reportProgress(0.60 * frac)
			}
		}
		iter++
		entry := queue[0]
		queue = queue[1:]

		if !triOverlapsTangentBounds(entry.tc) {
			continue
		}

		// Save original face/neighbors before potential in-place subdivision.
		// Subdivision may overwrite model.Faces[entry.tri] with one of its
		// children, but adj.Neighbors[entry.tri] is unchanged because adj was
		// built once up-front.
		origFace := model.Faces[entry.tri]
		origNeighbors := adj.Neighbors[entry.tri]

		// Accept (possibly splitting into smaller children if the triangle's
		// 3D extent exceeds the sticker's scale).
		acceptTriSubdividing(model, &acceptedTris, vertUV, &visited,
			entry.tri, origFace, entry.tc, subdivideThreshold,
			triOverlapsTangentBounds)

		// Expand to neighbors through each original edge. We use origFace
		// rather than model.Faces[entry.tri] because subdivision may have
		// replaced the latter with a child sub-triangle.
		v0 := model.Vertices[origFace[0]]
		v1 := model.Vertices[origFace[1]]
		v2 := model.Vertices[origFace[2]]
		var curNormal [3]float32
		if cosMaxAngle > -1 {
			curNormal = triNormalFromVerts(v0, v1, v2)
		}
		// If this triangle's tc has drifted far outside the sticker rect
		// (DEM runaway from a pathologically-large parent), a DEM unfold
		// across its edges would propagate the bad layout indefinitely. Fall
		// back to planar tangent-plane projection for the neighbors so BFS
		// keeps walking past the poisoned region instead of stalling.
		// Note: this only rescues BFS coverage. vertUV is first-write-wins,
		// so vertices at the DEM/reset seam keep whichever tc arrived first
		// (typically the DEM path), and ARAP starts from a discontinuous
		// layout across that seam. ARAP's Laplacian absorbs most of the
		// discontinuity, but stickers on coarsely-triangulated meshes may
		// show a visible seam where the reset began.
		reset := tcRunaway(entry.tc, fHalfW, fHalfH)
		for ei, ni := range origNeighbors {
			if ni < 0 || visited[ni] {
				continue
			}
			if cosMaxAngle > -1 {
				n2 := faceNormal32(model, ni)
				dot := curNormal[0]*n2[0] + curNormal[1]*n2[1] + curNormal[2]*n2[2]
				if dot < cosMaxAngle {
					continue
				}
			}
			visited[ni] = true

			// Find which edge of the neighbor connects back. entry.tri is
			// always an original pre-sticker face index (BFS never enqueues
			// subdivided children), so the equality still matches adj's
			// pre-sticker-stage records even after our in-place subdivision.
			nej := -1
			for e := 0; e < 3; e++ {
				if adj.Neighbors[ni][e] == entry.tri {
					nej = e
					break
				}
			}
			if nej < 0 {
				continue
			}

			nbrI0 := edgeVertIdx[nej][0]
			nbrI1 := edgeVertIdx[nej][1]
			nbrOther := edgeOtherIdx[nej]
			nFace := model.Faces[ni]

			var nTC [3][2]float32
			if reset {
				// Planar projection reset.
				nTC = [3][2]float32{
					planarTangent(model.Vertices[nFace[0]]),
					planarTangent(model.Vertices[nFace[1]]),
					planarTangent(model.Vertices[nFace[2]]),
				}
			} else {
				curI0 := edgeVertIdx[ei][0]
				curI1 := edgeVertIdx[ei][1]
				// Map neighbor edge endpoints to current tc by matching positions.
				var tcA, tcB [2]float32
				if SnapPos(model.Vertices[origFace[curI0]]) == SnapPos(model.Vertices[nFace[nbrI0]]) {
					tcA = entry.tc[curI0]
					tcB = entry.tc[curI1]
				} else {
					tcA = entry.tc[curI1]
					tcB = entry.tc[curI0]
				}
				nbrPosA := model.Vertices[nFace[nbrI0]]
				nbrPosB := model.Vertices[nFace[nbrI1]]
				nbrPosC := model.Vertices[nFace[nbrOther]]

				flatCurrent := entry.tc[edgeOtherIdx[ei]]
				tcC := unfoldVertex(nbrPosA, nbrPosB, nbrPosC, tcA, tcB, flatCurrent)

				nTC[nbrI0] = tcA
				nTC[nbrI1] = tcB
				nTC[nbrOther] = tcC
			}
			queue = append(queue, bfsEntry{tri: ni, tc: nTC})
		}
	}

	if len(acceptedTris) == 0 {
		return decal, nil
	}

	// ARAP relaxation: fix DEM distortion by iteratively rotating each
	// triangle's reference frame to match the current UV layout, then
	// solving a cotangent-Laplacian system to redistribute vertex UVs
	// so the layout is as-rigid-as-possible relative to 3D geometry.
	// The seed triangle's three vertices are pinned so the sticker's
	// position/orientation/scale are preserved.
	reportProgress(0.60)
	const arapOuterIters = 10
	const cgInnerIters = 50
	region := buildArapRegion(model, acceptedTris, vertUV, seedTri)
	region.Solve(arapOuterIters, cgInnerIters, func(i int) {
		reportProgress(0.60 + 0.30*float64(i+1)/float64(arapOuterIters))
	})
	// If ARAP produced non-finite UVs (e.g. an ill-conditioned decal that
	// CG couldn't handle), keep the DEM layout rather than overwrite with
	// garbage — the decal will still render, just without the relaxation.
	region.writeBack(vertUV)

	// Rebuild per-triangle UVs from the relaxed vertex UVs. We deliberately
	// keep redundant UV coverage: SampleNearestColor disambiguates by 3D
	// nearest-triangle, so two triangles whose UVs overlap (e.g. ARAP
	// shrinking one sibling into another's footprint) is harmless. An
	// earlier version ran an occupancy rasterizer that rejected such
	// overlaps as fold-backs, but on real meshes with skinny tessellation
	// it routinely dropped legitimate coverage and produced visible gaps.
	for idx, triIdx := range acceptedTris {
		if idx%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			reportProgress(0.90 + 0.10*float64(idx)/float64(len(acceptedTris)))
		}
		f := model.Faces[triIdx]
		var tc [3][2]float32
		for k := 0; k < 3; k++ {
			tc[k] = vertUV[SnapPos(model.Vertices[f[k]])]
		}
		if !triOverlapsTangentBounds(tc) {
			continue
		}
		decal.TriUVs[triIdx] = tangentTrisToUV(tc)
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

// buildStickerTangentFrame returns (t, b, n): the sticker's tangent, bitangent,
// and normal in world coordinates. Matches the convention used by both decal
// builders and the frontend's floating-billboard preview: n along the surface
// normal, t across, b up on the surface; rotationDeg rotates (t,b) around n.
// Positive rotationDeg rotates the sticker image clockwise when viewed from
// outside the surface (matches the intuitive thumbnail rotation in the UI).
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
		// Negate so positive rotationDeg is CW from the outside-viewer's POV.
		rad := -rotationDeg * math.Pi / 180
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
//
// Positive rotationDeg rotates the sticker clockwise when viewed from outside
// the surface (i.e. looking down the normal toward the mesh).
func BuildStickerDecalProjection(
	ctx context.Context,
	model *loader.LoadedModel,
	img image.Image,
	center [3]float64,
	normal [3]float64,
	up [3]float64,
	scale float64,
	rotationDeg float64,
	onProgress func(float64),
) (*StickerDecal, error) {
	reportProgress := func(f float64) {
		if onProgress != nil {
			onProgress(f)
		}
	}
	reportProgress(0)
	defer reportProgress(1.0)
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
		tri        int32
		tcs        [3][2]float32 // tangent-plane coords per vertex
		depths     [3]float32    // depth along +n per vertex
		cx, cy     float32       // centroid tangent coords
		cdepth     float32       // centroid depth
		minU, maxU float32       // tangent-plane AABB
		minV, maxV float32
	}

	// Gather candidates: front-facing (well past edge-on) triangles whose
	// tangent-plane AABB overlaps the sticker rectangle. Zero-area faces are
	// skipped explicitly; we don't rely on faceNormal32's [0,0,1] fallback,
	// which would spuriously pass the front-face test when the sticker
	// normal happens to point +Z.
	var cands []candidate
	totalFaces := len(model.Faces)
	for fi := range model.Faces {
		if fi%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if totalFaces > 0 {
				reportProgress(0.60 * float64(fi) / float64(totalFaces))
			}
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
			minU:   minU, maxU: maxU,
			minV: minV, maxV: maxV,
		})
	}

	decal := &StickerDecal{
		Image:  img,
		TriUVs: make(map[int32][3][2]float32),
	}
	if len(cands) == 0 {
		return decal, nil
	}

	// Occlusion test: rasterize candidates into a depth buffer at sticker
	// resolution. For each pixel of the rect, only the frontmost
	// (largest depth-along-+n) candidate covering that pixel "wins". A
	// candidate is kept iff it wins at least one pixel.
	//
	// This replaces a previous centroid-only test that asked "does some
	// other candidate's tangent triangle cover MY centroid, with greater
	// depth?". That test failed for two distinct shapes:
	//   1. Tall thin triangles whose centroid lies outside the sticker
	//      rect — no other candidate covers a point outside the rect, so
	//      occlusion was never detected even when the front of the mesh
	//      fully obscured the candidate inside the rect.
	//   2. Triangles whose centroid lies inside a tangent-space crack of
	//      the front mesh tiling — adjacent skinny triangles often produce
	//      sub-pixel cracks that miss exact-point lookups.
	// A pixel-grained depth test makes both go away.
	rectW := 2 * fHalfW
	rectH := 2 * fHalfH
	if rectW <= 0 || rectH <= 0 {
		return decal, nil
	}

	// Resolution: aim for a buffer with at least as many pixels as the
	// sticker image itself, so the depth test resolves features at sticker
	// resolution. Cap at 1024 in either dimension so very large stickers
	// don't blow memory.
	depthW := imgBounds.Dx()
	depthH := imgBounds.Dy()
	if depthW < 256 {
		depthW = 256
	}
	if depthH < 256 {
		depthH = 256
	}
	if depthW > 1024 {
		depthW = 1024
	}
	if depthH > 1024 {
		depthH = 1024
	}
	depthBuf := make([]float32, depthW*depthH)
	ownerBuf := make([]int32, depthW*depthH)
	const negInf = float32(-1e30)
	for i := range depthBuf {
		depthBuf[i] = negInf
		ownerBuf[i] = -1
	}

	fW := float32(depthW)
	fH := float32(depthH)
	uToPx := fW / rectW
	vToPy := fH / rectH

	reportProgress(0.60)
	for i := range cands {
		if i%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			reportProgress(0.60 + 0.20*float64(i)/float64(len(cands)))
		}
		c := &cands[i]
		// Pixel AABB clipped to buffer.
		px0 := int(math.Floor(float64((c.minU + fHalfW) * uToPx)))
		py0 := int(math.Floor(float64((c.minV + fHalfH) * vToPy)))
		px1 := int(math.Ceil(float64((c.maxU + fHalfW) * uToPx)))
		py1 := int(math.Ceil(float64((c.maxV + fHalfH) * vToPy)))
		if px0 < 0 {
			px0 = 0
		}
		if py0 < 0 {
			py0 = 0
		}
		if px1 > depthW {
			px1 = depthW
		}
		if py1 > depthH {
			py1 = depthH
		}
		// Rasterize: for each pixel center inside the candidate triangle,
		// keep the deeper-along-+n value (frontmost wins). The barycentric
		// margin is intentionally lenient (negative eps): pixel centers
		// that fall on or near a shared edge between two front triangles
		// are claimed by BOTH, so the depth test runs on both. Without
		// this, near-edge pixels would be unclaimed by any front triangle
		// and a back-of-mesh candidate could win them, producing visible
		// slivers along front-mesh edges.
		const baryEps = float32(1e-3)
		for py := py0; py < py1; py++ {
			cv := -fHalfH + (float32(py)+0.5)/vToPy
			for px := px0; px < px1; px++ {
				cu := -fHalfW + (float32(px)+0.5)/uToPx
				bary, ok := barycentric2D(cu, cv, c.tcs)
				if !ok {
					continue
				}
				if bary[0] < -baryEps || bary[1] < -baryEps || bary[2] < -baryEps {
					continue
				}
				d := bary[0]*c.depths[0] + bary[1]*c.depths[1] + bary[2]*c.depths[2]
				idx := py*depthW + px
				if d > depthBuf[idx] {
					depthBuf[idx] = d
					ownerBuf[idx] = int32(i)
				}
			}
		}
	}

	reportProgress(0.80)
	// Mark winners: any candidate that owns at least one pixel.
	wins := make([]bool, len(cands))
	for _, o := range ownerBuf {
		if o >= 0 {
			wins[o] = true
		}
	}

	// Filter out winners that are far behind the front cluster. Pixels
	// where the front mesh has a tangent-space gap (e.g. a seam in the
	// original tessellation) are won by whatever back-of-mesh surface
	// happens to be front-facing relative to the sticker normal — usually
	// the inside of the far wall of a hollow shape. These winners form a
	// distinct depth cluster well below the legitimate front-surface
	// winners.
	//
	// Find the cluster boundary by sorting winner depths and looking for
	// a gap much larger than the local front-surface variation (5×).
	// This adapts to mesh-specific depth scales (a deep embossing vs. a
	// thin shell) without coupling to the user-controlled sticker
	// `scale`. The default -inf sentinel means "no cut": if there's no
	// qualifying gap, every winner is kept.
	depthFloor := float32(-math.MaxFloat32)
	winnerDepths := make([]float32, 0, len(cands))
	for i, w := range wins {
		if w {
			winnerDepths = append(winnerDepths, cands[i].cdepth)
		}
	}
	if n := len(winnerDepths); n >= 4 {
		sort.Slice(winnerDepths, func(a, b int) bool { return winnerDepths[a] < winnerDepths[b] })
		// Front-cluster spread, measured as the 90th-50th percentile of
		// depth (robust to outliers on either tail).
		p50 := winnerDepths[n/2]
		p90 := winnerDepths[(9*n)/10]
		frontSpread := p90 - p50
		if frontSpread <= 0 {
			frontSpread = 1e-3 // pathological flat surface; tiny non-zero floor
		}
		// Walk gaps top-down from the median. The first gap exceeding
		// 5× front-cluster spread marks the front/back boundary; anything
		// below it is the back cluster (or further-back outliers). Going
		// top-down means a multi-modal back distribution can't trick us
		// into cutting deeper than intended — we stop at the first
		// qualifying gap we encounter walking from the front.
		for i := n / 2; i >= 1; i-- {
			gap := winnerDepths[i] - winnerDepths[i-1]
			if gap > frontSpread*5 {
				depthFloor = winnerDepths[i] // keep this and everything above
				break
			}
		}
	}

	for i, c := range cands {
		if i%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			reportProgress(0.80 + 0.20*float64(i)/float64(len(cands)))
		}
		if !wins[i] {
			continue
		}
		if c.cdepth < depthFloor {
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
	return triNormalFromVerts(model.Vertices[f[0]], model.Vertices[f[1]], model.Vertices[f[2]])
}

// triNormalFromVerts computes the unit normal of a triangle given its three
// vertex positions directly.
func triNormalFromVerts(v0, v1, v2 [3]float32) [3]float32 {
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

// acceptTriSubdividing either accepts the triangle (adds triIdx to
// acceptedTris and seeds vertUV) if its 3D edges are all ≤ threshold, or
// splits it with a 1-to-4 midpoint subdivision and recurses. Children whose
// tc no longer overlaps the sticker rect are dropped, so pathological parent
// triangles whose tc AABB straddles the rect get clipped to just the portion
// that actually falls inside.
//
// The parent's entry in model.Faces is reused for the corner child at vertex
// 0; the other three children are appended. Child per-vertex UVs are
// produced by linearly interpolating the parent's tc across midpoints —
// valid because DEM unfold maps a single triangle isometrically, so any
// sub-region's UVs are the corresponding linear interpolation of the
// parent's UVs.
//
// Adjacency is NOT maintained for subdivided children: BFS never expands
// through them. The parent's original adjacency is used for neighbor
// expansion (caller captures origFace/origNeighbors before calling us). The
// children's face indices are pre-marked visited so that no later BFS
// expansion stumbles onto them.
func acceptTriSubdividing(
	model *loader.LoadedModel,
	acceptedTris *[]int32,
	vertUV map[[3]float32][2]float32,
	visited *[]bool,
	triIdx int32,
	face [3]uint32,
	tc [3][2]float32,
	threshold float32,
	overlapsRect func(tc [3][2]float32) bool,
) {
	if !overlapsRect(tc) {
		return
	}

	v0 := model.Vertices[face[0]]
	v1 := model.Vertices[face[1]]
	v2 := model.Vertices[face[2]]
	l01 := edgeLen3D(v0, v1)
	l12 := edgeLen3D(v1, v2)
	l20 := edgeLen3D(v2, v0)
	maxEdge := l01
	if l12 > maxEdge {
		maxEdge = l12
	}
	if l20 > maxEdge {
		maxEdge = l20
	}

	if maxEdge <= threshold {
		*acceptedTris = append(*acceptedTris, triIdx)
		for k := 0; k < 3; k++ {
			snap := SnapPos(model.Vertices[face[k]])
			if _, ok := vertUV[snap]; !ok {
				vertUV[snap] = tc[k]
			}
		}
		return
	}

	// Midpoint subdivision. mAB = midpoint of edge v0-v1, etc.
	mAB := [3]float32{(v0[0] + v1[0]) / 2, (v0[1] + v1[1]) / 2, (v0[2] + v1[2]) / 2}
	mBC := [3]float32{(v1[0] + v2[0]) / 2, (v1[1] + v2[1]) / 2, (v1[2] + v2[2]) / 2}
	mCA := [3]float32{(v2[0] + v0[0]) / 2, (v2[1] + v0[1]) / 2, (v2[2] + v0[2]) / 2}

	mABIdx := appendMidpointVertex(model, face[0], face[1], mAB)
	mBCIdx := appendMidpointVertex(model, face[1], face[2], mBC)
	mCAIdx := appendMidpointVertex(model, face[2], face[0], mCA)

	tcAB := [2]float32{(tc[0][0] + tc[1][0]) / 2, (tc[0][1] + tc[1][1]) / 2}
	tcBC := [2]float32{(tc[1][0] + tc[2][0]) / 2, (tc[1][1] + tc[2][1]) / 2}
	tcCA := [2]float32{(tc[2][0] + tc[0][0]) / 2, (tc[2][1] + tc[0][1]) / 2}

	child0Face := [3]uint32{face[0], mABIdx, mCAIdx} // corner at v0
	child1Face := [3]uint32{mABIdx, face[1], mBCIdx} // corner at v1
	child2Face := [3]uint32{mCAIdx, mBCIdx, face[2]} // corner at v2
	child3Face := [3]uint32{mABIdx, mBCIdx, mCAIdx}  // central inverted triangle

	// Parent's face index is reused for child0; the other three are appended.
	// Per-face attribute arrays inherit the parent's values (material,
	// texture, base color, etc.) so they stay aligned with model.Faces.
	model.Faces[triIdx] = child0Face
	child1Idx := appendSubdividedFace(model, int(triIdx), child1Face)
	child2Idx := appendSubdividedFace(model, int(triIdx), child2Face)
	child3Idx := appendSubdividedFace(model, int(triIdx), child3Face)

	*visited = append(*visited, true, true, true)

	acceptTriSubdividing(model, acceptedTris, vertUV, visited,
		triIdx, child0Face, [3][2]float32{tc[0], tcAB, tcCA}, threshold, overlapsRect)
	acceptTriSubdividing(model, acceptedTris, vertUV, visited,
		child1Idx, child1Face, [3][2]float32{tcAB, tc[1], tcBC}, threshold, overlapsRect)
	acceptTriSubdividing(model, acceptedTris, vertUV, visited,
		child2Idx, child2Face, [3][2]float32{tcCA, tcBC, tc[2]}, threshold, overlapsRect)
	acceptTriSubdividing(model, acceptedTris, vertUV, visited,
		child3Idx, child3Face, [3][2]float32{tcAB, tcBC, tcCA}, threshold, overlapsRect)
}

func edgeLen3D(a, b [3]float32) float32 {
	dx := a[0] - b[0]
	dy := a[1] - b[1]
	dz := a[2] - b[2]
	return float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
}

// appendMidpointVertex appends a vertex at `pos` and, if present, per-vertex
// attribute entries (UVs, VertexColors) to keep those slices aligned with
// model.Vertices. Attributes are averaged from the two endpoint vertices for
// a reasonable guess when this vertex is reached in a shaded preview.
func appendMidpointVertex(model *loader.LoadedModel, aIdx, bIdx uint32, pos [3]float32) uint32 {
	idx := uint32(len(model.Vertices))
	model.Vertices = append(model.Vertices, pos)
	if model.UVs != nil {
		ua := model.UVs[aIdx]
		ub := model.UVs[bIdx]
		model.UVs = append(model.UVs, [2]float32{(ua[0] + ub[0]) / 2, (ua[1] + ub[1]) / 2})
	}
	if model.VertexColors != nil {
		ca := model.VertexColors[aIdx]
		cb := model.VertexColors[bIdx]
		model.VertexColors = append(model.VertexColors, [4]uint8{
			uint8((uint16(ca[0]) + uint16(cb[0])) / 2),
			uint8((uint16(ca[1]) + uint16(cb[1])) / 2),
			uint8((uint16(ca[2]) + uint16(cb[2])) / 2),
			uint8((uint16(ca[3]) + uint16(cb[3])) / 2),
		})
	}
	return idx
}

// appendSubdividedFace appends a new face that is a sub-triangle of the face
// at parentIdx, copying every per-face attribute from the parent so every
// per-face slice stays aligned with model.Faces. Returns the new face index.
func appendSubdividedFace(model *loader.LoadedModel, parentIdx int, face [3]uint32) int32 {
	idx := int32(len(model.Faces))
	model.Faces = append(model.Faces, face)
	if model.FaceTextureIdx != nil {
		model.FaceTextureIdx = append(model.FaceTextureIdx, model.FaceTextureIdx[parentIdx])
	}
	if model.FaceAlpha != nil {
		model.FaceAlpha = append(model.FaceAlpha, model.FaceAlpha[parentIdx])
	}
	if model.FaceBaseColor != nil {
		model.FaceBaseColor = append(model.FaceBaseColor, model.FaceBaseColor[parentIdx])
	}
	if model.NoTextureMask != nil {
		model.NoTextureMask = append(model.NoTextureMask, model.NoTextureMask[parentIdx])
	}
	if model.FaceMeshIdx != nil {
		model.FaceMeshIdx = append(model.FaceMeshIdx, model.FaceMeshIdx[parentIdx])
	}
	return idx
}

// tcRunaway returns true when any vertex of the triangle's tc sits more than
// 2× the sticker's half-extent outside the sticker rect. DEM unfold of a
// pathologically large mesh triangle produces tc values like that; propagating
// them through BFS poisons downstream neighbors' unfolds, so we stop here.
// The 2× pad lets legitimate triangles that barely poke past the sticker
// boundary continue to propagate.
func tcRunaway(tc [3][2]float32, fHalfW, fHalfH float32) bool {
	const padMul = 2
	limitU := padMul * fHalfW
	limitV := padMul * fHalfH
	for k := 0; k < 3; k++ {
		if tc[k][0] > limitU || tc[k][0] < -limitU {
			return true
		}
		if tc[k][1] > limitV || tc[k][1] < -limitV {
			return true
		}
	}
	return false
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
