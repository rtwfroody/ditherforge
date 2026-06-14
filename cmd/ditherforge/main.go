package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexflint/go-arg"

	"github.com/rtwfroody/ditherforge/internal/debugrender"
	"github.com/rtwfroody/ditherforge/internal/diskcache"
	"github.com/rtwfroody/ditherforge/internal/export3mf"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
	"github.com/rtwfroody/ditherforge/internal/progress"
)

// expandColors splits comma-separated --color values into individual color strings.
func expandColors(colors []string) []string {
	var result []string
	for _, c := range colors {
		for _, part := range strings.Split(c, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				result = append(result, part)
			}
		}
	}
	return result
}

// Args defines the CLI arguments.
type Args struct {
	Input                           string   `arg:"positional,required" help:"Input .glb, .3mf, .stl, .obj, or .obj-in-.zip file"`
	NumColors                       int      `arg:"-n" default:"4" help:"Number of palette colors"`
	Color                           []string `arg:"--color,separate" help:"Lock a color (CSS name or hex, repeatable, comma-separated)"`
	Inventory                       string   `arg:"--inventory" help:"Inventory file for remaining colors"`
	Scale                           float32  `arg:"--scale" default:"1.0" help:"Additional scale multiplier"`
	Output                          string   `arg:"--output" help:"Output .3mf file (default: <input>.3mf)"`
	BaseColor                       string   `arg:"--base-color" help:"Hex color for untextured faces (e.g. #FF0000)"`
	BaseMaterialX                   string   `arg:"--base-materialx" help:"Path to a .mtlx file or .zip archive containing one (with adjacent textures) applied as the base color of untextured faces (overrides --base-color)"`
	BaseMaterialXTileMM             float64  `arg:"--base-materialx-tile-mm" default:"10" help:"Object-space scale (mm per shading-unit cycle) for the MaterialX procedural"`
	BaseMaterialXTriplanarSharpness float64  `arg:"--base-materialx-triplanar-sharpness" default:"4" help:"Triplanar projection sharpness for image-backed MaterialX (higher = sharper axis transitions; ignored by procedural .mtlx)"`
	NozzleDiameter                  float32  `arg:"--nozzle-diameter" default:"0.4" help:"Nozzle diameter in mm"`
	LayerHeight                     float32  `arg:"--layer-height" default:"0.2" help:"Layer height in mm"`
	Printer                         string   `arg:"--printer" help:"Target printer profile id (e.g. snapmaker_u1, snapmaker_j1, prusa_xl, prusa_xl_5t, bambu_h2d, bambu_h2d_pro); defaults to snapmaker_u1"`
	Brightness                      float32  `arg:"--brightness" default:"0" help:"Brightness adjustment (-100 to +100)"`
	Contrast                        float32  `arg:"--contrast" default:"0" help:"Contrast adjustment (-100 to +100)"`
	Saturation                      float32  `arg:"--saturation" default:"0" help:"Saturation adjustment (-100 to +100)"`
	Dither                          string   `arg:"--dither" default:"floyd-steinberg" help:"Dithering mode: floyd-steinberg, riemersma, riemersma-pair, blue-noise, dizzy-corrected, none (also accepted: dizzy-2hop, dizzy-recover)"`
	RiemersmaInputBias              float64  `arg:"--riemersma-bias" default:"0.85" help:"Riemersma input-bias maximum (0..1). 0 = pure dither; higher pulls toward nearest-input palette in near-palette regions"`
	BlueNoiseTolerance              float64  `arg:"--blue-noise-tol" help:"Blue-noise dither per-cell projection-error tolerance (RGB units). Smaller = lower wander but more drift; larger = more wander but less drift. 0 = use built-in default (currently 20)"`
	NoMerge                         bool     `arg:"--no-merge" help:"Skip coplanar triangle merging"`
	NoSimplify                      bool     `arg:"--no-simplify" help:"Skip QEM mesh decimation before clipping"`
	NoInteriorFaceFootprint         bool     `arg:"--no-interior-face-footprint" help:"Advanced: disable projecting thin between-plane horizontal faces into slab footprints (for A/B timing of that augmentation)"`
	NoCellMerge                     bool     `arg:"--no-cell-merge" help:"Advanced: clip every cell individually instead of pairing adjacent same-color cells per slab (slower, more triangles, no effect on dithered output). Merging is on by default."`
	Size                            *float32 `arg:"--size" help:"Scale model so largest extent equals this value in mm"`
	Force                           bool     `arg:"--force" help:"Bypass extent size check"`
	Stats                           bool     `arg:"--stats" help:"Print face counts per material"`
	ColorSnap                       float64  `arg:"--color-snap" default:"5" help:"Shift cell colors toward nearest palette color by this many delta E units (0 to disable)"`
	AlphaWrap                       bool     `arg:"--alpha-wrap" help:"Clean up the loaded mesh with CGAL Alpha_wrap_3 (requires uv on PATH)"`
	AlphaWrapAlpha                  float32  `arg:"--alpha-wrap-alpha" help:"Alpha-wrap probe radius in mm (default: nozzle diameter)"`
	AlphaWrapOffset                 float32  `arg:"--alpha-wrap-offset" help:"Alpha-wrap offset distance in mm (default: alpha/30)"`
	Layer0AdhesionXYScale           float32  `arg:"--layer0-adhesion-xy-scale" default:"2" help:"Multiplier on layer-0 minimum feature size (= printer-profile initial-layer line width if set, else nozzle diameter). Higher = bigger first-layer color blobs for bed adhesion."`
	UpperLayerXYScale               float32  `arg:"--upper-layer-xy-scale" default:"1.25" help:"Multiplier on upper-layer minimum feature size (= printer-profile line width if set, else nozzle diameter). Higher = coarser color detail with fewer primitives."`
	DebugRender                     string   `arg:"--debug-render" help:"After running the pipeline, write PNG renders (input + dithered + sampled, four views each) into this directory. Useful for headless debugging."`
	DebugRenderRes                  int      `arg:"--debug-render-res" default:"800" help:"PNG resolution (square) for --debug-render output"`
	DebugCellsDir                   string   `arg:"--debug-cells-dir" help:"After Voxelize, write per-slab cell PNGs colored by the sampled RGB into this directory."`
	Split                           bool     `arg:"--split" help:"Cut the model into two halves laid out side by side on the bed (requires --alpha-wrap)"`
	SplitAxis                       string   `arg:"--split-axis" default:"z" help:"Split cut axis: x, y, or z"`
	SplitOffset                     float64  `arg:"--split-offset" help:"Split cut position along the axis in mm (model coords, post --size, post normalize). X/Y are centered on 0; Z rests on 0 so use ~half the height."`
	SplitConnector                  string   `arg:"--split-connector" default:"none" help:"Split connector style: none, pegs, or dowels"`
}

