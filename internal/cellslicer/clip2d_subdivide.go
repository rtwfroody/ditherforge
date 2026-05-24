// Slab-wide two-phase subdivision used by ClipMeshToCells2D.
//
// Phase 1 (clipPolyToCells): for each slab-clipped source polygon
// (slabPoly), clip against every candidate cell's outer polygon and
// collect the resulting per-cell 3D fragments. Every boundary vertex
// is added to a slab-wide union set (seen3D, Clipper-integer 3D
// coords).
//
// Phase 2 (appendCellPiece): splice each per-cell piece's edges with
// any union-set vertex that lies on the edge's interior, then Earcut
// the spliced polygon on the 2D plane perpendicular to the largest
// |normal| component (so a near-vertical source projects to a
// non-degenerate 2D polygon, not a thin XY sliver), and emit faces
// tagged with the cell index. The splice is exact in Clipper integer
// space, so a kink that one cell's outline introduces also appears
// as a vertex on the neighbour's matching edge.
//
// A slab-wide (not per-source-polygon) splice set is required to
// cover the case where two source triangles share an edge: cells
// straddling the source-edge contribute crossings that must appear
// on both sides. The cube STL bottom-face diagonal between the two
// cap source triangles was the motivating case — per-source-tri
// splice left ~50 boundary edges on that diagonal.

package cellslicer

import (
	"math"
	"sort"
	"sync/atomic"
)

// Diagnostic counters used by ClipMeshToCells2D's hole-report mode.
// Only meaningful while DITHERFORGE_HOLE_REPORT=1; unconditional
// AddUint64 is cheap and avoids gating the inner loop.
var (
	// Histogram buckets for post-splice polygon vertex counts. Bucket
	// boundaries: [≤8, ≤16, ≤32, ≤64, ≤128, ≤256, ≤512, ≤1024, >1024].
	// Lets us see whether a hang/blowup is "many small polygons" or
	// "a handful of pathological huge ones".
	phase2NHist [9]uint64
	// Max polygon size ever seen (after splice), and the number of
	// pieces with N > 100 — pathological-piece sentinel.
	phase2NMaxSeen      uint64
	phase2NPathological uint64
)

// bumpNHist updates the histogram bucket for one polygon size n.
// No-op when DITHERFORGE_HOLE_REPORT is unset (gated via the
// package-level debugHoles flag in clip2d.go) — keeps the three
// atomics out of the production inner loop.
func bumpNHist(n int) {
	if !debugHoles {
		return
	}
	var idx int
	switch {
	case n <= 8:
		idx = 0
	case n <= 16:
		idx = 1
	case n <= 32:
		idx = 2
	case n <= 64:
		idx = 3
	case n <= 128:
		idx = 4
	case n <= 256:
		idx = 5
	case n <= 512:
		idx = 6
	case n <= 1024:
		idx = 7
	default:
		idx = 8
	}
	atomic.AddUint64(&phase2NHist[idx], 1)
	un := uint64(n)
	// CAS-loop max update so we don't lose the high-water under
	// concurrent workers.
	for {
		cur := atomic.LoadUint64(&phase2NMaxSeen)
		if un <= cur || atomic.CompareAndSwapUint64(&phase2NMaxSeen, cur, un) {
			break
		}
	}
	if n > 100 {
		atomic.AddUint64(&phase2NPathological, 1)
	}
}

// int3D is a Clipper-integer 3D point. Z uses the same 1µm scale.
type int3D struct {
	X, Y, Z int64
}

func int3DOf(p [3]float32) int3D {
	return int3D{
		X: int64(math.Round(float64(p[0]) * clipperScale)),
		Y: int64(math.Round(float64(p[1]) * clipperScale)),
		Z: int64(math.Round(float64(p[2]) * clipperScale)),
	}
}

func int3DToFloat(p int3D) [3]float32 {
	return [3]float32{
		float32(float64(p.X) * invClipperScale),
		float32(float64(p.Y) * invClipperScale),
		float32(float64(p.Z) * invClipperScale),
	}
}

