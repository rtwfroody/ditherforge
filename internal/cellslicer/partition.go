package cellslicer

import (
	"math"
)

// boundaryMark marks an arc-length position on a FootprintLoop.
type boundaryMark struct {
	point   Point2
	edgeIdx int
	edgeT   float32
}

// walkLoopAtCellSize emits marks at uniform arc-length spacing along
// loop. nMarks = round(perim/cellSize); actual spacing is close to
// cellSize but not exact.
func walkLoopAtCellSize(loop *FootprintLoop, cellSize float32) []boundaryMark {
	n := len(loop.Points)
	if n < 3 {
		return nil
	}
	cum := make([]float32, n+1)
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		dx := loop.Points[j][0] - loop.Points[i][0]
		dy := loop.Points[j][1] - loop.Points[i][1]
		cum[i+1] = cum[i] + hypot(dx, dy)
	}
	perim := cum[n]
	nMarks := int(math.Round(float64(perim / cellSize)))
	if nMarks < 1 {
		nMarks = 1
	}
	step := perim / float32(nMarks)
	marks := make([]boundaryMark, nMarks)
	edge := 0
	for i := 0; i < nMarks; i++ {
		target := float32(i) * step
		for edge < n && cum[edge+1] < target {
			edge++
		}
		if edge >= n {
			edge = n - 1
		}
		segLen := cum[edge+1] - cum[edge]
		var t float32
		if segLen > 0 {
			t = (target - cum[edge]) / segLen
		}
		a := loop.Points[edge]
		b := loop.Points[(edge+1)%n]
		marks[i] = boundaryMark{
			point: Point2{
				a[0] + t*(b[0]-a[0]),
				a[1] + t*(b[1]-a[1]),
			},
			edgeIdx: edge,
			edgeT:   t,
		}
	}
	return marks
}

// extractArc returns the polyline along loop from mA to mB in the
// forward (CCW) direction, including endpoints.
func extractArc(loop *FootprintLoop, mA, mB boundaryMark) []Point2 {
	n := len(loop.Points)
	out := []Point2{mA.point}
	if mA.edgeIdx == mB.edgeIdx && mA.edgeT <= mB.edgeT {
		out = append(out, mB.point)
		return out
	}
	cur := (mA.edgeIdx + 1) % n
	for {
		out = append(out, loop.Points[cur])
		if cur == mB.edgeIdx {
			break
		}
		cur = (cur + 1) % n
	}
	out = append(out, mB.point)
	return out
}

// inwardNormal returns the unit normal pointing into the polygon
// interior at mark's edge (assumes CCW outer loop from Clipper non-
// zero union). 90° CCW of the tangent.
func inwardNormal(loop *FootprintLoop, m boundaryMark) [2]float32 {
	n := len(loop.Points)
	a := loop.Points[m.edgeIdx]
	b := loop.Points[(m.edgeIdx+1)%n]
	tx, ty := b[0]-a[0], b[1]-a[1]
	length := hypot(tx, ty)
	if length == 0 {
		return [2]float32{0, 0}
	}
	return [2]float32{-ty / length, tx / length}
}

// GenerateRingCells walks each outer loop of fp at cellSize spacing
// and emits one cell per consecutive pair of marks: the outer arc
// plus two perpendicular chords dropping inward by an overshoot
// depth, then Boolean-clipped to fp so the cell stays inside the
// footprint. For wide regions this yields a cellSize×cellSize ring
// cell; for narrow regions the clip absorbs the excess and gives a
// full-width trapezoid.
func GenerateRingCells(fp *Footprint, cellSize float32) []Cell {
	cells := []Cell{}
	const depthFactor = 3
	depth := depthFactor * cellSize
	for i := range fp.Loops {
		loop := &fp.Loops[i]
		if loop.IsHole {
			continue
		}
		marks := walkLoopAtCellSize(loop, cellSize)
		if len(marks) == 0 {
			continue
		}
		for k := range marks {
			mA := marks[k]
			mB := marks[(k+1)%len(marks)]
			nA := inwardNormal(loop, mA)
			nB := inwardNormal(loop, mB)
			innerB := Point2{
				mB.point[0] + depth*nB[0],
				mB.point[1] + depth*nB[1],
			}
			innerA := Point2{
				mA.point[0] + depth*nA[0],
				mA.point[1] + depth*nA[1],
			}
			arc := extractArc(loop, mA, mB)
			raw := make([]Point2, 0, len(arc)+2)
			raw = append(raw, arc...)
			raw = append(raw, innerB, innerA)
			if len(raw) < 3 {
				continue
			}
			clipped := clipPolygonToFootprint(raw, fp)
			for _, c := range clipped {
				if len(c) >= 3 {
					cells = append(cells, Cell{Outer: c, Kind: KindRing})
				}
			}
		}
	}
	return cells
}

