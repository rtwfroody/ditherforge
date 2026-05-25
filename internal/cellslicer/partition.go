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

// generateRingCellsRaw mirrors GenerateRingCells but emits the
// unclipped trapezoidal cell polygons directly — no Clipper-clip
// against the footprint. The footprint mask is applied later by
// the rasteriser, so the per-cell Clipper call goes away entirely.
func generateRingCellsRaw(fp *Footprint, cellSize float32) []Cell {
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
			cells = append(cells, Cell{Outer: raw, Kind: KindRing})
		}
	}
	return cells
}

// generateHexCellsRaw mirrors GenerateHexCells but emits the
// unclipped regular hexagons directly — no Clipper-clip against
// the inner footprint. Hexes whose centres fall outside the inner
// footprint are still emitted (the rasteriser drops them naturally
// when none of their pixels are in the outer footprint mask).
func generateHexCellsRaw(inner *Footprint, cellSize float32) []Cell {
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
			cells = append(cells, Cell{Outer: hexagonAt(x, y, r), Kind: KindHex})
		}
		row++
	}
	return cells
}

// PartitionSlabRaster is the raster-first partition pass. It needs
// fpCur for the slab itself plus fpBelow / fpAbove for neighbouring
// slabs so it can compute cap masks; pass nil for either neighbour
// at the top/bottom of the model.
//
// Cell generation:
//
//   - Ring cells along the boundary band (raw trapezoids stamped
//     into the inFootprint mask), as before.
//   - Hex cells gated by capMask = (cap_top ∪ cap_bottom) ∩ inner,
//     where cap_top = inFootprint − inAbove and cap_bottom =
//     inFootprint − inBelow. Pure wall slabs have empty capMask and
//     produce no interior cells; cap slabs (top/bottom of model
//     or interior horizontal feature) get a full hex tiling over
//     the cap region.
//
// Each surviving cell's Outer is recovered via marching squares on
// the cellID grid. The dense raster is dropped on return — callers
// see polygons only.
// PartitionStats reports diagnostic counters from one
// PartitionSlabRaster call so callers can spot cells lost to slivers,
// disconnected-component splits, or pixel-count starvation. Aggregated
// across slabs by the pipeline driver.
type PartitionStats struct {
	RawRing       int    // ring cells generated by generateRingCellsRaw
	RawHex        int    // hex cells generated by generateHexCellsRaw
	SplitAdded    int    // extra cells emitted by splitDisconnectedCells
	DroppedZeroPx int    // cells dropped because their pixel count was 0
	Final         int    // surviving cells returned to the caller
	PxHist        [5]int // pixel-count histogram: [1, 2-4, 5-16, 17-64, 65+]
}

