// cellslicer-prototype slices a mesh into slabs and renders the
// surface-conforming cell partition for each slab as a PNG.
//
// The cell partition (footprint, ring cells, hex cells) is produced
// by internal/cellslicer; this binary is a thin client that
// rasterizes the cell polygons into a PNG for visual inspection,
// applying a pixel-based small-cell merge as a debug aid.
//
// Usage:
//
//	cellslicer-prototype --mesh tests/objects/low_poly_building.glb \
//	    --out /tmp/cells --cell-size 1.0 --layer-height 0.2 --size 100
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rtwfroody/ditherforge/internal/cellslicer"
	"github.com/rtwfroody/ditherforge/internal/loader"
)

func main() {
	var (
		meshPath  = flag.String("mesh", "", "input mesh path (.glb, .stl, .3mf)")
		outDir    = flag.String("out", "out", "output directory for slab PNGs")
		layerH    = flag.Float64("layer-height", 0.2, "layer / slab height in mm")
		cellSize  = flag.Float64("cell-size", 1.0, "target cell size in mm")
		pixelSize = flag.Float64("pixel-size", 0.0, "pixel size in mm (0 = cellSize/8)")
		maxSlabs  = flag.Int("max-slabs", 0, "max slabs to render (0 = all)")
		slabStep  = flag.Int("slab-step", 1, "render every N-th slab (1 = all)")
		padPx     = flag.Int("pad", 8, "padding pixels around slab bbox")
		size      = flag.Float64("size", 100, "target max extent in mm (model is uniformly scaled)")
		mergeFrac = flag.Float64("merge-frac", 0.5, "merge cells smaller than this fraction of cellSize² into largest neighbor (0 disables)")
	)
	flag.Parse()

	if *meshPath == "" {
		flag.Usage()
		log.Fatal("--mesh is required")
	}

	pxSize := float32(*pixelSize)
	if pxSize <= 0 {
		pxSize = float32(*cellSize) / 8
	}

	model, err := loadModel(*meshPath)
	if err != nil {
		log.Fatalf("load: %v", err)
	}

	extent := maxExtent(model)
	if extent > 0 {
		loader.ScaleModel(model, float32(*size/float64(extent)))
	}

	slabs := cellslicer.PartitionModel(model, float32(*layerH), float32(*cellSize))
	if len(slabs) == 0 {
		log.Fatalf("model has fewer than 2 slicing planes")
	}
	zMin, zMax := slabs[0].ZBot, slabs[len(slabs)-1].ZTop
	fmt.Printf("Sliced %d slabs from Z=[%.3f, %.3f] (slab height %.3f)\n",
		len(slabs), zMin, zMax, *layerH)

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}

	rendered := 0
	for i := range slabs {
		if i%*slabStep != 0 {
			continue
		}
		if *maxSlabs > 0 && rendered >= *maxSlabs {
			break
		}
		s := &slabs[i]
		if s.Footprint == nil || len(s.Footprint.Loops) == 0 {
			continue
		}
		t0 := time.Now()
		img, nRing, nHex, nMerges := renderSlab(s, float32(*cellSize), pxSize, *padPx, float32(*mergeFrac))
		fname := filepath.Join(*outDir, fmt.Sprintf("slab_%04d.png", i))
		if err := writePNG(img, fname); err != nil {
			log.Fatalf("write %s: %v", fname, err)
		}
		b := img.Bounds()
		nBot := 0
		nTop := 0
		if s.BotLayer != nil {
			nBot = len(s.BotLayer.Loops)
		}
		if s.TopLayer != nil {
			nTop = len(s.TopLayer.Loops)
		}
		fmt.Printf("  slab %d: bot=%dloops top=%dloops ring=%d hex=%d merges=%d %dx%dpx %.2fs\n",
			i, nBot, nTop, nRing, nHex, nMerges,
			b.Dx(), b.Dy(), time.Since(t0).Seconds())
		rendered++
	}

	fmt.Printf("Wrote %d slab PNGs to %s\n", rendered, *outDir)
}

func loadModel(path string) (*loader.LoadedModel, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".glb":
		return loader.LoadGLB(path, -1)
	case ".3mf":
		return loader.Load3MF(path, -1)
	case ".stl":
		return loader.LoadSTL(path, -1)
	default:
		return nil, fmt.Errorf("unsupported format %q", ext)
	}
}

func maxExtent(m *loader.LoadedModel) float32 {
	if len(m.Vertices) == 0 {
		return 0
	}
	mn := m.Vertices[0]
	mx := m.Vertices[0]
	for _, v := range m.Vertices[1:] {
		for i := 0; i < 3; i++ {
			if v[i] < mn[i] {
				mn[i] = v[i]
			}
			if v[i] > mx[i] {
				mx[i] = v[i]
			}
		}
	}
	best := mx[0] - mn[0]
	for i := 1; i < 3; i++ {
		if d := mx[i] - mn[i]; d > best {
			best = d
		}
	}
	return best
}