// splicePoly3DEdges walks each edge of poly and inserts any vertex
// from set that lies strictly between the edge endpoints (collinear
// in Clipper-integer 3D space), ordered along the edge direction.
//
// Called in Phase 2 with the slab-wide seen3D union set produced by
// Phase 1. Adjacent cells' outlines can have unequal vertex sets
// along their shared boundary (one straight, the other kinked at a
// polyomino corner); two source triangles meeting on a shared edge
// can also contribute mismatched vertex chains. Splicing every
// piece against the slab-wide union produces matching subdivisions
// on both sides — no T-junctions inside the slab.
//
// Integer-overflow safety: the cross and dot products below are
// O(coord²) in int64. clipperScale = 1000 gives coords ≤ ~1e8 in
// the worst-case 100m-side model, so individual products stay
// under ~3e16 and the int64 (max ~9.2e18) cross/dot sums never
// wrap. The cube test sits comfortably at ~2.5e9.
func splicePoly3DEdges(poly [][3]float32, set []int3D) [][3]float32 {
	if len(poly) < 2 || len(set) == 0 {
		return poly
	}
	polyInt := make([]int3D, len(poly))
	for i, p := range poly {
		polyInt[i] = int3DOf(p)
	}
	out := make([][3]float32, 0, len(poly)+len(set))
	outInt := make([]int3D, 0, len(poly)+len(set))
	n := len(polyInt)
	type cand struct {
		t int64
		p int3D
	}
	for i := 0; i < n; i++ {
		a := polyInt[i]
		b := polyInt[(i+1)%n]
		out = append(out, poly[i])
		outInt = append(outInt, a)
		bx := b.X - a.X
		by := b.Y - a.Y
		bz := b.Z - a.Z
		ab2 := bx*bx + by*by + bz*bz
		if ab2 == 0 {
			continue
		}
		var cands []cand
		for _, p := range set {
			if p == a || p == b {
				continue
			}
			px := p.X - a.X
			py := p.Y - a.Y
			pz := p.Z - a.Z
			// Collinear: (p-a) × (b-a) == 0.
			cx := py*bz - pz*by
			cy := pz*bx - px*bz
			cz := px*by - py*bx
			if cx != 0 || cy != 0 || cz != 0 {
				continue
			}
			t := px*bx + py*by + pz*bz
			if t <= 0 || t >= ab2 {
				continue
			}
			cands = append(cands, cand{t: t, p: p})
		}
		if len(cands) > 1 {
			sort.Slice(cands, func(i, j int) bool { return cands[i].t < cands[j].t })
		}
		for _, c := range cands {
			if len(outInt) > 0 && outInt[len(outInt)-1] == c.p {
				continue
			}
			outInt = append(outInt, c.p)
			out = append(out, int3DToFloat(c.p))
		}
	}
	if n2 := len(outInt); n2 > 1 && outInt[0] == outInt[n2-1] {
		out = out[:n2-1]
	}
	return out
}

// slabCellIndex is a simple per-slab spatial index over cell.Outer
// bboxes. Linear scan is fine for cell counts in the thousands; if
// the partition grows past 10⁴ cells per slab a uniform grid would
// pay off.
type slabCellIndex struct {
	bbox []cellBBox
}

type cellBBox struct {
	minX, minY, maxX, maxY float32
}

func buildSlabCellIndex(s *Slab) *slabCellIndex {
	idx := &slabCellIndex{bbox: make([]cellBBox, len(s.Cells))}
	for i := range s.Cells {
		if len(s.Cells[i].Outer) == 0 {
			continue
		}
		mn0, mn1, mx0, mx1 := polyBoundsP2(s.Cells[i].Outer)
		idx.bbox[i] = cellBBox{mn0, mn1, mx0, mx1}
	}
	return idx
}

