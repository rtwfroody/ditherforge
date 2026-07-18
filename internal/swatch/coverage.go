package swatch

import (
	"math"

	colorful "github.com/lucasb-eyer/go-colorful"
)

// SectionCoverage holds the per-section realized measurements for one face of a
// plate: the fraction of B (the intended mixing curve) and the fraction assigned
// to a FOREIGN color — any palette entry that is neither of the plate's two.
// Foreign should be 0; a nonzero value means the pipeline let a third color
// contaminate the plate (see SnapToPairs), so it is surfaced rather than
// silently folded into the "not B" bucket.
type SectionCoverage struct {
	B       float64
	Foreign float64
}

// MeasureCoverage computes, per section of each plate and separately for the
// front and back faces, the realized fraction of filament B and the fraction
// assigned to a foreign (non-pair) color, from a run's output mesh (verts in
// pipeline-mm Z-up, faces, and per-face palette-index assignments as returned by
// pipeline.OutputFaceAssignments).
//
// Method: a face is a front face when its outward normal points along -Y and a
// back face when it points along +Y (rim and interior-wall faces, whose
// normals lie in-plane, are ignored). Each such face is matched to a plate by
// its centroid Y, then its (X,Z) projection is clipped to each section's
// 10x10mm box — inset by ~1.5 block widths to exclude the boundary strip where
// voxel cells straddle section lines — and the clipped area is accumulated as
// total, filament-B, and foreign area. This area-exact attribution is immune to
// the coplanar-merge coalescence that makes per-face-centroid binning unreliable.
//
// Returns front[plate][section] and back[plate][section]; a section with no
// measured area reports zeroes.
func MeasureCoverage(plan Plan, verts [][3]float32, faces [][3]uint32, assignments []int32) (front, back [][]SectionCoverage) {
	type acc struct{ b, foreign, tot float64 }
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
		var isB, isForeign bool
		if fi < len(assignments) {
			a := int(assignments[fi])
			isB = a == plate.B
			isForeign = a != plate.A && a != plate.B
		}

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
			if isForeign {
				dst[plateIdx][sec].foreign += a
			}
		}
	}

	toCov := func(a [][]acc) [][]SectionCoverage {
		out := make([][]SectionCoverage, len(a))
		for p := range a {
			out[p] = make([]SectionCoverage, Sections)
			for s := range a[p] {
				if a[p][s].tot > 0 {
					out[p][s] = SectionCoverage{
						B:       a[p][s].b / a[p][s].tot,
						Foreign: a[p][s].foreign / a[p][s].tot,
					}
				}
			}
		}
		return out
	}
	return toCov(fAcc), toCov(bAcc)
}

// SnapToPairs returns a copy of assignments in which every front/back/rim face
// is forced to the nearer (CIELAB ΔE) of its plate's two filaments {A,B}. A
// voxel cell straddling a block boundary can be painted a blended color that
// the palette assignment rounds to a THIRD entry (e.g. a White+Orange blend
// landing on Beige); since a physical swatch plate contains only its two
// filaments, snapping the stray to the nearer pair member removes that
// contamination. Faces already A or B are unchanged, and faces that match no
// plate are left as-is.
func SnapToPairs(plan Plan, verts [][3]float32, faces [][3]uint32, assignments []int32) []int32 {
	out := make([]int32, len(assignments))
	copy(out, assignments)

	// Precompute Lab for each palette entry.
	lab := make([]colorful.Color, len(plan.Palette))
	for i, fil := range plan.Palette {
		r, g, b := hexToUnit(fil.Hex)
		lab[i] = colorful.Color{R: r, G: g, B: b}
	}
	nearestOfPair := func(a int, pa, pb int) int {
		if a == pa || a == pb || a < 0 || a >= len(lab) {
			return a
		}
		if lab[a].DistanceLab(lab[pa]) <= lab[a].DistanceLab(lab[pb]) {
			return pa
		}
		return pb
	}

	for fi, f := range faces {
		if fi >= len(out) || int(f[0]) >= len(verts) {
			continue
		}
		cy := float64(verts[f[0]][1]+verts[f[1]][1]+verts[f[2]][1]) / 3
		p := int(math.Round(cy / PitchMM))
		if p < 0 || p >= len(plan.Plates) {
			continue
		}
		plate := plan.Plates[p]
		out[fi] = int32(nearestOfPair(int(out[fi]), plate.A, plate.B))
	}
	return out
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
