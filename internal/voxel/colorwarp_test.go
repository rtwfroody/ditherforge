package voxel

import (
	"math"
	"testing"
)

func TestRgbLabRoundTrip(t *testing.T) {
	colors := [][3]uint8{
		{0, 0, 0},
		{255, 255, 255},
		{255, 0, 0},
		{0, 255, 0},
		{0, 0, 255},
		{128, 64, 200},
	}
	for _, c := range colors {
		L, a, b := rgbToLab(c)
		got := labToRGB(L, a, b)
		for ch := range 3 {
			diff := int(got[ch]) - int(c[ch])
			if diff < -1 || diff > 1 {
				t.Errorf("round trip %v: got %v (diff %d in channel %d)", c, got, diff, ch)
			}
		}
	}
}

// warpTo applies a warp-only ColorTransform (no brightness/contrast/
// saturation) to a single color via the live ColorTransform path used by
// the pipeline's sampler.
func warpTo(t *testing.T, pins []ColorWarpPin, in [3]uint8) [3]uint8 {
	t.Helper()
	ct, err := NewColorTransform(ColorAdjustment{}, pins)
	if err != nil {
		t.Fatal(err)
	}
	return ct.Apply(in)
}

func TestColorTransformIdentity(t *testing.T) {
	ct, err := NewColorTransform(ColorAdjustment{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !ct.IsIdentity() {
		t.Error("no adjustment and no pins should be identity")
	}
	in := [3]uint8{100, 150, 200}
	if got := ct.Apply(in); got != in {
		t.Errorf("identity transform changed color: %v -> %v", in, got)
	}
}

func TestWarpSinglePin(t *testing.T) {
	// A single pin: red → blue. A red color should become blue.
	red := [3]uint8{255, 0, 0}
	blue := [3]uint8{0, 0, 255}
	got := warpTo(t, []ColorWarpPin{{Source: red, Target: blue}}, red)
	if dist := colorDist(got, blue); dist > 5 {
		t.Errorf("red→blue warp: expected close to %v, got %v (dist %.1f)", blue, got, dist)
	}
}

func TestWarpDistantColorUnchanged(t *testing.T) {
	// Pin red → blue. A distant color (green) should be mostly unchanged.
	red := [3]uint8{255, 0, 0}
	blue := [3]uint8{0, 0, 255}
	green := [3]uint8{0, 255, 0}
	// Use a tight sigma so the effect is very local.
	got := warpTo(t, []ColorWarpPin{{Source: red, Target: blue, Sigma: 10}}, green)
	if dist := colorDist(got, green); dist > 10 {
		t.Errorf("distant color should be mostly unchanged: expected ~%v, got %v (dist %.1f)", green, got, dist)
	}
}

func TestWarpTwoPins(t *testing.T) {
	// Two pins: red → blue and green → yellow. Both should land on their targets.
	red := [3]uint8{255, 0, 0}
	blue := [3]uint8{0, 0, 255}
	green := [3]uint8{0, 255, 0}
	yellow := [3]uint8{255, 255, 0}

	pins := []ColorWarpPin{
		{Source: red, Target: blue},
		{Source: green, Target: yellow},
	}

	if dist := colorDist(warpTo(t, pins, red), blue); dist > 5 {
		t.Errorf("red→blue: dist %.1f from target", dist)
	}
	if dist := colorDist(warpTo(t, pins, green), yellow); dist > 5 {
		t.Errorf("green→yellow: dist %.1f from target", dist)
	}
}

func TestWarpHeterogeneousSigmas(t *testing.T) {
	// Two pins with different sigmas. Pin 0 (tight) should have local effect;
	// pin 1 (wide) should affect a broader range.
	red := [3]uint8{255, 0, 0}
	blue := [3]uint8{0, 0, 255}
	green := [3]uint8{0, 200, 0}
	yellow := [3]uint8{255, 255, 0}
	// A color between red and green.
	olive := [3]uint8{128, 128, 0}

	pins := []ColorWarpPin{
		{Source: red, Target: blue, Sigma: 5},      // very tight
		{Source: green, Target: yellow, Sigma: 80}, // very wide
	}

	// Both pin sources should still land on their targets.
	if dist := colorDist(warpTo(t, pins, red), blue); dist > 5 {
		t.Errorf("red→blue: dist %.1f from target", dist)
	}
	if dist := colorDist(warpTo(t, pins, green), yellow); dist > 5 {
		t.Errorf("green→yellow: dist %.1f from target", dist)
	}

	// Olive is between red and green. The wide green pin (sigma=80) should
	// affect it more than the tight red pin (sigma=5). So olive should shift
	// at least slightly toward yellow. With Wendland C2, the effect is small
	// (olive is at r≈0.79 of green's radius) but nonzero.
	if dist := colorDist(warpTo(t, pins, olive), olive); dist < 1 {
		t.Errorf("olive should be shifted by wide green pin, but dist from original is only %.1f", dist)
	}
}

func TestColorTransformAdjustBeforeWarp(t *testing.T) {
	// The adjustment must run before the warp lookup. Brightness +100 turns
	// black into white; a white→red pin then fires. If the warp ran first
	// (on black, far from the white source) the result would be a brightened
	// black ≈ white, not red — so landing on red proves the ordering.
	black := [3]uint8{0, 0, 0}
	white := [3]uint8{255, 255, 255}
	red := [3]uint8{255, 0, 0}
	ct, err := NewColorTransform(ColorAdjustment{Brightness: 100}, []ColorWarpPin{{Source: white, Target: red}})
	if err != nil {
		t.Fatal(err)
	}
	if got := ct.Apply(black); colorDist(got, red) > 8 {
		t.Errorf("adjust-before-warp: black+bright→white→red expected ~%v, got %v (dist %.1f)", red, got, colorDist(got, red))
	}
}

func TestGaussElimIdentity(t *testing.T) {
	// 3x3 identity matrix.
	A := [][]float64{
		{1, 0, 0},
		{0, 1, 0},
		{0, 0, 1},
	}
	b := []float64{3, 5, 7}
	x, err := gaussElim(A, b)
	if err != nil {
		t.Fatal(err)
	}
	for i := range b {
		if math.Abs(x[i]-b[i]) > 1e-10 {
			t.Errorf("x[%d] = %f, want %f", i, x[i], b[i])
		}
	}
}

func TestGaussElimSingular(t *testing.T) {
	A := [][]float64{
		{1, 2},
		{2, 4}, // linearly dependent
	}
	b := []float64{1, 2}
	_, err := gaussElim(A, b)
	if err == nil {
		t.Error("expected singular matrix error")
	}
}

// colorDist returns Euclidean distance in RGB space.
func colorDist(a, b [3]uint8) float64 {
	dr := float64(a[0]) - float64(b[0])
	dg := float64(a[1]) - float64(b[1])
	db := float64(a[2]) - float64(b[2])
	return math.Sqrt(dr*dr + dg*dg + db*db)
}
