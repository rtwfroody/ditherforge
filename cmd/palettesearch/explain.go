package main

// --explain: offline decomposition of the production palette-selection
// objective for two (or more) explicit palettes on one shared sample set.
//
// This is a calibration diagnostic, not part of any pipeline. It answers "why
// does the fast scorer prefer palette B when ground truth says A?" by printing
// (1) each palette's total production score, (2) a per-term breakdown of the
// objective with two defensible decompositions of the squaring, (3) a hue-bin
// sample-level story of where each palette wins and loses, and (4) for the
// highest-chroma samples of a chosen hue bin, the Lab convex-hull distance the
// scorer uses side by side with the distance to the set actually reachable by
// ADDITIVE mixing in linear light (the convex hull in linear RGB, measured in
// Lab) — the reachability question.
//
// All Lab quantities are printed in standard CIELAB units (the internal Lab is
// go-colorful's 1/100-scaled Lab, so printed values are scaled by 100). The
// objective Total is left in native units; "rmsD" is sqrt(Total/ΣW)·100, an
// interpretable weight-RMS of the per-sample distance d in ΔE-like units.

import (
	"fmt"
	"math"
	"sort"
	"strings"

	colorful "github.com/lucasb-eyer/go-colorful"

	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
)

// termNames are the five additive components of the per-sample distance d, in
// the order tdSelectState.score sums them.
var termNames = []string{"hull", "spread*near", "wash*washDist", "mu*mixSpread", "nu*mixCplx"}

// terms returns the five WEIGHTED components of d for one sample (they sum to
// s.D exactly).
func terms(t palette.SelectionTuning, s palette.ExplainSample) [5]float64 {
	return [5]float64{
		s.HullDist,
		s.SpreadCoef * s.NearDist,
		t.WashReachFactor * s.WashDist,
		t.MixSpreadMu * s.MixSpreadCost,
		t.MixComplexityNu * s.MixComplexity,
	}
}

// hueBins buckets a Lab sample by hue angle, with a low-chroma neutral bin.
// Boundaries are placed around where the CURATED FILAMENTS land in CIELAB, not
// around naive RGB intuition: a fire-engine red (#E72F1D) sits at Lab hue 39,
// orange (#F67405) at 57, yellow (#FFE800) at 97, green (#06924D) at 151, azure
// (#0066D9) at 287. Bin order is fixed so the palettes' tables line up.
var hueBins = []struct {
	name     string
	lo, hi   float64 // hue degrees, [lo, hi); wraps when lo > hi
	wrapping bool
}{
	{name: "red", lo: 0, hi: 45},
	{name: "orange", lo: 45, hi: 72},
	{name: "yellow", lo: 72, hi: 110},
	{name: "green", lo: 110, hi: 180},
	{name: "cyan", lo: 180, hi: 225},
	{name: "blue", lo: 225, hi: 300},
	{name: "purple", lo: 300, hi: 360},
}

const neutralChroma = 10.0 // CIELAB chroma below which a sample is "neutral"

// labPolar returns (chroma, hue°) in standard CIELAB units for an internal
// (1/100-scaled) Lab triple.
func labPolar(lab [3]float64) (chroma, hue float64) {
	a, b := lab[1]*100, lab[2]*100
	chroma = math.Hypot(a, b)
	hue = math.Atan2(b, a) * 180 / math.Pi
	if hue < 0 {
		hue += 360
	}
	return chroma, hue
}

// binOfSample returns the hue-bin index, or -1 for the neutral bin.
func binOfSample(lab [3]float64) int {
	chroma, hue := labPolar(lab)
	if chroma < neutralChroma {
		return -1
	}
	for i, b := range hueBins {
		if b.wrapping {
			if hue >= b.lo || hue < b.hi {
				return i
			}
		} else if hue >= b.lo && hue < b.hi {
			return i
		}
	}
	return -1
}

func binName(i int) string {
	if i < 0 {
		return "neutral"
	}
	return hueBins[i].name
}

