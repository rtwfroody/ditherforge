package voxel

import (
	"context"
	"testing"
)

// gridCells builds a w×h grid of cells all set to target color `col`,
// with a 4-neighbor adjacency graph (face weight 1.0). Area = 1 each.
func gridCells(w, h int, col [3]uint8) ([]ActiveCell, [][]Neighbor) {
	n := w * h
	cells := make([]ActiveCell, n)
	neighbors := make([][]Neighbor, n)
	at := func(x, y int) int { return y*w + x }
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := at(x, y)
			cells[i] = ActiveCell{Col: x, Row: y, Cx: float32(x), Cy: float32(y), Color: col, Area: 1}
			var nb []Neighbor
			if x > 0 {
				nb = append(nb, Neighbor{Idx: at(x-1, y), Weight: 1})
			}
			if x < w-1 {
				nb = append(nb, Neighbor{Idx: at(x+1, y), Weight: 1})
			}
			if y > 0 {
				nb = append(nb, Neighbor{Idx: at(x, y-1), Weight: 1})
			}
			if y < h-1 {
				nb = append(nb, Neighbor{Idx: at(x, y+1), Weight: 1})
			}
			neighbors[i] = nb
		}
	}
	return cells, neighbors
}

func fracAssignedTo(assigns []int32, idx int32) float64 {
	c := 0
	for _, a := range assigns {
		if a == idx {
			c++
		}
	}
	return float64(c) / float64(len(assigns))
}

// The neighbor/κ calibration used by the dither model in these tests (mirrors
// palette.DefaultNeighborPathMM / palette.TransmittanceKappa; kept local so the
// voxel package's tests don't depend on the palette constants).
const (
	testDitherEll   = 0.130
	testDitherKappa = 3.04
)

// TestDitherModelOpaqueIsClassic: an opaque DitherModel (honorTD = false) and a
// nil model both drive the classic nearest-color dither, byte-for-byte. This is
// the identity guarantee that keeps the default (non-HonorTD) pipeline unchanged
// by the transmittance port.
func TestDitherModelOpaqueIsClassic(t *testing.T) {
	target := [3]uint8{90, 160, 70}
	pal := [][3]uint8{{0, 0, 0}, {255, 255, 255}, {0, 255, 0}, {255, 0, 0}}
	tds := []float32{0.1, 0.3, 0.5, 3.3}
	cells, neighbors := gridCells(40, 40, target)
	ctx := context.Background()

	opaque := NewDitherModel(pal, tds, testDitherEll, testDitherKappa, false) // honorTD off → opaque
	viaModel, err := DitherWithNeighbors(ctx, cells, pal, opaque, neighbors, nil)
	if err != nil {
		t.Fatalf("opaque-model dither: %v", err)
	}
	viaNil, err := DitherWithNeighbors(ctx, cells, pal, nil, neighbors, nil)
	if err != nil {
		t.Fatalf("nil-model dither: %v", err)
	}
	for i := range viaNil {
		if viaModel[i] != viaNil[i] {
			t.Fatalf("opaque model differs from nil (classic) at cell %d: %d vs %d", i, viaModel[i], viaNil[i])
		}
	}
}

// TestDitherModelExactTargetMatches guards the swatch-export property: for a
// two-filament pair, a cell whose color is exactly filament A must be assigned A
// — eff(A, C_A) = C_A, so exact targets still match exactly even under the
// transmittance decision, translucent or not.
func TestDitherModelExactTargetMatches(t *testing.T) {
	pal := [][3]uint8{{0x08, 0x0A, 0x0D}, {0xF6, 0x74, 0x05}} // Black (A), Orange (B, translucent)
	tds := []float32{0.1, 3.3}
	for _, honor := range []bool{false, true} {
		m := NewDitherModel(pal, tds, testDitherEll, testDitherKappa, honor)
		for want, c := range pal {
			r := srgbToLinearLUT[c[0]]
			g := srgbToLinearLUT[c[1]]
			b := srgbToLinearLUT[c[2]]
			got, _, _, _ := m.choose(r, g, b, r, g, b) // N = t: zero-assigned-neighbors case
			if got != want {
				t.Errorf("honorTD=%v: exact target %v chose %d, want %d", honor, c, got, want)
			}
		}
	}
}

