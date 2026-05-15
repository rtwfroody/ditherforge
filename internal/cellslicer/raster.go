package cellslicer

import "sort"

// NoCellID is the sentinel CellID for pixels outside the footprint
// (or in-footprint pixels that no cell stamps onto — gaps).
const NoCellID = int32(-1)

// SlabRaster is the pixel-grid representation of one slab's cell
// partition.
//
//   - PxSize × PxSize world-space mm per pixel.
//   - (OriginX, OriginY) is the world-space coordinate of pixel
//     (0, 0)'s lower-left corner; pixel (px, py)'s center is at
//     (OriginX + (px+0.5)*PxSize, OriginY + (py+0.5)*PxSize).
//   - InFootprint is a bitmap with one bit per pixel (row-major,
//     little-endian within each uint64). Bit set ⇔ the pixel's
//     centre is inside the slab footprint.
//   - CellID is parallel to InFootprint at one int32 per pixel.
//     Pixels outside the footprint MUST be NoCellID. In-footprint
//     pixels carry the index of the cell that owns them, in the
//     same indexing as Slab.Cells.
//
// The raster lets us derive cell area, centroid, within-slab
// adjacency, and (eventually) cross-slab adjacency without exact
// polygon clipping. Each cell's polygon footprint becomes a derived
// quantity — recovered via marching squares when downstream stages
// need it.
type SlabRaster struct {
	OriginX, OriginY float32
	PxSize           float32
	W, H             int
	InFootprint      []uint64 // ceil(W*H / 64) words; bit (py*W+px)
	CellID           []int32  // length W*H; NoCellID outside footprint
}

// BitsForPixels returns the number of uint64 words needed to hold a
// W×H pixel bitmap.
func BitsForPixels(w, h int) int {
	return (w*h + 63) >> 6
}

// PixelInFootprint reports whether the (px, py) pixel is inside the
// rasterised footprint.
func (r *SlabRaster) PixelInFootprint(px, py int) bool {
	if px < 0 || py < 0 || px >= r.W || py >= r.H {
		return false
	}
	idx := py*r.W + px
	return r.InFootprint[idx>>6]&(uint64(1)<<uint(idx&63)) != 0
}

// PixelCenter returns the world-space (x, y) centre of pixel (px, py).
func (r *SlabRaster) PixelCenter(px, py int) (float32, float32) {
	return r.OriginX + (float32(px)+0.5)*r.PxSize,
		r.OriginY + (float32(py)+0.5)*r.PxSize
}

// RasterizeFootprint scan-converts fp into the destination bitmap.
// Each pixel whose centre lies inside fp (odd number of containing
// loops by even-odd fill) gets its bit set.
//
// inFootprint must already be sized to BitsForPixels(w, h); it is
// fully overwritten — cleared on entry, set per-scanline. Loops are
// treated as closed and their winding is ignored: even-odd handles
// holes regardless of orientation.
func RasterizeFootprint(fp *Footprint, originX, originY, pxSize float32, w, h int, inFootprint []uint64) {
	for i := range inFootprint {
		inFootprint[i] = 0
	}
	if fp == nil || w <= 0 || h <= 0 || pxSize <= 0 {
		return
	}
	// Working buffer reused across scanlines; capacity grows to
	// fit the worst-case intersection count.
	xs := make([]float32, 0, 32)
	for py := 0; py < h; py++ {
		y := originY + (float32(py)+0.5)*pxSize
		xs = xs[:0]
		for li := range fp.Loops {
			pts := fp.Loops[li].Points
			n := len(pts)
			if n < 3 {
				continue
			}
			// Bbox reject the loop against this scanline.
			if y < fp.Loops[li].MinY || y > fp.Loops[li].MaxY {
				continue
			}
			for i, j := 0, n-1; i < n; j, i = i, i+1 {
				yi := pts[i][1]
				yj := pts[j][1]
				// Half-open rule: edge counts iff exactly one
				// endpoint is strictly above y. This is the
				// standard even-odd convention; it correctly
				// skips horizontal edges and avoids double-
				// counting at vertex meetings.
				if (yi > y) == (yj > y) {
					continue
				}
				t := (y - yi) / (yj - yi)
				xi := pts[i][0] + t*(pts[j][0]-pts[i][0])
				xs = append(xs, xi)
			}
		}
		if len(xs) < 2 {
			continue
		}
		sort.Slice(xs, func(a, b int) bool { return xs[a] < xs[b] })
		rowBase := py * w
		// Fill between consecutive pairs.
		for k := 0; k+1 < len(xs); k += 2 {
			x0, x1 := xs[k], xs[k+1]
			// Map world x to pixel index by pixel CENTRE:
			//   pxCentre = originX + (px+0.5)*pxSize ≥ x0
			//   ⇒ px ≥ (x0 - originX)/pxSize - 0.5
			//   ⇒ smallest such px = ceil((x0 - originX)/pxSize - 0.5)
			f0 := (x0-originX)/pxSize - 0.5
			f1 := (x1-originX)/pxSize - 0.5
			px0 := intCeil(f0)
			px1 := intFloor(f1)
			if px0 < 0 {
				px0 = 0
			}
			if px1 >= w {
				px1 = w - 1
			}
			for px := px0; px <= px1; px++ {
				idx := rowBase + px
				inFootprint[idx>>6] |= uint64(1) << uint(idx&63)
			}
		}
	}
}

