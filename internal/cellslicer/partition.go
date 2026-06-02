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
func PartitionSlabAnalytic(fpCur, fpBelow, fpAbove *Footprint, cellSize float32) ([]Cell, *Footprint, PartitionStats) {
	var stats PartitionStats
	if fpCur == nil || len(fpCur.Loops) == 0 {
		return nil, nil, stats
	}
	inner := OffsetFootprint(fpCur, -cellSize)
	neighborBoth := FootprintIntersect(fpBelow, fpAbove)
	innerCap := FootprintDifference(inner, neighborBoth)
	band := FootprintDifference(fpCur, inner)
	// The surface shell the emitted cells tile: lateral band ∪ cap region.
	// Stored on the Slab so diagnostics can measure coverage against the
	// region cells are actually meant to fill, not the full footprint.
	coverTarget := FootprintUnion(band, innerCap)

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
	var seeds []Point2
	for i := range fpCur.Loops {
		for _, m := range walkLoopAtCellSize(&fpCur.Loops[i], cellSize) {
			seeds = append(seeds, m.point)
		}
	}
	stats.RawRing = len(seeds)

	interior := hexLatticeSeeds(innerCap, cellSize)
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
