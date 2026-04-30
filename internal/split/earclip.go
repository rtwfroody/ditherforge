package split

import (
	"fmt"
	"math"
	"sort"
)

// pt2 is a 2D point. The earclip routines operate purely on 2D
// coordinates; mapping back to per-half *LoadedModel vertex indices is
// done via a parallel index slice (idx[i] is the vertex index
// corresponding to pts[i]).
type pt2 struct {
	X, Y float64
}

// signedArea returns 2× the signed area of the polygon. Positive when
// CCW, negative when CW.
func signedArea(pts []pt2) float64 {
	n := len(pts)
	if n < 3 {
		return 0
	}
	var s float64
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		s += pts[i].X*pts[j].Y - pts[j].X*pts[i].Y
	}
	return s
}

// reverse reverses pts (and the parallel idx slice) in place.
func reversePoly(pts []pt2, idx []uint32) {
	for i, j := 0, len(pts)-1; i < j; i, j = i+1, j-1 {
		pts[i], pts[j] = pts[j], pts[i]
		idx[i], idx[j] = idx[j], idx[i]
	}
}

// triangulate builds a triangle list for a polygon with holes. Outer
// must be CCW and holes must be CW; if not, they are reversed in place.
// idx is the parallel slice of vertex indices (in the destination
// half's vertex space) for each 2D point. Returns the list of
// triangles as [3]uint32 of vertex indices.
func triangulate(outer []pt2, outerIdx []uint32, holes [][]pt2, holeIdx [][]uint32) ([][3]uint32, error) {
	if len(outer) < 3 {
		return nil, fmt.Errorf("triangulate: outer loop has %d vertices, need at least 3", len(outer))
	}
	// Ensure outer is CCW.
	if signedArea(outer) < 0 {
		reversePoly(outer, outerIdx)
	}
	// Ensure each hole is CW.
	for i := range holes {
		if signedArea(holes[i]) > 0 {
			reversePoly(holes[i], holeIdx[i])
		}
	}

	// Merge holes into outer by inserting bridges. We process holes in
	// order of decreasing rightmost-x so each merge happens against the
	// outer polygon as currently augmented (rather than being blocked
	// by a not-yet-merged hole that lies to the right).
	pts := append([]pt2(nil), outer...)
	idx := append([]uint32(nil), outerIdx...)

	type holeInfo struct {
		idx        int
		rightmostI int
		rightmostX float64
	}
	hi := make([]holeInfo, len(holes))
	for i, h := range holes {
		ri := 0
		for k, p := range h {
			if p.X > h[ri].X {
				ri = k
			}
		}
		hi[i] = holeInfo{idx: i, rightmostI: ri, rightmostX: h[ri].X}
	}
	sort.Slice(hi, func(a, b int) bool { return hi[a].rightmostX > hi[b].rightmostX })

	for _, info := range hi {
		hole := holes[info.idx]
		holeIndices := holeIdx[info.idx]
		var err error
		pts, idx, err = bridgeHole(pts, idx, hole, holeIndices, info.rightmostI)
		if err != nil {
			return nil, fmt.Errorf("triangulate: %w", err)
		}
	}

	// Dedup consecutive near-coincident vertices. Cap polygons recovered
	// from dense alpha-wrapped meshes can have float-roundoff duplicates
	// where two adjacent triangle midpoints round to the same 2D point;
	// those break earClip's collinearity test (cross == 0). Tolerance is
	// set relative to the polygon's bbox so it scales with the model.
	pts, idx = dedupConsecutive(pts, idx)

	return earClip(pts, idx)
}

