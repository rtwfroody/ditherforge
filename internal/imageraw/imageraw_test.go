package imageraw

import (
	"bytes"
	"encoding/gob"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// TestNRGBAFastPathPreservesPixels: an NRGBA image at origin (0,0) is the
// zero-copy fast path. Round-trip should preserve pixels exactly.
func TestNRGBAFastPathPreservesPixels(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	src.Set(0, 0, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
	src.Set(3, 3, color.NRGBA{R: 0, G: 255, B: 0, A: 128})
	rt := FromImage(src)
	dst := ToImage(rt)
	if dst == nil {
		t.Fatal("ToImage returned nil")
	}
	r, g, b, a := dst.At(0, 0).RGBA()
	if r>>8 != 200 || g>>8 != 100 || b>>8 != 50 || a>>8 != 255 {
		t.Errorf("(0,0): got r=%d g=%d b=%d a=%d", r>>8, g>>8, b>>8, a>>8)
	}
}

// TestYCbCrConversionPath: a JPEG-decoded *image.YCbCr triggers the
// conversion branch (NewNRGBA + draw.Copy). Pixels should round-trip
// within JPEG quantization tolerance.
func TestYCbCrConversionPath(t *testing.T) {
	// Build a recognizable source, encode to JPEG, decode back so we get
	// a real *image.YCbCr (not just a converted NRGBA).
	src := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			src.Set(x, y, color.NRGBA{R: uint8(x * 16), G: uint8(y * 16), B: 100, A: 255})
		}
	}
	var jpgBuf bytes.Buffer
	if err := jpeg.Encode(&jpgBuf, src, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(&jpgBuf)
	if err != nil {
		t.Fatal(err)
	}
	if _, isYCbCr := decoded.(*image.YCbCr); !isYCbCr {
		t.Fatalf("expected *image.YCbCr after JPEG decode, got %T", decoded)
	}

	rt := FromImage(decoded)
	if rt.W != 16 || rt.H != 16 {
		t.Errorf("size lost: w=%d h=%d", rt.W, rt.H)
	}
	dst := ToImage(rt)
	if dst == nil {
		t.Fatal("ToImage returned nil")
	}
	if dst.Bounds() != image.Rect(0, 0, 16, 16) {
		t.Errorf("bounds: got %v, want 16x16", dst.Bounds())
	}
	// Spot-check a pixel survived round-trip via the source values
	// (within JPEG tolerance — quality 95 typically holds within ~5).
	r, g, _, _ := dst.At(8, 8).RGBA()
	if absDiff(int(r>>8), 8*16) > 8 || absDiff(int(g>>8), 8*16) > 8 {
		t.Errorf("(8,8): r=%d g=%d expected ~%d/~%d", r>>8, g>>8, 8*16, 8*16)
	}
}

// TestNilImageHandling: a nil image becomes a zero-value Tex; ToImage
// turns it back into nil. Round-trip through gob preserves this.
func TestNilImageHandling(t *testing.T) {
	rt := FromImage(nil)
	if rt.Pix != nil || rt.W != 0 || rt.H != 0 {
		t.Errorf("FromImage(nil) = %+v, want zero", rt)
	}

	// Round-trip through gob too.
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(rt); err != nil {
		t.Fatal(err)
	}
	var got Tex
	if err := gob.NewDecoder(&buf).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.W != 0 || got.H != 0 || got.Pix != nil {
		t.Errorf("gob round-trip of zero Tex: got %+v", got)
	}
	if ToImage(got) != nil {
		t.Error("ToImage of zero Tex should be nil")
	}
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