// runExplain prints the full diagnostic for the report's subsets.
func runExplain(rep *pipeline.ExplainReport, deepBin string, deepChroma float64, deepTop int) {
	fmt.Printf("EXPLAIN: %d cells, %d weighted Lab samples, %d free slot(s), dithering=%v\n",
		rep.NumCells, rep.NumSamples, rep.FreeSlots, rep.Dithering)
	if len(rep.LockedHexes) > 0 {
		fmt.Printf("Locked:  %s\n", describeHexes(rep.LockedHexes, rep.LockedLabels))
	}
	fmt.Printf("Production scorer's own pick: %s\n", describeHexes(rep.ProductionHexes, rep.ProductionLabels))

	t := rep.Results[0].Tuning
	fmt.Printf("Tuning:  wash=%.3g mu=%.3g nu=%.3g mixsat=%.3g spread=%.3g chromaFalloff=%.3g  (tdLeak=%v)\n",
		t.WashReachFactor, t.MixSpreadMu, t.MixComplexityNu, t.MixSpreadSat,
		t.DitherSpreadFactor, t.ChromaSpreadFalloff, rep.Results[0].TDLeak)
	fmt.Println("All Lab distances below are in standard CIELAB units (x100 of the internal scale).")

	names := make([]string, len(rep.Results))
	for i := range rep.Results {
		names[i] = string(rune('A' + i))
		fmt.Printf("\n[%s] %s\n", names[i], describeHexes(rep.FreeHexes[i], rep.FreeLabels[i]))
		printVertices(rep.Results[i])
	}

	printTotals(rep, names)
	printTermBreakdown(rep, names)
	printHueBins(rep, names)
	if len(rep.Results) >= 2 {
		printPairDeltaByBin(rep, names)
	}
	d := computeReach(rep)
	printGlobalReachability(rep, names, d)
	printDeepDive(rep, names, d, deepBin, deepChroma, deepTop)
}

// reach holds, per palette per sample, the four hull-distance variants that
// bracket the reachability question:
//
//	[0] effLab -- distance to the Lab convex hull of the per-sample EFFECTIVE
//	    vertices. This is exactly the scorer's hullDist.
//	[1] effLin -- distance to the set reachable by ADDITIVE mixing of those same
//	    effective vertices in linear light (measured back in Lab).
//	[2] nomLab -- Lab convex hull of the NOMINAL filament colors.
//	[3] nomLin -- additive linear-light mixing of the NOMINAL filament colors:
//	    the physically honest reachable set for a dithered REGION, since the
//	    calibrated neighbor model is additive in linear light.
type reach [][][4]float64

func computeReach(rep *pipeline.ExplainReport) reach {
	out := make(reach, len(rep.Results))
	for p, r := range rep.Results {
		out[p] = make([][4]float64, len(r.Samples))
		for j, s := range r.Samples {
			effLin, _, _ := closestMix(s.Lab, s.EffLab, true)
			nomLab, _, _ := closestMix(s.Lab, r.NominalLab, false)
			nomLin, _, _ := closestMix(s.Lab, r.NominalLab, true)
			out[p][j] = [4]float64{s.HullDist, effLin, nomLab, nomLin}
		}
	}
	return out
}

var reachNames = []string{"eff:labHull", "eff:linHull", "nom:labHull", "nom:linHull"}

