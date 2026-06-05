package cellslicer

import "math"

// voronoiCells partitions `region` into one compact cell per seed using
// the seeds' clipped Voronoi diagram. Each returned cell is the set of
// region points nearer to its seed than to any other seed — so the whole
// seed set tiles `region` exactly: no gaps, no overlaps, and every pair
// of neighbouring cells (boundary–boundary, boundary–interior, or
// interior–interior) meets along a single shared bisector. There are no
// long-skinny trapezoids and no corner overlaps (the failure mode of the
// old depth-3 ring trapezoids), and no clip-seam artefacts where two
// independent diagrams used to abut (the old ring-band / hex-cap seam).
//
// Seeds come from two families, concatenated and tiled by one diagram:
// boundary seeds on the footprint loops (walked at cellSize spacing) and
// interior seeds on the hex lattice (cellSize spaced) inside the cap
// region. A seed's Voronoi cell is the intersection of the half-planes
// "closer to me than to neighbour j". kinds[i] tags the cell emitted for
// seeds[i] (KindRing for boundary, KindHex for interior); the diagram
// itself does not branch on kind.
//
// Locality keeps this ~O(seeds) rather than O(seeds²). The key fact: the
// seeds are a roughly uniform cellSize-spaced point set, so every cell —
// boundary or interior — reaches at most ~2*cellSize from its seed (a
// boundary cell spans ~cellSize along the contour and the band is
// cellSize deep; an interior hex cell reaches only its circumradius
// cellSize/√3). So we start each cell as a local box of half-width
// localHalf around the seed — NOT a global box — and clip only against
// seeds within clipRadius (a uniform grid supplies them in O(1)).
// Starting from a *global* box with only near seeds was an earlier bug:
// with no interior seeds (a pure wall slab) the cell stayed unbounded
// inward and ran clear across the model to the far wall, re-entering the
// band there as a phantom overlapping cell. The local box supplies that
// inward bound directly, so the far wall's seeds are no longer needed.
// The convex result is then Clipper-clipped to `region`, which resolves
// concavities/holes and may split one cell into several polygons (each
// its own Cell).
//
// Two radii, both keyed off localHalf:
//
//   - localHalf must exceed a cell's reach (~2*cellSize) or cells would
//     be truncated, leaving gaps. 4*cellSize is a generous margin
//     (verified by TestVoronoiBandCellsTilesExactly across square, hole,
//     thin-strip, and reflex-corner footprints).
//   - clipRadius must cover the local box's far CORNER, at distance
//     √2*localHalf — a seed's bisector can clip a box corner out to
//     2*√2*localHalf ≈ 2.83*localHalf. We use 3*localHalf so every seed
//     that can touch the box is clipped, making the convex cell exact
//     within the box; since the region-cell lies inside the box, its
//     Clipper-clip is then the exact Voronoi cell. (2*localHalf — the box
//     EDGE — would skip corner-cutting seeds and rely on a thin, subtle
//     margin instead.)
//
// pxArea is the cellSize/4 pixel area used for the diagnostic Pixels
// field, kept consistent with the rest of PartitionSlabAnalytic.
func voronoiCells(seeds []Point2, kinds []CellKind, region *Footprint, cellSize, pxArea float32) []Cell {
	if region == nil || len(region.Loops) == 0 || len(seeds) == 0 {
		return nil
	}
	localHalf := 4 * cellSize
	clipRadius := 3 * localHalf
	grid := newSeedGrid(seeds, clipRadius)

	// region is the same coverTarget for every cell in this slab, so
	// convert it to Clipper paths once here rather than re-converting
	// inside the per-cell clip (see clipPolygonToClipPaths). The mm→µm
	// path conversion was ~7% of voxelize CPU when done per cell.
	regionPaths := footprintToClipperPaths(region)

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
		for _, c := range clipPolygonToClipPaths(cell, regionPaths) {
			if len(c) < 3 {
				continue
			}
			area := signedArea(c)
			if area < 0 {
				area = -area
			}
			cells = append(cells, Cell{Outer: c, Kind: kinds[i], Pixels: int(area / pxArea)})
		}
	}
	return cells
}

// voronoiBandCells tiles `region` with the clipped Voronoi diagram of a
// single seed family, all tagged `kind`. Thin wrapper over voronoiCells
// for callers (and tests) that have one uniform seed set.
func voronoiBandCells(seeds []Point2, region *Footprint, cellSize, pxArea float32, kind CellKind) []Cell {
	kinds := make([]CellKind, len(seeds))
	for i := range kinds {
		kinds[i] = kind
	}
	return voronoiCells(seeds, kinds, region, cellSize, pxArea)
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

// add inserts p into the grid so subsequent queries see it. Used by the
// greedy seed-acceptance pass, which grows the accepted set incrementally.
func (g *seedGrid) add(p Point2) {
	j := len(g.seeds)
	g.seeds = append(g.seeds, p)
	k := g.key(p)
	g.cells[k] = append(g.cells[k], j)
}

// hasCloserThan reports whether any indexed seed is strictly closer than
// dist to p. dist must be <= the grid's bucket size so the 3×3 scan covers
// the full neighbourhood.
func (g *seedGrid) hasCloserThan(p Point2, dist float32) bool {
	d2 := dist * dist
	k := g.key(p)
	for dx := -1; dx <= 1; dx++ {
		for dy := -1; dy <= 1; dy++ {
			for _, j := range g.cells[[2]int{k[0] + dx, k[1] + dy}] {
				q := g.seeds[j]
				ddx := q[0] - p[0]
				ddy := q[1] - p[1]
				if ddx*ddx+ddy*ddy < d2 {
					return true
				}
			}
		}
	}
	return false
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
