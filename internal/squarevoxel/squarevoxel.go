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
	"sync/atomic"
	"time"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// regionParams holds parameters for voxelizing a Z-range of the model on a
// specific XY grid.
type regionParams struct {
	Grid     uint8
	CellSize float32
	LayerH   float32
	MinV     [3]float32
	NCols    int
	NRows    int
	LayerLo  int // inclusive
	LayerHi  int // inclusive
}

// voxelizeRegion finds cell keys that overlap the model in the given Z-range.
func voxelizeRegion(
	ctx context.Context,
	model *loader.LoadedModel,
	p regionParams,
) map[voxel.CellKey]struct{} {
	halfExtent := [3]float32{p.CellSize / 2, p.CellSize / 2, p.LayerH / 2}
	cellSet := make(map[voxel.CellKey]struct{})
	for fi := range model.Faces {
		if fi%1000 == 0 && ctx.Err() != nil {
			return cellSet
		}
		f := model.Faces[fi]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]
		tMin := [3]float32{
			min(v0[0], min(v1[0], v2[0])),
			min(v0[1], min(v1[1], v2[1])),
			min(v0[2], min(v1[2], v2[2])),
		}
		tMax := [3]float32{
			max(v0[0], max(v1[0], v2[0])),
			max(v0[1], max(v1[1], v2[1])),
			max(v0[2], max(v1[2], v2[2])),
		}
		colMin, colMax, rowMin, rowMax, layerMin, layerMax := voxel.AABBCellRange(tMin, tMax, p.MinV, p.CellSize, p.LayerH)
		colMin = max(colMin, 0)
		colMax = min(colMax, p.NCols-1)
		rowMin = max(rowMin, 0)
		rowMax = min(rowMax, p.NRows-1)
		layerMin = max(layerMin, p.LayerLo)
		layerMax = min(layerMax, p.LayerHi)
		for col := colMin; col <= colMax; col++ {
			cx := p.MinV[0] + float32(col)*p.CellSize
			for row := rowMin; row <= rowMax; row++ {
				cy := p.MinV[1] + float32(row)*p.CellSize
				for layer := layerMin; layer <= layerMax; layer++ {
					cz := p.MinV[2] + float32(layer)*p.LayerH
					center := [3]float32{cx, cy, cz}
					if voxel.TriangleAABBOverlap(v0, v1, v2, center, halfExtent) {
						cellSet[voxel.CellKey{Grid: p.Grid, Col: col, Row: row, Layer: layer}] = struct{}{}
					}
				}
			}
		}
	}
	return cellSet
}

// colorCells samples colors for all keys in cellSet and returns ActiveCells.
func colorCells(
	ctx context.Context,
	model *loader.LoadedModel,
	si *voxel.SpatialIndex,
	cellSet map[voxel.CellKey]struct{},
	p regionParams,
	tracker progress.Tracker,
	counter *atomic.Int64,
) ([]voxel.ActiveCell, error) {
	colorRadius := p.CellSize * 3
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
				cur := counter.Add(1)
				tracker.StageProgress("Coloring cells", int(cur))
				cx := p.MinV[0] + float32(k.Col)*p.CellSize
				cy := p.MinV[1] + float32(k.Row)*p.CellSize
				cz := p.MinV[2] + float32(k.Layer)*p.LayerH
				rgba := voxel.SampleNearestColor(
					[3]float32{cx, cy, cz},
					model, si, colorRadius, buf)
				if rgba[3] < 128 {
					continue
				}
				local = append(local, voxel.ActiveCell{
					Grid: k.Grid, Col: k.Col, Row: k.Row, Layer: k.Layer,
					Cx: cx, Cy: cy, Cz: cz,
					Color: [3]uint8{rgba[0], rgba[1], rgba[2]},
				})
			}
			workerCells[workerIdx] = local
		}(w, cellKeys[lo:hi])
	}
	wg.Wait()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var cells []voxel.ActiveCell
	for _, wc := range workerCells {
		cells = append(cells, wc...)
	}
	return cells, nil
}

// TwoGridResult holds the output from VoxelizeTwoGrids.
type TwoGridResult struct {
	Cells         []voxel.ActiveCell
	CellAssignMap map[voxel.CellKey]int
	MinV          [3]float32
	Layer0Size    float32
	UpperSize     float32
	LayerH        float32
}

