package voxel

import "github.com/rtwfroody/ditherforge/internal/palette"

// EffectivePalette applies the "layered" filament-translucency model: it
// returns, per palette entry, the color a viewer actually perceives once the
// finite translucent shell is accounted for, so the dither can quantize and
// diffuse against those effective colors with plain area weighting (rather
// than the area-renormalized opacity mix of PaletteAlphas / AlphaFromTD).
//
// The per-entry infill-leak fraction L_k is computed by palette.TDLeak (which
// package palette needs for TD-aware selection and which owns the model's
// definition and identity contract). For a palette entry k with transmission
// distance TD_k (mm), TDLeak returns the fraction of light that survives the
// whole shell; EffectivePalette then composites toward the infill:
//
//   - Effective color in linear-light RGB:
//     C_eff,k = (1−L_k)·lin(C_k) + L_k·lin(I),
//     converted back to sRGB bytes.
//
// Identity guarantees (exact, byte-level) — a palette of opaque filaments is
// transformed to itself so the common path stays bit-identical to the historical
// dither. TDLeak returns 0 (→ this function reuses the entry's original bytes,
// no float round-trip) whenever:
//
//   - The TD is garbage (≤ 0, NaN, or ±Inf) — a hand-authored --inventory file
//     can carry garbage that must not poison the color model.
//   - The TD is present but so opaque that L_k < 1/1024, so a purely-opaque
//     palette is exactly identity (not merely round-trip-equal through the
//     sRGB conversion).
//   - A missing TD (len(tds) < len(pal)) is read as 0 here, which TDLeak also
//     treats as opaque → identity.
//   - Degenerate geometry falls back inside TDLeak: h ≤ 0/NaN → 0.2 mm;
//     s ≤ 0/NaN → 0.84 mm.
//
// pal is never mutated; a new slice is returned (identity entries reuse the
// original [3]uint8 values).
func EffectivePalette(pal [][3]uint8, tds []float32, layerHeightMM, shellThicknessMM float32, infill [3]uint8) [][3]uint8 {
	out := make([][3]uint8, len(pal))

	layer := float64(layerHeightMM)
	shell := float64(shellThicknessMM)

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
		leak := palette.TDLeak(td, layer, shell)
		if leak == 0 {
			// Opaque (garbage/missing TD, or effectively opaque): keep the
			// original bytes bit-identical.
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
