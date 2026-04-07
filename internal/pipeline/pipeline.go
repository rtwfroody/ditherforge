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
	"github.com/rtwfroody/ditherforge/internal/squarevoxel"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// Options controls the pipeline behavior. Mirrors CLI flags.
type Options struct {
	Input          string
	NumColors      int
	LockedColors   []string
	AutoColors     bool
	Scale          float32
	Output         string
	NozzleDiameter float32
	LayerHeight    float32
	InventoryFile    string
	InventoryColors  [][3]uint8 `json:"InventoryColors,omitempty"`
	Brightness     float32
	Contrast       float32
	Saturation     float32
	Dither         string
	NoMerge        bool
	NoSimplify     bool
	Size           *float32
	Force          bool
	Stats          bool
	ColorSnap      float64
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
	InputMesh     *MeshData `json:"-"` // sent async via events, not in JSON response
	OutputMesh    *MeshData `json:"-"` // sent async via events, not in JSON response
	Duration      time.Duration
}

// PrepareResult summarizes the Prepare phase (kept for CLI backward compat).
type PrepareResult struct {
	NeedsForce    bool    // true if model exceeds size limit and Force was not set
	ModelExtentMM float32 // actual extent when NeedsForce is true
	InputMesh     *MeshData
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
// The optional onPalette callback is called with the resolved palette colors
// on every run (including when the palette stage is served from cache),
// allowing callers to update the UI before later stages finish.
func RunCached(ctx context.Context, cache *StageCache, opts Options, onPalette func([][3]uint8)) (*ProcessResult, error) {
	// Validate inputs before any expensive work.
	switch opts.Dither {
	case "none", "dizzy":
	default:
		return nil, fmt.Errorf("invalid --dither %q: must be none or dizzy", opts.Dither)
	}

	startFrom := cache.Invalidate(opts)
	start := time.Now()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Stage 0: Load
	if startFrom <= StageLoad {
		if err := runLoad(ctx, cache, opts); err != nil {
			return nil, err
		}
	}
	lo := cache.getLoad()

	// Force check (between load and voxelize).
	if !opts.Force {
		ext := modelMaxExtent(lo.Model)
		if ext > 300 {
			return &ProcessResult{
				NeedsForce:    true,
				ModelExtentMM: ext,
				InputMesh:     lo.InputMesh,
			}, nil
		}
	}

	// Stage 1: Voxelize
	if startFrom <= StageVoxelize {
		if err := runVoxelize(ctx, cache, opts, lo); err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	vo := cache.getVoxelize()

	// Stage 2: Decimate
	if startFrom <= StageDecimate {
		if err := runDecimate(ctx, cache, opts, lo, vo); err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Stage 3: Color adjustment
	if startFrom <= StageColorAdjust {
		if err := runColorAdjust(ctx, cache, opts, vo); err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	cao := cache.getColorAdjust()

	// Stage 4: Palette + snap colors
	if startFrom <= StagePalette {
		if err := runPalette(ctx, cache, opts, cao); err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	po := cache.getPalette()

	if onPalette != nil {
		onPalette(po.Palette)
	}

	// Stage 5: Dither + flood fill
	if startFrom <= StageDither {
		if err := runDither(ctx, cache, opts, po); err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	do := cache.getDither()

	// Stage 6: Clip
	if startFrom <= StageClip {
		if err := runClip(ctx, cache, opts, do, cache.getDecimate(), vo); err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Stage 7: Merge
	if startFrom <= StageMerge {
		if err := runMerge(ctx, cache, opts); err != nil {
			return nil, err
		}
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
		InputMesh:  lo.InputMesh,
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
	pr, err := RunCached(ctx, cache, opts, nil)
	if err != nil {
		return nil, nil, err
	}
	if pr.NeedsForce {
		return &PrepareResult{
			NeedsForce:    true,
			ModelExtentMM: pr.ModelExtentMM,
			InputMesh:     pr.InputMesh,
		}, nil, nil
	}

	faceCount, err := ExportFile(cache, opts.Output, opts.LayerHeight)
	if err != nil {
		return nil, nil, err
	}

	prepResult := &PrepareResult{
		InputMesh: pr.InputMesh,
	}
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

func runVoxelize(ctx context.Context, cache *StageCache, opts Options, lo *loadOutput) error {
	cellSize := opts.NozzleDiameter * 1.275
	layerH := opts.LayerHeight

	fmt.Println("Voxelizing...")
	cells, cellAssignMap, minV, err := squarevoxel.Voxelize(ctx, lo.Model, cellSize, layerH)
	if err != nil {
		return fmt.Errorf("voxelize: %w", err)
	}

	cache.setStage(StageVoxelize, stageKey(StageVoxelize, opts), &voxelizeOutput{
		Cells:         cells,
		CellAssignMap: cellAssignMap,
		MinV:          minV,
		CellSize:      cellSize,
		LayerH:        layerH,
	})
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

func runDecimate(ctx context.Context, cache *StageCache, opts Options, lo *loadOutput, vo *voxelizeOutput) error {
	fmt.Println("Decimating...")
	decimModel, err := squarevoxel.DecimateMesh(ctx, lo.Model, vo.Cells, vo.CellSize, opts.NoSimplify)
	if err != nil {
		return fmt.Errorf("decimate: %w", err)
	}

	cache.setStage(StageDecimate, stageKey(StageDecimate, opts), &decimateOutput{
		DecimModel: decimModel,
	})
	return nil
}

func runPalette(ctx context.Context, cache *StageCache, opts Options, cao *colorAdjustOutput) error {
	pcfg, err := buildPaletteConfig(opts)
	if err != nil {
		return err
	}

	if pcfg.NumColors > export3mf.MaxFilaments {
		return fmt.Errorf("palette has %d colors but max supported is %d", pcfg.NumColors, export3mf.MaxFilaments)
	}

	// Copy cells so SnapColors doesn't mutate the color-adjust output.
	cells := make([]voxel.ActiveCell, len(cao.Cells))
	copy(cells, cao.Cells)

	ditherMode := opts.Dither
	pal, palDisplay, err := voxel.ResolvePalette(cells, pcfg, ditherMode != "none")
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
		Palette: pal,
		Cells:   cells,
	})
	return nil
}

func runDither(ctx context.Context, cache *StageCache, opts Options, po *paletteOutput) error {
	ditherMode := opts.Dither
	cells := po.Cells
	pal := po.Palette

	tDither := time.Now()
	var assignments []int32
	var err error
	switch ditherMode {
	case "dizzy":
		assignments, err = voxel.DitherCellsDizzy(ctx, cells, pal)
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

	// Flood fill to merge same-color cells into patches.
	tFlood := time.Now()
	patchMap, numPatches, err := voxel.FloodFillPatches(ctx, cells, assignments)
	if err != nil {
		return err
	}
	fmt.Printf("  Flood fill: %d patches in %.1fs\n", numPatches, time.Since(tFlood).Seconds())

	// Build per-patch palette assignment.
	patchAssignment := make([]int32, numPatches)
	for i, c := range cells {
		k := voxel.CellKey{Col: c.Col, Row: c.Row, Layer: c.Layer}
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

func runClip(ctx context.Context, cache *StageCache, opts Options, do *ditherOutput, deco *decimateOutput, vo *voxelizeOutput) error {
	tClip := time.Now()
	shellVerts, shellFaces, shellAssignments, err := voxel.ClipMeshByPatches(
		ctx, deco.DecimModel, do.PatchMap, do.PatchAssignment, vo.MinV, vo.CellSize, vo.LayerH)
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

	// Parse locked colors.
	if len(opts.LockedColors) > 0 {
		locked, err := palette.ParsePalette(opts.LockedColors)
		if err != nil {
			return pcfg, err
		}
		pcfg.Locked = locked
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
		for _, c := range opts.InventoryColors {
			pcfg.Inventory = append(pcfg.Inventory, palette.InventoryEntry{Color: c})
		}
	} else if opts.AutoColors {
		pcfg.AutoColors = true
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
