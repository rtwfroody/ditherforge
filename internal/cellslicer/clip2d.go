// 2D per-slab clip: cuts each model triangle into per-cell fragments
// without any 3D boolean. The work is two cheap plane clips per
// (triangle, slab, cell) tuple:
//
//  1. Sutherland-Hodgman against the slab's z=zBot / z=zTop planes
//     (clipPolygonByZHalfSpace) — the triangle becomes a sub-polygon
//     living in [zBot, zTop].
//  2. Clipper 2D polygon intersection of that sub-polygon's XY
//     projection with the cell's Outer polygon, then lift the result
//     back to 3D via barycentric weights on the source triangle.
//
// Replaces the per-cell CGAL clip_surface path that used to live in
// clip.go: a 1.2M-cell pipeline runs in seconds instead of hours,
// with no CGAL setup amortization or thread-safety concerns.

package cellslicer

import (
	"runtime"
	"sync"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// slabTri is a triangle that lies entirely within a slab's Z range.
// Vertices are stored in mesh coords; Z lifting from a 2D (x, y) is
// done via barycentric interpolation in emitCapPiece, which is
// numerically stable for any non-degenerate XY projection (avoiding
// the divide-by-near-zero that the plane equation suffers on
// near-vertical triangles). Triangles whose XY projection has zero
// area are dropped at sliceTriangleToSlab time.
type slabTri struct {
	V0, V1, V2 [3]float32
	// InvAreaXY is 1 / signed_area_xy(V0,V1,V2), precomputed so
	// per-point barycentric weights are 3 multiplies + 3 cross-
	// product evaluations.
	InvAreaXY float32
}

// ClipMeshToCells2D returns a mesh whose faces are fragments of the
// input model, each tagged with the global cell index it falls in.
// For each slab, every model triangle is Z-clipped to the slab and
// then 2D-clipped against each candidate cell's outer polygon, and
// the result is lifted back to 3D via barycentric weights on the
// source triangle.
//
// Parallelized per slab (runtime.NumCPU() workers). Within a slab
// the work is serial because Phase 1 and Phase 2 share slab-wide
// vertex sets (seen2D / seen3D) used to eliminate T-junctions
// across cells and across source triangles — see clip2d_subdivide.go.
func ClipMeshToCells2D(model *loader.LoadedModel, slabs []Slab, triIdx *TriXYZIndex) (ClipResult, error) {
	offsets := make([]int, len(slabs)+1)
	for si := range slabs {
		offsets[si+1] = offsets[si] + len(slabs[si].Cells)
	}

	// Pre-slice every model triangle into per-slab pieces.
	slabTris := make([][]slabTri, len(slabs))
	slabVerticals := make([][]slabVerticalPoly, len(slabs))
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
			pieces, vpoly := sliceTriangleToSlab(a, b, c, s.ZBot, s.ZTop)
			if len(pieces) > 0 {
				slabTris[si] = append(slabTris[si], pieces...)
			}
			if vpoly != nil {
				slabVerticals[si] = append(slabVerticals[si], *vpoly)
			}
		}
	}
	_ = triIdx

	// Per-slab cell-bbox indices, built once. Re-used by every slab
	// triangle (and vertical) during candidate-cell lookup.
	cellIndices := make([]*slabCellIndex, len(slabs))
	for si := range slabs {
		if len(slabs[si].Cells) > 0 {
			cellIndices[si] = buildSlabCellIndex(&slabs[si])
		}
	}

	// Per-slab parallelism: each slab's per-tri subdivision is
	// independent and produces its own verts/faces/cellIdx slices
	// that the reducer concatenates in slab order.
	type slabResult struct {
		verts        [][3]float32
		faces        [][3]uint32
		localCellIdx []int32 // slab-local cell idx for each face
	}
	results := make([]slabResult, len(slabs))

	nWorkers := runtime.NumCPU()
	if nWorkers > len(slabs) {
		nWorkers = len(slabs)
	}
	if nWorkers < 1 {
		nWorkers = 1
	}
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
				zBot := slabs[si].ZBot
				zTop := slabs[si].ZTop

				// Phase 1: clip all source tris/verticals against
				// candidate cells, collecting boundary vertices into
				// slab-wide splice sets. The slab-wide sets eliminate
				// T-junctions across source-tri boundaries (e.g. the
				// cube cap's STL diagonal where both source tris
				// meet — without the slab-wide union, each tri's
				// splice only saw its own cells' vertices and the
				// diagonal subdivision didn't match between sides).
				seen2D := make(map[intPt]struct{}, 64)
				seen3D := make(map[int3D]struct{}, 16)
				var capPieces []capPiece
				var wallPieces []wallPiece
				for _, t := range slabTris[si] {
					capPieces, candidates = clipSlabTriPieces(t, si, slabs, idx, capPieces, seen2D, candidates)
				}
				for _, vp := range slabVerticals[si] {
					wallPieces, candidates = clipSlabVerticalPieces(vp, si, slabs, idx, wallPieces, seen3D, candidates)
				}
				if len(capPieces) == 0 && len(wallPieces) == 0 {
					continue
				}

				// Promote slab-wide sets to slices.
				splice2D := make([]intPt, 0, len(seen2D))
				for p := range seen2D {
					splice2D = append(splice2D, p)
				}
				splice3D := make([]int3D, 0, len(seen3D))
				for p := range seen3D {
					splice3D = append(splice3D, p)
				}

				// Phase 2: splice each piece against the slab-wide
				// union and emit.
				var res slabResult
				for _, pc := range capPieces {
					res.verts, res.faces, res.localCellIdx = appendCapPiece(pc, splice2D, zBot, zTop, res.verts, res.faces, res.localCellIdx)
				}
				for _, pc := range wallPieces {
					res.verts, res.faces, res.localCellIdx = appendWallPiece(pc, splice3D, res.verts, res.faces, res.localCellIdx)
				}
				results[si] = res
			}
		}()
	}
	wg.Wait()

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

	// Cross-piece vertex dedup. appendCapPiece/appendWallPiece emit
	// fresh vertex IDs per cell-fragment, so adjacent fragments sharing
	// a boundary vertex (guaranteed coincident by the slab-wide
	// seen2D/seen3D splice in Clipper integer space) end up with
	// distinct vertex IDs. Without dedup, downstream slicing reads
	// each wall fragment in isolation and the first-layer cross-section
	// comes out as N disconnected segments → Orca reports "empty
	// initial layer". Dedup by int3DOf (1µm-quantized Clipper-integer
	// 3D position) — same key the splice sets use, so coincident-coord
	// verts hash equal. Cross-slab dedup works for free because
	// slabs[k].ZTop and slabs[k+1].ZBot come from the same planes[k+1]
	// float32.
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
// z >= zBot and z <= zTop and returns the resulting pieces split by
// type:
//
//   - slabTris: pieces with non-degenerate XY projection. Each one
//     is a triangle of a fan over the sub-polygon; later code lifts
//     Z via barycentric weights on the XY projection.
//   - slabVerticalPoly (zero or one entry): the whole slab-clipped
//     sub-polygon when its XY projection has effectively zero area
//     (a near-vertical source triangle). XY-barycentric lift is
//     ill-defined for these, so a separate path clips them in 3D
//     against each cell's prism — see clip2d_vertical.go.
func sliceTriangleToSlab(a, b, c [3]float32, zBot, zTop float32) ([]slabTri, *slabVerticalPoly) {
	// Drop fully outside.
	zMin := minf3(a[2], b[2], c[2])
	zMax := maxf3(a[2], b[2], c[2])
	if zMax < zBot || zMin > zTop {
		return nil, nil
	}
	// Build the sub-polygon by clipping against z >= zBot then z <= zTop.
	poly := [][3]float32{a, b, c}
	poly = clipPolygonByZHalfSpace(poly, zBot, true /* keep z >= zBot */)
	if len(poly) < 3 {
		return nil, nil
	}
	poly = clipPolygonByZHalfSpace(poly, zTop, false /* keep z <= zTop */)
	if len(poly) < 3 {
		return nil, nil
	}
	// Whole-polygon XY area check. If the slab-clipped sub-polygon
	// has near-zero XY projection it came from a near-vertical
	// source triangle (e.g. the side wall of a cube), and the
	// barycentric Z-lift used by slabTri/emitCapPiece is unstable.
	// Route it to the vertical-clip path instead — every per-fan
	// triangle would otherwise fail the same area check and the
	// surface would vanish from the output.
	//
	// The relative threshold uses max(xRange, yRange)² as the scale,
	// not bbox-area, so it survives the axis-aligned case: a
	// triangle on a Y=constant or X=constant plane (a flat cube
	// wall) collapses its XY bbox to zero area in one dimension,
	// which would otherwise zero out a bbox-relative threshold and
	// let float-precision noise (~3e-5 from shoelace cancellation
	// on a 20-unit polygon) slip past, dropping every wall fragment
	// in that slab. Found 2026-05-15 on the cube's -Y face.
	polyAreaXY := polygonXYSignedArea(poly)
	xMin, yMin := poly[0][0], poly[0][1]
	xMax, yMax := xMin, yMin
	for _, p := range poly[1:] {
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
	if absf(polyAreaXY) < 1e-6*scale*scale || absf(polyAreaXY) < 1e-12 {
		return nil, &slabVerticalPoly{
			Pts:    poly,
			Normal: triangleNormal(a, b, c),
		}
	}
	// Fan-triangulate (the slabbed sub-polygon is convex — Z-plane
	// clipping of a triangle produces a convex polygon). Drop any
	// resulting sub-triangle whose XY projection has near-zero
	// area; their barycentric-lift would be unstable.
	tris := make([]slabTri, 0, len(poly)-2)
	for i := 1; i < len(poly)-1; i++ {
		v0 := poly[0]
		v1 := poly[i]
		v2 := poly[i+1]
		areaXY := (v1[0]-v0[0])*(v2[1]-v0[1]) - (v2[0]-v0[0])*(v1[1]-v0[1])
		// Threshold relative to the sub-triangle's XY bbox so the
		// filter scales with the per-triangle size. A ratio of 1e-6
		// catches degenerate cases without rejecting legitimate
		// small slivers.
		bboxXY := (maxf3(v0[0], v1[0], v2[0]) - minf3(v0[0], v1[0], v2[0])) *
			(maxf3(v0[1], v1[1], v2[1]) - minf3(v0[1], v1[1], v2[1]))
		if absf(areaXY) < 1e-6*absf(bboxXY) || absf(areaXY) < 1e-12 {
			continue
		}
		tris = append(tris, slabTri{
			V0:        v0,
			V1:        v1,
			V2:        v2,
			InvAreaXY: 1.0 / areaXY,
		})
	}
	return tris, nil
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