// dedupConsecutive collapses runs of vertices that are within an
// auto-scaled tolerance of each other. Keeps the parallel pts/idx
// slices in sync. Tolerance is 1e-9 × bbox diagonal — tighter than
// any meaningful geometric feature but still much larger than float
// noise.
func dedupConsecutive(pts []pt2, idx []uint32) ([]pt2, []uint32) {
	if len(pts) < 2 {
		return pts, idx
	}
	minX, maxX := pts[0].X, pts[0].X
	minY, maxY := pts[0].Y, pts[0].Y
	for _, p := range pts[1:] {
		if p.X < minX {
			minX = p.X
		}
		if p.X > maxX {
			maxX = p.X
		}
		if p.Y < minY {
			minY = p.Y
		}
		if p.Y > maxY {
			maxY = p.Y
		}
	}
	bboxDiag := math.Hypot(maxX-minX, maxY-minY)
	tol := 1e-9 * bboxDiag
	if tol <= 0 {
		return pts, idx
	}
	tolSq := tol * tol
	close := func(a, b pt2) bool {
		dx := a.X - b.X
		dy := a.Y - b.Y
		return dx*dx+dy*dy < tolSq
	}
	out := pts[:0:0]
	outIdx := idx[:0:0]
	for i, p := range pts {
		if len(out) > 0 && close(out[len(out)-1], p) {
			continue
		}
		out = append(out, p)
		outIdx = append(outIdx, idx[i])
	}
	// Wrap-around: if last == first, drop the last entry.
	for len(out) > 3 && close(out[len(out)-1], out[0]) {
		out = out[:len(out)-1]
		outIdx = outIdx[:len(outIdx)-1]
	}
	return out, outIdx
}

// bridgeHole inserts a hole into the outer polygon by finding a
// visible bridge from the hole's rightmost vertex to the outer loop
// and splicing the hole's vertices into the outer at that bridge
// point. Follows the Mapbox earcut algorithm: find the closest +X
// intersection with an outer edge, then verify the bridge endpoint is
// visible (no reflex outer vertex blocks the segment M–P).
func bridgeHole(outer []pt2, outerIdx []uint32, hole []pt2, holeIdx []uint32, rightmostI int) ([]pt2, []uint32, error) {
	M := hole[rightmostI]

	// Step 1: find the closest +X intersection with an outer edge,
	// and remember the upper-y endpoint of that edge as the candidate
	// bridge vertex.
	bestX := math.Inf(1)
	var candidateIdx int = -1
	var bestIntersectX float64
	for i := 0; i < len(outer); i++ {
		j := (i + 1) % len(outer)
		a := outer[i]
		b := outer[j]
		if a.Y == b.Y {
			continue
		}
		if (a.Y < M.Y) == (b.Y < M.Y) {
			continue
		}
		t := (M.Y - a.Y) / (b.Y - a.Y)
		x := a.X + t*(b.X-a.X)
		if x < M.X {
			continue
		}
		if x < bestX {
			bestX = x
			bestIntersectX = x
			if a.X > b.X {
				candidateIdx = i
			} else {
				candidateIdx = j
			}
		}
	}
	if candidateIdx < 0 {
		return nil, nil, fmt.Errorf("could not find bridge edge for hole (rightmost vertex (%g, %g))", M.X, M.Y)
	}

	// Step 2: find a visible bridge endpoint. The default candidate
	// is whichever endpoint of the closest outer edge is at higher
	// x. But that endpoint may not be visible from M if a reflex
	// outer vertex lies inside the triangle M–intersection–P.
	// Following Mapbox earcut: scan all reflex outer vertices inside
	// that triangle and pick the one with the smallest angle to M
	// (or the closest x if angles tie). Falls back to the candidate
	// when no reflex vertices are inside.
	bridgeOuterIdx := candidateIdx
	intersect := pt2{X: bestIntersectX, Y: M.Y}
	bestAngle := math.Inf(1)
	for k := range outer {
		if k == candidateIdx {
			continue
		}
		v := outer[k]
		// Only reflex vertices can block visibility.
		ip := outer[(k-1+len(outer))%len(outer)]
		in := outer[(k+1)%len(outer)]
		if cross(ip, v, in) > 0 {
			continue // convex
		}
		if v.X < M.X {
			continue
		}
		P := outer[candidateIdx]
		if !pointInTriangle(v, M, intersect, P) {
			continue
		}
		// Angle to M (smaller is closer to the M→+X ray).
		dy := math.Abs(v.Y - M.Y)
		dx := v.X - M.X
		ang := dy / dx
		if ang < bestAngle || (ang == bestAngle && v.X < outer[bridgeOuterIdx].X) {
			bestAngle = ang
			bridgeOuterIdx = k
		}
	}

	// Splice: at outer[bridgeOuterIdx], insert M, walk the hole
	// starting from rightmostI (around CW since holes are CW), then
	// return to outer[bridgeOuterIdx]. The merged polygon walks:
	//
	//   outer[0], ..., outer[bridgeOuterIdx], M=hole[rm],
	//   hole[rm-1], hole[rm-2], ..., hole[rm+1], hole[rm], outer[bridgeOuterIdx],
	//   outer[bridgeOuterIdx+1], ...
	//
	// We need the hole walked in CCW direction relative to the bridge:
	// since holes are CW, we walk them backwards (from rm, decreasing).
	merged := make([]pt2, 0, len(outer)+len(hole)+2)
	mergedIdx := make([]uint32, 0, len(outerIdx)+len(holeIdx)+2)
	merged = append(merged, outer[:bridgeOuterIdx+1]...)
	mergedIdx = append(mergedIdx, outerIdx[:bridgeOuterIdx+1]...)

	// Walk the hole in its native CW direction, starting from
	// rightmostI: rm, rm+1, rm+2, ..., rm-1, rm. This keeps the
	// annulus (the region we want triangulated) on the left of the
	// merged polygon's CCW traversal.
	n := len(hole)
	for k := 0; k <= n; k++ {
		hi := (rightmostI + k) % n
		merged = append(merged, hole[hi])
		mergedIdx = append(mergedIdx, holeIdx[hi])
	}

	merged = append(merged, outer[bridgeOuterIdx])
	mergedIdx = append(mergedIdx, outerIdx[bridgeOuterIdx])
	merged = append(merged, outer[bridgeOuterIdx+1:]...)
	mergedIdx = append(mergedIdx, outerIdx[bridgeOuterIdx+1:]...)

	return merged, mergedIdx, nil
}