// GenerateHexCells tessellates the inward-offset footprint with
// regular hexagons of seed-to-seed spacing = cellSize. Each hex is
// the regular hexagon of radius cellSize/√3 centered on a seed,
// clipped to the inner footprint. Tiny boundary slivers are left in
// the output; downstream merge handles them.
func GenerateHexCells(inner *Footprint, cellSize float32) []Cell {
	cells := []Cell{}
	if len(inner.Loops) == 0 {
		return cells
	}
	minX, minY, maxX, maxY, _ := inner.Bounds()
	r := cellSize / float32(math.Sqrt(3))
	dx := cellSize
	dy := cellSize * float32(math.Sqrt(3)/2)
	row := 0
	for y := minY; y <= maxY; y += dy {
		offset := float32(0)
		if row%2 == 1 {
			offset = dx / 2
		}
		for x := minX + offset; x <= maxX; x += dx {
			hex := hexagonAt(x, y, r)
			clipped := clipPolygonToFootprint(hex, inner)
			for _, c := range clipped {
				if len(c) >= 3 {
					cells = append(cells, Cell{Outer: c, Kind: KindHex})
				}
			}
		}
		row++
	}
	return cells
}

func hexagonAt(cx, cy, r float32) []Point2 {
	pts := make([]Point2, 6)
	for k := 0; k < 6; k++ {
		angle := math.Pi/6 + float64(k)*math.Pi/3
		pts[k] = Point2{
			cx + r*float32(math.Cos(angle)),
			cy + r*float32(math.Sin(angle)),
		}
	}
	return pts
}

// PartitionSlab partitions a single slab's footprint (derived from
// bot+top loops) into ring + hex cells. Convenience wrapper used
// when slicing is driven by the caller.
func PartitionSlab(bot, top []Loop, cellSize float32) ([]Cell, *Footprint) {
	fp := ComputeFootprint(bot, top)
	inner := OffsetFootprint(fp, -cellSize)
	cells := GenerateRingCells(fp, cellSize)
	cells = append(cells, GenerateHexCells(inner, cellSize)...)
	return cells, fp
}

// ringInsetFrac is the ring-seed inset distance as a fraction of cellSize.
const ringInsetFrac = 0.5

