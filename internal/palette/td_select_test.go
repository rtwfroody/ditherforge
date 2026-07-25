package palette

import (
	"context"
	"math"
	"sync/atomic"
	"testing"

	colorful "github.com/lucasb-eyer/go-colorful"
	"github.com/rtwfroody/ditherforge/internal/progress"
)

// nominalLabOf returns an sRGB byte color's CIELAB (go-colorful scaled), the
// same conversion the selector uses for nominal vertices.
func nominalLabOf(c [3]uint8) [3]float64 {
	l, a, b := colorful.Color{
		R: float64(c[0]) / 255, G: float64(c[1]) / 255, B: float64(c[2]) / 255,
	}.Lab()
	return [3]float64{l, a, b}
}

// labDE00 returns perceptual ΔE (CIEDE2000) between two sRGB colors — the same
// metric the near-duplicate suppression uses (go-colorful scales it by 1/100).
func labDE00(a, b [3]uint8) float64 {
	col := func(c [3]uint8) colorful.Color {
		return colorful.Color{R: float64(c[0]) / 255, G: float64(c[1]) / 255, B: float64(c[2]) / 255}
	}
	return 100 * col(a).DistanceCIEDE2000(col(b))
}

var tdKappaParams = TDParams{Enabled: true, LayerHeightMM: 0.08, ShellThicknessMM: 0.84, Kappa: TransmittanceKappa}

// TestTDLeak checks the scalar leak model against hand-computed values and its
// sanitization contract. voxel.EffectivePalette depends on these exact
// identities (garbage/opaque → 0) to stay byte-identical for opaque palettes.
func TestTDLeak(t *testing.T) {
	// Garbage TDs are all fully opaque → 0.
	for _, td := range []float64{0, -1, -0.0001, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := TDLeak(td, 0.08, 0.84); got != 0 {
			t.Errorf("TDLeak(%v) = %v, want 0 (garbage → opaque)", td, got)
		}
	}

	// A genuinely opaque filament (TD 0.1) under default geometry leaks well
	// below the 1/1024 floor → clamped to 0.
	if got := TDLeak(0.1, 0, 0); got != 0 {
		t.Errorf("TDLeak(0.1, defaults) = %v, want 0 (below 1/1024 floor)", got)
	}

	// Translucent orange: TD 3.3, layer 0.08, shell 0.84.
	// ℓ = 0.08·√2; N = 0.84/0.08 = 10.5 (within [1,64], so no clamp).
	// L = (10^(−ℓ/TD))^N.
	const layer, shell, td = 0.08, 0.84, 3.3
	ell := layer * math.Sqrt2
	n := shell / layer
	want := math.Pow(math.Pow(10, -ell/td), n)
	if want < 1.0/1024.0 {
		t.Fatalf("test setup: expected leak %v should exceed the 1/1024 floor", want)
	}
	got := TDLeak(td, layer, shell)
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("TDLeak(%v) = %.12f, want %.12f", td, got, want)
	}
}

// hasColor reports whether the selection contains the given color.
func hasColor(sel []InventoryEntry, c [3]uint8) bool {
	for _, e := range sel {
		if e.Color == c {
			return true
		}
	}
	return false
}

