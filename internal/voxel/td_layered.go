package voxel

import (
	"math"

	"github.com/rtwfroody/ditherforge/internal/palette"
)

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

// NeighborEffectivePalette returns, per palette entry, the print-simulation
// color under the neighbor translucency model, using the global area-weighted
// mean of the cells' target colors as the stand-in neighborhood every cell
// washes toward. It is the palette-level analogue of EffectiveCellColors (which
// blends each cell toward its actual neighbors) and of TD-aware palette
// selection (which composites each inventory entry toward the same target
// mean): all three share palette.NeighborLeak and an area-weighted target mean,
// so the GUI fallback swatches, the printSimAvailable signal, the per-cell sim,
// and the color selection stay consistent.
//
// An opaque or garbage-TD entry (β = 0) is returned byte-identical to its
// nominal color, so an all-opaque palette is exact identity. pal is never
// mutated; a new slice is returned.
func NeighborEffectivePalette(pal [][3]uint8, tds []float32, cells []ActiveCell, neighborPathMM float32) [][3]uint8 {
	out := make([][3]uint8, len(pal))

	// Area-weighted mean of the target (cell) colors, in linear light — the
	// same target mean TD-aware selection composites toward.
	const areaEps = 1e-6
	var mean [3]float64
	var wsum float64
	for _, c := range cells {
		w := math.Max(float64(c.Area), areaEps)
		mean[0] += w * float64(srgbToLinearLUT[c.Color[0]])
		mean[1] += w * float64(srgbToLinearLUT[c.Color[1]])
		mean[2] += w * float64(srgbToLinearLUT[c.Color[2]])
		wsum += w
	}
	if wsum > 0 {
		mean[0] /= wsum
		mean[1] /= wsum
		mean[2] /= wsum
	}

	for i, c := range pal {
		out[i] = c // default: identity (opaque/garbage TD)

		var td float64
		if i < len(tds) {
			td = float64(tds[i])
		}
		beta := palette.NeighborLeak(td, float64(neighborPathMM))
		if beta == 0 {
			continue
		}
		for ch := 0; ch < 3; ch++ {
			linC := float64(srgbToLinearLUT[c[ch]])
			eff := (1-beta)*linC + beta*mean[ch]
			out[i][ch] = linearToSrgbByte(float32(eff))
		}
	}
	return out
}

// EffectiveCellColors returns one perceived "print-simulation" color per
// visible cell. Where EffectivePalette composites every cell of a given
// filament toward the single infill color, this models the dominant effect
// physical test prints actually showed: a translucent cell picks up the
// colors of the cells touching it (above, below, beside), NOT the infill. A
// translucent orange speckle surrounded by grey/black reads as a dark,
// muddied orange rather than a burnt-orange-over-infill.
//
// For cell i assigned filament k = assignments[i], the lateral leak
//
//	β_i = 10^(−neighborPathMM / TD_k)
//
// is the fraction of a neighbor's color that shows through cell i's own
// filament over a representative in-plane path. β_i is 0 for an opaque or
// garbage-TD filament (β below 1/1024, TD ≤ 0, NaN, or ±Inf), so opaque cells
// come back byte-identical to their nominal color; it is clamped to [0, 0.95]
// so a cell never fully dissolves into its neighbors.
//
// The blend is a few Jacobi passes in linear-light RGB:
//
//	C_{t+1}(i) = (1−β_i)·C0(i) + β_i · (Σ_j w_ij·C_t(j)) / (Σ_j w_ij)
//
// where C0(i) is cell i's nominal filament color and w_ij = Neighbor.Weight ×
// max(area_j, ε). The self term always references C0 (not C_t(i)), keeping the
// cell's own filament dominant and the iteration gently convergent. A cell
// with no (valid) neighbors keeps C0, and an all-opaque palette returns the
// nominal colors unchanged.
//
// cells, assignments, neighbors and the returned slice are all parallel and
// indexed by visible-cell position. An out-of-range assignment yields a gray
// placeholder for that cell and is excluded as a blend source and target.
//
// Runs serially: two Jacobi passes over a few hundred thousand cells is cheap,
// and correctness is easier to reason about without a worker pool.
func EffectiveCellColors(cells []ActiveCell, assignments []int32, pal [][3]uint8, tds []float32, neighbors [][]Neighbor, neighborPathMM float32, iterations int) [][3]uint8 {
	n := len(cells)
	out := make([][3]uint8, n)
	if n == 0 {
		return out
	}

	c0 := make([][3]float64, n)      // nominal color, linear-light
	nominal := make([][3]uint8, n)   // nominal color bytes (identity fast paths)
	beta := make([]float64, n)       // lateral leak per cell
	valid := make([]bool, n)         // false = out-of-range assignment (gray)
	const grayByte uint8 = 128

	anyTranslucent := false
	for i := range cells {
		k := -1
		if i < len(assignments) {
			k = int(assignments[i])
		}
		if k < 0 || k >= len(pal) {
			nominal[i] = [3]uint8{grayByte, grayByte, grayByte}
			out[i] = nominal[i]
			continue
		}
		valid[i] = true
		c := pal[k]
		nominal[i] = c
		out[i] = c
		c0[i] = [3]float64{
			float64(srgbToLinearLUT[c[0]]),
			float64(srgbToLinearLUT[c[1]]),
			float64(srgbToLinearLUT[c[2]]),
		}
		var td float64
		if k < len(tds) {
			td = float64(tds[k])
		}
		// Lateral leak β; shared with TD-aware palette selection so the
		// simulation and the selection can't drift (see palette.NeighborLeak).
		b := palette.NeighborLeak(td, float64(neighborPathMM))
		beta[i] = b
		if b > 0 {
			anyTranslucent = true
		}
	}

	if !anyTranslucent {
		// Every cell opaque (or invalid): nominal colors, byte-identical.
		return out
	}

	const areaEps = 1e-6
	cur := make([][3]float64, n)
	copy(cur, c0)
	next := make([][3]float64, n)
	for iter := 0; iter < iterations; iter++ {
		for i := 0; i < n; i++ {
			if !valid[i] || beta[i] == 0 || i >= len(neighbors) || len(neighbors[i]) == 0 {
				next[i] = c0[i]
				continue
			}
			var sum [3]float64
			var wsum float64
			for _, nb := range neighbors[i] {
				j := nb.Idx
				if j < 0 || j >= n || !valid[j] {
					continue
				}
				w := float64(nb.Weight) * math.Max(float64(cells[j].Area), areaEps)
				wsum += w
				sum[0] += w * cur[j][0]
				sum[1] += w * cur[j][1]
				sum[2] += w * cur[j][2]
			}
			if wsum == 0 {
				next[i] = c0[i]
				continue
			}
			b := beta[i]
			for ch := 0; ch < 3; ch++ {
				next[i][ch] = (1-b)*c0[i][ch] + b*sum[ch]/wsum
			}
		}
		cur, next = next, cur
	}

	for i := 0; i < n; i++ {
		if !valid[i] || beta[i] == 0 {
			// Invalid → gray placeholder (already set); opaque → nominal
			// bytes, byte-identical to the plain dither.
			continue
		}
		out[i] = [3]uint8{
			linearToSrgbByte(float32(cur[i][0])),
			linearToSrgbByte(float32(cur[i][1])),
			linearToSrgbByte(float32(cur[i][2])),
		}
	}
	return out
}
