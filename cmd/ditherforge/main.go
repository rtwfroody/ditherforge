package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/alexflint/go-arg"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
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
	Input          string   `arg:"positional,required" help:"Input .glb, .3mf, or .stl file"`
	NumColors      int      `arg:"-n" default:"4" help:"Number of palette colors"`
	Color          []string `arg:"--color,separate" help:"Lock a color (CSS name or hex, repeatable, comma-separated)"`
	Inventory      string   `arg:"--inventory" help:"Inventory file for remaining colors"`
	Scale          float32  `arg:"--scale" default:"1.0" help:"Additional scale multiplier"`
	Output         string   `arg:"--output" default:"output.3mf" help:"Output .3mf file"`
	NozzleDiameter float32  `arg:"--nozzle-diameter" default:"0.4" help:"Nozzle diameter in mm"`
	LayerHeight    float32  `arg:"--layer-height" default:"0.2" help:"Layer height in mm"`
	Brightness     float32  `arg:"--brightness" default:"0" help:"Brightness adjustment (-100 to +100)"`
	Contrast       float32  `arg:"--contrast" default:"0" help:"Contrast adjustment (-100 to +100)"`
	Saturation     float32  `arg:"--saturation" default:"0" help:"Saturation adjustment (-100 to +100)"`
	Dither         string   `arg:"--dither" default:"dizzy" help:"Dithering mode: none, dizzy"`
	NoMerge        bool     `arg:"--no-merge" help:"Skip coplanar triangle merging"`
	NoSimplify     bool     `arg:"--no-simplify" help:"Skip QEM mesh decimation before clipping"`
	Size           *float32 `arg:"--size" help:"Scale model so largest extent equals this value in mm"`
	Force          bool     `arg:"--force" help:"Bypass extent size check"`
	Stats          bool     `arg:"--stats" help:"Print face counts per material"`
	ColorSnap      float64  `arg:"--color-snap" default:"5" help:"Shift cell colors toward nearest palette color by this many delta E units (0 to disable)"`
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
		NumColors:      args.NumColors,
		LockedColors:   expandColors(args.Color),
		InventoryFile:  args.Inventory,
		Scale:          args.Scale,
		Output:         args.Output,
		NozzleDiameter: args.NozzleDiameter,
		LayerHeight:    args.LayerHeight,
		Brightness:     args.Brightness,
		Contrast:       args.Contrast,
		Saturation:     args.Saturation,
		Dither:         args.Dither,
		NoMerge:        args.NoMerge,
		NoSimplify:     args.NoSimplify,
		Size:           args.Size,
		Force:          args.Force,
		Stats:          args.Stats,
		ColorSnap:      args.ColorSnap,
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