// ringSeeds places the boundary (KindRing) Voronoi seeds for a slab.
//
// The seeds sit on a line inset ringInset = cellSize/2 into the solid,
// rather than on the footprint perimeter: a seed on the boundary gives a
// Voronoi cell straddling fpCur, so the clip to coverTarget keeps only its
// inner ~half and the surviving ring cell is a sliver the slicer drops.
// Insetting lands the whole cell inside, yielding a ~full-size ring cell.
//
// Crucially the spacing is cellSize measured ALONG THE INSET LINE, not along
// the outer perimeter, because that is the line the cell centers actually
// live on. We get this for free by walking the real inset curve (fpCur eroded
// by ringInset). On a convex stretch the inset curve is shorter than the
// perimeter, so it receives fewer, correctly-spaced centers; on a concave
// stretch (hole wall, reflex corner) it is longer and receives more. Walking
// the perimeter and pushing each mark inward — as an earlier version did —
// instead packed convex centers closer than cellSize and spread concave ones
// wider, so cells were not a uniform cellSize "regardless of footprint shape".
//
// Thin-feature fallback: a feature narrower than 2*ringInset = cellSize
// collapses under the erosion, leaving that stretch with no inset curve and
// thus no ring seed. Re-inflating the inset curve by ringInset recovers
// everything the erosion kept (a morphological open of fpCur); whatever of
// fpCur that misses is a thin neck. For perimeter marks there we fall back to
// the inward-pushed point (or the on-perimeter point if even that exits
// fpCur), so the neck still gets seeded. When fpCur is thin everywhere (a
// pure wall slab) the inset curve is empty and this reduces to the old
// perimeter-walk-with-inward-push for the whole footprint.
func ringSeeds(fpCur *Footprint, cellSize float32) []Point2 {
	ringInset := ringInsetFrac * cellSize
	seedLine := OffsetFootprint(fpCur, -ringInset)

	var seeds []Point2
	for i := range seedLine.Loops {
		lp := &seedLine.Loops[i]
		for _, m := range walkLoopAtCellSize(lp, cellSize) {
			seeds = append(seeds, m.point)
		}
	}

	// kept ≈ open(fpCur): the part of fpCur the inset curve represents.
	// Anything outside it is a thin feature that lost its inset curve.
	kept := OffsetFootprint(seedLine, ringInset)
	for i := range fpCur.Loops {
		loop := &fpCur.Loops[i]
		for _, m := range walkLoopAtCellSize(loop, cellSize) {
			// inwardNormal ("left of travel") points into the solid for
			// both CCW outer loops and CW hole loops, so no per-loop sign
			// flip is needed.
			nrm := inwardNormal(loop, m)
			cand := Point2{m.point[0] + ringInset*nrm[0], m.point[1] + ringInset*nrm[1]}
			if kept.Contains(cand[0], cand[1]) {
				continue // fat region: the inset-curve walk already seeded it
			}
			if fpCur.Contains(cand[0], cand[1]) {
				seeds = append(seeds, cand)
			} else {
				seeds = append(seeds, m.point) // inset collapsed: stay on perimeter
			}
		}
	}
	return seeds
}

// minSeedSpacingFrac sets the greedy drop threshold for cap seeds as a
// fraction of cellSize. Two seeds closer than this are not both kept,
// because a Voronoi cell's in-circle radius is half the distance to its
// nearest neighbour — so a nearest-neighbour distance of cellSize is what
// guarantees the cell holds a cellSize-diameter circle. The <1 slack
// absorbs the FP rounding in the inward offsets and the arc-length walk so
// legitimate ~cellSize neighbours (e.g. the first cap ring sitting exactly
// cellSize inside the boundary seeds) are not dropped.
const minSeedSpacingFrac = 0.999

// concentricCapSeeds fills the cap surface (innerCap) with seeds laid on
// curves offset progressively inward from the footprint boundary, so cap
// cells run parallel to the ring cells instead of sitting on an axis-
// aligned hex grid — which removes the visible seam where the ring meets
// the cap (the grid's first row hit the contour-following ring at an
// arbitrary angle and phase).
//
// Each ring k is fpCur eroded by cellSize/2 + k*cellSize, walked at
// cellSize; ring 0 is the boundary seeds themselves (passed in as
// ringSeedPts and pinned). Candidates are accepted outermost-first and any
// candidate closer than ~cellSize to an already-accepted seed is dropped
// (greedy Poisson-disk thinning, via the cellSize spatial hash). That keeps
// every cell big enough for a cellSize-diameter circle and naturally cleans
// up the two crowding spots: the ring/cap interface (thinned against the
// pinned boundary seeds) and the medial axis, where inward rings converge
// and the surplus is dropped, leaving a sparse central spine.
//
// innerCap empty (a buried/wall slab with no visible cap) yields no seeds,
// so the slicer stays surface-only.
func concentricCapSeeds(fpCur, innerCap *Footprint, cellSize float32, ringSeedPts []Point2) []Point2 {
	if innerCap == nil || len(innerCap.Loops) == 0 {
		return nil
	}
	minSpacing := minSeedSpacingFrac * cellSize

	// Greedy min-distance acceptance over the pinned boundary seeds plus the
	// cap seeds accepted so far. Copy ringSeedPts: the grid grows its own
	// backing slice and must not disturb the caller's seed list.
	g := newSeedGrid(append([]Point2(nil), ringSeedPts...), cellSize)

	// The inward offsets terminate when the cap is exhausted (OffsetFootprint
	// returns empty); the bbox-diagonal cap is only a backstop so a
	// pathological non-collapsing offset can't spin forever.
	minX, minY, maxX, maxY, _ := fpCur.Bounds()
	maxRings := int(hypot(maxX-minX, maxY-minY)/cellSize) + 2

	var capSeeds []Point2
	for k := 1; k <= maxRings; k++ {
		d := cellSize/2 + float32(k)*cellSize
		ring := OffsetFootprint(fpCur, -d)
		if len(ring.Loops) == 0 {
			break
		}
		// Thin THIS ring against the boundary seeds and the rings already
		// placed, but not against itself: a ring's own walk sets its spacing
		// (cellSize, with the same arc-length rounding the boundary seeds
		// use), so its points are added to the grid only after the whole ring
		// is processed. Filtering within a ring would let a ring whose walk
		// rounded to just under cellSize decimate itself to every-other-point.
		var ringPts []Point2
		for i := range ring.Loops {
			lp := &ring.Loops[i]
			for _, m := range walkLoopAtCellSize(lp, cellSize) {
				p := m.point
				if !innerCap.Contains(p[0], p[1]) {
					continue // outside the visible cap surface
				}
				if g.hasCloserThan(p, minSpacing) {
					continue // crowds a kept seed: would undersize a cell
				}
				ringPts = append(ringPts, p)
			}
		}
		for _, p := range ringPts {
			g.add(p)
		}
		capSeeds = append(capSeeds, ringPts...)
	}
	return capSeeds
}

