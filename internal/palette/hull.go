package palette

import "math"

// distToConvexHull returns the Euclidean distance from point p to the convex
// hull of vertices in 3D. For small vertex counts (typical palette sizes) we
// check all sub-simplices: tetrahedra, triangles, edges, and vertices.
func distToConvexHull(p [3]float64, verts [][3]float64) float64 {
	n := len(verts)
	if n == 0 {
		return math.MaxFloat64
	}

	best := math.MaxFloat64

	// Check all vertices.
	for i := 0; i < n; i++ {
		d := dist3(p, verts[i])
		if d < best {
			best = d
		}
	}

	// Check all edges.
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			d := distToSegment(p, verts[i], verts[j])
			if d < best {
				best = d
			}
		}
	}

	// Check all triangles.
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			for k := j + 1; k < n; k++ {
				d := distToTriangle(p, verts[i], verts[j], verts[k])
				if d < best {
					best = d
				}
			}
		}
	}

	// Check all tetrahedra.
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			for k := j + 1; k < n; k++ {
				for l := k + 1; l < n; l++ {
					if pointInTetrahedron(p, verts[i], verts[j], verts[k], verts[l]) {
						return 0
					}
				}
			}
		}
	}

	return best
}

func dist3(a, b [3]float64) float64 {
	d0 := a[0] - b[0]
	d1 := a[1] - b[1]
	d2 := a[2] - b[2]
	return math.Sqrt(d0*d0 + d1*d1 + d2*d2)
}

func sub3(a, b [3]float64) [3]float64 {
	return [3]float64{a[0] - b[0], a[1] - b[1], a[2] - b[2]}
}

func dot3f(a, b [3]float64) float64 {
	return a[0]*b[0] + a[1]*b[1] + a[2]*b[2]
}

func cross3f(a, b [3]float64) [3]float64 {
	return [3]float64{
		a[1]*b[2] - a[2]*b[1],
		a[2]*b[0] - a[0]*b[2],
		a[0]*b[1] - a[1]*b[0],
	}
}

// distToSegment returns the distance from p to the line segment a-b.
func distToSegment(p, a, b [3]float64) float64 {
	ab := sub3(b, a)
	ap := sub3(p, a)
	t := dot3f(ap, ab) / dot3f(ab, ab)
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	closest := [3]float64{a[0] + t*ab[0], a[1] + t*ab[1], a[2] + t*ab[2]}
	return dist3(p, closest)
}

// distToTriangle returns the distance from p to triangle (a, b, c) in 3D.
func distToTriangle(p, a, b, c [3]float64) float64 {
	ab := sub3(b, a)
	ac := sub3(c, a)
	ap := sub3(p, a)

	d00 := dot3f(ab, ab)
	d01 := dot3f(ab, ac)
	d11 := dot3f(ac, ac)
	d20 := dot3f(ap, ab)
	d21 := dot3f(ap, ac)

	denom := d00*d11 - d01*d01
	if math.Abs(denom) < 1e-12 {
		// Degenerate triangle — fall back to edge distances.
		d1 := distToSegment(p, a, b)
		d2 := distToSegment(p, a, c)
		d3 := distToSegment(p, b, c)
		return math.Min(d1, math.Min(d2, d3))
	}

	u := (d11*d20 - d01*d21) / denom
	v := (d00*d21 - d01*d20) / denom

	if u >= 0 && v >= 0 && u+v <= 1 {
		// Projection is inside triangle.
		proj := [3]float64{
			a[0] + u*ab[0] + v*ac[0],
			a[1] + u*ab[1] + v*ac[1],
			a[2] + u*ab[2] + v*ac[2],
		}
		return dist3(p, proj)
	}

	// Outside triangle — closest point is on an edge.
	d1 := distToSegment(p, a, b)
	d2 := distToSegment(p, a, c)
	d3 := distToSegment(p, b, c)
	return math.Min(d1, math.Min(d2, d3))
}

// pointInTetrahedron returns true if p is inside the tetrahedron (a, b, c, d).
func pointInTetrahedron(p, a, b, c, d [3]float64) bool {
	// Use barycentric coordinates via signed volumes.
	va := signedVolume(p, b, c, d)
	vb := signedVolume(a, p, c, d)
	vc := signedVolume(a, b, p, d)
	vd := signedVolume(a, b, c, p)

	allPos := va >= 0 && vb >= 0 && vc >= 0 && vd >= 0
	allNeg := va <= 0 && vb <= 0 && vc <= 0 && vd <= 0
	return allPos || allNeg
}

func signedVolume(a, b, c, d [3]float64) float64 {
	ab := sub3(b, a)
	ac := sub3(c, a)
	ad := sub3(d, a)
	return dot3f(ab, cross3f(ac, ad))
}
