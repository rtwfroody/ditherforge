package ditherpreview

import (
	"context"
	"image"
	"image/color"
	"slices"
	"testing"
)

// testPalette is a small fixed palette spanning enough of the cube to force
// visible dithering on the mid-tone test image below.
var testPalette = [][3]uint8{
	{16, 16, 16},    // near-black
	{240, 240, 240}, // near-white
	{220, 80, 60},   // red
	{60, 120, 210},  // blue
}

// buildTestImage makes a deterministic 16x12 gradient-over-flat image so the
// dither has something non-trivial to work with.
func buildTestImage() *image.NRGBA {
	const w, h = 16, 12
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			var c color.NRGBA
			if y < h/2 {
				t := float64(x) / float64(w-1)
				c = color.NRGBA{
					R: uint8(220*(1-t) + 60*t),
					G: uint8(80*(1-t) + 120*t),
					B: uint8(60*(1-t) + 210*t),
					A: 255,
				}
			} else {
				c = color.NRGBA{R: 128, G: 128, B: 128, A: 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// TestDitherImageAllModes exercises every GUI mode: each must produce an opaque
// image the same size as the input, painted only with palette colours.
func TestDitherImageAllModes(t *testing.T) {
	src := buildTestImage()
	inPalette := func(p [3]uint8) bool {
		return slices.Contains(testPalette, p)
	}
	for _, mode := range Modes {
		t.Run(mode, func(t *testing.T) {
			out, err := DitherImage(context.Background(), src, testPalette, mode, DefaultTuning())
			if err != nil {
				t.Fatalf("DitherImage(%s): %v", mode, err)
			}
			if out.Bounds() != image.Rect(0, 0, 16, 12) {
				t.Fatalf("mode %s: got bounds %v, want 16x12", mode, out.Bounds())
			}
			for y := range 12 {
				for x := range 16 {
					px := out.NRGBAAt(x, y)
					if px.A != 255 {
						t.Fatalf("mode %s: pixel (%d,%d) alpha=%d, want opaque", mode, x, y, px.A)
					}
					if !inPalette([3]uint8{px.R, px.G, px.B}) {
						t.Fatalf("mode %s: pixel (%d,%d) = %v not in palette", mode, x, y, px)
					}
				}
			}
		})
	}
}

// TestDitherImageDeterministic verifies a fixed (image, palette, mode, tuning)
// yields byte-identical pixels across runs for every mode — the property the
// static-thumbnail generator and cache-free previews both rely on.
func TestDitherImageDeterministic(t *testing.T) {
	src := buildTestImage()
	for _, mode := range Modes {
		a, err := DitherImage(context.Background(), src, testPalette, mode, DefaultTuning())
		if err != nil {
			t.Fatalf("mode %s run 1: %v", mode, err)
		}
		b, err := DitherImage(context.Background(), src, testPalette, mode, DefaultTuning())
		if err != nil {
			t.Fatalf("mode %s run 2: %v", mode, err)
		}
		if len(a.Pix) != len(b.Pix) {
			t.Fatalf("mode %s: pixel buffer length mismatch", mode)
		}
		for i := range a.Pix {
			if a.Pix[i] != b.Pix[i] {
				t.Fatalf("mode %s: non-deterministic at byte %d (%d != %d)", mode, i, a.Pix[i], b.Pix[i])
			}
		}
	}
}

// TestDitherImageUnknownMode rejects an unrecognised mode string.
func TestDitherImageUnknownMode(t *testing.T) {
	src := buildTestImage()
	if _, err := DitherImage(context.Background(), src, testPalette, "bogus", DefaultTuning()); err == nil {
		t.Fatal("expected error for unknown mode, got nil")
	}
}
