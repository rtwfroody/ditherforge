// Package ditherpreview drives the real internal/voxel dither implementations
// over an image (one ActiveCell per pixel) to produce image-space previews of
// each GUI dither mode. It is the shared core behind two callers:
//
//   - cmd/dither-thumbs, which renders the committed static picker thumbnails
//     from a fixed gradient test image; and
//   - the App.DitherModePreviews Wails endpoint, which renders live previews of
//     the currently loaded model for the Appearance section's mode picker.
//
// The pipeline's dither operates on surface cells connected by an adjacency
// graph, not on pixels. We drive it in image space the same way tests/
// ditherbench does: one ActiveCell per pixel (Col/Row = pixel x/y) with an
// 8-connected 2D neighbour graph whose weights mirror voxel.BuildNeighbors
// (face-adjacent = 1.0, diagonal = 0.1). Every mode is deterministic — the
// randomised modes seed math/rand with a fixed value inside internal/voxel —
// so a given (image, palette, mode, tuning) always yields the same output.
//
// This package is read-only: it touches no pipeline state, cache, or settings.
package ditherpreview

import (
	"context"
	"fmt"
	"image"
	"image/color"

	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// Mode identifiers match the frontend DITHER_OPTIONS values exactly (the
// strings persisted to settings JSON), so a caller can map mode -> asset or
// mode -> card directly.
const (
	ModeRiemersma      = "riemersma"
	ModeRiemersmaPair  = "riemersma-pair"
	ModeBlueNoise      = "blue-noise"
	ModeDizzyCorrected = "dizzy-corrected"
	ModeFloydSteinberg = "floyd-steinberg"
	ModeNone           = "none"
)

// Modes lists the six GUI dither modes in the picker's display order.
var Modes = []string{
	ModeRiemersma,
	ModeRiemersmaPair,
	ModeBlueNoise,
	ModeDizzyCorrected,
	ModeFloydSteinberg,
	ModeNone,
}

// Tuning carries the two adjustable knobs the GUI exposes as sliders. Use
// DefaultTuning() for the pipeline's canonical values and override as needed.
type Tuning struct {
	RiemersmaBias float64 // Riemersma / Riemersma-pair input bias (0..1)
	BlueNoiseTol  float64 // Blue-noise bracket tolerance (ΔE)
}

// DefaultTuning returns the same per-mode tuning constants the pipeline uses.
func DefaultTuning() Tuning {
	return Tuning{
		RiemersmaBias: voxel.RiemersmaInputBiasDefault,
		BlueNoiseTol:  voxel.BlueNoiseAdaptiveTolDefault,
	}
}

// DitherImage dithers img against palette using the named mode and returns a
// freshly rendered image the same size as img, with each pixel painted its
// assigned palette colour. The returned image has origin (0,0) regardless of
// img's bounds origin.
func DitherImage(ctx context.Context, img image.Image, palette [][3]uint8, mode string, tuning Tuning) (*image.NRGBA, error) {
	if len(palette) == 0 {
		return nil, fmt.Errorf("ditherpreview: empty palette")
	}
	cells := buildCells(img)
	nbrs := buildNeighbors2D(cells)
	assignments, err := runMode(ctx, mode, cells, palette, nbrs, tuning)
	if err != nil {
		return nil, err
	}
	return renderImage(img.Bounds(), cells, palette, assignments), nil
}

// runMode invokes the real internal/voxel implementation for mode, wiring the
// tuning knobs through and using the pipeline's fixed constants for the knobs
// the GUI does not expose (RiemersmaPair cancellation lambda).
func runMode(ctx context.Context, mode string, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor, tuning Tuning) ([]int32, error) {
	switch mode {
	case ModeRiemersma:
		return voxel.Riemersma(ctx, cells, pal, nil, nbrs, tuning.RiemersmaBias, progress.NullTracker{})
	case ModeRiemersmaPair:
		return voxel.RiemersmaPair(ctx, cells, pal, nil, nbrs, voxel.RiemersmaPairCancellationDefault, tuning.RiemersmaBias, progress.NullTracker{})
	case ModeBlueNoise:
		return voxel.BlueNoiseAdaptive(ctx, cells, pal, nil, nbrs, tuning.BlueNoiseTol, progress.NullTracker{})
	case ModeDizzyCorrected:
		return voxel.DitherCorrected(ctx, cells, pal, nil, nbrs, progress.NullTracker{})
	case ModeFloydSteinberg:
		return voxel.FloydSteinberg(ctx, cells, pal, nil, nbrs, progress.NullTracker{})
	case ModeNone:
		return voxel.AssignColors(ctx, cells, pal)
	default:
		return nil, fmt.Errorf("ditherpreview: unknown dither mode %q", mode)
	}
}

// buildCells lays out img as one ActiveCell per pixel in row-major order
// (y outer, x inner), reading each pixel's colour as an 8-bit-per-channel RGB
// triple. Alpha is ignored — the preview is fully opaque.
func buildCells(img image.Image) []voxel.ActiveCell {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	cells := make([]voxel.ActiveCell, 0, w*h)
	for y := range h {
		for x := range w {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			cells = append(cells, voxel.ActiveCell{
				Col:   x,
				Row:   y,
				Cx:    float32(x),
				Cy:    float32(y),
				Color: [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8)},
			})
		}
	}
	return cells
}

// buildNeighbors2D gives each cell its 8-connected 2D neighbours via a
// (Col, Row) lookup. Weights mirror voxel.BuildNeighbors: face-adjacent = 1.0,
// diagonal = 0.1. (Kept local so the preview drives the real dither code
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

// renderImage paints each cell's assigned palette colour into an opaque NRGBA
// image sized to bounds (with origin reset to 0,0).
func renderImage(bounds image.Rectangle, cells []voxel.ActiveCell, palette [][3]uint8, assignments []int32) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for i, c := range cells {
		p := palette[assignments[i]]
		img.SetNRGBA(c.Col, c.Row, color.NRGBA{R: p[0], G: p[1], B: p[2], A: 255})
	}
	return img
}
