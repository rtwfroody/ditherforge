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
func PartitionSlabRaster(fpCur, fpBelow, fpAbove *Footprint, cellSize, pxSize float32) ([]Cell, *SlabRaster) {
	if fpCur == nil || len(fpCur.Loops) == 0 {
		return nil, nil
	}
	inner := OffsetFootprint(fpCur, -cellSize)
	if pxSize <= 0 {
		pxSize = cellSize / 4
	}
	minX, minY, maxX, maxY, ok := fpCur.Bounds()
	if !ok {
		return nil, nil
	}
	margin := pxSize
	minX -= margin
	minY -= margin
	maxX += margin
	maxY += margin
	w := intCeil((maxX - minX) / pxSize)
	h := intCeil((maxY - minY) / pxSize)
	if w < 1 || h < 1 {
		return nil, nil
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

	// Count pixels per raw cell. Cells with zero pixels are dropped
	// — they're slivers smaller than a pixel, identical to what
	// Clipper would have rejected as empty intersections.
	counts := CellPixelCounts(r, len(rawCells))
	remap := make([]int32, len(rawCells))
	denseN := int32(0)
	for ci, c := range counts {
		if c == 0 {
			remap[ci] = NoCellID
			continue
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
		cells = append(cells, Cell{Outer: outline, Kind: rawCells[ci].Kind})
	}
	return cells, r
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
