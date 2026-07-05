// Command dither-thumbs renders the static preview thumbnails used by the
// GUI's visual dither-mode picker (Appearance section, Step 4 of the settings
// redesign). It builds one canned test image — a horizontal colour gradient
// over a flat mid-grey patch — and dithers it with each of the six modes the
// GUI exposes, using the *actual* dither implementations from internal/voxel
// (the same functions the pipeline calls). The output is one ~96x64 PNG per
// mode, written to frontend/src/assets/dither/, imported by App.svelte.
//
// The pipeline's dither operates on surface cells connected by an adjacency
// graph, not on pixels. We drive it in image space the same way tests/
// ditherbench does: one ActiveCell per pixel (Col/Row = pixel x/y) with an
// 8-connected 2D neighbour graph whose weights mirror voxel.BuildNeighbors
// (face-adjacent = 1.0, diagonal = 0.1). Every mode is deterministic — the
// randomised modes seed math/rand with a fixed value — so re-running this
// generator reproduces byte-identical PNGs.
//
// Regenerate the committed thumbnails from the repo root with:
//
//	go run ./cmd/dither-thumbs
//
// The palette is fixed here (not derived from the image) so the thumbnails
// stay stable regardless of palette-clustering changes elsewhere.
package main

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"

	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

const (
	imgW = 96
	imgH = 64
	// gradientH rows form the top colour-gradient band; the remaining
	// rows are the flat grey patch. The gradient exposes drift/banding;
	// the flat patch exposes each algorithm's flat-area texture.
	gradientH = 40
)

// palette is the fixed 4-colour palette every thumbnail is dithered against.
// The two accents (warm orange, cool teal) plus near-black and near-white
// spread far enough apart that the gradient's muddy midpoint and the flat
// mid-grey both sit between palette entries, forcing visible dithering.
var palette = [][3]uint8{
	{32, 32, 32},    // near-black
	{224, 224, 224}, // near-white
	{224, 136, 64},  // orange
	{64, 160, 224},  // teal
}

// gradient endpoints and the flat-patch grey.
var (
	gradLeft  = [3]uint8{224, 136, 64}  // orange
	gradRight = [3]uint8{64, 160, 224}  // teal
	flatGrey  = [3]uint8{128, 128, 128} // equidistant-ish from all four
)

// modes maps each GUI dither value (see frontend DITHER_OPTIONS) to a runner
// that invokes the real internal/voxel implementation with the pipeline's
// default tuning. Output filenames match these values so the frontend can
// map card -> PNG directly.
type mode struct {
	value string
	run   func(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error)
}

var modes = []mode{
	{"riemersma", func(ctx context.Context, c []voxel.ActiveCell, p [][3]uint8, n [][]voxel.Neighbor) ([]int32, error) {
		return voxel.Riemersma(ctx, c, p, nil, n, voxel.RiemersmaInputBiasDefault, progress.NullTracker{})
	}},
	{"riemersma-pair", func(ctx context.Context, c []voxel.ActiveCell, p [][3]uint8, n [][]voxel.Neighbor) ([]int32, error) {
		return voxel.RiemersmaPair(ctx, c, p, nil, n, voxel.RiemersmaPairCancellationDefault, voxel.RiemersmaInputBiasDefault, progress.NullTracker{})
	}},
	{"blue-noise", func(ctx context.Context, c []voxel.ActiveCell, p [][3]uint8, n [][]voxel.Neighbor) ([]int32, error) {
		return voxel.BlueNoiseAdaptive(ctx, c, p, nil, n, voxel.BlueNoiseAdaptiveTolDefault, progress.NullTracker{})
	}},
	{"dizzy-corrected", func(ctx context.Context, c []voxel.ActiveCell, p [][3]uint8, n [][]voxel.Neighbor) ([]int32, error) {
		return voxel.DitherCorrected(ctx, c, p, nil, n, progress.NullTracker{})
	}},
	{"floyd-steinberg", func(ctx context.Context, c []voxel.ActiveCell, p [][3]uint8, n [][]voxel.Neighbor) ([]int32, error) {
		return voxel.FloydSteinberg(ctx, c, p, nil, n, progress.NullTracker{})
	}},
	{"none", func(ctx context.Context, c []voxel.ActiveCell, p [][3]uint8, _ [][]voxel.Neighbor) ([]int32, error) {
		return voxel.AssignColors(ctx, c, p)
	}},
}

func main() {
	outDir := "frontend/src/assets/dither"
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", outDir, err)
		os.Exit(1)
	}

	cells := buildCells()
	nbrs := buildNeighbors2D(cells)
	ctx := context.Background()

	for _, m := range modes {
		assignments, err := m.run(ctx, cells, palette, nbrs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "dither %s: %v\n", m.value, err)
			os.Exit(1)
		}
		img := renderImage(cells, assignments)
		path := filepath.Join(outDir, m.value+".png")
		if err := writePNG(path, img); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", path, err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s\n", path)
	}
}

// buildCells lays out the test image as one ActiveCell per pixel: a horizontal
// orange->teal gradient in the top band over a flat mid-grey patch below.
func buildCells() []voxel.ActiveCell {
	cells := make([]voxel.ActiveCell, 0, imgW*imgH)
	for y := range imgH {
		for x := range imgW {
			var c [3]uint8
			if y < gradientH {
				t := float64(x) / float64(imgW-1)
				c = lerp(gradLeft, gradRight, t)
			} else {
				c = flatGrey
			}
			cells = append(cells, voxel.ActiveCell{
				Col:   x,
				Row:   y,
				Cx:    float32(x),
				Cy:    float32(y),
				Color: c,
			})
		}
	}
	return cells
}

func lerp(a, b [3]uint8, t float64) [3]uint8 {
	return [3]uint8{
		uint8(math.Round(float64(a[0])*(1-t) + float64(b[0])*t)),
		uint8(math.Round(float64(a[1])*(1-t) + float64(b[1])*t)),
		uint8(math.Round(float64(a[2])*(1-t) + float64(b[2])*t)),
	}
}

// buildNeighbors2D gives each cell its 8-connected 2D neighbours via a
// (Col, Row) lookup. Weights mirror voxel.BuildNeighbors: face-adjacent = 1.0,
// diagonal = 0.1. (Kept local so the generator drives the real dither code
// without depending on the 3D grid layout BuildNeighbors expects.)
func buildNeighbors2D(cells []voxel.ActiveCell) [][]voxel.Neighbor {
	pos := make(map[[2]int]int, len(cells))
	for i, c := range cells {
		pos[[2]int{c.Col, c.Row}] = i
	}
	out := make([][]voxel.Neighbor, len(cells))
	for i, c := range cells {
		var nbs []voxel.Neighbor
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				j, ok := pos[[2]int{c.Col + dx, c.Row + dy}]
				if !ok {
					continue
				}
				w := float32(1.0)
				if dx != 0 && dy != 0 {
					w = 0.1
				}
				nbs = append(nbs, voxel.Neighbor{Idx: j, Weight: w})
			}
		}
		out[i] = nbs
	}
	return out
}

// renderImage paints each cell's assigned palette colour into an RGBA image.
func renderImage(cells []voxel.ActiveCell, assignments []int32) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, imgW, imgH))
	for i, c := range cells {
		p := palette[assignments[i]]
		img.SetNRGBA(c.Col, c.Row, color.NRGBA{R: p[0], G: p[1], B: p[2], A: 255})
	}
	return img
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(f, img); err != nil {
		return err
	}
	return f.Close()
}
