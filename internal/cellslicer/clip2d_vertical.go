// Vertical (or near-vertical) source triangles can't be lifted via XY
// barycentric interpolation — their XY-projected area is zero (or so
// small that 1/area blows up), so the lifted Z would be NaN or wild.
// The standard 2D clip path drops them, which makes flat-walled
// models (e.g. a cube) lose every side face: only the top and bottom
// caps reach the output mesh.
//
// This file provides a parallel path that handles those triangles
// with a 3D Sutherland-Hodgman clip against the cell prism (cell.Outer
// extruded vertically, capped at [zBot, zTop]). The slab Z-clip has
// already run, so we only clip against the cell's outer edges and
// the polygon stays planar in the source triangle's plane.

package cellslicer

// clipCellVerticals clips each vertical sub-polygon against the
// cell's prism and fan-triangulates the surviving polygon. Output
// triangle winding is chosen so each triangle's 3D normal has a
// non-negative dot product with the source triangle's stored
// Normal — i.e. the wall faces the same way as the original mesh
// surface it came from. Without that step, back-face culling in
// downstream viewers (Three.js GUI) hides half of every wall.
//
// Returns (verts, faces) suitable for concatenation onto a cell's
// 2D-clip output (the caller offsets face indices appropriately).
func clipCellVerticals(verticals []slabVerticalPoly, cellOuter []Point2) ([][3]float32, [][3]uint32) {
	var verts [][3]float32
	var faces [][3]uint32
	for _, vp := range verticals {
		clipped := clipVerticalPolyToCell(vp.Pts, cellOuter)
		if len(clipped) < 3 {
			continue
		}
		base := uint32(len(verts))
		verts = append(verts, clipped...)
		for i := 1; i < len(clipped)-1; i++ {
			triN := triangleNormal(clipped[0], clipped[i], clipped[i+1])
			dot := triN[0]*vp.Normal[0] + triN[1]*vp.Normal[1] + triN[2]*vp.Normal[2]
			if dot >= 0 {
				faces = append(faces, [3]uint32{base, base + uint32(i), base + uint32(i+1)})
			} else {
				faces = append(faces, [3]uint32{base, base + uint32(i+1), base + uint32(i)})
			}
		}
	}
	return verts, faces
}

// slabVerticalPoly is a sub-polygon of a vertical (or near-vertical)
// source triangle whose XY projection has near-zero area. The
// vertices are stored in mesh coords, in the source triangle's plane,
// and already Z-clipped to the owning slab's [zBot, zTop] range.
//
// Normal is the source triangle's facing direction (cross of edges,
// preserved so the output fan-triangulation can pick a winding that
// matches the source). It is not unit-normalized — only its
// direction is used downstream.
type slabVerticalPoly struct {
	Pts    [][3]float32
	Normal [3]float32
}

// clipVerticalPolyToCell clips a vertical sub-polygon against the
// cell's outer polygon (extruded vertically). Returns the clipped
// polygon (still planar in the source triangle's plane) or nil if
// nothing survives.
//
// cellOuter winding can be CCW or CW; the function detects via signed
// area so the per-edge inward normal points consistently into the
// cell interior.
func clipVerticalPolyToCell(poly [][3]float32, cellOuter []Point2) [][3]float32 {
	if len(poly) < 3 || len(cellOuter) < 3 {
		return nil
	}
	// Detect cell winding via signed area.
	var twiceSignedArea float32
	for i := 0; i < len(cellOuter); i++ {
		j := (i + 1) % len(cellOuter)
		twiceSignedArea += cellOuter[i][0]*cellOuter[j][1] - cellOuter[j][0]*cellOuter[i][1]
	}
	sign := float32(1)
	if twiceSignedArea < 0 {
		sign = -1
	}
	out := poly
	n := len(cellOuter)
	for i := 0; i < n && len(out) >= 3; i++ {
		xa, ya := cellOuter[i][0], cellOuter[i][1]
		xb, yb := cellOuter[(i+1)%n][0], cellOuter[(i+1)%n][1]
		// Outward normal w.r.t. cell interior. For CCW winding,
		// (yb-ya, -(xb-xa)) points outward; for CW, flip via sign.
		nx := sign * (yb - ya)
		ny := sign * -(xb - xa)
		d := nx*xa + ny*ya
		out = clipPolyByPlaneXY(out, nx, ny, d)
	}
	if len(out) < 3 {
		return nil
	}
	return out
}

// clipPolyByPlaneXY clips a 3D polygon by the half-space
// nx*x + ny*y <= d (a vertical plane — Z is irrelevant). Standard
// Sutherland-Hodgman: each edge that straddles the plane is split at
// the intersection point, with Z linearly interpolated by t along the
// XY edge.
//
// Points exactly on the plane are treated as inside (<= d), so a
// polygon flush with a cell edge survives clipping rather than
// vanishing to floating-point noise.
func clipPolyByPlaneXY(poly [][3]float32, nx, ny, d float32) [][3]float32 {
	if len(poly) == 0 {
		return nil
	}
	out := make([][3]float32, 0, len(poly)+2)
	n := len(poly)
	for i := 0; i < n; i++ {
		s := poly[(i-1+n)%n]
		e := poly[i]
		sVal := nx*s[0] + ny*s[1]
		eVal := nx*e[0] + ny*e[1]
		sIn := sVal <= d
		eIn := eVal <= d
		if eIn {
			if !sIn {
				out = append(out, lerpAtPlaneXY(s, e, sVal, eVal, d))
			}
			out = append(out, e)
		} else if sIn {
			out = append(out, lerpAtPlaneXY(s, e, sVal, eVal, d))
		}
	}
	return out
}

// lerpAtPlaneXY returns the point on segment s→e where the plane's
// XY-evaluation equals d. (sVal, eVal) = nx*x+ny*y at endpoints.
func lerpAtPlaneXY(s, e [3]float32, sVal, eVal, d float32) [3]float32 {
	denom := eVal - sVal
	if absf(denom) < 1e-12 {
		return s
	}
	t := (d - sVal) / denom
	return [3]float32{
		s[0] + t*(e[0]-s[0]),
		s[1] + t*(e[1]-s[1]),
		s[2] + t*(e[2]-s[2]),
	}
}

// triangleNormal returns the (un-normalized) normal of triangle abc,
// i.e. (b-a) × (c-a). Used to remember a vertical source triangle's
// facing direction so its clipped fan output can be wound to match.
func triangleNormal(a, b, c [3]float32) [3]float32 {
	bx, by, bz := b[0]-a[0], b[1]-a[1], b[2]-a[2]
	cx, cy, cz := c[0]-a[0], c[1]-a[1], c[2]-a[2]
	return [3]float32{
		by*cz - bz*cy,
		bz*cx - bx*cz,
		bx*cy - by*cx,
	}
}

// polygonXYSignedArea returns 2× the signed XY area of poly.
func polygonXYSignedArea(poly [][3]float32) float32 {
	if len(poly) < 3 {
		return 0
	}
	var s float32
	n := len(poly)
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		s += poly[i][0]*poly[j][1] - poly[j][0]*poly[i][1]
	}
	return s
}
