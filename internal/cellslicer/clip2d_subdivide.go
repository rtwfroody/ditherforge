// Slab-wide two-phase subdivision used by ClipMeshToCells2D:
//
// Phase 1 (clipSlabTriPieces, clipSlabVerticalPieces): for each
// slab-clipped source-triangle piece (and each vertical source
// sub-polygon), clip against every candidate cell's outer polygon
// and collect the resulting per-cell integer polygons. Every
// boundary vertex is added to a SLAB-WIDE union set (2D for cap
// pieces, 3D for wall pieces).
//
// Phase 2 (appendCapPiece, appendWallPiece): splice each per-cell
// piece's edges with any union-set vertex that lies on the edge's
// interior, then earcut (cap) or fan-triangulate (wall). The splice
// is exact in Clipper integer space, so a kink that one cell's
// outline introduces also appears as a vertex on the neighbour's
// matching edge.
//
// A slab-wide (not per-source-triangle) splice set is required to
// cover the case where two source triangles share an edge: cells
// straddling the source-edge contribute crossings that must appear
// on both sides. The cube STL bottom-face diagonal between the two
// cap source triangles was the motivating case — per-source-tri
// splice left ~50 boundary edges on that diagonal.

package cellslicer

import (
	"math"
	"sort"

	clipper "github.com/ctessum/go.clipper"
)

// intPt is a Clipper-integer XY point. Used as a dedup key when
// merging boundary vertex sets across per-cell clip pieces of one
// source triangle.
type intPt struct {
	X, Y int64
}

func intPtOf(p Point2) intPt {
	return intPt{
		X: int64(math.Round(float64(p[0]) * clipperScale)),
		Y: int64(math.Round(float64(p[1]) * clipperScale)),
	}
}

func intPtToPoint2(p intPt) Point2 {
	return Point2{
		float32(float64(p.X) * invClipperScale),
		float32(float64(p.Y) * invClipperScale),
	}
}

// clipperPathToIntPts converts a Clipper Path to []intPt, dropping
// consecutive duplicates and the closing duplicate. The integer
// coordinates are preserved exactly (no float roundtrip).
func clipperPathToIntPts(path clipper.Path) []intPt {
	out := make([]intPt, 0, len(path))
	for _, ip := range path {
		p := intPt{X: int64(ip.X), Y: int64(ip.Y)}
		if n := len(out); n > 0 && out[n-1] == p {
			continue
		}
		out = append(out, p)
	}
	if n := len(out); n > 1 && out[0] == out[n-1] {
		out = out[:n-1]
	}
	return out
}

