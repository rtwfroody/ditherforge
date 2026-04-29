package split

import (
	"container/heap"
	"math"
)

// poleOfInaccessibility returns the point inside the polygon-with-holes
// that maximizes the distance to any polygon edge, plus that distance.
// The polygon's outer loop should be CCW and its holes CW, but the
// algorithm is robust to either orientation (it uses point-in-polygon
// tests, not signed area).
//
// precision is the termination threshold; smaller is more accurate at
// the cost of more iterations. A value of bbox_diagonal / 100 is fine
// for connector placement.
//
// Returned distance ≤ 0 means no interior point was found (degenerate
// or self-intersecting polygon). Callers should treat this as "no
// inscribed circle" — connectorPlacement uses the dist >= 2×D check to
// achieve this.
//
// Algorithm: Mapbox polylabel — priority-queue subdivision of the
// bbox into cells, ordered by upper-bound on max distance achievable
// in the cell. See https://github.com/mapbox/polylabel.
func poleOfInaccessibility(outer []pt2, holes [][]pt2, precision float64) (pt2, float64) {
	if len(outer) == 0 {
		return pt2{}, 0
	}
	minX, minY := outer[0].X, outer[0].Y
	maxX, maxY := outer[0].X, outer[0].Y
	for _, p := range outer {
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
	width := maxX - minX
	height := maxY - minY
	// Use min for the initial cell size when both dims are positive;
	// fall back to max for sliver polygons (one dim collapsed).
	cellSize := math.Min(width, height)
	if cellSize == 0 {
		cellSize = math.Max(width, height)
	}
	if cellSize == 0 {
		return pt2{X: minX, Y: minY}, 0
	}
	h := cellSize / 2

	// Initial best: bbox-center cell.
	best := newPolylabelCell(minX+width/2, minY+height/2, 0, outer, holes)

	pq := &polylabelHeap{}
	heap.Init(pq)
	for x := minX; x < maxX; x += cellSize {
		for y := minY; y < maxY; y += cellSize {
			c := newPolylabelCell(x+h, y+h, h, outer, holes)
			heap.Push(pq, c)
		}
	}

	for pq.Len() > 0 {
		c := heap.Pop(pq).(*polylabelCell)
		if c.dist > best.dist {
			best = c
		}
		// Skip if no child cell could beat best by `precision`.
		if c.maxDist-best.dist <= precision {
			continue
		}
		half := c.h / 2
		heap.Push(pq, newPolylabelCell(c.x-half, c.y-half, half, outer, holes))
		heap.Push(pq, newPolylabelCell(c.x+half, c.y-half, half, outer, holes))
		heap.Push(pq, newPolylabelCell(c.x-half, c.y+half, half, outer, holes))
		heap.Push(pq, newPolylabelCell(c.x+half, c.y+half, half, outer, holes))
	}

	// best.dist <= 0 means no interior point was sampled; the polygon
	// is degenerate or wholly outside its bbox-sample grid. Surface
	// this as a non-positive distance so callers can reject without
	// returning a misleading "valid" position.
	if best.dist <= 0 {
		return pt2{X: best.x, Y: best.y}, 0
	}
	return pt2{X: best.x, Y: best.y}, best.dist
}

// polylabelCell is a square subregion of the polygon's bbox.
type polylabelCell struct {
	x, y    float64 // center
	h       float64 // half-side
	dist    float64 // signed distance from (x, y) to polygon: + inside, - outside
	maxDist float64 // upper bound on dist achievable inside this cell: dist + h*√2
}

func newPolylabelCell(x, y, h float64, outer []pt2, holes [][]pt2) *polylabelCell {
	d := pointToPolygonSignedDist(pt2{X: x, Y: y}, outer, holes)
	return &polylabelCell{
		x:       x,
		y:       y,
		h:       h,
		dist:    d,
		maxDist: d + h*math.Sqrt2,
	}
}

// pointToPolygonSignedDist returns +distance to the nearest edge if p
// is inside the polygon-with-holes, -distance if outside.
func pointToPolygonSignedDist(p pt2, outer []pt2, holes [][]pt2) float64 {
	inside := pointInPolygon(p, outer)
	for _, h := range holes {
		if pointInPolygon(p, h) {
			inside = false
			break
		}
	}
	minDistSq := math.Inf(1)
	minDistSq = updateMinSegDistSq(minDistSq, p, outer)
	for _, h := range holes {
		minDistSq = updateMinSegDistSq(minDistSq, p, h)
	}
	d := math.Sqrt(minDistSq)
	if !inside {
		d = -d
	}
	return d
}

func updateMinSegDistSq(curr float64, p pt2, poly []pt2) float64 {
	n := len(poly)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		d := segDistSq(p, poly[i], poly[j])
		if d < curr {
			curr = d
		}
	}
	return curr
}

// segDistSq returns the squared distance from p to segment ab.
func segDistSq(p, a, b pt2) float64 {
	dx, dy := b.X-a.X, b.Y-a.Y
	if dx == 0 && dy == 0 {
		ddx, ddy := p.X-a.X, p.Y-a.Y
		return ddx*ddx + ddy*ddy
	}
	t := ((p.X-a.X)*dx + (p.Y-a.Y)*dy) / (dx*dx + dy*dy)
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	ddx := p.X - (a.X + t*dx)
	ddy := p.Y - (a.Y + t*dy)
	return ddx*ddx + ddy*ddy
}

// polylabelHeap orders cells by maxDist desc (max-heap).
type polylabelHeap []*polylabelCell

func (h polylabelHeap) Len() int            { return len(h) }
func (h polylabelHeap) Less(i, j int) bool  { return h[i].maxDist > h[j].maxDist }
func (h polylabelHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *polylabelHeap) Push(x any)         { *h = append(*h, x.(*polylabelCell)) }
func (h *polylabelHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
