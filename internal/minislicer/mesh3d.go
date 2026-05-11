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
	m, faceAssign, _ := BuildPrintableMeshFull(layers, sections, assignments, layerH)
	return m, faceAssign
}

// BuildPrintableMeshFull is BuildPrintableMesh plus a parallel
// faceSection slice: faceSection[i] is the index into `sections`
// that produced face i, or -1 for faces that don't trace to a
// single section (earcut cap interior triangles). Used by the
// pipeline's ShowSampledColors debug mode to color faces by the
// originating section's raw sampled RGB instead of its dithered
// palette index.
func BuildPrintableMeshFull(layers []Layer, sections []Section, assignments []int32, layerH float32) (*loader.LoadedModel, []int32, []int32) {
	m := &loader.LoadedModel{}
	var faceAssign []int32
	var faceSection []int32

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

	// Per-(layer, loop) ribbon midpoints, used to find the nearest
	// ribbon section for each earcut cap triangle's centroid. The
	// lookup is scoped to the specific outer loop being capped + its
	// hole children — not "any ribbon in the layer", which leaks
	// color across disjoint outer loops (e.g. the cutting board's
	// top cap picking up the fish's color because the fish loop is
	// closer to most cap interior points than the cutting board's
	// own perimeter).
	layerLoopRibbons := make(map[loopKey][]ribbonRef)
	for i, s := range sections {
		if s.Kind != KindRibbon {
			continue
		}
		k := loopKey{s.LayerIdx, s.LoopIdx}
		layerLoopRibbons[k] = append(layerLoopRibbons[k], ribbonRef{int32(i), s.Mid})
	}

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
			emitLoopWall(m, &faceAssign, &faceSection, loop, ids, sections, assignments,
				zBot, zTop, loop.IsHole)
		}

		// Caps: one earcut per outer (outer minus direct hole
		// children) ALWAYS emitted at the exact Z face. When a
		// layer also has tiled cap sections covering the exposed
		// portion of the cap, those tiles are emitted at a Z
		// pushed slightly outward (+capTileEpsilon for top,
		// −capTileEpsilon for bottom) so they win the depth test
		// from outside the model. The earcut underneath fills the
		// "covered" remainder so adjacent layers don't see through
		// to nothing when their footprints don't perfectly align.
		_ = hasTopCap
		_ = hasBotCap
		for lp := range layer.Loops {
			outer := &layer.Loops[lp]
			if outer.IsHole {
				continue
			}
			holes, _ := collectChildHolesWithIdx(layer.Loops, lp)
			// Scope nearest-ribbon to THIS outer loop's own ribbons
			// only. Holes are not included even when they're real
			// cavities, because:
			//   - For true cavities, the cap material is bounded by
			//     the outer; outer perimeter is on the same surface
			//     as the cap, so its sample matches.
			//   - For "holes" that are actually a separate object's
			//     outer loop nested inside this one, the hole's
			//     ribbons sample an unrelated material and would
			//     leak that color into the cap.
			ribbons := layerLoopRibbons[loopKey{li, lp}]
			// Top vs bottom cap colorers prefer ribbons whose
			// source-triangle normal matches the cap's facing
			// direction; falls back to plain nearest-XY when no
			// candidate matches the orientation. See pickRibbon for
			// the scoring detail.
			colorAtTop := makeColorAt(ribbons, sections, assignments, fallback, +1)
			colorAtBot := makeColorAt(ribbons, sections, assignments, fallback, -1)
			emitEarcutCap(m, &faceAssign, &faceSection, outer.Points, holes, zTop, true, colorAtTop)
			emitEarcutCap(m, &faceAssign, &faceSection, outer.Points, holes, zBot, false, colorAtBot)
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
		emitCapTiles(m, &faceAssign, &faceSection, ids, sections, assignments)
	}

	return m, faceAssign, faceSection
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
	holes, _ := collectChildHolesWithIdx(loops, outerIdx)
	return holes
}

// collectChildHolesWithIdx is collectChildHoles that also returns
// the per-hole loop index in `loops` for each returned hole.
func collectChildHolesWithIdx(loops []Loop, outerIdx int) ([][]Point2, []int) {
	outer := &loops[outerIdx]
	var out [][]Point2
	var idxs []int
	for hi := range loops {
		hole := &loops[hi]
		if !hole.IsHole || len(hole.Points) < 3 {
			continue
		}
		if !pointInPolygon(outer.Points, hole.Points[0][0], hole.Points[0][1]) {
			continue
		}
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
			idxs = append(idxs, hi)
		}
	}
	return out, idxs
}

