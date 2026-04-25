package voxel

import (
	"image"
	"image/color"
	"math/rand"
	"testing"
)

// genericImage wraps an image.Image so a type switch on the wrapped value
// falls through to the generic path. Used to verify the fast paths match
// the generic path byte-for-byte.
type genericImage struct {
	inner image.Image
}

func (g genericImage) ColorModel() color.Model { return g.inner.ColorModel() }
func (g genericImage) Bounds() image.Rectangle { return g.inner.Bounds() }
func (g genericImage) At(x, y int) color.Color { return g.inner.At(x, y) }

// TestBilinearSampleFastPathMatchesGeneric verifies the NRGBA and RGBA
// fast paths produce identical output to the generic img.At() path.
func TestBilinearSampleFastPathMatchesGeneric(t *testing.T) {
	nrgba := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	rgba := image.NewRGBA(image.Rect(0, 0, 8, 8))
	r := rand.New(rand.NewSource(42))
	for i := 0; i < len(nrgba.Pix); i++ {
		nrgba.Pix[i] = uint8(r.Intn(256))
		rgba.Pix[i] = uint8(r.Intn(256))
	}

	uvs := [][2]float32{
		{0, 0}, {0.5, 0.5}, {0.999, 0.001}, {0.1, 0.9},
		{0.333, 0.666}, {1.0, 1.0}, {0.25, 0.75},
	}

	for _, uv := range uvs {
		u, v := uv[0], uv[1]
		fast := BilinearSample(nrgba, u, v)
		slow := BilinearSample(genericImage{nrgba}, u, v)
		if fast != slow {
			t.Errorf("NRGBA u=%v v=%v: fast=%v slow=%v", u, v, fast, slow)
		}
		fastR := BilinearSample(rgba, u, v)
		slowR := BilinearSample(genericImage{rgba}, u, v)
		if fastR != slowR {
			t.Errorf("RGBA u=%v v=%v: fast=%v slow=%v", u, v, fastR, slowR)
		}
	}
}

// TestBilinearSampleNonZeroOriginBounds verifies that images whose Rect.Min
// is not (0,0) are sampled correctly. image.SubImage produces such images.
func TestBilinearSampleNonZeroOriginBounds(t *testing.T) {
	full := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	r := rand.New(rand.NewSource(7))
	for i := range full.Pix {
		full.Pix[i] = uint8(r.Intn(256))
	}
	sub := full.SubImage(image.Rect(4, 4, 12, 12)).(*image.NRGBA)

	uvs := [][2]float32{{0, 0}, {0.5, 0.5}, {0.7, 0.3}, {0.999, 0.999}}
	for _, uv := range uvs {
		fast := BilinearSample(sub, uv[0], uv[1])
		slow := BilinearSample(genericImage{sub}, uv[0], uv[1])
		if fast != slow {
			t.Errorf("SubImage u=%v v=%v: fast=%v slow=%v", uv[0], uv[1], fast, slow)
		}
	}
}

// TestBilinearSampleKnownPixelNRGBA verifies that uv=(0,0) returns the
// top-left pixel's premultiplied value. This catches a class of bugs where
// fast and generic paths both read a wrong-but-consistent offset.
func TestBilinearSampleKnownPixelNRGBA(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{R: 200, G: 100, B: 50, A: 128})
	img.SetNRGBA(1, 0, color.NRGBA{R: 0, G: 0, B: 0, A: 255})
	img.SetNRGBA(0, 1, color.NRGBA{R: 0, G: 0, B: 0, A: 255})
	img.SetNRGBA(1, 1, color.NRGBA{R: 0, G: 0, B: 0, A: 255})

	got := BilinearSample(img, 0, 0)
	// Expected: same math as color.NRGBA.RGBA() — (R * 0x101 * A / 0xff) >> 8.
	expR := uint8((uint32(200) * 0x101 * 128 / 0xff) >> 8)
	expG := uint8((uint32(100) * 0x101 * 128 / 0xff) >> 8)
	expB := uint8((uint32(50) * 0x101 * 128 / 0xff) >> 8)
	want := [4]uint8{expR, expG, expB, 128}
	if got != want {
		t.Errorf("at (0,0): got %v want %v", got, want)
	}
}

