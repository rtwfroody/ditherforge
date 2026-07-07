// Package percep provides a small perceptual-image comparison layer
// used by the dither research tools (tests/ditherbench and
// tests/ditherrender).
//
// Rationale: a dithered field is a spatial pattern of hard palette
// tiles. Whether it "looks like" the continuous-color original is not
// a per-tile question — the eye integrates adjacent tiles as photons
// (linear light) at some angular scale, and only the integrated result
// is what a viewer perceives. So the correct perceptual comparison is:
//
//  1. work in LINEAR light (the space photons add in),
//  2. blur both the dithered field and the reference at the
//     integration scale (a Gaussian whose sigma is that scale),
//  3. measure color difference in CIE Lab (perceptually near-uniform).
//
// The blur is mask-normalized so the transparent background outside a
// silhouette does not bleed darkness into the edges. The resulting mean
// Lab ΔE is the number dither modes should be judged by — it subsumes
// drift (a constant offset survives any blur) and banding (structure at
// or above sigma survives the blur), and it lives in a space with no
// byte-averaging Jensen gap (unlike a naive mean of sRGB bytes).
package percep

import (
	"image"
	"image/color"
	"math"
	"sort"

	colorful "github.com/lucasb-eyer/go-colorful"
)

// Image is a rectangular field of LINEAR RGB in [0,1] with a presence
// mask. Pix and Mask are row-major and both len == W*H. A pixel with
// Mask[i] == false is "absent" (transparent background) and is excluded
// from blur weighting and ΔE comparison.
type Image struct {
	W, H int
	Pix  [][3]float64 // linear RGB, row-major
	Mask []bool       // present/opaque flag, row-major
}

// FromRGBA builds a linear-light Image from an sRGB RGBA image.
// Pixels with alpha == 0 are marked absent; opaque pixels' sRGB bytes
// are converted to linear light via the standard IEC 61966-2-1 EOTF.
func FromRGBA(img *image.RGBA) *Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	im := &Image{
		W:    w,
		H:    h,
		Pix:  make([][3]float64, w*h),
		Mask: make([]bool, w*h),
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := img.RGBAAt(b.Min.X+x, b.Min.Y+y)
			i := y*w + x
			if c.A == 0 {
				continue
			}
			im.Mask[i] = true
			im.Pix[i] = [3]float64{
				srgb8ToLinear(c.R),
				srgb8ToLinear(c.G),
				srgb8ToLinear(c.B),
			}
		}
	}
	return im
}

// ToRGBA converts the linear-light Image back to an sRGB RGBA image.
// Absent pixels are left fully transparent. Used to dump blurred
// renders for visual inspection.
func (im *Image) ToRGBA() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, im.W, im.H))
	for i := 0; i < im.W*im.H; i++ {
		if !im.Mask[i] {
			continue
		}
		p := im.Pix[i]
		img.SetRGBA(i%im.W, i/im.W, color.RGBA{
			R: linearToSrgbByte(p[0]),
			G: linearToSrgbByte(p[1]),
			B: linearToSrgbByte(p[2]),
			A: 255,
		})
	}
	return img
}

// Blur applies a separable Gaussian of standard deviation sigmaPx
// (kernel radius ceil(3σ)) in linear light. It is mask-normalized:
// each output pixel's weights are renormalized over only the present
// input pixels within the kernel, so the transparent background never
// darkens silhouette edges. The output mask equals the input mask (an
// absent pixel stays absent; a present pixel with no present neighbors
// keeps its own value). σ <= 0 returns a copy.
func (im *Image) Blur(sigmaPx float64) *Image {
	if sigmaPx <= 0 {
		return im.clone()
	}
	radius := int(math.Ceil(3 * sigmaPx))
	kernel := make([]float64, radius+1)
	inv2s2 := 1.0 / (2 * sigmaPx * sigmaPx)
	for d := 0; d <= radius; d++ {
		kernel[d] = math.Exp(-float64(d*d) * inv2s2)
	}
	// Horizontal pass, then vertical pass.
	tmp := im.blur1D(kernel, radius, true)
	return tmp.blur1D(kernel, radius, false)
}

