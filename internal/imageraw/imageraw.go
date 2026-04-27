// Package imageraw is a tiny helper for serializing image.Image as raw
// NRGBA pixel buffers via gob. The diskcache layer applies zstd over the
// whole gob payload, so storing pixels uncompressed here avoids a
// redundant compression pass (PNG bytes don't compress further with zstd)
// and is much faster to encode/decode.
package imageraw

import (
	"image"

	"golang.org/x/image/draw"
)

// Tex is the on-disk shape: NRGBA pixels with their bounds. gob can
// serialize this directly. The exported field names matter because gob
// encodes them.
type Tex struct {
	W, H   int
	Stride int
	Pix    []uint8
}

// FromImage converts an arbitrary image.Image to a Tex. Most loader-decoded
// images are *image.NRGBA at origin (0,0) — that path is zero-copy. JPEGs
// (*image.YCbCr) and other types are converted via draw.Copy.
func FromImage(img image.Image) Tex {
	if img == nil {
		return Tex{}
	}
	b := img.Bounds()
	if nr, ok := img.(*image.NRGBA); ok && nr.Rect.Min == (image.Point{}) {
		return Tex{W: nr.Rect.Dx(), H: nr.Rect.Dy(), Stride: nr.Stride, Pix: nr.Pix}
	}
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Copy(dst, image.Point{}, img, b, draw.Src, nil)
	return Tex{W: dst.Rect.Dx(), H: dst.Rect.Dy(), Stride: dst.Stride, Pix: dst.Pix}
}

// ToImage reconstructs an *image.NRGBA from a Tex. Returns nil for the
// zero value (used to represent absent textures).
func ToImage(t Tex) image.Image {
	if t.W == 0 || t.H == 0 || len(t.Pix) == 0 {
		return nil
	}
	return &image.NRGBA{Pix: t.Pix, Stride: t.Stride, Rect: image.Rect(0, 0, t.W, t.H)}
}