// TestTransmittanceDitherWiredAllModes: with a translucent filament in play the
// transmittance model must actually change what each production dither mode
// places (vs the opaque model), i.e. the model is wired into every mode. Uses an
// orange target over {red, translucent-yellow}: the printed appearance of the
// translucent yellow differs from its nominal, so the decision must shift.
func TestTransmittanceDitherWiredAllModes(t *testing.T) {
	orange := [3]uint8{255, 140, 0}
	pal := [][3]uint8{{255, 0, 0}, {255, 255, 0}} // red (opaque), yellow (translucent)
	tds := []float32{0.1, 3.3}
	cells, neighbors := gridCells(48, 48, orange)
	ctx := context.Background()

	opaque := NewDitherModel(pal, tds, testDitherEll, testDitherKappa, false)
	tmodel := NewDitherModel(pal, tds, testDitherEll, testDitherKappa, true)

	modes := []struct {
		name string
		run  func(m *DitherModel) ([]int32, error)
	}{
		{"dlc", func(m *DitherModel) ([]int32, error) {
			return DitherLocalCorrectedTuned(ctx, cells, pal, m, neighbors, nil, 0.3, 7)
		}},
		{"floyd-steinberg", func(m *DitherModel) ([]int32, error) {
			return FloydSteinberg(ctx, cells, pal, m, neighbors, nil)
		}},
		{"riemersma", func(m *DitherModel) ([]int32, error) {
			return Riemersma(ctx, cells, pal, m, neighbors, RiemersmaInputBiasDefault, nil)
		}},
	}
	for _, mode := range modes {
		base, err := mode.run(opaque)
		if err != nil {
			t.Fatalf("%s opaque: %v", mode.name, err)
		}
		tr, err := mode.run(tmodel)
		if err != nil {
			t.Fatalf("%s transmittance: %v", mode.name, err)
		}
		changed := 0
		for i := range base {
			if base[i] != tr[i] {
				changed++
			}
		}
		by, ty := fracAssignedTo(base, 1), fracAssignedTo(tr, 1)
		t.Logf("%-16s yellow frac: opaque=%.3f transmittance=%.3f (%d/%d cells changed)",
			mode.name, by, ty, changed, len(base))
		if changed == 0 {
			t.Errorf("%s: transmittance model changed nothing — not wired in", mode.name)
		}
		if by <= 0 || by >= 1 {
			t.Errorf("%s: opaque baseline degenerate (yellow frac %.3f) — not dithering", mode.name, by)
		}
	}
}

// TestFloydSteinbergTransmittanceStable is the regression guard for the FS × TD
// instability class (the old opacity-mass diffusion exploded to ~1e8 under FS's
// deterministic scan order and collapsed whole walls to one color). The
// transmittance model diffuses the residual t − eff(chosen) = (1−β·T)∘(t − C),
// which is SMALLER than the classic t − C (never larger), so FS stays bounded.
// On a uniform brick-orange field with a real translucent palette no color may
// dominate (>65%) and the output must be a genuine dither.
func TestFloydSteinbergTransmittanceStable(t *testing.T) {
	pal := [][3]uint8{
		{0xF6, 0x74, 0x05}, // Orange
		{0xD6, 0x02, 0x12}, // Wine Red
		{0x61, 0x64, 0x69}, // Steel Grey
		{0xEE, 0xD1, 0xA8}, // Cream
	}
	tds := []float32{3.3, 1.0, 0.4, 1.5}
	cells, neighbors := gridCells(64, 64, [3]uint8{180, 90, 60})
	ctx := context.Background()

	m := NewDitherModel(pal, tds, testDitherEll, testDitherKappa, true)
	withTD, err := FloydSteinberg(ctx, cells, pal, m, neighbors, nil)
	if err != nil {
		t.Fatalf("FS+transmittance: %v", err)
	}
	nonEmpty := 0
	for i := range pal {
		f := fracAssignedTo(withTD, int32(i))
		t.Logf("FS+transmittance palette[%d] fraction: %.3f", i, f)
		if f > 0.65 {
			t.Errorf("FS+transmittance collapsed: palette[%d] got %.1f%% of cells", i, 100*f)
		}
		if f > 0 {
			nonEmpty++
		}
	}
	if nonEmpty < 2 {
		t.Errorf("FS+transmittance not dithering: only %d colors used", nonEmpty)
	}
}

