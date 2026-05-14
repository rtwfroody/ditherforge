package cellslicer

import (
	"math"
	"sort"
)

// Earcut triangulates a polygon-with-holes via ear-clipping with
// hole bridging. Each hole is bridged into the outer to produce a
// single non-self-intersecting polygon; then ear-clipping removes
// "ears" (convex triangles with no other polygon vertices inside)
// one at a time until only one triangle is left.
//
// `outer` is the outer boundary; `holes` are inner closed loops
// that punch holes in the outer. Both must be closed polygons
// without their first vertex repeated at the end. Winding direction
// doesn't matter — Earcut normalizes outer to CCW and holes to CW
// internally.
//
// Returns a flat vertex array (outer + each hole appended in
// `holes` order, possibly with bridge-duplicate vertices appended)
// and a triangle list where each triangle is three indices into
// that vertex array.
//
// Adapted from Mapbox's earcut.js (ISC).
//
// TODO: Mapbox's earcut runs the ear-search inner loop against a
// Z-order (Morton-code) hash bucket so it only checks vertices
// within the candidate ear's bounding box — amortized
// ~O(n log n) instead of the O(n²) we have today. Worth adding
// when cap polygons grow past a few hundred vertices and the
// slicer starts to feel slow on big models; cap counts in the
// 10s–100s (typical for sliced layers) are fast enough as-is.
func Earcut(outer []Point2, holes [][]Point2) (verts []Point2, tris [][3]uint32) {
	if len(outer) < 3 {
		return outer, nil
	}
	// Concatenate vertices into a single array. Hole start indices
	// are used by linkedList to build a hole's circular list.
	verts = append(verts, outer...)
	holeStart := make([]int, len(holes))
	for i, h := range holes {
		holeStart[i] = len(verts)
		verts = append(verts, h...)
	}

	outerEnd := len(outer)
	outerNode := linkedList(verts, 0, outerEnd, true)
	if outerNode == nil || outerNode.next == outerNode.prev {
		return verts, nil
	}

	if len(holes) > 0 {
		outerNode = eliminateHoles(verts, holeStart, outerNode)
	}

	earcutLinked(outerNode, &tris)
	return verts, tris
}

// ecNode is a doubly-linked list node for earcut. `i` is the
// 0-based index into the combined vertex array.
type ecNode struct {
	i       int
	x, y    float64
	prev    *ecNode
	next    *ecNode
	steiner bool
}

// linkedList builds a circular doubly-linked list from
// `verts[start:end]`. If the polygon's winding matches `ccw`,
// vertices are linked forward; otherwise backward (so the resulting
// list is always in CCW order for outer, CW for holes when caller
// passes false).
func linkedList(verts []Point2, start, end int, ccw bool) *ecNode {
	a := signedAreaSlice(verts, start, end)
	var last *ecNode
	if (a > 0) == ccw {
		for i := start; i < end; i++ {
			last = ecInsert(i, float64(verts[i][0]), float64(verts[i][1]), last)
		}
	} else {
		for i := end - 1; i >= start; i-- {
			last = ecInsert(i, float64(verts[i][0]), float64(verts[i][1]), last)
		}
	}
	if last != nil && ecEquals(last, last.next) {
		ecRemove(last)
		last = last.next
	}
	return last
}

func ecInsert(i int, x, y float64, last *ecNode) *ecNode {
	n := &ecNode{i: i, x: x, y: y}
	if last == nil {
		n.prev = n
		n.next = n
	} else {
		n.next = last.next
		n.prev = last
		last.next.prev = n
		last.next = n
	}
	return n
}

func ecRemove(p *ecNode) {
	p.next.prev = p.prev
	p.prev.next = p.next
}

func ecEquals(a, b *ecNode) bool {
	return a.x == b.x && a.y == b.y
}

