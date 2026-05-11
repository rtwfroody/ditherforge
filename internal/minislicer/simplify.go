package minislicer

// SimplifyAndReclassify runs Douglas-Peucker on every loop in every
// layer, then re-runs the outer/hole classifier. Mutates `layers`
// in place.
//
// Slicer output for curved surfaces (e.g. a 100mm Benchy hull) has
// one polygon vertex per crossing triangle — easily 500+ per loop.
// That pushes earcut's O(n²) ear search into multi-minute territory.
// Simplifying to a per-vertex perpendicular distance threshold (a
// fraction of cellSize is plenty fine for cap geometry and color
// section midpoints) brings each loop down to tens of vertices and
// the whole pipeline back to sub-second.
func SimplifyAndReclassify(layers []Layer, tolerance float32) {
	if tolerance <= 0 {
		return
	}
	for li := range layers {
		for lp := range layers[li].Loops {
			pts, tris := simplifyClosedDPWithTris(
				layers[li].Loops[lp].Points,
				layers[li].Loops[lp].EdgeTris,
				tolerance)
			if len(pts) < 3 {
				continue
			}
			layers[li].Loops[lp].Points = pts
			layers[li].Loops[lp].EdgeTris = tris
			layers[li].Loops[lp].SignedArea = signedArea(pts)
		}
		for lp := range layers[li].Loops {
			layers[li].Loops[lp].IsHole = false
			layers[li].Loops[lp].HasHoleChild = false
		}
		classifyHoles(layers[li].Loops)
	}
}

// simplifyClosedDP runs Douglas-Peucker on a closed polygon by
// splitting at the diameter pair (the two vertices farthest apart)
// and DP-simplifying each arc independently. Returns a new slice
// of points; never modifies the input.
func simplifyClosedDP(pts []Point2, tol float32) []Point2 {
	out, _ := simplifyClosedDPWithTris(pts, nil, tol)
	return out
}

// simplifyClosedDPWithTris is simplifyClosedDP that also carries
// a parallel per-edge triangle index array. tris[i] is the source
// triangle for the edge pts[i] → pts[(i+1)%n]. After simplification
// the surviving edges' triangle indices come from the LAST
// pre-simplification edge that ended at the surviving vertex —
// equivalently, the original edge whose endpoint is the surviving
// vertex's "next" (preserves the invariant that edge i in the
// output points to a triangle whose intersection with the slicing
// plane actually contained the section's midpoint XY, modulo DP
// tolerance). Pass nil tris to skip the bookkeeping.
func simplifyClosedDPWithTris(pts []Point2, tris []int32, tol float32) ([]Point2, []int32) {
	n := len(pts)
	if n < 4 {
		return pts, tris
	}
	a, b := 0, 0
	maxSq := float32(-1)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			dx := pts[j][0] - pts[i][0]
			dy := pts[j][1] - pts[i][1]
			d := dx*dx + dy*dy
			if d > maxSq {
				maxSq = d
				a, b = i, j
			}
		}
	}
	keep := make([]bool, n)
	keep[a] = true
	keep[b] = true
	tolSq := tol * tol
	dpStep(pts, a, b, tolSq, keep, n)
	dpStep(pts, b, a, tolSq, keep, n)

	outPts := make([]Point2, 0, n)
	var outTris []int32
	if tris != nil {
		outTris = make([]int32, 0, n)
	}
	for i := 0; i < n; i++ {
		if !keep[i] {
			continue
		}
		outPts = append(outPts, pts[i])
		if tris != nil {
			// Each surviving vertex i in `keep` becomes a vertex in
			// outPts; the outgoing edge to the NEXT surviving vertex
			// spans the original edges (i, i+1, ..., next-1). We use
			// the FIRST original edge's triangle (tris[i]) as the
			// representative; this is the triangle whose original
			// segment shared vertex i and is most likely to contain
			// the simplified edge's midpoint.
			if i < len(tris) {
				outTris = append(outTris, tris[i])
			} else {
				outTris = append(outTris, -1)
			}
		}
	}
	return outPts, outTris
}

// dpStep is the recursive Douglas-Peucker step over the half-arc
// from lo to hi (indices into pts, going forward through the
// cyclic polygon). Marks the vertex farthest from the lo-hi chord
// as kept (if its perpendicular distance² > tolSq) and recurses.
func dpStep(pts []Point2, lo, hi int, tolSq float32, keep []bool, n int) {
	maxDistSq := float32(0)
	maxIdx := -1
	i := (lo + 1) % n
	steps := 0
	for i != hi && steps < n {
		d := perpDistSq(pts[lo], pts[hi], pts[i])
		if d > maxDistSq {
			maxDistSq = d
			maxIdx = i
		}
		i = (i + 1) % n
		steps++
	}
	if maxDistSq > tolSq && maxIdx >= 0 {
		keep[maxIdx] = true
		dpStep(pts, lo, maxIdx, tolSq, keep, n)
		dpStep(pts, maxIdx, hi, tolSq, keep, n)
	}
}

// perpDistSq returns the squared perpendicular distance from p to
// the line through a and b. If a == b, returns squared distance
// from p to a.
func perpDistSq(a, b, p Point2) float32 {
	dx := b[0] - a[0]
	dy := b[1] - a[1]
	px := p[0] - a[0]
	py := p[1] - a[1]
	edgeSq := dx*dx + dy*dy
	if edgeSq == 0 {
		return px*px + py*py
	}
	cross := px*dy - py*dx
	return cross * cross / edgeSq
}
