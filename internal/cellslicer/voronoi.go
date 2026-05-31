package cellslicer

// voronoiBandCells partitions `region` (a thin boundary band) into one
// compact cell per seed, using the seeds' clipped Voronoi diagram. Each
// returned cell is the set of band points nearer to its seed than to
// any other seed — convex, non-overlapping, and contiguous by
// construction, so there are no long-skinny trapezoids and no corner
// overlaps (the failure mode of the old depth-3 ring trapezoids).
//
// Seeds are expected to lie on the band's outer edge (the footprint
// loops, walked at cellSize spacing). A seed's Voronoi cell is the
// intersection of the half-planes "closer to me than to neighbour j"
// over EVERY other seed j. The full diagram matters even though the
// band is thin: with no interior seeds (a pure wall slab) a boundary
// seed's cell is bounded inward only by the *opposite* wall's seeds, at
// the medial axis — far beyond cellSize. Skipping those (an earlier
// neighbour-radius optimisation) let the convex cell run clear across
// the model as an unbounded strip, which then re-entered the ring band
// on the far wall and produced phantom overlapping cells. So we clip
// against all seeds (O(seeds²) cheap half-plane clips per slab; the
// convex cell stays small so each clip is a handful of edges).
// The convex half-plane intersection is then Clipper-clipped to the
// band, which resolves the band's concavities/holes and may split one
// Voronoi cell into several polygons (each its own Cell).
//
// kind tags the emitted cells (KindRing for boundary cells). pxArea is
// the cellSize/4 pixel area used for the diagnostic Pixels field, kept
// consistent with the rest of PartitionSlabAnalytic.
func voronoiBandCells(seeds []Point2, region *Footprint, cellSize, pxArea float32, kind CellKind) []Cell {
	if region == nil || len(region.Loops) == 0 || len(seeds) == 0 {
		return nil
	}
	minX, minY, maxX, maxY, ok := region.Bounds()
	if !ok {
		return nil
	}
	// Initial convex cell: the region bbox padded out so every seed's
	// unbounded Voronoi directions start inside it before clipping.
	pad := 4 * cellSize
	box := []Point2{
		{minX - pad, minY - pad},
		{maxX + pad, minY - pad},
		{maxX + pad, maxY + pad},
		{minX - pad, maxY + pad},
	}

	cells := make([]Cell, 0, len(seeds))
	for i := range seeds {
		si := seeds[i]
		cell := box
		for j := range seeds {
			if j == i {
				continue
			}
			cell = clipHalfPlaneCloserTo(cell, si, seeds[j])
			if len(cell) < 3 {
				break
			}
		}
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
