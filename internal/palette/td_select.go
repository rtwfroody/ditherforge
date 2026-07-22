package palette

import (
	"math"
	"os"
	"strconv"

	colorful "github.com/lucasb-eyer/go-colorful"
)

// nominalDupDeltaE is the perceptual ΔE (CIEDE2000) below which two palette
// entries' NOMINAL filament colors count as near-duplicates. The Panchroma
// Basic collection carries two near-identical whites (#D9DFE5/#EBF7FF, ΔE00
// 5.44) and two near-identical yellows (#FFE800/#EED230, ΔE00 5.65); picking
// both of a pair wastes a slot (one ends up at ~0% coverage after dithering —
// defect 1). 6 sits cleanly in the gap between those duplicate pairs (≤ 5.65)
// and the next, genuinely-distinct pair (Red/Wine Red at ΔE00 6.11), so it
// suppresses accidental duplicates without rejecting legitimately close-but-
// useful choices. CIE76 (plain Lab Euclidean) can't separate these — the whites
// are CIE76 8.3 apart, further than several distinct pairs — so the check uses
// CIEDE2000.
const nominalDupDeltaE = 6.0

// usageDeadFraction is the predicted-usage floor (fraction of area-weighted
// samples for which a member is the nearest achievable vertex) below which a
// free pick is considered dead weight and eligible for the usage safety net.
// A member the dither will barely place (the ~0.1% ColdWhite of defect 1) is
// not earning its slot.
const usageDeadFraction = 0.005

// usageSwapTolerance is how much worse (fractionally) an alternative may score
// and still be accepted by the usage safety net. The net only fires when the
// swap "barely changes the score" (per the design): a dead member is replaced
// by a genuinely-used alternative as long as the objective grows by no more
// than this fraction.
const usageSwapTolerance = 0.02

// washReachFactor is the strength of the per-sample "reach" (contrast) penalty:
// how much a target's cost is inflated by the WASHING distance of the nearest
// achievable vertex — |eff(c*,s) − nominal(c*)|, the Lab distance a translucent
// filament had to travel from its own color toward the sample to reach it.
//
// Rationale (the defect-1 chameleon): the per-sample eff assumes each cell's
// neighborhood equals its target. That holds for a lone cell dropped into a sea
// of the target color, but the MAJORITY filament of a solid region is surrounded
// by ITSELF, so a tiled region reads as the filament's NOMINAL color, not its
// washed-toward-target eff. A near-neutral, high-β filament (e.g. Dark Olive
// Drab, TD defaulted to 1.0) therefore looks universally capable under bare eff
// scoring — its vertex slides ~60-74% toward every target — yet prints as flat
// olive and the dither can't converge (global drift stays ~15 ΔE vs ~7 for an
// honest palette). Charging the washing distance makes such a chameleon pay for
// the reach it can't deliver in bulk, while an opaque filament sitting AT the
// target (β = 0, wash = 0) pays nothing, and a saturated translucent filament
// whose hue filter T kills the channels it would need to move stays far in eff
// space anyway (it never becomes the nearest vertex to a color it can't render).
// Note wash ≈ βT·|s − C|, so this is a soft re-introduction of the nominal
// distance, gated to the filament actually relied upon for that sample.
//
// Calibrated (see internal/palette/td_select_test.go and the orzel drivers) so
// free 4-slot selection recovers the Brown/White/Black eagle body while the
// defect-2 Brown-over-WineRed fix and the color fixtures are preserved.
// Overridable via DITHERFORGE_SELECT_WASH for recalibration.
const washReachFactor = 0.6

func washReachFactorFromEnv() float64 {
	if v := os.Getenv("DITHERFORGE_SELECT_WASH"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			return f
		}
	}
	return washReachFactor
}

// tdSelectState precomputes everything the per-sample TD-aware subset scorer
// needs. Unlike the nominal scorer — which gives each filament ONE static Lab
// vertex — the TD-aware scorer's vertices vary per sample: entry e contributes
// eff(e, s), the neighbor-transmittance composite of e's filament toward THAT
// sample's target color, exactly mirroring the per-cell rule the dither itself
// applies (voxel.EffectiveCellColors). The old global-mean approximation gave a
// translucent filament a single vertex pulled toward the whole model's average,
// which let a saturated filament (Wine Red) fake-enclose interior body colors it
// physically cannot render per cell. eff(e, s) depends only on (entry, sample) —
// never on the subset — so the full [entry][sample] Lab matrix is precomputed
// once and the scorer just indexes it.
type tdSelectState struct {
	inventory []InventoryEntry
	nLocked   int
	samples   []WeightedLabSample

	invEff  [][][3]float64 // invEff[e][j]: eff Lab of inventory entry e at sample j
	lockEff [][][3]float64 // lockEff[i][j]: eff Lab of locked entry i at sample j

	invNomLab  [][3]float64 // nominal Lab per inventory entry (reach penalty)
	lockNomLab [][3]float64 // nominal Lab per locked entry

	invCol  []colorful.Color // nominal color per inventory entry (near-duplicate ΔE00)
	lockCol []colorful.Color // nominal color per locked entry

	knee      []float64 // per-sample chromaKnee = exp(-chroma/chromaSpreadFalloff)
	dithering bool
	wash      float64 // washReachFactor (env-overridable)
}

