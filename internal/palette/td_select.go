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

// mixSpreadMu weights the MIX-SPREAD cost: per sample, the barycentric-weighted
// distance from the supporting hull vertices to the target, μ·Σ_k bary_k·|eff_k
// − s|. Dither speckle visibility scales with how far apart the colors being
// mixed sit, so hull distance alone can't tell "reached by mixing two near
// neighbours" (cheap, the legitimate use of dithering) from "reached by mixing
// black + yellow + azure" (garish) — both land on the hull. This term charges
// the spread, so a near-match workhorse beats a saturated hull-inflating extreme
// under strong search. Local mixing between nearby colors stays nearly free.
//
// Calibrated (mu, nu) = (0.15, 0.08) by sweeping the earth / bricks_benchy /
// orzel / gray-eagle fixtures under budgeted-exhaustive search so each GLOBAL
// optimum is perceptually right (earth keeps a land colour, orzel drops the
// Purple/OliveGreen hull-spanners for an opaque {Black, Brown, DarkOliveDrab,
// Tan}, bricks keeps a warm chromatic, gray keeps its dark anchor). Higher mu
// starts collapsing toward k-medoids (gamut shrink); higher nu drops necessary
// dark anchors. Overridable via DITHERFORGE_SELECT_MU.
const mixSpreadMu = 0.15

func mixSpreadMuFromEnv() float64 {
	if v := os.Getenv("DITHERFORGE_SELECT_MU"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			return f
		}
	}
	return mixSpreadMu
}

// mixComplexityNu weights the MIX-COMPLEXITY cost: ν·max(0, effN − 2) where
// effN = 1/Σ_k bary_k² is the Simpson effective number of participating vertices.
// A color made by mixing two filaments reads cleaner than one made from three or
// more, so pure matches (effN = 1) and 2-mixes (effN = 2) are free while balanced
// 3+ mixes pay proportionally. effN (not support cardinality) is used because an
// interior point in 3D Lab generically has 4-vertex barycentric support even when
// its weight is concentrated on two — effN discounts the near-zero contributors
// smoothly. Calibrated to 0.08 (see mixSpreadMu); the fixture sweep breaks above
// ~0.10 (the complexity charge starts dropping the dark anchor a warm body needs
// to reduce its 3-mix). Overridable via DITHERFORGE_SELECT_NU.
const mixComplexityNu = 0.08

func mixComplexityNuFromEnv() float64 {
	if v := os.Getenv("DITHERFORGE_SELECT_NU"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			return f
		}
	}
	return mixComplexityNu
}

// ditherSpreadFactorFromEnv returns the effective ditherSpreadFactor, overridable
// via DITHERFORGE_SELECT_SPREAD for recalibration (see ditherSpreadFactor).
func ditherSpreadFactorFromEnv() float64 {
	if v := os.Getenv("DITHERFORGE_SELECT_SPREAD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			return f
		}
	}
	return ditherSpreadFactor
}