// earClip triangulates a simple polygon (CCW, no holes — bridges
// already merged) using the standard ear-clipping algorithm. Returns a
// flat triangle list. Triangles are emitted CCW.
func earClip(pts []pt2, idx []uint32) ([][3]uint32, error) {
	n := len(pts)
	if n < 3 {
		return nil, fmt.Errorf("earClip: %d vertices, need at least 3", n)
	}
	// Doubly linked list of remaining vertices.
	prev := make([]int, n)
	next := make([]int, n)
	for i := 0; i < n; i++ {
		prev[i] = (i - 1 + n) % n
		next[i] = (i + 1) % n
	}

	tris := make([][3]uint32, 0, n-2)
	remaining := n
	guard := 2 * n // cycle guard

	// Tolerance starts strict (only accept cross > 0 — convex corners
	// with positive triangle area). When the strict pass stalls
	// (guard hits 0), relax to accept slightly-reflex ears (cross >
	// -tolerance), which lets near-collinear sequences from dense
	// alpha-wrapped cap polygons clip through. Relaxed ears still go
	// through isEar's pointInTriangle interior check, so we never
	// produce a self-intersecting triangulation — only thinner
	// triangles than ideal.
	bbDiag := bboxDiag2D(pts)
	tolerances := []float64{0, -1e-12 * bbDiag * bbDiag, -1e-9 * bbDiag * bbDiag, -1e-6 * bbDiag * bbDiag}
	tolIdx := 0

	i := 0
	for remaining > 3 {
		guard--
		if guard < 0 {
			tolIdx++
			if tolIdx >= len(tolerances) {
				return nil, fmt.Errorf("earClip: failed to find an ear after %d tolerance levels (degenerate polygon, %d vertices remaining)", len(tolerances), remaining)
			}
			guard = 2 * remaining
		}
		ip := prev[i]
		in := next[i]
		if isEar(pts, prev, next, ip, i, in, tolerances[tolIdx]) {
			tris = append(tris, [3]uint32{idx[ip], idx[i], idx[in]})
			next[ip] = in
			prev[in] = ip
			remaining--
			i = in
		} else {
			i = next[i]
		}
	}
	// Final triangle.
	ip := prev[i]
	in := next[i]
	tris = append(tris, [3]uint32{idx[ip], idx[i], idx[in]})
	return tris, nil
}

// bboxDiag2D returns the diagonal of the 2D bounding box of pts.
func bboxDiag2D(pts []pt2) float64 {
	if len(pts) == 0 {
		return 0
	}
	minX, maxX := pts[0].X, pts[0].X
	minY, maxY := pts[0].Y, pts[0].Y
	for _, p := range pts[1:] {
		if p.X < minX {
			minX = p.X
		}
		if p.X > maxX {
			maxX = p.X
		}
		if p.Y < minY {
			minY = p.Y
		}
		if p.Y > maxY {
			maxY = p.Y
		}
	}
	return math.Hypot(maxX-minX, maxY-minY)
}

