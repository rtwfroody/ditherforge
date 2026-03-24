package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexflint/go-arg"
	"github.com/rtwfroody/text2filament/internal/export3mf"
	"github.com/rtwfroody/text2filament/internal/loader"
	"github.com/rtwfroody/text2filament/internal/palette"
	"github.com/rtwfroody/text2filament/internal/sample"
	"github.com/rtwfroody/text2filament/internal/subdivide"
)

// Args defines the CLI arguments.
type Args struct {
	Input       string  `arg:"positional,required" help:"Input .glb file"`
	Palette     string  `arg:"--palette" default:"white,red,green,blue" help:"Comma-separated colors (CSS names or hex)"`
	AutoPalette *int    `arg:"--auto-palette" help:"Compute N dominant colors from texture (mutually exclusive with --palette)"`
	Resolution  float32 `arg:"--resolution" default:"0.5" help:"Target max edge length in mm (default: 0.5)"`
	GlbUnit     string  `arg:"--glb-unit" default:"m" help:"GLB coordinate unit: m, dm, cm, mm"`
	Scale       float32 `arg:"--scale" default:"1.0" help:"Additional scale multiplier"`
	Output      string  `arg:"--output" default:"output.3mf" help:"Output .3mf file"`
	ColorSpace  string  `arg:"--color-space" default:"cielab" help:"Color distance: cielab or rgb"`
	Printer      string `arg:"--printer" default:"snapmaker-u1" help:"Printer profile: snapmaker-u1, bambu-a1, bambu-p2s"`
	OrcaProfiles string `arg:"--orca-profiles" help:"Path to OrcaSlicer profiles directory"`
	NoDither     bool   `arg:"--no-dither" help:"Disable Floyd-Steinberg dithering"`
	Stats        bool   `arg:"--stats" help:"Print face counts per material"`
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

	// Validate printer.
	printerDef, ok := export3mf.Printers[args.Printer]
	if !ok {
		var names []string
		for k := range export3mf.Printers {
			names = append(names, k)
		}
		return fmt.Errorf("invalid --printer %q: must be one of %s", args.Printer, strings.Join(names, ", "))
	}

	// Find OrcaSlicer profiles directory.
	profilesDir := args.OrcaProfiles
	if profilesDir == "" {
		// Search common locations.
		home, _ := os.UserHomeDir()
		candidates := []string{
			filepath.Join(home, "programs", "OrcaSlicer", "resources", "profiles"),
			"/usr/share/OrcaSlicer/resources/profiles",
			"/opt/OrcaSlicer/resources/profiles",
		}
		for _, c := range candidates {
			if fi, err := os.Stat(c); err == nil && fi.IsDir() {
				profilesDir = c
				break
			}
		}
		if profilesDir == "" {
			fmt.Println("  Warning: OrcaSlicer profiles not found; 3MF will have minimal printer settings.")
			fmt.Println("  Use --orca-profiles to specify the path.")
		}
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

	// Subdivide.
	resolution := args.Resolution
	var roots []*subdivide.Node
	var subdivVerts [][3]float32
	var subdivUVs [][2]float32
	var leafModel *loader.LoadedModel
	for {
		fmt.Printf("Subdividing to %.4g mm max edge length...\n", resolution)
		var tooMany *subdivide.TooManyVerticesError
		roots, subdivVerts, subdivUVs, err = subdivide.Subdivide(model, resolution, 1_000_000)
		if errors.As(err, &tooMany) {
			resolution *= 1.5
			fmt.Fprintf(os.Stderr, "  Would exceed 1,000,000 vertices; retrying with resolution %.4g mm...\n", resolution)
			continue
		}
		if err != nil {
			return fmt.Errorf("subdivision: %w", err)
		}
		leafModel = subdivide.Leaves(roots, subdivVerts, subdivUVs, model)
		fmt.Printf("  %d vertices, %d faces after subdivision\n", len(leafModel.Vertices), len(leafModel.Faces))
		break
	}

	// Sample and assign palette.
	var assignments []int32
	if !args.NoDither {
		fmt.Println("Sampling texture colors (Floyd-Steinberg dither)...")
		assignments = sample.SampleFaceIndices(leafModel, paletteRGB)
	} else {
		fmt.Println("Sampling texture colors...")
		faceColors := sample.SampleFaceColors(leafModel)
		fmt.Println("Matching palette...")
		if args.ColorSpace == "rgb" {
			assignments = assignRGB(faceColors, paletteRGB)
		} else {
			assignments = palette.AssignPalette(faceColors, paletteRGB)
		}
	}

	// Override no-texture faces to palette[0].
	if leafModel.NoTextureMask != nil {
		for i, noTex := range leafModel.NoTextureMask {
			if noTex {
				assignments[i] = 0
			}
		}
	}

	// Merge: collapse subtrees where all leaves share one color back to the
	// ancestor face, reducing output triangle count.
	fmt.Println("Merging uniform regions...")
	mergedFaces := subdivide.Merge(roots, assignments)
	model, assignments = subdivide.BuildModel(mergedFaces, subdivVerts, subdivUVs, model)
	before, after := len(leafModel.Faces), len(model.Faces)
	pct := 100.0 * float64(before-after) / float64(before)
	fmt.Printf("  %d faces after merge (reduced from %d, %.1f%% smaller)\n", after, before, pct)

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
	if err := export3mf.Export(model, assignments, args.Output, paletteRGB, printerDef, profilesDir); err != nil {
		return fmt.Errorf("exporting 3MF: %w", err)
	}
	fmt.Println("Done.")
	return nil
}

// assignRGB assigns faces using RGB Euclidean distance.
func assignRGB(faceColors [][3]uint8, pal [][3]uint8) []int32 {
	assignments := make([]int32, len(faceColors))
	for fi, fc := range faceColors {
		bestIdx := 0
		bestDist := float64(1e18)
		for pi, p := range pal {
			dr := float64(int(fc[0]) - int(p[0]))
			dg := float64(int(fc[1]) - int(p[1]))
			db := float64(int(fc[2]) - int(p[2]))
			d := dr*dr + dg*dg + db*db
			if d < bestDist {
				bestDist = d
				bestIdx = pi
			}
		}
		assignments[fi] = int32(bestIdx)
	}
	return assignments
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
