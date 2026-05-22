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

	clipper "github.com/ctessum/go.clipper"
)

// intPt is a Clipper-integer XY point. Used by intermediate
// conversions to/from Clipper's integer space.
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

// clipPolyToCellsCap is the Clipper 2D intersection path. New
// vertices introduced by the intersection get Z from the source
// polygon's plane equation: z = (d - n.x*x - n.y*y) / n.z, with
// (n.x, n.y, n.z) the source triangle's normal and d the plane
// offset. The dispatcher's isPolyXYDegenerate check bounds n.z
// away from zero.
func clipPolyToCellsCap(
	p slabPoly,
	slabIdx int,
	slabs []Slab,
	candidates []int,
	dst []cellPiece,
	seen map[int3D]struct{},
) []cellPiece {
	// Source plane equation: n·X = d. Clamp the lifted Z to the
	// slab range — Clipper output vertices on the cell.Outer interior
	// can land at XYs where the source plane evaluates a hair outside
	// [zBot, zTop] (float noise; mathematically the slabPoly's interior
	// is bounded by the slab planes), and a clamp keeps cross-slab
	// seams matching exactly.
	zBot := slabs[slabIdx].ZBot
	zTop := slabs[slabIdx].ZTop
	nx, ny, nz := p.Normal[0], p.Normal[1], p.Normal[2]
	d := nx*p.Pts[0][0] + ny*p.Pts[0][1] + nz*p.Pts[0][2]
	zLift := func(x, y float32) float32 {
		z := (d - nx*x - ny*y) / nz
		if z < zBot {
			z = zBot
		} else if z > zTop {
			z = zTop
		}
		return z
	}

	// Convert source polygon to Clipper integer XY path. Z is
	// dropped for the intersection; we recover it for each output
	// vertex via zLift.
	triPath := make(clipper.Path, 0, len(p.Pts))
	for _, v := range p.Pts {
		triPath = append(triPath, &clipper.IntPoint{
			X: clipper.CInt(math.Round(float64(v[0]) * clipperScale)),
			Y: clipper.CInt(math.Round(float64(v[1]) * clipperScale)),
		})
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
			ring := clipperPathToIntPts(path)
			if len(ring) < 3 {
				continue
			}
			pts := make([][3]float32, len(ring))
			for i, ip := range ring {
				p2 := intPtToPoint2(ip)
				pts[i] = [3]float32{p2[0], p2[1], zLift(p2[0], p2[1])}
				seen[int3DOf(pts[i])] = struct{}{}
			}
			dst = append(dst, cellPiece{
				cellIdx: int32(ci),
				pts:     pts,
				normal:  p.Normal,
			})
		}
	}
	return dst
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
) ([][3]float32, [][3]uint32, []int32) {
	spliced := splicePoly3DEdges(pc.pts, spliceSet)
	if len(spliced) < 3 {
		return verts, faces, localIdx
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
	earVerts, earTris := Earcut(floatPoly, nil)
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