// PartitionSlabAnalytic partitions a slab's footprint into compact
// boundary + hex cells using exact Clipper polygon booleans, with no
// raster round-trip. Every cell is sized so a cellSize-diameter circle
// fits inside it (inradius ≈ cellSize/2) with minimal extra — no long-
// skinny cells, no overlaps.
//
// Region algebra (all Clipper set ops on the slab footprints):
//
//	inner    = fpCur shrunk inward by cellSize.
//	innerCap = inner minus the region covered by BOTH neighbours.
//	           That leaves only cap surface — the model's top/bottom
//	           or an interior horizontal feature. Pure wall slabs
//	           have empty innerCap (the interior is hidden between
//	           neighbours), so they produce no interior hex cells.
//	band     = fpCur minus inner — the cellSize-wide ring along every
//	           footprint loop (outer AND hole). This is the lateral
//	           (wall) surface; its width is exactly one cell, so the
//	           wall slab's deep interior is left uncovered on purpose
//	           (surface-only — interior cells would leak error into
//	           invisible volume). Angled/bulging walls are handled
//	           upstream by fpCur being the in-band surface SILHOUETTE
//	           (the XY projection of the surface clipped to the slab),
//	           not just the two bounding-plane contours — so the band
//	           already reaches the surface's true outermost extent.
//
// band and innerCap are disjoint and meet cleanly along `inner`.
//
// fpCur is the COVERAGE footprint (the in-band silhouette) and drives
// inner/band/seeds — what the cells must tile. fpBelow/fpAbove are the
// neighbours' BOUNDING-PLANE footprints (zBot/zTop contours), used only
// for the buried-wall test: a column is buried (no cap) iff solid both
// just below and just above, which is a question about the cross-sections
// AT the planes. Passing the silhouette there instead would let a
// neighbour's mid-slab wall bulge read as "buried" and suppress real
// caps, so the caller deliberately feeds the plane footprints. (inner
// staying silhouette-based is safe: erosion by cellSize removes any
// sub-cellSize bulge, so innerCap is unchanged in practice.)
//
// Boundary cells are the clipped Voronoi diagram of cellSize-spaced
// seeds along the footprint loops, restricted to band (voronoiBandCells)
// — compact, non-overlapping, and contiguous, replacing the old depth-3
// ring trapezoids that fanned out and overlapped at convex corners. The
// interior is the raw hex lattice clipped to innerCap; a triangular
// lattice's Voronoi is itself the hex tiling, so the two cell families
// follow the same "cellSize circle fits inside" rule. Each Clipper
// intersection may return several disjoint polygons (e.g. a hexagon
// pinched by a concave footprint); each becomes its own cell. Empty
// intersections are never emitted.
//
// Pass nil for either neighbour at the top/bottom of the model.
// slabCoverRegions computes the surface-shell region algebra shared by
// the plain and colour-aware partitioners. Both must derive the exposed
// cap and the cell-tiling cover target identically, or the colour path
// would tile a different shell than PartitionSlabAnalytic and break its
// byte-identical no-cut fallback — so this is the single source of truth.
//
//   - innerCap: interior surface left exposed where the neighbours' caps
//     don't both cover it (empty for a pure-wall slab).
//   - coverTarget: the shell the cells tile = lateral band ∪ innerCap.
func slabCoverRegions(fpCur, fpBelow, fpAbove *Footprint, cellSize float32) (innerCap, coverTarget *Footprint) {
	inner := OffsetFootprint(fpCur, -cellSize)
	neighborBoth := FootprintIntersect(fpBelow, fpAbove)
	innerCap = FootprintDifference(inner, neighborBoth)
	band := FootprintDifference(fpCur, inner)
	coverTarget = FootprintUnion(band, innerCap)
	return innerCap, coverTarget
}

