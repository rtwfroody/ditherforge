// 2D per-slab clip: cuts each model triangle into per-cell fragments
// without any 3D boolean.
//
// Pipeline (per source triangle):
//
//  1. sliceTriangleToSlab — Sutherland-Hodgman against the slab's
//     z=zBot / z=zTop planes. Yields one planar 3D sub-polygon
//     (3-to-7 vertices, convex) that lives in [zBot, zTop] and the
//     source triangle's plane.
//
//  2. clipPolyToCells — intersect that sub-polygon against each
//     candidate cell's outer polygon. Internally dispatches to a
//     Clipper 2D path (when the sub-polygon has measurable XY area;
//     Z is recovered from the source plane equation) or a vertical-
//     scan path (when its XY projection is degenerate, i.e. the
//     source triangle was near-vertical). Both paths emit cellPieces
//     with full 3D vertices.
//
//  3. appendCellPiece — splice each cell-piece against the slab-wide
//     3D vertex union (to eliminate T-junctions), triangulate, and
//     emit faces tagged with the cell index.
//
// Replaces the per-cell CGAL clip_surface path that used to live in
// clip.go: a 1.2M-cell pipeline runs in seconds instead of hours,
// with no CGAL setup amortization or thread-safety concerns.

package cellslicer

import (
	"math"
	"runtime"
	"sync"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// slabPoly is one source triangle clipped against a slab's Z range.
// Vertices are stored in mesh coords (full 3D), wound in the source
// triangle's order. The polygon is planar (it lives in the source
// triangle's plane) and convex (Z-clipping a triangle with two
// half-spaces preserves convexity).
//
// Normal is the source triangle's facing direction, cross-product
// of its edges. Not unit-normalized — only its direction is used
// downstream (winding decisions in appendCellPiece, dominant-axis
// pick for Earcut projection, and the cap Z-lift's plane equation,
// which is invariant to a uniform scale of n).
type slabPoly struct {
	Pts    [][3]float32
	Normal [3]float32
}

// ClipMeshToCells2D returns a mesh whose faces are fragments of the
// input model, each tagged with the global cell index it falls in.
// For each slab, every model triangle is Z-clipped to the slab and
// then 2D-clipped against each candidate cell's outer polygon.
//
// Runs as two slab-parallel passes with a barrier between them:
// Phase 1 (clip slabPolys, build per-slab seen3D) has no cross-slab
// dependency; Phase 2 (splice + emit) needs every slab's Phase 1
// seen3D in order to contribute neighbour boundary vertices on the
// shared Z planes. Each pass uses runtime.NumCPU() workers; details
// of the splice/triangulation are in clip2d_subdivide.go.
func ClipMeshToCells2D(model *loader.LoadedModel, slabs []Slab, triIdx *TriXYZIndex) (ClipResult, error) {
	offsets := make([]int, len(slabs)+1)
	for si := range slabs {
		offsets[si+1] = offsets[si] + len(slabs[si].Cells)
	}

	// Pre-slice every model triangle into per-slab pieces.
	slabPolys := make([][]slabPoly, len(slabs))
	for ti := range model.Faces {
		f := model.Faces[ti]
		a := model.Vertices[f[0]]
		b := model.Vertices[f[1]]
		c := model.Vertices[f[2]]
		zMin := minf3(a[2], b[2], c[2])
		zMax := maxf3(a[2], b[2], c[2])
		siLo := 0
		siHi := len(slabs) - 1
		for siLo <= siHi && slabs[siLo].ZTop < zMin {
			siLo++
		}
		for siHi >= siLo && slabs[siHi].ZBot > zMax {
			siHi--
		}
		for si := siLo; si <= siHi; si++ {
			if si < 0 || si >= len(slabs) {
				continue
			}
			s := &slabs[si]
			if poly := sliceTriangleToSlab(a, b, c, s.ZBot, s.ZTop); poly != nil {
				slabPolys[si] = append(slabPolys[si], *poly)
			}
		}
	}
	_ = triIdx

	// Per-slab cell-bbox indices, built once. Re-used by every slab
	// polygon during candidate-cell lookup.
	cellIndices := make([]*slabCellIndex, len(slabs))
	for si := range slabs {
		if len(slabs[si].Cells) > 0 {
			cellIndices[si] = buildSlabCellIndex(&slabs[si])
		}
	}

	// Two-pass per-slab parallelism with a barrier between phases:
	//
	//   Phase 1 (all slabs concurrent): clip every slabPoly against
	//   candidate cells, collecting cellPieces and a slab-wide seen3D
	//   set. No cross-slab dependencies.
	//
	//   Phase 2 (all slabs concurrent, after barrier): splice each
	//   cellPiece against (own seen3D) ∪ (neighbour-below's vertices
	//   on the shared zBot plane) ∪ (neighbour-above's vertices on
	//   the shared zTop plane), then Earcut and emit. Splice is read-
	//   only against the frozen seen3D maps, so no synchronization is
	//   needed once Phase 1 is done.
	//
	// The slab-wide seen3D eliminates within-slab T-junctions (e.g.
	// cube cap's STL diagonal between two source tris). The cross-slab
	// boundary contribution eliminates T-junctions on the shared Z
	// plane between adjacent slabs, whose cell partitions differ.
	type slabPhase1 struct {
		pieces []cellPiece
		seen3D map[int3D]struct{}
	}
	type slabResult struct {
		verts        [][3]float32
		faces        [][3]uint32
		localCellIdx []int32 // slab-local cell idx for each face
	}
	// Memory note: every slab's Phase 1 result lives until the barrier
	// completes (Phase 2 needs the neighbour seen3D maps). The old
	// fused-worker code recycled each slab's intermediate immediately;
	// this version's peak live set is roughly the sum across all slabs.
	// Bounded by the eventual output mesh size, so not concerning for
	// printable-object workloads — revisit if a memory regression
	// shows up on a very large model.
	phase1 := make([]slabPhase1, len(slabs))
	results := make([]slabResult, len(slabs))

	nWorkers := runtime.NumCPU()
	if nWorkers > len(slabs) {
		nWorkers = len(slabs)
	}
	if nWorkers < 1 {
		nWorkers = 1
	}

	// Phase 1 — clip slabPolys, build seen3D.
	//
	// NOTE: splice_diag_test.go:runPhase1ForDiag mirrors this loop for
	// the SPLICE_DIAG diagnostic — the inner clipPolyToCells call and
	// the seen3D semantics AND the worker fan-out (NumCPU goroutines,
	// jobCh, per-worker candidates buffer). Any change to either layer
	// here must be reflected there or the diagnostic will silently
	// report against a stale algorithm.
	{
		jobCh := make(chan int, len(slabs))
		for si := range slabs {
			jobCh <- si
		}
		close(jobCh)
		var wg sync.WaitGroup
		for w := 0; w < nWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var candidates []int
				for si := range jobCh {
					idx := cellIndices[si]
					if idx == nil {
						continue
					}
					seen3D := make(map[int3D]struct{}, 64)
					var pieces []cellPiece
					for _, p := range slabPolys[si] {
						pieces, candidates = clipPolyToCells(p, si, slabs, idx, pieces, seen3D, candidates)
					}
					phase1[si] = slabPhase1{pieces: pieces, seen3D: seen3D}
				}
			}()
		}
		wg.Wait()
	}

	// Filter helper: returns the int3D Z value for a slab boundary
	// plane, so Phase 2 workers can pick neighbour seen3D entries on
	// the shared plane with an exact == on the 1µm-quantized Z.
	//
	// The exact-equality filter relies on:
	//   - clipPolygonByZHalfSpace's lerpAtZ writes z = zPlane verbatim
	//     (slab Z-clip output).
	//   - clipPolyToCellsCap clips slabPolys against cell prisms in 3D
	//     via Sutherland-Hodgman (clipPolyByPlaneXY), whose lerp
	//     interpolates Z linearly along an edge. The slabPoly is
	//     planar; any new vertex from intersecting one of its edges
	//     with a vertical cell face lies on that same plane, and
	//     linear lerp between two on-plane endpoints stays on the
	//     plane exactly. Combined with the slab Z-clip placing the
	//     top/bottom slab-plane vertices at zPlane verbatim, no
	//     boundary vertex drifts off the plane — no clamp needed.
	//     (See commit 21b7b25 for context: the previous Clipper-2D-
	//     then-re-lift cap path drifted by |grad_xy(z)| × 1µm on
	//     slanted near-walls, which is what motivated dropping it.)
	//   - clipPolyByPlaneXY's lerpAtPlaneXY interpolates Z linearly;
	//     both endpoints on the slab plane → exact plane Z out.
	//
	// A vertex that drifts past 1µm and slips the filter would just
	// fail to participate in the cross-slab splice for that neighbour
	// (manifoldness degrades locally; geometry stays valid).
	planeZInt := func(z float32) int64 {
		return int64(math.Round(float64(z) * clipperScale))
	}

	// Phase 2 — splice + emit, with neighbour boundary contributions.
	{
		jobCh := make(chan int, len(slabs))
		for si := range slabs {
			jobCh <- si
		}
		close(jobCh)
		var wg sync.WaitGroup
		for w := 0; w < nWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for si := range jobCh {
					p1 := phase1[si]
					if len(p1.pieces) == 0 {
						continue
					}
					splice3D := make([]int3D, 0, len(p1.seen3D))
					for p := range p1.seen3D {
						splice3D = append(splice3D, p)
					}
					// Neighbour below: vertices on slabs[si].ZBot.
					if si > 0 {
						zb := planeZInt(slabs[si].ZBot)
						for p := range phase1[si-1].seen3D {
							if p.Z == zb {
								splice3D = append(splice3D, p)
							}
						}
					}
					// Neighbour above: vertices on slabs[si].ZTop.
					if si+1 < len(slabs) {
						zt := planeZInt(slabs[si].ZTop)
						for p := range phase1[si+1].seen3D {
							if p.Z == zt {
								splice3D = append(splice3D, p)
							}
						}
					}

					var res slabResult
					for _, pc := range p1.pieces {
						res.verts, res.faces, res.localCellIdx = appendCellPiece(pc, splice3D, res.verts, res.faces, res.localCellIdx)
					}
					results[si] = res
				}
			}()
		}
		wg.Wait()
	}

	totalV, totalF := 0, 0
	for _, r := range results {
		totalV += len(r.verts)
		totalF += len(r.faces)
	}
	cr := ClipResult{
		Verts:       make([][3]float32, 0, totalV),
		Faces:       make([][3]uint32, 0, totalF),
		FaceCellIdx: make([]int32, 0, totalF),
	}
	for si, r := range results {
		if len(r.faces) == 0 {
			continue
		}
		base := uint32(len(cr.Verts))
		off := int32(offsets[si])
		cr.Verts = append(cr.Verts, r.verts...)
		for i, f := range r.faces {
			cr.Faces = append(cr.Faces, [3]uint32{f[0] + base, f[1] + base, f[2] + base})
			cr.FaceCellIdx = append(cr.FaceCellIdx, off+r.localCellIdx[i])
		}
	}

	// Cross-piece vertex dedup. appendCellPiece emits fresh vertex
	// IDs per cell-fragment, so adjacent fragments sharing a boundary
	// vertex (guaranteed coincident by the slab-wide seen3D splice in
	// Clipper integer space) end up with distinct vertex IDs. Without
	// dedup, downstream slicing reads each fragment in isolation and
	// the first-layer cross-section comes out as N disconnected
	// segments → Orca reports "empty initial layer". Dedup by
	// int3DOf (1µm-quantized 3D position) — same key the splice set
	// uses, so coincident-coord verts hash equal. Cross-slab dedup
	// works for free because slabs[k].ZTop and slabs[k+1].ZBot come
	// from the same planes[k+1] float32.
	if len(cr.Verts) > 0 {
		seen := make(map[int3D]uint32, len(cr.Verts)/3)
		remap := make([]uint32, len(cr.Verts))
		// In-place compaction: kept aliases cr.Verts. Safe because
		// len(kept) <= i+1 throughout, so the append's write at
		// kept[len(kept)] never overtakes the range loop's read at
		// cr.Verts[i+1].
		kept := cr.Verts[:0]
		for i, v := range cr.Verts {
			key := int3DOf(v)
			id, ok := seen[key]
			if !ok {
				id = uint32(len(kept))
				seen[key] = id
				kept = append(kept, v)
			}
			remap[i] = id
		}
		cr.Verts = kept
		for i, f := range cr.Faces {
			cr.Faces[i] = [3]uint32{remap[f[0]], remap[f[1]], remap[f[2]]}
		}
	}
	return cr, nil
}