// blur1D runs a single mask-normalized 1D Gaussian pass. When
// horizontal is true it convolves along X, otherwise along Y.
func (im *Image) blur1D(kernel []float64, radius int, horizontal bool) *Image {
	out := &Image{
		W:    im.W,
		H:    im.H,
		Pix:  make([][3]float64, im.W*im.H),
		Mask: make([]bool, im.W*im.H),
	}
	copy(out.Mask, im.Mask)
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			i := y*im.W + x
			if !im.Mask[i] {
				continue
			}
			var sum [3]float64
			var wsum float64
			for d := -radius; d <= radius; d++ {
				var sx, sy int
				if horizontal {
					sx, sy = x+d, y
				} else {
					sx, sy = x, y+d
				}
				if sx < 0 || sx >= im.W || sy < 0 || sy >= im.H {
					continue
				}
				j := sy*im.W + sx
				if !im.Mask[j] {
					continue
				}
				w := kernel[abs(d)]
				sum[0] += w * im.Pix[j][0]
				sum[1] += w * im.Pix[j][1]
				sum[2] += w * im.Pix[j][2]
				wsum += w
			}
			if wsum > 0 {
				out.Pix[i] = [3]float64{sum[0] / wsum, sum[1] / wsum, sum[2] / wsum}
			}
		}
	}
	return out
}

func (im *Image) clone() *Image {
	out := &Image{
		W:    im.W,
		H:    im.H,
		Pix:  make([][3]float64, len(im.Pix)),
		Mask: make([]bool, len(im.Mask)),
	}
	copy(out.Pix, im.Pix)
	copy(out.Mask, im.Mask)
	return out
}

// MeanLabDE compares two images over the pixels present in BOTH. Each
// linear RGB pixel is converted to sRGB then CIE Lab (D65), and the
// Euclidean ΔE(ab) is taken. Returns the mean ΔE, the 99th-percentile
// ΔE, and the number of compared pixels. The two images must share the
// same dimensions.
func MeanLabDE(a, b *Image) (mean, p99 float64, n int) {
	if a.W != b.W || a.H != b.H {
		return 0, 0, 0
	}
	des := make([]float64, 0, len(a.Mask))
	var sum float64
	for i := range a.Mask {
		if !a.Mask[i] || !b.Mask[i] {
			continue
		}
		aL, aA, aB := linToLab(a.Pix[i])
		bL, bA, bB := linToLab(b.Pix[i])
		de := math.Sqrt((aL-bL)*(aL-bL) + (aA-bA)*(aA-bA) + (aB-bB)*(aB-bB))
		des = append(des, de)
		sum += de
	}
	n = len(des)
	if n == 0 {
		return 0, 0, 0
	}
	mean = sum / float64(n)
	sort.Float64s(des)
	p99 = percentile(des, 0.99)
	return mean, p99, n
}

// percentile returns the q-quantile (q in [0,1]) of an already-sorted
// slice via nearest-rank.
func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(q*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// linToLab converts a linear RGB pixel to CIE Lab on the same scale
// ditherbench uses (L*100, a*100, b*100). It mirrors
// toLab(linearToSrgb255(...)) — go-colorful takes sRGB in [0,1], so we
// encode linear -> sRGB first.
func linToLab(p [3]float64) (float64, float64, float64) {
	c := colorful.Color{
		R: linearToSrgb01(p[0]),
		G: linearToSrgb01(p[1]),
		B: linearToSrgb01(p[2]),
	}
	L, A, B := c.Lab()
	return L * 100, A * 100, B * 100
}

// srgb8ToLinear converts an sRGB byte (0-255) to linear light in [0,1]
// using the standard IEC 61966-2-1 transfer function. Matches
// ditherbench's copy of the same formula.
func srgb8ToLinear(c uint8) float64 {
	x := float64(c) / 255
	if x <= 0.04045 {
		return x / 12.92
	}
	return math.Pow((x+0.055)/1.055, 2.4)
}

// linearToSrgb01 is the inverse of srgb8ToLinear, returning an sRGB
// value in [0,1].
func linearToSrgb01(x float64) float64 {
	if x <= 0.0031308 {
		return 12.92 * x
	}
	return 1.055*math.Pow(x, 1/2.4) - 0.055
}

// linearToSrgbByte encodes linear light to a clamped sRGB byte.
func linearToSrgbByte(x float64) uint8 {
	s := linearToSrgb01(x) * 255
	if s < 0 {
		s = 0
	}
	if s > 255 {
		s = 255
	}
	return uint8(math.Round(s))
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
