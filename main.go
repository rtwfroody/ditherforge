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
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// Args defines the CLI arguments.
type Args struct {
	Input       string  `arg:"positional,required" help:"Input .glb file"`
	Palette     string  `arg:"--palette" help:"Comma-separated colors (CSS names or hex). Default: cyan,magenta,yellow + black or white based on texture brightness"`
	AutoPalette *int    `arg:"--auto-palette" help:"Compute N dominant colors from mesh surface"`
	GlbUnit     string  `arg:"--glb-unit" default:"m" help:"GLB coordinate unit: m, dm, cm, mm"`
	Scale       float32 `arg:"--scale" default:"1.0" help:"Additional scale multiplier"`
	Output      string  `arg:"--output" default:"output.3mf" help:"Output .3mf file"`
	Mode            string   `arg:"--mode" default:"squarevoxel" help:"Remesh mode: squarevoxel or hexvoxel"`
	NozzleDiameter  float32  `arg:"--nozzle-diameter" default:"0.4" help:"Nozzle diameter in mm (hexvoxel mode)"`
	LayerHeight     float32  `arg:"--layer-height" default:"0.2" help:"Layer height in mm (hexvoxel mode)"`
	InventoryFile   string   `arg:"--inventory-file" help:"File with one filament color per line (CSS names or hex)"`
	Inventory       *int     `arg:"--inventory" help:"Pick best N colors from inventory file (requires --inventory-file)"`
Dither          string   `arg:"--dither" default:"dizzy" help:"Dithering mode: none, fs, dizzy"`
	NoMerge         bool     `arg:"--no-merge" help:"Skip coplanar triangle merging"`
	Extent          *float32 `arg:"--extent" help:"Scale model so largest extent equals this value in mm"`
	Force           bool     `arg:"--force" help:"Bypass extent size check"`
	Stats           bool     `arg:"--stats" help:"Print face counts per material"`
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

	// Auto-scale to --extent if specified.
	if args.Extent != nil {
		ext := modelMaxExtent(model)
		if ext != *args.Extent {
			rescale := *args.Extent / ext
			fmt.Printf("  Extent %.1f mm, target %.0f mm, scaling by %.4f\n", ext, *args.Extent, rescale)
			model, err = loader.LoadGLB(args.Input, scale*rescale)
			if err != nil {
				return fmt.Errorf("loading GLB (rescaled): %w", err)
			}
		}
	}

	// Check model extent.
	if !args.Force {
		ext := modelMaxExtent(model)
		if ext > 300 {
			return fmt.Errorf("model extent %.0f mm exceeds 300 mm; use --scale or --extent to reduce size (or --force to bypass)", ext)
		}
	}

	// Validate flags.
	if args.Inventory != nil && args.InventoryFile == "" {
		return fmt.Errorf("--inventory requires --inventory-file")
	}
	switch args.Dither {
	case "none", "fs", "dizzy":
	default:
		return fmt.Errorf("invalid --dither %q: must be none, fs, or dizzy", args.Dither)
	}
	// Build palette config. For inventory and auto-palette modes, the actual
	// palette is determined after voxelization using real cell colors.
	var pcfg voxel.PaletteConfig
	if args.Inventory != nil {
		inv, err := palette.ParseInventoryFile(args.InventoryFile)
		if err != nil {
			return err
		}
		pcfg.Inventory = inv
		pcfg.InventoryN = *args.Inventory
	} else if args.AutoPalette != nil {
		pcfg.AutoPaletteN = *args.AutoPalette
	} else if args.Palette == "" {
		// Default palette: CMY + black or white based on texture brightness.
		bw := "white"
		if averageTextureBrightness(model) < 85 {
			bw = "black"
		}
		fmt.Printf("  Default palette: cyan,magenta,yellow,%s\n", bw)
		pcfg.Palette, _ = palette.ParsePalette([]string{"cyan", "magenta", "yellow", bw})
	} else {
		colorStrs := strings.Split(args.Palette, ",")
		for i := range colorStrs {
			colorStrs[i] = strings.TrimSpace(colorStrs[i])
		}
		pcfg.Palette, err = palette.ParsePalette(colorStrs)
		if err != nil {
			return err
		}
	}

	if pcfg.Palette != nil && len(pcfg.Palette) > export3mf.MaxFilaments {
		return fmt.Errorf("palette has %d colors but max supported is %d", len(pcfg.Palette), export3mf.MaxFilaments)
	}

	switch args.Mode {
	case "hexvoxel":
		return runHexvoxel(args, model, pcfg)
	case "squarevoxel":
		return runSquarevoxel(args, model, pcfg)
	default:
		return fmt.Errorf("invalid --mode %q: must be hexvoxel or squarevoxel", args.Mode)
	}
}

func runHexvoxel(args Args, model *loader.LoadedModel, pcfg voxel.PaletteConfig) error {
	cfg := hexvoxel.Config{
		NozzleDiameter: args.NozzleDiameter,
		LayerHeight:    args.LayerHeight,
		NoMerge:        args.NoMerge,
	}

	fmt.Println("Generating hexagonal voxel shell...")
	hexModel, assignments, paletteRGB, err := hexvoxel.Remesh(model, pcfg, cfg, args.Dither)
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

func runSquarevoxel(args Args, model *loader.LoadedModel, pcfg voxel.PaletteConfig) error {
	cfg := squarevoxel.Config{
		NozzleDiameter: args.NozzleDiameter,
		LayerHeight:    args.LayerHeight,
		NoMerge:        args.NoMerge,
	}

	fmt.Println("Generating square voxel shell...")
	sqModel, assignments, paletteRGB, err := squarevoxel.Remesh(model, pcfg, cfg, args.Dither)
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

// modelMaxExtent returns the largest bounding box extent in mm.
func modelMaxExtent(model *loader.LoadedModel) float32 {
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
	ext := float32(0)
	for i := 0; i < 3; i++ {
		d := maxV[i] - minV[i]
		if d > ext {
			ext = d
		}
	}
	return ext
}

// averageTextureBrightness samples the model's textures and returns an
// average brightness in 0-255. Used to pick black vs white for the default palette.
func averageTextureBrightness(model *loader.LoadedModel) float64 {
	if len(model.Textures) == 0 {
		return 128
	}
	var totalR, totalG, totalB float64
	var count int
	for _, tex := range model.Textures {
		bounds := tex.Bounds()
		// Sample every 16th pixel for speed.
		for y := bounds.Min.Y; y < bounds.Max.Y; y += 16 {
			for x := bounds.Min.X; x < bounds.Max.X; x += 16 {
				r, g, b, _ := tex.At(x, y).RGBA()
				totalR += float64(r >> 8)
				totalG += float64(g >> 8)
				totalB += float64(b >> 8)
				count++
			}
		}
	}
	if count == 0 {
		return 128
	}
	// Perceived brightness (ITU-R BT.601).
	return (0.299*totalR + 0.587*totalG + 0.114*totalB) / float64(count)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
