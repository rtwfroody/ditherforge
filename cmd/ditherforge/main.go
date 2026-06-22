package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexflint/go-arg"

	"github.com/rtwfroody/ditherforge/internal/collection"
	"github.com/rtwfroody/ditherforge/internal/debugrender"
	"github.com/rtwfroody/ditherforge/internal/diskcache"
	"github.com/rtwfroody/ditherforge/internal/export3mf"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/settings"
)

// Args defines the CLI arguments.
//
// All processing options come from the DitherForge settings .json file
// (the same format the GUI saves and loads). The only flags here are the
// ones that are NOT processing options: where to write the result, the
// size-check override, and the CLI-only debug renders.
type Args struct {
	Settings       string `arg:"positional,required" help:"DitherForge settings .json file (saved from the GUI). Holds the input model path and every processing option."`
	Output         string `arg:"--output" help:"Output .3mf file (default: derived from the settings file's input model name)"`
	Force          bool   `arg:"--force" help:"Bypass the model extent size check"`
	DebugRender    string `arg:"--debug-render" help:"After running, write PNG renders (input + dithered + sampled, four views each) into this directory. Useful for headless debugging."`
	DebugRenderRes int    `arg:"--debug-render-res" default:"800" help:"PNG resolution (square) for --debug-render output"`
	DebugCellsDir  string `arg:"--debug-cells-dir" help:"After Voxelize, write per-slab cell PNGs colored by the sampled RGB into this directory."`
}

func (Args) Description() string {
	return "Convert a model to a multi-material 3MF file using options from a DitherForge settings JSON file."
}

func (Args) Version() string {
	return pipeline.Version
}

// deriveOutput builds the default output .3mf name from the input model
// path (basename, in the current directory). A .3mf input gets a "-df"
// suffix so it never collides with itself.
func deriveOutput(input string) string {
	base := filepath.Base(input)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if strings.EqualFold(ext, ".3mf") {
		return stem + "-df.3mf"
	}
	return stem + ".3mf"
}

func main() {
	var args Args
	arg.MustParse(&args)

	s, legacyAbsoluteUnits, err := settings.Load(args.Settings)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if s.InputFile == "" {
		fmt.Fprintln(os.Stderr, "Error: settings file has no input model (inputFile). Open the model in the GUI and re-save the settings, then run the CLI on that file.")
		os.Exit(1)
	}
	// Verify the input model exists up front (settings.Load already resolved
	// it relative to the settings file's directory) so a moved/missing asset
	// produces a clear error here rather than a generic failure deep in the
	// pipeline's load stage.
	if _, statErr := os.Stat(s.InputFile); statErr != nil {
		fmt.Fprintf(os.Stderr, "Error: input model %q not found: %v\n", s.InputFile, statErr)
		os.Exit(1)
	}

	// Resolve the inventory collection only when the settings name one, then
	// convert to pipeline options through the exact same path the GUI uses,
	// so a given settings file produces identical output from either front
	// end. A fully-locked palette needs no collections, so we don't force
	// the (filesystem-touching) manager into existence; if it can't load, or
	// the named collection is absent, warn and proceed on the locked slots.
	var mgr *collection.Manager
	if s.InventoryCollection != "" {
		if m, mErr := collection.NewManager(); mErr != nil {
			fmt.Fprintf(os.Stderr, "warning: filament collections unavailable (%v); proceeding with locked palette colors only\n", mErr)
		} else {
			mgr = m
			if _, ok := m.Get(s.InventoryCollection); !ok {
				fmt.Fprintf(os.Stderr, "warning: inventory collection %q not found; proceeding with locked palette colors only\n", s.InventoryCollection)
			}
		}
	}
	opts, err := settings.ToOptions(s, mgr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	opts.Force = args.Force
	opts.LegacyAbsoluteUnits = legacyAbsoluteUnits

	opts.Output = args.Output
	if opts.Output == "" {
		opts.Output = deriveOutput(s.InputFile)
	}

	ctx := context.Background()
	cache := pipeline.NewStageCache()
	// Attach the same on-disk cache the GUI uses. ExportFile reads stage
	// outputs back from disk, so a CLI run without disk attached can't
	// export — the GUI configures this automatically in NewApp.
	if dir, err := diskcache.DefaultDir(); err == nil {
		if d, derr := diskcache.Open(dir); derr == nil {
			cache.SetDisk(d)
		} else {
			fmt.Fprintf(os.Stderr, "disk cache disabled: %v\n", derr)
		}
	}
	cb := &pipeline.Callbacks{Progress: progress.NewCLITracker()}

	pr, runErr := pipeline.RunCached(ctx, cache, opts, cb)

	// Even if RunCached fails, the Voxelize stage may have completed —
	// emit debug cell PNGs from the cache before exiting.
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
		fmt.Fprintf(os.Stderr, "Error: model extent %.0f mm exceeds 300 mm; reduce the size in the settings file (or pass --force to bypass)\n", pr.ModelExtentMM)
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
		if err := writeDebugRenders(ctx, cache, opts, opts.Input, args.DebugRender, args.DebugRenderRes, pr.OutputMesh); err != nil {
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
// The dithered mesh comes from the caller's existing pipeline run; the
// sampled mesh is produced by re-running RunCached with
// ShowSampledColors=true, which reuses everything up to the dither stage
// from `cache` and only redoes the post-merge bypass.
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
