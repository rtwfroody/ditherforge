package minislicer

import (
	"github.com/rtwfroody/ditherforge/internal/loader"
)

// BuildPrintableMesh assembles a 3D triangle mesh from the per-layer
// contour partition.
//
// Geometry per layer:
//   - Walls: one tube per loop (outer or hole), painted per ribbon
//     section. Outer-loop walls face outward; hole-loop walls face
//     inward into the cavity.
//   - Caps: per-outer earcut of (outer minus immediate hole
//     children) at the top and bottom Z faces. Caps are emitted with
//     the model's most-common visible color as a fallback (cap
//     faces are interior to the print and not separately
//     colored). When a layer's top or bottom face is being tiled
//     by cap sections (the visible top/bottom of the model), the
//     earcut cap at that Z is skipped so the tiles are the only
//     geometry there.
//
// The output is a flat triangle list packed into a LoadedModel
// (Vertices + Faces only) plus a parallel `assignments` slice with
// one palette index per face.
func BuildPrintableMesh(layers []Layer, sections []Section, assignments []int32, layerH float32) (*loader.LoadedModel, []int32) {
	m := &loader.LoadedModel{}
	var faceAssign []int32

	type loopKey struct{ layer, loop int }
	loopSecs := make(map[loopKey][]int)
	for i, s := range sections {
		loopSecs[loopKey{s.LayerIdx, s.LoopIdx}] = append(loopSecs[loopKey{s.LayerIdx, s.LoopIdx}], i)
	}
	for _, ids := range loopSecs {
		sortByIndex(ids, sections)
	}

	hasTopCap := make(map[int]bool)
	hasBotCap := make(map[int]bool)
	for _, s := range sections {
		switch s.Kind {
		case KindCapTop:
			hasTopCap[s.LayerIdx] = true
		case KindCapBottom:
			hasBotCap[s.LayerIdx] = true
		}
	}

	fallback := mostCommonNonNegSafe(assignments)

	for li, layer := range layers {
		zBot := layer.Z - layerH/2
		zTop := layer.Z + layerH/2

		// Walls: every loop (outer or hole) gets walls; cap fans are
		// no longer emitted here.
		for lp := range layer.Loops {
			loop := &layer.Loops[lp]
			ids := loopSecs[loopKey{li, lp}]
			if len(ids) == 0 || sections[ids[0]].Kind != KindRibbon {
				continue
			}
			emitLoopWall(m, &faceAssign, loop, ids, sections, assignments,
				zBot, zTop, loop.IsHole)
		}

		// Caps: one earcut per outer (outer minus direct hole
		// children). Skipped per-face when a tiled cap is covering
		// that Z face.
		for lp := range layer.Loops {
			outer := &layer.Loops[lp]
			if outer.IsHole {
				continue
			}
			holes := collectChildHoles(layer.Loops, lp)
			if !hasTopCap[layer.LayerIdx] {
				emitEarcutCap(m, &faceAssign, outer.Points, holes, zTop, true, fallback)
			}
			if !hasBotCap[layer.LayerIdx] {
				emitEarcutCap(m, &faceAssign, outer.Points, holes, zBot, false, fallback)
			}
		}
	}

	// Tiled caps for the visible top/bottom of the model.
	for _, ids := range loopSecs {
		if len(ids) == 0 {
			continue
		}
		k := sections[ids[0]].Kind
		if k != KindCapTop && k != KindCapBottom {
			continue
		}
		emitCapTiles(m, &faceAssign, ids, sections, assignments)
	}

	return m, faceAssign
}

// mostCommonNonNegSafe returns the most frequent non-negative
// palette index in a, or 0 if there are no non-negative entries.
func mostCommonNonNegSafe(a []int32) int32 {
	counts := map[int32]int{}
	for _, v := range a {
		if v >= 0 {
			counts[v]++
		}
	}
	var best int32
	bestN := -1
	for k, n := range counts {
		if n > bestN {
			best = k
			bestN = n
		}
	}
	return best
}

// collectChildHoles returns the points of every hole loop that's a
// direct child of `loops[outerIdx]` — inside this outer, and not
// inside any other outer that's itself inside this one.
//
// Vertex-based containment (consistent with classifyHoles); two
// non-intersecting polygons in the slicer's output have all of
// one's vertices either entirely inside or entirely outside the
// other, so testing the first vertex is sufficient.
func collectChildHoles(loops []Loop, outerIdx int) [][]Point2 {
	outer := &loops[outerIdx]
	var out [][]Point2
	for hi := range loops {
		hole := &loops[hi]
		if !hole.IsHole || len(hole.Points) < 3 {
			continue
		}
		if !pointInPolygon(outer.Points, hole.Points[0][0], hole.Points[0][1]) {
			continue
		}
		// Reject if another outer (different from ours) sits between
		// us and this hole.
		isDirect := true
		for oj := range loops {
			if oj == outerIdx || loops[oj].IsHole {
				continue
			}
			other := &loops[oj]
			if len(other.Points) < 1 {
				continue
			}
			if pointInPolygon(other.Points, hole.Points[0][0], hole.Points[0][1]) &&
				pointInPolygon(outer.Points, other.Points[0][0], other.Points[0][1]) {
				isDirect = false
				break
			}
		}
		if isDirect {
			out = append(out, hole.Points)
		}
	}
	return out
}