func PartitionSlabRaster(fpCur, fpBelow, fpAbove *Footprint, cellSize, pxSize float32) ([]Cell, *SlabRaster, PartitionStats) {
	var stats PartitionStats
	if fpCur == nil || len(fpCur.Loops) == 0 {
		return nil, nil, stats
	}
	inner := OffsetFootprint(fpCur, -cellSize)
	if pxSize <= 0 {
		pxSize = cellSize / 4
	}
	minX, minY, maxX, maxY, ok := fpCur.Bounds()
	if !ok {
		return nil, nil, stats
	}
	margin := pxSize
	minX -= margin
	minY -= margin
	maxX += margin
	maxY += margin
	w := intCeil((maxX - minX) / pxSize)
	h := intCeil((maxY - minY) / pxSize)
	if w < 1 || h < 1 {
		return nil, nil, stats
	}
	r := &SlabRaster{
		OriginX:     minX,
		OriginY:     minY,
		PxSize:      pxSize,
		W:           w,
		H:           h,
		InFootprint: make([]uint64, BitsForPixels(w, h)),
		CellID:      make([]int32, w*h),
	}
	RasterizeFootprint(fpCur, minX, minY, pxSize, w, h, r.InFootprint)
	for i := range r.CellID {
		r.CellID[i] = NoCellID
	}

	// Inner footprint mask. Used both to compute the cap-and-
	// interior region and (when no cap exists) as the historical
	// hex-stamping gate.
	wordCount := BitsForPixels(w, h)
	var innerMask []uint64
	if inner != nil && len(inner.Loops) > 0 {
		innerMask = make([]uint64, wordCount)
		RasterizeFootprint(inner, minX, minY, pxSize, w, h, innerMask)
	}

	// Neighbour footprints → cap masks. cap_top = inFootprint
	// minus the slab-above footprint; cap_bottom likewise. Open
	// boundaries (no slab above/below) contribute the entire
	// inFootprint as a cap, so the model's top and bottom slabs
	// get full interior coverage automatically.
	belowMask := neighborMaskOrNil(fpBelow, minX, minY, pxSize, w, h)
	aboveMask := neighborMaskOrNil(fpAbove, minX, minY, pxSize, w, h)
	capMask := make([]uint64, wordCount)
	if innerMask != nil {
		copy(capMask, innerMask)
	} else {
		copy(capMask, r.InFootprint)
	}
	// capMask &= ((inFootprint &^ belowMask) | (inFootprint &^ aboveMask))
	// i.e. keep only inner pixels that have either no slab below
	// them or no slab above them at the same (x, y).
	for i := 0; i < wordCount; i++ {
		inFp := r.InFootprint[i]
		below := uint64(0)
		if belowMask != nil {
			below = belowMask[i]
		}
		above := uint64(0)
		if aboveMask != nil {
			above = aboveMask[i]
		}
		capMask[i] &= (inFp &^ below) | (inFp &^ above)
	}
	// Hex stamping gate. If there's no inner region at all (a very
	// thin footprint) we fall back to the outer mask so cell
	// generation doesn't silently empty out.
	hexGate := capMask
	if innerMask == nil {
		hexGate = r.InFootprint
	}

	rawRing := generateRingCellsRaw(fpCur, cellSize)
	var rawHex []Cell
	if inner != nil {
		rawHex = generateHexCellsRaw(inner, cellSize)
	}
	stats.RawRing = len(rawRing)
	stats.RawHex = len(rawHex)
	// Hex-first / ring-second so hex owns the cap interior; ring
	// then fills the boundary band that hex didn't claim.
	rawCells := make([]Cell, 0, len(rawHex)+len(rawRing))
	rawCells = append(rawCells, rawHex...)
	rawCells = append(rawCells, rawRing...)
	for ci := range rawHex {
		StampCellByPolygonMasked(rawCells[ci].Outer, int32(ci), r, hexGate)
	}
	hexOffset := len(rawHex)
	for k := range rawRing {
		ci := hexOffset + k
		StampCellByPolygon(rawCells[ci].Outer, int32(ci), r)
	}

	// Recover pixels lost to the conservative-rasterisation ribbon
	// at the inner cap boundary. See [[backfillAnalyticCap]].
	backfillAnalyticCap(r, fpCur, fpAbove, fpBelow, rawCells, aboveMask, belowMask)

	// Backfill any in-footprint pixel that no cell polygon claimed
	// to a 4-connected neighbour's cellID. The pixel-centre point-
	// in-polygon test in StampCellByPolygon misses pixels whose
	// centres fall just outside the smooth fp boundary even though
	// the pixel's interior overlaps fp — RasterizeFootprint marks
	// those pixels in (conservative-overlap rasterisation), but no
	// trapezoid/hex contains the pixel centre, so the pixel stays
	// unassigned and any source-triangle surface there gets dropped
	// at clip time (visible as thin tangent-to-silhouette gaps on
	// curved models). Backfill propagates ownership outward until
	// every in-footprint pixel belongs to a cell.
	backfillUnassigned(r)

	// Split any cell whose pixel ownership ended up in multiple
	// disconnected components into one cell per component. Without
	// this, CellOutlineFromRaster only returns the outline of the
	// first walked loop, leaving the other component's pixels
	// invisible to downstream renderers and to clip2d's per-cell
	// polygon — manifesting as red coverage gaps in capMask on
	// near-pole slabs of curved models.
	beforeSplit := len(rawCells)
	rawCells = splitDisconnectedCells(r, rawCells)
	stats.SplitAdded = len(rawCells) - beforeSplit

	// Count pixels per raw cell. Cells with zero pixels are dropped
	// — they're slivers smaller than a pixel, identical to what
	// Clipper would have rejected as empty intersections.
	counts := CellPixelCounts(r, len(rawCells))
	remap := make([]int32, len(rawCells))
	denseN := int32(0)
	for ci, c := range counts {
		if c == 0 {
			remap[ci] = NoCellID
			stats.DroppedZeroPx++
			continue
		}
		switch {
		case c == 1:
			stats.PxHist[0]++
		case c <= 4:
			stats.PxHist[1]++
		case c <= 16:
			stats.PxHist[2]++
		case c <= 64:
			stats.PxHist[3]++
		default:
			stats.PxHist[4]++
		}
		remap[ci] = denseN
		denseN++
	}
	// Rewrite r.CellID to use dense indices.
	for i, id := range r.CellID {
		if id < 0 {
			continue
		}
		r.CellID[i] = remap[id]
	}
	// Build dense cell list, recover Outer per cell from raster.
	cells := make([]Cell, 0, denseN)
	// Build per-cell bbox in pixels to bound marching squares scope.
	type bbox struct {
		minX, minY, maxX, maxY int
	}
	bboxes := make([]bbox, denseN)
	for i := range bboxes {
		bboxes[i] = bbox{r.W, r.H, -1, -1}
	}
	for py := 0; py < r.H; py++ {
		rowBase := py * r.W
		for px := 0; px < r.W; px++ {
			id := r.CellID[rowBase+px]
			if id < 0 {
				continue
			}
			b := &bboxes[id]
			if px < b.minX {
				b.minX = px
			}
			if py < b.minY {
				b.minY = py
			}
			if px > b.maxX {
				b.maxX = px
			}
			if py > b.maxY {
				b.maxY = py
			}
		}
	}
	for ci := range rawCells {
		newIdx := remap[ci]
		if newIdx < 0 {
			continue
		}
		b := bboxes[newIdx]
		outline := CellOutlineFromRaster(r, newIdx, b.minX, b.minY, b.maxX, b.maxY)
		if len(outline) < 3 {
			continue
		}
		cells = append(cells, Cell{Outer: outline, Kind: rawCells[ci].Kind, Pixels: int(counts[ci])})
	}
	stats.Final = len(cells)
	// Tag each cell's outer-boundary edges so the cell-prism clip can
	// run open-ended on the partition's outer perimeter. See Cell's
	// OuterEdgeOnBoundary field doc for the why.
	MarkOuterEdges(cells, fpCur)
	return cells, r, stats
}