// makeColorAt builds an earcut-cap color callback. It picks a
// ribbon section to source the cap face's section index and
// dithered-palette index from, preferring ribbons whose source
// triangle is roughly aligned with the cap's facing direction:
//
//   - capDir = +1 → top cap (face normal +Z) → prefer ribbons
//     with source-tri normal_z > 0 (e.g. an upper-dome side
//     triangle, whose surface lies above the cap material).
//   - capDir = -1 → bottom cap → prefer normal_z < 0.
//
// A vertical "wall" triangle (cut surface, side of a box) has
// normal_z ≈ 0 and is ineligible under either preference, so
// it's only used as a fallback when no aligned ribbon exists.
// Among aligned candidates we pick the nearest in XY distance,
// the same metric the previous version used unconditionally.
//
// This filter exists to stop a salmon-colored cut surface inside
// a fish dome (normal_z ≈ 0) from being chosen as the nearest
// ribbon for a dome-region cap, which manifests as horizontal
// salmon stripes in front/side renderings (the cap edge-on at
// each Z layer).
func makeColorAt(
	ribbons []ribbonRef,
	sections []Section,
	assignments []int32,
	fallback int32,
	capDir float32,
) func(p Point2) (int32, int32) {
	const alignedThresh = 0.05
	return func(p Point2) (int32, int32) {
		if len(ribbons) == 0 {
			return -1, fallback
		}
		bestSid := int32(-1)
		bestColor := fallback
		bestSq := float32(1e30)
		bestAligned := false
		for _, r := range ribbons {
			dx := r.mid[0] - p[0]
			dy := r.mid[1] - p[1]
			d := dx*dx + dy*dy
			aligned := sections[r.sid].SrcTriNormalZ*capDir > alignedThresh
			// Aligned candidates dominate non-aligned ones outright
			// (even at greater XY distance). Within each tier we
			// take the nearest in XY.
			switch {
			case aligned && !bestAligned:
				bestSq = d
				bestSid = r.sid
				bestAligned = true
				if assignments[r.sid] >= 0 {
					bestColor = assignments[r.sid]
				}
			case aligned == bestAligned && d < bestSq:
				bestSq = d
				bestSid = r.sid
				if assignments[r.sid] >= 0 {
					bestColor = assignments[r.sid]
				}
			}
		}
		return bestSid, bestColor
	}
}

// ribbonRef is one ribbon section's id and XY midpoint, used by
// the earcut color callback for nearest-ribbon lookup.
type ribbonRef struct {
	sid int32
	mid Point2
}

// emitEarcutCap triangulates outer + holes via Earcut and appends
// the triangles to m at the given Z. isTop selects the winding so
// the normal faces +Z (top cap) or -Z (bottom cap). Each triangle's
// faceSection + faceAssign come from the colorAt(centroid)
// callback — typically "nearest visible ribbon section in this
// layer." Falls back to (-1, fallback) when no ribbons exist.
func emitEarcutCap(
	m *loader.LoadedModel,
	faceAssign *[]int32,
	faceSection *[]int32,
	outer []Point2,
	holes [][]Point2,
	z float32,
	isTop bool,
	colorAt func(p Point2) (sid, color int32),
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
		a := verts[tr[0]]
		b := verts[tr[1]]
		c := verts[tr[2]]
		centroid := Point2{(a[0] + b[0] + c[0]) / 3, (a[1] + b[1] + c[1]) / 3}
		sid, col := colorAt(centroid)
		if isTop {
			m.Faces = append(m.Faces, [3]uint32{baseV + tr[0], baseV + tr[1], baseV + tr[2]})
		} else {
			m.Faces = append(m.Faces, [3]uint32{baseV + tr[0], baseV + tr[2], baseV + tr[1]})
		}
		*faceAssign = append(*faceAssign, col)
		*faceSection = append(*faceSection, sid)
	}
}

// emitCapTiles emits 2 triangles per cap section forming the tile
// rectangle. Top caps wind CCW (normal +Z); bottom caps wind CW
// (normal -Z) so they face outward. Both triangles per tile are
// tagged with the originating section index in faceSection.
func emitCapTiles(
	m *loader.LoadedModel,
	faceAssign *[]int32,
	faceSection *[]int32,
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
		*faceSection = append(*faceSection, int32(sid), int32(sid))
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
	faceSection *[]int32,
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
	wallSections := make([]int32, 0, n*2)
	for _, sid := range sectionIDs {
		s := sections[sid]
		color := assignments[sid]
		wallPts = append(wallPts, pointAtArc(pts, cum, s.StartArc))
		wallColors = append(wallColors, color)
		wallSections = append(wallSections, int32(sid))
		for i := 0; i < n; i++ {
			a := cum[i]
			if a > s.StartArc+1e-5 && a < s.EndArc-1e-5 {
				wallPts = append(wallPts, pts[i])
				wallColors = append(wallColors, color)
				wallSections = append(wallSections, int32(sid))
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
		sec := wallSections[i]
		*faceAssign = append(*faceAssign, col, col)
		*faceSection = append(*faceSection, sec, sec)
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
