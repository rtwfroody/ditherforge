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

// washReachFactor is the strength of the per-sample BULK-REACH SHORTFALL
// penalty: how much a target's cost is inflated by coverage the effective-color
// hull promises but a tiled region cannot actually deliver (see
// tdSelectState.score for the formula).
//
// Rationale (the defect-1 chameleon): the per-sample eff assumes each cell's
// neighborhood equals its target. That holds for a lone cell dropped into a sea
// of the target color, but the MAJORITY filament of a solid region is surrounded
// by ITSELF, so a tiled region reads as the filament's NOMINAL color, not its
// washed-toward-target eff. A near-neutral, high-β filament (e.g. Dark Olive
// Drab, TD defaulted to 1.0) therefore looks universally capable under bare eff
// scoring — its vertex slides ~60-74% toward every target — yet prints as flat
// olive and the dither can't converge (global drift stays ~15 ΔE vs ~7 for an
// honest palette). Charging the shortfall makes such a chameleon pay for the
// reach it can't deliver in bulk, while an opaque filament (β = 0) pays nothing.
//
// The charge is levied on the MIX, not per vertex: what a dithered region can
// truly make is the hull of the supporting filaments' nominal colors, so
// filaments that bracket the target pay ~0 no matter how translucent they are,
// and only a filament relied upon ALONE pays its full nominal distance. The
// earlier per-vertex form Σ bary_k·|eff_k − nom_k| could not draw that
// distinction (with κ=0 it collapsed to Σ bary_k·β_k·|s − nom_k|) and taxed
// honest dithering at the chameleon's rate.
//
// Set by regret minimization over calibration/groundtruth/ via calibration/
// tune.sh — coordinate descent minimizing total palette-selection regret across
// the 4 ground-truth fixtures — not hand-tuned. Overridable via
// DITHERFORGE_SELECT_WASH for recalibration.
const washReachFactor = 0.9

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
// Set by regret minimization over calibration/groundtruth/ via calibration/
// tune.sh (2026-07-24), not hand-tuned. The stronger μ (up from the hand-tuned
// 0.15) is harsher on wide mixes — it charges more for reaching a target by
// dithering colors that sit far apart — which the coordinate descent found
// strictly improves all 4 ground-truth fixtures. Overridable via
// DITHERFORGE_SELECT_MU.
const mixSpreadMu = 0.30

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
// smoothly. Set by regret minimization over calibration/groundtruth/ via
// calibration/tune.sh (2026-07-24): the descent drove ν to 0.0, which DISABLES
// the mix-complexity term at the shipped default (the stronger μ mix-spread
// charge now carries the load the ν term used to). The term and its env override
// (DITHERFORGE_SELECT_NU) are retained for future tuning — set ν > 0 to
// re-enable it. Overridable via DITHERFORGE_SELECT_NU.
const mixComplexityNu = 0.0

func mixComplexityNuFromEnv() float64 {
	if v := os.Getenv("DITHERFORGE_SELECT_NU"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			return f
		}
	}
	return mixComplexityNu
}

// mixSpreadSat is the saturation scale S0 of the MIX-SPREAD cost. The raw
// per-sample mix spread S = Σ_k bary_k·|eff_k − s| grows without bound as the
// supporting hull vertices spread apart, but dither GRAININESS — what the term
// is meant to model — saturates: once the mixed colors are already far apart the
// speckle is fully visible and pushing them further apart barely changes the
// perceived texture. The linear μ·S therefore over-taxed palettes built from
// gamut extremes (a mid-hue sample barycentrically supported by Black + a
// saturated primary has a large S even though the dither renders the mix fine —
// ground truth confirms benchy's top entries all use such extremes). The
// saturating form μ·S/(1 + S/S0) is ≈ linear for S ≪ S0 and asymptotes to μ·S0
// for S ≫ S0, so near-neighbour mixing stays cheap while distant mixing is
// charged a bounded graininess penalty instead of an ever-growing distance.
// A very large S0 recovers the old linear behavior. Overridable via
// DITHERFORGE_SELECT_MIXSAT (must be strictly positive — it is a divisor).
const mixSpreadSat = 30.0

