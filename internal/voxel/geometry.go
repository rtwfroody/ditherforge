package voxel

import "math"

// PointInTriangleXY tests if (px, py) is inside the XY projection of triangle
// (v0, v1, v2) and returns barycentric coordinates.
func PointInTriangleXY(px, py float32, v0, v1, v2 [3]float32) (bool, [3]float32) {
	d00x := v1[0] - v0[0]
	d00y := v1[1] - v0[1]
	d01x := v2[0] - v0[0]
	d01y := v2[1] - v0[1]
	d02x := px - v0[0]
	d02y := py - v0[1]

	dot00 := d00x*d00x + d00y*d00y
	dot01 := d00x*d01x + d00y*d01y
	dot02 := d00x*d02x + d00y*d02y
	dot11 := d01x*d01x + d01y*d01y
	dot12 := d01x*d02x + d01y*d02y

	denom := dot00*dot11 - dot01*dot01
	if denom == 0 {
		return false, [3]float32{}
	}

	invDenom := 1.0 / denom
	u := (dot11*dot02 - dot01*dot12) * invDenom
	v := (dot00*dot12 - dot01*dot02) * invDenom

	if u >= 0 && v >= 0 && u+v <= 1 {
		return true, [3]float32{1 - u - v, u, v}
	}
	return false, [3]float32{}
}

// ClosestRegion indicates where on a triangle the closest point lies.
type ClosestRegion int

const (
	RegionInterior ClosestRegion = 0
	RegionEdge01   ClosestRegion = 1 // edge v0→v1
	RegionEdge12   ClosestRegion = 2 // edge v1→v2
	RegionEdge20   ClosestRegion = 3 // edge v2→v0
	RegionVertex0  ClosestRegion = 4
	RegionVertex1  ClosestRegion = 5
	RegionVertex2  ClosestRegion = 6
)

// ClosestPointOnTriangle3D returns the closest point on triangle (v0,v1,v2)
// to point p in 3D, and the squared distance.
func ClosestPointOnTriangle3D(p, v0, v1, v2 [3]float32) ([3]float32, float32) {
	cp, dSq, _ := ClosestPointOnTriangle3DEx(p, v0, v1, v2)
	return cp, dSq
}

// ClosestPointOnTriangle3DEx is like ClosestPointOnTriangle3D but also returns
// the region of the triangle containing the closest point.
func ClosestPointOnTriangle3DEx(p, v0, v1, v2 [3]float32) ([3]float32, float32, ClosestRegion) {
	e0 := [3]float32{v1[0] - v0[0], v1[1] - v0[1], v1[2] - v0[2]}
	e1 := [3]float32{v2[0] - v0[0], v2[1] - v0[1], v2[2] - v0[2]}
	d := [3]float32{v0[0] - p[0], v0[1] - p[1], v0[2] - p[2]}

	a := Dot3(e0, e0)
	b := Dot3(e0, e1)
	c := Dot3(e1, e1)
	dd := Dot3(e0, d)
	e := Dot3(e1, d)

	det := a*c - b*b
	s := b*e - c*dd
	t := b*dd - a*e

	if s+t <= det {
		if s < 0 {
			if t < 0 {
				if dd < 0 {
					t = 0
					s = ClampF(-dd/a, 0, 1)
				} else {
					s = 0
					t = ClampF(-e/c, 0, 1)
				}
			} else {
				s = 0
				t = ClampF(-e/c, 0, 1)
			}
		} else if t < 0 {
			t = 0
			s = ClampF(-dd/a, 0, 1)
		} else {
			invDet := 1.0 / det
			s *= invDet
			t *= invDet
		}
	} else {
		if s < 0 {
			tmp0 := b + dd
			tmp1 := c + e
			if tmp1 > tmp0 {
				numer := tmp1 - tmp0
				denom := a - 2*b + c
				s = ClampF(numer/denom, 0, 1)
				t = 1 - s
			} else {
				s = 0
				t = ClampF(-e/c, 0, 1)
			}
		} else if t < 0 {
			tmp0 := b + e
			tmp1 := a + dd
			if tmp1 > tmp0 {
				numer := tmp1 - tmp0
				denom := a - 2*b + c
				t = ClampF(numer/denom, 0, 1)
				s = 1 - t
			} else {
				t = 0
				s = ClampF(-dd/a, 0, 1)
			}
		} else {
			numer := (c + e) - (b + dd)
			if numer <= 0 {
				s = 0
			} else {
				denom := a - 2*b + c
				s = ClampF(numer/denom, 0, 1)
			}
			t = 1 - s
		}
	}

	closest := [3]float32{
		v0[0] + s*e0[0] + t*e1[0],
		v0[1] + s*e0[1] + t*e1[1],
		v0[2] + s*e0[2] + t*e1[2],
	}
	dx := p[0] - closest[0]
	dy := p[1] - closest[1]
	dz := p[2] - closest[2]

	const eps = 1e-6
	region := RegionInterior
	onV0 := s < eps && t < eps
	onV1 := s > 1-eps && t < eps
	onV2 := s < eps && t > 1-eps
	if onV0 {
		region = RegionVertex0
	} else if onV1 {
		region = RegionVertex1
	} else if onV2 {
		region = RegionVertex2
	} else if t < eps {
		region = RegionEdge01
	} else if s < eps {
		region = RegionEdge20
	} else if s+t > 1-eps {
		region = RegionEdge12
	}

	return closest, dx*dx + dy*dy + dz*dz, region
}

