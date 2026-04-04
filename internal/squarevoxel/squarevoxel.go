// Package squarevoxel generates a colored mesh by voxelizing the input model
// for color decisions, then clipping the original mesh along color patch
// boundaries.
package squarevoxel

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"sort"
	"time"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/voxel"
	"github.com/schollz/progressbar/v3"
	"golang.org/x/term"
)

// Config holds parameters for square voxel remeshing.
type Config struct {
	NozzleDiameter float32
	LayerHeight    float32
	NoMerge        bool
	NoSimplify     bool
	ColorSnap      float64 // shift cell colors toward nearest palette color by this many ΔE (0 to disable)
}

var isTTY = term.IsTerminal(int(os.Stderr.Fd()))

func newBar(total int, description string) *progressbar.ProgressBar {
	if !isTTY {
		// Non-interactive: return a silent bar.
		return progressbar.NewOptions(total,
			progressbar.OptionSetVisibility(false),
		)
	}
	return progressbar.NewOptions(total,
		progressbar.OptionSetDescription(description),
		progressbar.OptionSetWidth(30),
		progressbar.OptionShowCount(),
		progressbar.OptionClearOnFinish(),
		progressbar.OptionThrottle(100*time.Millisecond),
	)
}

func finishBar(bar *progressbar.ProgressBar, description string, detail string, elapsed time.Duration) {
	bar.Finish()
	fmt.Printf("  %s %s in %.1fs\n", description, detail, elapsed.Seconds())
}

