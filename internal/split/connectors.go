package split

import (
	"math"
)

// connectorSegments is the polygonal approximation count for connector
// circles. 16 segments gives a max radial error of about r·(1−cos(π/16))
// ≈ 1.9% — fine for FDM print resolution.
const connectorSegments = 16

// connectorPlacement records one connector position and the per-half
// metadata generated during placement and hole insertion. After
// addConnectorHoles has run, LoopVerts[h] holds the 16 cap-plane
// vertex indices (in halves[h].Vertices) that form the connector
// circle. addConnectorBodies later uses these as the bottom ring of
// each half's cylindrical body (peg on the male side, pocket on the
// female side).
type connectorPlacement struct {
	Pos3D     [3]float64 // on the cut plane
	Radius    [2]float64 // [0] for half 0, [1] for half 1
	HasBody   [2]bool    // true → emit cylinder/pocket geometry
	BodyDepth float64    // distance the body extends along cap normal
	LoopVerts [2][]uint32
}

// placeConnectors picks 1..3 connector positions on the cut polygon
// and returns them along with per-half radii and body flags. Returns
// nil when the polygon is too small to fit even a single connector
// with the required boundary margin.
func (b *cutBuilder) placeConnectors(loops [2][][]uint32, plane Plane, settings ConnectorSettings) []connectorPlacement {
	if settings.Style == NoConnectors || settings.DiamMM <= 0 {
		return nil
	}

	// Project half 0's loops to 2D in half 0's cap basis (u × v = +n).
	u, v := planeBasis(plane.Normal)
	half := b.halves[0]
	loop2d := make([][]pt2, 0, len(loops[0]))
	for _, loop := range loops[0] {
		pts := make([]pt2, len(loop))
		for k, vi := range loop {
			pts[k] = project3Dto2D(half.Vertices[vi], u, v)
		}
		loop2d = append(loop2d, pts)
	}
	if len(loop2d) == 0 {
		return nil
	}

	// Outer = largest |area|; everything else is a hole / cavity.
	outerI := 0
	bestArea := math.Abs(signedArea(loop2d[0]))
	for i := 1; i < len(loop2d); i++ {
		a := math.Abs(signedArea(loop2d[i]))
		if a > bestArea {
			bestArea = a
			outerI = i
		}
	}
	outer := loop2d[outerI]
	holes := make([][]pt2, 0, len(loop2d)-1)
	for i := range loop2d {
		if i != outerI {
			holes = append(holes, loop2d[i])
		}
	}

	// Bbox diagonal sets the polylabel precision.
	minX, minY := outer[0].X, outer[0].Y
	maxX, maxY := outer[0].X, outer[0].Y
	for _, p := range outer {
		if p.X < minX {
			minX = p.X
		}
		if p.X > maxX {
			maxX = p.X
		}
		if p.Y < minY {
			minY = p.Y
		}
		if p.Y > maxY {
			maxY = p.Y
		}
	}
	bboxDiag2 := math.Hypot(maxX-minX, maxY-minY)
	precision := bboxDiag2 / 1000
	if precision <= 0 {
		return nil
	}

	D := settings.DiamMM

	// Reject early if even one connector won't fit with 2× diameter
	// boundary margin.
	_, R := poleOfInaccessibility(outer, holes, precision)
	if R < 2*D {
		return nil
	}

	// Auto-count heuristic on the inscribed-circle radius.
	//
	// PHASE 2 LIMITATION: multi-connector triangulation hits a
	// bridge-spike edge case in earClip when two connectors land at
	// nearly equal y-values, which the polylabel-with-exclusion
	// strategy frequently produces. Rather than ship broken multi-
	// connector geometry, we cap auto-count at 1 and clamp explicit
	// Count to 1 too; multi-connector support is a phase 2.5
	// follow-up that needs a more robust earClip port (see
	// docs/SPLIT.md "Phase 2 follow-ups").
	count := settings.Count
	if count == 0 {
		switch {
		case R < 4*D:
			count = 1
		case R > 12*D:
			count = 3
		default:
			count = 2
		}
	}
	if count > 3 {
		count = 3
	}
	if count > 1 {
		count = 1
	}

	// Place iteratively. Each placed connector adds an exclusion
	// "hole" of radius 2×D so the next polylabel call avoids it.
	excluded := append([][]pt2(nil), holes...)
	var positions []pt2
	for i := 0; i < count; i++ {
		pole, dist := poleOfInaccessibility(outer, excluded, precision)
		if dist < 2*D {
			break
		}
		positions = append(positions, pole)
		excluded = append(excluded, makeCircle2D(pole, 2*D, connectorSegments))
	}

	// Convert each 2D position back to a 3D point on the cut plane.
	out := make([]connectorPlacement, 0, len(positions))
	for _, p := range positions {
		pos3D := [3]float64{
			plane.D*plane.Normal[0] + p.X*u[0] + p.Y*v[0],
			plane.D*plane.Normal[1] + p.X*u[1] + p.Y*v[1],
			plane.D*plane.Normal[2] + p.X*u[2] + p.Y*v[2],
		}
		var radii [2]float64
		var hasBody [2]bool
		switch settings.Style {
		case Pegs:
			// Half 0 = male (solid peg), half 1 = female (pocket).
			radii = [2]float64{D / 2, D/2 + settings.ClearanceMM}
			hasBody = [2]bool{true, true}
		case Dowels:
			// Both halves get a clearance-sized pocket. Bodies are
			// emitted on both sides.
			r := D/2 + settings.ClearanceMM
			radii = [2]float64{r, r}
			hasBody = [2]bool{true, true}
		}
		out = append(out, connectorPlacement{
			Pos3D:     pos3D,
			Radius:    radii,
			HasBody:   hasBody,
			BodyDepth: settings.DepthMM,
		})
	}
	return out
}

