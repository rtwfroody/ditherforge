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

// TestDitherImageColorSnap verifies the ColorSnap tuning reaches the real
// voxel.SnapColors transform: pulling cell colours toward the palette before
// dithering changes an error-diffusion mode's output, while the default (snap
// 0) matches an explicit zero. Guards the wiring the live preview depends on.
func TestDitherImageColorSnap(t *testing.T) {
	src := buildTestImage()
	ctx := context.Background()

	base, err := DitherImage(ctx, src, testPalette, ModeFloydSteinberg, Tuning{ColorSnap: 0})
	if err != nil {
		t.Fatalf("snap 0: %v", err)
	}
	snapped, err := DitherImage(ctx, src, testPalette, ModeFloydSteinberg, Tuning{ColorSnap: 30})
	if err != nil {
		t.Fatalf("snap 30: %v", err)
	}
	if slices.Equal(base.Pix, snapped.Pix) {
		t.Fatal("color snap 30 produced identical output to snap 0; the ColorSnap knob is not wired through")
	}

	// DefaultTuning disables snap (ColorSnap 0), so the committed static
	// thumbnails stay bit-identical.
	def, err := DitherImage(ctx, src, testPalette, ModeFloydSteinberg, DefaultTuning())
	if err != nil {
		t.Fatalf("default tuning: %v", err)
	}
	if !slices.Equal(base.Pix, def.Pix) {
		t.Fatal("DefaultTuning must apply no color snap (ColorSnap 0)")
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

// TestDitherImageAlphaMask verifies alpha masking: transparent input pixels
// stay fully transparent in the output, while opaque input pixels get an
// opaque palette colour. The image is a transparent border around an opaque
// interior patch.
func TestDitherImageAlphaMask(t *testing.T) {
	const w, h = 16, 12
	const border = 3
	isOpaque := func(x, y int) bool {
		return x >= border && x < w-border && y >= border && y < h-border
	}

	src := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			if isOpaque(x, y) {
				// Mid grey, deliberately between palette entries.
				src.SetNRGBA(x, y, color.NRGBA{R: 130, G: 120, B: 110, A: 255})
			} else {
				src.SetNRGBA(x, y, color.NRGBA{}) // fully transparent
			}
		}
	}

	inPalette := func(p [3]uint8) bool { return slices.Contains(testPalette, p) }

	for _, mode := range Modes {
		t.Run(mode, func(t *testing.T) {
			out, err := DitherImage(context.Background(), src, testPalette, mode, DefaultTuning())
			if err != nil {
				t.Fatalf("DitherImage(%s): %v", mode, err)
			}
			for y := range h {
				for x := range w {
					px := out.NRGBAAt(x, y)
					if isOpaque(x, y) {
						if px.A != 255 {
							t.Fatalf("mode %s: opaque pixel (%d,%d) alpha=%d, want 255", mode, x, y, px.A)
						}
						if !inPalette([3]uint8{px.R, px.G, px.B}) {
							t.Fatalf("mode %s: opaque pixel (%d,%d) = %v not in palette", mode, x, y, px)
						}
					} else if px.A != 0 {
						t.Fatalf("mode %s: background pixel (%d,%d) alpha=%d, want 0 (transparent)", mode, x, y, px.A)
					}
				}
			}
		})
	}
}

// TestDitherImageFullyTransparent handles a snapshot with no opaque pixels:
// the result is a fully transparent image of the same size, not an error.
func TestDitherImageFullyTransparent(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 8, 6))
	for _, mode := range Modes {
		out, err := DitherImage(context.Background(), src, testPalette, mode, DefaultTuning())
		if err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		if out.Bounds() != image.Rect(0, 0, 8, 6) {
			t.Fatalf("mode %s: got bounds %v, want 8x6", mode, out.Bounds())
		}
		for _, v := range out.Pix {
			if v != 0 {
				t.Fatalf("mode %s: expected fully transparent output, found non-zero byte", mode)
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