// renderSlab rasterizes the slab's cell polygons to a cellIDs grid,
// applies the pixel-based small-cell merge, and renders the result as
// a PNG. Returns the image and used-ring / used-hex / merge counts.
func renderSlab(s *cellslicer.Slab, cellSize, pxSize float32, padPx int, mergeFrac float32) (image.Image, int, int, int) {
	minX, minY, maxX, maxY, hasBounds := s.Footprint.Bounds()
	if !hasBounds {
		return image.NewRGBA(image.Rect(0, 0, 1, 1)), 0, 0, 0
	}
	margin := 2 * cellSize
	minX -= margin
	minY -= margin
	maxX += margin
	maxY += margin
	w := int(math.Ceil(float64((maxX-minX)/pxSize))) + 2*padPx
	h := int(math.Ceil(float64((maxY-minY)/pxSize))) + 2*padPx
	if w < 1 || h < 1 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1)), 0, 0, 0
	}

	cells := s.Cells
	cellIDs := rasterizeCells(cells, minX, minY, pxSize, padPx, w, h)

	nMerges := 0
	if mergeFrac > 0 {
		minAreaMM2 := float64(mergeFrac) * float64(cellSize) * float64(cellSize)
		minPixels := int(math.Round(minAreaMM2 / (float64(pxSize) * float64(pxSize))))
		if minPixels < 1 {
			minPixels = 1
		}
		nMerges = mergeSmallCells(cellIDs, w, h, minPixels)
	}

	usedRing := map[int64]struct{}{}
	usedHex := map[int64]struct{}{}
	for _, id := range cellIDs {
		if id < 0 || int(id) >= len(cells) {
			continue
		}
		if cells[id].Kind == cellslicer.KindRing {
			usedRing[id] = struct{}{}
		} else {
			usedHex[id] = struct{}{}
		}
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for py := 0; py < h; py++ {
		for px := 0; px < w; px++ {
			id := cellIDs[py*w+px]
			if id < 0 {
				img.SetRGBA(px, py, color.RGBA{255, 255, 255, 255})
				continue
			}
			img.SetRGBA(px, py, cellColor(id))
		}
	}
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

	return img, len(usedRing), len(usedHex), nMerges
}

// rasterizeCells paints each cell polygon onto a cellIDs grid using
// pixel-by-pixel point-in-polygon. Pixels outside any cell stay at
// -1. Pixels inside multiple cells get the last-painted cell's ID
// (overlap shouldn't happen by construction but is harmless).
func rasterizeCells(cells []cellslicer.Cell, minX, minY, pxSize float32, padPx, w, h int) []int64 {
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
				if pointInPolygon(cell.Outer, wx, wy) {
					cellIDs[py*w+px] = int64(idx)
				}
			}
		}
	}
	return cellIDs
}

func polyBounds(pts []cellslicer.Point2) (float32, float32, float32, float32) {
	minX, minY := pts[0][0], pts[0][1]
	maxX, maxY := pts[0][0], pts[0][1]
	for _, p := range pts[1:] {
		if p[0] < minX {
			minX = p[0]
		}
		if p[0] > maxX {
			maxX = p[0]
		}
		if p[1] < minY {
			minY = p[1]
		}
		if p[1] > maxY {
			maxY = p[1]
		}
	}
	return minX, minY, maxX, maxY
}

func pointInPolygon(pts []cellslicer.Point2, x, y float32) bool {
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

// mergeSmallCells merges cells with pixel count < minPixels into
// their largest-shared-boundary neighbor, repeating until no
// eligible cells remain. Modifies cellIDs in place; returns the
// number of merges. -1 (outside) pixels are never merged.
func mergeSmallCells(cellIDs []int64, w, h, minPixels int) int {
	type stats struct {
		area      int
		neighbors map[int64]int
	}
	cells := map[int64]*stats{}
	get := func(id int64) *stats {
		s, ok := cells[id]
		if !ok {
			s = &stats{neighbors: map[int64]int{}}
			cells[id] = s
		}
		return s
	}
	for py := 0; py < h; py++ {
		for px := 0; px < w; px++ {
			id := cellIDs[py*w+px]
			if id < 0 {
				continue
			}
			get(id).area++
			if px+1 < w {
				nid := cellIDs[py*w+px+1]
				if nid >= 0 && nid != id {
					get(id).neighbors[nid]++
					get(nid).neighbors[id]++
				}
			}
			if py+1 < h {
				nid := cellIDs[(py+1)*w+px]
				if nid >= 0 && nid != id {
					get(id).neighbors[nid]++
					get(nid).neighbors[id]++
				}
			}
		}
	}
	remap := map[int64]int64{}
	nMerges := 0
	for {
		var smallID int64
		smallArea := minPixels
		found := false
		for id, s := range cells {
			if s.area < smallArea {
				smallArea = s.area
				smallID = id
				found = true
			}
		}
		if !found {
			break
		}
		ss := cells[smallID]
		if len(ss.neighbors) == 0 {
			delete(cells, smallID)
			continue
		}
		var targetID int64
		bestShare := -1
		for nid, share := range ss.neighbors {
			if share > bestShare {
				bestShare = share
				targetID = nid
			}
		}
		tt := cells[targetID]
		tt.area += ss.area
		for nid, share := range ss.neighbors {
			if nid == targetID {
				continue
			}
			tt.neighbors[nid] += share
			cells[nid].neighbors[targetID] += share
			delete(cells[nid].neighbors, smallID)
		}
		delete(tt.neighbors, smallID)
		delete(cells, smallID)
		remap[smallID] = targetID
		nMerges++
	}
	for i := range cellIDs {
		id := cellIDs[i]
		if id < 0 {
			continue
		}
		for {
			next, ok := remap[id]
			if !ok {
				break
			}
			id = next
		}
		cellIDs[i] = id
	}
	return nMerges
}

func cellColor(id int64) color.RGBA {
	h := uint64(14695981039346656037)
	for i := 0; i < 8; i++ {
		h ^= uint64(byte(id >> (i * 8)))
		h *= 1099511628211
	}
	r := uint8(128 + (h>>0)%128)
	g := uint8(128 + (h>>16)%128)
	b := uint8(128 + (h>>32)%128)
	return color.RGBA{r, g, b, 255}
}

func writePNG(img image.Image, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
