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

	// Check tetrahedra first — if point is inside one, distance is 0.
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

	best := math.MaxFloat64

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

	// Check all edges.
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			d := distToSegment(p, verts[i], verts[j])
			if d < best {
				best = d
			}
		}
	}

	// Check all vertices.
	for i := 0; i < n; i++ {
		d := dist3(p, verts[i])
		if d < best {
			best = d
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

// closestSegmentBary is distToSegment that also returns the barycentric weights
// (wa, wb), wa+wb == 1, of the closest point along a-b.
func closestSegmentBary(p, a, b [3]float64) (float64, [2]float64) {
	ab := sub3(b, a)
	ap := sub3(p, a)
	denom := dot3f(ab, ab)
	var t float64
	if denom > 0 {
		t = dot3f(ap, ab) / denom
	}
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	closest := [3]float64{a[0] + t*ab[0], a[1] + t*ab[1], a[2] + t*ab[2]}
	return dist3(p, closest), [2]float64{1 - t, t}
}

// closestTriangleBary is distToTriangle that also returns the barycentric
// weights (wa, wb, wc), summing to 1, of the closest point on triangle (a,b,c).
// When the projection falls outside the triangle (or the triangle is
// degenerate) the closest point lies on an edge, so the opposite vertex's weight
// is 0. Edge ties are broken toward the first edge (a-b, then a-c, then b-c) for
// determinism, matching distToTriangle's min-over-edges order.
func closestTriangleBary(p, a, b, c [3]float64) (float64, [3]float64) {
	ab := sub3(b, a)
	ac := sub3(c, a)
	ap := sub3(p, a)

	d00 := dot3f(ab, ab)
	d01 := dot3f(ab, ac)
	d11 := dot3f(ac, ac)
	d20 := dot3f(ap, ab)
	d21 := dot3f(ap, ac)

	denom := d00*d11 - d01*d01
	if math.Abs(denom) >= 1e-12 {
		u := (d11*d20 - d01*d21) / denom
		v := (d00*d21 - d01*d20) / denom
		if u >= 0 && v >= 0 && u+v <= 1 {
			proj := [3]float64{
				a[0] + u*ab[0] + v*ac[0],
				a[1] + u*ab[1] + v*ac[1],
				a[2] + u*ab[2] + v*ac[2],
			}
			return dist3(p, proj), [3]float64{1 - u - v, u, v}
		}
	}

	// Outside the triangle (or degenerate): closest point is on an edge.
	d1, w1 := closestSegmentBary(p, a, b) // a-b
	d2, w2 := closestSegmentBary(p, a, c) // a-c
	d3, w3 := closestSegmentBary(p, b, c) // b-c
	best, bary := d1, [3]float64{w1[0], w1[1], 0}
	if d2 < best {
		best, bary = d2, [3]float64{w2[0], 0, w2[1]}
	}
	if d3 < best {
		best, bary = d3, [3]float64{0, w3[0], w3[1]}
	}
	return best, bary
}

// tetraBarycentric returns the barycentric weights of an interior point p with
// respect to tetrahedron (a,b,c,d) via signed volumes. Weights sum to 1; for a
// point known to be inside they are all ≥ 0. A degenerate (near-zero volume)
// tetrahedron falls back to uniform weights.
func tetraBarycentric(p, a, b, c, d [3]float64) [4]float64 {
	va := signedVolume(p, b, c, d)
	vb := signedVolume(a, p, c, d)
	vc := signedVolume(a, b, p, d)
	vd := signedVolume(a, b, c, p)
	total := va + vb + vc + vd
	if math.Abs(total) < 1e-12 {
		return [4]float64{0.25, 0.25, 0.25, 0.25}
	}
	return [4]float64{va / total, vb / total, vc / total, vd / total}
}

// closestHullFeature returns the distance from p to the convex hull of verts,
// plus the indices of the supporting sub-simplex of the closest hull point and
// that point's barycentric weights over those vertices (weights ≥ 0, sum 1). It
// mirrors distToConvexHull's feature set and iteration order exactly — the first
// containing tetrahedron ⇒ interior (distance 0), otherwise the nearest
// triangle, edge, or vertex — so the returned distance is identical to
// distToConvexHull. The extra attribution lets callers charge a cost to the
// vertices actually responsible for the coverage (see tdSelectState.score).
// Ties resolve toward the lexicographically-first feature, so the result is
// deterministic regardless of scheduling.
func closestHullFeature(p [3]float64, verts [][3]float64) (float64, []int, []float64) {
	n := len(verts)
	if n == 0 {
		return math.MaxFloat64, nil, nil
	}

	// Interior: first containing tetrahedron (lexicographic order) ⇒ distance 0.
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			for k := j + 1; k < n; k++ {
				for l := k + 1; l < n; l++ {
					if pointInTetrahedron(p, verts[i], verts[j], verts[k], verts[l]) {
						w := tetraBarycentric(p, verts[i], verts[j], verts[k], verts[l])
						return 0, []int{i, j, k, l}, []float64{w[0], w[1], w[2], w[3]}
					}
				}
			}
		}
	}

	best := math.MaxFloat64
	var bestIdx []int
	var bestBary []float64
	consider := func(d float64, idx []int, bary []float64) {
		if d < best {
			best = d
			bestIdx = idx
			bestBary = bary
		}
	}

	// Triangles, then edges, then vertices — same order as distToConvexHull.
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			for k := j + 1; k < n; k++ {
				d, w := closestTriangleBary(p, verts[i], verts[j], verts[k])
				consider(d, []int{i, j, k}, []float64{w[0], w[1], w[2]})
			}
		}
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			d, w := closestSegmentBary(p, verts[i], verts[j])
			consider(d, []int{i, j}, []float64{w[0], w[1]})
		}
	}
	for i := 0; i < n; i++ {
		consider(dist3(p, verts[i]), []int{i}, []float64{1})
	}
	return best, bestIdx, bestBary
}
