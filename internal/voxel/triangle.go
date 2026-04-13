package voxel

// ClosestPointResult holds the result of a closest-point-on-triangle query.
type ClosestPointResult struct {
	DistSq float32 // squared distance from query point to closest point
	S, T   float32 // barycentric coordinates (bary = [1-S-T, S, T])
}

// ClosestPointOnTriangle computes the closest point on triangle (v0,v1,v2) to
// point p and returns the squared distance and barycentric coordinates.
// The barycentric coordinates are (1-S-T, S, T) corresponding to (v0, v1, v2).
func ClosestPointOnTriangle(p, v0, v1, v2 [3]float32) ClosestPointResult {
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

	dx := d[0] + s*e0[0] + t*e1[0]
	dy := d[1] + s*e0[1] + t*e1[1]
	dz := d[2] + s*e0[2] + t*e1[2]

	return ClosestPointResult{
		DistSq: dx*dx + dy*dy + dz*dz,
		S:      s,
		T:      t,
	}
}
