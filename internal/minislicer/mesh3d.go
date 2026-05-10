package minislicer

import (
	"github.com/rtwfroody/ditherforge/internal/loader"
)

// BuildPrintableMesh assembles a 3D triangle mesh from the per-layer
// contour partition. Each outer loop becomes a layer prism (top cap
// + bottom cap + side wall) of height layerH; the side wall is
// segmented by section so each face carries the assigned palette
// index.
//
// The output is a flat triangle list packed into a LoadedModel
// (Vertices + Faces only) plus a parallel `assignments` slice with
// one palette index per face. assignments[i] >= 0 indexes into the
// palette; -1 marks "interior" faces (top/bottom caps and seam
// quads) which are not visible on the print.
//
// Limitations of this prototype:
//   - Hole loops (signed area < 0) are skipped. The output for
//     models with internal cavities is therefore conservative.
//   - Top and bottom caps are fan-triangulated from the polygon
//     centroid, which is correct for convex / star-shaped contours
//     and may produce extraneous triangles for strongly concave
//     ones. Acceptable for the visual prototype.
//   - Adjacent layer prisms share Z faces but those are emitted
//     independently per layer (not welded). The stack is still
//     watertight per layer.
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

	// Determine which layers have tiled top/bottom caps so the
	// ribbon-prism emitter can skip the fan-triangulated fallback
	// at those Z faces. Currently keyed only by LayerIdx (not by
	// island), so a multi-island topmost layer either has tiles for
	// every island or for none.
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

	// Emit ribbon walls (one prism per ribbon loop). Cap geometry
	// rules:
	//   - Hole loops: never emit caps; cavity stays open.
	//   - Outer loops with hole children: skip the fan cap (a fan
	//     from the centroid would cover the hole region). A real
	//     polygon-with-holes cap would go here in the future; for
	//     now adjacent layers' caps generally seal the gap.
	//   - Outer loops without hole children: emit fan caps unless a
	//     tiled top/bottom cap is painting over them. This applies
	//     even when other outers in the same layer DO have holes —
	//     a multi-island layer with one boat-hull-with-cavity and
	//     a separate solid smokestack should still cap the
	//     smokestack.
	for li, layer := range layers {
		zBot := layer.Z - layerH/2
		zTop := layer.Z + layerH/2
		for lp := range layer.Loops {
			loop := &layer.Loops[lp]
			ids := loopSecs[loopKey{li, lp}]
			if len(ids) == 0 || sections[ids[0]].Kind != KindRibbon {
				continue
			}
			isHole := loop.IsHole
			capUntileable := isHole || loop.HasHoleChild
			skipTop := capUntileable || hasTopCap[layer.LayerIdx]
			skipBot := capUntileable || hasBotCap[layer.LayerIdx]
			emitLoopPrism(m, &faceAssign, loop, ids, sections, assignments,
				zBot, zTop, isHole, skipTop, skipBot)
		}
	}

	// Emit tiled caps. One quad per cap section, painted with the
	// section's palette index. The Z lives in the section itself.
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
		// Tile corner order: 0=(x0,y0), 1=(x1,y0), 2=(x1,y1), 3=(x0,y1).
		m.Vertices = append(m.Vertices,
			[3]float32{x0, y0, z},
			[3]float32{x1, y0, z},
			[3]float32{x1, y1, z},
			[3]float32{x0, y1, z})
		col := assignments[sid]
		if s.Kind == KindCapTop {
			// CCW triangles: 0-1-2, 0-2-3.
			m.Faces = append(m.Faces,
				[3]uint32{baseV, baseV + 1, baseV + 2},
				[3]uint32{baseV, baseV + 2, baseV + 3})
		} else {
			// CW triangles: 0-2-1, 0-3-2.
			m.Faces = append(m.Faces,
				[3]uint32{baseV, baseV + 2, baseV + 1},
				[3]uint32{baseV, baseV + 3, baseV + 2})
		}
		*faceAssign = append(*faceAssign, col, col)
	}
}