// isEar reports whether vertex v with neighbours p and n forms an ear
// of the current polygon (no other reflex vertex inside). minArea2 is
// the lower bound for cross(a,b,c) (= 2·signed area); 0 is strict
// (accept only convex corners). Callers that have stalled on
// near-collinear vertices can pass a small negative minArea2 to accept
// thin slightly-reflex ears, with the safety net that the
// pointInTriangle interior check still runs.
func isEar(pts []pt2, prev, next []int, p, v, n int, minArea2 float64) bool {
	a := pts[p]
	b := pts[v]
	c := pts[n]
	if cross(a, b, c) <= minArea2 {
		// Reflex beyond tolerance, collinear (when strict), or
		// below-threshold — not an ear.
		return false
	}
	// Walk all OTHER current-polygon vertices; if any reflex vertex is
	// inside triangle abc, this isn't an ear.
	for i := next[n]; i != p; i = next[i] {
		if i == p || i == v || i == n {
			continue
		}
		ip := prev[i]
		in := next[i]
		if cross(pts[ip], pts[i], pts[in]) > 0 {
			// Convex vertex; even if inside, it doesn't disqualify.
			continue
		}
		if pointInTriangle(pts[i], a, b, c) {
			return false
		}
	}
	return true
}

// cross returns 2× the signed area of triangle abc.
func cross(a, b, c pt2) float64 {
	return (b.X-a.X)*(c.Y-a.Y) - (b.Y-a.Y)*(c.X-a.X)
}

// pointInTriangle reports whether p is strictly inside triangle abc
// (assumed CCW). Boundary points are considered outside, so collinear
// vertices don't block ear removal.
func pointInTriangle(p, a, b, c pt2) bool {
	d1 := cross(a, b, p)
	d2 := cross(b, c, p)
	d3 := cross(c, a, p)
	return d1 > 0 && d2 > 0 && d3 > 0
}

// pointInPolygon reports whether p is strictly inside the given simple
// polygon, using the ray-casting algorithm (ray going +x). Points on
// the boundary are not considered inside.
func pointInPolygon(p pt2, poly []pt2) bool {
	inside := false
	n := len(poly)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		a, b := poly[i], poly[j]
		// Edge crosses horizontal line through p?
		if (a.Y > p.Y) == (b.Y > p.Y) {
			continue
		}
		// X-coordinate where the edge crosses y = p.Y.
		x := a.X + (p.Y-a.Y)*(b.X-a.X)/(b.Y-a.Y)
		if p.X < x {
			inside = !inside
		}
	}
	return inside
}

// planeBasis returns an orthonormal basis (u, v) on the given plane
// normal n, such that u × v = n. The choice of u is arbitrary but
// stable: we pick the world axis least aligned with n.
func planeBasis(n [3]float64) (u, v [3]float64) {
	ax := math.Abs(n[0])
	ay := math.Abs(n[1])
	az := math.Abs(n[2])
	var seed [3]float64
	switch {
	case ax <= ay && ax <= az:
		seed = [3]float64{1, 0, 0}
	case ay <= az:
		seed = [3]float64{0, 1, 0}
	default:
		seed = [3]float64{0, 0, 1}
	}
	// u = normalize(seed - (seed·n) n).
	dot := seed[0]*n[0] + seed[1]*n[1] + seed[2]*n[2]
	u = [3]float64{seed[0] - dot*n[0], seed[1] - dot*n[1], seed[2] - dot*n[2]}
	ulen := math.Sqrt(u[0]*u[0] + u[1]*u[1] + u[2]*u[2])
	u[0] /= ulen
	u[1] /= ulen
	u[2] /= ulen
	// v = n × u.
	v = [3]float64{
		n[1]*u[2] - n[2]*u[1],
		n[2]*u[0] - n[0]*u[2],
		n[0]*u[1] - n[1]*u[0],
	}
	return u, v
}

// project3Dto2D projects a 3D point onto the (u, v) plane basis.
func project3Dto2D(p [3]float32, u, v [3]float64) pt2 {
	x := float64(p[0])*u[0] + float64(p[1])*u[1] + float64(p[2])*u[2]
	y := float64(p[0])*v[0] + float64(p[1])*v[1] + float64(p[2])*v[2]
	return pt2{X: x, Y: y}
}