// TestNeighborhoodZeroAssignedMatchesTargetModel: with no assigned neighbors the
// realized neighborhood N_i equals the target t, so choose()/effResidual under N
// must equal the old self-consistent model (N implicit = t). Invariant 2.
func TestNeighborhoodZeroAssignedMatchesTargetModel(t *testing.T) {
	pal := [][3]uint8{{0x08, 0x0A, 0x0D}, {0xF6, 0x74, 0x05}, {0x61, 0x64, 0x69}}
	tds := []float32{0.1, 3.3, 0.4}
	m := NewDitherModel(pal, tds, testDitherEll, testDitherKappa, true)
	if !m.td {
		t.Fatal("expected a translucent model for this test")
	}
	// A handful of arbitrary linear-light targets.
	targets := [][3]float32{
		{0.3, 0.2, 0.1}, {0.7, 0.5, 0.2}, {0.05, 0.05, 0.05}, {0.9, 0.9, 0.8},
	}
	for _, tr := range targets {
		r, g, b := tr[0], tr[1], tr[2]
		// Zero assigned neighbors: neighborhood() returns t.
		nbrs := []Neighbor{{Idx: 0, Weight: 1}, {Idx: 1, Weight: 1}}
		processed := []bool{false, false, false}
		assigns := []int32{0, 0, 0}
		areas := []float32{1, 1, 1}
		nR, nG, nB := m.neighborhood(r, g, b, nbrs, assigns, processed, areas)
		if nR != r || nG != g || nB != b {
			t.Fatalf("zero-assigned neighborhood %v,%v,%v != target %v,%v,%v", nR, nG, nB, r, g, b)
		}
		// choose under N=t must equal the manual self-consistent decision.
		gotIdx, eR, eG, eB := m.choose(r, g, b, nR, nG, nB)
		// Manual: pick min ΔE(eff(i,t), t); residual = t − eff(chosen,t).
		wantIdx := 0
		bestD := float32(1e30)
		tL, tA, tB := linearToLab(r, g, b)
		for i := range pal {
			gi := m.gain[i]
			er := m.lin[i][0] + gi[0]*(r-m.lin[i][0])
			eg := m.lin[i][1] + gi[1]*(g-m.lin[i][1])
			eb := m.lin[i][2] + gi[2]*(b-m.lin[i][2])
			el, ea, ebb := linearToLab(er, eg, eb)
			d := (tL-el)*(tL-el) + (tA-ea)*(tA-ea) + (tB-ebb)*(tB-ebb)
			if d < bestD {
				bestD = d
				wantIdx = i
			}
		}
		if gotIdx != wantIdx {
			t.Errorf("target %v: choose idx %d, want %d", tr, gotIdx, wantIdx)
		}
		gi := m.gain[wantIdx]
		wantR := (1 - gi[0]) * (r - m.lin[wantIdx][0])
		wantG := (1 - gi[1]) * (g - m.lin[wantIdx][1])
		wantB := (1 - gi[2]) * (b - m.lin[wantIdx][2])
		if !approxEq(eR, wantR) || !approxEq(eG, wantG) || !approxEq(eB, wantB) {
			t.Errorf("target %v: residual (%v,%v,%v) != self-consistent (%v,%v,%v)",
				tr, eR, eG, eB, wantR, wantG, wantB)
		}
	}
}

func approxEq(a, b float32) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-6
}