// Dot3 computes the dot product of two 3D vectors.
func Dot3(a, b [3]float32) float32 {
	return a[0]*b[0] + a[1]*b[1] + a[2]*b[2]
}

// Cross3f computes the cross product of two 3D vectors.
func Cross3f(a, b [3]float32) [3]float32 {
	return [3]float32{
		a[1]*b[2] - a[2]*b[1],
		a[2]*b[0] - a[0]*b[2],
		a[0]*b[1] - a[1]*b[0],
	}
}

// TriNormal returns the unit normal of a triangle.
func TriNormal(a, b, c [3]float32) [3]float32 {
	ab := [3]float32{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
	ac := [3]float32{c[0] - a[0], c[1] - a[1], c[2] - a[2]}
	n := [3]float32{
		ab[1]*ac[2] - ab[2]*ac[1],
		ab[2]*ac[0] - ab[0]*ac[2],
		ab[0]*ac[1] - ab[1]*ac[0],
	}
	l := float32(math.Sqrt(float64(n[0]*n[0] + n[1]*n[1] + n[2]*n[2])))
	if l < 1e-12 {
		return [3]float32{}
	}
	return [3]float32{n[0] / l, n[1] / l, n[2] / l}
}

// TriangleAABBOverlap tests whether a triangle (v0, v1, v2) overlaps an
// axis-aligned bounding box defined by its center and half-extents.
// Uses the Separating Axis Theorem (Akenine-Möller method).
func TriangleAABBOverlap(v0, v1, v2, center, halfExtent [3]float32) bool {
	// Translate triangle so box center is at origin.
	t0 := [3]float32{v0[0] - center[0], v0[1] - center[1], v0[2] - center[2]}
	t1 := [3]float32{v1[0] - center[0], v1[1] - center[1], v1[2] - center[2]}
	t2 := [3]float32{v2[0] - center[0], v2[1] - center[1], v2[2] - center[2]}

	// Triangle edges.
	e0 := [3]float32{t1[0] - t0[0], t1[1] - t0[1], t1[2] - t0[2]}
	e1 := [3]float32{t2[0] - t1[0], t2[1] - t1[1], t2[2] - t1[2]}
	e2 := [3]float32{t0[0] - t2[0], t0[1] - t2[1], t0[2] - t2[2]}

	// Test 9 cross-product axes (3 edges × 3 box face normals).
	// Each axis is edgeN × (1,0,0), edgeN × (0,1,0), edgeN × (0,0,1).
	edges := [3][3]float32{e0, e1, e2}
	for _, e := range edges {
		for a := 0; a < 3; a++ {
			// axis = e × unitAxis[a]
			// For a=0 (X): axis = (0, -e[2], e[1])
			// For a=1 (Y): axis = (e[2], 0, -e[0])
			// For a=2 (Z): axis = (-e[1], e[0], 0)
			b := (a + 1) % 3
			c := (a + 2) % 3
			// Project triangle vertices onto axis.
			p0 := -e[c]*t0[b] + e[b]*t0[c]
			p1 := -e[c]*t1[b] + e[b]*t1[c]
			p2 := -e[c]*t2[b] + e[b]*t2[c]
			minP := p0
			maxP := p0
			if p1 < minP {
				minP = p1
			}
			if p1 > maxP {
				maxP = p1
			}
			if p2 < minP {
				minP = p2
			}
			if p2 > maxP {
				maxP = p2
			}
			// Project box onto axis.
			abs_ec := e[c]
			if abs_ec < 0 {
				abs_ec = -abs_ec
			}
			abs_eb := e[b]
			if abs_eb < 0 {
				abs_eb = -abs_eb
			}
			r := halfExtent[b]*abs_ec + halfExtent[c]*abs_eb
			if minP > r || maxP < -r {
				return false
			}
		}
	}

	// Test 3 box face normals (AABB axes).
	// Use half-open interval [−half, +half) so a triangle exactly on the
	// boundary between two cells is counted in exactly one of them.
	for a := 0; a < 3; a++ {
		minT := t0[a]
		maxT := t0[a]
		if t1[a] < minT {
			minT = t1[a]
		}
		if t1[a] > maxT {
			maxT = t1[a]
		}
		if t2[a] < minT {
			minT = t2[a]
		}
		if t2[a] > maxT {
			maxT = t2[a]
		}
		if minT >= halfExtent[a] || maxT < -halfExtent[a] {
			return false
		}
	}

	// Test triangle normal.
	n := Cross3f(e0, e1)
	d := Dot3(n, t0)
	abs_nx := n[0]
	if abs_nx < 0 {
		abs_nx = -abs_nx
	}
	abs_ny := n[1]
	if abs_ny < 0 {
		abs_ny = -abs_ny
	}
	abs_nz := n[2]
	if abs_nz < 0 {
		abs_nz = -abs_nz
	}
	r := halfExtent[0]*abs_nx + halfExtent[1]*abs_ny + halfExtent[2]*abs_nz
	if d > r || d < -r {
		return false
	}

	return true
}

// ComputeBounds returns the min and max corners of a point set.
func ComputeBounds(verts [][3]float32) ([3]float32, [3]float32) {
	minV := verts[0]
	maxV := verts[0]
	for _, v := range verts[1:] {
		for i := 0; i < 3; i++ {
			if v[i] < minV[i] {
				minV[i] = v[i]
			}
			if v[i] > maxV[i] {
				maxV[i] = v[i]
			}
		}
	}
	return minV, maxV
}

// ClampF clamps v to [lo, hi].
func ClampF(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Minf returns the minimum of two float32s.
func Minf(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

// Maxf returns the maximum of two float32s.
func Maxf(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