// newTDSelectState builds the per-sample effective-color matrices. invLab and
// lockedLab are the nominal Lab colors (used only for near-duplicate
// suppression); the eff matrices are computed from the entries' linear colors
// via neighborEffLab, the same math the per-cell print simulation uses, so
// selection and simulation can't drift.
func newTDSelectState(inventory, locked []InventoryEntry, invLab, lockedLab [][3]float64, samples []WeightedLabSample, neighborPath, kappa float64, dithering bool) *tdSelectState {
	// Per-sample target in linear-light RGB (the neighborhood each translucent
	// filament washes toward) plus its chroma knee.
	sLin := make([][3]float64, len(samples))
	knee := make([]float64, len(samples))
	for j := range samples {
		lab := samples[j].Lab
		r, g, b := colorful.Lab(lab[0], lab[1], lab[2]).LinearRgb()
		sLin[j] = [3]float64{r, g, b}
		// Standard CIELAB chroma (go-colorful scales Lab by 1/100).
		chroma := math.Sqrt(lab[1]*lab[1]+lab[2]*lab[2]) * 100
		knee[j] = math.Exp(-chroma / chromaSpreadFalloff)
	}

	buildEff := func(entries []InventoryEntry, nomLab [][3]float64) [][][3]float64 {
		eff := make([][][3]float64, len(entries))
		for e := range entries {
			lin := linearOf(entries[e].Color)
			beta := NeighborLeak(normSelTD(entries[e].TD), neighborPath)
			row := make([][3]float64, len(samples))
			if beta == 0 {
				// Opaque: the filament prints its own color everywhere, so the
				// vertex is the nominal Lab at every sample (bit-identical to
				// the nominal scorer for this entry).
				for j := range samples {
					row[j] = nomLab[e]
				}
			} else {
				for j := range samples {
					row[j] = neighborEffLab(lin, beta, sLin[j], kappa)
				}
			}
			eff[e] = row
		}
		return eff
	}

	colsOf := func(entries []InventoryEntry) []colorful.Color {
		cols := make([]colorful.Color, len(entries))
		for e := range entries {
			c := entries[e].Color
			cols[e] = colorful.Color{R: float64(c[0]) / 255, G: float64(c[1]) / 255, B: float64(c[2]) / 255}
		}
		return cols
	}

	return &tdSelectState{
		inventory:  inventory,
		nLocked:    len(locked),
		samples:    samples,
		invEff:     buildEff(inventory, invLab),
		lockEff:    buildEff(locked, lockedLab),
		invNomLab:  invLab,
		lockNomLab: lockedLab,
		invCol:     colsOf(inventory),
		lockCol:    colsOf(locked),
		knee:       knee,
		dithering:  dithering,
		wash:       washReachFactorFromEnv(),
	}
}

// hasNominalDuplicate reports whether the subset (locked + candidate indices)
// contains a pair of near-identical NOMINAL colors involving at least one free
// pick. Locked-vs-locked pairs are the user's explicit choice and are never
// rejected; a free pick landing near a locked color, or two free picks landing
// near each other, is rejected (defect 1).
func (st *tdSelectState) hasNominalDuplicate(indices []int) bool {
	// go-colorful's DistanceCIEDE2000 is scaled by 1/100 vs standard ΔE00.
	const thresh = nominalDupDeltaE / 100.0
	// Free pick vs locked color (a free pick near a locked color is rejected).
	for a := 0; a < st.nLocked; a++ {
		for b := range indices {
			if st.lockCol[a].DistanceCIEDE2000(st.invCol[indices[b]]) < thresh {
				return true
			}
		}
	}
	// Free pick vs free pick.
	for a := range indices {
		for b := a + 1; b < len(indices); b++ {
			if st.invCol[indices[a]].DistanceCIEDE2000(st.invCol[indices[b]]) < thresh {
				return true
			}
		}
	}
	return false
}