func PartitionSlabAnalytic(fpCur, fpBelow, fpAbove *Footprint, cellSize float32) ([]Cell, *Footprint, PartitionStats) {
	var stats PartitionStats
	if fpCur == nil || len(fpCur.Loops) == 0 {
		return nil, nil, stats
	}
	// innerCap and coverTarget (band ∪ cap). coverTarget is stored on the
	// Slab so diagnostics can measure coverage against the region cells
	// actually fill, not the full footprint.
	innerCap, coverTarget := slabCoverRegions(fpCur, fpBelow, fpAbove, cellSize)

	// Pixels is a diagnostic only (run.go's partition histogram); the
	// raster path counted real pixels at pxSize = cellSize/4, so report
	// the polygon area in those same pixel units to keep the histogram
	// comparable.
	pxArea := (cellSize / 4) * (cellSize / 4)

	// A single Voronoi diagram tiles the whole surface shell. Seeds come
	// from two families, concatenated and partitioned by one diagram so
	// boundary and interior cells meet along clean shared bisectors
	// instead of the arbitrary clip seam that the old separate ring-band
	// and hex-cap passes produced:
	//
	//   - Boundary (KindRing): every footprint loop (outer AND hole)
	//     walked at cellSize spacing. Voronoi needs no inward-normal
	//     special-casing for holes — a hole-loop seed's cell simply fills
	//     its share of the band.
	//   - Interior (KindHex): the cellSize hex lattice inside innerCap. A
	//     hex lattice's Voronoi is the regular hex tiling, so the interior
	//     reproduces the old hexagons; empty innerCap (a pure wall slab)
	//     means no interior seeds, so the deep interior stays uncovered
	//     (surface-only).
	//
	// Every cell is clipped to coverTarget (band ∪ innerCap), not to band
	// and innerCap separately, so the diagram tiles the shell exactly with
	// no gap or overlap at the boundary/interior interface.
	seeds := ringSeeds(fpCur, cellSize)
	stats.RawRing = len(seeds)

	interior := concentricCapSeeds(fpCur, innerCap, cellSize, seeds)
	stats.RawHex = len(interior)

	kinds := make([]CellKind, 0, len(seeds)+len(interior))
	for range seeds {
		kinds = append(kinds, KindRing)
	}
	for range interior {
		kinds = append(kinds, KindHex)
	}
	seeds = append(seeds, interior...)

	cells := voronoiCells(seeds, kinds, coverTarget, cellSize, pxArea)
	stats.Final = len(cells)

	// Tag outer-perimeter edges so the per-cell prism clip can open-end
	// there (see Cell.OuterEdgeOpen). Same call the raster path made.
	MarkOuterEdges(cells, fpCur)
	return cells, coverTarget, stats
}

