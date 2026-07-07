package voxel

import "math"

// EffectivePalette applies the "layered" filament-translucency model: it
// returns, per palette entry, the color a viewer actually perceives once the
// finite translucent shell is accounted for, so the dither can quantize and
// diffuse against those effective colors with plain area weighting (rather
// than the area-renormalized opacity mix of PaletteAlphas / AlphaFromTD).
//
// The model is the N-crossing Beer–Lambert recursion terminated at the infill
// color, per the "Corrected model" section of docs/td-translucency-model.md.
// For a palette entry k with transmission distance TD_k (mm):
//
//   - Per-crossing path length ℓ = h·√2, where h is the layer height (mm) and
//     √2 = 1/cos(45°) is a representative viewing angle (straight-on is the
//     easy case, grazing is hopeless; 45° is the defensible statistical pick).
//   - Per-crossing transmission T_k = 10^(−ℓ/TD_k) (Beer–Lambert).
//   - Number of dither-cell crossings N = s/h (shell thickness / layer
//     height), clamped to [1,64] as a float — not rounded — so N doesn't step
//     discontinuously as the shell/layer ratio slides.
//   - Infill leak L_k = T_k^N: the fraction of light that survives the whole
//     shell and returns tinted by the infill filament.
//   - Effective color in linear-light RGB:
//     C_eff,k = (1−L_k)·lin(C_k) + L_k·lin(I),
//     converted back to sRGB bytes.
//
// Identity guarantees (exact, byte-level) — a palette of opaque filaments is
// transformed to itself so the common path stays bit-identical to the historical
// dither:
//
//   - A garbage TD (≤ 0, NaN, or ±Inf) is treated as fully opaque and the
//     entry's original bytes are returned unchanged. This mirrors the
//     sanitization contract of AlphaFromTD: a hand-authored --inventory file
//     can carry garbage that must not poison the color model.
//   - Entries whose TD is present but so opaque that L_k < 1/1024 also return
//     the original bytes unchanged, so a purely-opaque palette is exactly
//     identity (not merely round-trip-equal through the sRGB conversion).
//   - A missing TD (len(tds) < len(pal)) is opaque → identity.
//   - Degenerate geometry falls back to defaults: h ≤ 0 or NaN → 0.2 mm;
//     s ≤ 0 or NaN → 0.84 mm.
//
// pal is never mutated; a new slice is returned (identity entries reuse the
// original [3]uint8 values).
func EffectivePalette(pal [][3]uint8, tds []float32, layerHeightMM, shellThicknessMM float32, infill [3]uint8) [][3]uint8 {
	out := make([][3]uint8, len(pal))

	// Geometry with defensive fallbacks. !(x > 0) catches both NaN and ≤ 0.
	h := float64(layerHeightMM)
	if !(h > 0) {
		h = 0.2
	}
	s := float64(shellThicknessMM)
	if !(s > 0) {
		s = 0.84
	}

	ell := h * math.Sqrt2

	// N = shell / layer, clamped to [1,64] (float, not rounded).
	n := s / h
	if n < 1 {
		n = 1
	} else if n > 64 {
		n = 64
	}

	linI := [3]float64{
		float64(srgbToLinearLUT[infill[0]]),
		float64(srgbToLinearLUT[infill[1]]),
		float64(srgbToLinearLUT[infill[2]]),
	}

	for i, c := range pal {
		// Default: identity — reuse the original bytes.
		out[i] = c

		var td float64
		if i < len(tds) {
			td = float64(tds[i])
		}
		// !(td > 0) catches NaN and ≤ 0; IsInf catches ±Inf. Any → opaque.
		if !(td > 0) || math.IsInf(td, 0) {
			continue
		}

		// Per-crossing transmission and the fraction that leaks all the way
		// to the infill after N crossings.
		t := math.Pow(10, -ell/td)
		leak := math.Pow(t, n)
		if leak < 1.0/1024.0 {
			// Effectively opaque: keep the original bytes bit-identical.
			continue
		}

		for ch := 0; ch < 3; ch++ {
			linC := float64(srgbToLinearLUT[c[ch]])
			eff := (1-leak)*linC + leak*linI[ch]
			out[i][ch] = linearToSrgbByte(float32(eff))
		}
	}
	return out
}
