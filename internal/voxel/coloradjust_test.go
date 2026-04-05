package voxel

import (
	"testing"
)

func TestAdjustColorIdentity(t *testing.T) {
	adj := ColorAdjustment{0, 0, 0}
	r, g, b := AdjustColor(128, 128, 128, adj)
	if r != 128 || g != 128 || b != 128 {
		t.Errorf("identity: got (%d,%d,%d), want (128,128,128)", r, g, b)
	}

	r, g, b = AdjustColor(0, 127, 255, adj)
	if r != 0 || g != 127 || b != 255 {
		t.Errorf("identity colors: got (%d,%d,%d), want (0,127,255)", r, g, b)
	}
}

func TestAdjustColorBrightness(t *testing.T) {
	// Max brightness: everything should clamp to 255.
	r, g, b := AdjustColor(0, 0, 0, ColorAdjustment{Brightness: 100})
	if r != 255 || g != 255 || b != 255 {
		t.Errorf("brightness +100 on black: got (%d,%d,%d), want (255,255,255)", r, g, b)
	}

	// Min brightness: everything should clamp to 0.
	r, g, b = AdjustColor(255, 255, 255, ColorAdjustment{Brightness: -100})
	if r != 0 || g != 0 || b != 0 {
		t.Errorf("brightness -100 on white: got (%d,%d,%d), want (0,0,0)", r, g, b)
	}
}

func TestAdjustColorContrast(t *testing.T) {
	// Contrast -100: multiplier = 0, everything collapses to mid-gray.
	r, g, b := AdjustColor(0, 128, 255, ColorAdjustment{Contrast: -100})
	if r != 128 || g != 128 || b != 128 {
		t.Errorf("contrast -100: got (%d,%d,%d), want (128,128,128)", r, g, b)
	}

	// Mid-gray at max contrast: 128/255 ≈ 0.502, so (0.502-0.5)*2+0.5 ≈ 0.504 → 129.
	// This is expected 8-bit rounding behavior.
	r, g, b = AdjustColor(128, 128, 128, ColorAdjustment{Contrast: 100})
	if r != 129 || g != 129 || b != 129 {
		t.Errorf("contrast +100 on mid-gray: got (%d,%d,%d), want (129,129,129)", r, g, b)
	}
}

func TestAdjustColorSaturation(t *testing.T) {
	// Saturation -100: output should be grayscale (all channels equal).
	r, g, b := AdjustColor(255, 0, 0, ColorAdjustment{Saturation: -100})
	if r != g || g != b {
		t.Errorf("saturation -100 on red: got (%d,%d,%d), expected grayscale", r, g, b)
	}

	// Pure gray should be unchanged at any saturation.
	r, g, b = AdjustColor(128, 128, 128, ColorAdjustment{Saturation: 100})
	if r != 128 || g != 128 || b != 128 {
		t.Errorf("saturation +100 on gray: got (%d,%d,%d), want (128,128,128)", r, g, b)
	}
}

func TestAdjustColorClamping(t *testing.T) {
	// Extreme settings shouldn't overflow or underflow.
	r, g, b := AdjustColor(255, 255, 255, ColorAdjustment{Brightness: 100, Contrast: 100, Saturation: 100})
	if r > 255 || g > 255 || b > 255 {
		t.Errorf("overflow: got (%d,%d,%d)", r, g, b)
	}

	r, g, b = AdjustColor(0, 0, 0, ColorAdjustment{Brightness: -100, Contrast: -100, Saturation: -100})
	// All extreme negatives should clamp to valid range.
	if r > 255 || g > 255 || b > 255 {
		t.Errorf("underflow: got (%d,%d,%d)", r, g, b)
	}
}
