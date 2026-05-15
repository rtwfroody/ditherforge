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

// StampCellsFromOuter fills r.CellID by point-in-polygon-testing
// each cell's Outer polygon against every pixel in the cell's bbox.
// In-footprint pixels that aren't covered by any cell keep NoCellID
// (these are the gaps between cells along the footprint boundary,
// usually a 1-pixel-wide band).
//
// Pixels outside the footprint are forced to NoCellID, so a stale
// CellID buffer is safely overwritten.
//
// This is the validation path: it consumes existing exact polygon
// data and produces a raster equivalent we can compare against. The
// production path (next commit) will stamp directly from cell
// centres without ever touching polygons.
func StampCellsFromOuter(cells []Cell, r *SlabRaster) {
	for i := range r.CellID {
		r.CellID[i] = NoCellID
	}
	for ci := range cells {
		pts := cells[ci].Outer
		if len(pts) < 3 {
			continue
		}
		minX, minY, maxX, maxY := polyBounds(pts)
		// Cell-bbox → pixel range (inclusive, by pixel centre).
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
				// In-footprint AND not already claimed: claim it
				// only if the polygon contains the pixel centre.
				// (Pre-cleared to NoCellID, so the "not already
				// claimed" branch is the common one.)
				bitWord := r.InFootprint[idx>>6]
				if bitWord&(uint64(1)<<uint(idx&63)) == 0 {
					continue
				}
				if r.CellID[idx] != NoCellID {
					continue
				}
				x := r.OriginX + (float32(px)+0.5)*r.PxSize
				if pointInPolygon(pts, x, y) {
					r.CellID[idx] = int32(ci)
				}
			}
		}
	}
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