// splicePolyIntEdges walks each edge of poly and inserts any vertex
// from set that lies strictly between the edge endpoints, ordered
// along the edge direction. Operates entirely in integer space, so
// the collinearity test is exact: an inserted vertex is geometrically
// on the edge, not just within a tolerance.
//
// Used per-source-tri after computing per-cell Clipper intersections
// against every candidate cell: adjacent cells' outlines can have
// unequal vertex sets along their shared boundary (one straight, the
// other kinked at a polyomino corner). Splicing each piece's edges
// with the union vertex set produces matching subdivisions on both
// sides — no T-junctions within the source triangle's coverage.
func splicePolyIntEdges(poly []intPt, set []intPt) []intPt {
	if len(poly) < 2 || len(set) == 0 {
		return poly
	}
	out := make([]intPt, 0, len(poly)+len(set))
	n := len(poly)
	type cand struct {
		t int64
		p intPt
	}
	for i := 0; i < n; i++ {
		a := poly[i]
		b := poly[(i+1)%n]
		out = append(out, a)
		bx := b.X - a.X
		by := b.Y - a.Y
		ab2 := bx*bx + by*by
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
			// Collinear: cross product == 0.
			if px*by-py*bx != 0 {
				continue
			}
			// Strictly between: 0 < t < ab2 (t = dot(p-a, b-a)).
			t := px*bx + py*by
			if t <= 0 || t >= ab2 {
				continue
			}
			cands = append(cands, cand{t: t, p: p})
		}
		if len(cands) > 1 {
			sort.Slice(cands, func(i, j int) bool { return cands[i].t < cands[j].t })
		}
		for _, c := range cands {
			if len(out) > 0 && out[len(out)-1] == c.p {
				continue
			}
			out = append(out, c.p)
		}
	}
	// Drop any final consecutive duplicate against the first vertex
	// (closed polygon).
	if n2 := len(out); n2 > 1 && out[0] == out[n2-1] {
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

// capPiece is one Clipper-integer polygon produced by clipping a
// cap-style (non-vertical) source-tri slab piece against one cell.
// Phase 1 (clipSlabTriPieces) populates the slab-wide vertex set
// from these pieces; phase 2 (emitCapPiece) splices each piece
// against the slab-wide union and earcuts/emits. The slab-wide
// splice eliminates T-junctions across source-tri boundaries —
// e.g. the cube bottom-face's diagonal between two STL source
// triangles.
type capPiece struct {
	cellIdx int32
	poly    []intPt
	t       slabTri // for Z-lift via barycentric
}

// wallPiece is the vertical-clip counterpart of capPiece.
type wallPiece struct {
	cellIdx int32
	pts     [][3]float32
	normal  [3]float32
}

// clipSlabTriPieces is phase 1 of the slab-wide subdivision: clip
// source-tri t against every candidate cell and append the resulting
// per-cell integer polygons to dst, while inserting every boundary
// vertex into seen. The caller does splice/emit in phase 2 with a
// slab-wide vertex set, so T-junctions across source-tri boundaries
// (e.g. the cube's bottom-cap diagonal) are eliminated.
func clipSlabTriPieces(
	t slabTri,
	slabIdx int,
	slabs []Slab,
	idx *slabCellIndex,
	dst []capPiece,
	seen map[intPt]struct{},
	candidateBuf []int,
) ([]capPiece, []int) {
	tMinX := minf3(t.V0[0], t.V1[0], t.V2[0])
	tMaxX := maxf3(t.V0[0], t.V1[0], t.V2[0])
	tMinY := minf3(t.V0[1], t.V1[1], t.V2[1])
	tMaxY := maxf3(t.V0[1], t.V1[1], t.V2[1])

	candidates := idx.candidates(tMinX, tMinY, tMaxX, tMaxY, candidateBuf)
	if len(candidates) == 0 {
		return dst, candidates
	}

	triA := intPt{
		X: int64(math.Round(float64(t.V0[0]) * clipperScale)),
		Y: int64(math.Round(float64(t.V0[1]) * clipperScale)),
	}
	triB := intPt{
		X: int64(math.Round(float64(t.V1[0]) * clipperScale)),
		Y: int64(math.Round(float64(t.V1[1]) * clipperScale)),
	}
	triC := intPt{
		X: int64(math.Round(float64(t.V2[0]) * clipperScale)),
		Y: int64(math.Round(float64(t.V2[1]) * clipperScale)),
	}
	triPath := clipper.Path{
		&clipper.IntPoint{X: clipper.CInt(triA.X), Y: clipper.CInt(triA.Y)},
		&clipper.IntPoint{X: clipper.CInt(triB.X), Y: clipper.CInt(triB.Y)},
		&clipper.IntPoint{X: clipper.CInt(triC.X), Y: clipper.CInt(triC.Y)},
	}

	for _, ci := range candidates {
		cell := &slabs[slabIdx].Cells[ci]
		cellPath := pointsToClipperPath(cell.Outer)
		c := clipper.NewClipper(clipper.IoNone)
		c.AddPaths(clipper.Paths{triPath}, clipper.PtSubject, true)
		c.AddPaths(clipper.Paths{cellPath}, clipper.PtClip, true)
		result, ok := c.Execute1(clipper.CtIntersection, clipper.PftNonZero, clipper.PftNonZero)
		if !ok {
			continue
		}
		for _, path := range result {
			poly := clipperPathToIntPts(path)
			if len(poly) < 3 {
				continue
			}
			// No dedup pass: Clipper output is already free of
			// consecutive duplicates (clipperPathToIntPts strips
			// them and the closing duplicate). The wall path needs
			// dedup3DPoly because clipPolyByPlaneXY can emit a
			// vertex twice when an edge is flush with a clip plane.
			dst = append(dst, capPiece{cellIdx: int32(ci), poly: poly, t: t})
			for _, p := range poly {
				seen[p] = struct{}{}
			}
		}
	}
	return dst, candidates
}

// clipSlabVerticalPieces is phase 1 for vertical source polys.
func clipSlabVerticalPieces(
	vp slabVerticalPoly,
	slabIdx int,
	slabs []Slab,
	idx *slabCellIndex,
	dst []wallPiece,
	seen map[int3D]struct{},
	candidateBuf []int,
) ([]wallPiece, []int) {
	if len(vp.Pts) < 3 {
		return dst, candidateBuf
	}
	minX, minY := vp.Pts[0][0], vp.Pts[0][1]
	maxX, maxY := minX, minY
	for _, p := range vp.Pts[1:] {
		if p[0] < minX {
			minX = p[0]
		} else if p[0] > maxX {
			maxX = p[0]
		}
		if p[1] < minY {
			minY = p[1]
		} else if p[1] > maxY {
			maxY = p[1]
		}
	}
	candidates := idx.candidates(minX, minY, maxX, maxY, candidateBuf)
	if len(candidates) == 0 {
		return dst, candidates
	}
	for _, ci := range candidates {
		cell := &slabs[slabIdx].Cells[ci]
		cellPieces := clipVerticalPolyToCell(vp.Pts, cell.Outer)
		for _, clipped := range cellPieces {
			clipped = dedup3DPoly(clipped)
			if len(clipped) < 3 {
				continue
			}
			dst = append(dst, wallPiece{cellIdx: int32(ci), pts: clipped, normal: vp.Normal})
			for _, p := range clipped {
				seen[int3DOf(p)] = struct{}{}
			}
		}
	}
	return dst, candidates
}

// appendCapPiece splices pc against the slab-wide 2D splice set,
// earcuts the result, Z-lifts each vertex via barycentric on pc.t,
// and appends to (verts, faces, localIdx) — returning the updated
// slices. Each emitted face is tagged with pc.cellIdx in localIdx.
func appendCapPiece(
	pc capPiece,
	spliceSet []intPt,
	zBot, zTop float32,
	verts [][3]float32,
	faces [][3]uint32,
	localIdx []int32,
) ([][3]float32, [][3]uint32, []int32) {
	spliced := splicePolyIntEdges(pc.poly, spliceSet)
	if len(spliced) < 3 {
		return verts, faces, localIdx
	}
	floatPoly := make([]Point2, len(spliced))
	for i, p := range spliced {
		floatPoly[i] = intPtToPoint2(p)
	}
	earVerts, earTris := Earcut(floatPoly, nil)
	if len(earTris) == 0 || len(earVerts) != len(floatPoly) {
		return verts, faces, localIdx
	}
	base := uint32(len(verts))
	t := pc.t
	for _, p := range floatPoly {
		areaA := ((t.V1[0]-p[0])*(t.V2[1]-p[1]) - (t.V2[0]-p[0])*(t.V1[1]-p[1])) * t.InvAreaXY
		areaB := ((t.V2[0]-p[0])*(t.V0[1]-p[1]) - (t.V0[0]-p[0])*(t.V2[1]-p[1])) * t.InvAreaXY
		areaC := 1 - areaA - areaB
		z := areaA*t.V0[2] + areaB*t.V1[2] + areaC*t.V2[2]
		if z < zBot {
			z = zBot
		} else if z > zTop {
			z = zTop
		}
		verts = append(verts, [3]float32{p[0], p[1], z})
	}
	// Zero-area earcut tris are kept on purpose — they carry boundary-
	// edge connectivity for collinear boundary vertices V_a-V_b-V_c
	// (when the slab-wide splice inserts V_b on a straight edge that
	// V_a→V_c already covered). Dropping them turns V_a-V_b / V_b-V_c
	// into 1-use boundary edges and re-introduces the T-junctions the
	// splice set was added to eliminate.
	for _, tri := range earTris {
		if t.InvAreaXY < 0 {
			faces = append(faces, [3]uint32{base + tri[0], base + tri[2], base + tri[1]})
		} else {
			faces = append(faces, [3]uint32{base + tri[0], base + tri[1], base + tri[2]})
		}
		localIdx = append(localIdx, pc.cellIdx)
	}
	return verts, faces, localIdx
}

// appendWallPiece splices pc against the slab-wide 3D set, fan-
// triangulates with winding matched to pc.normal, and appends to
// (verts, faces, localIdx).
func appendWallPiece(
	pc wallPiece,
	spliceSet []int3D,
	verts [][3]float32,
	faces [][3]uint32,
	localIdx []int32,
) ([][3]float32, [][3]uint32, []int32) {
	spliced := splicePoly3DEdges(pc.pts, spliceSet)
	if len(spliced) < 3 {
		return verts, faces, localIdx
	}
	base := uint32(len(verts))
	verts = append(verts, spliced...)
	for i := 1; i < len(spliced)-1; i++ {
		triN := triangleNormal(spliced[0], spliced[i], spliced[i+1])
		dot := triN[0]*pc.normal[0] + triN[1]*pc.normal[1] + triN[2]*pc.normal[2]
		if dot >= 0 {
			faces = append(faces, [3]uint32{base, base + uint32(i), base + uint32(i+1)})
		} else {
			faces = append(faces, [3]uint32{base, base + uint32(i+1), base + uint32(i)})
		}
		localIdx = append(localIdx, pc.cellIdx)
	}
	return verts, faces, localIdx
}

// dedup3DPoly removes consecutive (in Clipper-integer space) duplicate
// vertices and a trailing closing duplicate. Sutherland-Hodgman can
// emit a vertex twice when the polygon's edge is flush with a clip
// plane (or grazes a vertex), and the duplicates produce zero-area
// triangles after fan-triangulation — those triangles show up as
// shared edges on the wall (count=4 / count=6 boundary-edge buckets).
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

// splicePoly3DEdges is splicePolyIntEdges in 3D. The vertical-clip
// pieces lie in the source triangle's plane (planar 3D polygons),
// so a 3D collinearity test on each edge picks up cell-boundary
// kinks that pose as T-junctions between adjacent cells along the
// wall.
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


