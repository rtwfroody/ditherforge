package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	Output         string   `arg:"--output" help:"Output .3mf file (default: <input>.3mf)"`
	BaseColor                       string  `arg:"--base-color" help:"Hex color for untextured faces (e.g. #FF0000)"`
	BaseMaterialX                   string  `arg:"--base-materialx" help:"Path to a .mtlx file or .zip archive containing one (with adjacent textures) applied as the base color of untextured faces (overrides --base-color)"`
	BaseMaterialXTileMM             float64 `arg:"--base-materialx-tile-mm" default:"10" help:"Object-space scale (mm per shading-unit cycle) for the MaterialX procedural"`
	BaseMaterialXTriplanarSharpness float64 `arg:"--base-materialx-triplanar-sharpness" default:"4" help:"Triplanar projection sharpness for image-backed MaterialX (higher = sharper axis transitions; ignored by procedural .mtlx)"`
	NozzleDiameter float32  `arg:"--nozzle-diameter" default:"0.4" help:"Nozzle diameter in mm"`
	LayerHeight    float32  `arg:"--layer-height" default:"0.2" help:"Layer height in mm"`
	Printer        string   `arg:"--printer" help:"Target printer profile id (e.g. snapmaker_u1, snapmaker_j1, prusa_xl, prusa_xl_5t, bambu_h2d, bambu_h2d_pro); defaults to snapmaker_u1"`
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
	AlphaWrap       bool    `arg:"--alpha-wrap" help:"Clean up the loaded mesh with CGAL Alpha_wrap_3 (requires uv on PATH)"`
	AlphaWrapAlpha  float32 `arg:"--alpha-wrap-alpha" help:"Alpha-wrap probe radius in mm (default: nozzle diameter)"`
	AlphaWrapOffset float32 `arg:"--alpha-wrap-offset" help:"Alpha-wrap offset distance in mm (default: alpha/30)"`
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
		LayerHeight:    args.LayerHeight,
		Printer:        args.Printer,
		Brightness:     args.Brightness,
		Contrast:       args.Contrast,
		Saturation:     args.Saturation,
		Dither:         args.Dither,
		NoMerge:        args.NoMerge,
		NoSimplify:     args.NoSimplify,
		Size:           args.Size,
		Force:          args.Force,
		Stats:          args.Stats,
		ColorSnap:       args.ColorSnap,
		ObjectIndex:     -1, // load all objects (no CLI flag yet; GUI has a picker dialog)
		AlphaWrap:       args.AlphaWrap,
		AlphaWrapAlpha:  args.AlphaWrapAlpha,
		AlphaWrapOffset: args.AlphaWrapOffset,
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
