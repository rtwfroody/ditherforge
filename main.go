package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/rtwfroody/ditherforge/internal/export3mf"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/squarevoxel"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// Args defines the CLI arguments.
type Args struct {
	Input          string   `arg:"positional,required" help:"Input .glb file"`
	Palette        string   `arg:"--palette" help:"Comma-separated colors (CSS names or hex). Default: best 4 of cyan,magenta,yellow,black,white,red,green,blue"`
	AutoPalette    *int     `arg:"--auto-palette" help:"Compute N dominant colors from mesh surface"`
	Scale          float32  `arg:"--scale" default:"1.0" help:"Additional scale multiplier"`
	Output         string   `arg:"--output" default:"output.3mf" help:"Output .3mf file"`
	NozzleDiameter float32  `arg:"--nozzle-diameter" default:"0.4" help:"Nozzle diameter in mm"`
	LayerHeight    float32  `arg:"--layer-height" default:"0.2" help:"Layer height in mm"`
	InventoryFile  string   `arg:"--inventory-file" help:"File with one filament color per line (CSS names or hex)"`
	Inventory      *int     `arg:"--inventory" help:"Pick best N colors from inventory file (requires --inventory-file)"`
	Dither         string   `arg:"--dither" default:"dizzy" help:"Dithering mode: none, fs, dizzy"`
	NoMerge        bool     `arg:"--no-merge" help:"Skip coplanar triangle merging"`
	Size           *float32 `arg:"--size" help:"Scale model so largest extent equals this value in mm"`
	Force          bool     `arg:"--force" help:"Bypass extent size check"`
	Stats          bool     `arg:"--stats" help:"Print face counts per material"`
	Infill         bool     `arg:"--infill" help:"Generate infill object inside the shell"`
	InfillOnly     bool     `arg:"--infill-only" help:"Export only the infill mesh (for debugging, implies --infill)"`
}

func (Args) Description() string {
	return "Convert a textured GLB model to a multi-material 3MF file."
}

func (Args) Version() string {
	return "ditherforge 0.1.2-alpha"
}

func run() error {
	var args Args
	arg.MustParse(&args)

	// GLB files use meters by default; convert to mm.
	const unitScale = float32(1000.0)

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

	fmt.Printf("Loading %s...", args.Input)
	tLoad := time.Now()
	model, err := loader.LoadGLB(args.Input, scale)
	if err != nil {
		return fmt.Errorf("loading GLB: %w", err)
	}
	fmt.Printf(" %d vertices, %d faces in %.1fs\n", len(model.Vertices), len(model.Faces), time.Since(tLoad).Seconds())

	// Auto-scale to --size if specified.
	if args.Size != nil {
		ext := modelMaxExtent(model)
		if ext != *args.Size {
			rescale := *args.Size / ext
			fmt.Printf("  Rescaling to %.0f mm...", *args.Size)
			tRescale := time.Now()
			model, err = loader.LoadGLB(args.Input, scale*rescale)
			if err != nil {
				return fmt.Errorf("loading GLB (rescaled): %w", err)
			}
			fmt.Printf(" done in %.1fs\n", time.Since(tRescale).Seconds())
		}
	}

	ex := modelExtents(model)
	fmt.Printf("  Extent: %.1f x %.1f x %.1f mm\n", ex[0], ex[1], ex[2])

	// Check model extent.
	if !args.Force {
		ext := modelMaxExtent(model)
		if ext > 300 {
			return fmt.Errorf("model extent %.0f mm exceeds 300 mm; use --scale or --size to reduce size (or --force to bypass)", ext)
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
		// Default: pick best 4 from a standard set of colors.
		defaultColors := []string{"cyan", "magenta", "yellow", "black", "white", "red", "green", "blue"}
		for _, name := range defaultColors {
			rgb, _ := palette.ParsePalette([]string{name})
			pcfg.Inventory = append(pcfg.Inventory, palette.InventoryEntry{Color: rgb[0], Label: name})
		}
		pcfg.InventoryN = 4
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

	return runRemesh(args, model, pcfg)
}

func runRemesh(args Args, model *loader.LoadedModel, pcfg voxel.PaletteConfig) error {
	cfg := squarevoxel.Config{
		NozzleDiameter: args.NozzleDiameter,
		LayerHeight:    args.LayerHeight,
		NoMerge:        args.NoMerge,
		Infill:         args.Infill || args.InfillOnly,
	}

	fmt.Println("Remeshing...")
	meshParts, paletteRGB, err := squarevoxel.Remesh(model, pcfg, cfg, args.Dither)
	if err != nil {
		return fmt.Errorf("squarevoxel remesh: %w", err)
	}

	if args.Stats {
		printStats(meshParts, paletteRGB)
	}

	fmt.Printf("Exporting %s...", args.Output)
	tExport := time.Now()
	exportParts := filterParts(meshParts, args.InfillOnly)
	parts := meshPartsToExportParts(exportParts)
	if err := export3mf.Export(parts, args.Output, paletteRGB, args.LayerHeight); err != nil {
		return fmt.Errorf("exporting 3MF: %w", err)
	}
	fmt.Printf(" done in %.1fs\n", time.Since(tExport).Seconds())
	return nil
}

// modelExtents returns the bounding box extents [x, y, z] in mm.
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

// modelMaxExtent returns the largest bounding box extent in mm.
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

func filterParts(meshParts []voxel.MeshPart, infillOnly bool) []voxel.MeshPart {
	if infillOnly && len(meshParts) > 1 {
		return meshParts[1:]
	}
	return meshParts
}

func meshPartsToExportParts(meshParts []voxel.MeshPart) []export3mf.Part {
	parts := make([]export3mf.Part, len(meshParts))
	for i, mp := range meshParts {
		parts[i] = export3mf.Part{
			Model:       mp.Model,
			Assignments: mp.Assignments,
		}
	}
	return parts
}

func printStats(meshParts []voxel.MeshPart, paletteRGB [][3]uint8) {
	fmt.Println("  Face counts per material:")
	for i, p := range paletteRGB {
		hexColor := fmt.Sprintf("#%02X%02X%02X", p[0], p[1], p[2])
		count := 0
		for _, mp := range meshParts {
			for _, a := range mp.Assignments {
				if int(a) == i {
					count++
				}
			}
		}
		fmt.Printf("    [%d] %s: %d faces\n", i, hexColor, count)
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