func (idx *slabCellIndex) candidates(xMin, yMin, xMax, yMax float32, out []int) []int {
	out = out[:0]
	for i, b := range idx.bbox {
		if b.maxX < xMin || b.minX > xMax || b.maxY < yMin || b.minY > yMax {
			continue
		}
		out = append(out, i)
	}
	return out
}

// cellPiece is one fragment of a slabPoly that lies inside one cell.
// Vertices are full 3D, in the source triangle's plane. normal
// carries the source triangle's facing direction so phase 2 can
// emit faces with matching winding.
type cellPiece struct {
	cellIdx int32
	pts     [][3]float32
	normal  [3]float32
}

// clipPolyToCells is phase 1 of the slab-wide subdivision: clip
// slab-polygon p against every candidate cell and append the
// resulting 3D per-cell fragments to dst, while inserting every
// boundary vertex into seen.
//
// Dispatches internally: polygons with measurable XY area take the
// Clipper-based 2D intersection path (clipPolyToCellsCap), with Z
// recovered from the source plane equation; polygons whose XY
// projection is degenerate (near-vertical source triangle) take the
// vertical-scan path (clipPolyToCellsVertical), which keeps the
// fragments in 3D throughout.
func clipPolyToCells(
	p slabPoly,
	slabIdx int,
	slabs []Slab,
	idx *slabCellIndex,
	dst []cellPiece,
	seen map[int3D]struct{},
	candidateBuf []int,
) ([]cellPiece, []int) {
	if len(p.Pts) < 3 {
		return dst, candidateBuf
	}
	minX, minY := p.Pts[0][0], p.Pts[0][1]
	maxX, maxY := minX, minY
	for _, v := range p.Pts[1:] {
		if v[0] < minX {
			minX = v[0]
		} else if v[0] > maxX {
			maxX = v[0]
		}
		if v[1] < minY {
			minY = v[1]
		} else if v[1] > maxY {
			maxY = v[1]
		}
	}
	candidates := idx.candidates(minX, minY, maxX, maxY, candidateBuf)
	if len(candidates) == 0 {
		return dst, candidates
	}
	if isPolyXYDegenerate(p.Pts) {
		return clipPolyToCellsVertical(p, slabIdx, slabs, candidates, dst, seen), candidates
	}
	return clipPolyToCellsCap(p, slabIdx, slabs, candidates, dst, seen), candidates
}

// clipPolyToCellsCap clips slabPoly against each candidate cell's
// vertical prism in 3D, preserving Z exactly through Sutherland-
// Hodgman linear interpolation. Cell.Outer can be non-convex
// (polyomino corners from raster partitioning), so it's earcut into
// triangles and the slabPoly is clipped against each triangle's
// prism — three vertical half-spaces per triangle. The collection
// of non-empty piece results covers the slabPoly's intersection
// with the cell.
//
// Replaces the prior Clipper-2D-then-re-lift-Z approach: that
// re-lift's z = (d - n.x*x - n.y*y) / n.z amplifies Clipper's 1µm
// XY-integer roundtrip by the source plane's |grad_xy(z)| =
// |n_xy|/|n_z|. On slanted near-walls (e.g. low_poly_building.glb
// at ~4° from vertical, gradient ≈ 14.5) drift reached ~2–15µm,
// pushing vertices off the slab plane bucket and breaking the
// cross-slab splice's exact-Z filter — visible as gaps and
// T-junctions at slab boundaries.
//
// SH lerp blends Z linearly between segment endpoints, which is
// exact for any point on the slabPoly's plane (it IS planar). No
// re-lift, no gradient amplification, no clamp.
func clipPolyToCellsCap(
	p slabPoly,
	slabIdx int,
	slabs []Slab,
	candidates []int,
	dst []cellPiece,
	seen map[int3D]struct{},
) []cellPiece {
	for _, ci := range candidates {
		cell := &slabs[slabIdx].Cells[ci]
		pieces := clipSlabPolyToCellPrism3D(p.Pts, cell.Outer)
		for _, piece := range pieces {
			piece = dedup3DPoly(piece)
			if len(piece) < 3 {
				continue
			}
			for _, v := range piece {
				seen[int3DOf(v)] = struct{}{}
			}
			dst = append(dst, cellPiece{
				cellIdx: int32(ci),
				pts:     piece,
				normal:  p.Normal,
			})
		}
	}
	return dst
}

