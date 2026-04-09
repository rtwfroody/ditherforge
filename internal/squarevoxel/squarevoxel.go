// Package squarevoxel generates a colored mesh by voxelizing the input model
// for color decisions, then clipping the original mesh along color patch
// boundaries.
package squarevoxel

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"sync"
	"time"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// Voxelize performs voxelization and cell coloring on the input model.
// Returns the active cells, a cell-key-to-index map, and the grid minimum
// vertex position.
func Voxelize(ctx context.Context, model *loader.LoadedModel, cellSize, layerH float32) ([]voxel.ActiveCell, map[voxel.CellKey]int, [3]float32, error) {
	if len(model.Vertices) == 0 || len(model.Faces) == 0 {
		return nil, nil, [3]float32{}, fmt.Errorf("empty model")
	}

	fmt.Printf("  Input mesh: %s\n", voxel.CheckWatertight(model.Faces))

	// 1. Bounding box.
	var minV, maxV [3]float32
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
	bar := progress.NewBar(len(model.Faces), "  Voxelizing")
	halfExtent := [3]float32{cellSize / 2, cellSize / 2, layerH / 2}
	cellSet := make(map[voxel.CellKey]struct{})
	for fi := range model.Faces {
		if fi%1000 == 0 && ctx.Err() != nil {
			return nil, nil, [3]float32{}, ctx.Err()
		}
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
	progress.FinishBar(bar, "Voxelized", fmt.Sprintf("%d cells", len(cellSet)), time.Since(tVoxelize))

	// Color each active cell (parallelized).
	tColor := time.Now()
	colorRadius := cellSize * 3
	barColor := progress.NewBar(len(cellSet), "  Coloring cells")

	// Convert map to slice for indexed partitioning.
	cellKeys := make([]voxel.CellKey, 0, len(cellSet))
	for k := range cellSet {
		cellKeys = append(cellKeys, k)
	}

	numWorkers := runtime.NumCPU()
	if numWorkers > len(cellKeys) {
		numWorkers = len(cellKeys)
	}
	if numWorkers < 1 {
		numWorkers = 1
	}
	workerCells := make([][]voxel.ActiveCell, numWorkers)
	var wg sync.WaitGroup
	chunkSize := (len(cellKeys) + numWorkers - 1) / numWorkers

	for w := range numWorkers {
		lo := w * chunkSize
		hi := lo + chunkSize
		if hi > len(cellKeys) {
			hi = len(cellKeys)
		}
		if lo >= hi {
			continue
		}
		wg.Add(1)
		go func(workerIdx int, keys []voxel.CellKey) {
			defer wg.Done()
			buf := voxel.NewSearchBuf(len(model.Faces))
			local := make([]voxel.ActiveCell, 0, len(keys))
			for i, k := range keys {
				if i%1000 == 0 && ctx.Err() != nil {
					return
				}
				barColor.Add(1)
				cx := minV[0] + float32(k.Col)*cellSize
				cy := minV[1] + float32(k.Row)*cellSize
				cz := minV[2] + float32(k.Layer)*layerH
				rgba := voxel.SampleNearestColor(
					[3]float32{cx, cy, cz},
					model, si, colorRadius, buf)
				if rgba[3] < 128 {
					continue
				}
				local = append(local, voxel.ActiveCell{
					Col: k.Col, Row: k.Row, Layer: k.Layer,
					Cx: cx, Cy: cy, Cz: cz,
					Color: [3]uint8{rgba[0], rgba[1], rgba[2]},
				})
			}
			workerCells[workerIdx] = local
		}(w, cellKeys[lo:hi])
	}
	wg.Wait()

	if ctx.Err() != nil {
		return nil, nil, [3]float32{}, ctx.Err()
	}

	// Concatenate per-worker results.
	var cells []voxel.ActiveCell
	for _, wc := range workerCells {
		cells = append(cells, wc...)
	}
	progress.FinishBar(barColor, "Colored cells", fmt.Sprintf("%d cells", len(cells)), time.Since(tColor))
	if len(cells) == 0 {
		return nil, nil, [3]float32{}, fmt.Errorf("no active cells found")
	}

	// Build cell lookup map.
	cellAssignMap := make(map[voxel.CellKey]int, len(cells))
	for i, c := range cells {
		k := voxel.CellKey{Col: c.Col, Row: c.Row, Layer: c.Layer}
		cellAssignMap[k] = i
	}

	return cells, cellAssignMap, minV, nil
}

// DecimateMesh simplifies the model geometry. All color info has been
// extracted into the voxel grid, so we can simplify purely for geometry.
func DecimateMesh(ctx context.Context, model *loader.LoadedModel, cells []voxel.ActiveCell, cellSize float32, noSimplify bool) (*loader.LoadedModel, error) {
	if noSimplify {
		return model, nil
	}

	var opaqueFaces [][3]uint32
	for fi := range model.Faces {
		if voxel.FaceAlpha(fi, model) >= 128 {
			opaqueFaces = append(opaqueFaces, model.Faces[fi])
		}
	}
	// Target ~1 triangle per surface cell. Clipping recreates geometry
	// at voxel boundaries, so the decimated mesh just needs a rough hull.
	targetFaces := len(cells)
	if targetFaces < len(opaqueFaces) {
		decVerts, decFaces, err := voxel.Decimate(ctx, model.Vertices, opaqueFaces, targetFaces, float64(cellSize))
		if err != nil {
			return nil, err
		}
		wr := voxel.CheckWatertight(decFaces)
		fmt.Printf("  Decimated mesh: %s\n", wr)
		return &loader.LoadedModel{
			Vertices: decVerts,
			Faces:    decFaces,
		}, nil
	}
	return model, nil
}

