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
// as-rigid-as-possible with respect to the 3D geometry. Occupancy
// rejection runs on the final relaxed UVs, so ARAP gets a chance to fix
// distortion before we decide which triangles to keep.
//
// Positive rotationDeg rotates the sticker clockwise when viewed from outside
// the surface (i.e. looking down the normal toward the mesh).
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

	// UV-space occupancy bitmap. Tracks which regions of the sticker have
	// been claimed. The BFS rejects a triangle whose pixel-center footprint
	// is majority-claimed, preventing the sticker from repeating when
	// geodesic unfolding folds back on non-developable surfaces.
	//
	// Resolution is decoupled from the sticker image: the grid must be fine
	// enough that adjacent mesh triangles don't share pixel centers, else
	// they falsely reject each other. The smallest feature that can survive
	// the output pipeline is one voxel wide, so sizing the occupancy grid
	// so that each voxel-length span is covered by several pixels gives
	// adequate headroom. We oversample by 4× per voxel.
	const occPixelsPerVoxel = 4
	occW := imgBounds.Dx()
	occH := imgBounds.Dy()
	if voxelSize > 0 {
		wVoxel := int(math.Ceil(scale / voxelSize * occPixelsPerVoxel))
		hVoxel := int(math.Ceil(scale * aspect / voxelSize * occPixelsPerVoxel))
		if wVoxel > occW {
			occW = wVoxel
		}
		if hVoxel > occH {
			occH = hVoxel
		}
	}
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

	// Rebuild per-triangle UVs from the relaxed vertex UVs and apply the
	// occupancy rasterizer. After ARAP, the 2D layout matches 3D edge
	// lengths closely, so genuine fold-backs (non-developable surface
	// wrapping back on itself) are the main thing the rasterizer catches.
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
		uvs := tangentTrisToUV(tc)
		if !checkAndPaintTriangle(uvs, occupancy, occW, occH) {
			continue
		}
		decal.TriUVs[triIdx] = uvs
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

// checkAndPaintTriangle rasterizes the triangle's UV footprint into the
// occupancy bitmap. If the majority of covered pixel centers are already
// claimed by earlier triangles (a geodesic fold-back), the bitmap is left
// unchanged and false is returned. Otherwise the covered pixels are marked
// claimed and true is returned.
//
// Using the same "pixel center inside triangle" test as the claim step
// guarantees that adjacent triangles have disjoint footprints — so two
// sub-pixel neighbors along a shared edge never mutually reject, even when
// the sticker is scaled far larger than the individual mesh triangles.
//
// Sub-pixel triangles (no pixel center lands inside) are always accepted
// and leave the occupancy bitmap untouched.
func checkAndPaintTriangle(uvs [3][2]float32, occ []bool, w, h int) bool {
	fw, fh := float32(w), float32(h)
	minPx := int(max(0, min(uvs[0][0], min(uvs[1][0], uvs[2][0]))*fw))
	maxPx := int(min(fw-1, max(uvs[0][0], max(uvs[1][0], uvs[2][0]))*fw))
	minPy := int(max(0, min(uvs[0][1], min(uvs[1][1], uvs[2][1]))*fh))
	maxPy := int(min(fh-1, max(uvs[0][1], max(uvs[1][1], uvs[2][1]))*fh))

	var pixels []int
	claimed := 0
	for py := minPy; py <= maxPy; py++ {
		for px := minPx; px <= maxPx; px++ {
			u := (float32(px) + 0.5) / fw
			v := (float32(py) + 0.5) / fh
			if !pointStrictlyInsideTriangle2D(u, v, uvs) {
				continue
			}
			idx := py*w + px
			pixels = append(pixels, idx)
			if occ[idx] {
				claimed++
			}
		}
	}
	if len(pixels) > 0 && claimed*2 >= len(pixels) {
		return false
	}
	for _, idx := range pixels {
		occ[idx] = true
	}
	return true
}

