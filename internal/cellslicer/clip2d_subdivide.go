// Slab-wide two-phase subdivision used by ClipMeshToCells2D.
//
// Phase 1 (clipPolyToCells): for each slab-clipped source polygon
// (slabPoly), clip against every candidate cell's outer polygon and
// collect the resulting per-cell 3D fragments. Every boundary vertex
// is added to a slab-wide union set (seen3D, Clipper-integer 3D
// coords).
//
// Phase 2 (appendCellPiece): splice each per-cell piece's edges with
// any union-set vertex that lies within 1µm of the edge's interior,
// then fan-triangulate the spliced polygon on the 2D plane
// perpendicular to the largest |normal| component (so a near-
// vertical source projects to a non-degenerate 2D polygon) and emit
// faces tagged with the cell index. cellPieces are convex by
// construction so fan-tri is O(n) and correct; the splice's float
// tolerance lets it bridge slanted-segment cell-boundary crossings
// that strict int64 collinearity would reject.
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
// from set that lies within 1µm of the edge's interior, in order
// along the edge.
//
// Called in Phase 2 with the slab-wide seen3D union set produced by
// Phase 1. Adjacent cells' outlines can have unequal vertex sets
// along their shared boundary (one straight, the other kinked at a
// polyomino corner); two source triangles meeting on a shared edge
// can also contribute mismatched vertex chains. Splicing every
// piece against the slab-wide union produces matching subdivisions
// on both sides — no T-junctions inside the slab.
//
// Float math. The math used to be int64 cross-product == 0
// collinearity, which is mathematically exact but operates on
// int3D values that came from float→int rounding upstream. For
// slanted source segments, a cell-boundary crossing computed in
// float and rounded to 1µm ends up within a bucket of the line but
// not *exactly* collinear in int64 — splice rejected those, leaving
// the cross-slab seams it was designed to fix unfixed. The float-
// tolerance check matches what the comparison actually means
// (perpendicular distance ≤ one 1µm bucket).
//
// The set is keyed by int3D for hash dedup, but the splice math
// itself is float64; we convert each candidate via int3DToFloat for
// the comparison. The inserted point is the bucket-position
// (int3DToFloat-rounded float) so coincident splices across cells
// hash-equal in the downstream cross-piece dedup.
func splicePoly3DEdges(poly [][3]float32, set []int3D) [][3]float32 {
	if len(poly) < 2 || len(set) == 0 {
		return poly
	}
	// Tolerance: 1 Clipper bucket = 1µm in model coords. Squared
	// since the check uses |cross|² ≤ tol² × |b-a|² to avoid a sqrt.
	const tol = float64(invClipperScale)
	const tol2 = tol * tol

	// Pre-compute the int3D bucket of every polygon vertex. Used
	// to reject candidates equal to ANY polygon vertex, not just
	// the current edge's endpoints — without this, a candidate
	// that lies on edge i AND coincides (in bucket) with vertex
	// i+2 would get inserted on edge i and then re-emitted as
	// poly[i+2], creating a consecutive same-bucket pair in
	// outInt. After cross-piece dedup (clip2d.go), that pair
	// collapses to one global vertex and fan triangulation emits
	// degenerate triangles whose half-edges blow up the
	// non-manifold count.
	polyKey := make(map[int3D]struct{}, len(poly))
	for _, p := range poly {
		polyKey[int3DOf(p)] = struct{}{}
	}

	// Pre-convert set to float64 once for the per-candidate math,
	// keeping the int3D parallel for endpoint-equality + insert
	// dedup. Drop candidates that match any polygon vertex up-front
	// (see polyKey rationale above).
	type candPt struct {
		ip int3D
		x  float64
		y  float64
		z  float64
	}
	pts := make([]candPt, 0, len(set))
	for _, p := range set {
		if _, ok := polyKey[p]; ok {
			continue
		}
		pts = append(pts, candPt{
			ip: p,
			x:  float64(p.X) * invClipperScale,
			y:  float64(p.Y) * invClipperScale,
			z:  float64(p.Z) * invClipperScale,
		})
	}

	out := make([][3]float32, 0, len(poly)+len(pts))
	// outInt is the parallel int3D log of what's been appended,
	// used to suppress consecutive duplicate inserts (two near-
	// coincident candidates that round to the same bucket).
	outInt := make([]int3D, 0, len(poly)+len(pts))
	n := len(poly)
	// emitVertex appends v/vi to out/outInt but only if vi differs
	// from the immediately-prior emitted bucket. Centralising the
	// dedup means the polygon-vertex append at the top of every
	// edge iteration also gets it — without that, a sequence
	// `[..., insertOnEdgeI=X, poly[i+1]=X]` would slip through.
	emitVertex := func(v [3]float32, vi int3D) {
		if len(outInt) > 0 && outInt[len(outInt)-1] == vi {
			return
		}
		out = append(out, v)
		outInt = append(outInt, vi)
	}
	type cand struct {
		t  float64
		ip int3D
		x  float64
		y  float64
		z  float64
	}
	for i := 0; i < n; i++ {
		af := poly[i]
		bf := poly[(i+1)%n]
		ax, ay, az := float64(af[0]), float64(af[1]), float64(af[2])
		bx, by, bz := float64(bf[0])-ax, float64(bf[1])-ay, float64(bf[2])-az
		ab2 := bx*bx + by*by + bz*bz
		ai := int3DOf(af)
		emitVertex(af, ai)
		if ab2 == 0 {
			continue
		}
		tolAb2 := tol2 * ab2
		var cands []cand
		for _, c := range pts {
			px := c.x - ax
			py := c.y - ay
			pz := c.z - az
			// Cross (p−a) × (b−a). |cross|² ≤ tol² × |b−a|²
			// iff perpendicular distance from p to the line ≤ tol.
			cx := py*bz - pz*by
			cy := pz*bx - px*bz
			cz := px*by - py*bx
			cross2 := cx*cx + cy*cy + cz*cz
			if cross2 > tolAb2 {
				continue
			}
			// Project p onto the edge: t = (p−a)·(b−a). Strictly
			// between the endpoints means 0 < t < |b−a|².
			t := px*bx + py*by + pz*bz
			if t <= 0 || t >= ab2 {
				continue
			}
			cands = append(cands, cand{t: t, ip: c.ip, x: c.x, y: c.y, z: c.z})
		}
		if len(cands) > 1 {
			sort.Slice(cands, func(i, j int) bool { return cands[i].t < cands[j].t })
		}
		for _, c := range cands {
			emitVertex([3]float32{float32(c.x), float32(c.y), float32(c.z)}, c.ip)
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
		// Defensive: the open-ended outer-edge rule only fires inside
		// the cap path (clipSlabPolyToCellPrism3D). If a vertical-path
		// slabPoly has any vertex outside the slab footprint, the
		// fragment past the partition outline gets silently dropped
		// here — same failure mode the cap path used to have. Count
		// these so a regression or new-model corner case shows up
		// loudly at end-of-clip instead of going unnoticed.
		fp := slabs[slabIdx].Footprint
		if fp != nil {
			for _, v := range p.Pts {
				if !fp.Contains(v[0], v[1]) {
					atomic.AddUint64(&verticalPathRiskCount, 1)
					break
				}
			}
		}
		return clipPolyToCellsVertical(p, slabIdx, slabs, candidates, dst, seen), candidates
	}
	return clipPolyToCellsCap(p, slabIdx, slabs, candidates, dst, seen), candidates
}

// verticalPathRiskCount tracks vertical-path slabPolys that have at
// least one vertex falling outside the slab footprint. Non-zero at end
// of ClipMeshToCells2D means the vertical clip path may be dropping
// geometry that open-ended cells would have absorbed in the cap path.
// Always-on (no env gate) so future-us notices the regression without
// re-enabling diagnostics.
var verticalPathRiskCount uint64

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
		pieces := clipSlabPolyToCellPrism3D(p.Pts, cell)
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
// Open-ended outer edges: any cell.Outer edge tagged in
// cell.OuterEdgeOnBoundary skips its Sutherland-Hodgman cut entirely
// — slabPoly fragments that extend past the partition's outer boundary
// land in this cell instead of being silently dropped. See the field
// doc on Cell for the rationale. Nil OuterEdgeOnBoundary keeps the
// historical strict-clip behaviour (every edge cuts), so legacy
// PartitionSlab cells still work unchanged.
//
// For the earcut path the "outer" rule applies only to the earcut
// triangle's cell.Outer edges (those whose endpoints are consecutive
// in cell.Outer). Earcut diagonals — internal to the cell — must
// always clip; without them one earcut triangle's piece would leak
// into its sibling's region and produce duplicate geometry.
//
// Orientation: both paths use the inward-half-space derivation
// (nx, ny) = (dy, -dx), which assumes CCW cell.Outer. PartitionSlabRaster
// emits CCW (raster-derived marching-squares); legacy PartitionSlab
// emits CCW (Clipper non-zero union normalises). The defensive
// reversal that used to live here was dropped along with the open-ended
// feature: silently reversing the polygon while NOT reversing the
// OuterEdgeOnBoundary flags would scramble the open/closed-edge
// mapping. If a future caller breaks the CCW invariant, the
// open-ended logic would mark all the wrong edges; better to fail
// loudly via assert-CCW upstream than to half-cover the failure here.
//
// Output piece vertex Z values come straight from clipPolyByPlaneXY's
// linear lerp, which is exact for points on the source plane.
func clipSlabPolyToCellPrism3D(slabPolyPts [][3]float32, cell *Cell) [][][3]float32 {
	cellOuter := cell.Outer
	if len(slabPolyPts) < 3 || len(cellOuter) < 3 {
		return nil
	}
	outerFlags := cell.OuterEdgeOnBoundary
	n := len(cellOuter)
	if isConvex(cellOuter) {
		clipped := slabPolyPts
		for i := 0; i < n; i++ {
			if outerFlags != nil && outerFlags[i] {
				continue
			}
			clipped = clipPolyByCellEdge(clipped, cellOuter[i], cellOuter[(i+1)%n])
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
		clipped := slabPolyPts
		for k := 0; k < 3; k++ {
			i, j := int(tri[k]), int(tri[(k+1)%3])
			if outerFlags != nil {
				// Cell.Outer edge? Endpoints consecutive (in either direction)
				// in the original cell.Outer index space.
				switch {
				case j == (i+1)%n:
					if outerFlags[i] {
						continue
					}
				case i == (j+1)%n:
					if outerFlags[j] {
						continue
					}
				}
				// Otherwise (diagonals), fall through to always clip.
			}
			a := earVerts[i]
			b := earVerts[j]
			clipped = clipPolyByCellEdge(clipped, a, b)
			if len(clipped) < 3 {
				break
			}
		}
		if len(clipped) >= 3 {
			out = append(out, clipped)
		}
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
// fan-triangulates the spliced polygon, and appends to (verts, faces,
// localIdx) — returning the updated slices. Each emitted face is
// tagged with pc.cellIdx in localIdx.
//
// Triangulation: fan-from-vertex-0 on a 2D projection of the spliced
// polygon. The projection axis dropped is the one aligned with the
// largest |pc.normal| component, which maximizes projected area and
// keeps a near-vertical source's piece from collapsing to a thin XY
// sliver. cellPieces are convex by construction (see the dispatch
// notes in the triangulation block below), so fan-tri is O(n) and
// covers the polygon exactly.
//
// Winding is decided per output triangle: compute the 3D triangle's
// normal, dot against pc.normal, flip if they disagree. Robust to
// the projected polygon's winding (which depends on which axis was
// dropped) and to sub-bucket wobble from splice insertions.
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
		atomic.StoreInt64(stepPtr, 3) // triangulate
	}
	// cellPieces are convex by construction:
	//   - cap path (clipSlabPolyToCellPrism3D): convex slabPoly
	//     intersected with a convex cell (or convex earcut sub-
	//     triangle for non-convex cell.Outer) → convex.
	//   - vertical path (clipVerticalPolyToCell): wall strips, each
	//     a rectangle → convex.
	// Splice inserts vertices within 1µm of an existing boundary
	// edge — the post-splice polygon stays convex within bucket
	// tolerance. Fan-triangulate in O(n) directly; per-triangle
	// winding is fixed below via the (triN · pc.normal) test, so
	// any tiny orientation flip from a sub-bucket bump corrects.
	//
	// (Was Earcut, which hung on the building model — the ear-search
	// inner loop spent unbounded time on small (≤42 vert) polygons
	// whose vertices were near-collinear from splice insertions.
	// See commit 3411f97's diagnostic infrastructure for the
	// goroutine-stack evidence.)
	earVerts := floatPoly
	earTris := fanTriangulate(len(floatPoly))
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