// VoxelizeTwoGrids voxelizes the model with two XY cell sizes: layer0Size for
// layer 0 and upperSize for layers 1+.
func VoxelizeTwoGrids(ctx context.Context, model *loader.LoadedModel, layer0Size, upperSize, layerH float32, tracker progress.Tracker) (*TwoGridResult, error) {
	if len(model.Vertices) == 0 || len(model.Faces) == 0 {
		return nil, fmt.Errorf("empty model")
	}

	fmt.Printf("  Input mesh: %s\n", voxel.CheckWatertight(model.Faces))

	minV, maxV := voxel.ComputeBounds(model.Vertices)
	maxCellSize := max(layer0Size, upperSize)
	xyPad := maxCellSize * 2
	zPad := layerH * 2
	minV[0] -= xyPad
	minV[1] -= xyPad
	// Minimal downward Z padding — normalizeZ places the model bottom at
	// z=0 so layer 0 covers the first layer, but a tiny epsilon guards
	// against floating-point geometry slightly below z=0.
	minV[2] -= layerH * 0.01
	maxV[0] += xyPad
	maxV[1] += xyPad
	maxV[2] += zPad

	nLayers := int(math.Ceil(float64(maxV[2]-minV[2])/float64(layerH))) + 1
	si := voxel.NewSpatialIndex(model, maxCellSize*2)

	tVoxelize := time.Now()

	// First layer: grid 0 (wide voxels)
	nCols0 := int(math.Ceil(float64(maxV[0]-minV[0])/float64(layer0Size))) + 1
	nRows0 := int(math.Ceil(float64(maxV[1]-minV[1])/float64(layer0Size))) + 1
	p0 := regionParams{
		Grid: 0, CellSize: layer0Size, LayerH: layerH,
		MinV: minV, NCols: nCols0, NRows: nRows0,
		LayerLo: 0, LayerHi: 0,
	}
	cellSet0 := voxelizeRegion(ctx, model, p0)

	// Remaining layers: grid 1 (narrow voxels)
	var cellSet1 map[voxel.CellKey]struct{}
	nCols1 := int(math.Ceil(float64(maxV[0]-minV[0])/float64(upperSize))) + 1
	nRows1 := int(math.Ceil(float64(maxV[1]-minV[1])/float64(upperSize))) + 1
	if nLayers > 1 {
		p1 := regionParams{
			Grid: 1, CellSize: upperSize, LayerH: layerH,
			MinV: minV, NCols: nCols1, NRows: nRows1,
			LayerLo: 1, LayerHi: nLayers - 1,
		}
		cellSet1 = voxelizeRegion(ctx, model, p1)
	}

	totalCells := len(cellSet0) + len(cellSet1)
	fmt.Printf("  Voxelized: %d cells (layer0: %d, upper: %d) in %.1fs\n",
		totalCells, len(cellSet0), len(cellSet1), time.Since(tVoxelize).Seconds())

	// Color cells
	tColor := time.Now()
	tracker.StageStart("Coloring cells", true, totalCells)
	var counter atomic.Int64

	cells0, err := colorCells(ctx, model, si, cellSet0, p0, tracker, &counter)
	if err != nil {
		return nil, err
	}
	cells1, err := colorCells(ctx, model, si, cellSet1, regionParams{
		Grid: 1, CellSize: upperSize, LayerH: layerH,
		MinV: minV, NCols: nCols1, NRows: nRows1,
		LayerLo: 1, LayerHi: nLayers - 1,
	}, tracker, &counter)
	if err != nil {
		return nil, err
	}

	cells := append(cells0, cells1...)
	tracker.StageDone("Coloring cells")
	fmt.Printf("  Colored cells: %d cells in %.1fs\n", len(cells), time.Since(tColor).Seconds())
	if len(cells) == 0 {
		return nil, fmt.Errorf("no active cells found")
	}

	cellAssignMap := make(map[voxel.CellKey]int, len(cells))
	for i, c := range cells {
		k := voxel.CellKey{Grid: c.Grid, Col: c.Col, Row: c.Row, Layer: c.Layer}
		cellAssignMap[k] = i
	}

	return &TwoGridResult{
		Cells:         cells,
		CellAssignMap: cellAssignMap,
		MinV:          minV,
		Layer0Size:    layer0Size,
		UpperSize:     upperSize,
		LayerH:        layerH,
	}, nil
}

// Voxelize performs voxelization with a single uniform cell size.
func Voxelize(ctx context.Context, model *loader.LoadedModel, cellSize, layerH float32, tracker progress.Tracker) ([]voxel.ActiveCell, map[voxel.CellKey]int, [3]float32, error) {
	if len(model.Vertices) == 0 || len(model.Faces) == 0 {
		return nil, nil, [3]float32{}, fmt.Errorf("empty model")
	}

	fmt.Printf("  Input mesh: %s\n", voxel.CheckWatertight(model.Faces))

	minV, maxV := voxel.ComputeBounds(model.Vertices)
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

	si := voxel.NewSpatialIndex(model, cellSize*2)

	tVoxelize := time.Now()
	p := regionParams{
		Grid: 0, CellSize: cellSize, LayerH: layerH,
		MinV: minV, NCols: nCols, NRows: nRows,
		LayerLo: 0, LayerHi: nLayers - 1,
	}
	cellSet := voxelizeRegion(ctx, model, p)
	fmt.Printf("  Voxelized: %d cells in %.1fs\n", len(cellSet), time.Since(tVoxelize).Seconds())

	tColor := time.Now()
	tracker.StageStart("Coloring cells", true, len(cellSet))
	var counter atomic.Int64
	cells, err := colorCells(ctx, model, si, cellSet, p, tracker, &counter)
	if err != nil {
		return nil, nil, [3]float32{}, err
	}
	tracker.StageDone("Coloring cells")
	fmt.Printf("  Colored cells: %d cells in %.1fs\n", len(cells), time.Since(tColor).Seconds())
	if len(cells) == 0 {
		return nil, nil, [3]float32{}, fmt.Errorf("no active cells found")
	}

	cellAssignMap := make(map[voxel.CellKey]int, len(cells))
	for i, c := range cells {
		k := voxel.CellKey{Grid: c.Grid, Col: c.Col, Row: c.Row, Layer: c.Layer}
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