// sliceTriangleToSlab clips triangle (a,b,c) against the half-spaces
// z >= zBot and z <= zTop and returns the resulting planar 3D
// sub-polygon, or nil if the triangle does not overlap the slab.
// The output polygon's vertices stay in the source triangle's plane;
// downstream code chooses how to project to 2D for cell clipping.
func sliceTriangleToSlab(a, b, c [3]float32, zBot, zTop float32) *slabPoly {
	// Drop fully outside.
	zMin := minf3(a[2], b[2], c[2])
	zMax := maxf3(a[2], b[2], c[2])
	if zMax < zBot || zMin > zTop {
		return nil
	}
	// Build the sub-polygon by clipping against z >= zBot then z <= zTop.
	poly := [][3]float32{a, b, c}
	poly = clipPolygonByZHalfSpace(poly, zBot, true /* keep z >= zBot */)
	if len(poly) < 3 {
		return nil
	}
	poly = clipPolygonByZHalfSpace(poly, zTop, false /* keep z <= zTop */)
	if len(poly) < 3 {
		return nil
	}
	return &slabPoly{
		Pts:    poly,
		Normal: triangleNormal(a, b, c),
	}
}

// isPolyXYDegenerate reports whether the slab-clipped polygon's XY
// projection has insufficient area for the Clipper-based cap clip
// (which lifts Z from the source plane equation: z = (d - n.x*x -
// n.y*y) / n.z, where n.z is proportional to the XY signed area).
// For polygons that come from a near-vertical source triangle, n.z
// is near zero and the lift is ill-conditioned; route to the
// vertical-scan path instead.
//
// The relative threshold uses max(xRange, yRange)² as the scale, not
// bbox-area, so it survives the axis-aligned case: a triangle on a
// Y=constant or X=constant plane (a flat cube wall) collapses its
// XY bbox to zero area in one dimension, which would otherwise zero
// out a bbox-relative threshold and let float-precision noise (~3e-5
// from shoelace cancellation on a 20-unit polygon) slip past,
// dropping every wall fragment in that slab. Found 2026-05-15 on the
// cube's -Y face.
func isPolyXYDegenerate(pts [][3]float32) bool {
	if len(pts) < 3 {
		return true
	}
	areaXY := polygonXYSignedArea(pts)
	xMin, yMin := pts[0][0], pts[0][1]
	xMax, yMax := xMin, yMin
	for _, p := range pts[1:] {
		if p[0] < xMin {
			xMin = p[0]
		} else if p[0] > xMax {
			xMax = p[0]
		}
		if p[1] < yMin {
			yMin = p[1]
		} else if p[1] > yMax {
			yMax = p[1]
		}
	}
	scale := xMax - xMin
	if yr := yMax - yMin; yr > scale {
		scale = yr
	}
	return absf(areaXY) < 1e-6*scale*scale || absf(areaXY) < 1e-12
}

