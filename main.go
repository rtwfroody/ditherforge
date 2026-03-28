package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/alexflint/go-arg"
	"github.com/rtwfroody/ditherforge/internal/export3mf"
	"github.com/rtwfroody/ditherforge/internal/hexvoxel"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/squarevoxel"
)

// Args defines the CLI arguments.
type Args struct {
	Input       string  `arg:"positional,required" help:"Input .glb file"`
	Palette     string  `arg:"--palette" default:"white,cyan,magenta,yellow" help:"Comma-separated colors (CSS names or hex)"`
	AutoPalette *int    `arg:"--auto-palette" help:"Compute N dominant colors from texture (mutually exclusive with --palette)"`
	GlbUnit     string  `arg:"--glb-unit" default:"m" help:"GLB coordinate unit: m, dm, cm, mm"`
	Scale       float32 `arg:"--scale" default:"1.0" help:"Additional scale multiplier"`
	Output      string  `arg:"--output" default:"output.3mf" help:"Output .3mf file"`
	Mode           string  `arg:"--mode" default:"hexvoxel" help:"Remesh mode: hexvoxel or squarevoxel"`
	NozzleDiameter float32 `arg:"--nozzle-diameter" default:"0.4" help:"Nozzle diameter in mm (hexvoxel mode)"`
	LayerHeight    float32 `arg:"--layer-height" default:"0.2" help:"Layer height in mm (hexvoxel mode)"`
	NoDither       bool    `arg:"--no-dither" help:"Disable Floyd-Steinberg dithering"`
	NoMerge        bool    `arg:"--no-merge" help:"Skip coplanar triangle merging"`
	Force          bool    `arg:"--force" help:"Bypass extent size check"`
	Stats          bool    `arg:"--stats" help:"Print face counts per material"`
}

func (Args) Description() string {
	return "Convert a textured GLB model to a multi-material 3MF file."
}

func run() error {
	var args Args
	arg.MustParse(&args)

	// Validate GlbUnit.
	unitScales := map[string]float32{
		"m":  1000.0,
		"dm": 100.0,
		"cm": 10.0,
		"mm": 1.0,
	}
	unitScale, ok := unitScales[args.GlbUnit]
	if !ok {
		return fmt.Errorf("invalid --glb-unit %q: must be one of m, dm, cm, mm", args.GlbUnit)
	}

	// Validate output extension.
	ext := strings.ToLower(args.Output)
	dotIdx := strings.LastIndex(ext, ".")
	outputExt := ""
	if dotIdx >= 0 {
		outputExt = ext[dotIdx:]
	}
	if outputExt != ".3mf" {
		return fmt.Errorf("output must be .3mf, got %q", outputExt)
	}

	scale := unitScale * args.Scale

	fmt.Printf("Loading %s...\n", args.Input)
	model, err := loader.LoadGLB(args.Input, scale)
	if err != nil {
		return fmt.Errorf("loading GLB: %w", err)
	}

	// Check model extent.
	if !args.Force {
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
		for i := 0; i < 3; i++ {
			ext := maxV[i] - minV[i]
			if ext > 300 {
				return fmt.Errorf("model extent %.0f mm exceeds 300 mm; use --scale to reduce size (or --force to bypass)", ext)
			}
		}
	}

	// Build palette.
	var paletteRGB [][3]uint8
	if args.AutoPalette != nil {
		n := *args.AutoPalette
		fmt.Printf("Computing %d-color palette from texture...\n", n)
		paletteRGB = palette.ComputePalette(model.Textures, n)
		hexStrs := make([]string, len(paletteRGB))
		for i, p := range paletteRGB {
			hexStrs[i] = fmt.Sprintf("#%02X%02X%02X", p[0], p[1], p[2])
		}
		fmt.Printf("  Palette: %s\n", strings.Join(hexStrs, ","))
	} else {
		colorStrs := strings.Split(args.Palette, ",")
		for i := range colorStrs {
			colorStrs[i] = strings.TrimSpace(colorStrs[i])
		}
		paletteRGB, err = palette.ParsePalette(colorStrs)
		if err != nil {
			return err
		}
	}

	if len(paletteRGB) > export3mf.MaxFilaments {
		return fmt.Errorf("palette has %d colors but max supported is %d", len(paletteRGB), export3mf.MaxFilaments)
	}

	switch args.Mode {
	case "hexvoxel":
		return runHexvoxel(args, model, paletteRGB)
	case "squarevoxel":
		return runSquarevoxel(args, model, paletteRGB)
	default:
		return fmt.Errorf("invalid --mode %q: must be hexvoxel or squarevoxel", args.Mode)
	}
}

func runHexvoxel(args Args, model *loader.LoadedModel, paletteRGB [][3]uint8) error {
	cfg := hexvoxel.Config{
		NozzleDiameter: args.NozzleDiameter,
		LayerHeight:    args.LayerHeight,
		NoMerge:        args.NoMerge,
	}

	fmt.Println("Generating hexagonal voxel shell...")
	hexModel, assignments, err := hexvoxel.Remesh(model, paletteRGB, cfg, !args.NoDither)
	if err != nil {
		return fmt.Errorf("hexvoxel remesh: %w", err)
	}
	fmt.Printf("  %d vertices, %d faces\n", len(hexModel.Vertices), len(hexModel.Faces))

	if args.Stats {
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

	fmt.Printf("Exporting %s...\n", args.Output)
	if err := export3mf.Export(hexModel, assignments, args.Output, paletteRGB, args.LayerHeight); err != nil {
		return fmt.Errorf("exporting 3MF: %w", err)
	}
	fmt.Println("Done.")
	return nil
}

func runSquarevoxel(args Args, model *loader.LoadedModel, paletteRGB [][3]uint8) error {
	cfg := squarevoxel.Config{
		NozzleDiameter: args.NozzleDiameter,
		LayerHeight:    args.LayerHeight,
		NoMerge:        args.NoMerge,
	}

	fmt.Println("Generating square voxel shell...")
	sqModel, assignments, err := squarevoxel.Remesh(model, paletteRGB, cfg, !args.NoDither)
	if err != nil {
		return fmt.Errorf("squarevoxel remesh: %w", err)
	}
	fmt.Printf("  %d vertices, %d faces\n", len(sqModel.Vertices), len(sqModel.Faces))

	if args.Stats {
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

	fmt.Printf("Exporting %s...\n", args.Output)
	if err := export3mf.Export(sqModel, assignments, args.Output, paletteRGB, args.LayerHeight); err != nil {
		return fmt.Errorf("exporting 3MF: %w", err)
	}
	fmt.Println("Done.")
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