// makeCircle2D returns a CCW polygonal circle with n segments around
// center, in 2D.
func makeCircle2D(center pt2, radius float64, n int) []pt2 {
	out := make([]pt2, n)
	for i := 0; i < n; i++ {
		theta := 2 * math.Pi * float64(i) / float64(n)
		out[i] = pt2{
			X: center.X + radius*math.Cos(theta),
			Y: center.Y + radius*math.Sin(theta),
		}
	}
	return out
}

// addConnectorHoles allocates 16-vertex cap-plane circles in each half
// for every placement, appends those loops into loops[h], and stores
// the circle vertex indices on the placement (LoopVerts) for later
// body-geometry generation.
//
// Each half uses its own cap basis so the circle is naturally CCW in
// 2D. cap.go reverses any non-outer CCW loop to CW for the
// polygon-with-holes triangulator, so the resulting triangulation
// leaves the connector circles as holes.
func (b *cutBuilder) addConnectorHoles(loops *[2][][]uint32, plane Plane, placements []connectorPlacement) {
	for h := 0; h < 2; h++ {
		var capNormal [3]float64
		if h == 0 {
			capNormal = plane.Normal
		} else {
			capNormal = [3]float64{-plane.Normal[0], -plane.Normal[1], -plane.Normal[2]}
		}
		u, v := planeBasis(capNormal)
		for pi := range placements {
			r := placements[pi].Radius[h]
			if r <= 0 {
				continue
			}
			verts := make([]uint32, connectorSegments)
			for i := 0; i < connectorSegments; i++ {
				theta := 2 * math.Pi * float64(i) / float64(connectorSegments)
				dx := r * math.Cos(theta)
				dy := r * math.Sin(theta)
				pos := [3]float32{
					float32(placements[pi].Pos3D[0] + dx*u[0] + dy*v[0]),
					float32(placements[pi].Pos3D[1] + dx*u[1] + dy*v[1]),
					float32(placements[pi].Pos3D[2] + dx*u[2] + dy*v[2]),
				}
				verts[i] = b.appendCapVertex(h, pos)
			}
			placements[pi].LoopVerts[h] = verts
			(*loops)[h] = append((*loops)[h], verts)
		}
	}
}