// emitLoopPrism appends the geometry for one closed CCW polygon
// extruded between zBot and zTop into m. faceAssign receives one
// palette index per emitted face (-1 for caps).
func emitLoopPrism(
	m *loader.LoadedModel,
	faceAssign *[]int32,
	loop *Loop,
	sectionIDs []int,
	sections []Section,
	assignments []int32,
	zBot, zTop float32,
	isHole bool,
	skipTopCap bool,
	skipBottomCap bool,
) {
	pts := loop.Points
	n := len(pts)
	if n < 3 {
		return
	}
	// Outer loops want CCW (interior = print material; wall normal
	// faces outward). Hole loops want CW (interior = cavity; wall
	// normal faces into the cavity). Reverse the polygon if it's
	// not in the desired winding for its role.
	ccw := loop.SignedArea > 0
	want := !isHole // outer wants CCW, hole wants CW
	if ccw != want {
		rev := make([]Point2, n)
		for i, p := range pts {
			rev[n-1-i] = p
		}
		pts = rev
	}
	cum := loopCumLen(pts)

	// Centroid for fan triangulation of the caps.
	var cx, cy float64
	for _, p := range pts {
		cx += float64(p[0])
		cy += float64(p[1])
	}
	cx /= float64(n)
	cy /= float64(n)
	centroid := Point2{float32(cx), float32(cy)}

	// Build the wall as a sequence of (XY point, palette index)
	// pairs. A wall vertex is generated at every loop vertex and at
	// every section start (to introduce a color seam there).
	wallPts := make([]Point2, 0, n*2)
	wallColors := make([]int32, 0, n*2)
	// Emit wall vertices in arc order: for each section, push its
	// StartArc point and any intermediate loop vertex strictly
	// inside [StartArc, EndArc). Section's color is the assignment
	// (or -1 if hidden, treated as interior).
	for _, sid := range sectionIDs {
		s := sections[sid]
		color := assignments[sid]
		// Section boundary point at StartArc.
		wallPts = append(wallPts, pointAtArc(pts, cum, s.StartArc))
		wallColors = append(wallColors, color)
		// Loop vertices strictly inside the section's arc range.
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

	// Build the wall: for each consecutive pair (wallPts[i],
	// wallPts[i+1]) in cyclic order, emit two triangles forming a
	// quad between zBot and zTop. The quad's color is wallColors[i]
	// (the color "leaving" wallPts[i] along the wall).
	w := len(wallPts)
	baseV := uint32(len(m.Vertices))
	for _, p := range wallPts {
		m.Vertices = append(m.Vertices, [3]float32{p[0], p[1], zBot})
		m.Vertices = append(m.Vertices, [3]float32{p[0], p[1], zTop})
	}
	// Each pair contributes 2 vertices: (lo[i], hi[i]) at indices
	// baseV+2i, baseV+2i+1.
	for i := 0; i < w; i++ {
		j := (i + 1) % w
		i0 := baseV + uint32(2*i)   // lo[i]
		i1 := baseV + uint32(2*i+1) // hi[i]
		j0 := baseV + uint32(2*j)   // lo[j]
		j1 := baseV + uint32(2*j+1) // hi[j]
		// Outward-facing triangulation: outside is the loop's
		// outer side (CCW assumed). For a CCW loop, the outward
		// normal at edge i→j points to the right of the edge
		// direction, i.e., 90° CW from (j−i). The two triangles
		// lo[i]-lo[j]-hi[j] and lo[i]-hi[j]-hi[i] should wind so
		// their normal is outward.
		m.Faces = append(m.Faces, [3]uint32{i0, j0, j1})
		m.Faces = append(m.Faces, [3]uint32{i0, j1, i1})
		col := wallColors[i]
		*faceAssign = append(*faceAssign, col, col)
	}

	// Caps: fan-triangulate from the centroid. Top cap winds CCW
	// (so normal is +Z). Bottom cap winds CW (so normal is -Z).
	// Each cap face is tagged as -1 (interior) since they're not
	// part of the visible exterior wall — except where the caller
	// has indicated tiled cap sections will paint over them, in
	// which case we omit the fan triangles entirely so the tiles
	// are the only geometry at that Z face.
	var cBot, cTop uint32
	if !skipBottomCap {
		cBot = uint32(len(m.Vertices))
		m.Vertices = append(m.Vertices, [3]float32{centroid[0], centroid[1], zBot})
	}
	if !skipTopCap {
		cTop = uint32(len(m.Vertices))
		m.Vertices = append(m.Vertices, [3]float32{centroid[0], centroid[1], zTop})
	}

	for i := 0; i < w; i++ {
		j := (i + 1) % w
		lo_i := baseV + uint32(2*i)
		lo_j := baseV + uint32(2*j)
		hi_i := baseV + uint32(2*i+1)
		hi_j := baseV + uint32(2*j+1)
		if !skipBottomCap {
			// Bottom cap: fan from cBot, winding for -Z normal.
			m.Faces = append(m.Faces, [3]uint32{cBot, lo_j, lo_i})
			*faceAssign = append(*faceAssign, -1)
		}
		if !skipTopCap {
			// Top cap: fan from cTop, winding for +Z normal.
			m.Faces = append(m.Faces, [3]uint32{cTop, hi_i, hi_j})
			*faceAssign = append(*faceAssign, -1)
		}
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