// TestBilinearSampleKnownPixelSubImage samples a SubImage at uv=(0,0) and
// verifies the result is the pixel at the sub-image's top-left corner in
// the *parent's* coordinate system.
func TestBilinearSampleKnownPixelSubImage(t *testing.T) {
	full := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	// Paint a unique marker at parent (3,3); all others opaque black.
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			full.SetNRGBA(x, y, color.NRGBA{R: 0, G: 0, B: 0, A: 255})
		}
	}
	full.SetNRGBA(3, 3, color.NRGBA{R: 222, G: 111, B: 33, A: 255})
	sub := full.SubImage(image.Rect(3, 3, 7, 7)).(*image.NRGBA)

	got := BilinearSample(sub, 0, 0)
	want := [4]uint8{222, 111, 33, 255}
	if got != want {
		t.Errorf("SubImage at (0,0): got %v want %v (should be parent(3,3))", got, want)
	}
}

// TestBilinearSampleAlphaBoundaries exercises alpha=0 (transparent) and
// alpha=255 (opaque) for NRGBA, which gate the two rounding regimes of the
// premultiply formula.
func TestBilinearSampleAlphaBoundaries(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, G: 128, B: 64, A: 0})
	img.SetNRGBA(1, 0, color.NRGBA{R: 255, G: 128, B: 64, A: 255})
	img.SetNRGBA(0, 1, color.NRGBA{R: 255, G: 128, B: 64, A: 0})
	img.SetNRGBA(1, 1, color.NRGBA{R: 255, G: 128, B: 64, A: 255})

	if got := BilinearSample(img, 0, 0); got != [4]uint8{0, 0, 0, 0} {
		t.Errorf("alpha=0: got %v want [0 0 0 0]", got)
	}
	if got := BilinearSample(img, 0.999, 0); got != [4]uint8{255, 128, 64, 255} {
		t.Errorf("alpha=255: got %v want [255 128 64 255]", got)
	}
}

// TestBilinearSampleRGBASubImage covers the RGBA fast path with a
// non-(0,0) origin, ensuring Pix indexing is correct for pre-premultiplied
// sub-images too.
func TestBilinearSampleRGBASubImage(t *testing.T) {
	full := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			full.SetRGBA(x, y, color.RGBA{R: 0, G: 0, B: 0, A: 255})
		}
	}
	full.SetRGBA(5, 2, color.RGBA{R: 77, G: 88, B: 99, A: 255})
	sub := full.SubImage(image.Rect(5, 2, 8, 8)).(*image.RGBA)

	got := BilinearSample(sub, 0, 0)
	want := [4]uint8{77, 88, 99, 255}
	if got != want {
		t.Errorf("RGBA SubImage at (0,0): got %v want %v", got, want)
	}
}

// TestBilinearSampleTinyImages covers the corner-clamp path for 1x1 and
// thin images where the x1/y1 = x0+1 branch always clamps.
func TestBilinearSampleTinyImages(t *testing.T) {
	one := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	one.SetNRGBA(0, 0, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
	for _, uv := range [][2]float32{{0, 0}, {0.5, 0.5}, {0.999, 0.999}} {
		got := BilinearSample(one, uv[0], uv[1])
		if got != [4]uint8{200, 100, 50, 255} {
			t.Errorf("1x1 at uv=%v: got %v want [200 100 50 255]", uv, got)
		}
	}

	row := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	row.SetNRGBA(0, 0, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
	row.SetNRGBA(3, 0, color.NRGBA{R: 0, G: 0, B: 255, A: 255})
	// Cross-path equality suffices here — the known-pixel cases above already
	// pin down the absolute semantics.
	for _, uv := range [][2]float32{{0, 0.5}, {0.5, 0.5}, {0.999, 0.5}} {
		fast := BilinearSample(row, uv[0], uv[1])
		slow := BilinearSample(genericImage{row}, uv[0], uv[1])
		if fast != slow {
			t.Errorf("4x1 at uv=%v: fast=%v slow=%v", uv, fast, slow)
		}
	}
}

func makeBenchNRGBA(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	r := rand.New(rand.NewSource(1))
	for i := range img.Pix {
		img.Pix[i] = uint8(r.Intn(256))
	}
	return img
}

func BenchmarkBilinearSample(b *testing.B) {
	img := makeBenchNRGBA(256)
	b.Run("FastPath_NRGBA", func(b *testing.B) {
		var sink [4]uint8
		u := float32(0.0)
		for i := 0; i < b.N; i++ {
			sink = BilinearSample(img, u, 1-u)
			u += 0.001
			if u > 1 {
				u -= 1
			}
		}
		_ = sink
	})
	b.Run("Generic_WrappedNRGBA", func(b *testing.B) {
		wrapped := genericImage{img}
		var sink [4]uint8
		u := float32(0.0)
		for i := 0; i < b.N; i++ {
			sink = BilinearSample(wrapped, u, 1-u)
			u += 0.001
			if u > 1 {
				u -= 1
			}
		}
		_ = sink
	})
}