// PartitionStats reports diagnostic counters from one
// PartitionSlabAnalytic call. Aggregated across slabs by the pipeline
// driver. RawRing/RawHex are the pre-clip generator output; Final is
// the surviving cell count after clipping each raw cell to its region
// (empty intersections are never emitted, so Final <= RawRing+RawHex).
type PartitionStats struct {
	RawRing int // ring cells generated before clipping to the ring region
	RawHex  int // hex cells generated before clipping to the cap region
	Final   int // cells returned to the caller
}

// MarkOuterEdges populates each cell's OuterEdgeOpen field by
// scanning the slab's cells for shared directed half-edges. An edge
// is "outer" iff:
//
//  1. No other cell in the slab owns its reverse half-edge.
//  2. The half-space immediately outside the edge is outside fp
//     (the slab footprint). Without this guard a true partition gap
//     would let two cells facing the gap both mark their gap-side
//     edges outer; clip-time open-ending would then double-claim
//     geometry inside the gap and produce non-manifold faces.
//
// Vertex equality is on the 1µm Clipper-integer bucket (int2DOf), so
// two cells' independently-rounded shared corners match.
//
// fp may be nil; the safety check is then disabled (rule 2 always
// passes). That keeps the function usable from test fixtures that
// don't carry a real footprint.
//
// O(Σ |cell.Outer|) for the edge-map build, plus one Footprint.Contains
// per candidate-outer edge for the safety check.
//
// TODO: AddWithinSlabAdjacency in pipeline/run.go also walks cell-Outer
// edges to build cell-cell adjacency. Worth folding the two passes into
// one once we're confident the open-ended behaviour stays — would save
// one full pass over the same data.
func MarkOuterEdges(cells []Cell, fp *Footprint) {
	edges := make(map[dirEdge]struct{}, len(cells)*8)
	for ci := range cells {
		outer := cells[ci].Outer
		n := len(outer)
		for k := 0; k < n; k++ {
			edges[dirEdgeOf(outer[k], outer[(k+1)%n])] = struct{}{}
		}
	}
	for ci := range cells {
		outer := cells[ci].Outer
		n := len(outer)
		if n == 0 {
			continue
		}
		flags := make([]bool, n)
		for k := 0; k < n; k++ {
			a, b := outer[k], outer[(k+1)%n]
			if _, hasMate := edges[dirEdgeOf(a, b).reverse()]; hasMate {
				continue
			}
			if fp != nil && insideFootprintOnOuterSide(fp, a, b) {
				continue
			}
			flags[k] = true
		}
		cells[ci].OuterEdgeOpen = flags
	}
}

// insideFootprintOnOuterSide reports whether the half-space
// immediately outside edge a→b (assuming a CCW outer polygon — outward
// is the right-hand side of the edge direction) lies inside fp. Used
// by MarkOuterEdges to distinguish "edge faces the partition's outer
// boundary" (outside fp → safe to open-end) from "edge faces a gap
// inside the partition" (inside fp → not safe to open-end, another
// cell would double-claim).
//
// Probes 1µm out from the edge midpoint along the outward normal.
// Returns false for zero-length edges (defensive — should never
// happen on a real cell.Outer).
func insideFootprintOnOuterSide(fp *Footprint, a, b Point2) bool {
	dx, dy := b[0]-a[0], b[1]-a[1]
	length2 := dx*dx + dy*dy
	if length2 == 0 {
		// Zero-length edge — treat as "inside fp" so the caller
		// keeps the edge clipping rather than tagging it as a
		// past-partition opening. Real partitions don't produce
		// zero-length cell-Outer edges; this branch is paranoia
		// against future raster simplification regressions.
		return true
	}
	length := float32(math.Sqrt(float64(length2)))
	// CCW polygon → interior on the left of edge direction →
	// outward is the right (perpendicular rotated 90° CW from
	// edge direction).
	nx, ny := dy/length, -dx/length
	midX, midY := (a[0]+b[0])/2, (a[1]+b[1])/2
	// Probe ≫ int2D bucket size (1µm @ clipperScale=1000) to clear
	// bucket-grid noise but well inside the smallest pxSize cells
	// (~125µm) so we stay clear of legitimate neighbour territory.
	const probeMM = float32(0.01)
	return fp.Contains(midX+probeMM*nx, midY+probeMM*ny)
}