// clipPolygonByZHalfSpace clips polygon by a Z half-space.
//
//	keepGreater = true  → keep z >= zPlane
//	keepGreater = false → keep z <= zPlane
//
// Standard Sutherland-Hodgman.
func clipPolygonByZHalfSpace(poly [][3]float32, zPlane float32, keepGreater bool) [][3]float32 {
	if len(poly) == 0 {
		return nil
	}
	out := make([][3]float32, 0, len(poly)+2)
	inside := func(p [3]float32) bool {
		if keepGreater {
			return p[2] >= zPlane
		}
		return p[2] <= zPlane
	}
	n := len(poly)
	for i := 0; i < n; i++ {
		s := poly[(i-1+n)%n]
		e := poly[i]
		sIn := inside(s)
		eIn := inside(e)
		if eIn {
			if !sIn {
				out = append(out, lerpAtZ(s, e, zPlane))
			}
			out = append(out, e)
		} else if sIn {
			out = append(out, lerpAtZ(s, e, zPlane))
		}
	}
	return out
}

// lerpAtZ returns the point on segment a→b at Z = z.
func lerpAtZ(a, b [3]float32, z float32) [3]float32 {
	if absf(b[2]-a[2]) < 1e-12 {
		return a
	}
	t := (z - a[2]) / (b[2] - a[2])
	return [3]float32{
		a[0] + t*(b[0]-a[0]),
		a[1] + t*(b[1]-a[1]),
		z,
	}
}

func polyBoundsP2(pts []Point2) (minX, minY, maxX, maxY float32) {
	minX, minY = pts[0][0], pts[0][1]
	maxX, maxY = pts[0][0], pts[0][1]
	for _, p := range pts[1:] {
		if p[0] < minX {
			minX = p[0]
		}
		if p[1] < minY {
			minY = p[1]
		}
		if p[0] > maxX {
			maxX = p[0]
		}
		if p[1] > maxY {
			maxY = p[1]
		}
	}
	return
}

func absf(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}
