package percep

import (
	"math"
	"testing"
)

// uniformImage builds a fully-present W×H image with every pixel set to the
// same linear-RGB color.
func uniformImage(w, h int, c [3]float64) *Image {
	im := &Image{W: w, H: h, Pix: make([][3]float64, w*h), Mask: make([]bool, w*h)}
	for i := range im.Pix {
		im.Pix[i] = c
		im.Mask[i] = true
	}
	return im
}

// checkerImage builds a fully-present W×H image alternating c0/c1 with the
// given period (in pixels) along both axes.
func checkerImage(w, h, period int, c0, c1 [3]float64) *Image {
	im := &Image{W: w, H: h, Pix: make([][3]float64, w*h), Mask: make([]bool, w*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if ((x/period)+(y/period))%2 == 0 {
				im.Pix[i] = c0
			} else {
				im.Pix[i] = c1
			}
			im.Mask[i] = true
		}
	}
	return im
}

func maxChannelDiff(a, b *Image) float64 {
	var m float64
	for i := range a.Pix {
		for c := 0; c < 3; c++ {
			d := math.Abs(a.Pix[i][c] - b.Pix[i][c])
			if d > m {
				m = d
			}
		}
	}
	return m
}

// TestCSFBandWeightsMonotonic checks that for a geometry whose band
// frequencies all sit at or above the CSF peak, weights are in (0,1] and
// non-decreasing with band index (band 0 is the FINEST/highest-frequency
// band, so it gets the most attenuation).
func TestCSFBandWeightsMonotonic(t *testing.T) {
	// Small pixel pitch + long viewing distance pushes every band to high
	// cycles/degree, so the CSF roll-off applies across the whole stack.
	const mmPerPx, viewMM = 0.5, 3000.0
	freqs, weights := CSFBandWeights(mmPerPx, viewMM)
	if len(freqs) != len(weights) || len(weights) == 0 {
		t.Fatalf("bad lengths: freqs=%d weights=%d", len(freqs), len(weights))
	}
	for k, w := range weights {
		if w <= 0 || w > 1 {
			t.Errorf("weight[%d] = %g out of (0,1]", k, w)
		}
	}
	for k := 1; k < len(weights); k++ {
		if weights[k] < weights[k-1]-1e-12 {
			t.Errorf("weights not non-decreasing with band index: weights[%d]=%g < weights[%d]=%g",
				k, weights[k], k-1, weights[k-1])
		}
	}
	// Finest band must be attenuated more than the coarsest.
	if weights[0] >= weights[len(weights)-1] {
		t.Errorf("expected weights[0] (%g) < weights[last] (%g)", weights[0], weights[len(weights)-1])
	}
	// Frequencies fall from finest to coarsest band.
	for k := 1; k < len(freqs); k++ {
		if freqs[k] >= freqs[k-1] {
			t.Errorf("freqs not decreasing with band index: freqs[%d]=%g >= freqs[%d]=%g",
				k, freqs[k], k-1, freqs[k-1])
		}
	}
}

// TestCSFFilterUniformUnchanged verifies a constant-color, full-mask image
// passes through CSFFilter unchanged (all detail bands are ≈ 0).
func TestCSFFilterUniformUnchanged(t *testing.T) {
	img := uniformImage(64, 48, [3]float64{0.3, 0.6, 0.2})
	out := CSFFilter(img, 0.5, 2000)
	if d := maxChannelDiff(img, out); d > 1e-9 {
		t.Errorf("uniform image changed by CSFFilter: max channel diff %g", d)
	}
}

// TestVisibleDEDistanceFalloff verifies that a fine checkerboard's visible
// difference from its flat mean shrinks as the viewer moves farther away, and
// that at a long distance it is well below the raw (unfiltered) ΔE.
func TestVisibleDEDistanceFalloff(t *testing.T) {
	c0 := [3]float64{0.05, 0.05, 0.05}
	c1 := [3]float64{0.8, 0.8, 0.8}
	mean := [3]float64{(c0[0] + c1[0]) / 2, (c0[1] + c1[1]) / 2, (c0[2] + c1[2]) / 2}
	check := checkerImage(64, 64, 1, c0, c1) // period 1 → 2px spatial period
	flat := uniformImage(64, 64, mean)

	const mmPerPx = 0.5
	near, _, _ := VisibleDE(check, flat, mmPerPx, 300)
	far, _, _ := VisibleDE(check, flat, mmPerPx, 3000)
	if !(far < near) {
		t.Errorf("expected visible ΔE to shrink with distance: near(300mm)=%g far(3000mm)=%g", near, far)
	}
	raw, _, _ := MeanLabDE(check, flat)
	if !(far < raw*0.5) {
		t.Errorf("expected far visible ΔE (%g) well below raw ΔE (%g)", far, raw)
	}
}

// TestVisibleDEIdentityZero verifies two identical images have zero visible
// difference.
func TestVisibleDEIdentityZero(t *testing.T) {
	img := checkerImage(32, 32, 2, [3]float64{0.1, 0.2, 0.3}, [3]float64{0.7, 0.6, 0.5})
	mean, _, _ := VisibleDE(img, img.clone(), 0.5, 2000)
	if mean != 0 {
		t.Errorf("identical images gave nonzero visible ΔE mean = %g", mean)
	}
}

// TestVisibleDEMaskAware verifies that masked-out pixels don't contribute:
// with a stripe of rows absent in both images, VisibleDE returns finite sane
// values and n equals the number of pixels present in both.
func TestVisibleDEMaskAware(t *testing.T) {
	c0 := [3]float64{0.05, 0.05, 0.05}
	c1 := [3]float64{0.8, 0.8, 0.8}
	mean := [3]float64{(c0[0] + c1[0]) / 2, (c0[1] + c1[1]) / 2, (c0[2] + c1[2]) / 2}
	w, h := 48, 48
	check := checkerImage(w, h, 1, c0, c1)
	flat := uniformImage(w, h, mean)
	// Mask out a horizontal stripe in both images.
	present := 0
	for y := 0; y < h; y++ {
		absent := y >= 20 && y < 28
		for x := 0; x < w; x++ {
			i := y*w + x
			if absent {
				check.Mask[i] = false
				flat.Mask[i] = false
			} else {
				present++
			}
		}
	}
	m, p99, n := VisibleDE(check, flat, 0.5, 2000)
	if n != present {
		t.Errorf("expected n=%d present-in-both pixels, got %d", present, n)
	}
	if math.IsNaN(m) || math.IsInf(m, 0) || m < 0 {
		t.Errorf("non-finite/negative mean visible ΔE: %g", m)
	}
	if math.IsNaN(p99) || math.IsInf(p99, 0) || p99 < 0 {
		t.Errorf("non-finite/negative p99 visible ΔE: %g", p99)
	}
}
