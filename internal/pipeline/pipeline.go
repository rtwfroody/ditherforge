// Package pipeline implements the core ditherforge processing pipeline:
// load model, validate, remesh, and export to 3MF.
package pipeline

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rtwfroody/ditherforge/internal/export3mf"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/squarevoxel"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// Options controls the pipeline behavior. Mirrors CLI flags.
type Options struct {
	Input          string
	NumColors      int
	LockedColors   []string
	Scale          float32
	Output         string
	NozzleDiameter float32
	LayerHeight    float32
	InventoryFile    string
	InventoryColors  [][3]uint8 `json:"InventoryColors,omitempty"`
	InventoryLabels  []string   `json:"InventoryLabels,omitempty"` // parallel to InventoryColors
	Brightness     float32
	Contrast       float32
	Saturation     float32
	Dither         string
	NoMerge        bool
	NoSimplify     bool
	UniformGrid    bool
	Size           *float32
	Force          bool
	ReloadSeq      int64 // bumped to force re-read of the same input file
	Stats          bool
	ColorSnap      float64
	WarpPins       []WarpPin `json:"WarpPins,omitempty"`
}

// WarpPin maps a source image color to a target filament color for RBF warping.
type WarpPin struct {
	SourceHex string  `json:"sourceHex"` // e.g. "#FF0000"
	TargetHex string  `json:"targetHex"` // e.g. "#00FF00"
	Sigma     float64 `json:"sigma"`     // falloff in delta-E units; 0 = auto
}

// Callbacks groups optional callbacks for a pipeline run.
type Callbacks struct {
	OnInputMesh func(*MeshData)
	OnPalette   func([][3]uint8, []string)
	Progress    progress.Tracker
}

// stageNames maps StageID to a human-readable name for progress reporting.
var stageNames = map[StageID]string{
	StageLoad:        "Loading",
	StageVoxelize:    "Voxelizing",
	StageDecimate:    "Decimating",
	StageColorAdjust: "Adjusting colors",
	StageColorWarp:   "Warping colors",
	StagePalette:     "Building palette",
	StageDither:      "Dithering",
	StageClip:        "Clipping",
	StageMerge:       "Merging",
}

// MeshData holds flat arrays for 3D preview rendering.
type MeshData struct {
	Vertices       []float32 `json:"Vertices"`                 // flat [x,y,z, x,y,z, ...]
	Faces          []uint32  `json:"Faces"`                    // flat [i,j,k, i,j,k, ...]
	FaceColors     []uint16  `json:"FaceColors"`               // flat [r,g,b, r,g,b, ...] per face (uint16 to avoid base64 JSON encoding of []uint8)
	UVs            []float32 `json:"UVs,omitempty"`            // flat [u,v, u,v, ...] per vertex, optional
	Textures       []string  `json:"Textures,omitempty"`       // base64 JPEG images, optional
	FaceTextureIdx []int32   `json:"FaceTextureIdx,omitempty"` // per-face texture index; -1 = use FaceColors
}

// ProcessResult summarizes a completed pipeline run (stages 0–6, no file export).
type ProcessResult struct {
	NeedsForce    bool
	ModelExtentMM float32
	OutputMesh    *MeshData `json:"-"` // sent async via events, not in JSON response
	Duration      time.Duration
}

// PrepareResult summarizes the Prepare phase (kept for CLI backward compat).
type PrepareResult struct {
	NeedsForce    bool    // true if model exceeds size limit and Force was not set
	ModelExtentMM float32 // actual extent when NeedsForce is true
}

// Result summarizes a completed pipeline run (kept for CLI backward compat).
type Result struct {
	OutputPath string
	FaceCount  int
	Duration   time.Duration
	OutputMesh *MeshData
}

