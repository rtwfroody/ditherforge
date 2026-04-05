// Package pipeline implements the core ditherforge processing pipeline:
// load model, validate, remesh, and export to 3MF.
package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
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
	Palette        string
	AutoPalette    *int
	Scale          float32
	Output         string
	NozzleDiameter float32
	LayerHeight    float32
	InventoryFile  string
	Inventory      *int
	Dither         string
	NoMerge        bool
	NoSimplify     bool
	Size           *float32
	Force          bool
	Stats          bool
	ColorSnap      float64
	NoCache        bool
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

// PreparedModel holds the intermediate state after Prepare, reusable
// across multiple Render calls with different color options.
type PreparedModel struct {
	Model    *loader.LoadedModel      // original loaded model
	Prepared *squarevoxel.PreparedData // voxelized cells + decimated model
}

// PrepareResult summarizes the Prepare phase.
type PrepareResult struct {
	NeedsForce    bool    // true if model exceeds size limit and Force was not set
	ModelExtentMM float32 // actual extent when NeedsForce is true
	InputMesh     *MeshData
}

// Result summarizes a completed pipeline run.
type Result struct {
	OutputPath string
	FaceCount  int
	Duration   time.Duration
	OutputMesh *MeshData
}

// Prepare loads and voxelizes the model. The returned PreparedModel can
// be passed to Render multiple times with different color options.
func Prepare(ctx context.Context, opts Options) (*PreparedModel, *PrepareResult, error) {
	// Compute scale: unit conversion (GLB meters→mm) * user scale.
	inputExt := strings.ToLower(filepath.Ext(opts.Input))
	scale := unitScaleForExt(inputExt) * opts.Scale

	fmt.Printf("Loading %s...", opts.Input)
	tLoad := time.Now()
	model, err := loadModel(opts.Input, scale)
	if err != nil {
		return nil, nil, fmt.Errorf("loading %s: %w", inputExt, err)
	}
	fmt.Printf(" %d vertices, %d faces in %.1fs\n", len(model.Vertices), len(model.Faces), time.Since(tLoad).Seconds())

	// Auto-scale to --size if specified.
	if opts.Size != nil {
		ext := modelMaxExtent(model)
		if ext != *opts.Size {
			rescale := *opts.Size / ext
			fmt.Printf("  Rescaling to %.0f mm...", *opts.Size)
			tRescale := time.Now()
			model, err = loadModel(opts.Input, scale*rescale)
			if err != nil {
				return nil, nil, fmt.Errorf("loading %s (rescaled): %w", inputExt, err)
			}
			fmt.Printf(" done in %.1fs\n", time.Since(tRescale).Seconds())
		}
	}

	ex := modelExtents(model)
	fmt.Printf("  Extent: %.1f x %.1f x %.1f mm\n", ex[0], ex[1], ex[2])

	// Check model extent.
	if !opts.Force {
		ext := modelMaxExtent(model)
		if ext > 300 {
			return nil, &PrepareResult{NeedsForce: true, ModelExtentMM: ext}, nil
		}
	}

	cfg := squarevoxel.Config{
		NozzleDiameter: opts.NozzleDiameter,
		LayerHeight:    opts.LayerHeight,
		NoSimplify:     opts.NoSimplify,
	}

	cacheOpts := squarevoxel.CacheOptions{
		InputPath:  opts.Input,
		ConfigHash: ConfigHash(opts),
	}
	var cached *squarevoxel.CacheData
	if !opts.NoCache {
		cached = squarevoxel.LoadCache(cacheOpts)
	}

	fmt.Println("Preparing...")
	prepared, newCache, err := squarevoxel.Prepare(ctx, model, cfg, cached)
	if err != nil {
		return nil, nil, fmt.Errorf("squarevoxel prepare: %w", err)
	}
	if newCache != nil {
		squarevoxel.SaveCache(newCache, cacheOpts)
	}

	pm := &PreparedModel{
		Model:    model,
		Prepared: prepared,
	}

	result := &PrepareResult{
		InputMesh: buildInputMeshData(model),
	}

	return pm, result, nil
}

