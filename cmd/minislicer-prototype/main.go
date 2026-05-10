// minislicer-prototype is a standalone driver for the
// internal/minislicer package: it loads a model, slices it, dithers
// across the section graph, writes per-layer SVGs, and reports
// whether each colored patch meets the min-length criterion.
package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexflint/go-arg"

	"github.com/rtwfroody/ditherforge/internal/export3mf"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/minislicer"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

type args struct {
	Input     string  `arg:"required,positional" help:"input model: .stl, .glb, .3mf"`
	Inventory string  `arg:"--inventory" help:"path to color inventory file (one #RRGGBB per line, optional label)"`
	NumColors int     `arg:"--colors" default:"8" help:"palette size"`
	CellSize  float32 `arg:"--cell" default:"1.0" help:"target arc length per section, in mm (a.k.a. nozzle diameter)"`
	LayerH    float32 `arg:"--layer" default:"0.2" help:"layer height in mm"`
	Out       string  `arg:"--out" default:"./minislicer_out" help:"output directory for SVGs"`
	Size      float32 `arg:"--size" default:"0" help:"normalize model so its largest dimension equals this many mm; 0 = no rescale (after unit conversion)"`
	ThreeMF   string  `arg:"--3mf" help:"if set, also write a 3MF artifact at this path (per-section painted layer prisms)"`
	Verbose   bool    `arg:"--verbose,-v"`
}

func (args) Description() string {
	return "Prototype mini-slicer: per-layer color partition + dither + SVG visualization."
}

func main() {
	var a args
	arg.MustParse(&a)

	model, err := loadModel(a.Input)
	if err != nil {
		log.Fatalf("load %s: %v", a.Input, err)
	}
	// Convert to mm. GLB files are in meters; STL/3MF assume mm.
	scale := unitScaleForExt(filepath.Ext(a.Input))
	if scale != 1 {
		loader.ScaleModel(model, scale)
	}
	if a.Verbose {
		log.Printf("loaded %s: %d verts, %d faces (scale ×%g)",
			a.Input, len(model.Vertices), len(model.Faces), scale)
	}

	if a.Size > 0 {
		extent := maxExtent(model)
		if extent > 0 {
			s := a.Size / extent
			loader.ScaleModel(model, s)
			if a.Verbose {
				log.Printf("normalized: largest dim ×%g → %.2f mm", s, a.Size)
			}
		}
	}

	zMin, zMax := zRange(model)
	if zMax <= zMin {
		log.Fatalf("model has zero Z extent (zMin=%g, zMax=%g)", zMin, zMax)
	}
	planes := minislicer.PlanesForRange(zMin, zMax, a.LayerH)
	if a.Verbose {
		log.Printf("Z range: [%.3f, %.3f] mm; %d slicing planes at layerH=%g",
			zMin, zMax, len(planes), a.LayerH)
	}

	layers := minislicer.SliceMesh(model, planes)
	totalLoops := 0
	for _, l := range layers {
		totalLoops += len(l.Loops)
	}
	if a.Verbose {
		log.Printf("sliced: %d non-empty layers, %d total loops",
			countNonEmpty(layers), totalLoops)
	}

	sections := minislicer.PartitionLoops(layers, a.CellSize)
	if a.Verbose {
		log.Printf("partitioned: %d sections (target length %.3f mm)", len(sections), a.CellSize)
	}

	si := voxel.NewSpatialIndex(model, a.CellSize)
	colors, alpha := minislicer.SampleSectionColors(model, si, sections, a.CellSize)
	if a.Verbose {
		visible := 0
		for _, v := range alpha {
			if v {
				visible++
			}
		}
		log.Printf("sampled %d colors (%d visible)", len(colors), visible)
	}

	neighbors := minislicer.BuildSectionGraph(sections, layers, a.CellSize)

	inv, err := loadInventory(a.Inventory)
	if err != nil {
		log.Fatalf("inventory: %v", err)
	}
	pcfg := voxel.PaletteConfig{
		NumColors: a.NumColors,
		Inventory: inv,
	}
	pal, palLabels, palSrc, assignments, err := minislicer.DitherSections(
		context.Background(), sections, colors, alpha, neighbors,
		pcfg, a.LayerH, nil)
	if err != nil {
		log.Fatalf("dither: %v", err)
	}
	if a.Verbose {
		log.Printf("palette (source=%s): %d colors", palSrc, len(pal))
		for i, c := range pal {
			label := ""
			if i < len(palLabels) {
				label = palLabels[i]
			}
			log.Printf("  pal[%d] = #%02x%02x%02x %s", i, c[0], c[1], c[2], label)
		}
	}

	cfg := minislicer.DefaultRenderConfig(a.Out)
	if err := minislicer.RenderLayers(layers, sections, pal, assignments, cfg); err != nil {
		log.Fatalf("render: %v", err)
	}

	if a.ThreeMF != "" {
		mesh, faceAssign := minislicer.BuildPrintableMesh(layers, sections, assignments, a.LayerH)
		// Find a fallback color for interior faces (we want the
		// most common section color in the model so the slicer
		// renders a sensible interior).
		fallback := mostCommonAssignment(assignments)
		safe := minislicer.SafeAssignments(faceAssign, fallback)
		if a.Verbose {
			log.Printf("3MF mesh: %d verts, %d faces (interior fallback color = pal[%d])",
				len(mesh.Vertices), len(mesh.Faces), fallback)
		}
		expOpts := export3mf.Options{
			PrinterID:      export3mf.DefaultPrinterID,
			NozzleDiameter: 0, // 0 = printer default (typically 0.4 mm)
			LayerHeight:    a.LayerH,
		}
		if err := export3mf.Export(mesh, safe, a.ThreeMF, pal, expOpts); err != nil {
			log.Fatalf("export 3MF: %v", err)
		}
		if a.Verbose {
			log.Printf("wrote 3MF: %s", a.ThreeMF)
		}
	}

	reports, ok := minislicer.VerifyPatchLengths(sections, layers, assignments, a.CellSize)
	fmt.Println(minislicer.FormatReport(reports, a.CellSize))
	if !ok {
		fmt.Fprintln(os.Stderr, "FAIL: at least one colored patch is shorter than cellSize")
		os.Exit(1)
	}
	fmt.Println("OK: all colored patches >= cellSize")
}