// MarkOuterEdges populates each cell's OuterEdgeOnBoundary field by
// scanning the slab's cells for shared directed half-edges. An edge
// is "outer" iff:
//
//	1. No other cell in the slab owns its reverse half-edge.
//	2. The half-space immediately outside the edge is outside fp
//	   (the slab footprint). Without this guard a true partition gap
//	   would let two cells facing the gap both mark their gap-side
//	   edges outer; clip-time open-ending would then double-claim
//	   geometry inside the gap and produce non-manifold faces.
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
	type edgeKey struct{ a, b int2D }
	edges := make(map[edgeKey]struct{}, len(cells)*8)
	for ci := range cells {
		outer := cells[ci].Outer
		n := len(outer)
		for k := 0; k < n; k++ {
			edges[edgeKey{int2DOf(outer[k]), int2DOf(outer[(k+1)%n])}] = struct{}{}
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
			if _, hasMate := edges[edgeKey{int2DOf(b), int2DOf(a)}]; hasMate {
				continue
			}
			if fp != nil && insideFootprintOnOuterSide(fp, a, b) {
				continue
			}
			flags[k] = true
		}
		cells[ci].OuterEdgeOnBoundary = flags
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
		return false
	}
	length := float32(math.Sqrt(float64(length2)))
	// CCW polygon → interior on the left of edge direction →
	// outward is the right (perpendicular rotated 90° CW from
	// edge direction).
	nx, ny := dy/length, -dx/length
	midX, midY := (a[0]+b[0])/2, (a[1]+b[1])/2
	const probeMM = float32(0.001) // 1µm — clear of float-precision noise at the edge
	return fp.Contains(midX+probeMM*nx, midY+probeMM*ny)
}

