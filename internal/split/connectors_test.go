package split

import (
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// TestPolylabel_Square — pole of a unit square at origin should be at
// the centre with distance 0.5.
func TestPolylabel_Square(t *testing.T) {
	square := []pt2{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	pole, dist := poleOfInaccessibility(square, nil, 0.001)
	if math.Abs(pole.X-0.5) > 0.01 || math.Abs(pole.Y-0.5) > 0.01 {
		t.Errorf("pole=%+v, want ≈(0.5, 0.5)", pole)
	}
	if math.Abs(dist-0.5) > 0.01 {
		t.Errorf("dist=%g, want ≈0.5", dist)
	}
}

// TestPolylabel_LShape — pole of an L-shape should land near the
// inner concave corner where the largest inscribed circle fits
// (touching the inner corner and two outer edges).
func TestPolylabel_LShape(t *testing.T) {
	// L-shape: 4×4 outer with a 2×2 cut from the upper-right corner.
	L := []pt2{
		{0, 0}, {4, 0}, {4, 2}, {2, 2}, {2, 4}, {0, 4},
	}
	pole, dist := poleOfInaccessibility(L, nil, 0.01)
	// The largest inscribed circle touches the bottom edge (y=0),
	// the left edge (x=0), and the inner corner (2, 2). Centred at
	// (a, a) with radius a = sqrt(2)·(2−a) → a = 4−2·sqrt(2) ≈ 1.17.
	want := 4 - 2*math.Sqrt(2)
	if math.Abs(dist-want) > 0.05 {
		t.Errorf("dist=%g, want ≈%g", dist, want)
	}
	if pole.X < 0 || pole.Y < 0 || (pole.X > 2 && pole.Y > 2) {
		t.Errorf("pole=%+v, want inside L-shape", pole)
	}
}

// TestCut_DowelHoles — cube cut at z=0.5 with one dowel-style
// connector (4mm diameter, 5mm depth, 0.15mm clearance). Each half
// should have a closed pocket cavity along the cap. Both halves
// remain watertight; volumes change by exactly the pocket volume.
func TestCut_DowelHoles(t *testing.T) {
	// Use a 50mm cube to give polylabel reasonable headroom.
	verts := [][3]float32{
		{0, 0, 0}, {50, 0, 0}, {50, 50, 0}, {0, 50, 0},
		{0, 0, 50}, {50, 0, 50}, {50, 50, 50}, {0, 50, 50},
	}
	faces := [][3]uint32{
		{0, 2, 1}, {0, 3, 2},
		{4, 5, 6}, {4, 6, 7},
		{0, 1, 5}, {0, 5, 4},
		{2, 3, 7}, {2, 7, 6},
		{0, 4, 7}, {0, 7, 3},
		{1, 2, 6}, {1, 6, 5},
	}
	cube := &loader.LoadedModel{Vertices: verts, Faces: faces}

	settings := ConnectorSettings{
		Style:       Dowels,
		Count:       1,
		DiamMM:      4,
		DepthMM:     5,
		ClearanceMM: 0.15,
	}
	res, err := Cut(cube, AxisPlane(2, 25), settings)
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	for h := 0; h < 2; h++ {
		assertWatertight(t, res.Halves[h], "dowel half "+string(rune('0'+h)))
	}
	// Each half should have lost approximately π × 2.15² × 5 ≈ 72.6 mm³,
	// minus a small ~2% under-approximation from the 16-segment circle.
	r := 4.0/2 + 0.15
	pocketArea := 8 * r * r * math.Sin(math.Pi/8) // 16-segment polygon area
	wantHalfVol := 50.0*50.0*25 - pocketArea*5
	for h := 0; h < 2; h++ {
		v := math.Abs(closedMeshVolume(res.Halves[h]))
		if math.Abs(v-wantHalfVol)/wantHalfVol > 0.001 {
			t.Errorf("dowel half %d: volume %g, want ≈ %g", h, v, wantHalfVol)
		}
	}
}

// TestCut_PegConnector — same cube, but with a peg/pocket pair. Half 0
// gains the peg volume; half 1 loses the (clearance-sized) pocket
// volume. Both halves stay watertight.
func TestCut_PegConnector(t *testing.T) {
	verts := [][3]float32{
		{0, 0, 0}, {50, 0, 0}, {50, 50, 0}, {0, 50, 0},
		{0, 0, 50}, {50, 0, 50}, {50, 50, 50}, {0, 50, 50},
	}
	faces := [][3]uint32{
		{0, 2, 1}, {0, 3, 2},
		{4, 5, 6}, {4, 6, 7},
		{0, 1, 5}, {0, 5, 4},
		{2, 3, 7}, {2, 7, 6},
		{0, 4, 7}, {0, 7, 3},
		{1, 2, 6}, {1, 6, 5},
	}
	cube := &loader.LoadedModel{Vertices: verts, Faces: faces}

	settings := ConnectorSettings{
		Style:       Pegs,
		Count:       1,
		DiamMM:      4,
		DepthMM:     5,
		ClearanceMM: 0.15,
	}
	res, err := Cut(cube, AxisPlane(2, 25), settings)
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	for h := 0; h < 2; h++ {
		assertWatertight(t, res.Halves[h], "peg half "+string(rune('0'+h)))
	}
	pegR := 4.0 / 2
	pocketR := pegR + 0.15
	pegArea := 8 * pegR * pegR * math.Sin(math.Pi/8)
	pocketArea := 8 * pocketR * pocketR * math.Sin(math.Pi/8)
	wantHalf0 := 50.0*50.0*25 + pegArea*5    // half 0 grows
	wantHalf1 := 50.0*50.0*25 - pocketArea*5 // half 1 shrinks
	v0 := math.Abs(closedMeshVolume(res.Halves[0]))
	v1 := math.Abs(closedMeshVolume(res.Halves[1]))
	if math.Abs(v0-wantHalf0)/wantHalf0 > 0.001 {
		t.Errorf("peg half 0 (male): volume %g, want ≈ %g", v0, wantHalf0)
	}
	if math.Abs(v1-wantHalf1)/wantHalf1 > 0.001 {
		t.Errorf("peg half 1 (female): volume %g, want ≈ %g", v1, wantHalf1)
	}
}

// TestCut_NoConnectors — sanity check that the new ConnectorSettings
// parameter doesn't change behavior when Style==NoConnectors.
func TestCut_NoConnectors(t *testing.T) {
	cube := makeUnitCube()
	res, err := Cut(cube, AxisPlane(2, 0.5), ConnectorSettings{})
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	for h := 0; h < 2; h++ {
		assertWatertight(t, res.Halves[h], "noconn half "+string(rune('0'+h)))
		v := math.Abs(closedMeshVolume(res.Halves[h]))
		if math.Abs(v-0.5) > 1e-5 {
			t.Errorf("half %d volume %g, want 0.5", h, v)
		}
	}
}

// TestCut_ConnectorTooSmallDeclined — when the cap polygon is too
// small for even one connector with margin, none are placed and the
// halves are plain capped meshes.
func TestCut_ConnectorTooSmallDeclined(t *testing.T) {
	cube := makeUnitCube() // 1mm × 1mm × 1mm
	settings := ConnectorSettings{
		Style:       Dowels,
		Count:       1,
		DiamMM:      4,  // too big for a 1mm cube
		DepthMM:     5,
		ClearanceMM: 0.15,
	}
	res, err := Cut(cube, AxisPlane(2, 0.5), settings)
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	// Should fall back to plain caps; volumes ≈ 0.5 each.
	for h := 0; h < 2; h++ {
		v := math.Abs(closedMeshVolume(res.Halves[h]))
		if math.Abs(v-0.5) > 1e-3 {
			t.Errorf("half %d volume %g, want 0.5 (no connectors fit)", h, v)
		}
	}
}

// TestCut_AutoConnectorCount — auto count produces a single
// connector for now. Phase 2 caps multi-connector to 1 due to the
// bridge-spike issue noted in placeConnectors; phase 2.5 will lift
// this cap.
func TestCut_AutoConnectorCount(t *testing.T) {
	verts := [][3]float32{
		{0, 0, 0}, {50, 0, 0}, {50, 50, 0}, {0, 50, 0},
		{0, 0, 50}, {50, 0, 50}, {50, 50, 50}, {0, 50, 50},
	}
	faces := [][3]uint32{
		{0, 2, 1}, {0, 3, 2}, {4, 5, 6}, {4, 6, 7},
		{0, 1, 5}, {0, 5, 4}, {2, 3, 7}, {2, 7, 6},
		{0, 4, 7}, {0, 7, 3}, {1, 2, 6}, {1, 6, 5},
	}
	cube := &loader.LoadedModel{Vertices: verts, Faces: faces}
	settings := ConnectorSettings{
		Style:       Dowels,
		Count:       0, // auto → currently always 1
		DiamMM:      4,
		DepthMM:     5,
		ClearanceMM: 0.15,
	}
	res, err := Cut(cube, AxisPlane(2, 25), settings)
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	r := 4.0/2 + 0.15
	pocketArea := 8 * r * r * math.Sin(math.Pi/8)
	for h := 0; h < 2; h++ {
		base := 62500.0
		v := math.Abs(closedMeshVolume(res.Halves[h]))
		nPockets := math.Round((base - v) / (pocketArea * 5))
		if nPockets != 1 {
			t.Errorf("half %d: deduced %d connectors, want 1 (phase 2 cap)", h, int(nPockets))
		}
	}
}
