package materialx

import (
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"strings"
	"sync"
)

// AddressMode controls how out-of-[0,1] UV coordinates are wrapped
// before sampling. Mirrors MaterialX's uaddressmode/vaddressmode
// inputs on the image node.
type AddressMode int

const (
	AddressPeriodic AddressMode = iota
	AddressClamp
	AddressMirror
)

func parseAddressMode(s string) AddressMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "clamp":
		return AddressClamp
	case "mirror":
		return AddressMirror
	}
	return AddressPeriodic
}

// FilterType selects the texel-resampling kernel.
type FilterType int

const (
	FilterLinear FilterType = iota
	FilterClosest
)

func parseFilterType(s string) FilterType {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "closest", "nearest":
		return FilterClosest
	}
	return FilterLinear
}

// decodedImage is a CPU-resident texture decoded once at sampler
// construction time. Pixels are stored row-major as 4-channel RGBA8
// regardless of the source format. Sample is reentrant — the struct
// is immutable after construction.
type decodedImage struct {
	w, h   int
	pixels []uint8
	// srgb is true when the source declared colorspace="srgb_texture";
	// for ditherforge's downstream sRGB-quantized output we keep the
	// values as 8-bit sRGB (no linearization), matching how flat
	// FaceBaseColor is treated elsewhere in the pipeline.
	srgb bool
}

func decodeImage(r io.Reader, srgb bool) (*decodedImage, error) {
	img, _, err := image.Decode(r)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	pixels := make([]uint8, 4*w*h)
	idx := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			pixels[idx+0] = uint8(r >> 8)
			pixels[idx+1] = uint8(g >> 8)
			pixels[idx+2] = uint8(bl >> 8)
			pixels[idx+3] = uint8(a >> 8)
			idx += 4
		}
	}
	return &decodedImage{w: w, h: h, pixels: pixels, srgb: srgb}, nil
}

// sample looks up an RGB triplet at the given UV with the requested
// address/filter modes. Output is in [0, 1] per channel; alpha is
// ignored (ditherforge bakes alpha separately from base color).
func (img *decodedImage) sample(uv [2]float64, uMode, vMode AddressMode, filter FilterType) [3]float64 {
	u := wrapUV(uv[0], uMode)
	v := wrapUV(uv[1], vMode)
	// MaterialX texture origin is bottom-left; image package origin
	// is top-left. Flip V so loaded textures match what reference
	// renderers produce.
	v = 1 - v

	x := u * float64(img.w)
	y := v * float64(img.h)
	if filter == FilterClosest {
		ix := wrapPixel(int(math.Floor(x)), img.w, uMode)
		iy := wrapPixel(int(math.Floor(y)), img.h, vMode)
		return img.fetch(ix, iy)
	}
	// Bilinear: shift by -0.5 so integer pixel coords sit at texel centers.
	x -= 0.5
	y -= 0.5
	x0 := int(math.Floor(x))
	y0 := int(math.Floor(y))
	fx := x - float64(x0)
	fy := y - float64(y0)
	x1 := x0 + 1
	y1 := y0 + 1
	x0 = wrapPixel(x0, img.w, uMode)
	x1 = wrapPixel(x1, img.w, uMode)
	y0 = wrapPixel(y0, img.h, vMode)
	y1 = wrapPixel(y1, img.h, vMode)
	c00 := img.fetch(x0, y0)
	c10 := img.fetch(x1, y0)
	c01 := img.fetch(x0, y1)
	c11 := img.fetch(x1, y1)
	var out [3]float64
	for i := range 3 {
		a := c00[i]*(1-fx) + c10[i]*fx
		b := c01[i]*(1-fx) + c11[i]*fx
		out[i] = a*(1-fy) + b*fy
	}
	return out
}

// fetch returns the un-converted RGB at integer pixel (x, y) in [0, 1].
// Caller must have already wrapped (x, y) into the image bounds.
func (img *decodedImage) fetch(x, y int) [3]float64 {
	off := 4 * (y*img.w + x)
	return [3]float64{
		float64(img.pixels[off+0]) / 255,
		float64(img.pixels[off+1]) / 255,
		float64(img.pixels[off+2]) / 255,
	}
}

func wrapUV(t float64, mode AddressMode) float64 {
	switch mode {
	case AddressPeriodic:
		t = t - math.Floor(t)
	case AddressClamp:
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
	case AddressMirror:
		t = math.Mod(math.Abs(t), 2.0)
		if t > 1 {
			t = 2 - t
		}
	}
	return t
}

func wrapPixel(i, n int, mode AddressMode) int {
	switch mode {
	case AddressClamp:
		if i < 0 {
			return 0
		}
		if i >= n {
			return n - 1
		}
		return i
	case AddressMirror:
		// Reflect at boundaries, period 2*n.
		period := 2 * n
		i = ((i % period) + period) % period
		if i >= n {
			i = period - 1 - i
		}
		return i
	}
	// Periodic.
	i = i % n
	if i < 0 {
		i += n
	}
	return i
}

// imageCache deduplicates image loads across multiple references in a
// single graph (e.g. a base_color + roughness shader pack referencing
// the same .png from two image nodes). Used during sampler compile.
type imageCache struct {
	mu     sync.Mutex
	images map[string]*decodedImage
}

func newImageCache() *imageCache {
	return &imageCache{images: map[string]*decodedImage{}}
}

func (c *imageCache) load(r ResourceResolver, relpath, colorspace string) (*decodedImage, error) {
	if r == nil {
		return nil, fmt.Errorf("no resource resolver — load .mtlx via ParsePackage or ParseFile to enable image nodes")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if img, ok := c.images[relpath]; ok {
		return img, nil
	}
	rc, err := r.Open(relpath)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	img, err := decodeImage(rc, strings.EqualFold(colorspace, "srgb_texture"))
	if err != nil {
		return nil, err
	}
	c.images[relpath] = img
	return img, nil
}