// chromaSpreadFalloffFromEnv returns the effective chromaSpreadFalloff,
// overridable via DITHERFORGE_SELECT_CHROMA_FALLOFF (see chromaSpreadFalloff).
// The override must be strictly positive — it is a divisor in the chroma knee.
func chromaSpreadFalloffFromEnv() float64 {
	if v := os.Getenv("DITHERFORGE_SELECT_CHROMA_FALLOFF"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return chromaSpreadFalloff
}

// SelectionTuning captures the effective (post-env-override where applicable)
// numeric constants that parameterize palette subset selection. The pipeline
// hashes these into the palette-stage cache key (see hashPaletteSettings) so a
// weight-tuning change — a constant edit or an env override — invalidates stale
// selections instead of silently serving them.
type SelectionTuning struct {
	WashReachFactor     float64
	MixSpreadMu         float64
	MixComplexityNu     float64
	DitherSpreadFactor  float64
	ChromaSpreadFalloff float64
	NominalDupDeltaE    float64
	SelectEvalBudget    float64
	NumVNDAnchorStarts  float64
	VNDEvalCap          float64
	UsageDeadFraction   float64
	UsageSwapTolerance  float64
}

// EffectiveSelectionTuning reports the tuning constants actually in force for
// this process, resolving every env override exactly as selection does.
func EffectiveSelectionTuning() SelectionTuning {
	return SelectionTuning{
		WashReachFactor:     washReachFactorFromEnv(),
		MixSpreadMu:         mixSpreadMuFromEnv(),
		MixComplexityNu:     mixComplexityNuFromEnv(),
		DitherSpreadFactor:  ditherSpreadFactorFromEnv(),
		ChromaSpreadFalloff: chromaSpreadFalloffFromEnv(),
		NominalDupDeltaE:    nominalDupDeltaE,
		SelectEvalBudget:    selectEvalBudget,
		NumVNDAnchorStarts:  numVNDAnchorStarts,
		VNDEvalCap:          vndEvalCap,
		UsageDeadFraction:   usageDeadFraction,
		UsageSwapTolerance:  usageSwapTolerance,
	}
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

	knee      []float64 // per-sample chromaKnee = exp(-chroma/falloff)
	dithering bool
	wash      float64 // washReachFactor (env-overridable)
	mu        float64 // mixSpreadMu (env-overridable)
	nu        float64 // mixComplexityNu (env-overridable)
	spread    float64 // ditherSpreadFactor (env-overridable)
	falloff   float64 // chromaSpreadFalloff (env-overridable)
}

// newTDSelectState builds the per-sample effective-color matrices. invLab and
// lockedLab are the nominal Lab colors (used only for near-duplicate
// suppression); the eff matrices are computed from the entries' linear colors
// via neighborEffLab, the same math the per-cell print simulation uses, so
// selection and simulation can't drift.
// forceOpaque, when true, pins every filament's lateral leak β to 0 regardless
// of its TD — eff becomes the nominal Lab at every sample. This is the unified
// dithering=true entry point when no real leak is in play (honorTD off, uniform
// TDs, or every filament effectively opaque): the per-sample scorer's wash term
// self-zeroes (eff == nominal) while its mix-spread, mix-complexity, duplicate
// reject, usage net, and search all still apply.
func newTDSelectState(inventory, locked []InventoryEntry, invLab, lockedLab [][3]float64, samples []WeightedLabSample, neighborPath, kappa float64, dithering, forceOpaque bool) *tdSelectState {
	falloff := chromaSpreadFalloffFromEnv()
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
		knee[j] = math.Exp(-chroma / falloff)
	}

	buildEff := func(entries []InventoryEntry, nomLab [][3]float64) [][][3]float64 {
		eff := make([][][3]float64, len(entries))
		for e := range entries {
			lin := linearOf(entries[e].Color)
			beta := NeighborLeak(normSelTD(entries[e].TD), neighborPath)
			if forceOpaque {
				beta = 0
			}
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
		mu:         mixSpreadMuFromEnv(),
		nu:         mixComplexityNuFromEnv(),
		spread:     ditherSpreadFactorFromEnv(),
		falloff:    falloff,
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
//
// bound is the branch-and-bound early-abort budget (see scoreFunc): the
// per-sample accumulation is monotonically non-decreasing (every term is
// non-negative), so once the running total strictly exceeds bound the final
// score can only be larger and the loop returns noBound. Pruning is
// strictly-greater, so any subset whose exact score merely ties bound is scored
// in full and the returned value matches an unbounded evaluation — the global
// optimum (and anything tied with it) is never pruned, keeping selection
// bit-identical. Pass noBound to disable pruning and get the exact score.
func (st *tdSelectState) score(indices []int, bound float64) float64 {
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
		if total > bound {
			return noBound
		}
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
			spread := st.spread * knee
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
			// MIX-SPREAD: barycentric-weighted distance from the supporting
			// vertices to the TARGET — how far apart the colors dithered to reach
			// this sample sit (speckle visibility). MIX-COMPLEXITY: the Simpson
			// effective number of participating vertices effN = 1/Σ bary², charged
			// only past 2 (mixing 3+ filaments reads worse than 2). Both computed
			// from the same barycentric feature as the wash term.
			mixSpread := 0.0
			sumSq := 0.0
			for m, vi := range feat {
				b := bary[m]
				washDist += b * dist3(verts[vi], nom[vi])
				mixSpread += b * dist3(verts[vi], s.Lab)
				sumSq += b * b
			}
			mixComplexity := 0.0
			if sumSq > 0 {
				if effN := 1.0 / sumSq; effN > 2 {
					mixComplexity = effN - 2
				}
			}
			d = hullDist + spread*nearDist + st.wash*washDist + st.mu*mixSpread + st.nu*mixComplexity
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
	curScore := st.score(cur, noBound)

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
			// Exact score needed: it feeds the usage-tie-break comparison below.
			sc := st.score(trial, noBound)
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