// signedAreaSlice is twice the signed area of the polygon
// verts[start:end]. Positive = CCW in math (Y-up) coords.
func signedAreaSlice(verts []Point2, start, end int) float64 {
	if end-start < 3 {
		return 0
	}
	var s float64
	j := end - 1
	for i := start; i < end; i++ {
		s += float64(verts[j][0])*float64(verts[i][1]) -
			float64(verts[i][0])*float64(verts[j][1])
		j = i
	}
	return s
}

// eliminateHoles bridges every hole into the outer linked list,
// producing a single simple polygon ear-clipping can handle.
func eliminateHoles(verts []Point2, holeStart []int, outerNode *ecNode) *ecNode {
	queue := make([]*ecNode, 0, len(holeStart))
	for i := range holeStart {
		start := holeStart[i]
		end := len(verts)
		if i+1 < len(holeStart) {
			end = holeStart[i+1]
		}
		list := linkedList(verts, start, end, false) // hole: CW
		if list == nil {
			continue
		}
		if list == list.next {
			list.steiner = true
		}
		queue = append(queue, leftmost(list))
	}
	// Process holes left-to-right so each bridge sees previously
	// inserted bridges as part of the outer.
	sort.SliceStable(queue, func(a, b int) bool {
		if queue[a].x != queue[b].x {
			return queue[a].x < queue[b].x
		}
		return queue[a].y < queue[b].y
	})
	for _, h := range queue {
		outerNode = eliminateHole(h, outerNode)
	}
	return outerNode
}

// leftmost returns the leftmost (smallest x; tie-break smallest y)
// node in the circular list rooted at start.
func leftmost(start *ecNode) *ecNode {
	n := start
	best := start
	for {
		if n.x < best.x || (n.x == best.x && n.y < best.y) {
			best = n
		}
		n = n.next
		if n == start {
			break
		}
	}
	return best
}

// eliminateHole bridges `hole` into the polygon rooted at
// `outerNode`. Returns the (possibly new) outer node to use for
// subsequent passes.
func eliminateHole(hole, outerNode *ecNode) *ecNode {
	bridge := findHoleBridge(hole, outerNode)
	if bridge == nil {
		// Couldn't find a bridge — degenerate input; skip.
		return outerNode
	}
	bridgeReverse := splitPolygon(bridge, hole)
	// Filter colinear / duplicate points on both sides so they
	// don't cause zero-area ears later.
	filterPoints(bridgeReverse, bridgeReverse.next)
	return filterPoints(outerNode, outerNode.next)
}

// findHoleBridge finds a vertex on the outer polygon visible from
// `hole`'s leftmost vertex that can serve as a bridge target.
// Returns nil if no candidate is visible (degenerate).
func findHoleBridge(hole, outerNode *ecNode) *ecNode {
	p := outerNode
	hx, hy := hole.x, hole.y
	qx := math.Inf(-1)
	var m *ecNode

	// Find the rightmost intersection of a horizontal ray at y=hy
	// going left-to-right with any outer edge that's strictly to
	// the left of hx.
	for {
		if hy <= p.y && hy >= p.next.y && p.next.y != p.y {
			x := p.x + (hy-p.y)*(p.next.x-p.x)/(p.next.y-p.y)
			if x <= hx && x > qx {
				qx = x
				if x == hx {
					if hy == p.y {
						return p
					}
					if hy == p.next.y {
						return p.next
					}
				}
				if p.x < p.next.x {
					m = p
				} else {
					m = p.next
				}
			}
		}
		p = p.next
		if p == outerNode {
			break
		}
	}
	if m == nil {
		return nil
	}
	if hx == qx {
		return m
	}

	// Look for a reflex vertex inside the bridge triangle that's
	// closer to the hole than m — using it instead of m avoids
	// crossings when multiple outer points sit between the ray hit
	// and hx.
	stop := m
	mx, my := m.x, m.y
	tanMin := math.Inf(1)
	p = m
	for {
		if hx >= p.x && p.x >= mx && hx != p.x &&
			pointInTriangleFloat(
				bridgeAx(hy, my, hx, mx), hy,
				mx, my,
				bridgeBx(hy, my, hx, mx), hy,
				p.x, p.y) {
			tan := math.Abs(hy-p.y) / (hx - p.x)
			if locallyInside(p, hole) && (tan < tanMin ||
				(tan == tanMin && (p.x > m.x || (p.x == m.x && sectorContainsSector(m, p))))) {
				m = p
				tanMin = tan
			}
		}
		p = p.next
		if p == stop {
			break
		}
	}
	return m
}