func mixSpreadSatFromEnv() float64 {
	if v := os.Getenv("DITHERFORGE_SELECT_MIXSAT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return mixSpreadSat
}

// saturate maps a non-negative raw spread s through the soft-knee s/(1 + s/s0):
// ≈ s for s ≪ s0, exactly s0/2 at s == s0, and asymptotically s0 for s ≫ s0. It
// is monotone increasing in s. A very large s0 recovers the identity (linear)
// mapping. s0 must be > 0 (guaranteed by mixSpreadSatFromEnv).
func saturate(s, s0 float64) float64 {
	return s / (1 + s/s0)
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
	MixSpreadSat        float64
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
		MixSpreadSat:        mixSpreadSatFromEnv(),
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
	sat       float64 // mixSpreadSat S0 (env-overridable)
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
		sat:        mixSpreadSatFromEnv(),
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
	// Scratch for the supporting vertices' nominal colors (the bulk-reach hull).
	// At most nv of them, reused across samples to keep score() allocation-free
	// in its inner loop.
	nomSup := make([][3]float64, nv)
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
			// sample, else the nearest triangle/edge/vertex). hullDist is the exact
			// hull distance; the feature just tells us WHO provides the reach.
			hullDist, feat, bary := closestHullFeature(s.Lab, verts)
			knee := st.knee[j]
			nearDist := nearestVertexDistChromaWeighted(s.Lab, verts, knee)
			spread := st.spread * knee
			// Reach (contrast) penalty: the BULK-REACH SHORTFALL, i.e. how much
			// of the eff hull's promised coverage survives when the region is
			// actually tiled. eff assumes every cell is surrounded by its target;
			// the bulk truth is that a region tiled from the supporting filaments
			// reads at their NOMINAL colors, so what the dither can really make
			// there is the hull of those nominals. Charge the excess:
			//
			//     wash = max(0, dist(s, hull(nominals of support)) − hullDist)
			//
			// A chameleon relied on alone (support = {it}) collapses that hull to
			// a single distant point and pays the full nominal distance — it can
			// only slide toward the target as a lone cell in a sea of that target,
			// never as the majority filament. Two filaments that genuinely
			// bracket the target (Red + Yellow across an orange band) have a
			// nominal segment that already contains it and pay ~0, because that
			// mix is exactly what the dither will lay down. Opaque filaments have
			// eff == nominal, so the support hull IS the closest hull feature and
			// the term self-zeroes — keeping the forceOpaque path bit-identical
			// to the nominal scorer.
			//
			// This replaces the per-vertex form Σ bary_k·|eff_k − nom_k|, which
			// with the calibrated κ=0 reduced to Σ bary_k·β_k·|s − nom_k| — a
			// nominal-space spread charge that could not tell a lone chameleon
			// from honest two-color dithering, and taxed both. That blunt
			// distrust-all-translucency pressure is gone: a leaky filament is now
			// charged only where the mix it participates in genuinely cannot
			// reach the target in bulk.
			//
			// MIX-SPREAD: barycentric-weighted distance from the supporting
			// vertices to the TARGET — how far apart the colors dithered to reach
			// this sample sit (speckle visibility). MIX-COMPLEXITY: the Simpson
			// effective number of participating vertices effN = 1/Σ bary², charged
			// only past 2 (mixing 3+ filaments reads worse than 2). Both share the
			// barycentric feature with the wash term, so all three come out of one
			// pass over the support.
			nSup := len(feat)
			mixSpread := 0.0
			sumSq := 0.0
			for m, vi := range feat {
				b := bary[m]
				nomSup[m] = nom[vi]
				mixSpread += b * dist3(verts[vi], s.Lab)
				sumSq += b * b
			}
			washDist := 0.0
			if bulk := hullDistance(s.Lab, nomSup[:nSup]); bulk > hullDist {
				washDist = bulk - hullDist
			}
			mixComplexity := 0.0
			if sumSq > 0 {
				if effN := 1.0 / sumSq; effN > 2 {
					mixComplexity = effN - 2
				}
			}
			// Saturate the mix-spread contribution: it tracks the raw spread S for
			// S ≪ S0 but asymptotes to S0 for S ≫ S0, modeling graininess (which
			// saturates once the mixed colors are already far apart) rather than raw
			// distance (which grows without bound and over-taxes gamut extremes).
			mixSpreadCost := saturate(mixSpread, st.sat)
			d = hullDist + spread*nearDist + st.wash*washDist + st.mu*mixSpreadCost + st.nu*mixComplexity
		} else {
			d = nearestVertexDist(s.Lab, verts)
		}
		total += d * d * s.Weight
	}
	return total
}

// usage returns each palette member's predicted usage: its barycentric share of
// the area-weighted samples. Each sample's weight is split across the vertices of
// its closest hull feature (the containing tetrahedron for an interior sample,
// else the nearest triangle/edge/vertex) by that feature's barycentric weights —
// the SAME attribution score() charges through closestHullFeature. The returned
// slice is indexed [0..nLocked) for locked members then [nLocked..) for the
// candidate indices, matching the vertex order in score. It is the selection-time
// stand-in for "how much will the dither actually place this filament" without
// running the (expensive) dither.
//
// The old nearest-vertex-only attribution predicted 0.000 for a filament that is
// a minority barycentric contributor to many samples yet never the single closest
// vertex — exactly the kind the dither still places a few percent of the time —
// so membership attribution both matches score()'s cost model and makes the
// dead-weight net (refineUsage) accurate. Membership usage is a superset of the
// nearest-only usage (a nearest vertex always carries bary weight), so the
// usageDeadFraction floor only gets sharper.
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
		// bary sums to 1 over feat, so the sample's full weight is conserved.
		_, feat, bary := closestHullFeature(s.Lab, verts)
		for m, vi := range feat {
			counts[vi] += bary[m] * s.Weight
		}
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
