package main

import (
	"context"
	"fmt"
	"os"

	"github.com/alexflint/go-arg"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
)

// Args defines the CLI arguments.
type Args struct {
	Input          string   `arg:"positional,required" help:"Input .glb or .3mf file"`
	Palette        string   `arg:"--palette" help:"Comma-separated colors (CSS names or hex). Default: best 4 of cyan,magenta,yellow,black,white,red,green,blue"`
	AutoPalette    *int     `arg:"--auto-palette" help:"Compute N dominant colors from mesh surface"`
	Scale          float32  `arg:"--scale" default:"1.0" help:"Additional scale multiplier"`
	Output         string   `arg:"--output" default:"output.3mf" help:"Output .3mf file"`
	NozzleDiameter float32  `arg:"--nozzle-diameter" default:"0.4" help:"Nozzle diameter in mm"`
	LayerHeight    float32  `arg:"--layer-height" default:"0.2" help:"Layer height in mm"`
	InventoryFile  string   `arg:"--inventory-file" help:"File with one filament color per line (CSS names or hex)"`
	Inventory      *int     `arg:"--inventory" help:"Pick best N colors from inventory file (requires --inventory-file)"`
	Dither         string   `arg:"--dither" default:"dizzy" help:"Dithering mode: none, dizzy"`
	NoMerge        bool     `arg:"--no-merge" help:"Skip coplanar triangle merging"`
	NoSimplify     bool     `arg:"--no-simplify" help:"Skip QEM mesh decimation before clipping"`
	Size           *float32 `arg:"--size" help:"Scale model so largest extent equals this value in mm"`
	Force          bool     `arg:"--force" help:"Bypass extent size check"`
	Stats          bool     `arg:"--stats" help:"Print face counts per material"`
	ColorSnap      float64  `arg:"--color-snap" default:"5" help:"Shift cell colors toward nearest palette color by this many delta E units (0 to disable)"`
	NoCache        bool     `arg:"--no-cache" help:"Disable voxelization cache"`
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

	opts := pipeline.Options{
		Input:          args.Input,
		Palette:        args.Palette,
		AutoPalette:    args.AutoPalette,
		Scale:          args.Scale,
		Output:         args.Output,
		NozzleDiameter: args.NozzleDiameter,
		LayerHeight:    args.LayerHeight,
		InventoryFile:  args.InventoryFile,
		Inventory:      args.Inventory,
		Dither:         args.Dither,
		NoMerge:        args.NoMerge,
		NoSimplify:     args.NoSimplify,
		Size:           args.Size,
		Force:          args.Force,
		Stats:          args.Stats,
		ColorSnap:      args.ColorSnap,
		NoCache:        args.NoCache,
	}

	prepResult, _, err := pipeline.Run(context.Background(), opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if prepResult.NeedsForce {
		fmt.Fprintf(os.Stderr, "Error: model extent %.0f mm exceeds 300 mm; use --scale or --size to reduce size (or --force to bypass)\n", prepResult.ModelExtentMM)
		os.Exit(1)
	}
}