// score returns the TD-aware subset cost: the weighted sum over samples of the
// squared distance from each target to the per-sample effective-color hull
// (plus the dithering spread penalty), with near-duplicate subsets rejected.
func (st *tdSelectState) score(indices []int) float64 {
	if st.hasNominalDuplicate(indices) {
		return math.MaxFloat64
	}
	nv := st.nLocked + len(indices)
	verts := make([][3]float64, nv)
	// Nominal vertices are sample-independent (the reach penalty measures each
	// eff vertex's wash from its own nominal), so assemble them once.
	nom := make([][3]float64, nv)
	for i := 0; i < st.nLocked; i++ {
		nom[i] = st.lockNomLab[i]
	}
	for k, idx := range indices {
		nom[st.nLocked+k] = st.invNomLab[idx]
	}
	total := 0.0
	for j := range st.samples {
		s := st.samples[j]
		for i := 0; i < st.nLocked; i++ {
			verts[i] = st.lockEff[i][j]
		}
		for k, idx := range indices {
			verts[st.nLocked+k] = st.invEff[idx][j]
		}
		var d float64
		if st.dithering {
			// Attribute the target's hull coverage to the vertices actually
			// supporting the closest hull point (barycentric weights over the
			// enclosing simplex — the containing tetrahedron for an interior
			// sample, else the nearest triangle/edge/vertex). hullDist equals
			// distToConvexHull; the feature just tells us WHO provides the reach.
			hullDist, feat, bary := closestHullFeature(s.Lab, verts)
			knee := st.knee[j]
			nearDist := nearestVertexDistChromaWeighted(s.Lab, verts, knee)
			spread := ditherSpreadFactor * knee
			// Reach (contrast) penalty, charged on hull MEMBERSHIP rather than
			// only the nearest vertex: sum each supporting vertex's washing
			// distance (how far its translucent eff had to slide from its own
			// nominal color to reach this target) weighted by its barycentric
			// share of the coverage. This is what stops a saturated translucent
			// "chameleon" (e.g. Magenta) from enclosing interior body colors for
			// free — it never had to be the NEAREST vertex to fake-cover them, so
			// the old nearest-only charge missed it entirely, yet under the
			// additive model its eff slides most of the way to every target.
			washDist := 0.0
			for m, vi := range feat {
				washDist += bary[m] * dist3(verts[vi], nom[vi])
			}
			d = hullDist + spread*nearDist + st.wash*washDist
		} else {
			d = nearestVertexDist(s.Lab, verts)
		}
		total += d * d * s.Weight
	}
	return total
}

// usage returns each palette member's predicted usage: the fraction of
// area-weighted samples for which that member is the nearest achievable
// (per-sample eff) vertex. The returned slice is indexed [0..nLocked) for
// locked members then [nLocked..) for the candidate indices, matching the vertex
// order in score. It is the selection-time stand-in for "how much will the
// dither actually place this filament" without running the (expensive) dither.
func (st *tdSelectState) usage(indices []int) []float64 {
	counts := make([]float64, st.nLocked+len(indices))
	var totalW float64
	verts := make([][3]float64, st.nLocked+len(indices))
	for j := range st.samples {
		s := st.samples[j]
		for i := 0; i < st.nLocked; i++ {
			verts[i] = st.lockEff[i][j]
		}
		for k, idx := range indices {
			verts[st.nLocked+k] = st.invEff[idx][j]
		}
		best := math.MaxFloat64
		bestV := 0
		for v := range verts {
			dd := dist3(s.Lab, verts[v])
			if dd < best {
				best = dd
				bestV = v
			}
		}
		counts[bestV] += s.Weight
		totalW += s.Weight
	}
	if totalW > 0 {
		for i := range counts {
			counts[i] /= totalW
		}
	}
	return counts
}

// refineUsage is the predicted-usage safety net (not an optimizer): if a free
// pick's predicted usage is ~0 (usageDeadFraction) it swaps that member for the
// non-selected inventory entry that both scores within usageSwapTolerance of the
// current subset AND is genuinely used, preferring the alternative with the
// highest predicted usage. This catches the defect-1 tail where a duplicate-ish
// pick survives scoring yet the dither never places it. Bounded to one swap per
// free slot.
func (st *tdSelectState) refineUsage(best []int) []int {
	cur := make([]int, len(best))
	copy(cur, best)
	inUse := make(map[int]bool, len(cur))
	for _, idx := range cur {
		inUse[idx] = true
	}
	curScore := st.score(cur)

	for pass := 0; pass < len(cur); pass++ {
		u := st.usage(cur)
		// Find the free member with the lowest dead-level usage.
		deadPos := -1
		deadUsage := usageDeadFraction
		for k := range cur {
			if uk := u[st.nLocked+k]; uk < deadUsage {
				deadUsage = uk
				deadPos = k
			}
		}
		if deadPos < 0 {
			break
		}

		bestAlt := -1
		bestAltScore := math.MaxFloat64
		bestAltUsage := deadUsage
		for ci := range st.inventory {
			if inUse[ci] {
				continue
			}
			trial := make([]int, len(cur))
			copy(trial, cur)
			trial[deadPos] = ci
			if st.hasNominalDuplicate(trial) {
				continue
			}
			sc := st.score(trial)
			if sc > curScore*(1+usageSwapTolerance) {
				continue
			}
			ut := st.usage(trial)[st.nLocked+deadPos]
			// Prefer the most-used alternative (must beat the dead member's own
			// usage); break exact ties toward the lower score. bestAlt >= 0
			// guards the tie branch so the initial bestAltUsage == deadUsage
			// seed can't accept an equally-dead alternative.
			if ut > bestAltUsage || (bestAlt >= 0 && ut == bestAltUsage && sc < bestAltScore) {
				bestAlt = ci
				bestAltScore = sc
				bestAltUsage = ut
			}
		}
		if bestAlt < 0 {
			break
		}
		delete(inUse, cur[deadPos])
		inUse[bestAlt] = true
		cur[deadPos] = bestAlt
		curScore = bestAltScore
	}
	return cur
}