// TestHighTDYellowNoBulkPlacement is the actual golden-eagle regression: a flat
// mid-gray/tan field with a high-TD saturated yellow plus opaque dark and light.
// Under the old N=t model eff(yellow, t) ≈ t made yellow a chameleon that won in
// bulk; the neighborhood model must keep its share bounded AND the print-sim-
// realized average must land near the target (the yellow reads as its own bright
// color once surrounded by itself). Runs the DEFAULT dlc mode.
func TestHighTDYellowNoBulkPlacement(t *testing.T) {
	// tan-ish mid target.
	target := [3]uint8{150, 140, 110}
	pal := [][3]uint8{
		{0x20, 0x1C, 0x14}, // dark brown (opaque)
		{0xE8, 0xE0, 0xD0}, // light cream (opaque)
		{0xFF, 0xE0, 0x00}, // saturated yellow (high TD → translucent)
	}
	tds := []float32{0.3, 0.4, 3.3}
	cells, neighbors := gridCells(64, 64, target)
	ctx := context.Background()

	m := NewDitherModel(pal, tds, testDitherEll, testDitherKappa, true)
	assigns, err := DitherLocalCorrected(ctx, cells, pal, m, neighbors, nil)
	if err != nil {
		t.Fatalf("dlc: %v", err)
	}
	yellowFrac := fracAssignedTo(assigns, 2)
	t.Logf("high-TD yellow fraction (neighborhood model): %.3f", yellowFrac)
	// The bug had yellow dominating (>60%). Bound it well under a dominating share.
	if yellowFrac > 0.45 {
		t.Errorf("high-TD yellow placed in bulk: %.1f%% of cells (bug not fixed)", 100*yellowFrac)
	}

	// The print-sim-realized average over the field must land near the target:
	// this is what the dither is now honest about.
	eff := EffectiveCellColors(cells, assigns, pal, tds, neighbors, testDitherEll, 4, testDitherKappa)
	var sr, sg, sb float64
	for _, c := range eff {
		sr += float64(srgbToLinearLUT[c[0]])
		sg += float64(srgbToLinearLUT[c[1]])
		sb += float64(srgbToLinearLUT[c[2]])
	}
	n := float64(len(eff))
	avgL, avgA, avgB := linearToLab(float32(sr/n), float32(sg/n), float32(sb/n))
	tL, tA, tB := linearToLab(srgbToLinearLUT[target[0]], srgbToLinearLUT[target[1]], srgbToLinearLUT[target[2]])
	dE := float32Sqrt((avgL-tL)*(avgL-tL) + (avgA-tA)*(avgA-tA) + (avgB-tB)*(avgB-tB))
	t.Logf("print-sim realized average ΔE from target: %.2f", dE)
	if dE > 12 {
		t.Errorf("print-sim realized average ΔE %.2f too far from target (dither dishonest)", dE)
	}
}

func float32Sqrt(x float32) float32 {
	if x <= 0 {
		return 0
	}
	// Newton, plenty for a test tolerance check.
	g := x
	for i := 0; i < 40; i++ {
		g = 0.5 * (g + x/g)
	}
	return g
}

// TestNeighborhoodDitherDeterministic: the neighborhood-context dither must be
// bit-identical across runs (cache-desync hard requirement).
func TestNeighborhoodDitherDeterministic(t *testing.T) {
	target := [3]uint8{150, 140, 110}
	pal := [][3]uint8{{0x20, 0x1C, 0x14}, {0xE8, 0xE0, 0xD0}, {0xFF, 0xE0, 0x00}}
	tds := []float32{0.3, 0.4, 3.3}
	cells, neighbors := gridCells(48, 48, target)
	ctx := context.Background()
	m := NewDitherModel(pal, tds, testDitherEll, testDitherKappa, true)

	runs := map[string]func(*DitherModel) ([]int32, error){
		"dlc": func(mm *DitherModel) ([]int32, error) {
			return DitherLocalCorrected(ctx, cells, pal, mm, neighbors, nil)
		},
		"fs": func(mm *DitherModel) ([]int32, error) {
			return FloydSteinberg(ctx, cells, pal, mm, neighbors, nil)
		},
		"riemersma": func(mm *DitherModel) ([]int32, error) {
			return Riemersma(ctx, cells, pal, mm, neighbors, RiemersmaInputBiasDefault, nil)
		},
	}
	for name, fn := range runs {
		a, err := fn(m)
		if err != nil {
			t.Fatalf("%s run1: %v", name, err)
		}
		b, err := fn(m)
		if err != nil {
			t.Fatalf("%s run2: %v", name, err)
		}
		for i := range a {
			if a[i] != b[i] {
				t.Fatalf("%s nondeterministic at cell %d: %d vs %d", name, i, a[i], b[i])
			}
		}
	}
}