// RunCached executes the pipeline using per-stage caching. Only stages whose
// settings changed (or whose dependencies changed) are re-executed.
// The optional cb.OnPalette callback is called with the resolved palette colors
// on every run (including when the palette stage is served from cache),
// allowing callers to update the UI before later stages finish.
func RunCached(ctx context.Context, cache *StageCache, opts Options, cb *Callbacks) (*ProcessResult, error) {
	// Validate inputs before any expensive work.
	switch opts.Dither {
	case "none", "dizzy":
	default:
		return nil, fmt.Errorf("invalid --dither %q: must be none or dizzy", opts.Dither)
	}

	// Extract callbacks, using safe defaults for nil.
	var onInputMesh func(*MeshData)
	var onPalette func([][3]uint8, []string)
	var tracker progress.Tracker = progress.NullTracker{}
	if cb != nil {
		onInputMesh = cb.OnInputMesh
		onPalette = cb.OnPalette
		if cb.Progress != nil {
			tracker = cb.Progress
		}
	}

	startFrom := cache.Invalidate(opts)
	start := time.Now()

	// Emit instant start+done for cached (skipped) stages so the UI shows
	// them as completed.
	for s := StageID(0); s < startFrom && s < numStages; s++ {
		if name, ok := stageNames[s]; ok {
			tracker.StageStart(name, false, 0)
			tracker.StageDone(name)
		}
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Stage 0: Load
	if startFrom <= StageLoad {
		tracker.StageStart(stageNames[StageLoad], false, 0)
		if err := runLoad(ctx, cache, opts); err != nil {
			return nil, err
		}
		tracker.StageDone(stageNames[StageLoad])
	}
	lo := cache.getLoad()

	if onInputMesh != nil && lo.InputMesh != nil {
		// Send input mesh at preview scale so it matches the input viewer's
		// coordinate space. We copy and rescale rather than mutating the cache.
		mesh := lo.InputMesh
		if lo.PreviewScale != 1 {
			scaled := *mesh
			scaled.Vertices = make([]float32, len(mesh.Vertices))
			copy(scaled.Vertices, mesh.Vertices)
			for i := range scaled.Vertices {
				scaled.Vertices[i] *= lo.PreviewScale
			}
			mesh = &scaled
		}
		onInputMesh(mesh)
	}

	// Force check (between load and voxelize).
	if !opts.Force {
		ext := modelMaxExtent(lo.Model)
		if ext > 300 {
			return &ProcessResult{
				NeedsForce:    true,
				ModelExtentMM: ext,
			}, nil
		}
	}

	// Stage 1: Voxelize
	if startFrom <= StageVoxelize {
		tracker.StageStart(stageNames[StageVoxelize], false, 0)
		if err := runVoxelize(ctx, cache, opts, lo, tracker); err != nil {
			return nil, err
		}
		tracker.StageDone(stageNames[StageVoxelize])
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	vo := cache.getVoxelize()

	// Stage 2: Decimate
	if startFrom <= StageDecimate {
		tracker.StageStart(stageNames[StageDecimate], false, 0)
		if err := runDecimate(ctx, cache, opts, lo, vo); err != nil {
			return nil, err
		}
		tracker.StageDone(stageNames[StageDecimate])
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Stage 3: Color adjustment
	if startFrom <= StageColorAdjust {
		tracker.StageStart(stageNames[StageColorAdjust], false, 0)
		if err := runColorAdjust(ctx, cache, opts, vo); err != nil {
			return nil, err
		}
		tracker.StageDone(stageNames[StageColorAdjust])
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	cao := cache.getColorAdjust()

	// Stage 4: Color warp (RBF-based color space warping)
	if startFrom <= StageColorWarp {
		tracker.StageStart(stageNames[StageColorWarp], false, 0)
		if err := runColorWarp(ctx, cache, opts, cao); err != nil {
			return nil, err
		}
		tracker.StageDone(stageNames[StageColorWarp])
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	cwo := cache.getColorWarp()

	// Stage 5: Palette + snap colors
	if startFrom <= StagePalette {
		tracker.StageStart(stageNames[StagePalette], false, 0)
		if err := runPalette(ctx, cache, opts, cwo, tracker); err != nil {
			return nil, err
		}
		tracker.StageDone(stageNames[StagePalette])
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	po := cache.getPalette()

	if onPalette != nil {
		onPalette(po.Palette, po.PaletteLabels)
	}

	// Stage 6: Dither + flood fill
	if startFrom <= StageDither {
		tracker.StageStart(stageNames[StageDither], false, 0)
		if err := runDither(ctx, cache, opts, po, vo); err != nil {
			return nil, err
		}
		tracker.StageDone(stageNames[StageDither])
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	do := cache.getDither()

	// Stage 7: Clip
	if startFrom <= StageClip {
		tracker.StageStart(stageNames[StageClip], false, 0)
		if err := runClip(ctx, cache, opts, do, cache.getDecimate(), vo); err != nil {
			return nil, err
		}
		tracker.StageDone(stageNames[StageClip])
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Stage 8: Merge
	if startFrom <= StageMerge {
		tracker.StageStart(stageNames[StageMerge], false, 0)
		if err := runMerge(ctx, cache, opts); err != nil {
			return nil, err
		}
		tracker.StageDone(stageNames[StageMerge])
	}

	mo := cache.getMerge()

	// Build output preview mesh from merge result + palette.
	// Scale vertices to match the preview's coordinate space so both
	// viewers use the same scale.
	outModel := buildOutputModel(lo.Model, mo)
	outputMesh := buildMeshData(outModel, mo.ShellAssignments, po.Palette)
	if lo.PreviewScale != 1 {
		for i := range outputMesh.Vertices {
			outputMesh.Vertices[i] *= lo.PreviewScale
		}
	}

	if opts.Stats {
		printStats(mo.ShellAssignments, po.Palette)
	}

	return &ProcessResult{
		OutputMesh: outputMesh,
		Duration:   time.Since(start),
	}, nil
}

// Run executes the full pipeline with a fresh cache, then exports the file.
// Convenience wrapper for CLI.
func Run(ctx context.Context, opts Options) (*PrepareResult, *Result, error) {
	// Validate output before doing any work.
	outputExt := strings.ToLower(filepath.Ext(opts.Output))
	if outputExt != ".3mf" {
		return nil, nil, fmt.Errorf("output must be .3mf, got %q", outputExt)
	}

	cache := NewStageCache()
	pr, err := RunCached(ctx, cache, opts, &Callbacks{
		Progress: progress.NewCLITracker(),
	})
	if err != nil {
		return nil, nil, err
	}
	if pr.NeedsForce {
		return &PrepareResult{
			NeedsForce:    true,
			ModelExtentMM: pr.ModelExtentMM,
		}, nil, nil
	}

	faceCount, err := ExportFile(cache, opts.Output, opts.LayerHeight)
	if err != nil {
		return nil, nil, err
	}

	prepResult := &PrepareResult{}
	result := &Result{
		OutputPath: opts.Output,
		FaceCount:  faceCount,
		Duration:   pr.Duration,
		OutputMesh: pr.OutputMesh,
	}
	return prepResult, result, nil
}

// ExportFile writes a 3MF file using cached pipeline results.
// Returns the number of faces in the output.
func ExportFile(cache *StageCache, outputPath string, layerHeight float32) (int, error) {
	lo := cache.getLoad()
	po := cache.getPalette()
	mo := cache.getMerge()
	if lo == nil || po == nil || mo == nil {
		return 0, fmt.Errorf("pipeline has not been run yet")
	}

	outModel := buildOutputModel(lo.Model, mo)

	fmt.Printf("Exporting %s...", outputPath)
	tExport := time.Now()
	if err := export3mf.Export(outModel, mo.ShellAssignments, outputPath, po.Palette, layerHeight); err != nil {
		return 0, fmt.Errorf("exporting 3MF: %w", err)
	}
	fmt.Printf(" done in %.1fs\n", time.Since(tExport).Seconds())

	return len(outModel.Faces), nil
}

// buildOutputModel constructs a LoadedModel from merge output, suitable for
// export or preview mesh building.
func buildOutputModel(srcModel *loader.LoadedModel, mo *mergeOutput) *loader.LoadedModel {
	placeholder := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	placeholder.SetNRGBA(0, 0, color.NRGBA{128, 128, 128, 255})
	var textures []image.Image
	if len(srcModel.Textures) > 0 {
		textures = srcModel.Textures[:1]
	} else {
		textures = []image.Image{placeholder}
	}

	return &loader.LoadedModel{
		Vertices:       mo.ShellVerts,
		Faces:          mo.ShellFaces,
		UVs:            make([][2]float32, len(mo.ShellVerts)),
		Textures:       textures,
		FaceTextureIdx: make([]int32, len(mo.ShellFaces)),
	}
}

// --- Per-stage helpers ---

func runLoad(ctx context.Context, cache *StageCache, opts Options) error {
	inputExt := strings.ToLower(filepath.Ext(opts.Input))
	unitScale := unitScaleForExt(inputExt)
	scale := unitScale * opts.Scale

	fmt.Printf("Loading %s...", opts.Input)
	tLoad := time.Now()
	model, err := loadModel(opts.Input, scale)
	if err != nil {
		return fmt.Errorf("loading %s: %w", inputExt, err)
	}
	fmt.Printf(" %d vertices, %d faces in %.1fs\n", len(model.Vertices), len(model.Faces), time.Since(tLoad).Seconds())

	// Track the total scale applied so we can convert output mesh
	// vertices back to preview scale (which uses unitScale only).
	totalScale := scale

	// Auto-scale to --size if specified.
	if opts.Size != nil {
		ext := modelMaxExtent(model)
		if ext != *opts.Size {
			rescale := *opts.Size / ext
			totalScale = scale * rescale
			fmt.Printf("  Rescaling to %.0f mm...", *opts.Size)
			tRescale := time.Now()
			model, err = loadModel(opts.Input, totalScale)
			if err != nil {
				return fmt.Errorf("loading %s (rescaled): %w", inputExt, err)
			}
			fmt.Printf(" done in %.1fs\n", time.Since(tRescale).Seconds())
		}
	}

	// Normalize Z so the model bottom sits at z=0. This ensures the
	// first voxel layer aligns with grid layer 0.
	normalizeZ(model)

	ex := modelExtents(model)
	fmt.Printf("  Extent: %.1f x %.1f x %.1f mm\n", ex[0], ex[1], ex[2])

	if ctx.Err() != nil {
		return ctx.Err()
	}

	cache.setStage(StageLoad, stageKey(StageLoad, opts), &loadOutput{
		Model:        model,
		InputMesh:    buildInputMeshData(model),
		PreviewScale: unitScale / totalScale,
	})
	return nil
}

func runVoxelize(ctx context.Context, cache *StageCache, opts Options, lo *loadOutput, tracker progress.Tracker) error {
	layer0Size := opts.NozzleDiameter * 1.275
	upperSize := opts.NozzleDiameter * 1.05
	layerH := opts.LayerHeight
	twoGrid := !opts.UniformGrid

	fmt.Println("Voxelizing...")
	if twoGrid {
		result, err := squarevoxel.VoxelizeTwoGrids(ctx, lo.Model, layer0Size, upperSize, layerH, tracker)
		if err != nil {
			return fmt.Errorf("voxelize: %w", err)
		}
		cache.setStage(StageVoxelize, stageKey(StageVoxelize, opts), &voxelizeOutput{
			Cells:         result.Cells,
			CellAssignMap: result.CellAssignMap,
			MinV:          result.MinV,
			Layer0Size:    layer0Size,
			UpperSize:     upperSize,
			LayerH:        layerH,
			TwoGrid:       true,
		})
	} else {
		cellSize := layer0Size
		cells, cellAssignMap, minV, err := squarevoxel.Voxelize(ctx, lo.Model, cellSize, layerH, tracker)
		if err != nil {
			return fmt.Errorf("voxelize: %w", err)
		}
		cache.setStage(StageVoxelize, stageKey(StageVoxelize, opts), &voxelizeOutput{
			Cells:         cells,
			CellAssignMap: cellAssignMap,
			MinV:          minV,
			Layer0Size:    cellSize,
			UpperSize:     cellSize,
			LayerH:        layerH,
			TwoGrid:       false,
		})
	}
	return nil
}

func runColorAdjust(ctx context.Context, cache *StageCache, opts Options, vo *voxelizeOutput) error {
	adj := voxel.ColorAdjustment{
		Brightness: opts.Brightness,
		Contrast:   opts.Contrast,
		Saturation: opts.Saturation,
	}
	tAdj := time.Now()
	cells, err := voxel.AdjustCellColors(ctx, vo.Cells, adj)
	if err != nil {
		return err
	}
	if !adj.IsIdentity() {
		fmt.Printf("  Adjusted colors (B:%+.0f C:%+.0f S:%+.0f) in %.1fs\n",
			opts.Brightness, opts.Contrast, opts.Saturation, time.Since(tAdj).Seconds())
	}

	cache.setStage(StageColorAdjust, stageKey(StageColorAdjust, opts), &colorAdjustOutput{
		Cells: cells,
	})
	return nil
}

func runColorWarp(ctx context.Context, cache *StageCache, opts Options, cao *colorAdjustOutput) error {
	if len(opts.WarpPins) == 0 {
		// Pass through — copy cells to avoid aliasing cached output.
		out := make([]voxel.ActiveCell, len(cao.Cells))
		copy(out, cao.Cells)
		cache.setStage(StageColorWarp, stageKey(StageColorWarp, opts), &colorWarpOutput{Cells: out})
		return nil
	}

	pins := make([]voxel.ColorWarpPin, len(opts.WarpPins))
	for i, p := range opts.WarpPins {
		src, err := palette.ParsePalette([]string{p.SourceHex})
		if err != nil {
			return fmt.Errorf("warp pin %d source: %w", i, err)
		}
		tgt, err := palette.ParsePalette([]string{p.TargetHex})
		if err != nil {
			return fmt.Errorf("warp pin %d target: %w", i, err)
		}
		pins[i] = voxel.ColorWarpPin{Source: src[0], Target: tgt[0], Sigma: p.Sigma}
	}

	tWarp := time.Now()
	cells, err := voxel.WarpCellColors(ctx, cao.Cells, pins)
	if err != nil {
		return err
	}
	fmt.Printf("  Warped colors (%d pins) in %.1fs\n", len(pins), time.Since(tWarp).Seconds())

	cache.setStage(StageColorWarp, stageKey(StageColorWarp, opts), &colorWarpOutput{Cells: cells})
	return nil
}

func runDecimate(ctx context.Context, cache *StageCache, opts Options, lo *loadOutput, vo *voxelizeOutput) error {
	fmt.Println("Decimating...")
	decimModel, err := squarevoxel.DecimateMesh(ctx, lo.Model, vo.Cells, min(vo.Layer0Size, vo.UpperSize), opts.NoSimplify)
	if err != nil {
		return fmt.Errorf("decimate: %w", err)
	}

	cache.setStage(StageDecimate, stageKey(StageDecimate, opts), &decimateOutput{
		DecimModel: decimModel,
	})
	return nil
}

func runPalette(ctx context.Context, cache *StageCache, opts Options, cwo *colorWarpOutput, tracker progress.Tracker) error {
	pcfg, err := buildPaletteConfig(opts)
	if err != nil {
		return err
	}

	if pcfg.NumColors > export3mf.MaxFilaments {
		return fmt.Errorf("palette has %d colors but max supported is %d", pcfg.NumColors, export3mf.MaxFilaments)
	}

	// Copy cells so SnapColors doesn't mutate the color-warp output.
	cells := make([]voxel.ActiveCell, len(cwo.Cells))
	copy(cells, cwo.Cells)

	ditherMode := opts.Dither
	pal, palLabels, palDisplay, err := voxel.ResolvePalette(ctx, cells, pcfg, ditherMode != "none", tracker)
	if err != nil {
		return err
	}
	if palDisplay != "" {
		fmt.Printf("%s\n", palDisplay)
	}
	if len(pal) == 0 {
		return fmt.Errorf("no palette colors")
	}

	if opts.ColorSnap > 0 {
		if err := voxel.SnapColors(ctx, cells, pal, opts.ColorSnap); err != nil {
			return err
		}
		fmt.Printf("  Snapped cell colors toward palette by delta E %.1f\n", opts.ColorSnap)
	}

	cache.setStage(StagePalette, stageKey(StagePalette, opts), &paletteOutput{
		Palette:       pal,
		PaletteLabels: palLabels,
		Cells:         cells,
	})
	return nil
}

func runDither(ctx context.Context, cache *StageCache, opts Options, po *paletteOutput, vo *voxelizeOutput) error {
	ditherMode := opts.Dither
	cells := po.Cells
	pal := po.Palette

	tDither := time.Now()
	var assignments []int32
	var err error
	switch ditherMode {
	case "dizzy":
		if vo.TwoGrid {
			neighbors := voxel.BuildTwoGridNeighbors(cells, vo.Layer0Size, vo.UpperSize, vo.MinV)
			assignments, err = voxel.DitherWithNeighbors(ctx, cells, pal, neighbors)
		} else {
			assignments, err = voxel.DitherCellsDizzy(ctx, cells, pal)
		}
	default:
		assignments, err = voxel.AssignColors(ctx, cells, pal)
	}
	if err != nil {
		return err
	}
	fmt.Printf("  Dithered (%s) %d cells in %.1fs\n", ditherMode, len(cells), time.Since(tDither).Seconds())

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

	// Flood fill per grid, then merge patch maps.
	tFlood := time.Now()
	var patchMap map[voxel.CellKey]int
	var numPatches int
	if vo.TwoGrid {
		patchMap, numPatches, err = floodFillTwoGrids(ctx, cells, assignments)
	} else {
		patchMap, numPatches, err = voxel.FloodFillPatches(ctx, cells, assignments)
	}
	if err != nil {
		return err
	}
	fmt.Printf("  Flood fill: %d patches in %.1fs\n", numPatches, time.Since(tFlood).Seconds())

	// Build per-patch palette assignment.
	patchAssignment := make([]int32, numPatches)
	for i, c := range cells {
		k := voxel.CellKey{Grid: c.Grid, Col: c.Col, Row: c.Row, Layer: c.Layer}
		pid := patchMap[k]
		patchAssignment[pid] = assignments[i]
	}

	cache.setStage(StageDither, stageKey(StageDither, opts), &ditherOutput{
		Assignments:     assignments,
		PatchMap:        patchMap,
		NumPatches:      numPatches,
		PatchAssignment: patchAssignment,
	})
	return nil
}

// floodFillTwoGrids runs flood fill separately for each grid and merges results.
func floodFillTwoGrids(ctx context.Context, cells []voxel.ActiveCell, assignments []int32) (map[voxel.CellKey]int, int, error) {
	// Partition cells by grid.
	var cells0, cells1 []voxel.ActiveCell
	var assign0, assign1 []int32
	idx0 := make([]int, 0, len(cells))
	idx1 := make([]int, 0, len(cells))
	for i, c := range cells {
		if c.Grid == 0 {
			cells0 = append(cells0, c)
			assign0 = append(assign0, assignments[i])
			idx0 = append(idx0, i)
		} else {
			cells1 = append(cells1, c)
			assign1 = append(assign1, assignments[i])
			idx1 = append(idx1, i)
		}
	}

	pm0, n0, err := voxel.FloodFillPatches(ctx, cells0, assign0)
	if err != nil {
		return nil, 0, err
	}
	pm1, n1, err := voxel.FloodFillPatches(ctx, cells1, assign1)
	if err != nil {
		return nil, 0, err
	}

	// Merge: offset grid-1 patch IDs by n0.
	merged := make(map[voxel.CellKey]int, len(cells))
	for k, v := range pm0 {
		merged[k] = v
	}
	for k, v := range pm1 {
		merged[k] = v + n0
	}
	return merged, n0 + n1, nil
}

func runClip(ctx context.Context, cache *StageCache, opts Options, do *ditherOutput, deco *decimateOutput, vo *voxelizeOutput) error {
	tClip := time.Now()
	var shellVerts [][3]float32
	var shellFaces [][3]uint32
	var shellAssignments []int32
	var err error
	if vo.TwoGrid {
		cfg := voxel.TwoGridConfig{
			MinV:       vo.MinV,
			Layer0Size: vo.Layer0Size,
			UpperSize:  vo.UpperSize,
			LayerH:     vo.LayerH,
			SeamZ:      vo.MinV[2] + 0.5*vo.LayerH,
		}
		shellVerts, shellFaces, shellAssignments, err = voxel.ClipMeshByPatchesTwoGrid(
			ctx, deco.DecimModel, do.PatchMap, do.PatchAssignment, cfg)
	} else {
		shellVerts, shellFaces, shellAssignments, err = voxel.ClipMeshByPatches(
			ctx, deco.DecimModel, do.PatchMap, do.PatchAssignment, vo.MinV, vo.Layer0Size, vo.LayerH)
	}
	if err != nil {
		return fmt.Errorf("clip: %w", err)
	}
	fmt.Printf("  Clipped mesh: %d faces in %.1fs\n", len(shellFaces), time.Since(tClip).Seconds())
	fmt.Printf("  After clip: %s\n", voxel.CheckWatertight(shellFaces))

	cache.setStage(StageClip, stageKey(StageClip, opts), &clipOutput{
		ShellVerts:       shellVerts,
		ShellFaces:       shellFaces,
		ShellAssignments: shellAssignments,
	})
	return nil
}

func runMerge(ctx context.Context, cache *StageCache, opts Options) error {
	co := cache.getClip()
	shellVerts := co.ShellVerts
	shellFaces := co.ShellFaces
	shellAssignments := co.ShellAssignments

	if !opts.NoMerge {
		tMerge := time.Now()
		before := len(shellFaces)
		var err error
		shellFaces, shellAssignments, err = voxel.MergeCoplanarTriangles(ctx, shellVerts, shellFaces, shellAssignments)
		if err != nil {
			return fmt.Errorf("merge: %w", err)
		}
		fmt.Printf("  Merged shell: %d -> %d faces in %.1fs\n", before, len(shellFaces), time.Since(tMerge).Seconds())
	}
	fmt.Printf("  Output mesh: %s\n", voxel.CheckWatertight(shellFaces))

	cache.setStage(StageMerge, stageKey(StageMerge, opts), &mergeOutput{
		ShellVerts:       shellVerts,
		ShellFaces:       shellFaces,
		ShellAssignments: shellAssignments,
	})
	return nil
}

func buildPaletteConfig(opts Options) (voxel.PaletteConfig, error) {
	var pcfg voxel.PaletteConfig
	pcfg.NumColors = opts.NumColors
	if pcfg.NumColors <= 0 {
		pcfg.NumColors = 4
	}

	// Parse locked colors. Labels are managed by the frontend and not
	// included in LockedColors, so locked entries have empty labels here.
	if len(opts.LockedColors) > 0 {
		colors, err := palette.ParsePalette(opts.LockedColors)
		if err != nil {
			return pcfg, err
		}
		pcfg.Locked = make([]palette.InventoryEntry, len(colors))
		for i, c := range colors {
			pcfg.Locked[i] = palette.InventoryEntry{Color: c}
		}
	}
	if len(pcfg.Locked) > pcfg.NumColors {
		return pcfg, fmt.Errorf("locked %d colors but only %d total requested", len(pcfg.Locked), pcfg.NumColors)
	}

	if opts.InventoryFile != "" {
		inv, err := palette.ParseInventoryFile(opts.InventoryFile)
		if err != nil {
			return pcfg, err
		}
		pcfg.Inventory = inv
	} else if len(opts.InventoryColors) > 0 {
		for i, c := range opts.InventoryColors {
			label := ""
			if i < len(opts.InventoryLabels) {
				label = opts.InventoryLabels[i]
			}
			pcfg.Inventory = append(pcfg.Inventory, palette.InventoryEntry{Color: c, Label: label})
		}
	} else {
		// Default: select from built-in color set.
		defaultColors := []string{"cyan", "magenta", "yellow", "black", "white", "red", "green", "blue"}
		for _, name := range defaultColors {
			rgb, _ := palette.ParsePalette([]string{name})
			pcfg.Inventory = append(pcfg.Inventory, palette.InventoryEntry{Color: rgb[0], Label: name})
		}
	}
	return pcfg, nil
}

func modelExtents(model *loader.LoadedModel) [3]float32 {
	minV, maxV := model.Vertices[0], model.Vertices[0]
	for _, v := range model.Vertices[1:] {
		for i := 0; i < 3; i++ {
			if v[i] < minV[i] {
				minV[i] = v[i]
			}
			if v[i] > maxV[i] {
				maxV[i] = v[i]
			}
		}
	}
	return [3]float32{maxV[0] - minV[0], maxV[1] - minV[1], maxV[2] - minV[2]}
}

func modelMaxExtent(model *loader.LoadedModel) float32 {
	ex := modelExtents(model)
	m := ex[0]
	if ex[1] > m {
		m = ex[1]
	}
	if ex[2] > m {
		m = ex[2]
	}
	return m
}

func normalizeZ(model *loader.LoadedModel) {
	if len(model.Vertices) == 0 {
		return
	}
	minZ := model.Vertices[0][2]
	for _, v := range model.Vertices[1:] {
		if v[2] < minZ {
			minZ = v[2]
		}
	}
	if minZ != 0 {
		for i := range model.Vertices {
			model.Vertices[i][2] -= minZ
		}
	}
}

func printStats(assignments []int32, paletteRGB [][3]uint8) {
	fmt.Println("  Face counts per material:")
	for i, p := range paletteRGB {
		hexColor := fmt.Sprintf("#%02X%02X%02X", p[0], p[1], p[2])
		count := 0
		for _, a := range assignments {
			if int(a) == i {
				count++
			}
		}
		fmt.Printf("    [%d] %s: %d faces\n", i, hexColor, count)
	}
}