// clipSlabPolyToCellPrism3D clips slabPolyPts (3D, planar in the
// source-triangle plane) against the cell's vertical prism. Cell.Outer
// can be non-convex (polyomino corners from raster partitioning), so
// the routine dispatches:
//
//   - Convex cell.Outer (the common case — hex cells, simple ring
//     trapezoids): one Sutherland-Hodgman pass per cell.Outer edge,
//     producing a single output piece. O(slab × cellEdges) work.
//
//   - Non-convex cell.Outer: earcut into triangles, clip slabPolyPts
//     against each triangle's prism (three half-spaces). Emits one
//     piece per non-empty triangle clip — N-2 pieces in the worst
//     case. The earcut diagonals become internal edges in the output;
//     dedup3DPoly + the splice machinery in Phase 2 keep the seams
//     topologically clean.
//
// Orientation: both paths use the inward-half-space derivation
// (nx, ny) = (dy, -dx), which is CCW-only — for a CW input the
// half-space inverts and SH clips away the entire interior, silently
// dropping the cell. Production cell producers all emit CCW (raster
// path's marching-squares, the older ring/hex generators), but the
// reversed-copy fallback below is cheap insurance against future
// callers, and removes the "did anyone change cell winding?" failure
// mode from a hot path that's hard to debug.
//
// Output piece vertex Z values come straight from clipPolyByPlaneXY's
// linear lerp, which is exact for points on the source plane.
func clipSlabPolyToCellPrism3D(slabPolyPts [][3]float32, cellOuter []Point2) [][][3]float32 {
	if len(slabPolyPts) < 3 || len(cellOuter) < 3 {
		return nil
	}
	if !isCCW(cellOuter) {
		rev := make([]Point2, len(cellOuter))
		for i, p := range cellOuter {
			rev[len(cellOuter)-1-i] = p
		}
		cellOuter = rev
	}
	if isConvex(cellOuter) {
		clipped := slabPolyPts
		n := len(cellOuter)
		for i := 0; i < n; i++ {
			a := cellOuter[i]
			b := cellOuter[(i+1)%n]
			clipped = clipPolyByCellEdge(clipped, a, b)
			if len(clipped) < 3 {
				return nil
			}
		}
		return [][][3]float32{clipped}
	}
	earVerts, tris := Earcut(cellOuter, nil)
	if len(tris) == 0 {
		return nil
	}
	out := make([][][3]float32, 0, len(tris))
	for _, tri := range tris {
		a := earVerts[tri[0]]
		b := earVerts[tri[1]]
		c := earVerts[tri[2]]
		// Earcut emits CCW triangles (it normalizes outer to CCW
		// internally — see earcut.go's linkedList). For a CCW
		// triangle the interior lies to the LEFT of each edge
		// V_i→V_{i+1}, so the outward XY normal is the edge direction
		// rotated 90° CW: (dy, -dx) where (dx, dy) = V_{i+1} - V_i.
		// The interior half-space is then nx*x + ny*y <= d with
		// d = nx*V_i.x + ny*V_i.y — the same convention
		// clipPolyByPlaneXY uses (and it treats on-plane points as
		// inside, so a slabPoly edge flush with a cell edge survives).
		clipped := slabPolyPts
		clipped = clipPolyByCellEdge(clipped, a, b)
		if len(clipped) < 3 {
			continue
		}
		clipped = clipPolyByCellEdge(clipped, b, c)
		if len(clipped) < 3 {
			continue
		}
		clipped = clipPolyByCellEdge(clipped, c, a)
		if len(clipped) < 3 {
			continue
		}
		out = append(out, clipped)
	}
	return out
}