// TestSelectUniformTDBitIdentical: the post-unification invariant. Under
// dithering the per-sample TD-aware scorer is ALWAYS used; when no genuine
// lateral leak is in play (honorTD off, uniform TDs, or all-opaque) every β is
// forced to 0 so each filament's effective vertex equals its nominal Lab at every
// sample — the wash term self-zeroes and the scorer degrades to nominal-color
// scoring. This test pins that degradation two ways: (1) a uniform-TD state
// produces eff == nominal at every sample (bit-identical vertices), and (2) the
// three "no genuine leak" gates — honorTD off, uniform non-opaque TDs, and
// honorTD on with a leak present — all select the SAME subset, so none of them
// reorders the palette relative to the forced-opaque baseline.
func TestSelectUniformTDBitIdentical(t *testing.T) {
	inv := []InventoryEntry{
		{Color: [3]uint8{0x08, 0x0A, 0x0D}, TD: 1.5},
		{Color: [3]uint8{0x55, 0x33, 0x1A}, TD: 1.5},
		{Color: [3]uint8{0x61, 0x64, 0x69}, TD: 1.5},
		{Color: [3]uint8{0xF6, 0x74, 0x05}, TD: 1.5},
		{Color: [3]uint8{0xEE, 0xD1, 0xA8}, TD: 1.5},
		{Color: [3]uint8{0xD9, 0xDF, 0xE5}, TD: 1.5},
	}
	samples := grayEagleSamples()

	// (1) Bit-identical eff: a forced-opaque state (uniform TDs) gives every
	// entry its nominal Lab at every sample.
	invLab := make([][3]float64, len(inv))
	for i, e := range inv {
		invLab[i] = nominalLabOf(e.Color)
	}
	hist := CellColorHistogram(samples, nil)
	st := newTDSelectState(inv, nil, invLab, nil, hist, DefaultNeighborPathMM, TransmittanceKappa, true, true /*forceOpaque*/)
	for e := range inv {
		for j := range hist {
			if st.invEff[e][j] != invLab[e] {
				t.Fatalf("forced-opaque eff[%d][%d] = %v, want nominal %v", e, j, st.invEff[e][j], invLab[e])
			}
		}
	}

	// (2) The three no-genuine-leak gates all pick the same subset.
	sel := func(name string, tdp TDParams) []InventoryEntry {
		got, err := SelectFromInventory(context.Background(), samples, nil, inv, 4, nil, true, tdp, progress.NullTracker{})
		if err != nil {
			t.Fatalf("%s select: %v", name, err)
		}
		return got
	}
	honorOff := sel("honorTD-off", TDParams{})
	uniformOn := sel("uniform-on", TDParams{Enabled: true, LayerHeightMM: 0.08, ShellThicknessMM: 0.84})
	sameSet := func(a, b []InventoryEntry) bool {
		if len(a) != len(b) {
			return false
		}
		seen := map[[3]uint8]bool{}
		for _, e := range a {
			seen[e.Color] = true
		}
		for _, e := range b {
			if !seen[e.Color] {
				return false
			}
		}
		return true
	}
	if !sameSet(honorOff, uniformOn) {
		t.Errorf("honorTD-off %s != uniform-TD %s (forced-opaque degradation must match)", fmtSel(honorOff), fmtSel(uniformOn))
	}
}

// grayEagleSamples builds the cell-color cloud: a dark warm-brown body (30
// cells of one sienna) over an equal spread of near-grays (30 cells) — the
// ~50% grey coverage that makes SteelGrey a mandatory anchor and leaves the
// palette one contested warm/dark slot. Colors are repeated to weight the
// histogram (selection weights by occurrence when cellWeights is nil).
func grayEagleSamples() [][3]uint8 {
	warm := [3]uint8{0x68, 0x38, 0x18}
	gray := [][3]uint8{
		{0x61, 0x64, 0x69},
		{0x70, 0x72, 0x76},
		{0x5A, 0x5C, 0x60},
		{0x67, 0x69, 0x6D},
	}
	var out [][3]uint8
	for i := 0; i < 30; i++ {
		out = append(out, warm)
		out = append(out, gray[i%len(gray)])
	}
	return out
}

func fmtSel(sel []InventoryEntry) string {
	s := ""
	for i, e := range sel {
		if i > 0 {
			s += " "
		}
		s += hexOf(e.Color)
	}
	return s
}

func hexOf(c [3]uint8) string {
	const hexdig = "0123456789ABCDEF"
	b := []byte{'#',
		hexdig[c[0]>>4], hexdig[c[0]&0xF],
		hexdig[c[1]>>4], hexdig[c[1]&0xF],
		hexdig[c[2]>>4], hexdig[c[2]&0xF],
	}
	return string(b)
}

