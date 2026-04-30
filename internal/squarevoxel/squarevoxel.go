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
	"github.com/rtwfroody/ditherforge/internal/plog"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/split"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// SplitInfo carries per-half geometry plus the forward transforms
// that produced the laid-out halves. VoxelizeTwoGrids calls
// Xform[i].ApplyInverse on each cell centroid to map bed coords
// back into original-mesh coords, where colorModel, stickerModel,
// and the sticker spatial index live unmoved.
//
// Xform is the FORWARD transform (orig → bed), not the inverse.
// The "inverse" lives in voxelize's call to ApplyInverse, not in
// the field. This matches splitOutput.Xform in docs/SPLIT.md.
//
// When SplitInfo is nil, VoxelizeTwoGrids voxelizes the single
// `model` argument with no transform (bit-identical to the
// pre-split path).
type SplitInfo struct {
	Halves [2]*loader.LoadedModel
	Xform  [2]split.Transform
}

// Cell size multipliers relative to nozzle diameter.
const (
	Layer0CellScale = 1.275 // wider cells for the first layer
	UpperCellScale  = 1.05  // standard cells for upper layers
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
// Increments counter and emits StageProgress("Voxelizing", current) once per
// 1000-face chunk. Pass progress.NullTracker{} and a discard counter to
// silence reporting.
func voxelizeRegion(
	ctx context.Context,
	model *loader.LoadedModel,
	p regionParams,
	tracker progress.Tracker,
	counter *atomic.Int64,
) map[voxel.CellKey]struct{} {
	halfExtent := [3]float32{p.CellSize / 2, p.CellSize / 2, p.LayerH / 2}
	cellSet := make(map[voxel.CellKey]struct{})
	chunk := int64(0)
	for fi := range model.Faces {
		if fi%1000 == 0 {
			if ctx.Err() != nil {
				counter.Add(chunk)
				return cellSet
			}
			counter.Add(chunk)
			chunk = 0
			tracker.StageProgress("Voxelizing", int(counter.Load()))
		}
		chunk++
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
	counter.Add(chunk)
	return cellSet
}

// colorCells samples colors for all keys in cellSet and returns ActiveCells.
//
// stickerModel/stickerSI may be nil; when non-nil and distinct from
// colorModel, decal lookups go against that mesh (alpha-wrap mode).
//
// halfIdx is recorded on every emitted cell. invXform maps the cell
// centroid (which is in bed coords) back to original-mesh coords for
// color sampling on the unmoved colorModel/stickerModel; pass
// split.IdentityTransform for the unsplit path.
func colorCells(
	ctx context.Context,
	colorModel *loader.LoadedModel,
	si *voxel.SpatialIndex,
	stickerModel *loader.LoadedModel,
	stickerSI *voxel.SpatialIndex,
	cellSet map[voxel.CellKey]struct{},
	p regionParams,
	tracker progress.Tracker,
	counter *atomic.Int64,
	decals []*voxel.StickerDecal,
	halfIdx uint8,
	invXform split.Transform,
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

	separateSticker := stickerModel != nil && stickerModel != colorModel && stickerSI != nil

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
			buf := voxel.NewSearchBuf(len(colorModel.Faces))
			var stickerBuf *voxel.SearchBuf
			if separateSticker {
				stickerBuf = voxel.NewSearchBuf(len(stickerModel.Faces))
			}
			local := make([]voxel.ActiveCell, 0, len(keys))
			for i, k := range keys {
				if i%1000 == 0 && ctx.Err() != nil {
					return
				}
				cur := counter.Add(1)
				tracker.StageProgress("Coloring cells", int(cur))
				// (cx, cy, cz) is in bed coords (the grid lives on the
				// bed). For color sampling, project back into
				// original-mesh coords via the per-half inverse
				// transform — colorModel/stickerModel are unmoved.
				cx := p.MinV[0] + float32(k.Col)*p.CellSize
				cy := p.MinV[1] + float32(k.Row)*p.CellSize
				cz := p.MinV[2] + float32(k.Layer)*p.LayerH
				samplePos := invXform.ApplyInverse([3]float32{cx, cy, cz})
				var rgba [4]uint8
				if separateSticker {
					rgba = voxel.SampleNearestColorWithSticker(
						samplePos,
						colorModel, si, colorRadius, buf, decals,
						stickerModel, stickerSI, stickerBuf)
				} else {
					rgba = voxel.SampleNearestColor(
						samplePos,
						colorModel, si, colorRadius, buf, decals)
				}
				if rgba[3] < 128 {
					continue
				}
				local = append(local, voxel.ActiveCell{
					Grid: k.Grid, Col: k.Col, Row: k.Row, Layer: k.Layer,
					Cx: cx, Cy: cy, Cz: cz,
					Color:   [3]uint8{rgba[0], rgba[1], rgba[2]},
					HalfIdx: halfIdx,
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
// layer 0 and upperSize for layers 1+. Geometry cells are marked using
// model; base colors are sampled from colorModel. Pass the same model twice
// if the caller has no separate color mesh.
//
// stickerModel/stickerSI carry decal UVs when stickers live on a different
// mesh than the color sampler — typically the alpha-wrap mesh while
// colorModel is the original textured mesh. Pass nil for both to use
// colorModel for sticker lookups (which also covers the no-sticker case).
//
// When splitInfo is non-nil, the `model` parameter is ignored; geometry
// comes from splitInfo.Halves and each cell records its halfIdx. The
// `colorModel` parameter is required (no fallback) because the geometry
// meshes are in bed coords while colorModel must be in original coords.
func VoxelizeTwoGrids(
	ctx context.Context,
	model, colorModel *loader.LoadedModel,
	stickerModel *loader.LoadedModel, stickerSI *voxel.SpatialIndex,
	layer0Size, upperSize, layerH float32,
	tracker progress.Tracker,
	decals []*voxel.StickerDecal,
	splitInfo *SplitInfo,
) (*TwoGridResult, error) {
	// Decide the geometry meshes and per-mesh inverse transforms.
	// Unsplit path (splitInfo == nil) takes the single `model`
	// argument with identity transform; split path iterates the
	// two halves with their respective inverse transforms.
	type geomEntry struct {
		mesh     *loader.LoadedModel
		invXform split.Transform
		halfIdx  uint8
	}
	var entries []geomEntry
	if splitInfo == nil {
		if model == nil || len(model.Vertices) == 0 || len(model.Faces) == 0 {
			return nil, fmt.Errorf("empty model")
		}
		entries = []geomEntry{{mesh: model, invXform: split.IdentityTransform, halfIdx: 0}}
	} else {
		for h := 0; h < 2; h++ {
			m := splitInfo.Halves[h]
			if m == nil || len(m.Vertices) == 0 || len(m.Faces) == 0 {
				return nil, fmt.Errorf("empty split half %d", h)
			}
			entries = append(entries, geomEntry{
				mesh:     m,
				invXform: splitInfo.Xform[h],
				halfIdx:  uint8(h),
			})
		}
	}

	if colorModel == nil {
		// In the unsplit path colorModel can fall back to the
		// geometry mesh; in the split path the caller must supply
		// colorModel explicitly (it lives in original coords,
		// distinct from the laid-out half meshes).
		if splitInfo != nil {
			return nil, fmt.Errorf("split voxelize requires explicit colorModel (lives in original coords, distinct from laid-out halves)")
		}
		colorModel = model
	}

	for _, e := range entries {
		if len(entries) > 1 {
			plog.Printf("  Input mesh (half %d): %s", e.halfIdx, voxel.CheckWatertight(e.mesh.Faces))
		} else {
			plog.Printf("  Input mesh: %s", voxel.CheckWatertight(e.mesh.Faces))
		}
	}

	// Bbox is the union over all geometry meshes (in bed coords for
	// the split path).
	minV, maxV := voxel.ComputeBounds(entries[0].mesh.Vertices)
	for _, e := range entries[1:] {
		mn, mx := voxel.ComputeBounds(e.mesh.Vertices)
		for i := 0; i < 3; i++ {
			minV[i] = min(minV[i], mn[i])
			maxV[i] = max(maxV[i], mx[i])
		}
	}
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
	si := voxel.NewSpatialIndex(colorModel, maxCellSize*2)

	// "Voxelizing" covers just the geometry-traversal phase here. The
	// color-sampling phase below is a separate stage ("Coloring cells")
	// with its own progress bar, so the UI shows them sequentially instead
	// of leaving "Voxelizing" running throughout. Total work is faces
	// processed across both regions (or just one if nLayers <= 1).
	regions := 1
	if nLayers > 1 {
		regions = 2
	}
	totalFaces := 0
	for _, e := range entries {
		totalFaces += len(e.mesh.Faces)
	}
	tracker.StageStart("Voxelizing", true, totalFaces*regions)
	var voxCounter atomic.Int64

	tVoxelize := time.Now()

	nCols0 := int(math.Ceil(float64(maxV[0]-minV[0])/float64(layer0Size))) + 1
	nRows0 := int(math.Ceil(float64(maxV[1]-minV[1])/float64(layer0Size))) + 1
	p0 := regionParams{
		Grid: 0, CellSize: layer0Size, LayerH: layerH,
		MinV: minV, NCols: nCols0, NRows: nRows0,
		LayerLo: 0, LayerHi: 0,
	}
	nCols1 := int(math.Ceil(float64(maxV[0]-minV[0])/float64(upperSize))) + 1
	nRows1 := int(math.Ceil(float64(maxV[1]-minV[1])/float64(upperSize))) + 1
	p1 := regionParams{
		Grid: 1, CellSize: upperSize, LayerH: layerH,
		MinV: minV, NCols: nCols1, NRows: nRows1,
		LayerLo: 1, LayerHi: nLayers - 1,
	}

	// Voxelize each geometry mesh into per-mesh region cell sets.
	type meshCells struct {
		layer0 map[voxel.CellKey]struct{}
		upper  map[voxel.CellKey]struct{}
	}
	perMesh := make([]meshCells, len(entries))
	totalCells := 0
	for i, e := range entries {
		perMesh[i].layer0 = voxelizeRegion(ctx, e.mesh, p0, tracker, &voxCounter)
		totalCells += len(perMesh[i].layer0)
		if nLayers > 1 {
			perMesh[i].upper = voxelizeRegion(ctx, e.mesh, p1, tracker, &voxCounter)
			totalCells += len(perMesh[i].upper)
		}
	}

	plog.Printf("  Voxelized: %d cells across %d mesh(es) in %.1fs",
		totalCells, len(entries), time.Since(tVoxelize).Seconds())

	tracker.StageDone("Voxelizing")

	// Color cells per mesh, threading the per-mesh inverse transform
	// so color sampling on colorModel/stickerModel happens in
	// original-mesh coordinates.
	tColor := time.Now()
	tracker.StageStart("Coloring cells", true, totalCells)
	var counter atomic.Int64

	var cells []voxel.ActiveCell
	for i, e := range entries {
		cells0, err := colorCells(ctx, colorModel, si, stickerModel, stickerSI,
			perMesh[i].layer0, p0, tracker, &counter, decals,
			e.halfIdx, e.invXform)
		if err != nil {
			return nil, err
		}
		cells = append(cells, cells0...)
		if nLayers > 1 {
			cells1, err := colorCells(ctx, colorModel, si, stickerModel, stickerSI,
				perMesh[i].upper, p1, tracker, &counter, decals,
				e.halfIdx, e.invXform)
			if err != nil {
				return nil, err
			}
			cells = append(cells, cells1...)
		}
	}

	tracker.StageDone("Coloring cells")
	plog.Printf("  Colored cells: %d cells in %.1fs", len(cells), time.Since(tColor).Seconds())
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

// Voxelize performs voxelization with a single uniform cell size. Geometry
// cells are marked using model; colors are sampled from colorModel. Pass
// the same model twice if the caller has no separate color mesh.
func Voxelize(ctx context.Context, model, colorModel *loader.LoadedModel, cellSize, layerH float32, tracker progress.Tracker, decals []*voxel.StickerDecal) ([]voxel.ActiveCell, map[voxel.CellKey]int, [3]float32, error) {
	if len(model.Vertices) == 0 || len(model.Faces) == 0 {
		return nil, nil, [3]float32{}, fmt.Errorf("empty model")
	}
	if colorModel == nil {
		colorModel = model
	}

	plog.Printf("  Input mesh: %s", voxel.CheckWatertight(model.Faces))

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

	si := voxel.NewSpatialIndex(colorModel, cellSize*2)

	// See VoxelizeTwoGrids for the rationale on splitting into two stages.
	tracker.StageStart("Voxelizing", true, len(model.Faces))
	var voxCounter atomic.Int64

	tVoxelize := time.Now()
	p := regionParams{
		Grid: 0, CellSize: cellSize, LayerH: layerH,
		MinV: minV, NCols: nCols, NRows: nRows,
		LayerLo: 0, LayerHi: nLayers - 1,
	}
	cellSet := voxelizeRegion(ctx, model, p, tracker, &voxCounter)
	plog.Printf("  Voxelized: %d cells in %.1fs", len(cellSet), time.Since(tVoxelize).Seconds())

	tracker.StageDone("Voxelizing")

	tColor := time.Now()
	tracker.StageStart("Coloring cells", true, len(cellSet))
	var counter atomic.Int64
	cells, err := colorCells(ctx, model, si, nil, nil, cellSet, p, tracker, &counter, decals, 0, split.IdentityTransform)
	if err != nil {
		return nil, nil, [3]float32{}, err
	}
	tracker.StageDone("Coloring cells")
	plog.Printf("  Colored cells: %d cells in %.1fs", len(cells), time.Since(tColor).Seconds())
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

// CountSurfaceCells quickly counts the number of surface voxel cells for the
// given model and grid parameters, without performing color sampling.
func CountSurfaceCells(ctx context.Context, model *loader.LoadedModel, nozzleDiameter, layerHeight float32) int {
	cellSize := nozzleDiameter * UpperCellScale

	minV, maxV := voxel.ComputeBounds(model.Vertices)
	xyPad := cellSize * 2
	zPad := layerHeight * 2
	minV[0] -= xyPad
	minV[1] -= xyPad
	minV[2] -= zPad
	maxV[0] += xyPad
	maxV[1] += xyPad
	maxV[2] += zPad

	nCols := int(math.Ceil(float64(maxV[0]-minV[0])/float64(cellSize))) + 1
	nRows := int(math.Ceil(float64(maxV[1]-minV[1])/float64(cellSize))) + 1
	nLayers := int(math.Ceil(float64(maxV[2]-minV[2])/float64(layerHeight))) + 1

	p := regionParams{
		Grid: 0, CellSize: cellSize, LayerH: layerHeight,
		MinV: minV, NCols: nCols, NRows: nRows,
		LayerLo: 0, LayerHi: nLayers - 1,
	}
	var discard atomic.Int64
	cellSet := voxelizeRegion(ctx, model, p, progress.NullTracker{}, &discard)
	return len(cellSet)
}

// DecimateMesh simplifies the model geometry purely based on shape, targeting
// roughly one triangle per surface voxel cell.
//
// Emits its own "Decimating" stage events (including a progress bar when
// decimation is actually performed). The caller should not also emit
// StageStart/StageDone for this stage.
func DecimateMesh(ctx context.Context, model *loader.LoadedModel, targetCells int, cellSize float32, noSimplify bool, tracker progress.Tracker) (*loader.LoadedModel, error) {
	if noSimplify {
		tracker.StageStart("Decimating", false, 0)
		tracker.StageDone("Decimating")
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
	if targetCells < len(opaqueFaces) {
		tracker.StageStart("Decimating", true, len(opaqueFaces)-targetCells)
		defer tracker.StageDone("Decimating")

		decVerts, decFaces, err := voxel.Decimate(ctx, model.Vertices, opaqueFaces, targetCells, float64(cellSize), tracker)
		if err != nil {
			return nil, err
		}
		wr := voxel.CheckWatertight(decFaces)
		plog.Printf("  Decimated mesh: %s", wr)
		return &loader.LoadedModel{
			Vertices: decVerts,
			Faces:    decFaces,
		}, nil
	}
	tracker.StageStart("Decimating", false, 0)
	tracker.StageDone("Decimating")
	return model, nil
}

// DecimateHalves runs DecimateMesh once per Split half, splitting the
// total target cell count between halves proportional to each half's
// face count. Used by the StageSplit-aware pipeline path; the
// unsplit path keeps using DecimateMesh directly.
//
// Each half is closed-watertight in its own right (post-Layout), so
// the underlying voxel.Decimate runs unmodified. Cap planarity is
// preserved by QEM's planar-affinity bias: collapsing a
// cap-perimeter vertex moves it off the cap plane, which is high
// quadric error and is disfavored by the heap. (Verified by
// TestDecimate_HalfPreservesCapPlanarity.)
func DecimateHalves(ctx context.Context, halves [2]*loader.LoadedModel, totalTargetCells int, cellSize float32, noSimplify bool, tracker progress.Tracker) ([2]*loader.LoadedModel, error) {
	// split.Cut's contract guarantees both halves are non-nil; we rely
	// on that here rather than guarding for nil.
	totalFaces := len(halves[0].Faces) + len(halves[1].Faces)
	var out [2]*loader.LoadedModel
	for i, h := range halves {
		// Proportional split with a floor of 1 (avoid degenerate
		// "decimate to 0 faces" requests).
		perHalfTarget := totalTargetCells * len(h.Faces) / totalFaces
		if perHalfTarget < 1 {
			perHalfTarget = 1
		}
		decimated, err := DecimateMesh(ctx, h, perHalfTarget, cellSize, noSimplify, tracker)
		if err != nil {
			return out, fmt.Errorf("decimate half %d: %w", i, err)
		}
		out[i] = decimated
	}
	return out, nil
}