// isCCW reports whether poly winds counter-clockwise via signed area.
// Zero area (collinear / degenerate) is reported as CCW so callers
// reach a single decision branch; cell.Outer with zero XY area would
// be filtered upstream anyway.
func isCCW(poly []Point2) bool {
	n := len(poly)
	if n < 3 {
		return true
	}
	var s float32
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		s += poly[i][0]*poly[j][1] - poly[j][0]*poly[i][1]
	}
	return s >= 0
}

// isConvex reports whether poly (assumed CCW; orient first via isCCW)
// has no concave corners. Picks the convex fast path for the cell-
// prism clip — concave cells take the earcut detour instead.
// Collinear runs (cross == 0) are skipped so a polygon with an
// extra-vertex straight edge counts as convex.
func isConvex(poly []Point2) bool {
	n := len(poly)
	if n < 3 {
		return false
	}
	for i := 0; i < n; i++ {
		a := poly[i]
		b := poly[(i+1)%n]
		c := poly[(i+2)%n]
		cross := (b[0]-a[0])*(c[1]-b[1]) - (b[1]-a[1])*(c[0]-b[0])
		if cross < 0 {
			return false
		}
	}
	return true
}

// clipPolyByCellEdge clips poly against the inward half-space of a CCW
// triangle edge a→b. See clipSlabPolyToCellPrism3D for the half-space
// derivation.
func clipPolyByCellEdge(poly [][3]float32, a, b Point2) [][3]float32 {
	dx := b[0] - a[0]
	dy := b[1] - a[1]
	nx := dy
	ny := -dx
	d := nx*a[0] + ny*a[1]
	return clipPolyByPlaneXY(poly, nx, ny, d)
}

// clipPolyToCellsVertical is the vertical-scan path used when the
// source slabPoly's XY projection is degenerate (near-vertical
// source triangle). clipVerticalPolyToCell returns 3D fragments
// directly — no Z re-lift needed.
func clipPolyToCellsVertical(
	p slabPoly,
	slabIdx int,
	slabs []Slab,
	candidates []int,
	dst []cellPiece,
	seen map[int3D]struct{},
) []cellPiece {
	for _, ci := range candidates {
		cell := &slabs[slabIdx].Cells[ci]
		cellPieces := clipVerticalPolyToCell(p.Pts, cell.Outer)
		for _, clipped := range cellPieces {
			clipped = dedup3DPoly(clipped)
			if len(clipped) < 3 {
				continue
			}
			for _, v := range clipped {
				seen[int3DOf(v)] = struct{}{}
			}
			dst = append(dst, cellPiece{
				cellIdx: int32(ci),
				pts:     clipped,
				normal:  p.Normal,
			})
		}
	}
	return dst
}