// bridgeAx / bridgeBx return the x-coordinates of the bridge
// triangle's left and right corners at y=hy. The bridge triangle is
// the candidate visibility region between the hole point and the
// ray-hit edge — points inside this triangle could potentially be
// closer bridge targets than the ray-hit vertex m.
func bridgeAx(hy, my, hx, mx float64) float64 {
	if hy < my {
		return hx
	}
	return mx
}
func bridgeBx(hy, my, hx, mx float64) float64 {
	if hy < my {
		return mx
	}
	return hx
}

func pointInTriangleFloat(ax, ay, bx, by, cx, cy, px, py float64) bool {
	return (cx-px)*(ay-py) >= (ax-px)*(cy-py) &&
		(ax-px)*(by-py) >= (bx-px)*(ay-py) &&
		(bx-px)*(cy-py) >= (cx-px)*(by-py)
}

// locallyInside reports whether vertex b lies inside the polygon
// near a — used to validate bridge candidates.
func locallyInside(a, b *ecNode) bool {
	if area3(a.prev, a, a.next) < 0 {
		return area3(a, b, a.next) >= 0 && area3(a, a.prev, b) >= 0
	}
	return area3(a, b, a.prev) < 0 || area3(a, a.next, b) < 0
}

// sectorContainsSector reports whether the angular sector at m
// covers the angular sector at p — used to break ties in the bridge
// search.
func sectorContainsSector(m, p *ecNode) bool {
	return area3(m.prev, m, p.prev) < 0 && area3(p.next, m, m.next) < 0
}

// area3 is the cross product of edges (p→q) and (q→r). Returns
// NEGATIVE for a CCW math-convention triangle and POSITIVE for CW.
// Matches Mapbox earcut.js's `area` so isEar / locallyInside /
// intersects can use that algorithm's sign conventions verbatim.
func area3(p, q, r *ecNode) float64 {
	return (q.y-p.y)*(r.x-q.x) - (q.x-p.x)*(r.y-q.y)
}

// splitPolygon inserts a bridge connection between a and b in the
// linked list, splitting one cycle into two (or merging two cycles
// into one, in the hole-elimination direction). Returns a fresh
// node that's a duplicate of a but lives on the hole side.
func splitPolygon(a, b *ecNode) *ecNode {
	a2 := &ecNode{i: a.i, x: a.x, y: a.y}
	b2 := &ecNode{i: b.i, x: b.x, y: b.y}
	an := a.next
	bp := b.prev

	a.next = b
	b.prev = a

	a2.next = an
	an.prev = a2

	b2.next = a2
	a2.prev = b2

	bp.next = b2
	b2.prev = bp

	return b2
}

// filterPoints removes consecutive duplicate or collinear vertices
// from start..end (cyclic). Returns the new end node.
func filterPoints(start, end *ecNode) *ecNode {
	if start == nil {
		return nil
	}
	if end == nil {
		end = start
	}
	p := start
	for {
		again := false
		if !p.steiner && (ecEquals(p, p.next) || area3(p.prev, p, p.next) == 0) {
			ecRemove(p)
			p = p.prev
			end = p
			if p == p.next {
				return nil
			}
			again = true
		}
		if !again {
			p = p.next
		}
		if p == end {
			break
		}
	}
	return end
}

