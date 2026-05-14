package cellslicer

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"

	"github.com/rtwfroody/ditherforge/internal/minislicer"
)

// DebugPNGOptions controls the per-slab cell visualization rendered
// by WriteDebugPNGs. Zero values pick sensible defaults.
type DebugPNGOptions struct {
	// PixelSizeMM is the world-space size of one output pixel. 0 →
	// cellSize / 8 (cellSize must be passed alongside).
	PixelSizeMM float32
	// CellSizeMM is used only when PixelSizeMM <= 0 to derive the
	// pixel size. Pass the same cellSize given to PartitionModel.
	CellSizeMM float32
	// PadPx is the padding (in pixels) around the slab's bbox.
	// 0 → 8.
	PadPx int
	// FillBackgroundWhite makes pixels outside any cell white. When
	// false they're transparent.
	FillBackgroundWhite bool
	// DrawEdges, when true, paints a 1-pixel black edge between
	// neighboring cells.
	DrawEdges bool
}

// WriteDebugPNGs writes one slab_NNNN.png per slab into dir,
// rendering each cell filled with the matching CellSample's RGB
// (cells with no sample / Alpha == false are rendered transparent).
// samples must be the SampleCells output for the same slabs.
func WriteDebugPNGs(slabs []Slab, samples []CellSample, dir string, opt DebugPNGOptions) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	bySlab := make([][]int, len(slabs))
	for gi, s := range samples {
		if s.SlabIdx < 0 || s.SlabIdx >= len(slabs) {
			continue
		}
		bySlab[s.SlabIdx] = append(bySlab[s.SlabIdx], gi)
	}
	for si := range slabs {
		s := &slabs[si]
		if s.Footprint == nil || len(s.Footprint.Loops) == 0 {
			continue
		}
		img := renderSlabDebug(s, samples, bySlab[si], opt)
		if img == nil {
			continue
		}
		p := filepath.Join(dir, slabFilename(si))
		f, err := os.Create(p)
		if err != nil {
			return err
		}
		if err := png.Encode(f, img); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

func slabFilename(idx int) string {
	digits := []byte("0000")
	for i := 3; i >= 0 && idx > 0; i-- {
		digits[i] = byte('0' + idx%10)
		idx /= 10
	}
	return "slab_" + string(digits) + ".png"
}

func renderSlabDebug(s *Slab, samples []CellSample, sampleIdxs []int, opt DebugPNGOptions) image.Image {
	pxSize := opt.PixelSizeMM
	if pxSize <= 0 {
		cs := opt.CellSizeMM
		if cs <= 0 {
			cs = 1
		}
		pxSize = cs / 8
	}
	padPx := opt.PadPx
	if padPx <= 0 {
		padPx = 8
	}
	minX, minY, maxX, maxY, ok := s.Footprint.Bounds()
	if !ok {
		return nil
	}
	margin := pxSize * 4
	if opt.CellSizeMM > margin {
		margin = 2 * opt.CellSizeMM
	}
	minX -= margin
	minY -= margin
	maxX += margin
	maxY += margin
	w := int(math.Ceil(float64((maxX-minX)/pxSize))) + 2*padPx
	h := int(math.Ceil(float64((maxY-minY)/pxSize))) + 2*padPx
	if w < 1 || h < 1 {
		return nil
	}

	// Index samples by cell-within-slab so a cell's RGB lookup is O(1).
	cellColor := make(map[int][4]uint8, len(sampleIdxs))
	for _, gi := range sampleIdxs {
		sp := samples[gi]
		if !sp.Alpha {
			continue
		}
		cellColor[sp.CellIdx] = [4]uint8{sp.Color[0], sp.Color[1], sp.Color[2], 255}
	}

	cellIDs := rasterizeCellsForDebug(s.Cells, minX, minY, pxSize, padPx, w, h)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bg := color.RGBA{0, 0, 0, 0}
	if opt.FillBackgroundWhite {
		bg = color.RGBA{255, 255, 255, 255}
	}
	for py := 0; py < h; py++ {
		for px := 0; px < w; px++ {
			id := cellIDs[py*w+px]
			if id < 0 {
				img.SetRGBA(px, py, bg)
				continue
			}
			c, has := cellColor[int(id)]
			if !has {
				img.SetRGBA(px, py, color.RGBA{180, 180, 180, 255})
				continue
			}
			img.SetRGBA(px, py, color.RGBA{c[0], c[1], c[2], c[3]})
		}
	}
	if opt.DrawEdges {
		for py := 0; py < h; py++ {
			for px := 0; px < w; px++ {
				id := cellIDs[py*w+px]
				if id < 0 {
					continue
				}
				edge := false
				if px+1 < w && cellIDs[py*w+px+1] != id {
					edge = true
				} else if py+1 < h && cellIDs[(py+1)*w+px] != id {
					edge = true
				}
				if edge {
					img.SetRGBA(px, py, color.RGBA{0, 0, 0, 255})
				}
			}
		}
	}
	return img
}

func rasterizeCellsForDebug(cells []Cell, minX, minY, pxSize float32, padPx, w, h int) []int64 {
	cellIDs := make([]int64, w*h)
	for i := range cellIDs {
		cellIDs[i] = -1
	}
	for idx, cell := range cells {
		cMinX, cMinY, cMaxX, cMaxY := polyBounds(cell.Outer)
		pxMin := int(math.Floor(float64((cMinX-minX)/pxSize))) + padPx - 1
		pxMax := int(math.Ceil(float64((cMaxX-minX)/pxSize))) + padPx + 1
		pyMin := int(math.Floor(float64((cMinY-minY)/pxSize))) + padPx - 1
		pyMax := int(math.Ceil(float64((cMaxY-minY)/pxSize))) + padPx + 1
		if pxMin < 0 {
			pxMin = 0
		}
		if pyMin < 0 {
			pyMin = 0
		}
		if pxMax >= w {
			pxMax = w - 1
		}
		if pyMax >= h {
			pyMax = h - 1
		}
		for py := pyMin; py <= pyMax; py++ {
			wy := minY + (float32(py-padPx)+0.5)*pxSize
			for px := pxMin; px <= pxMax; px++ {
				wx := minX + (float32(px-padPx)+0.5)*pxSize
				if pointInPolygonDebug(cell.Outer, wx, wy) {
					cellIDs[py*w+px] = int64(idx)
				}
			}
		}
	}
	return cellIDs
}

func pointInPolygonDebug(pts []minislicer.Point2, x, y float32) bool {
	inside := false
	n := len(pts)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		if (pts[i][1] > y) != (pts[j][1] > y) {
			xIntersect := (pts[j][0]-pts[i][0])*(y-pts[i][1])/(pts[j][1]-pts[i][1]) + pts[i][0]
			if x < xIntersect {
				inside = !inside
			}
		}
	}
	return inside
}