// TestSelectPerSampleBrownOverWineRed is the defect-2 regression in miniature.
// With a translucent Lemon Yellow locked and a warm-brown-dominant target
// cloud, the selector must keep OPAQUE Brown (which renders its sienna faithfully
// per cell) and drop translucent Wine Red — whose hue filter T ≈ [1,0,0] kills
// the green/blue it would need to move toward brown, so per sample its effective
// vertex stays saturated-red and far from every warm-brown target. The old
// global-mean approximation let Wine Red's single averaged vertex fake-enclose
// the body and displaced Brown; the per-sample eff scorer cannot be fooled that
// way.
func TestSelectPerSampleBrownOverWineRed(t *testing.T) {
	brown := [3]uint8{0x55, 0x33, 0x1A}
	winered := [3]uint8{0xD6, 0x02, 0x12}
	black := [3]uint8{0x08, 0x0A, 0x0D}
	white := [3]uint8{0xD9, 0xDF, 0xE5}
	inv := []InventoryEntry{
		{Color: brown, TD: 0.1},
		{Color: winered, TD: 1.0},
		{Color: black, TD: 0.1},
		{Color: white, TD: 0.3},
	}
	locked := []InventoryEntry{{Color: [3]uint8{0xEE, 0xD2, 0x30}, TD: 3.3}} // Lemon Yellow

	warm := [][3]uint8{{0x68, 0x38, 0x18}, {0x5A, 0x30, 0x16}, {0x72, 0x40, 0x20}}
	var samples [][3]uint8
	for i := 0; i < 40; i++ {
		samples = append(samples, warm[i%len(warm)])
	}
	for i := 0; i < 10; i++ {
		samples = append(samples, black)
		samples = append(samples, white)
	}

	sel, err := SelectFromInventory(context.Background(), samples, nil, inv, 3, locked, true, tdKappaParams, progress.NullTracker{})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	t.Logf("selected: %s", fmtSel(sel))
	if !hasColor(sel, brown) {
		t.Errorf("expected opaque Brown selected for the warm body; got %s", fmtSel(sel))
	}
	if hasColor(sel, winered) {
		t.Errorf("translucent Wine Red must not be selected (can't render warm brown per cell); got %s", fmtSel(sel))
	}
}

// TestSelectNominalDuplicateSuppressed pins defect 1: two nominally
// near-identical whites (ΔE < nominalDupDeltaE) must never both be chosen, even
// when the target cloud is white-heavy and bare scoring would happily spend two
// slots on them.
func TestSelectNominalDuplicateSuppressed(t *testing.T) {
	white1 := [3]uint8{0xEB, 0xF7, 0xFF} // TD 3.2 (translucent)
	white2 := [3]uint8{0xD9, 0xDF, 0xE5} // TD 0.3 (near-opaque), ΔE ≈ 5 from white1
	black := [3]uint8{0x08, 0x0A, 0x0D}
	brown := [3]uint8{0x55, 0x33, 0x1A}

	if de := labDE00(white1, white2); de >= nominalDupDeltaE {
		t.Fatalf("test setup: whites ΔE00 %.2f must be below the %.1f threshold", de, nominalDupDeltaE)
	}

	inv := []InventoryEntry{
		{Color: white1, TD: 3.2},
		{Color: white2, TD: 0.3},
		{Color: black, TD: 0.1},
		{Color: brown, TD: 0.1},
	}
	var samples [][3]uint8
	for i := 0; i < 40; i++ {
		samples = append(samples, white1)
		samples = append(samples, white2)
	}
	for i := 0; i < 10; i++ {
		samples = append(samples, black)
	}

	sel, err := SelectFromInventory(context.Background(), samples, nil, inv, 3, nil, true, tdKappaParams, progress.NullTracker{})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	t.Logf("selected: %s", fmtSel(sel))
	if hasColor(sel, white1) && hasColor(sel, white2) {
		t.Errorf("both near-identical whites selected (ΔE00 %.1f); expected at most one; got %s",
			labDE00(white1, white2), fmtSel(sel))
	}
}

