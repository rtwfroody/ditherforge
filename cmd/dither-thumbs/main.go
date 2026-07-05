// Command dither-thumbs renders the static preview thumbnails used by the
// GUI's visual dither-mode picker (Appearance section, Step 4 of the settings
// redesign). It builds one canned test image — a horizontal colour gradient
// over a flat mid-grey patch — and dithers it with each of the six modes the
// GUI exposes via internal/ditherpreview, which drives the *actual* dither
// implementations from internal/voxel (the same functions the pipeline calls).
// The output is one ~96x64 PNG per mode, written to frontend/src/assets/dither/,
// imported by App.svelte as the committed fallback thumbnails.
//
// These static PNGs are the fallback shown before a model loads (and on any
// error); the live per-model previews go through the same internal/ditherpreview
// core via the App.DitherModePreviews Wails endpoint.
//
// Every mode is deterministic — the randomised modes seed math/rand with a
// fixed value inside internal/voxel — so re-running this generator reproduces
// byte-identical PNGs.
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

	"github.com/rtwfroody/ditherforge/internal/ditherpreview"
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

func main() {
	outDir := "frontend/src/assets/dither"
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", outDir, err)
		os.Exit(1)
	}

	src := buildTestImage()
	ctx := context.Background()

	for _, mode := range ditherpreview.Modes {
		img, err := ditherpreview.DitherImage(ctx, src, palette, mode, ditherpreview.DefaultTuning())
		if err != nil {
			fmt.Fprintf(os.Stderr, "dither %s: %v\n", mode, err)
			os.Exit(1)
		}
		path := filepath.Join(outDir, mode+".png")
		if err := writePNG(path, img); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", path, err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s\n", path)
	}
}

// buildTestImage lays out the canned test image: a horizontal orange->teal
// gradient in the top band over a flat mid-grey patch below. Colours are baked
// to 8-bit-per-channel so reading them back in ditherpreview is lossless.
func buildTestImage() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, imgW, imgH))
	for y := range imgH {
		for x := range imgW {
			var c [3]uint8
			if y < gradientH {
				t := float64(x) / float64(imgW-1)
				c = lerp(gradLeft, gradRight, t)
			} else {
				c = flatGrey
			}
			img.SetNRGBA(x, y, color.NRGBA{R: c[0], G: c[1], B: c[2], A: 255})
		}
	}
	return img
}

func lerp(a, b [3]uint8, t float64) [3]uint8 {
	return [3]uint8{
		uint8(math.Round(float64(a[0])*(1-t) + float64(b[0])*t)),
		uint8(math.Round(float64(a[1])*(1-t) + float64(b[1])*t)),
		uint8(math.Round(float64(a[2])*(1-t) + float64(b[2])*t)),
	}
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