func loadModel(path string) (*loader.LoadedModel, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".glb":
		return loader.LoadGLB(path, 0)
	case ".3mf":
		return loader.Load3MF(path, 0)
	case ".stl":
		return loader.LoadSTL(path, 0)
	default:
		return nil, fmt.Errorf("unsupported format %q (use .glb, .3mf, or .stl)", ext)
	}
}

func unitScaleForExt(ext string) float32 {
	if strings.ToLower(ext) == ".glb" {
		return 1000
	}
	return 1
}

func maxExtent(m *loader.LoadedModel) float32 {
	if len(m.Vertices) == 0 {
		return 0
	}
	mn := m.Vertices[0]
	mx := m.Vertices[0]
	for _, v := range m.Vertices {
		for k := 0; k < 3; k++ {
			if v[k] < mn[k] {
				mn[k] = v[k]
			}
			if v[k] > mx[k] {
				mx[k] = v[k]
			}
		}
	}
	var ext float32
	for k := 0; k < 3; k++ {
		if d := mx[k] - mn[k]; d > ext {
			ext = d
		}
	}
	return ext
}

func zRange(m *loader.LoadedModel) (zMin, zMax float32) {
	zMin, zMax = float32(math.Inf(1)), float32(math.Inf(-1))
	for _, v := range m.Vertices {
		if v[2] < zMin {
			zMin = v[2]
		}
		if v[2] > zMax {
			zMax = v[2]
		}
	}
	return
}

func countNonEmpty(layers []minislicer.Layer) int {
	n := 0
	for _, l := range layers {
		if len(l.Loops) > 0 {
			n++
		}
	}
	return n
}

// mostCommonAssignment returns the palette index that appears most
// often in assignments, ignoring -1 (hidden) entries.
func mostCommonAssignment(assignments []int32) int32 {
	counts := map[int32]int{}
	for _, a := range assignments {
		if a >= 0 {
			counts[a]++
		}
	}
	var best int32
	bestN := -1
	for k, v := range counts {
		if v > bestN {
			best = k
			bestN = v
		}
	}
	return best
}

// loadInventory reads an inventory file or returns a small default
// palette of saturated colors when path is empty.
func loadInventory(path string) ([]palette.InventoryEntry, error) {
	if path == "" {
		// Default inventory: 8 saturated primaries.
		return []palette.InventoryEntry{
			{Color: [3]uint8{0xff, 0x00, 0x00}, Label: "red"},
			{Color: [3]uint8{0x00, 0xff, 0x00}, Label: "green"},
			{Color: [3]uint8{0x00, 0x00, 0xff}, Label: "blue"},
			{Color: [3]uint8{0xff, 0xff, 0x00}, Label: "yellow"},
			{Color: [3]uint8{0xff, 0x00, 0xff}, Label: "magenta"},
			{Color: [3]uint8{0x00, 0xff, 0xff}, Label: "cyan"},
			{Color: [3]uint8{0xff, 0xff, 0xff}, Label: "white"},
			{Color: [3]uint8{0x00, 0x00, 0x00}, Label: "black"},
		}, nil
	}
	return palette.ParseInventoryFile(path)
}