// StampCellsFromOuter clears CellID and stamps every cell's polygon
// into r. Same algorithm as StampCellByPolygon called in a loop.
// Pre-existed for validation; kept as a convenience wrapper.
func StampCellsFromOuter(cells []Cell, r *SlabRaster) {
	for i := range r.CellID {
		r.CellID[i] = NoCellID
	}
	for ci := range cells {
		StampCellByPolygon(cells[ci].Outer, int32(ci), r)
	}
}

// StampCellByPolygon stamps cellIdx into r.CellID for every in-
// footprint pixel whose centre lies inside poly. Pixels already
// owned by another cell are not overwritten — earlier cells in the
// stamping order "win" overlapping regions.
//
// poly does NOT need to lie inside r's footprint; the InFootprint
// mask gates writes. The caller is responsible for clearing the
// CellID buffer beforehand (StampCellsFromOuter does it once, then
// streams cells through).
func StampCellByPolygon(poly []Point2, cellIdx int32, r *SlabRaster) {
	StampCellByPolygonMasked(poly, cellIdx, r, r.InFootprint)
}

// StampCellByPolygonMasked is StampCellByPolygon with a caller-
// supplied gate bitmap in place of r.InFootprint. Used by the
// PartitionSlabRaster path to confine hex stamping to the inner
// footprint mask while ring stamping uses the outer footprint —
// without that distinction, the raw ring trapezoids (which extend
// inward by depth = 3 × cellSize as scratch space for the old
// Clipper clip) would re-claim pixels that hex cells should own.
func StampCellByPolygonMasked(poly []Point2, cellIdx int32, r *SlabRaster, gate []uint64) {
	if len(poly) < 3 {
		return
	}
	minX, minY, maxX, maxY := polyBounds(poly)
	px0 := intCeil((minX-r.OriginX)/r.PxSize - 0.5)
	px1 := intFloor((maxX-r.OriginX)/r.PxSize - 0.5)
	py0 := intCeil((minY-r.OriginY)/r.PxSize - 0.5)
	py1 := intFloor((maxY-r.OriginY)/r.PxSize - 0.5)
	if px0 < 0 {
		px0 = 0
	}
	if py0 < 0 {
		py0 = 0
	}
	if px1 >= r.W {
		px1 = r.W - 1
	}
	if py1 >= r.H {
		py1 = r.H - 1
	}
	for py := py0; py <= py1; py++ {
		y := r.OriginY + (float32(py)+0.5)*r.PxSize
		rowBase := py * r.W
		for px := px0; px <= px1; px++ {
			idx := rowBase + px
			if gate[idx>>6]&(uint64(1)<<uint(idx&63)) == 0 {
				continue
			}
			if r.CellID[idx] != NoCellID {
				continue
			}
			x := r.OriginX + (float32(px)+0.5)*r.PxSize
			if pointInPolygon(poly, x, y) {
				r.CellID[idx] = cellIdx
			}
		}
	}
}