// printGlobalReachability answers the design question directly: over the WHOLE
// model, how much would each palette's score change if the reachable set were
// modeled as the convex hull in linear RGB (what additive dithering can really
// make) instead of the convex hull in Lab (what the scorer assumes)?
func printGlobalReachability(rep *pipeline.ExplainReport, names []string, d reach) {
	fmt.Printf("\n== REACHABILITY, WHOLE MODEL ==\n")
	fmt.Printf("Weighted means of the four hull variants (CIELAB units), then the objective\n")
	fmt.Printf("recomputed with hullDist REPLACED by each variant (every other term untouched).\n")
	fmt.Printf("If the Lab hull were over-crediting unreachable colors, the linear variants would\n")
	fmt.Printf("be substantially LARGER, and larger for the wrong pick than for the winner.\n\n")
	fmt.Printf("%-3s", "id")
	for _, n := range reachNames {
		fmt.Printf(" %-13s", n)
	}
	fmt.Printf(" |")
	for _, n := range reachNames {
		fmt.Printf(" %-13s", "tot/"+n)
	}
	fmt.Println()
	totals := make([][4]float64, len(rep.Results))
	for p, r := range rep.Results {
		var tw float64
		var mean [4]float64
		var tot [4]float64
		for j, s := range r.Samples {
			tw += s.Weight
			for k := 0; k < 4; k++ {
				mean[k] += d[p][j][k] * s.Weight
				dd := s.D - s.HullDist + d[p][j][k]
				tot[k] += dd * dd * s.Weight
			}
		}
		totals[p] = tot
		fmt.Printf("%-3s", names[p])
		for k := 0; k < 4; k++ {
			fmt.Printf(" %-13.4f", mean[k]/tw*100)
		}
		fmt.Printf(" |")
		for k := 0; k < 4; k++ {
			fmt.Printf(" %-13.6g", tot[k])
		}
		fmt.Println()
	}
	if len(rep.Results) >= 2 {
		fmt.Printf("\ngap (Total_B - Total_A) under each reachability model (negative = scorer prefers B,\nthe production pick; a FIX must make this positive):\n")
		for k := 0; k < 4; k++ {
			fmt.Printf("   %-13s %+.6g\n", reachNames[k], totals[1][k]-totals[0][k])
		}
	}
}

func printVertices(r *palette.ExplainResult) {
	for i := range r.Hexes {
		role := "free"
		if r.Locked[i] {
			role = "LOCKED"
		}
		lab := r.NominalLab[i]
		chroma, hue := labPolar(lab)
		fmt.Printf("      v%d %-7s %s %-16s TD=%.2f  nominal Lab (%.1f, %.1f, %.1f)  C=%.1f h=%.0f\n",
			i, role, r.Hexes[i], r.Labels[i], r.TDs[i],
			lab[0]*100, lab[1]*100, lab[2]*100, chroma, hue)
	}
}

func printTotals(rep *pipeline.ExplainReport, names []string) {
	fmt.Printf("\n== TOTAL PRODUCTION SCORE (the objective SelectFromInventory minimizes; lower wins) ==\n")
	fmt.Printf("%-3s %-16s %-12s %-10s %s\n", "id", "total", "rmsD(dE)", "dup-reject", "palette")
	var best float64 = math.Inf(1)
	bestID := ""
	for i, r := range rep.Results {
		var wsum float64
		for _, s := range r.Samples {
			wsum += s.Weight
		}
		rms := math.Sqrt(r.Total/wsum) * 100
		fmt.Printf("%-3s %-16.6g %-10.4f %-12v %s\n", names[i], r.Total, rms, r.RejectedDuplicate,
			strings.Join(rep.FreeHexes[i], " "))
		if r.Total < best {
			best, bestID = r.Total, names[i]
		}
	}
	if len(rep.Results) >= 2 {
		a, b := rep.Results[0].Total, rep.Results[1].Total
		fmt.Printf("\nScorer prefers [%s]. B-A = %+.6g (%+.3f%% of A).\n", bestID, b-a, 100*(b-a)/a)
	}
}

