// Vertical (or near-vertical) source triangles can't take the
// Clipper 2D cap path: their XY-projected area is zero (or so small
// that the plane-equation Z-lift's 1/nz blows up), and Clipper's
// 2D intersection on a degenerate XY input either drops the piece
// or produces a sliver. Without a parallel path, flat-walled models
// (e.g. a cube) lose every side face — only top and bottom caps
// reach the output mesh.
//
// This file provides that parallel path: clip the vertical sub-
// polygon against the cell's outer polygon (extruded vertically as
// a prism) in 3D, keeping the polygon planar in the source triangle's
// plane. The slab Z-clip has already run, so we only clip against
// the cell's vertical walls.

package cellslicer

import (
	"math"
	"sort"
)

// clipVerticalPolyToCell clips a vertical sub-polygon against the
// cell's outer polygon (extruded vertically). Returns one piece per
// disjoint interval where the wall line lies inside cell.Outer
// (typically one interval; multiple appear when the wall grazes a
// non-convex cell's concavity).
//
// Why a scan-based approach: Sutherland-Hodgman against every
// cell.Outer edge is only correct when cell.Outer is convex.
// Polyomino cells routinely kink concavely, and the L-shape corner
// cell at the cube's (X=0, Y=-20) wedge wrongly truncated the wall
// (found 2026-05-17 when snapmaker orca refused the first layer).
// Triangulating cell.Outer and clipping per triangle worked but
// introduced *bridge* vertices on the wall plane that the bottom
// cap's separate cell clip didn't have — creating fresh T-junctions
// between cap and wall at Y=-20, Z=0.
//
// The scan approach treats the wall plane as a line in XY, walks the
// cell.Outer boundary, and pairs the crossings into u-intervals where
// the line is inside the cell. Each interval becomes one wall-strip
// rectangle, clipped against the wall polygon via Sutherland-Hodgman
// against the strip's two parallel sides (those are convex by
// construction). The crossings are the EXACT cell.Outer/wall-line
// intersections — the same points the cap clip computes — so wall
// and cap share matching vertices on their seam.
func clipVerticalPolyToCell(poly [][3]float32, cellOuter []Point2) [][][3]float32 {
	if len(poly) < 3 || len(cellOuter) < 3 {
		return nil
	}
	// Wall plane normal in XY: pick two XY-distinct wall vertices and
	// build the perpendicular. The wall is vertical so its plane has
	// a well-defined XY normal (the source-tri's XY normal direction).
	nx, ny, ok := wallXYNormal(poly)
	if !ok {
		return nil
	}
	// d: signed offset such that nx*x + ny*y == d for points on the wall.
	d := nx*poly[0][0] + ny*poly[0][1]
	// Tangent direction (along the wall in XY).
	ux, uy := -ny, nx
	intervals := cellOuterScanIntervals(cellOuter, nx, ny, d, ux, uy)
	if len(intervals) == 0 {
		return nil
	}
	pieces := make([][][3]float32, 0, len(intervals))
	for _, iv := range intervals {
		// Clip wall polygon to u∈[iv[0], iv[1]] via two half-spaces.
		clipped := poly
		// Keep u >= iv[0]: -(ux*x + uy*y) <= -iv[0]
		clipped = clipPolyByPlaneXY(clipped, -ux, -uy, -iv[0])
		if len(clipped) < 3 {
			continue
		}
		// Keep u <= iv[1]: ux*x + uy*y <= iv[1]
		clipped = clipPolyByPlaneXY(clipped, ux, uy, iv[1])
		if len(clipped) >= 3 {
			pieces = append(pieces, clipped)
		}
	}
	return pieces
}

// wallXYNormal computes a unit-length XY normal for a vertical wall
// polygon. Picks the first pair of vertices with distinct XY and
// returns the rotated direction. Returns ok=false if no such pair
// exists (degenerate input).
func wallXYNormal(poly [][3]float32) (float32, float32, bool) {
	if len(poly) < 2 {
		return 0, 0, false
	}
	for i := 1; i < len(poly); i++ {
		dx := poly[i][0] - poly[0][0]
		dy := poly[i][1] - poly[0][1]
		l2 := dx*dx + dy*dy
		if l2 <= 1e-20 {
			continue
		}
		inv := float32(1) / float32(sqrt32(l2))
		dx *= inv
		dy *= inv
		// Normal perpendicular to tangent (dx, dy): rotate 90° CW.
		return dy, -dx, true
	}
	return 0, 0, false
}

func sqrt32(x float32) float32 {
	return float32(math.Sqrt(float64(x)))
}

func sortFloat32s(xs []float32) {
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
}

// cellOuterScanIntervals walks cellOuter (a simple polygon) and
// returns the sorted u-intervals where the wall line (nx*x+ny*y=d)
// lies inside the polygon. The u coordinate is u(x,y) = ux*x+uy*y;
// (ux, uy) must be perpendicular to (nx, ny).
//
// Counts each polygon boundary crossing once: proper sign-change
// crossings, plus on-line vertices that are transversal (preceding
// and following sides are opposite), plus the endpoints of any
// edge that lies entirely on the line.
func cellOuterScanIntervals(cellOuter []Point2, nx, ny, d, ux, uy float32) [][2]float32 {
	n := len(cellOuter)
	if n < 3 {
		return nil
	}
	const eps = float32(1e-6)
	sides := make([]int8, n)
	for i := 0; i < n; i++ {
		s := nx*cellOuter[i][0] + ny*cellOuter[i][1] - d
		if s > eps {
			sides[i] = 1
		} else if s < -eps {
			sides[i] = -1
		}
	}
	uOf := func(p Point2) float32 { return ux*p[0] + uy*p[1] }
	var crossings []float32
	for i := 0; i < n; i++ {
		a := cellOuter[i]
		b := cellOuter[(i+1)%n]
		sa := sides[i]
		sb := sides[(i+1)%n]
		switch {
		case sa == 0 && sb == 0:
			// Edge lies on the wall line: both endpoints are on-line
			// boundary contributions.
			crossings = append(crossings, uOf(a), uOf(b))
		case sa > 0 && sb < 0, sa < 0 && sb > 0:
			da := nx*a[0] + ny*a[1] - d
			db := nx*b[0] + ny*b[1] - d
			t := da / (da - db)
			x := a[0] + t*(b[0]-a[0])
			y := a[1] + t*(b[1]-a[1])
			crossings = append(crossings, ux*x+uy*y)
		case sa == 0 && sb != 0:
			// Vertex a on line; transversal iff preceding-vertex
			// side is opposite to sb.
			prev := sides[(i-1+n)%n]
			if prev != 0 && prev != sb {
				crossings = append(crossings, uOf(a))
			}
		}
		// sa != 0 && sb == 0: picked up on next iteration as a-on-line.
	}
	if len(crossings) < 2 {
		return nil
	}
	sortFloat32s(crossings)
	var out [][2]float32
	for i := 0; i+1 < len(crossings); i += 2 {
		if crossings[i+1]-crossings[i] > eps {
			out = append(out, [2]float32{crossings[i], crossings[i+1]})
		}
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