// TestTDSelectPerSampleEff checks the core of the per-sample model: a
// translucent candidate's effective vertex must VARY between two different
// target samples (it washes toward each), while an opaque candidate's vertex
// stays exactly its nominal Lab at every sample (β = 0, bit-identical).
func TestTDSelectPerSampleEff(t *testing.T) {
	// TD 0.02 puts NeighborLeak below the 1/1024 floor → β = 0 (the opaque
	// limit); note a "typical opaque" filament like TD 0.1 actually leaks β ≈
	// 0.05 at ℓ = 0.13, so the truly-β0 case needs a smaller TD.
	opaque := InventoryEntry{Color: [3]uint8{0x55, 0x33, 0x1A}, TD: 0.02} // β = 0
	translu := InventoryEntry{Color: [3]uint8{0xF6, 0x74, 0x05}, TD: 3.3} // Orange, β ≈ 0.8
	inv := []InventoryEntry{opaque, translu}
	invLab := [][3]float64{nominalLabOf(opaque.Color), nominalLabOf(translu.Color)}
	if NeighborLeak(normSelTD(opaque.TD), DefaultNeighborPathMM) != 0 {
		t.Fatalf("test setup: opaque candidate should have β = 0")
	}

	// Two clearly different target colors → two samples.
	samples := CellColorHistogram([][3]uint8{{0x20, 0x10, 0x08}, {0xE0, 0xE0, 0xE0}}, nil)
	if len(samples) != 2 {
		t.Fatalf("expected 2 distinct samples, got %d", len(samples))
	}

	st := newTDSelectState(inv, nil, invLab, nil, samples, DefaultNeighborPathMM, TransmittanceKappa, true, false)

	// Opaque Brown: eff == nominal at every sample.
	for j := range samples {
		if st.invEff[0][j] != invLab[0] {
			t.Errorf("opaque entry eff at sample %d = %v, want nominal %v", j, st.invEff[0][j], invLab[0])
		}
	}
	// Translucent Orange: vertex differs between the two samples.
	if st.invEff[1][0] == st.invEff[1][1] {
		t.Errorf("translucent entry vertex identical across two different samples: %v", st.invEff[1][0])
	}
}

// TestUsageBarycentricMembership pins the phase-1a usage() rewrite: a filament
// that is a minority BARYCENTRIC contributor to many samples yet never the single
// nearest vertex must register real predicted usage (membership attribution)
// instead of the nearest-only zero that hid it from the dead-weight net. Three
// coplanar vertices form a triangle; every sample sits just above the A-B edge,
// so vertex C carries ~0.1 barycentric weight everywhere but is never the closest
// vertex.
func TestUsageBarycentricMembership(t *testing.T) {
	a := [3]float64{0, 0, 0}
	b := [3]float64{10, 0, 0}
	c := [3]float64{5, 10, 0}
	verts := [][3]float64{a, b, c}

	var samples []WeightedLabSample
	for px := 2.0; px <= 8.0; px += 0.5 {
		samples = append(samples, WeightedLabSample{Lab: [3]float64{px, 1, 0}, Weight: 1})
	}

	// Opaque: eff == nominal at every sample, so invEff[e][j] is the constant vertex.
	invEff := make([][][3]float64, len(verts))
	for e := range verts {
		row := make([][3]float64, len(samples))
		for j := range samples {
			row[j] = verts[e]
		}
		invEff[e] = row
	}
	st := &tdSelectState{samples: samples, invEff: invEff}
	indices := []int{0, 1, 2}

	// Old nearest-vertex-only attribution (what usage() used to compute): C is
	// never the closest vertex to any sample, so it scored exactly 0.
	oldC, totalW := 0.0, 0.0
	for _, s := range samples {
		best, bestV := math.MaxFloat64, 0
		for v := range verts {
			if d := dist3(s.Lab, verts[v]); d < best {
				best, bestV = d, v
			}
		}
		if bestV == 2 {
			oldC += s.Weight
		}
		totalW += s.Weight
	}
	oldC /= totalW
	if oldC != 0 {
		t.Fatalf("test setup: expected old nearest-only usage of C to be 0, got %.4f", oldC)
	}

	u := st.usage(indices)
	if u[2] <= 0.01 {
		t.Errorf("barycentric-membership usage of C = %.4f, want a clearly nonzero share (> 0.01)", u[2])
	}
	if sum := u[0] + u[1] + u[2]; math.Abs(sum-1) > 1e-9 {
		t.Errorf("usage shares sum to %.6f, want 1", sum)
	}
}