// pointStrictlyInsideTriangle2D returns true if (px,py) is strictly inside
// the 2D triangle defined by uvs, with a small barycentric margin (eps=1e-4,
// ~1000× float32 relative precision) that excludes points within roundoff
// distance of any edge. The margin is essential for occupancy tracking:
// without it, a pixel center that lies near-exactly on a shared edge
// between two triangles can be reported as "inside" both due to roundoff,
// causing adjacent triangles to wrongly fight over the pixel. Excluding
// the margin from both sides leaves the thin band near each edge unclaimed,
// which is harmless.
func pointStrictlyInsideTriangle2D(px, py float32, uvs [3][2]float32) bool {
	x0, y0 := uvs[0][0], uvs[0][1]
	x1, y1 := uvs[1][0], uvs[1][1]
	x2, y2 := uvs[2][0], uvs[2][1]

	// Edge functions (signed double-areas of sub-triangles).
	f0 := (x1-x0)*(py-y0) - (y1-y0)*(px-x0) // edge V0→V1
	f1 := (x2-x1)*(py-y1) - (y2-y1)*(px-x1) // edge V1→V2
	f2 := (x0-x2)*(py-y2) - (y0-y2)*(px-x2) // edge V2→V0

	twoArea := f0 + f1 + f2
	absTwoArea := twoArea
	if absTwoArea < 0 {
		absTwoArea = -absTwoArea
	}
	if absTwoArea < 1e-20 {
		return false // degenerate
	}

	// Barycentric coordinate > eps on all three edges means the point is
	// strictly inside, away from any edge.
	const eps = 1e-4
	threshold := eps * absTwoArea
	if twoArea > 0 {
		return f0 > threshold && f1 > threshold && f2 > threshold
	}
	return f0 < -threshold && f1 < -threshold && f2 < -threshold
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

	// Occlusion test: for each candidate, check if any OTHER candidate is
	// closer to the projector at its centroid (t,b). If so, drop it.
	//
	// Accelerate with a uniform 2D grid over the sticker rectangle: each cell
	// lists candidates whose tangent-plane AABB overlaps that cell. To test a
	// candidate's centroid, only candidates in the containing cell need a
	// point-in-triangle query. This turns an O(N²) sweep into ~O(N·N/cells).
	//
	// depthEps scales with sticker size so coplanar surfaces within 0.01%
	// of the sticker's width are treated as ties rather than occluders.
	const baryEps = float32(1e-4)
	depthEps := float32(scale) * 1e-4

	rectW := 2 * fHalfW
	rectH := 2 * fHalfH
	if rectW <= 0 || rectH <= 0 {
		return decal, nil
	}
	gridDim := int(math.Sqrt(float64(len(cands))))
	if gridDim < 1 {
		gridDim = 1
	}
	if gridDim > 256 {
		gridDim = 256
	}
	invCellU := float32(gridDim) / rectW
	invCellV := float32(gridDim) / rectH
	clampCell := func(v int) int {
		if v < 0 {
			return 0
		}
		if v >= gridDim {
			return gridDim - 1
		}
		return v
	}
	cellOf := func(u, v float32) (int, int) {
		ix := int((u + fHalfW) * invCellU)
		iy := int((v + fHalfH) * invCellV)
		return clampCell(ix), clampCell(iy)
	}
	grid := make([][]int32, gridDim*gridDim)
	for i := range cands {
		c := &cands[i]
		ix0, iy0 := cellOf(c.minU, c.minV)
		ix1, iy1 := cellOf(c.maxU, c.maxV)
		for iy := iy0; iy <= iy1; iy++ {
			row := iy * gridDim
			for ix := ix0; ix <= ix1; ix++ {
				grid[row+ix] = append(grid[row+ix], int32(i))
			}
		}
	}

	reportProgress(0.60)
	for i, c := range cands {
		if i%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			reportProgress(0.60 + 0.40*float64(i)/float64(len(cands)))
		}
		ix, iy := cellOf(c.cx, c.cy)
		bucket := grid[iy*gridDim+ix]
		occluded := false
		for _, j := range bucket {
			if int(j) == i {
				continue
			}
			other := &cands[j]
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
