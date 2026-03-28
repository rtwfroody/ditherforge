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