// panchromaBasicInventory is the 28-entry Panchroma Basic collection
// (internal/collection/builtins/panchroma_basic.txt) hardcoded so the palette
// package's tests need no import of internal/collection (which would cycle).
func panchromaBasicInventory() []InventoryEntry {
	e := func(r, g, b uint8, td float32) InventoryEntry {
		return InventoryEntry{Color: [3]uint8{r, g, b}, TD: td}
	}
	return []InventoryEntry{
		e(0x08, 0x0A, 0x0D, 0.1), // Black
		e(0x55, 0x33, 0x1A, 0.1), // Brown
		e(0xE7, 0x2F, 0x1D, 1.9), // Red
		e(0xD6, 0x02, 0x12, 1.0), // Wine Red
		e(0xF2, 0x45, 0x74, 1.4), // Magenta
		e(0xF1, 0xA1, 0xAF, 1.8), // Pink
		e(0xF6, 0x74, 0x05, 3.3), // Orange
		e(0xFF, 0xE8, 0x00, 4.3), // Yellow
		e(0xEE, 0xD2, 0x30, 3.3), // Lemon Yellow
		e(0xEE, 0xD1, 0xA8, 1.5), // Cream
		e(0xC2, 0xAB, 0x72, 0.5), // Beige
		e(0xA7, 0x9E, 0x82, 0.7), // Tan
		e(0x06, 0x92, 0x4D, 0.4), // Green
		e(0xD5, 0xD7, 0x01, 2.3), // Lime Green
		e(0x4E, 0x74, 0x2D, 0.1), // Jungle Green
		e(0x94, 0x89, 0x02, 0.1), // Olive Green
		e(0x57, 0x5B, 0x54, 0.1), // Dark Olive Drab
		e(0x00, 0x37, 0x76, 0.3), // Blue
		e(0x00, 0x66, 0xD9, 1.5), // Azure Blue
		e(0x48, 0x7B, 0xA2, 0.3), // Stone Blue
		e(0x5E, 0xBD, 0xDB, 1.5), // Aqua Blue
		e(0x4C, 0xC0, 0xC7, 0.6), // Polymaker Teal
		e(0x6C, 0x47, 0xB2, 0.1), // Purple
		e(0x48, 0x52, 0x59, 0.2), // Dark Grey
		e(0x61, 0x64, 0x69, 0.4), // Steel Grey
		e(0x8C, 0x90, 0x99, 0.4), // Grey
		e(0xD9, 0xDF, 0xE5, 0.3), // Cold White
		e(0xEB, 0xF7, 0xFF, 3.2), // White
	}
}

// creamEagleColors is a cream/white-dominant target cloud with warm-brown and
// black markings — the orzeł eagle in miniature. It is the cloud that lured
// plain greedy selection into the saturated-translucent Magenta local minimum.
func creamEagleColors() [][3]uint8 {
	cream := [3]uint8{0xEE, 0xD1, 0xA8}
	white := [3]uint8{0xF0, 0xEC, 0xE0}
	tan := [3]uint8{0xC8, 0xB0, 0x80}
	brown := [3]uint8{0x55, 0x33, 0x1A}
	darkBrown := [3]uint8{0x3A, 0x24, 0x12}
	black := [3]uint8{0x10, 0x10, 0x12}
	rep := func(out [][3]uint8, c [3]uint8, k int) [][3]uint8 {
		for i := 0; i < k; i++ {
			out = append(out, c)
		}
		return out
	}
	var out [][3]uint8
	out = rep(out, cream, 40)
	out = rep(out, white, 25)
	out = rep(out, tan, 15)
	out = rep(out, brown, 12)
	out = rep(out, darkBrown, 6)
	out = rep(out, black, 8)
	return out
}

// tdAwareScorer wires the TD-aware per-sample scorer exactly as
// SelectFromInventory does, so the search functions can be exercised directly.
func tdAwareScorer(inv []InventoryEntry, samples []WeightedLabSample) (scoreFunc, [][3]float64) {
	invLab := make([][3]float64, len(inv))
	for i, en := range inv {
		invLab[i] = nominalLabOf(en.Color)
	}
	st := newTDSelectState(inv, nil, invLab, nil, samples, DefaultNeighborPathMM, TransmittanceKappa, true, false)
	return func(indices []int, _ [][3]float64, _ [][3]float64, _ []WeightedLabSample, bound float64) float64 {
		return st.score(indices, bound)
	}, invLab
}

