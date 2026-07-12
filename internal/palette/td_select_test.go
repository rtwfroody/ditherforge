package palette

import (
	"context"
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/progress"
)

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
// toward the area-weighted mean of the target colors by its lateral leak β (the
// neighbor model: β = 10^(−ℓ/TD), ℓ = DefaultNeighborPathMM = 0.3 mm) before
// scoring. The effective picture then wants opaque Brown (β = 0, its full sienna
// survives) as the dark warm anchor, so Brown enters the palette (displacing
// Black) exactly where nominal scoring never would.
//
// The recorded outcome (ℓ = 0.3 mm, the calibrated default):
//
//	nominal:  Black  SteelGrey Orange Cream
//	td-aware: Brown  SteelGrey Orange ColdWhite
//
// Two colors move under the neighbor model: Black → Brown on the opaque dark
// anchor (the load-bearing fix — nominal can't justify opaque Brown, TD-aware
// does), and Cream → ColdWhite on the light anchor.
//
// Orange (TD 3.3, β ≈ 0.81) SURVIVES here even though it barely delivers its own
// color, because this cloud is ~50% warm brown: a translucent orange cell washes
// toward its warm neighbors and still reads warm. That is exactly the effect the
// neighbor model captures and the old infill-composite model (orange over black
// infill → muddy gray) got wrong — whether Orange survives is target-dependent,
// so the stable, load-bearing claim remains the opaque Brown/Black swap.
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
		TDParams{Enabled: true, LayerHeightMM: 0.08, ShellThicknessMM: 0.84}, progress.NullTracker{})
	if err != nil {
		t.Fatalf("td-aware select: %v", err)
	}

	t.Logf("nominal:  %s", fmtSel(nominal))
	t.Logf("td-aware: %s", fmtSel(tdAware))

	// Exact TD-aware selection under the neighbor model (see docstring).
	for _, c := range [][3]uint8{brown, steel, orange, white} {
		if !hasColor(tdAware, c) {
			t.Errorf("td-aware selection missing %s; got %s", hexOf(c), fmtSel(tdAware))
		}
	}
	// Pin the exact set: the two colors the neighbor model displaces must be
	// gone (Black → Brown on the dark anchor, Cream → ColdWhite on the light).
	if hasColor(tdAware, black) {
		t.Errorf("td-aware should drop Black for opaque Brown; got %s", fmtSel(tdAware))
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
