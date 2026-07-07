package percep

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// solidRGBA builds a w×h opaque image filled with one sRGB color.
func solidRGBA(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// TestSelfDEIsZero: an image compared with itself is perceptually
// identical (ΔE == 0), before and after a blur.
func TestSelfDEIsZero(t *testing.T) {
	im := FromRGBA(solidRGBA(16, 16, color.RGBA{123, 45, 200, 255}))
	mean, p99, n := MeanLabDE(im, im)
	if n != 16*16 {
		t.Fatalf("n = %d, want %d", n, 16*16)
	}
	if mean != 0 || p99 != 0 {
		t.Fatalf("self ΔE = (%.6f, %.6f), want 0", mean, p99)
	}
	b := im.Blur(3)
	mean, _, _ = MeanLabDE(b, b)
	if mean != 0 {
		t.Fatalf("blurred self ΔE = %.6f, want 0", mean)
	}
}

// TestBlurUniformIsIdentity: blurring a uniform fully-present image
// leaves every present pixel unchanged (mask normalization makes the
// weights sum to the same constant everywhere).
func TestBlurUniformIsIdentity(t *testing.T) {
	orig := FromRGBA(solidRGBA(20, 20, color.RGBA{200, 100, 50, 255}))
	blurred := orig.Blur(4)
	for i := range orig.Pix {
		if !orig.Mask[i] || !blurred.Mask[i] {
			t.Fatalf("pixel %d lost its mask", i)
		}
		for k := 0; k < 3; k++ {
			if math.Abs(orig.Pix[i][k]-blurred.Pix[i][k]) > 1e-12 {
				t.Fatalf("pixel %d chan %d changed: %.12f -> %.12f",
					i, k, orig.Pix[i][k], blurred.Pix[i][k])
			}
		}
	}
}

// TestMaskEdgeNoDarkening: a fully-present half next to an absent half
// must not darken along the mask boundary — mask-normalized blur
// renormalizes over present pixels only, so the uniform present region
// stays exactly its input value even at the edge.
func TestMaskEdgeNoDarkening(t *testing.T) {
	w, h := 20, 20
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < w/2 {
				img.SetRGBA(x, y, color.RGBA{180, 180, 180, 255})
			}
			// right half left transparent (A=0)
		}
	}
	orig := FromRGBA(img)
	blurred := orig.Blur(3)
	for y := 0; y < h; y++ {
		for x := 0; x < w/2; x++ {
			i := y*w + x
			if !blurred.Mask[i] {
				t.Fatalf("present pixel (%d,%d) lost mask", x, y)
			}
			for k := 0; k < 3; k++ {
				if math.Abs(blurred.Pix[i][k]-orig.Pix[i][k]) > 1e-12 {
					t.Fatalf("edge darkened at (%d,%d) chan %d: %.6f -> %.6f",
						x, y, k, orig.Pix[i][k], blurred.Pix[i][k])
				}
			}
		}
		// absent half must remain absent
		for x := w / 2; x < w; x++ {
			if blurred.Mask[y*w+x] {
				t.Fatalf("absent pixel (%d,%d) gained mask", x, y)
			}
		}
	}
}

// TestGrayVsWhiteDEOrder: solid mid-gray vs solid white must give a
// positive ΔE, and a lighter gray must be closer to white than a
// darker one (monotone ordering sanity check).
func TestGrayVsWhiteDEOrder(t *testing.T) {
	white := FromRGBA(solidRGBA(8, 8, color.RGBA{255, 255, 255, 255}))
	lightGray := FromRGBA(solidRGBA(8, 8, color.RGBA{200, 200, 200, 255}))
	darkGray := FromRGBA(solidRGBA(8, 8, color.RGBA{100, 100, 100, 255}))

	deLight, _, _ := MeanLabDE(white, lightGray)
	deDark, _, _ := MeanLabDE(white, darkGray)
	if deLight <= 0 {
		t.Fatalf("white vs light-gray ΔE = %.3f, want > 0", deLight)
	}
	if !(deLight < deDark) {
		t.Fatalf("expected light-gray closer to white than dark-gray: light=%.3f dark=%.3f",
			deLight, deDark)
	}
}