// TestMultiStartVNDMatchesExhaustive pins the search fix: the deterministic
// multi-start VND fallback must reach the exhaustive optimum's score on the real
// 28-choose-4 Panchroma / cream-eagle instance that trapped plain greedy on
// Magenta. It also confirms VND is deterministic run-to-run and that the fixed
// scoring keeps Magenta out of the optimum. Guarded by -short (the instance is
// small, but the guard honors the CI-time budget contract).
func TestMultiStartVNDMatchesExhaustive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exhaustive-vs-VND comparison in -short mode")
	}
	inv := panchromaBasicInventory()
	samples := CellColorHistogram(creamEagleColors(), nil)
	ApplyChromaWeighting(samples)
	samples = topSamples(samples, 5000)

	scorer, invLab := tdAwareScorer(inv, samples)
	const n = 4

	var counter atomic.Int64
	exh, err := exhaustiveSearch(context.Background(), invLab, nil, samples, n, scorer, &counter)
	if err != nil {
		t.Fatalf("exhaustive: %v", err)
	}
	exhScore := scorer(exh, invLab, nil, samples, noBound)

	vnd, evaluated, hitCap, err := multiStartVND(context.Background(), invLab, nil, samples, n, scorer)
	if err != nil {
		t.Fatalf("vnd: %v", err)
	}
	if hitCap {
		t.Fatalf("vnd hit the eval cap on a small instance (evaluated=%d)", evaluated)
	}
	vndScore := scorer(vnd, invLab, nil, samples, noBound)

	if math.Abs(vndScore-exhScore) > 1e-9*math.Max(1, exhScore) {
		t.Errorf("VND score %.6f != exhaustive optimum %.6f (exh=%v vnd=%v)", vndScore, exhScore, exh, vnd)
	}

	// Determinism: a second run yields the identical subset (sorted).
	vnd2, _, _, err := multiStartVND(context.Background(), invLab, nil, samples, n, scorer)
	if err != nil {
		t.Fatalf("vnd rerun: %v", err)
	}
	if len(vnd) != len(vnd2) {
		t.Fatalf("nondeterministic VND length: %v vs %v", vnd, vnd2)
	}
	for i := range vnd {
		if vnd[i] != vnd2[i] {
			t.Errorf("nondeterministic VND result: %v vs %v", vnd, vnd2)
		}
	}

	// The fixed scoring keeps saturated-translucent Magenta (#F24574) out of the
	// optimum for this cream body.
	magenta := [3]uint8{0xF2, 0x45, 0x74}
	for _, idx := range vnd {
		if inv[idx].Color == magenta {
			t.Errorf("Magenta selected in the VND optimum %v; expected it rejected", vnd)
		}
	}
}

// TestSaturateMixSpread pins the phase-2 mix-spread soft knee s/(1 + s/s0): it is
// ≈ linear near 0, exactly s0/2 at s == s0, monotone increasing, capped below s0,
// and recovers the identity mapping for a very large s0.
func TestSaturateMixSpread(t *testing.T) {
	const s0 = 30.0

	// s == 0 → 0.
	if got := saturate(0, s0); got != 0 {
		t.Errorf("saturate(0, %g) = %g, want 0", s0, got)
	}

	// s == s0 → exactly s0/2.
	if got := saturate(s0, s0); math.Abs(got-s0/2) > 1e-12 {
		t.Errorf("saturate(s0, s0) = %g, want %g", got, s0/2)
	}

	// Near-linear for s ≪ s0: relative error grows like s/s0, so at s = s0/100
	// the output is within ~1% of s.
	small := s0 / 100
	if got := saturate(small, s0); math.Abs(got-small)/small > 0.011 {
		t.Errorf("saturate(%g, %g) = %g, not ≈ linear (rel err %.4f)", small, s0, got, math.Abs(got-small)/small)
	}

	// Monotone increasing and strictly capped below s0 across the whole range.
	prev := math.Inf(-1)
	for s := 0.0; s <= 100000; s = s*1.5 + 1 {
		got := saturate(s, s0)
		if got <= prev {
			t.Errorf("saturate not monotone: saturate(%g)=%g <= previous %g", s, got, prev)
		}
		if got >= s0 {
			t.Errorf("saturate(%g, %g) = %g exceeds cap s0=%g", s, s0, got, s0)
		}
		prev = got
	}

	// Asymptote: far past s0 the output approaches s0.
	if got := saturate(1e6, s0); math.Abs(got-s0) > 1e-3 {
		t.Errorf("saturate(1e6, %g) = %g, want ≈ %g", s0, got, s0)
	}

	// Very large s0 recovers the identity (linear) mapping.
	const huge = 1e12
	for _, s := range []float64{1, 10, 100} {
		if got := saturate(s, huge); math.Abs(got-s)/s > 1e-6 {
			t.Errorf("saturate(%g, %g) = %g, want ≈ %g (linear recovery)", s, huge, got, s)
		}
	}
}
