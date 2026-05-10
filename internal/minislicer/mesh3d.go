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

	for li, layer := range layers {
		zBot := layer.Z - layerH/2
		zTop := layer.Z + layerH/2
		for lp := range layer.Loops {
			loop := &layer.Loops[lp]
			ids := loopSecs[loopKey{li, lp}]
			if len(ids) == 0 {
				continue
			}
			// For the prototype we treat every loop as an outer
			// boundary. Hole-vs-outer requires nesting analysis; a
			// CW (signed area < 0) orientation here just means our
			// segment-chaining picked the reverse winding, not that
			// the loop is a hole. emitLoopPrism reorients via
			// signed area so the wall normals always face outward.
			emitLoopPrism(m, &faceAssign, loop, ids, sections, assignments, zBot, zTop)
		}
	}

	return m, faceAssign
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
) {
	pts := loop.Points
	n := len(pts)
	if n < 3 {
		return
	}
	// We assume CCW orientation downstream (outward wall normal,
	// +Z top cap, −Z bottom cap). Reverse the polygon if the
	// slicer chained it CW so the rest of this function sees a
	// canonical CCW loop.
	ccw := loop.SignedArea > 0
	if !ccw {
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
	// part of the visible exterior wall.
	cBot := uint32(len(m.Vertices))
	m.Vertices = append(m.Vertices, [3]float32{centroid[0], centroid[1], zBot})
	cTop := uint32(len(m.Vertices))
	m.Vertices = append(m.Vertices, [3]float32{centroid[0], centroid[1], zTop})

	for i := 0; i < w; i++ {
		j := (i + 1) % w
		lo_i := baseV + uint32(2*i)
		lo_j := baseV + uint32(2*j)
		hi_i := baseV + uint32(2*i+1)
		hi_j := baseV + uint32(2*j+1)
		// Bottom cap: fan from cBot, winding for -Z normal.
		m.Faces = append(m.Faces, [3]uint32{cBot, lo_j, lo_i})
		// Top cap: fan from cTop, winding for +Z normal.
		m.Faces = append(m.Faces, [3]uint32{cTop, hi_i, hi_j})
		*faceAssign = append(*faceAssign, -1, -1)
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