func printTermBreakdown(rep *pipeline.ExplainReport, names []string) {
	fmt.Printf("\n== PER-TERM BREAKDOWN ==\n")
	fmt.Printf("raw:    weight-mean of the term itself, in CIELAB units (Sum w*t / Sum w * 100)\n")
	fmt.Printf("share:  Sum 2*d*t*w, normalized by 2*Total. EXACT (d = Sum t so the shares sum to 1),\n")
	fmt.Printf("        but it is the first-order/Euler attribution of d^2 = d * Sum t, not a\n")
	fmt.Printf("        separable decomposition -- a term's share is inflated by the OTHER terms\n")
	fmt.Printf("        making d large at the same samples.\n")
	fmt.Printf("zero:   Total - Total recomputed with that term deleted from d. This is the honest\n")
	fmt.Printf("        counterfactual, but the five zero-deltas do NOT sum to Total (cross terms).\n")

	for i, r := range rep.Results {
		t := r.Tuning
		var wsum, rawSum [5]float64
		var shareSum [5]float64
		var zeroTotal [5]float64
		var totW float64
		for _, s := range r.Samples {
			tv := terms(t, s)
			totW += s.Weight
			for k := 0; k < 5; k++ {
				rawSum[k] += tv[k] * s.Weight
				shareSum[k] += 2 * s.D * tv[k] * s.Weight
				d := s.D - tv[k]
				zeroTotal[k] += d * d * s.Weight
			}
			_ = wsum
		}
		fmt.Printf("\n[%s] %s   total %.6g\n", names[i], strings.Join(rep.FreeHexes[i], " "), r.Total)
		fmt.Printf("   %-16s %-12s %-10s %-14s\n", "term", "raw(dE)", "share", "zero-delta")
		for k := 0; k < 5; k++ {
			fmt.Printf("   %-16s %-12.4f %-10.4f %-14.6g\n",
				termNames[k], rawSum[k]/totW*100, shareSum[k]/(2*r.Total), r.Total-zeroTotal[k])
		}
	}

	if len(rep.Results) < 2 {
		return
	}
	// Pairwise: which term accounts for the B-A gap, under the zero-counterfactual
	// (recompute both totals with the term deleted and compare the gaps).
	a, b := rep.Results[0], rep.Results[1]
	fmt.Printf("\n-- which term creates the [%s]-[%s] gap? --\n", names[1], names[0])
	fmt.Printf("   gapWith = Total_B - Total_A with all terms; gapWithout = same with the term deleted.\n")
	fmt.Printf("   A term whose removal flips the sign of the gap is single-handedly responsible.\n")
	gapAll := b.Total - a.Total
	fmt.Printf("   %-16s %-14s %-14s %s\n", "term deleted", "gapWithout", "gapWith", "attributable")
	for k := 0; k < 5; k++ {
		var ta, tb float64
		for _, s := range a.Samples {
			d := s.D - terms(a.Tuning, s)[k]
			ta += d * d * s.Weight
		}
		for _, s := range b.Samples {
			d := s.D - terms(b.Tuning, s)[k]
			tb += d * d * s.Weight
		}
		gw := tb - ta
		fmt.Printf("   %-16s %-14.6g %-14.6g %+.6g\n", termNames[k], gw, gapAll, gapAll-gw)
	}
	// Also: raw-term-only comparison (each palette's weighted mean per term).
	fmt.Printf("\n-- per-term weight-mean (CIELAB units), A vs B --\n")
	fmt.Printf("   %-16s %-12s %-12s %s\n", "term", "A", "B", "B-A")
	var totW float64
	for _, s := range a.Samples {
		totW += s.Weight
	}
	for k := 0; k < 5; k++ {
		var ra, rb float64
		for _, s := range a.Samples {
			ra += terms(a.Tuning, s)[k] * s.Weight
		}
		for _, s := range b.Samples {
			rb += terms(b.Tuning, s)[k] * s.Weight
		}
		fmt.Printf("   %-16s %-12.4f %-12.4f %+.4f\n", termNames[k], ra/totW*100, rb/totW*100, (rb-ra)/totW*100)
	}
}

type binAgg struct {
	weight float64
	count  int
	cost   [8]float64 // per-palette total cost (cap 8 palettes)
	term   [8][5]float64
	nSam   int
}