// appendCellPiece splices pc against the slab-wide 3D splice set,
// triangulates the spliced polygon, and appends to (verts, faces,
// localIdx) — returning the updated slices. Each emitted face is
// tagged with pc.cellIdx in localIdx.
//
// Triangulation: Earcut on a 2D projection of the spliced polygon.
// The projection plane is chosen to drop the axis aligned with the
// largest |pc.normal| component, which maximizes projected area and
// keeps Earcut on non-degenerate input regardless of source-triangle
// orientation. Without this, a slanted-near-vertical slabPoly's cell
// fragment projects to a thin XY sliver and Earcut goes pathological
// (observed: a 7-vertex Y-dominant-normal piece running for >10 min
// on TestSampledMatchesInput/building before this fix).
//
// Cell outers can be non-convex, so the Clipper-intersection result
// can also be non-convex; Earcut handles that. The 3D vertices are
// preserved from spliced — Earcut only sees the 2D projection but
// the emitted verts carry full 3D.
//
// Winding is decided per output triangle: compute the 3D triangle's
// normal, dot against pc.normal, flip if they disagree. Robust to
// whatever winding convention the input polygon and Earcut happen
// to produce.
func appendCellPiece(
	pc cellPiece,
	spliceSet []int3D,
	verts [][3]float32,
	faces [][3]uint32,
	localIdx []int32,
	stepPtr *int64,
	splicedNPtr *int64,
) ([][3]float32, [][3]uint32, []int32) {
	if stepPtr != nil {
		atomic.StoreInt64(stepPtr, 2) // splice
	}
	spliced := splicePoly3DEdges(pc.pts, spliceSet)
	if len(spliced) < 3 {
		return verts, faces, localIdx
	}
	bumpNHist(len(spliced))
	if splicedNPtr != nil {
		atomic.StoreInt64(splicedNPtr, int64(len(spliced)))
	}

	// Pick the 2D projection axes by dropping the dimension of the
	// largest |pc.normal| component.
	ax, ay, az := absf(pc.normal[0]), absf(pc.normal[1]), absf(pc.normal[2])
	var u, v int
	switch {
	case az >= ax && az >= ay:
		u, v = 0, 1
	case ay >= ax:
		u, v = 0, 2
	default:
		u, v = 1, 2
	}

	floatPoly := make([]Point2, len(spliced))
	for i, p := range spliced {
		floatPoly[i] = Point2{p[u], p[v]}
	}
	if stepPtr != nil {
		atomic.StoreInt64(stepPtr, 3) // earcut
	}
	earVerts, earTris := Earcut(floatPoly, nil)
	if stepPtr != nil {
		atomic.StoreInt64(stepPtr, 4) // emit-tris
	}
	if len(earTris) == 0 || len(earVerts) != len(floatPoly) {
		return verts, faces, localIdx
	}
	base := uint32(len(verts))
	verts = append(verts, spliced...)
	// Zero-area earcut tris are kept on purpose — they carry
	// boundary-edge connectivity for collinear boundary vertices
	// V_a-V_b-V_c (when the slab-wide splice inserts V_b on a
	// straight edge that V_a→V_c already covered). Dropping them
	// turns V_a-V_b / V_b-V_c into 1-use boundary edges and re-
	// introduces the T-junctions the splice set was added to
	// eliminate.
	for _, tri := range earTris {
		triN := triangleNormal(spliced[tri[0]], spliced[tri[1]], spliced[tri[2]])
		dot := triN[0]*pc.normal[0] + triN[1]*pc.normal[1] + triN[2]*pc.normal[2]
		if dot < 0 {
			faces = append(faces, [3]uint32{base + tri[0], base + tri[2], base + tri[1]})
		} else {
			faces = append(faces, [3]uint32{base + tri[0], base + tri[1], base + tri[2]})
		}
		localIdx = append(localIdx, pc.cellIdx)
	}
	return verts, faces, localIdx
}

// dedup3DPoly removes consecutive (in Clipper-integer space)
// duplicate vertices and a trailing closing duplicate. Sutherland-
// Hodgman can emit a vertex twice when the polygon's edge is flush
// with a clip plane (or grazes a vertex), and the duplicates produce
// zero-area triangles after fan-triangulation — those triangles show
// up as shared edges on the wall (count=4 / count=6 boundary-edge
// buckets).
func dedup3DPoly(poly [][3]float32) [][3]float32 {
	if len(poly) == 0 {
		return poly
	}
	out := make([][3]float32, 0, len(poly))
	outInt := make([]int3D, 0, len(poly))
	for _, p := range poly {
		ip := int3DOf(p)
		if n := len(outInt); n > 0 && outInt[n-1] == ip {
			continue
		}
		out = append(out, p)
		outInt = append(outInt, ip)
	}
	if n := len(outInt); n > 1 && outInt[0] == outInt[n-1] {
		out = out[:n-1]
	}
	return out
}
