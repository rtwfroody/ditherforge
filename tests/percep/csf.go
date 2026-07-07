package percep

import (
	"math"
	"sync"
)

// This file adds a CSF-weighted "visible difference" metric on top of the
// plain blur-ΔE in percep.go.
//
// A single fixed-σ blur answers "does the dither match at THIS integration
// scale?" — but a real viewer integrates every scale at once, and the human
// contrast-sensitivity function (CSF) tells us how much each spatial
// frequency actually contributes to the percept. CSFFilter decomposes an
// image into difference-of-Gaussian frequency bands, attenuates each band by
// the CSF value at its physical center frequency (cycles/degree for a viewer
// at a given distance), and reconstructs. VisibleDE then measures the Lab ΔE
// between two images filtered identically this way: a single "total visible
// difference" number that rolls the whole σ sweep into one CSF-weighted score.
//
// The CSF here is the Mannos–Sakrison model. Its low-frequency roll-off is
// left FLAT (weight 1 from DC up to the sensitivity peak): that roll-off
// models luminance adaptation, not an inability to see large-area color
// error, so a uniform drift must keep full weight or the metric would forgive
// exactly the errors a dither is supposed to avoid.

// csfSigmas are the difference-of-Gaussian band cutoffs in pixels. Band k is
// (blur at σ_{k-1}) − (blur at σ_k), i.e. the detail lost between successive
// blur scales; band 0 uses the original image as its finer input. The final
// residual (blur at the coarsest σ) carries the DC / lowest frequencies.
var csfSigmas = []float64{0.5, 1, 2, 4, 8, 16}

// mannosSakrison is the Mannos–Sakrison contrast-sensitivity function at
// spatial frequency f in cycles per degree.
func mannosSakrison(f float64) float64 {
	return 2.6 * (0.0192 + 0.114*f) * math.Exp(-math.Pow(0.114*f, 1.1))
}

var (
	csfPeakOnce sync.Once
	csfFPeak    float64 // frequency (cpd) at which mannosSakrison peaks
	csfAPeak    float64 // mannosSakrison(csfFPeak)
)

// csfPeak returns the CSF peak frequency and its sensitivity, found once by a
// fine scan and cached (the peak is geometry-independent).
func csfPeak() (fPeak, aPeak float64) {
	csfPeakOnce.Do(func() {
		best := math.Inf(-1)
		bestF := 0.5
		for f := 0.5; f <= 30.0+1e-9; f += 0.01 {
			a := mannosSakrison(f)
			if a > best {
				best = a
				bestF = f
			}
		}
		csfFPeak = bestF
		csfAPeak = best
	})
	return csfFPeak, csfAPeak
}

// csfBandGeometry computes, for the given viewing geometry, each band's center
// frequency in cycles/degree and its CSF weight. It is the single source of
// truth shared by CSFFilter and CSFBandWeights so the two can't drift apart.
//
// mmPerPx converts pixels to millimeters on the surface; viewDistMM is the
// eye-to-surface distance. A band's center frequency (cycles/pixel) is the
// half-power cutoff of the pair of Gaussians that bracket it, 0.187/σ_geom,
// with σ_geom the geometric mean of the two bracketing σ. That is converted
// to cycles/degree via the pixel pitch and viewing distance.
func csfBandGeometry(mmPerPx, viewDistMM float64) (freqs, weights []float64) {
	fPeak, aPeak := csfPeak()
	freqs = make([]float64, len(csfSigmas))
	weights = make([]float64, len(csfSigmas))
	// Millimeters subtended by one degree of visual angle at this distance.
	mmPerDeg := viewDistMM * math.Pi / 180.0
	for k, s := range csfSigmas {
		prev := s / 2
		if k > 0 {
			prev = csfSigmas[k-1]
		}
		sigG := math.Sqrt(s * prev)
		u := 0.187 / sigG // cycles per pixel
		var f float64
		if mmPerPx > 0 {
			f = u / mmPerPx * mmPerDeg // cycles per degree
		}
		freqs[k] = f
		// Full weight up to the sensitivity peak (adaptation region), then
		// roll off with the CSF above it.
		w := 1.0
		if f > fPeak {
			w = mannosSakrison(f) / aPeak
		}
		weights[k] = w
	}
	return freqs, weights
}

// CSFBandWeights returns (centerFreqCPD, weight) per band for the given
// geometry — for diagnostics output.
func CSFBandWeights(mmPerPx, viewDistMM float64) (freqs, weights []float64) {
	return csfBandGeometry(mmPerPx, viewDistMM)
}

// CSFFilter returns a copy of img with each frequency band attenuated by the
// human contrast-sensitivity function at that band's physical frequency, for a
// viewer at viewDistMM. mmPerPx converts pixels to mm.
//
// The filter works in linear light and is mask-aware: absent pixels are left
// untouched (and zero), and every band is computed from the mask-normalized
// Blur so the transparent background never bleeds into silhouette edges. With
// all band weights equal to 1 the reconstruction telescopes back to the exact
// input, so a uniform field passes through unchanged.
func CSFFilter(img *Image, mmPerPx, viewDistMM float64) *Image {
	if mmPerPx <= 0 {
		// Degenerate geometry: no meaningful frequency mapping, so return the
		// image unfiltered rather than dividing by zero.
		return img.clone()
	}
	_, weights := csfBandGeometry(mmPerPx, viewDistMM)
	n := len(csfSigmas)
	lows := make([]*Image, n)
	for k, s := range csfSigmas {
		lows[k] = img.Blur(s)
	}
	out := &Image{
		W:    img.W,
		H:    img.H,
		Pix:  make([][3]float64, len(img.Pix)),
		Mask: make([]bool, len(img.Mask)),
	}
	copy(out.Mask, img.Mask)
	for i := range img.Pix {
		if !img.Mask[i] {
			continue
		}
		// Start from the coarsest residual (DC through the lowest band), then
		// add each weighted detail band back on top.
		acc := lows[n-1].Pix[i]
		for k := 0; k < n; k++ {
			var prevLow [3]float64
			if k == 0 {
				prevLow = img.Pix[i]
			} else {
				prevLow = lows[k-1].Pix[i]
			}
			w := weights[k]
			for c := 0; c < 3; c++ {
				band := prevLow[c] - lows[k].Pix[i][c]
				acc[c] += w * band
			}
		}
		// Band weighting can push a channel slightly out of range; clamp.
		for c := 0; c < 3; c++ {
			if acc[c] < 0 {
				acc[c] = 0
			} else if acc[c] > 1 {
				acc[c] = 1
			}
		}
		out.Pix[i] = acc
	}
	return out
}

// VisibleDE CSF-filters both images identically and returns their mean/p99 Lab
// ΔE — a "total visible difference" in ΔE units.
func VisibleDE(a, b *Image, mmPerPx, viewDistMM float64) (mean, p99 float64, n int) {
	fa := CSFFilter(a, mmPerPx, viewDistMM)
	fb := CSFFilter(b, mmPerPx, viewDistMM)
	return MeanLabDE(fa, fb)
}