// emitEarcutCap triangulates outer + holes via Earcut and appends
// the triangles to m at the given Z. isTop selects the winding so
// the normal faces +Z (top cap) or -Z (bottom cap).
func emitEarcutCap(
	m *loader.LoadedModel,
	faceAssign *[]int32,
	outer []Point2,
	holes [][]Point2,
	z float32,
	isTop bool,
	color int32,
) {
	if len(outer) < 3 {
		return
	}
	verts, tris := Earcut(outer, holes)
	if len(tris) == 0 {
		return
	}
	baseV := uint32(len(m.Vertices))
	for _, p := range verts {
		m.Vertices = append(m.Vertices, [3]float32{p[0], p[1], z})
	}
	for _, tr := range tris {
		if isTop {
			m.Faces = append(m.Faces, [3]uint32{baseV + tr[0], baseV + tr[1], baseV + tr[2]})
		} else {
			// Reverse winding so the bottom cap's normal points -Z.
			m.Faces = append(m.Faces, [3]uint32{baseV + tr[0], baseV + tr[2], baseV + tr[1]})
		}
		*faceAssign = append(*faceAssign, color)
	}
}

// emitCapTiles emits 2 triangles per cap section forming the tile
// rectangle. Top caps wind CCW (normal +Z); bottom caps wind CW
// (normal -Z) so they face outward.
func emitCapTiles(
	m *loader.LoadedModel,
	faceAssign *[]int32,
	sectionIDs []int,
	sections []Section,
	assignments []int32,
) {
	for _, sid := range sectionIDs {
		s := sections[sid]
		x0, y0, x1, y1 := s.CapBoundsXY[0], s.CapBoundsXY[1], s.CapBoundsXY[2], s.CapBoundsXY[3]
		z := s.Z
		baseV := uint32(len(m.Vertices))
		m.Vertices = append(m.Vertices,
			[3]float32{x0, y0, z},
			[3]float32{x1, y0, z},
			[3]float32{x1, y1, z},
			[3]float32{x0, y1, z})
		col := assignments[sid]
		if s.Kind == KindCapTop {
			m.Faces = append(m.Faces,
				[3]uint32{baseV, baseV + 1, baseV + 2},
				[3]uint32{baseV, baseV + 2, baseV + 3})
		} else {
			m.Faces = append(m.Faces,
				[3]uint32{baseV, baseV + 2, baseV + 1},
				[3]uint32{baseV, baseV + 3, baseV + 2})
		}
		*faceAssign = append(*faceAssign, col, col)
	}
}

// emitLoopWall appends only the wall geometry for one loop — no
// caps. faceAssign receives one palette index per triangle (each
// quad contributes two).
//
// Outer loops want CCW orientation (interior = print material; wall
// normal faces outward). Hole loops want CW orientation (interior =
// cavity; wall normal faces into the cavity). The polygon is
// re-oriented as needed before walking.
func emitLoopWall(
	m *loader.LoadedModel,
	faceAssign *[]int32,
	loop *Loop,
	sectionIDs []int,
	sections []Section,
	assignments []int32,
	zBot, zTop float32,
	isHole bool,
) {
	pts := loop.Points
	n := len(pts)
	if n < 3 {
		return
	}
	ccw := loop.SignedArea > 0
	want := !isHole
	if ccw != want {
		rev := make([]Point2, n)
		for i, p := range pts {
			rev[n-1-i] = p
		}
		pts = rev
	}
	cum := loopCumLen(pts)

	wallPts := make([]Point2, 0, n*2)
	wallColors := make([]int32, 0, n*2)
	for _, sid := range sectionIDs {
		s := sections[sid]
		color := assignments[sid]
		wallPts = append(wallPts, pointAtArc(pts, cum, s.StartArc))
		wallColors = append(wallColors, color)
		for i := 0; i < n; i++ {
			a := cum[i]
			if a > s.StartArc+1e-5 && a < s.EndArc-1e-5 {
				wallPts = append(wallPts, pts[i])
				wallColors = append(wallColors, color)
			}
		}
	}
	if len(wallPts) < 3 {
		return
	}

	w := len(wallPts)
	baseV := uint32(len(m.Vertices))
	for _, p := range wallPts {
		m.Vertices = append(m.Vertices, [3]float32{p[0], p[1], zBot})
		m.Vertices = append(m.Vertices, [3]float32{p[0], p[1], zTop})
	}
	for i := 0; i < w; i++ {
		j := (i + 1) % w
		i0 := baseV + uint32(2*i)
		i1 := baseV + uint32(2*i+1)
		j0 := baseV + uint32(2*j)
		j1 := baseV + uint32(2*j+1)
		m.Faces = append(m.Faces, [3]uint32{i0, j0, j1})
		m.Faces = append(m.Faces, [3]uint32{i0, j1, i1})
		col := wallColors[i]
		*faceAssign = append(*faceAssign, col, col)
	}
}

// SafeAssignments substitutes -1 entries (interior faces) with a
// fallback palette index so downstream tools that don't tolerate
// negative assignments behave correctly. Returns a new slice.
func SafeAssignments(assignments []int32, fallback int32) []int32 {
	out := make([]int32, len(assignments))
	for i, a := range assignments {
		if a < 0 {
			out[i] = fallback
		} else {
			out[i] = a
		}
	}
	return out
}

// MeshExtents returns the XYZ bbox of m's vertices for diagnostics.
func MeshExtents(m *loader.LoadedModel) (mn, mx [3]float32) {
	if len(m.Vertices) == 0 {
		return
	}
	mn = m.Vertices[0]
	mx = m.Vertices[0]
	for _, v := range m.Vertices {
		for k := 0; k < 3; k++ {
			if v[k] < mn[k] {
				mn[k] = v[k]
			}
			if v[k] > mx[k] {
				mx[k] = v[k]
			}
		}
	}
	return
}
