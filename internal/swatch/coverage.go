package swatch

import "math"

// MeasureCoverage computes the realized fraction of filament B in each section
// of each plate, separately for the front and back faces, from a run's output
// mesh (verts in pipeline-mm Z-up, faces, and per-face palette-index
// assignments as returned by pipeline.OutputFaceAssignments).
//
// Method: a face is a front face when its outward normal points along -Y and a
// back face when it points along +Y (rim and interior-wall faces, whose
// normals lie in-plane, are ignored). Each such face is matched to a plate by
// its centroid Y, then its (X,Z) projection is clipped to each section's
// 10x10mm box — inset by ~1.5 block widths to exclude the boundary strip where
// voxel cells straddle section lines — and the clipped area is accumulated as
// total vs. filament-B area. This area-exact attribution is immune to the
// coplanar-merge coalescence that makes per-face-centroid binning unreliable.
//
// Returns front[plate][section] and back[plate][section]; a section with no
// measured area reports 0.
func MeasureCoverage(plan Plan, verts [][3]float32, faces [][3]uint32, assignments []int32) (front, back [][]float64) {
	type acc struct{ b, tot float64 }
	fAcc := make([][]acc, len(plan.Plates))
	bAcc := make([][]acc, len(plan.Plates))
	for p := range fAcc {
		fAcc[p] = make([]acc, Sections)
		bAcc[p] = make([]acc, Sections)
	}

	// Horizontal inset ~1.5 cell widths clears cells straddling a section's
	// left/right boundary. Vertical inset spans a few slab rows so the top and
	// bottom pattern rows don't leak from the rim; size it to clear the taller
	// first-layer row plus a couple upper rows. Rows are much thinner than a
	// cell width, so the two insets differ.
	insetX := 1.5 * plan.BlockWidthMM
	insetZ := 3 * plan.UpperZMM
	if floor := plan.Layer0ZMM + 2*plan.UpperZMM; floor > insetZ {
		insetZ = floor
	}

	for fi, f := range faces {
		if int(f[0]) >= len(verts) || int(f[1]) >= len(verts) || int(f[2]) >= len(verts) {
			continue
		}
		v0, v1, v2 := verts[f[0]], verts[f[1]], verts[f[2]]
		e1 := [3]float64{float64(v1[0] - v0[0]), float64(v1[1] - v0[1]), float64(v1[2] - v0[2])}
		e2 := [3]float64{float64(v2[0] - v0[0]), float64(v2[1] - v0[1]), float64(v2[2] - v0[2])}
		nx := e1[1]*e2[2] - e1[2]*e2[1]
		ny := e1[2]*e2[0] - e1[0]*e2[2]
		nz := e1[0]*e2[1] - e1[1]*e2[0]
		mag := math.Sqrt(nx*nx + ny*ny + nz*nz)
		if mag == 0 {
			continue
		}
		nyU := ny / mag
		isFront := nyU < -0.7
		isBack := nyU > 0.7
		if !isFront && !isBack {
			continue // rim or interior wall
		}
		cy := float64(v0[1]+v1[1]+v2[1]) / 3

		// Match to a plate by the face's Y plane.
		plateIdx := -1
		for p := range plan.Plates {
			target := plan.Plates[p].YOffsetMM
			if isBack {
				target += ThickMM
			}
			if math.Abs(cy-target) <= 1.0 {
				plateIdx = p
				break
			}
		}
		if plateIdx < 0 {
			continue
		}
		plate := plan.Plates[plateIdx]
		isB := fi < len(assignments) && int(assignments[fi]) == plate.B

		tri := [3][2]float64{
			{float64(v0[0]), float64(v0[2])},
			{float64(v1[0]), float64(v1[2])},
			{float64(v2[0]), float64(v2[2])},
		}
		dst := fAcc
		if isBack {
			dst = bAcc
		}
		// Single row of Sections columns (left -> right). Clip to each section's
		// 10x10 box, inset horizontally by insetX and vertically by insetZ to
		// skip the boundary strips where voxel cells straddle section/rim lines.
		for sec := 0; sec < Sections; sec++ {
			a := clipTriToRect(tri,
				float64(sec)*SectionMM+insetX, float64(sec+1)*SectionMM-insetX,
				insetZ, PlateHeightMM-insetZ)
			if a <= 0 {
				continue
			}
			dst[plateIdx][sec].tot += a
			if isB {
				dst[plateIdx][sec].b += a
			}
		}
	}

	toCov := func(a [][]acc) [][]float64 {
		out := make([][]float64, len(a))
		for p := range a {
			out[p] = make([]float64, Sections)
			for s := range a[p] {
				if a[p][s].tot > 0 {
					out[p][s] = a[p][s].b / a[p][s].tot
				}
			}
		}
		return out
	}
	return toCov(fAcc), toCov(bAcc)
}

// clipTriToRect returns the area of triangle tri (2D) clipped to the
// axis-aligned rectangle [xlo,xhi]x[zlo,zhi], via Sutherland-Hodgman. Returns 0
// when the rectangle is degenerate (xlo>=xhi or zlo>=zhi) or there is no
// overlap.
func clipTriToRect(tri [3][2]float64, xlo, xhi, zlo, zhi float64) float64 {
	if xlo >= xhi || zlo >= zhi {
		return 0
	}
	poly := [][2]float64{tri[0], tri[1], tri[2]}
	poly = clipHalfPlane(poly, 0, xlo, true)
	poly = clipHalfPlane(poly, 0, xhi, false)
	poly = clipHalfPlane(poly, 1, zlo, true)
	poly = clipHalfPlane(poly, 1, zhi, false)
	return math.Abs(shoelace(poly))
}

// clipHalfPlane clips a convex polygon against a single axis-aligned half-plane.
// When keepGE is true it keeps points with coord[axis] >= bound, otherwise
// coord[axis] <= bound.
func clipHalfPlane(poly [][2]float64, axis int, bound float64, keepGE bool) [][2]float64 {
	if len(poly) == 0 {
		return poly
	}
	inside := func(p [2]float64) bool {
		if keepGE {
			return p[axis] >= bound
		}
		return p[axis] <= bound
	}
	var out [][2]float64
	for i := range poly {
		cur := poly[i]
		prev := poly[(i+len(poly)-1)%len(poly)]
		curIn := inside(cur)
		prevIn := inside(prev)
		if curIn {
			if !prevIn {
				out = append(out, edgeCross(prev, cur, axis, bound))
			}
			out = append(out, cur)
		} else if prevIn {
			out = append(out, edgeCross(prev, cur, axis, bound))
		}
	}
	return out
}

func edgeCross(a, b [2]float64, axis int, bound float64) [2]float64 {
	d := b[axis] - a[axis]
	if d == 0 {
		return a
	}
	t := (bound - a[axis]) / d
	return [2]float64{a[0] + t*(b[0]-a[0]), a[1] + t*(b[1]-a[1])}
}

func shoelace(p [][2]float64) float64 {
	if len(p) < 3 {
		return 0
	}
	s := 0.0
	for i := range p {
		j := (i + 1) % len(p)
		s += p[i][0]*p[j][1] - p[j][0]*p[i][1]
	}
	return 0.5 * s
}
