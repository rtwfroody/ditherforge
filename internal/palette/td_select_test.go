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

// TestSelectUniformTDBitIdentical: when every filament shares one TD, TD-aware
// selection must produce exactly the same result as nominal selection — a
// uniform shift toward infill can't change which subset scores best, and the
// downgrade routes both through the identical nominal code path.
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

	nominal, err := SelectFromInventory(context.Background(), samples, nil, inv, 4, nil, true, TDParams{}, progress.NullTracker{})
	if err != nil {
		t.Fatalf("nominal select: %v", err)
	}
	tdAware, err := SelectFromInventory(context.Background(), samples, nil, inv, 4, nil, true,
		TDParams{Enabled: true, LayerHeightMM: 0.08, ShellThicknessMM: 0.84}, progress.NullTracker{})
	if err != nil {
		t.Fatalf("td-aware select: %v", err)
	}
	if len(nominal) != len(tdAware) {
		t.Fatalf("length mismatch: nominal %d, td-aware %d", len(nominal), len(tdAware))
	}
	for i := range nominal {
		if nominal[i].Color != tdAware[i].Color {
			t.Errorf("entry %d differs: nominal %v, td-aware %v", i, nominal[i].Color, tdAware[i].Color)
		}
	}
}

// TestSelectTDAwareGrayEagle is the gray-eagle regression in miniature: a dark
// warm-brown body over ~50% near-gray coverage. Nominal scoring anchors the
// dark end on opaque Black and leans on translucent Orange (TD 3.3) for warmth,
// leaving opaque Brown out entirely. TD-aware scoring composites every filament
// toward the area-weighted mean of the target colors under the transmittance
// model (β = 10^(−ℓ/TD), the neighbor deviation filtered by the filament's own
// hue T = (C/max C)^κ) before scoring, with the shipped calibration
// ℓ = DefaultNeighborPathMM = 0.130 mm and κ = TransmittanceKappa = 3.04. The
// effective picture then wants opaque Brown (β = 0, its full sienna survives) as
// the dark warm anchor, so Brown enters the palette (displacing Black) exactly
// where nominal scoring never would.
//
// The recorded outcome (ℓ = 0.130 mm, κ = 3.04 — the shipped model; the
// load-bearing Brown/Black swap is unchanged from the earlier ℓ = 0.3, κ = 0
// additive model):
//
//	nominal:  Black SteelGrey Orange Cream
//	td-aware: Black Brown SteelGrey ColdWhite
//
// Two things move under the neighbor model. Brown ENTERS as the opaque warm
// anchor (the load-bearing fix — nominal can't justify opaque Brown, TD-aware
// does), and Cream → ColdWhite on the light anchor. Opaque Black stays as the
// dark anchor.
//
// Orange (TD 3.3, β ≈ 0.81 — the MOST translucent filament here) is REJECTED:
// the wash-reach penalty is charged on hull MEMBERSHIP (barycentric over the
// enclosing simplex), so a translucent filament pays for the coverage it
// provides even when it is never the nearest vertex. A solid orange region reads
// orange, not the warm brown its eff washes toward, so opaque Black + Brown win
// the two dark/warm anchors outright. This is the same anti-chameleon reach
// penalty that keeps a saturated translucent Magenta out of a cream body (see
// tdSelectState.score). Under the earlier nearest-vertex-only wash Orange
// displaced Black — it fake-enclosed the warm body for free because it was
// rarely the nearest vertex and so was never charged for the reach.
func TestSelectTDAwareGrayEagle(t *testing.T) {
	black := [3]uint8{0x08, 0x0A, 0x0D}
	brown := [3]uint8{0x55, 0x33, 0x1A}
	steel := [3]uint8{0x61, 0x64, 0x69}
	orange := [3]uint8{0xF6, 0x74, 0x05}
	cream := [3]uint8{0xEE, 0xD1, 0xA8}
	white := [3]uint8{0xD9, 0xDF, 0xE5}
	inv := []InventoryEntry{
		{Color: black, TD: 0.1},
		{Color: brown, TD: 0.1},
		{Color: steel, TD: 0.4},
		{Color: orange, TD: 3.3},
		{Color: cream, TD: 1.5},
		{Color: white, TD: 0.3},
	}
	samples := grayEagleSamples()

	nominal, err := SelectFromInventory(context.Background(), samples, nil, inv, 4, nil, true, TDParams{}, progress.NullTracker{})
	if err != nil {
		t.Fatalf("nominal select: %v", err)
	}
	tdAware, err := SelectFromInventory(context.Background(), samples, nil, inv, 4, nil, true,
		TDParams{Enabled: true, LayerHeightMM: 0.08, ShellThicknessMM: 0.84, Kappa: TransmittanceKappa}, progress.NullTracker{})
	if err != nil {
		t.Fatalf("td-aware select: %v", err)
	}

	t.Logf("nominal:  %s", fmtSel(nominal))
	t.Logf("td-aware: %s", fmtSel(tdAware))

	// Exact TD-aware selection under the neighbor model (see docstring): opaque
	// Black + Brown anchor the dark/warm ends, SteelGrey the greys, ColdWhite the
	// light end.
	for _, c := range [][3]uint8{black, brown, steel, white} {
		if !hasColor(tdAware, c) {
			t.Errorf("td-aware selection missing %s; got %s", hexOf(c), fmtSel(tdAware))
		}
	}
	// Pin the exact set: the hull-membership reach penalty rejects the most
	// translucent filament (Orange) in favour of opaque Brown, and Cream →
	// ColdWhite on the light anchor.
	if hasColor(tdAware, orange) {
		t.Errorf("td-aware should reject translucent Orange for opaque Brown; got %s", fmtSel(tdAware))
	}
	if hasColor(tdAware, cream) {
		t.Errorf("td-aware should drop Cream for ColdWhite; got %s", fmtSel(tdAware))
	}

	// The test only discriminates if the two paths actually diverge — that is
	// the whole point of the feature. Nominal must omit Brown (it anchors the
	// dark end on opaque Black) while still reaching for translucent Orange.
	if hasColor(nominal, brown) {
		t.Errorf("test does not discriminate: nominal already picks Brown; got %s", fmtSel(nominal))
	}
	if !hasColor(nominal, black) {
		t.Errorf("expected nominal to anchor the dark end on opaque Black; got %s", fmtSel(nominal))
	}
	if !hasColor(nominal, orange) {
		t.Errorf("expected nominal to pick translucent Orange; got %s", fmtSel(nominal))
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

	st := newTDSelectState(inv, nil, invLab, nil, samples, DefaultNeighborPathMM, TransmittanceKappa, true)

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
	st := newTDSelectState(inv, nil, invLab, nil, samples, DefaultNeighborPathMM, TransmittanceKappa, true)
	return func(indices []int, _ [][3]float64, _ [][3]float64, _ []WeightedLabSample) float64 {
		return st.score(indices)
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
	exhScore := scorer(exh, invLab, nil, samples)

	vnd, evaluated, hitCap, err := multiStartVND(context.Background(), invLab, nil, samples, n, scorer)
	if err != nil {
		t.Fatalf("vnd: %v", err)
	}
	if hitCap {
		t.Fatalf("vnd hit the eval cap on a small instance (evaluated=%d)", evaluated)
	}
	vndScore := scorer(vnd, invLab, nil, samples)

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
