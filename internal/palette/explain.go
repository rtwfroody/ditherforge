package palette

import (
	"fmt"

	colorful "github.com/lucasb-eyer/go-colorful"
)

// This file holds (a) the small setup seams SelectFromInventory and the offline
// explain harness share, and (b) ExplainSubset — a DEBUG-ONLY introspection API
// used by cmd/palettesearch --explain to decompose the production selection cost
// for an explicit subset. Nothing here is called from the production selection
// path; ExplainSubset exists so calibration work can read the objective's
// per-sample terms without duplicating (and drifting from) tdSelectState.

// selectionSamples builds the weighted Lab sample set the subset scorers
// consume: a deduplicated per-color histogram, chroma-reweighted, trimmed to the
// 5000-sample cap. Shared by SelectFromInventory and ExplainSubset so the
// explain harness scores the exact sample set production scores.
func selectionSamples(cellColors [][3]uint8, cellWeights []float32) []WeightedLabSample {
	samples := CellColorHistogram(cellColors, cellWeights)
	ApplyChromaWeighting(samples)
	return topSamples(samples, 5000)
}

// nominalLabs converts entries' nominal filament colors to Lab.
func nominalLabs(entries []InventoryEntry) [][3]float64 {
	labs := make([][3]float64, len(entries))
	for i, e := range entries {
		cf := colorful.Color{
			R: float64(e.Color[0]) / 255.0,
			G: float64(e.Color[1]) / 255.0,
			B: float64(e.Color[2]) / 255.0,
		}
		labs[i][0], labs[i][1], labs[i][2] = cf.Lab()
	}
	return labs
}

// selectionNeighborContext resolves the neighbor-model parameters (path length,
// κ) and decides whether a GENUINE per-sample lateral leak is in play — TD
// honored, the normalized TDs non-uniform, and at least one filament translucent
// enough to leak. Only then does each filament's effective color vary per
// sample; a uniform shift (or no shift, when every filament is effectively
// opaque) can't reorder the subsets being compared, so the scorer forces β = 0
// and eff ≡ nominal.
func selectionNeighborContext(inventory, locked []InventoryEntry, tdp TDParams) (neighborPath, kappa float64, tdLeak bool) {
	neighborPath = float64(tdp.NeighborPathMM)
	if neighborPath <= 0 {
		neighborPath = DefaultNeighborPathMM
	}
	kappa = float64(tdp.Kappa)
	if !tdp.Enabled {
		return neighborPath, kappa, false
	}
	uniform := true
	anyLeak := false
	var first float64
	seen := false
	checkTD := func(td float32) {
		nt := normSelTD(td)
		if !seen {
			first = nt
			seen = true
		} else if nt != first {
			uniform = false
		}
		if NeighborLeak(nt, neighborPath) > 0 {
			anyLeak = true
		}
	}
	for _, e := range inventory {
		checkTD(e.TD)
	}
	for _, e := range locked {
		checkTD(e.TD)
	}
	return neighborPath, kappa, !uniform && anyLeak
}

// ExplainSample is the per-sample decomposition of the TD-aware selection cost
// for one subset. The terms combine as
//
//	d    = HullDist + SpreadCoef*NearDist + wash*WashDist + mu*MixSpreadCost + nu*MixComplexity
//	Cost = d * d * Weight
//
// so the reported Cost is exactly this sample's contribution to the objective
// tdSelectState.score sums. All Lab quantities are in go-colorful's Lab scale
// (L in [0,1], a/b in ~[-1.3,1.3]) — i.e. 1/100 of standard CIELAB units.
type ExplainSample struct {
	Lab    [3]float64
	Count  int
	Weight float64

	Knee       float64 // exp(-chroma/falloff)
	SpreadCoef float64 // st.spread * Knee — the coefficient multiplying NearDist

	HullDist      float64
	NearDist      float64
	BulkDist      float64 // dist to hull of the support's NOMINAL colors
	WashDist      float64 // max(0, BulkDist − HullDist) — the value wash multiplies
	MixSpread     float64 // raw barycentric mix spread S, pre-saturation
	MixSpreadCost float64 // saturate(S, S0) — the value mu multiplies
	MixComplexity float64

	D    float64
	Cost float64

	// EffLab is the per-sample effective Lab of each palette vertex (locked
	// slots first, then the free picks in the order given), i.e. the `verts`
	// the scorer actually built its hull from at this sample.
	EffLab [][3]float64
	// Feat/Bary are the supporting sub-simplex of the closest hull point and
	// its barycentric weights, indices into EffLab.
	Feat []int
	Bary []float64
}

// ExplainResult is the full decomposition of one subset's production score.
type ExplainResult struct {
	Tuning  SelectionTuning
	TDLeak  bool // was a genuine per-sample lateral leak in play (β != 0)?
	Samples []ExplainSample

	// Total is the objective value: Σ Cost. It equals tdSelectState.score for
	// the same subset (verified internally), or +Inf when the subset is
	// rejected outright as a nominal near-duplicate.
	Total             float64
	RejectedDuplicate bool

	// Vertex metadata, locked slots first then free picks, parallel to
	// ExplainSample.EffLab.
	NominalLab [][3]float64
	Labels     []string
	Hexes      []string
	TDs        []float32
	Locked     []bool
}