func aggregateBins(rep *pipeline.ExplainReport) (map[int]*binAgg, float64) {
	bins := map[int]*binAgg{}
	var totW float64
	base := rep.Results[0]
	for j, s := range base.Samples {
		bi := binOfSample(s.Lab)
		ag := bins[bi]
		if ag == nil {
			ag = &binAgg{}
			bins[bi] = ag
		}
		ag.weight += s.Weight
		ag.count += s.Count
		ag.nSam++
		totW += s.Weight
		for p, r := range rep.Results {
			if p >= 8 {
				break
			}
			sp := r.Samples[j]
			ag.cost[p] += sp.Cost
			tv := terms(r.Tuning, sp)
			for k := 0; k < 5; k++ {
				ag.term[p][k] += 2 * sp.D * tv[k] * sp.Weight
			}
		}
	}
	return bins, totW
}

func sortedBinKeys(bins map[int]*binAgg) []int {
	keys := make([]int, 0, len(bins))
	for k := range bins {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return bins[keys[i]].weight > bins[keys[j]].weight
	})
	return keys
}

func printHueBins(rep *pipeline.ExplainReport, names []string) {
	bins, totW := aggregateBins(rep)
	fmt.Printf("\n== SAMPLE-LEVEL STORY: cost by hue bin ==\n")
	fmt.Printf("(bins by CIELAB hue angle; chroma < %.0f is 'neutral'. wt%% is the bin's share of\n", neutralChroma)
	fmt.Printf(" total sample WEIGHT -- the chroma-inflated weight the objective actually uses.)\n\n")
	hdr := fmt.Sprintf("%-9s %-8s %-7s %-6s", "bin", "wt%", "nsamp", "cells")
	for _, n := range names {
		hdr += fmt.Sprintf(" %-13s", "cost["+n+"]")
	}
	if len(names) >= 2 {
		hdr += fmt.Sprintf(" %-13s %-8s", "B-A", "B-A %ofgap")
	}
	fmt.Println(hdr)
	var gap float64
	if len(rep.Results) >= 2 {
		gap = rep.Results[1].Total - rep.Results[0].Total
	}
	for _, k := range sortedBinKeys(bins) {
		ag := bins[k]
		line := fmt.Sprintf("%-9s %-8.2f %-7d %-6d", binName(k), 100*ag.weight/totW, ag.nSam, ag.count)
		for p := range rep.Results {
			line += fmt.Sprintf(" %-13.6g", ag.cost[p])
		}
		if len(rep.Results) >= 2 {
			d := ag.cost[1] - ag.cost[0]
			pct := 0.0
			if gap != 0 {
				pct = 100 * d / gap
			}
			line += fmt.Sprintf(" %-+13.6g %-8.1f", d, pct)
		}
		fmt.Println(line)
	}
}

func printPairDeltaByBin(rep *pipeline.ExplainReport, names []string) {
	bins, _ := aggregateBins(rep)
	fmt.Printf("\n== WHERE THE GAP LIVES: per-bin, per-term share difference (Sum 2*d*t*w, B minus A) ==\n")
	fmt.Printf("(positive = palette B pays more of that term in that bin)\n\n")
	fmt.Printf("%-9s", "bin")
	for _, n := range termNames {
		fmt.Printf(" %-14s", n)
	}
	fmt.Println()
	for _, k := range sortedBinKeys(bins) {
		ag := bins[k]
		fmt.Printf("%-9s", binName(k))
		for t := 0; t < 5; t++ {
			fmt.Printf(" %-+14.6g", ag.term[1][t]-ag.term[0][t])
		}
		fmt.Println()
	}
	_ = names
}

// ---------------------------------------------------------------------------
// Reachability: Lab convex hull (what the scorer models) vs the set actually
// reachable by additive mixing in LINEAR light (what the print does).

// labToLinear converts an internal (1/100-scaled) Lab triple to linear RGB.
func labToLinear(lab [3]float64) [3]float64 {
	r, g, b := colorful.Lab(lab[0], lab[1], lab[2]).LinearRgb()
	return [3]float64{r, g, b}
}