// addConnectorBodies emits cylinder/pocket geometry that closes the
// connector hole in each half's cap. The hole vertices (LoopVerts) are
// the "bottom ring" at the cap; we add a "top ring" offset along the
// cap normal by depth, plus wall and floor faces.
//
// Watertight contract: after this call, every edge in each half has
// exactly two incident faces. The connector circle vertices are shared
// between the cap (on the polygon-with-holes side, normal = cap
// outward) and the wall (interior of cylinder, normal = -cap outward
// for pegs, +cap outward for pockets).
func (b *cutBuilder) addConnectorBodies(plane Plane, placements []connectorPlacement, settings ConnectorSettings) {
	for h := 0; h < 2; h++ {
		var capNormal [3]float64
		if h == 0 {
			capNormal = plane.Normal
		} else {
			capNormal = [3]float64{-plane.Normal[0], -plane.Normal[1], -plane.Normal[2]}
		}
		// Determine whether this half's body is a SOLID PEG or a
		// HOLLOW POCKET. By convention half 0 = male (solid peg) when
		// Style==Pegs; everywhere else (Dowels, half 1 in Pegs) is a
		// pocket.
		isPeg := settings.Style == Pegs && h == 0
		// Body offset along cap normal:
		//   - Peg: extends OUT of the half's solid (+cap_normal).
		//   - Pocket: extends INTO the half's solid (-cap_normal).
		var offsetSign float64
		if isPeg {
			offsetSign = +1
		} else {
			offsetSign = -1
		}

		for pi := range placements {
			if !placements[pi].HasBody[h] {
				continue
			}
			bottom := placements[pi].LoopVerts[h]
			if len(bottom) == 0 {
				continue
			}
			depth := placements[pi].BodyDepth
			if depth <= 0 {
				continue
			}

			// Build the top ring (offset by depth along capNormal in
			// the appropriate direction).
			top := make([]uint32, len(bottom))
			off := [3]float64{
				offsetSign * depth * capNormal[0],
				offsetSign * depth * capNormal[1],
				offsetSign * depth * capNormal[2],
			}
			for i, vi := range bottom {
				p := b.halves[h].Vertices[vi]
				top[i] = b.appendCapVertex(h, [3]float32{
					p[0] + float32(off[0]),
					p[1] + float32(off[1]),
					p[2] + float32(off[2]),
				})
			}

			// Wall faces. The same triangulation works for both peg
			// and pocket: the offsetSign on the top-ring direction
			// flips the normal. For a peg (top in +cap-normal
			// direction), the resulting triangle normal points
			// +radial (out of peg solid). For a pocket (top in
			// -cap-normal direction), it points -radial (into the
			// empty pocket = out of the surrounding solid).
			n := len(bottom)
			for i := 0; i < n; i++ {
				j := (i + 1) % n
				b.appendFace(h, -1, [3]uint32{bottom[i], bottom[j], top[j]})
				b.appendFace(h, -1, [3]uint32{bottom[i], top[j], top[i]})
			}

			// End cap fan from top[0]. The outward normal for both
			// peg-top and pocket-floor points away from the half's
			// solid material — i.e. away from the cap, in the
			// +offsetSign × capNormal direction (+capNormal for a
			// peg, -capNormal for a pocket).
			//
			// In each case, the top-ring vertices were generated by
			// translating bottom-ring vertices along that same
			// direction, so their theta order seen from "outward"
			// matches bottom's CCW order in the cap basis. Fan from
			// top[0] in (top[0], top[i], top[i+1]) order produces a
			// CCW loop in the basis matching the outward normal.
			for i := 1; i < n-1; i++ {
				b.appendFace(h, -1, [3]uint32{top[0], top[i], top[i+1]})
			}
		}
	}
}

// appendCapVertex adds a new vertex at the given position to halves[h]
// with conforming zero entries in every parallel array. Used for both
// connector-circle vertices and connector-body (top/bottom-ring)
// vertices.
func (b *cutBuilder) appendCapVertex(h int, pos [3]float32) uint32 {
	half := b.halves[h]
	idx := uint32(len(half.Vertices))
	half.Vertices = append(half.Vertices, pos)
	if half.UVs != nil {
		half.UVs = append(half.UVs, [2]float32{})
	}
	if half.VertexColors != nil {
		half.VertexColors = append(half.VertexColors, [4]uint8{})
	}
	return idx
}
