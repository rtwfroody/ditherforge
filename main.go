package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
// Fields tagged cache:"skip" do not affect the voxelization cache.
// All other fields are included in the cache key by default.
type Args struct {
	Input          string   `arg:"positional,required" help:"Input .glb or .3mf file"`
	Palette        string   `arg:"--palette" help:"Comma-separated colors (CSS names or hex). Default: best 4 of cyan,magenta,yellow,black,white,red,green,blue" cache:"skip"`
	AutoPalette    *int     `arg:"--auto-palette" help:"Compute N dominant colors from mesh surface" cache:"skip"`
	Scale          float32  `arg:"--scale" default:"1.0" help:"Additional scale multiplier"`
	Output         string   `arg:"--output" default:"output.3mf" help:"Output .3mf file" cache:"skip"`
	NozzleDiameter float32  `arg:"--nozzle-diameter" default:"0.4" help:"Nozzle diameter in mm"`
	LayerHeight    float32  `arg:"--layer-height" default:"0.2" help:"Layer height in mm"`
	InventoryFile  string   `arg:"--inventory-file" help:"File with one filament color per line (CSS names or hex)" cache:"skip"`
	Inventory      *int     `arg:"--inventory" help:"Pick best N colors from inventory file (requires --inventory-file)" cache:"skip"`
	Dither         string   `arg:"--dither" default:"dizzy" help:"Dithering mode: none, dizzy" cache:"skip"`
	NoMerge        bool     `arg:"--no-merge" help:"Skip coplanar triangle merging" cache:"skip"`
	NoSimplify     bool     `arg:"--no-simplify" help:"Skip QEM mesh decimation before clipping" cache:"skip"`
	Size           *float32 `arg:"--size" help:"Scale model so largest extent equals this value in mm"`
	Force          bool     `arg:"--force" help:"Bypass extent size check" cache:"skip"`
	Stats          bool     `arg:"--stats" help:"Print face counts per material" cache:"skip"`
	ColorSnap      float64  `arg:"--color-snap" default:"5" help:"Shift cell colors toward nearest palette color by this many delta E units (0 to disable)" cache:"skip"`
	NoCache        bool     `arg:"--no-cache" help:"Disable voxelization cache"`
}

func (Args) Description() string {
	return "Convert a textured GLB model to a multi-material 3MF file."
}

func (Args) Version() string {
	return "ditherforge 0.2.3-alpha"
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

	// Dispatch loader based on input extension.
	// GLB uses meters internally → multiply by 1000 to get mm.
	// 3MF is already in mm → no unit conversion needed.
	inputExt := strings.ToLower(filepath.Ext(args.Input))
	var baseScale float32
	switch inputExt {
	case ".glb":
		baseScale = unitScale * args.Scale
	case ".3mf":
		baseScale = args.Scale
	default:
		return fmt.Errorf("unsupported input format %q (use .glb or .3mf)", inputExt)
	}
	loadModel := func(scale float32) (*loader.LoadedModel, error) {
		switch inputExt {
		case ".glb":
			return loader.LoadGLB(args.Input, scale)
		default:
			return loader.Load3MF(args.Input, scale)
		}
	}
	scale := baseScale

	fmt.Printf("Loading %s...", args.Input)
	tLoad := time.Now()
	model, err := loadModel(scale)
	if err != nil {
		return fmt.Errorf("loading %s: %w", inputExt, err)
	}
	fmt.Printf(" %d vertices, %d faces in %.1fs\n", len(model.Vertices), len(model.Faces), time.Since(tLoad).Seconds())

	// Auto-scale to --size if specified.
	if args.Size != nil {
		ext := modelMaxExtent(model)
		if ext != *args.Size {
			rescale := *args.Size / ext
			fmt.Printf("  Rescaling to %.0f mm...", *args.Size)
			tRescale := time.Now()
			model, err = loadModel(scale * rescale)
			if err != nil {
				return fmt.Errorf("loading %s (rescaled): %w", inputExt, err)
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
	case "none", "dizzy":
	default:
		return fmt.Errorf("invalid --dither %q: must be none or dizzy", args.Dither)
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
		NoSimplify:     args.NoSimplify,
		ColorSnap:      args.ColorSnap,
	}

	cacheOpts := squarevoxel.CacheOptions{
		InputPath:  args.Input,
		ConfigHash: argsConfigHash(args),
	}
	var cached *squarevoxel.CacheData
	if !args.NoCache {
		cached = squarevoxel.LoadCache(cacheOpts)
	}

	fmt.Println("Remeshing...")
	outModel, assignments, paletteRGB, newCache, err := squarevoxel.Remesh(model, pcfg, cfg, args.Dither, cached)
	if err != nil {
		return fmt.Errorf("squarevoxel remesh: %w", err)
	}
	if newCache != nil {
		squarevoxel.SaveCache(newCache, cacheOpts)
	}

	if args.Stats {
		printStats(assignments, paletteRGB)
	}

	fmt.Printf("Exporting %s...", args.Output)
	tExport := time.Now()
	if err := export3mf.Export(outModel, assignments, args.Output, paletteRGB, args.LayerHeight); err != nil {
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

// argsConfigHash returns a SHA-256 hash of all Args fields that affect the
// voxelization cache. Fields tagged cache:"skip" are excluded. The app version
// is included so cache is invalidated on upgrades.
func argsConfigHash(args Args) [32]byte {
	h := sha256.New()
	fmt.Fprintf(h, "version=%s\n", Args{}.Version())

	v := reflect.ValueOf(args)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.Tag.Get("cache") == "skip" {
			continue
		}
		fv := v.Field(i)
		// Dereference pointers so we hash the value, not the address.
		if fv.Kind() == reflect.Ptr {
			if fv.IsNil() {
				fmt.Fprintf(h, "%s=<nil>\n", field.Name)
			} else {
				fmt.Fprintf(h, "%s=%v\n", field.Name, fv.Elem().Interface())
			}
		} else {
			fmt.Fprintf(h, "%s=%v\n", field.Name, fv.Interface())
		}
	}

	var hash [32]byte
	copy(hash[:], h.Sum(nil))
	return hash
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