// ExplainSubset reproduces the production selection objective for one explicit
// subset and returns its per-sample term decomposition. DEBUG ONLY: it is used
// by cmd/palettesearch --explain for scorer calibration and is never called from
// the pipeline. It mirrors SelectFromInventory's setup exactly (same sample
// construction, same nominal Lab conversion, same neighbor/tdLeak context, same
// env-resolved weights) and evaluates through the very same tdSelectState the
// production search uses, so its Total is the production score.
//
// inventory must be the locked-filtered inventory the production search sees;
// chosen are indices into it (the free picks). dithering mirrors the caller's
// `opts.Dither != "none"`.
func ExplainSubset(cellColors [][3]uint8, cellWeights []float32, inventory, locked []InventoryEntry, chosen []int, dithering bool, tdp TDParams) (*ExplainResult, error) {
	for _, idx := range chosen {
		if idx < 0 || idx >= len(inventory) {
			return nil, fmt.Errorf("palette: explain index %d out of range (inventory has %d entries)", idx, len(inventory))
		}
	}
	samples := selectionSamples(cellColors, cellWeights)
	invLab := nominalLabs(inventory)
	lockedLab := nominalLabs(locked)
	neighborPath, kappa, tdLeak := selectionNeighborContext(inventory, locked, tdp)
	st := newTDSelectState(inventory, locked, invLab, lockedLab, samples, neighborPath, kappa, dithering, !tdLeak)

	res := &ExplainResult{
		Tuning:            EffectiveSelectionTuning(),
		TDLeak:            tdLeak,
		RejectedDuplicate: st.hasNominalDuplicate(chosen),
	}
	nv := st.nLocked + len(chosen)
	res.NominalLab = make([][3]float64, nv)
	res.Labels = make([]string, nv)
	res.Hexes = make([]string, nv)
	res.TDs = make([]float32, nv)
	res.Locked = make([]bool, nv)
	describe := func(i int, e InventoryEntry, lab [3]float64, isLocked bool) {
		res.NominalLab[i] = lab
		res.Labels[i] = e.Label
		res.Hexes[i] = fmt.Sprintf("#%02X%02X%02X", e.Color[0], e.Color[1], e.Color[2])
		res.TDs[i] = e.TD
		res.Locked[i] = isLocked
	}
	for i := 0; i < st.nLocked; i++ {
		describe(i, locked[i], lockedLab[i], true)
	}
	for k, idx := range chosen {
		describe(st.nLocked+k, inventory[idx], invLab[idx], false)
	}

	// Re-run the exact per-sample cost of tdSelectState.score, retaining the
	// intermediate terms. Kept deliberately parallel to score() — the Total is
	// cross-checked against score() below, which catches any drift.
	verts := make([][3]float64, nv)
	res.Samples = make([]ExplainSample, len(samples))
	total := 0.0
	for j := range samples {
		s := samples[j]
		for i := 0; i < st.nLocked; i++ {
			verts[i] = st.lockEff[i][j]
		}
		for k, idx := range chosen {
			verts[st.nLocked+k] = st.invEff[idx][j]
		}
		es := ExplainSample{Lab: s.Lab, Count: s.Count, Weight: s.Weight}
		es.EffLab = make([][3]float64, nv)
		copy(es.EffLab, verts)

		if st.dithering {
			hullDist, feat, bary := closestHullFeature(s.Lab, verts)
			knee := st.knee[j]
			es.Knee = knee
			es.HullDist = hullDist
			es.NearDist = nearestVertexDistChromaWeighted(s.Lab, verts, knee)
			es.SpreadCoef = st.spread * knee
			es.Feat = append([]int(nil), feat...)
			es.Bary = append([]float64(nil), bary...)
			sumSq := 0.0
			nomSup := make([][3]float64, len(feat))
			for m, vi := range feat {
				b := bary[m]
				nomSup[m] = res.NominalLab[vi]
				es.MixSpread += b * dist3(verts[vi], s.Lab)
				sumSq += b * b
			}
			// Bulk-reach shortfall: what the support's NOMINAL mix can reach,
			// beyond what the eff hull already promised (see score()).
			es.BulkDist = hullDistance(s.Lab, nomSup)
			if es.BulkDist > hullDist {
				es.WashDist = es.BulkDist - hullDist
			}
			if sumSq > 0 {
				if effN := 1.0 / sumSq; effN > 2 {
					es.MixComplexity = effN - 2
				}
			}
			es.MixSpreadCost = saturate(es.MixSpread, st.sat)
			es.D = es.HullDist + es.SpreadCoef*es.NearDist + st.wash*es.WashDist +
				st.mu*es.MixSpreadCost + st.nu*es.MixComplexity
		} else {
			es.D = nearestVertexDist(s.Lab, verts)
		}
		es.Cost = es.D * es.D * s.Weight
		total += es.Cost
		res.Samples[j] = es
	}
	res.Total = total

	// Cross-check against the production scorer itself. Any divergence means
	// this decomposition has drifted from score() and the numbers are lies.
	if !res.RejectedDuplicate {
		want := st.score(chosen, noBound)
		if rel := absf(want-total) / maxf(1e-300, absf(want)); rel > 1e-12 {
			return nil, fmt.Errorf("palette: explain decomposition diverged from score(): %g vs %g (rel %g)", total, want, rel)
		}
	}
	return res, nil
}

func absf(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