// splitDisconnectedCells finds cells in r.CellID whose pixel sets
// form more than one 4-connected component. The first component
// (the one containing the leftmost-bottommost pixel, matching
// CellOutlineFromRaster's start-corner pick) keeps the original
// cell ID; each subsequent component gets a fresh ID appended to
// rawCells. The returned rawCells slice is the input extended by
// one placeholder Cell per new component (Kind copied from the
// donor cell; Outer is unused — outline gets recovered from the
// raster downstream).
func splitDisconnectedCells(r *SlabRaster, rawCells []Cell) []Cell {
	visited := make([]bool, r.W*r.H)
	seen := make([]bool, len(rawCells))
	for i := 0; i < r.W*r.H; i++ {
		id := r.CellID[i]
		if id < 0 || visited[i] {
			continue
		}
		px0 := i % r.W
		py0 := i / r.W
		var assignID int32
		if int(id) < len(seen) && !seen[id] {
			seen[id] = true
			assignID = id
		} else {
			assignID = int32(len(rawCells))
			rawCells = append(rawCells, Cell{Kind: rawCells[id].Kind})
		}
		stack := [][2]int{{px0, py0}}
		visited[i] = true
		if assignID != id {
			r.CellID[i] = assignID
		}
		for len(stack) > 0 {
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := top[0]+d[0], top[1]+d[1]
				if nx < 0 || ny < 0 || nx >= r.W || ny >= r.H {
					continue
				}
				nidx := ny*r.W + nx
				if visited[nidx] || r.CellID[nidx] != id {
					continue
				}
				visited[nidx] = true
				if assignID != id {
					r.CellID[nidx] = assignID
				}
				stack = append(stack, [2]int{nx, ny})
			}
		}
	}
	return rawCells
}

// backfillAnalyticCap fills any unowned pixel whose centre lies in
// the analytic cap region — inside fpCur and outside both fpAbove
// and fpBelow (each tested by pixel-centre point-in-polygon, ignoring
// the rasterised masks) — from a 4-connected hex-cell neighbour.
//
// Why only hex neighbours: conservative rasterisation of fpAbove
// over-claims pixels straddling the polygon's edge, so the cap mask
// loses a one-pixel-wide ribbon at the inner cap boundary. Hex
// cells (which stamp through the cap mask) cover the cap interior
// up to that lost ribbon; the analytic-cap pixels we want to recover
// are the ones immediately outside the rasterised cap interior,
// adjacent to a hex cell whose colour was sampled from the cap
// surface. Filling from ring cells instead would pull wall-band
// colours into the cap on slabs where ring polygons abut a different
// surface (e.g. a building's recessed sub-roof, where the wall
// cells along the building's perimeter are nearer to the recess's
// fpAbove-edge ribbon than any hex cell is).
func backfillAnalyticCap(r *SlabRaster, fpCur, fpAbove, fpBelow *Footprint, rawCells []Cell, aboveMask, belowMask []uint64) {
	if fpCur == nil {
		return
	}
	maskHit := func(mask []uint64, px, py int) bool {
		if mask == nil || px < 0 || py < 0 || px >= r.W || py >= r.H {
			return false
		}
		idx := py*r.W + px
		return mask[idx>>6]&(uint64(1)<<uint(idx&63)) != 0
	}
	// adjacentToNeighbour confines the fill to the polygon-edge
	// ribbon of a rasterised neighbour. On open boundaries (top or
	// bottom of the model) both masks are nil and this predicate is
	// uniformly false — no ribbon to recover, which is correct
	// since the open boundary's cap is the entire footprint and was
	// already stamped by hex.
	adjacentToNeighbour := func(px, py int) bool {
		for _, d := range [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
			nx, ny := px+d[0], py+d[1]
			if maskHit(aboveMask, nx, ny) || maskHit(belowMask, nx, ny) {
				return true
			}
		}
		return false
	}
	// A pixel is in the analytic cap if *any part* of its square
	// extent is in the cap region. A point is in cap iff it lies
	// in fpCur and is uncapped by at least one of fpAbove/fpBelow
	// — matching the slab-level capMask definition
	// `inFp & ~(above & below)` applied at a single point. We
	// approximate "any part overlaps" by sampling 5 points: the
	// pixel centre plus its 4 corners. This is symmetric with the
	// OUTER fp's conservative any-overlap rasterisation. Without
	// it the cap mask is over-eroded along the neighbour's polygon
	// edge by up to a pixel-diagonal, leaving a sawtooth ribbon of
	// unowned "missing geometry" pixels visible as polar rings
	// near the apex of curved models.
	half := r.PxSize / 2
	inCap := func(cx, cy float32) bool {
		points := [5][2]float32{
			{cx, cy},
			{cx - half, cy - half},
			{cx + half, cy - half},
			{cx - half, cy + half},
			{cx + half, cy + half},
		}
		for _, p := range points {
			if !fpCur.Contains(p[0], p[1]) {
				continue
			}
			if fpAbove != nil && fpAbove.Contains(p[0], p[1]) {
				if fpBelow == nil || fpBelow.Contains(p[0], p[1]) {
					continue
				}
			}
			return true
		}
		return false
	}
	pickHexNeighbour := func(px, py int) int32 {
		for _, d := range [4][2]int{{0, -1}, {0, 1}, {-1, 0}, {1, 0}} {
			nx, ny := px+d[0], py+d[1]
			if nx < 0 || ny < 0 || nx >= r.W || ny >= r.H {
				continue
			}
			v := r.CellID[ny*r.W+nx]
			if v >= 0 && int(v) < len(rawCells) && rawCells[v].Kind == KindHex {
				return v
			}
		}
		return NoCellID
	}
	for py := 0; py < r.H; py++ {
		y := r.OriginY + (float32(py)+0.5)*r.PxSize
		for px := 0; px < r.W; px++ {
			idx := py*r.W + px
			if r.CellID[idx] != NoCellID || !r.PixelInFootprint(px, py) {
				continue
			}
			x := r.OriginX + (float32(px)+0.5)*r.PxSize
			if !inCap(x, y) || !adjacentToNeighbour(px, py) {
				continue
			}
			if v := pickHexNeighbour(px, py); v >= 0 {
				r.CellID[idx] = v
			}
		}
	}
}