// linearToLab is the inverse.
func linearToLab(lin [3]float64) [3]float64 {
	l, a, b := colorful.LinearRgb(lin[0], lin[1], lin[2]).Lab()
	return [3]float64{l, a, b}
}

func labDist(a, b [3]float64) float64 {
	return math.Sqrt((a[0]-b[0])*(a[0]-b[0]) + (a[1]-b[1])*(a[1]-b[1]) + (a[2]-b[2])*(a[2]-b[2]))
}

// closestMix finds the convex combination of the given palette colors whose Lab
// is closest to the target Lab. When linear is true the mixing happens in
// LINEAR-RGB (the physically honest additive model the print simulation uses)
// and the result is mapped back to Lab; when false the mixing happens directly
// in Lab, which makes the answer exactly the Euclidean distance to the Lab
// convex hull — the model the production scorer uses. Running the same search
// both ways isolates the reachability question from every other difference.
//
// Deliberately brute force: a coarse barycentric grid over the simplex followed
// by pairwise mass-transfer refinement with a halving step. Returns (Lab
// distance, the winning barycentric weights, the reached Lab).
func closestMix(targetLab [3]float64, verts [][3]float64, linear bool) (float64, []float64, [3]float64) {
	n := len(verts)
	pts := make([][3]float64, n)
	for i, l := range verts {
		if linear {
			pts[i] = labToLinear(l)
		} else {
			pts[i] = l
		}
	}
	eval := func(w []float64) (float64, [3]float64) {
		var mix [3]float64
		for i := 0; i < n; i++ {
			mix[0] += w[i] * pts[i][0]
			mix[1] += w[i] * pts[i][1]
			mix[2] += w[i] * pts[i][2]
		}
		lab := mix
		if linear {
			lab = linearToLab(mix)
		}
		return labDist(lab, targetLab), lab
	}

	// Coarse grid: all compositions of `res` into n non-negative parts.
	const res = 32
	best := math.Inf(1)
	bestW := make([]float64, n)
	cur := make([]int, n)
	var walk func(i, left int)
	walk = func(i, left int) {
		if i == n-1 {
			cur[i] = left
			w := make([]float64, n)
			for k := 0; k < n; k++ {
				w[k] = float64(cur[k]) / res
			}
			if d, _ := eval(w); d < best {
				best = d
				copy(bestW, w)
			}
			return
		}
		for v := 0; v <= left; v++ {
			cur[i] = v
			walk(i+1, left-v)
		}
	}
	walk(0, res)

	// Refinement: move mass delta from j to i while it helps, halving delta.
	w := make([]float64, n)
	copy(w, bestW)
	trial := make([]float64, n)
	for delta := 1.0 / res; delta > 1e-7; delta /= 2 {
		improved := true
		for improved {
			improved = false
			for i := 0; i < n; i++ {
				for j := 0; j < n; j++ {
					if i == j || w[j] < delta {
						continue
					}
					copy(trial, w)
					trial[i] += delta
					trial[j] -= delta
					if d, _ := eval(trial); d < best-1e-15 {
						best = d
						copy(w, trial)
						improved = true
					}
				}
			}
		}
	}
	_, lab := eval(w)
	return best, w, lab
}