// parseSplitAxis maps an x/y/z axis name to the pipeline's 0/1/2 index.
func parseSplitAxis(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "x":
		return 0
	case "y":
		return 1
	default:
		return 2
	}
}

func (Args) Description() string {
	return "Convert a textured GLB model to a multi-material 3MF file."
}

func (Args) Version() string {
	return pipeline.Version
}

func main() {
	var args Args
	arg.MustParse(&args)

	if args.Output == "" {
		base := filepath.Base(args.Input)
		ext := filepath.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		if strings.EqualFold(ext, ".3mf") {
			args.Output = stem + "-df.3mf"
		} else {
			args.Output = stem + ".3mf"
		}
	}

	opts := pipeline.Options{
		Input:                                args.Input,
		NumColors:                            args.NumColors,
		LockedColors:                         expandColors(args.Color),
		InventoryFile:                        args.Inventory,
		Scale:                                args.Scale,
		Output:                               args.Output,
		BaseColor:                            args.BaseColor,
		BaseColorMaterialX:                   args.BaseMaterialX,
		BaseColorMaterialXTileMM:             args.BaseMaterialXTileMM,
		BaseColorMaterialXTriplanarSharpness: args.BaseMaterialXTriplanarSharpness,
		NozzleDiameter:                       args.NozzleDiameter,
		LayerHeight:                          args.LayerHeight,
		Printer:                              args.Printer,
		Brightness:                           args.Brightness,
		Contrast:                             args.Contrast,
		Saturation:                           args.Saturation,
		Dither:                               args.Dither,
		RiemersmaInputBias:                   args.RiemersmaInputBias,
		BlueNoiseTolerance:                   args.BlueNoiseTolerance,
		NoMerge:                              args.NoMerge,
		NoSimplify:                           args.NoSimplify,
		NoInteriorFaceFootprint:              args.NoInteriorFaceFootprint,
		NoCellMerge:                          args.NoCellMerge,
		Size:                                 args.Size,
		Force:                                args.Force,
		Stats:                                args.Stats,
		ColorSnap:                            args.ColorSnap,
		ObjectIndex:                          -1, // load all objects (no CLI flag yet; GUI has a picker dialog)
		AlphaWrap:                            args.AlphaWrap,
		AlphaWrapAlpha:                       args.AlphaWrapAlpha,
		AlphaWrapOffset:                      args.AlphaWrapOffset,
		Layer0AdhesionXYScale:                args.Layer0AdhesionXYScale,
		UpperLayerXYScale:                    args.UpperLayerXYScale,
	}
	if args.Split {
		opts.Split = pipeline.SplitSettings{
			Enabled:        true,
			Axis:           parseSplitAxis(args.SplitAxis),
			Offset:         args.SplitOffset,
			ConnectorStyle: args.SplitConnector,
		}
	}

	ctx := context.Background()
	cache := pipeline.NewStageCache()
	// Attach the same on-disk cache the GUI uses. ExportFile reads
	// stage outputs back from disk, so a CLI run without disk
	// attached can't export — the GUI configures this automatically
	// in NewApp.
	if dir, err := diskcache.DefaultDir(); err == nil {
		if d, derr := diskcache.Open(dir); derr == nil {
			cache.SetDisk(d)
		} else {
			fmt.Fprintf(os.Stderr, "disk cache disabled: %v\n", derr)
		}
	}
	cb := &pipeline.Callbacks{Progress: progress.NewCLITracker()}

	pr, runErr := pipeline.RunCached(ctx, cache, opts, cb)

	// Even if RunCached fails (Clip / Merge stubbed during the
	// cellslicer transition), the Voxelize stage may have completed
	// — emit debug cell PNGs from the cache before exiting.
	if args.DebugCellsDir != "" {
		if err := pipeline.WriteCellsDebugPNGs(cache, opts, args.DebugCellsDir); err != nil {
			fmt.Fprintf(os.Stderr, "debug-cells-dir: %v\n", err)
		} else {
			fmt.Printf("Wrote per-slab cell PNGs to %s\n", args.DebugCellsDir)
		}
	}

	if runErr != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", runErr)
		os.Exit(1)
	}
	if pr.NeedsForce {
		fmt.Fprintf(os.Stderr, "Error: model extent %.0f mm exceeds 300 mm; use --scale or --size to reduce size (or --force to bypass)\n", pr.ModelExtentMM)
		os.Exit(1)
	}

	if _, err := pipeline.ExportFile(cache, opts, opts.Output, export3mf.Options{
		PrinterID:      opts.Printer,
		NozzleDiameter: opts.NozzleDiameter,
		LayerHeight:    opts.LayerHeight,
		AppVersion:     pipeline.VersionSemver,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if args.DebugRender != "" {
		if err := writeDebugRenders(ctx, cache, opts, args.Input, args.DebugRender, args.DebugRenderRes, pr.OutputMesh); err != nil {
			fmt.Fprintf(os.Stderr, "debug-render: %v\n", err)
			os.Exit(1)
		}
	}
}

// writeDebugRenders writes PNG renders of three things to dir:
//   - input_<view>.png: the raw model with per-face sampled color
//   - dithered_<view>.png: the pipeline output (palette-quantized)
//   - sampled_<view>.png: the pipeline output with ShowSampledColors
//     (per-section raw sampled RGB before dithering)
//
// The dithered mesh comes from the caller's existing pipeline run;
// the sampled mesh is produced by re-running RunCached with
// ShowSampledColors=true, which reuses everything up to the dither
// stage from `cache` and only redoes the post-merge bypass.
func writeDebugRenders(ctx context.Context, cache *pipeline.StageCache, opts pipeline.Options, inputPath, dir string, res int, ditheredMesh *pipeline.MeshData) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	if inputMesh, err := debugrender.LoadInputMesh(inputPath, opts.Size); err != nil {
		fmt.Fprintf(os.Stderr, "debug-render: skipping input reference (%v)\n", err)
	} else {
		for _, v := range debugrender.DefaultViews {
			p := filepath.Join(dir, fmt.Sprintf("input_%s.png", v.Name))
			if err := debugrender.WritePNG(p, debugrender.RenderInput(inputMesh, v, res)); err != nil {
				return err
			}
		}
	}

	if ditheredMesh != nil {
		for _, v := range debugrender.DefaultViews {
			p := filepath.Join(dir, fmt.Sprintf("dithered_%s.png", v.Name))
			if err := debugrender.WritePNG(p, debugrender.RenderPipelineMesh(ditheredMesh, v, res)); err != nil {
				return err
			}
		}
	}

	sampledOpts := opts
	sampledOpts.ShowSampledColors = true
	sampledPr, err := pipeline.RunCached(ctx, cache, sampledOpts, &pipeline.Callbacks{Progress: progress.NewCLITracker()})
	if err != nil {
		return fmt.Errorf("sampled re-run: %w", err)
	}
	if sampledPr.OutputMesh != nil {
		for _, v := range debugrender.DefaultViews {
			p := filepath.Join(dir, fmt.Sprintf("sampled_%s.png", v.Name))
			if err := debugrender.WritePNG(p, debugrender.RenderPipelineMesh(sampledPr.OutputMesh, v, res)); err != nil {
				return err
			}
		}
	}
	return nil
}