// backfillUnassigned assigns any unowned in-footprint pixel that
// sits on the footprint boundary (has a non-footprint 4-neighbour)
// to a 4-connected in-cell neighbour. Interior pixels (no non-fp
// neighbour) stay unassigned — cellslicer cells must be surface-
// only, and surface only lives along the fp boundary for wall
// slabs. Without restricting to boundary pixels, the backfill
// would spread cells through the entire footprint interior,
// leaking visible-surface dither error into invisible volume.
//
// The backfill targets the failure mode of conservative
// RasterizeFootprint: it includes pixels whose centres fall just
// outside the smooth fp boundary, but the ring/hex stamps (which
// use pixel-centre point-in-polygon against the smooth-edged
// polygons) don't claim those pixels. Those pixels carry source-
// triangle surface, so leaving them unowned drops the surface
// at clip time — the visible "thin tangent gap" on curved silhouettes.
func backfillUnassigned(r *SlabRaster) {
	isBoundary := func(px, py int) bool {
		if py > 0 && !r.PixelInFootprint(px, py-1) {
			return true
		}
		if py < r.H-1 && !r.PixelInFootprint(px, py+1) {
			return true
		}
		if px > 0 && !r.PixelInFootprint(px-1, py) {
			return true
		}
		if px < r.W-1 && !r.PixelInFootprint(px+1, py) {
			return true
		}
		return px == 0 || px == r.W-1 || py == 0 || py == r.H-1
	}
	pickNeighbour := func(px, py int) int32 {
		if py > 0 {
			if v := r.CellID[(py-1)*r.W+px]; v >= 0 {
				return v
			}
		}
		if py < r.H-1 {
			if v := r.CellID[(py+1)*r.W+px]; v >= 0 {
				return v
			}
		}
		if px > 0 {
			if v := r.CellID[py*r.W+px-1]; v >= 0 {
				return v
			}
		}
		if px < r.W-1 {
			if v := r.CellID[py*r.W+px+1]; v >= 0 {
				return v
			}
		}
		return NoCellID
	}
	// Single pass: each unowned boundary pixel claims any assigned
	// 4-neighbour. The conservative-rasterisation gap is at most one
	// pixel wide at the fp boundary, so one pass suffices.
	for py := 0; py < r.H; py++ {
		for px := 0; px < r.W; px++ {
			idx := py*r.W + px
			if r.CellID[idx] != NoCellID {
				continue
			}
			if !r.PixelInFootprint(px, py) {
				continue
			}
			if !isBoundary(px, py) {
				continue
			}
			if v := pickNeighbour(px, py); v >= 0 {
				r.CellID[idx] = v
			}
		}
	}
}

// neighborMaskOrNil rasterises a neighbouring slab's footprint into
// the same (origin, pxSize, w, h) frame as the slab being
// partitioned, or returns nil if the neighbour is absent (top /
// bottom of the model) — a nil mask is treated as the empty set by
// the cap-mask combinator.
func neighborMaskOrNil(fp *Footprint, originX, originY, pxSize float32, w, h int) []uint64 {
	if fp == nil || len(fp.Loops) == 0 {
		return nil
	}
	mask := make([]uint64, BitsForPixels(w, h))
	RasterizeFootprint(fp, originX, originY, pxSize, w, h, mask)
	return mask
}