// printDeepDive prints the per-sample table: every sample (top N by weight),
// its hue-bin identity, and for each palette the full term decomposition plus
// the Lab-hull vs linear-RGB-hull reachability pair. deepBin/deepChroma select
// the subset that gets the extra per-vertex geometry dump.
func printDeepDive(rep *pipeline.ExplainReport, names []string, d reach, deepBin string, deepChroma float64, deepTop int) {
	base := rep.Results[0]
	type row struct {
		idx    int
		weight float64
		chroma float64
		hue    float64
	}
	rows := make([]row, 0, len(base.Samples))
	for j, s := range base.Samples {
		c, h := labPolar(s.Lab)
		rows = append(rows, row{j, s.Weight, c, h})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].weight > rows[j].weight })
	tw := totalWeight(base)

	fmt.Printf("\n== PER-SAMPLE TABLE (top %d of %d by weight) ==\n", minInt(deepTop, len(rows)), len(rows))
	fmt.Printf("labHull = the scorer's hullDist: Lab distance to the CONVEX HULL OF THE per-sample\n")
	fmt.Printf("          effective Lab vertices (0 = 'the scorer believes it can mix this exactly').\n")
	fmt.Printf("linHull = Lab distance to the nearest color actually reachable by ADDITIVE mixing in\n")
	fmt.Printf("          linear light: min over convex combos of the SAME vertices' LINEAR RGB,\n")
	fmt.Printf("          mapped back to Lab. This is the physically reachable set.\n")
	fmt.Printf("Both are reported twice: over the per-sample EFFECTIVE vertices (what the scorer\n")
	fmt.Printf("actually hulls, translucency-washed toward the target) and over the NOMINAL filament\n")
	fmt.Printf("colors (what a printed REGION averages to, since the neighbor model is additive).\n")
	fmt.Printf("wash/mix/near are the already-weighted terms (wash*washDist etc.), CIELAB units.\n\n")

	n := minInt(deepTop, len(rows))
	for _, rw := range rows[:n] {
		s0 := base.Samples[rw.idx]
		lab := s0.Lab
		fmt.Printf("sample #%-3d %s Lab(%6.1f,%6.1f,%6.1f) C=%-5.1f h=%-5.0f bin=%-8s wt=%6.2f%% cells=%d\n",
			rw.idx, labHex(lab), lab[0]*100, lab[1]*100, lab[2]*100, rw.chroma, rw.hue,
			binName(binOfSample(lab)), 100*rw.weight/tw, s0.Count)
		fmt.Printf("   %-3s | %-8s %-8s | %-8s %-8s | %-7s %-8s %-7s %-8s %-9s | %s\n",
			"id", "eff:lab", "eff:lin", "nom:lab", "nom:lin",
			"near", "wash", "mix", "d", "cost", "support (bary)")
		for p, r := range rep.Results {
			s := r.Samples[rw.idx]
			dv := d[p][rw.idx]
			tv := terms(r.Tuning, s)
			fmt.Printf("   %-3s | %-8.3f %-8.3f | %-8.3f %-8.3f | %-7.3f %-8.3f %-7.3f %-8.3f %-9.4g | %s\n",
				names[p], dv[0]*100, dv[1]*100, dv[2]*100, dv[3]*100,
				tv[1]*100, tv[2]*100, tv[3]*100, s.D*100, s.Cost, supportString(r, s))
		}
	}

	// Extra geometry dump for the selected bin/chroma slice.
	var deep []row
	for _, rw := range rows {
		if binName(binOfSample(base.Samples[rw.idx].Lab)) == deepBin && rw.chroma >= deepChroma {
			deep = append(deep, rw)
		}
	}
	fmt.Printf("\n== REACHABILITY DEEP DIVE: bin %q, chroma >= %.0f (%d samples) ==\n", deepBin, deepChroma, len(deep))
	if len(deep) == 0 {
		return
	}
	var wTot float64
	agg := make([][4]float64, len(rep.Results))
	for _, rw := range deep {
		wTot += rw.weight
		for p := range rep.Results {
			dv := d[p][rw.idx]
			for k := 0; k < 4; k++ {
				agg[p][k] += dv[k] * rw.weight
			}
		}
	}
	fmt.Printf("weighted means over those samples (CIELAB units), total weight %.4g (%.2f%% of model):\n",
		wTot, 100*wTot/tw)
	fmt.Printf("   %-3s %-11s %-11s %-11s %-11s %-11s\n",
		"id", "eff:labHull", "eff:linHull", "nom:labHull", "nom:linHull", "nom lin-lab")
	for p := range rep.Results {
		fmt.Printf("   %-3s %-11.4f %-11.4f %-11.4f %-11.4f %-+11.4f\n", names[p],
			agg[p][0]/wTot*100, agg[p][1]/wTot*100, agg[p][2]/wTot*100, agg[p][3]/wTot*100,
			(agg[p][3]-agg[p][2])/wTot*100)
	}

	heavy := deep[0]
	fmt.Printf("\nheaviest such sample: %s Lab (%.1f, %.1f, %.1f)\n",
		labHex(base.Samples[heavy.idx].Lab),
		base.Samples[heavy.idx].Lab[0]*100, base.Samples[heavy.idx].Lab[1]*100, base.Samples[heavy.idx].Lab[2]*100)
	for p, r := range rep.Results {
		s := r.Samples[heavy.idx]
		fmt.Printf("  [%s] %s\n", names[p], strings.Join(rep.FreeHexes[p], " "))
		for i := range s.EffLab {
			fmt.Printf("      v%d %-16s eff Lab (%6.1f,%6.1f,%6.1f)  nominal (%6.1f,%6.1f,%6.1f)  |eff-nom| %.1f\n",
				i, r.Labels[i],
				s.EffLab[i][0]*100, s.EffLab[i][1]*100, s.EffLab[i][2]*100,
				r.NominalLab[i][0]*100, r.NominalLab[i][1]*100, r.NominalLab[i][2]*100,
				labDist(s.EffLab[i], r.NominalLab[i])*100)
		}
		effLin, wl, reachedL := closestMix(s.Lab, s.EffLab, true)
		nomLabD, wnl, reachedNL := closestMix(s.Lab, r.NominalLab, false)
		nomLinD, wn, reachedN := closestMix(s.Lab, r.NominalLab, true)
		fmt.Printf("      eff:labHull %.3f (feat %v bary %s)   <- what the scorer charges\n",
			s.HullDist*100, s.Feat, fmtFloats(s.Bary))
		fmt.Printf("      eff:linHull %.3f  mix %s -> Lab (%.1f,%.1f,%.1f)\n",
			effLin*100, fmtFloats(wl), reachedL[0]*100, reachedL[1]*100, reachedL[2]*100)
		fmt.Printf("      nom:labHull %.3f  mix %s -> Lab (%.1f,%.1f,%.1f)\n",
			nomLabD*100, fmtFloats(wnl), reachedNL[0]*100, reachedNL[1]*100, reachedNL[2]*100)
		fmt.Printf("      nom:linHull %.3f  mix %s -> Lab (%.1f,%.1f,%.1f)   <- physically reachable\n",
			nomLinD*100, fmtFloats(wn), reachedN[0]*100, reachedN[1]*100, reachedN[2]*100)
		tv := terms(r.Tuning, s)
		fmt.Printf("      d %.4f = hull %.4f + spread %.4f + wash %.4f + mix %.4f + cplx %.4f   cost %.6g\n",
			s.D*100, tv[0]*100, tv[1]*100, tv[2]*100, tv[3]*100, tv[4]*100, s.Cost)
	}
}

// supportString names the vertices carrying the sample's hull coverage and
// their barycentric shares.
func supportString(r *palette.ExplainResult, s palette.ExplainSample) string {
	parts := make([]string, 0, len(s.Feat))
	for m, vi := range s.Feat {
		if s.Bary[m] < 1e-4 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %.2f", r.Labels[vi], s.Bary[m]))
	}
	return strings.Join(parts, ", ")
}

// labHex renders an internal Lab triple as its clamped sRGB hex, for eyeballing
// which band of the model a sample belongs to.
func labHex(lab [3]float64) string {
	c := colorful.Lab(lab[0], lab[1], lab[2]).Clamped()
	return strings.ToUpper(c.Hex())
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func totalWeight(r *palette.ExplainResult) float64 {
	var w float64
	for _, s := range r.Samples {
		w += s.Weight
	}
	return w
}

func fmtFloats(v []float64) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = fmt.Sprintf("%.3f", x)
	}
	return "[" + strings.Join(parts, " ") + "]"
}
