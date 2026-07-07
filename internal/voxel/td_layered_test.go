package voxel

import (
	"math"
	"testing"
)

// srgbToLinearLUT / linearToSrgbByte are exercised indirectly; these tests
// only compare bytes and coarse color-channel trends.

const (
	tdTestLayerH = float32(0.2)
	tdTestShell  = float32(0.84)
)

var (
	tdWhite = [3]uint8{255, 255, 255}
	tdRed   = [3]uint8{200, 40, 40}
)

// TestEffectivePaletteGarbageTDIdentity: a TD of 0, negative, NaN, or ±Inf is
// treated as fully opaque, so the entry's bytes come back untouched.
func TestEffectivePaletteGarbageTDIdentity(t *testing.T) {
	pal := [][3]uint8{tdRed, tdRed, tdRed, tdRed, tdRed}
	tds := []float32{
		0,
		-1,
		float32(math.NaN()),
		float32(math.Inf(1)),
		float32(math.Inf(-1)),
	}
	out := EffectivePalette(pal, tds, tdTestLayerH, tdTestShell, tdWhite)
	for i := range pal {
		if out[i] != pal[i] {
			t.Errorf("garbage TD %v: entry %d = %v, want identity %v", tds[i], i, out[i], pal[i])
		}
	}
}

// TestEffectivePaletteOpaqueIdentity: a genuinely opaque filament leaks a
// negligible amount of infill (L < 1/1024), so the short-circuit returns the
// entry's bytes exactly. TD=0.3 mm at h=0.2, s=0.84 gives L≈1e-4.
//
// NB: the spec's suggested "0.6 mm → ≤1 byte-step" example does NOT hold under
// this model — 0.6 mm leaks ~1% infill (≈10-byte shift on dark channels),
// consistent with the design doc's own figure that an "opaque" TD=0.8 filament
// still leaks ~4%. Only sub-~0.4 mm TDs trip the identity short-circuit here.
func TestEffectivePaletteOpaqueIdentity(t *testing.T) {
	pal := [][3]uint8{tdRed}
	tds := []float32{0.3}
	out := EffectivePalette(pal, tds, tdTestLayerH, tdTestShell, tdWhite)
	if out[0] != pal[0] {
		t.Errorf("opaque TD=0.3: got %v, want byte-identical %v", out[0], pal[0])
	}
}

// TestEffectivePaletteTranslucentTowardInfill: a translucent red over white
// infill gets lighter (higher luminance) and less saturated (channels pulled
// together toward white).
func TestEffectivePaletteTranslucentTowardInfill(t *testing.T) {
	pal := [][3]uint8{tdRed}
	tds := []float32{6}
	out := EffectivePalette(pal, tds, tdTestLayerH, tdTestShell, tdWhite)
	got := out[0]

	if got == tdRed {
		t.Fatalf("translucent TD=6 should transform, got identity %v", got)
	}
	// Every channel moves toward white (255): each ≥ the original.
	for ch := 0; ch < 3; ch++ {
		if got[ch] < tdRed[ch] {
			t.Errorf("ch %d moved away from white infill: %d < %d", ch, got[ch], tdRed[ch])
		}
	}
	// Less saturated: the green/blue channels rise more than red (which is
	// already near-white in the source), so the max-min spread shrinks.
	spreadIn := int(tdRed[0]) - int(tdRed[1])
	spreadOut := int(got[0]) - int(got[1])
	if spreadOut >= spreadIn {
		t.Errorf("saturation did not drop: spread out %d >= in %d", spreadOut, spreadIn)
	}
}

// TestEffectivePaletteMonotonic: larger TD shifts further toward infill, and a
// thicker shell shifts less.
func TestEffectivePaletteMonotonic(t *testing.T) {
	// Distance from red toward white infill along the red (0) channel.
	shift := func(td, shell float32) int {
		out := EffectivePalette([][3]uint8{tdRed}, []float32{td}, tdTestLayerH, shell, tdWhite)
		return int(out[0][1]) - int(tdRed[1]) // green channel rises toward 255
	}

	s4 := shift(4, tdTestShell)
	s6 := shift(6, tdTestShell)
	s10 := shift(10, tdTestShell)
	if !(s4 < s6 && s6 < s10) {
		t.Errorf("shift not monotonic in TD: TD4=%d TD6=%d TD10=%d", s4, s6, s10)
	}

	thin := shift(6, 0.4)
	thick := shift(6, 1.6)
	if !(thick < thin) {
		t.Errorf("thicker shell should shift less: thin=%d thick=%d", thin, thick)
	}
}

// TestEffectivePaletteUniformStillTransforms: unlike PaletteAlphas (which
// returns nil for a uniform-TD palette because a uniform opacity cancels in
// the renormalized mix), a uniformly translucent palette really does wash
// toward the infill and must transform.
func TestEffectivePaletteUniformStillTransforms(t *testing.T) {
	pal := [][3]uint8{tdRed, {40, 200, 40}, {40, 40, 200}}
	tds := []float32{6, 6, 6}
	out := EffectivePalette(pal, tds, tdTestLayerH, tdTestShell, tdWhite)
	for i := range pal {
		if out[i] == pal[i] {
			t.Errorf("uniform translucent entry %d unchanged: %v", i, out[i])
		}
	}
}

// TestEffectivePaletteNoMutation: the input palette slice is not modified.
func TestEffectivePaletteNoMutation(t *testing.T) {
	pal := [][3]uint8{tdRed, {40, 200, 40}}
	orig := [][3]uint8{pal[0], pal[1]}
	_ = EffectivePalette(pal, []float32{6, 6}, tdTestLayerH, tdTestShell, tdWhite)
	for i := range pal {
		if pal[i] != orig[i] {
			t.Errorf("input mutated at %d: %v != %v", i, pal[i], orig[i])
		}
	}
}

// TestEffectivePaletteMissingTDOpaque: fewer TDs than palette entries — the
// missing entries are opaque (identity).
func TestEffectivePaletteMissingTDOpaque(t *testing.T) {
	pal := [][3]uint8{tdRed, tdRed}
	out := EffectivePalette(pal, []float32{6}, tdTestLayerH, tdTestShell, tdWhite)
	if out[0] == pal[0] {
		t.Errorf("entry 0 (TD=6) should transform")
	}
	if out[1] != pal[1] {
		t.Errorf("entry 1 (no TD) should be identity, got %v", out[1])
	}
}