// Remesh voxelizes the model for color decisions, then clips the original
// mesh along color patch boundaries.
// If cached is non-nil, skips voxelization.
// Returns output model, per-face assignments, palette, cache data, and error.
func Remesh(model *loader.LoadedModel, pcfg voxel.PaletteConfig, cfg Config, ditherMode string, cached *CacheData) (*loader.LoadedModel, []int32, [][3]uint8, *CacheData, error) {
	if len(model.Vertices) == 0 || len(model.Faces) == 0 {
		return nil, nil, nil, nil, fmt.Errorf("empty model")
	}

	fmt.Printf("  Input mesh: %s\n", voxel.CheckWatertight(model.Faces))

	// Cell edge length. At 1.0× nozzle diameter the slicer can't fill the
	// bottom layer reliably. Empirically 1.275× (0.51mm for a 0.4mm nozzle)
	// is the minimum that produces solid first layers.
	cellSize := cfg.NozzleDiameter * 1.275
	layerH := cfg.LayerHeight

	var cells []voxel.ActiveCell
	var cellAssignMap map[voxel.CellKey]int
	var minV [3]float32

	if cached != nil {
		cells = cached.Cells
		cellAssignMap = cached.CellAssignMap
		minV = cached.MinV
		cellSize = cached.CellSize
		layerH = cached.LayerH
	}

	if cached == nil {

	// 1. Bounding box.
	var maxV [3]float32
	minV, maxV = voxel.ComputeBounds(model.Vertices)
	xyPad := cellSize * 2
	zPad := layerH * 2
	minV[0] -= xyPad
	minV[1] -= xyPad
	minV[2] -= zPad
	maxV[0] += xyPad
	maxV[1] += xyPad
	maxV[2] += zPad

	nCols := int(math.Ceil(float64(maxV[0]-minV[0])/float64(cellSize))) + 1
	nRows := int(math.Ceil(float64(maxV[1]-minV[1])/float64(cellSize))) + 1
	nLayers := int(math.Ceil(float64(maxV[2]-minV[2])/float64(layerH))) + 1

	// 2. Spatial index.
	si := voxel.NewSpatialIndex(model, cellSize*2)

	// 3. Triangle-overlap voxelization.
	tVoxelize := time.Now()
	bar := newBar(len(model.Faces), "  Voxelizing")
	halfExtent := [3]float32{cellSize / 2, cellSize / 2, layerH / 2}
	cellSet := make(map[voxel.CellKey]struct{})
	for fi := range model.Faces {
		bar.Add(1)
		f := model.Faces[fi]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]
		tMinX := min(v0[0], min(v1[0], v2[0]))
		tMaxX := max(v0[0], max(v1[0], v2[0]))
		tMinY := min(v0[1], min(v1[1], v2[1]))
		tMaxY := max(v0[1], max(v1[1], v2[1]))
		tMinZ := min(v0[2], min(v1[2], v2[2]))
		tMaxZ := max(v0[2], max(v1[2], v2[2]))
		tMin := [3]float32{tMinX, tMinY, tMinZ}
		tMax := [3]float32{tMaxX, tMaxY, tMaxZ}
		colMin, colMax, rowMin, rowMax, layerMin, layerMax := voxel.AABBCellRange(tMin, tMax, minV, cellSize, layerH)
		if colMin < 0 {
			colMin = 0
		}
		if colMax >= nCols {
			colMax = nCols - 1
		}
		if rowMin < 0 {
			rowMin = 0
		}
		if rowMax >= nRows {
			rowMax = nRows - 1
		}
		if layerMin < 0 {
			layerMin = 0
		}
		if layerMax >= nLayers {
			layerMax = nLayers - 1
		}
		for col := colMin; col <= colMax; col++ {
			cx := minV[0] + float32(col)*cellSize
			for row := rowMin; row <= rowMax; row++ {
				cy := minV[1] + float32(row)*cellSize
				for layer := layerMin; layer <= layerMax; layer++ {
					cz := minV[2] + float32(layer)*layerH
					center := [3]float32{cx, cy, cz}
					if voxel.TriangleAABBOverlap(v0, v1, v2, center, halfExtent) {
						cellSet[voxel.CellKey{Col: col, Row: row, Layer: layer}] = struct{}{}
					}
				}
			}
		}
	}
	finishBar(bar, "Voxelized", fmt.Sprintf("%d cells", len(cellSet)), time.Since(tVoxelize))

	// Color each active cell.
	tColor := time.Now()
	colorBuf := voxel.NewSearchBuf(len(model.Faces))
	colorRadius := cellSize * 3
	barColor := newBar(len(cellSet), "  Coloring cells")
	for k := range cellSet {
		barColor.Add(1)
		cx := minV[0] + float32(k.Col)*cellSize
		cy := minV[1] + float32(k.Row)*cellSize
		cz := minV[2] + float32(k.Layer)*layerH
		rgba := voxel.SampleNearestColor(
			[3]float32{cx, cy, cz},
			model, si, colorRadius, colorBuf)
		if rgba[3] < 128 {
			continue // skip translucent voxels
		}
		cells = append(cells, voxel.ActiveCell{
			Col: k.Col, Row: k.Row, Layer: k.Layer,
			Cx: cx, Cy: cy, Cz: cz,
			Color: [3]uint8{rgba[0], rgba[1], rgba[2]},
		})
	}
	finishBar(barColor, "Colored cells", fmt.Sprintf("%d cells", len(cells)), time.Since(tColor))
	if len(cells) == 0 {
		return nil, nil, nil, nil, fmt.Errorf("no active cells found")
	}

	// Build cell lookup map.
	cellAssignMap = make(map[voxel.CellKey]int, len(cells))
	for i, c := range cells {
		k := voxel.CellKey{Col: c.Col, Row: c.Row, Layer: c.Layer}
		cellAssignMap[k] = i
	}

	} // end if cached == nil

	// Build cache data to return for saving.
	var newCacheData *CacheData
	if cached == nil {
		newCacheData = &CacheData{
			Cells:         cells,
			CellAssignMap: cellAssignMap,
			MinV:          minV,
			CellSize:      cellSize,
			LayerH:        layerH,
		}
	}

	// 4. Resolve palette and assign / dither.
	pal, palDisplay := voxel.ResolvePalette(cells, pcfg, ditherMode != "none")
	if palDisplay != "" {
		fmt.Printf("%s\n", palDisplay)
	}
	if len(pal) == 0 {
		return nil, nil, nil, nil, fmt.Errorf("no palette colors")
	}
	if cfg.ColorSnap > 0 {
		voxel.SnapColors(cells, pal, cfg.ColorSnap)
		fmt.Printf("  Snapped cell colors toward palette by ΔE %.1f\n", cfg.ColorSnap)
	}
	tDither := time.Now()
	barDither := newBar(-1, fmt.Sprintf("  Dithering (%s)", ditherMode))
	var assignments []int32
	switch ditherMode {
	case "dizzy":
		assignments = voxel.DitherCellsDizzy(cells, pal)
	default:
		assignments = voxel.AssignColors(cells, pal)
	}
	finishBar(barDither, fmt.Sprintf("Dithered (%s)", ditherMode), fmt.Sprintf("%d cells", len(cells)), time.Since(tDither))

	// Print per-color usage, sorted by count descending.
	counts := make([]int, len(pal))
	for _, a := range assignments {
		counts[a]++
	}
	total := len(assignments)
	order := make([]int, len(pal))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return counts[order[a]] > counts[order[b]] })
	for _, i := range order {
		c := pal[i]
		fmt.Printf("    #%02X%02X%02X: %d cells (%.1f%%)\n", c[0], c[1], c[2], counts[i], 100*float64(counts[i])/float64(total))
	}

	// 5. Decimate the input mesh. All color info has been extracted into
	// the voxel grid, so we can simplify purely for geometry. Filter
	// transparent faces first since they won't be clipped.
	decimModel := model
	if !cfg.NoSimplify {
		var opaqueFaces [][3]uint32
		for fi := range model.Faces {
			if voxel.FaceAlpha(fi, model) >= 128 {
				opaqueFaces = append(opaqueFaces, model.Faces[fi])
			}
		}
		targetFaces := len(cells) * 2
		if targetFaces < len(opaqueFaces) {
			decVerts, decFaces := voxel.Decimate(model.Vertices, opaqueFaces, targetFaces, float64(cellSize))
			wr := voxel.CheckWatertight(decFaces)
			fmt.Printf("  Decimated mesh: %s\n", wr)
			decimModel = &loader.LoadedModel{
				Vertices: decVerts,
				Faces:    decFaces,
			}
		}
	}

	// 6. Flood fill to merge same-color cells into patches.
	tFlood := time.Now()
	patchMap, numPatches := voxel.FloodFillPatches(cells, assignments)
	fmt.Printf("  Flood fill: %d patches in %.1fs\n", numPatches, time.Since(tFlood).Seconds())

	// Build per-patch palette assignment.
	patchAssignment := make([]int32, numPatches)
	for i, c := range cells {
		k := voxel.CellKey{Col: c.Col, Row: c.Row, Layer: c.Layer}
		pid := patchMap[k]
		patchAssignment[pid] = assignments[i]
	}

	// 7. Clip mesh along patch boundaries.
	tClip := time.Now()
	barClip := newBar(-1, "  Clipping mesh")
	shellVerts, shellFaces, shellAssignments := voxel.ClipMeshByPatches(
		decimModel, patchMap, patchAssignment, minV, cellSize, layerH, false)
	finishBar(barClip, "Clipped mesh", fmt.Sprintf("%d faces", len(shellFaces)), time.Since(tClip))
	{
		wr := voxel.CheckWatertight(shellFaces)
		fmt.Printf("  After clip: %s\n", wr)
	}

	// 8. Optional coplanar merge.
	if !cfg.NoMerge {
		tMerge := time.Now()
		barMerge := newBar(-1, "  Merging shell")
		before := len(shellFaces)
		shellFaces, shellAssignments = voxel.MergeCoplanarTriangles(shellVerts, shellFaces, shellAssignments)
		finishBar(barMerge, "Merged shell", fmt.Sprintf("%d -> %d faces", before, len(shellFaces)), time.Since(tMerge))
	}
	fmt.Printf("  Output mesh: %s\n", voxel.CheckWatertight(shellFaces))

	// Build output model.
	placeholder := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	placeholder.SetNRGBA(0, 0, color.NRGBA{128, 128, 128, 255})
	var textures []image.Image
	if len(model.Textures) > 0 {
		textures = model.Textures[:1]
	} else {
		textures = []image.Image{placeholder}
	}

	outModel := &loader.LoadedModel{
		Vertices:       shellVerts,
		Faces:          shellFaces,
		UVs:            make([][2]float32, len(shellVerts)),
		Textures:       textures,
		FaceTextureIdx: make([]int32, len(shellFaces)),
	}

	return outModel, shellAssignments, pal, newCacheData, nil
}