// earcutLinked emits triangles by repeatedly clipping ears from
// the linked list. Stops when the polygon is reduced to one
// triangle (or fewer than 3 vertices for degenerate input).
func earcutLinked(ear *ecNode, tris *[][3]uint32) {
	if ear == nil {
		return
	}
	if ear == ear.next || ear == ear.next.next {
		return
	}
	// cureLocalIntersections can return cured=true without actually
	// shrinking the ring (its splitPolygon target sometimes doesn't
	// remove the "no ear found" obstruction), and the outer loop
	// would retry the same configuration forever. Bound the
	// cure-and-retry attempts so degenerate input always reaches
	// the fan fallback.
	cureAttempts := 0
	stop := ear
	for ear.prev != ear.next {
		prev := ear.prev
		next := ear.next
		if isEar(ear) {
			*tris = append(*tris, [3]uint32{uint32(prev.i), uint32(ear.i), uint32(next.i)})
			ecRemove(ear)
			ear = next.next
			stop = next.next
			cureAttempts = 0
			continue
		}
		ear = next
		if ear == stop {
			if cureAttempts < 4 {
				if cured := cureLocalIntersections(ear, tris); cured {
					cureAttempts++
					stop = ear
					continue
				}
			}
			emitFanFallback(ear, tris)
			return
		}
	}
}

// isEar reports whether the triangle (ear.prev, ear, ear.next) is a
// valid ear: convex at ear, and no other polygon vertex lies inside.
func isEar(ear *ecNode) bool {
	a := ear.prev
	b := ear
	c := ear.next
	if area3(a, b, c) >= 0 {
		return false // reflex or collinear
	}
	p := c.next
	for p != a {
		if pointInTriangleFloat(a.x, a.y, b.x, b.y, c.x, c.y, p.x, p.y) &&
			area3(p.prev, p, p.next) >= 0 {
			return false
		}
		p = p.next
	}
	return true
}

// cureLocalIntersections walks the polygon looking for pairs of
// non-adjacent vertices that coincide (a self-intersection produces
// these). Splits the polygon at such pairs, emitting any resulting
// triangle along the way. Returns true if it made progress.
//
// Bounded by an iteration count derived from the polygon length: if
// the loop's original `start` ever gets removed by an ecRemove call
// (the cure case), the `p == start` exit condition can never be
// reached (`start` is unlinked from the ring), so a degenerate
// polygon with a self-intersection at the entry node would loop
// forever. The iteration cap walks the polygon at most once.
func cureLocalIntersections(start *ecNode, tris *[][3]uint32) bool {
	cured := false
	// Count nodes in the current ring so we can bound the walk.
	maxIter := 0
	for n := start; ; n = n.next {
		maxIter++
		if n.next == start || maxIter > 1<<20 {
			break
		}
	}
	p := start
	for i := 0; i < maxIter+2; i++ {
		a := p.prev
		b := p.next.next
		if !ecEquals(a, b) && intersects(a, p, p.next, b) && locallyInside(a, b) && locallyInside(b, a) {
			*tris = append(*tris, [3]uint32{uint32(a.i), uint32(p.i), uint32(b.i)})
			ecRemove(p)
			ecRemove(p.next)
			p = b
			cured = true
			continue
		}
		p = p.next
		if p == start {
			break
		}
	}
	return cured
}

// intersects reports whether segment p1-p2 intersects p3-p4.
func intersects(p1, p2, q1, q2 *ecNode) bool {
	o1 := sign(area3(p1, p2, q1))
	o2 := sign(area3(p1, p2, q2))
	o3 := sign(area3(q1, q2, p1))
	o4 := sign(area3(q1, q2, p2))
	if o1 != o2 && o3 != o4 {
		return true
	}
	return false
}

func sign(v float64) int {
	if v > 0 {
		return 1
	}
	if v < 0 {
		return -1
	}
	return 0
}

// emitFanFallback emits a fan triangulation from the first vertex
// as a last resort when the polygon can't be ear-clipped (severely
// degenerate input). Produces N-2 triangles; some may be
// zero-area or overlap, but it gets SOME geometry into the mesh.
func emitFanFallback(start *ecNode, tris *[][3]uint32) {
	if start == nil {
		return
	}
	pivot := start
	a := start.next
	for a.next != start {
		b := a.next
		*tris = append(*tris, [3]uint32{uint32(pivot.i), uint32(a.i), uint32(b.i)})
		a = b
	}
}