// Render applies color options to a PreparedModel and exports the result.
func Render(ctx context.Context, pm *PreparedModel, opts Options) (*Result, error) {
	start := time.Now()

	// Validate output extension.
	outputExt := strings.ToLower(filepath.Ext(opts.Output))
	if outputExt != ".3mf" {
		return nil, fmt.Errorf("output must be .3mf, got %q", outputExt)
	}

	// Validate flags.
	if opts.Inventory != nil && opts.InventoryFile == "" {
		return nil, fmt.Errorf("--inventory requires --inventory-file")
	}
	switch opts.Dither {
	case "none", "dizzy":
	default:
		return nil, fmt.Errorf("invalid --dither %q: must be none or dizzy", opts.Dither)
	}

	// Build palette config.
	pcfg, err := buildPaletteConfig(opts)
	if err != nil {
		return nil, err
	}

	if pcfg.Palette != nil && len(pcfg.Palette) > export3mf.MaxFilaments {
		return nil, fmt.Errorf("palette has %d colors but max supported is %d", len(pcfg.Palette), export3mf.MaxFilaments)
	}

	// Only color-relevant fields; geometry options are baked into PreparedData.
	cfg := squarevoxel.Config{
		NoMerge:   opts.NoMerge,
		ColorSnap: opts.ColorSnap,
	}

	fmt.Println("Rendering...")
	outModel, assignments, paletteRGB, err := squarevoxel.Render(ctx, pm.Prepared, pm.Model, pcfg, cfg, opts.Dither)
	if err != nil {
		return nil, fmt.Errorf("squarevoxel render: %w", err)
	}

	if opts.Stats {
		printStats(assignments, paletteRGB)
	}

	fmt.Printf("Exporting %s...", opts.Output)
	tExport := time.Now()
	if err := export3mf.Export(outModel, assignments, opts.Output, paletteRGB, opts.LayerHeight); err != nil {
		return nil, fmt.Errorf("exporting 3MF: %w", err)
	}
	fmt.Printf(" done in %.1fs\n", time.Since(tExport).Seconds())

	return &Result{
		OutputPath: opts.Output,
		FaceCount:  len(outModel.Faces),
		Duration:   time.Since(start),
		OutputMesh: buildMeshData(outModel, assignments, paletteRGB),
	}, nil
}

// Run executes the full pipeline: Prepare + Render. Convenience wrapper
// for CLI and simple callers.
func Run(ctx context.Context, opts Options) (*PrepareResult, *Result, error) {
	pm, prepResult, err := Prepare(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	if prepResult.NeedsForce {
		return prepResult, nil, nil
	}
	result, err := Render(ctx, pm, opts)
	if err != nil {
		return prepResult, nil, err
	}
	return prepResult, result, nil
}

func buildPaletteConfig(opts Options) (voxel.PaletteConfig, error) {
	var pcfg voxel.PaletteConfig
	if opts.Inventory != nil {
		inv, err := palette.ParseInventoryFile(opts.InventoryFile)
		if err != nil {
			return pcfg, err
		}
		pcfg.Inventory = inv
		pcfg.InventoryN = *opts.Inventory
	} else if opts.AutoPalette != nil {
		pcfg.AutoPaletteN = *opts.AutoPalette
	} else if opts.Palette == "" {
		defaultColors := []string{"cyan", "magenta", "yellow", "black", "white", "red", "green", "blue"}
		for _, name := range defaultColors {
			rgb, _ := palette.ParsePalette([]string{name})
			pcfg.Inventory = append(pcfg.Inventory, palette.InventoryEntry{Color: rgb[0], Label: name})
		}
		pcfg.InventoryN = 4
	} else {
		colorStrs := strings.Split(opts.Palette, ",")
		for i := range colorStrs {
			colorStrs[i] = strings.TrimSpace(colorStrs[i])
		}
		var err error
		pcfg.Palette, err = palette.ParsePalette(colorStrs)
		if err != nil {
			return pcfg, err
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
