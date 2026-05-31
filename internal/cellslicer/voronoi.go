package cellslicer

import "math"

// voronoiBandCells partitions `region` (a thin boundary band) into one
// compact cell per seed, using the seeds' clipped Voronoi diagram. Each
// returned cell is the set of band points nearer to its seed than to
// any other seed — convex, non-overlapping, and contiguous by
// construction, so there are no long-skinny trapezoids and no corner
// overlaps (the failure mode of the old depth-3 ring trapezoids).
//
// Seeds are expected to lie on the band's outer edge (the footprint
// loops, walked at cellSize spacing). A seed's Voronoi cell is the
// intersection of the half-planes "closer to me than to neighbour j".
//
// Locality keeps this ~O(seeds) rather than O(seeds²). The key fact: a
// boundary seed's cell, once intersected with the cellSize-wide band,
// never reaches more than ~2*cellSize from its seed (it spans ~cellSize
// along the contour and the band is cellSize deep). So we start each
// cell as a local box of half-width localHalf around the seed — NOT a
// global box — and clip only against seeds within 2*localHalf. A seed
// farther than that has its bisector beyond the local box, so it cannot
// affect the cell and is safely skipped (a uniform grid supplies the
// near seeds in O(1)). Starting from a *global* box with only near seeds
// was the earlier bug: with no interior seeds (a pure wall slab) the
// cell stayed unbounded inward and ran clear across the model to the far
// wall, re-entering the band there as a phantom overlapping cell. The
// local box supplies that inward bound directly, so the far wall's seeds
// are no longer needed. The convex result is then Clipper-clipped to the
// band, which resolves concavities/holes and may split one cell into
// several polygons (each its own Cell).
//
// localHalf must exceed the band-cell's reach or cells would be
// truncated, leaving gaps; 4*cellSize is a generous safe margin over the
// ~2*cellSize worst case (verified by TestVoronoiBandCellsTilesExactly,
// which checks the cells tile the band with no gaps and no overlaps).
//
// kind tags the emitted cells (KindRing for boundary cells). pxArea is
// the cellSize/4 pixel area used for the diagnostic Pixels field, kept
// consistent with the rest of PartitionSlabAnalytic.
func voronoiBandCells(seeds []Point2, region *Footprint, cellSize, pxArea float32, kind CellKind) []Cell {
	if region == nil || len(region.Loops) == 0 || len(seeds) == 0 {
		return nil
	}
	localHalf := 4 * cellSize
	clipRadius := 2 * localHalf
	grid := newSeedGrid(seeds, clipRadius)

	cells := make([]Cell, 0, len(seeds))
	for i := range seeds {
		si := seeds[i]
		cell := []Point2{
			{si[0] - localHalf, si[1] - localHalf},
			{si[0] + localHalf, si[1] - localHalf},
			{si[0] + localHalf, si[1] + localHalf},
			{si[0] - localHalf, si[1] + localHalf},
		}
		grid.forEachWithin(si, func(j int) {
			if j != i {
				cell = clipHalfPlaneCloserTo(cell, si, seeds[j])
			}
		})
		if len(cell) < 3 {
			continue
		}
		for _, c := range clipPolygonToFootprint(cell, region) {
			if len(c) < 3 {
				continue
			}
			area := signedArea(c)
			if area < 0 {
				area = -area
			}
			cells = append(cells, Cell{Outer: c, Kind: kind, Pixels: int(area / pxArea)})
		}
	}
	return cells
}

// clipHalfPlaneCloserTo returns the part of convex polygon `poly` that
// is at least as close to `s` as to `t` — i.e. on s's side of the
// perpendicular bisector of segment s–t. Sutherland–Hodgman against the
// single bisector line; `poly` stays convex. Returns a polygon that
// always contains s (f(s) < 0), so it never empties for a box that
// contained s.
func clipHalfPlaneCloserTo(poly []Point2, s, t Point2) []Point2 {
	// Inside test: f(p) = (p - mid)·(t - s) <= 0  (mid is the bisector
	// foot). f(s) = -|t-s|²/2 < 0, so s is always inside.
	dx := t[0] - s[0]
	dy := t[1] - s[1]
	midx := (s[0] + t[0]) * 0.5
	midy := (s[1] + t[1]) * 0.5
	f := func(p Point2) float32 { return (p[0]-midx)*dx + (p[1]-midy)*dy }

	n := len(poly)
	if n == 0 {
		return poly
	}
	out := make([]Point2, 0, n+2)
	for k := 0; k < n; k++ {
		a := poly[k]
		b := poly[(k+1)%n]
		fa := f(a)
		fb := f(b)
		if fa <= 0 {
			out = append(out, a)
		}
		if (fa < 0) != (fb < 0) {
			// Edge crosses the bisector: emit the intersection.
			u := fa / (fa - fb)
			out = append(out, Point2{
				a[0] + u*(b[0]-a[0]),
				a[1] + u*(b[1]-a[1]),
			})
		}
	}
	return out
}

// seedGrid is a uniform spatial hash over seed positions, bucketed at
// the neighbour-search radius so a seed within that radius of a query
// point always lands in the 3×3 block of buckets around the query's
// bucket.
type seedGrid struct {
	bucket     float32
	radius2    float32
	minX, minY float32
	cells      map[[2]int][]int
	seeds      []Point2
}

func newSeedGrid(seeds []Point2, radius float32) *seedGrid {
	g := &seedGrid{
		bucket:  radius,
		radius2: radius * radius,
		minX:    math.MaxFloat32,
		minY:    math.MaxFloat32,
		cells:   make(map[[2]int][]int, len(seeds)),
		seeds:   seeds,
	}
	for _, p := range seeds {
		if p[0] < g.minX {
			g.minX = p[0]
		}
		if p[1] < g.minY {
			g.minY = p[1]
		}
	}
	for i, p := range seeds {
		k := g.key(p)
		g.cells[k] = append(g.cells[k], i)
	}
	return g
}

func (g *seedGrid) key(p Point2) [2]int {
	return [2]int{
		int(math.Floor(float64((p[0] - g.minX) / g.bucket))),
		int(math.Floor(float64((p[1] - g.minY) / g.bucket))),
	}
}

// forEachWithin calls fn(j) for every seed j within the grid radius of
// p. Scans the 3×3 block of buckets around p's bucket (the bucket size
// equals the radius, so that block covers the full radius) and filters
// by exact squared distance.
func (g *seedGrid) forEachWithin(p Point2, fn func(j int)) {
	k := g.key(p)
	for dx := -1; dx <= 1; dx++ {
		for dy := -1; dy <= 1; dy++ {
			for _, j := range g.cells[[2]int{k[0] + dx, k[1] + dy}] {
				q := g.seeds[j]
				ddx := q[0] - p[0]
				ddy := q[1] - p[1]
				if ddx*ddx+ddy*ddy <= g.radius2 {
					fn(j)
				}
			}
		}
	}
}
