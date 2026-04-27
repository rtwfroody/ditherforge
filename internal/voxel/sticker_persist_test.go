package voxel

import (
	"bytes"
	"encoding/gob"
	"image"
	"image/color"
	"testing"
)

// TestStickerDecalGobRoundTrip: round-tripping a decal preserves the image
// pixels (via PNG), the per-triangle UVs, and the LSCM residual.
func TestStickerDecalGobRoundTrip(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	img.Set(2, 1, color.NRGBA{R: 200, G: 50, B: 100, A: 255})
	src := &StickerDecal{
		Image: img,
		TriUVs: map[int32][3][2]float32{
			7:  {{0.1, 0.2}, {0.3, 0.4}, {0.5, 0.6}},
			42: {{0.0, 0.0}, {1.0, 0.0}, {0.5, 1.0}},
		},
		LSCMResidual: 1.5e-8,
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(src); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var dst StickerDecal
	if err := gob.NewDecoder(&buf).Decode(&dst); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dst.LSCMResidual != src.LSCMResidual {
		t.Errorf("LSCMResidual: got %v, want %v", dst.LSCMResidual, src.LSCMResidual)
	}
	if len(dst.TriUVs) != 2 {
		t.Fatalf("TriUVs len = %d, want 2", len(dst.TriUVs))
	}
	if dst.TriUVs[42][2][1] != 1.0 {
		t.Errorf("TriUVs[42][2][1] = %v, want 1.0", dst.TriUVs[42][2][1])
	}
	if dst.Image == nil {
		t.Fatal("Image was lost")
	}
	r, g, b, _ := dst.Image.At(2, 1).RGBA()
	if r>>8 != 200 || g>>8 != 50 || b>>8 != 100 {
		t.Errorf("pixel (2,1) lost: r=%d g=%d b=%d", r>>8, g>>8, b>>8)
	}
}

// TestStickerDecalGobNilImage: a decal with no image (e.g. degenerate
// projection result) survives gob round-trip.
func TestStickerDecalGobNilImage(t *testing.T) {
	src := &StickerDecal{
		TriUVs: map[int32][3][2]float32{1: {{0, 0}, {1, 0}, {0, 1}}},
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(src); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var dst StickerDecal
	if err := gob.NewDecoder(&buf).Decode(&dst); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dst.Image != nil {
		t.Error("nil Image was not preserved as nil")
	}
}