// CellOutlineFromRaster recovers a closed boundary polygon for the
// cell with index cellIdx by walking the pixel-grid edges between
// cell-pixels and non-cell-pixels. The returned polygon's vertices
// land on pixel corners (i.e. integer multiples of PxSize off
// OriginX/Y); collinear runs are NOT collapsed by this function —
// pass through simplifyCollinear if you want a shorter vertex list.
//
// Returns nil if the cell has no pixels in r. Returns the outer
// loop only — interior holes are ignored on the assumption that
// cells are simply connected (true for well-formed hex / ring
// partitions). bbox is the inclusive pixel-coordinate bbox of the
// cell's pixels; pass {0, 0, r.W-1, r.H-1} to scan the whole grid.
//
// Edge-walk convention: for each in-cell pixel, emit a unit-length
// boundary edge along any side whose neighbor is NOT in the cell.
// Edge direction is consistent (CCW around cell pixels), so all
// emitted edges form one or more closed loops that share no
// endpoints. We then chain the edges from any starting endpoint and
// return the first closed loop.
func CellOutlineFromRaster(r *SlabRaster, cellIdx int32, bboxPxMinX, bboxPxMinY, bboxPxMaxX, bboxPxMaxY int) []Point2 {
	if bboxPxMinX < 0 {
		bboxPxMinX = 0
	}
	if bboxPxMinY < 0 {
		bboxPxMinY = 0
	}
	if bboxPxMaxX >= r.W {
		bboxPxMaxX = r.W - 1
	}
	if bboxPxMaxY >= r.H {
		bboxPxMaxY = r.H - 1
	}
	if bboxPxMinX > bboxPxMaxX || bboxPxMinY > bboxPxMaxY {
		return nil
	}
	inCell := func(px, py int) bool {
		if px < 0 || py < 0 || px >= r.W || py >= r.H {
			return false
		}
		return r.CellID[py*r.W+px] == cellIdx
	}
	// Each edge connects two grid-corner points (integer (x, y) at
	// pixel boundaries). For chaining we store one edge per `from`
	// corner — at well-formed boundaries each corner has exactly
	// one out-edge, so a single-keyed map works.
	//
	// Edge directions (CCW around each in-cell pixel):
	//   bottom side (neighbor y-1 out): (px,   py)   → (px+1, py)
	//   right  side (neighbor x+1 out): (px+1, py)   → (px+1, py+1)
	//   top    side (neighbor y+1 out): (px+1, py+1) → (px,   py+1)
	//   left   side (neighbor x-1 out): (px,   py+1) → (px,   py)
	out := make(map[[2]int][2]int)
	for py := bboxPxMinY; py <= bboxPxMaxY; py++ {
		for px := bboxPxMinX; px <= bboxPxMaxX; px++ {
			if !inCell(px, py) {
				continue
			}
			if !inCell(px, py-1) {
				out[[2]int{px, py}] = [2]int{px + 1, py}
			}
			if !inCell(px+1, py) {
				out[[2]int{px + 1, py}] = [2]int{px + 1, py + 1}
			}
			if !inCell(px, py+1) {
				out[[2]int{px + 1, py + 1}] = [2]int{px, py + 1}
			}
			if !inCell(px-1, py) {
				out[[2]int{px, py + 1}] = [2]int{px, py}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	// Pick a start: leftmost-then-bottom-most corner ensures we
	// start on the outer boundary, not an inner hole. (Cells are
	// assumed simply connected, but this is robust either way.)
	var start [2]int
	first := true
	for k := range out {
		if first || k[0] < start[0] || (k[0] == start[0] && k[1] < start[1]) {
			start = k
			first = false
		}
	}
	poly := make([]Point2, 0, len(out))
	cur := start
	for {
		x := r.OriginX + float32(cur[0])*r.PxSize
		y := r.OriginY + float32(cur[1])*r.PxSize
		poly = append(poly, Point2{x, y})
		nxt, ok := out[cur]
		if !ok {
			// Open loop — shouldn't happen for well-formed input,
			// bail out with what we have.
			break
		}
		if nxt == start {
			break
		}
		cur = nxt
		if len(poly) > len(out)+1 {
			// Cycle guard: walked more edges than exist.
			break
		}
	}
	return simplifyCollinear(poly)
}

// simplifyCollinear removes vertices that lie on the straight
// segment between their neighbours. The marching-squares output is
// inherently rectilinear, so this collapses horizontal/vertical
// staircases that share an axis into single segments. A hex cell's
// stairstep outline (~24 vertices) collapses to ~12 vertices that
// trace the staircase corners; for clip2d that's still up from the
// pre-raster 6-vertex hex, but the cost stays linear in the
// boundary pixel count.
func simplifyCollinear(poly []Point2) []Point2 {
	n := len(poly)
	if n < 3 {
		return poly
	}
	out := poly[:0:0]
	for i := 0; i < n; i++ {
		prev := poly[(i-1+n)%n]
		cur := poly[i]
		next := poly[(i+1)%n]
		// Cross product of (cur-prev) × (next-cur). Zero ⇒ collinear.
		cx := (cur[0] - prev[0]) * (next[1] - cur[1])
		cy := (cur[1] - prev[1]) * (next[0] - cur[0])
		if cx == cy {
			continue
		}
		out = append(out, cur)
	}
	if len(out) < 3 {
		return poly
	}
	// Need a fresh backing array — out aliases poly.
	cp := make([]Point2, len(out))
	copy(cp, out)
	return cp
}

// CellPixelCounts returns the number of raster pixels owned by each
// cell, indexed by Slab.Cells index.
func CellPixelCounts(r *SlabRaster, nCells int) []int32 {
	out := make([]int32, nCells)
	for _, id := range r.CellID {
		if id < 0 || int(id) >= nCells {
			continue
		}
		out[id]++
	}
	return out
}

// CellAreasFromRaster returns the area (mm²) of each cell in the
// slab, derived by counting the cell's pixels in r. Cells with no
// pixels (slivers below the raster resolution) report 0.
func CellAreasFromRaster(r *SlabRaster, nCells int) []float32 {
	counts := CellPixelCounts(r, nCells)
	px2 := r.PxSize * r.PxSize
	out := make([]float32, nCells)
	for i, c := range counts {
		out[i] = float32(c) * px2
	}
	return out
}

// CellCentroidsFromRaster returns the centroid (world coords) of
// each cell derived by averaging pixel centres weighted by 1. Cells
// with no pixels return (NaN, NaN); callers should skip those —
// they're the same slivers CellAreasFromRaster reports as 0 area.
func CellCentroidsFromRaster(r *SlabRaster, nCells int) [][2]float32 {
	sumX := make([]float64, nCells)
	sumY := make([]float64, nCells)
	count := make([]int32, nCells)
	for py := 0; py < r.H; py++ {
		yCentre := float64(r.OriginY) + (float64(py)+0.5)*float64(r.PxSize)
		rowBase := py * r.W
		for px := 0; px < r.W; px++ {
			id := r.CellID[rowBase+px]
			if id < 0 || int(id) >= nCells {
				continue
			}
			xCentre := float64(r.OriginX) + (float64(px)+0.5)*float64(r.PxSize)
			sumX[id] += xCentre
			sumY[id] += yCentre
			count[id]++
		}
	}
	out := make([][2]float32, nCells)
	for i := range out {
		if count[i] == 0 {
			out[i] = [2]float32{nan32, nan32}
			continue
		}
		inv := 1.0 / float64(count[i])
		out[i] = [2]float32{float32(sumX[i] * inv), float32(sumY[i] * inv)}
	}
	return out
}

// BuildSlabRaster sizes a SlabRaster for s's footprint with a one-
// pixel margin on every side, rasterises the footprint into it, and
// stamps cell ownership from s.Cells. Returns nil for slabs with no
// footprint.
//
// pxSize <= 0 picks cellSize/4 to match the existing within-slab
// adjacency resolution.
func BuildSlabRaster(s *Slab, cellSize, pxSize float32) *SlabRaster {
	if s == nil || s.Footprint == nil || len(s.Footprint.Loops) == 0 {
		return nil
	}
	if pxSize <= 0 {
		pxSize = cellSize / 4
	}
	if pxSize <= 0 {
		return nil
	}
	minX, minY, maxX, maxY, ok := s.Footprint.Bounds()
	if !ok {
		return nil
	}
	// One-pixel margin so the in-footprint scan never touches the
	// outermost row/column (small but free safety against rounding).
	margin := pxSize
	minX -= margin
	minY -= margin
	maxX += margin
	maxY += margin
	w := intCeil((maxX - minX) / pxSize)
	h := intCeil((maxY - minY) / pxSize)
	if w < 1 || h < 1 {
		return nil
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
	RasterizeFootprint(s.Footprint, minX, minY, pxSize, w, h, r.InFootprint)
	StampCellsFromOuter(s.Cells, r)
	return r
}

// FootprintPixelCount returns the number of pixels marked
// in-footprint in r. Useful for sanity-checking footprint area vs.
// summed cell area.
func FootprintPixelCount(r *SlabRaster) int {
	n := 0
	for _, w := range r.InFootprint {
		// Hamming weight: stdlib math/bits.OnesCount64.
		x := w
		for x != 0 {
			x &= x - 1
			n++
		}
	}
	return n
}

// intCeil / intFloor: cheap float→int rounding that doesn't reach
// for math.Ceil/math.Floor (which take float64). The footprint
// rasteriser and cell stamper are the hot path; this matters.
func intCeil(v float32) int {
	i := int(v)
	if float32(i) < v {
		return i + 1
	}
	return i
}
func intFloor(v float32) int {
	i := int(v)
	if float32(i) > v {
		return i - 1
	}
	return i
}

// nan32 is a float32 NaN constant. We can't use math.NaN() (float64)
// in a float32 const context, so cast at init time.
var nan32 = func() float32 {
	x := float32(0)
	return x / x
}()
