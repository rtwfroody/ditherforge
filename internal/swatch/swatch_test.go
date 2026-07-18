package swatch

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// testPalette is a small 4-filament palette used across the tests.
var testPalette = []Filament{
	{Hex: "#D9DFE5", TD: 0.3, Label: "Cold White"},
	{Hex: "#C2AB72", TD: 0.5, Label: "Beige"},
	{Hex: "#080A0D", TD: 0.1, Label: "Black"},
	{Hex: "#F67405", TD: 3.3, Label: "Orange"},
}

// TestBayerCoverageExactness verifies that for each nominal coverage p=k/8, a
// full 8x8 Bayer tile contains exactly k*8 filament-B blocks. This is the
// property that makes the endpoint sections exactly solid and the interior
// sections carry a known, even coverage.
func TestBayerCoverageExactness(t *testing.T) {
	for k := 0; k <= 8; k++ {
		count := 0
		for i := 0; i < 8; i++ {
			for j := 0; j < 8; j++ {
				if blockIsB(i, j, k) {
					count++
				}
			}
		}
		want := k * 8
		if count != want {
			t.Errorf("coverage %d/8: got %d B blocks in 8x8 tile, want %d", k, count, want)
		}
	}
}

// TestBayerValuesComplete verifies the matrix holds 0..63 exactly once.
func TestBayerValuesComplete(t *testing.T) {
	var seen [64]bool
	for i := 0; i < 8; i++ {
		for j := 0; j < 8; j++ {
			v := Bayer8[i][j]
			if v < 0 || v > 63 {
				t.Fatalf("Bayer8[%d][%d]=%d out of range", i, j, v)
			}
			if seen[v] {
				t.Fatalf("Bayer8 value %d appears more than once", v)
			}
			seen[v] = true
		}
	}
}

// TestSectionIndex checks the single-row left->right mapping: section 0 is the
// leftmost 10mm, section 8 the rightmost, with out-of-range X clamped.
func TestSectionIndex(t *testing.T) {
	cases := []struct {
		cx   float64
		want int
	}{
		{5, 0},   // leftmost
		{15, 1},  //
		{45, 4},  // center
		{85, 8},  // rightmost
		{-3, 0},  // clamp low
		{200, 8}, // clamp high
	}
	for _, c := range cases {
		if got := SectionIndex(c.cx); got != c.want {
			t.Errorf("SectionIndex(%.0f)=%d, want %d", c.cx, got, c.want)
		}
	}
	// Every section's midpoint maps to that section.
	for s := 0; s < Sections; s++ {
		mid := (float64(s) + 0.5) * SectionMM
		if got := SectionIndex(mid); got != s {
			t.Errorf("SectionIndex(midpoint of %d)=%d, want %d", s, got, s)
		}
	}
}

// TestPlanPairCount checks all C(n,2) plates are produced with the expected
// A<B pairing and Y layout.
func TestPlanPairCount(t *testing.T) {
	plan := BuildPlan(testPalette, 0.5)
	wantPlates := 4 * 3 / 2 // C(4,2)=6
	if len(plan.Plates) != wantPlates {
		t.Fatalf("got %d plates, want %d", len(plan.Plates), wantPlates)
	}
	idx := 0
	for a := 0; a < 4; a++ {
		for b := a + 1; b < 4; b++ {
			p := plan.Plates[idx]
			if p.A != a || p.B != b {
				t.Errorf("plate %d: got pair (%d,%d), want (%d,%d)", idx, p.A, p.B, a, b)
			}
			if p.YOffsetMM != float64(idx)*PitchMM {
				t.Errorf("plate %d: YOffset=%.1f, want %.1f", idx, p.YOffsetMM, float64(idx)*PitchMM)
			}
			idx++
		}
	}
}

// TestBlockSnap checks the square block grid snaps to integer block counts
// spanning exactly the face height (10mm) and width (90mm), with square blocks
// and Nx = Sections*Nz so block boundaries align with section boundaries.
func TestBlockSnap(t *testing.T) {
	plan := BuildPlan(testPalette, 0.525)
	if plan.Nz < 1 || plan.Nx < 1 {
		t.Fatalf("Nx=%d Nz=%d", plan.Nx, plan.Nz)
	}
	if plan.Nx != Sections*plan.Nz {
		t.Errorf("Nx=%d, want Sections*Nz=%d", plan.Nx, Sections*plan.Nz)
	}
	if spanZ := plan.BlockMM * float64(plan.Nz); math.Abs(spanZ-PlateHeightMM) > 1e-9 {
		t.Errorf("blocks span %.6f mm in Z, want %.6f", spanZ, PlateHeightMM)
	}
	if spanX := plan.BlockMM * float64(plan.Nx); math.Abs(spanX-PlateWidthMM) > 1e-9 {
		t.Errorf("blocks span %.6f mm in X, want %.6f", spanX, PlateWidthMM)
	}
}

// TestWatertight verifies that every directed edge of the built mesh is matched
// by exactly one anti-parallel directed edge (closed, consistently-oriented
// manifold). Because plates are disjoint, this holds per plate as well.
func TestWatertight(t *testing.T) {
	plan := BuildPlan(testPalette, 1.5) // coarse grid keeps the test fast
	verts, tris := BuildMesh(plan)
	if len(verts) == 0 || len(tris) == 0 {
		t.Fatal("empty mesh")
	}
	type edge struct{ a, b int }
	dir := make(map[edge]int)
	add := func(a, b int) { dir[edge{a, b}]++ }
	for _, tr := range tris {
		add(tr.A, tr.B)
		add(tr.B, tr.C)
		add(tr.C, tr.A)
	}
	for e, count := range dir {
		if count != 1 {
			t.Errorf("directed edge (%d->%d) appears %d times, want 1", e.a, e.b, count)
		}
		if dir[edge{e.b, e.a}] != 1 {
			t.Errorf("edge (%d,%d) not shared by exactly 2 oppositely-wound triangles", e.a, e.b)
		}
	}
}

// TestLoadRoundTrip writes the OBJ+MTL and loads it back through the real OBJ
// loader, checking the face count, that face base colors match the exact
// palette hexes, and that the bounding box matches the expected 30x30x2-per-
// plate layout after the Y-up -> Z-up conversion.
func TestLoadRoundTrip(t *testing.T) {
	plan := BuildPlan(testPalette, 2.0) // coarse for speed
	dir := t.TempDir()
	objPath, err := WriteOBJ(plan, dir)
	if err != nil {
		t.Fatalf("WriteOBJ: %v", err)
	}
	if filepath.Base(objPath) != "swatch.obj" {
		t.Errorf("obj basename = %q", filepath.Base(objPath))
	}

	model, err := loader.LoadOBJ(objPath, -1)
	if err != nil {
		t.Fatalf("LoadOBJ: %v", err)
	}

	_, wantTris := BuildMesh(plan)
	if len(model.Faces) != len(wantTris) {
		t.Errorf("loaded %d faces, want %d", len(model.Faces), len(wantTris))
	}

	// Every face's base color must be exactly one of the palette hexes.
	palRGB := make(map[[3]uint8]bool)
	for _, f := range testPalette {
		palRGB[hexRGB(f.Hex)] = true
	}
	for i, bc := range model.FaceBaseColor {
		rgb := [3]uint8{bc[0], bc[1], bc[2]}
		if !palRGB[rgb] {
			t.Fatalf("face %d base color %v is not a palette color", i, rgb)
		}
	}

	// Bounding box: X in [0,90], Z in [0,10]; Y spans the plate layout.
	var min, max [3]float32
	for k := 0; k < 3; k++ {
		min[k] = math.MaxFloat32
		max[k] = -math.MaxFloat32
	}
	for _, v := range model.Vertices {
		for k := 0; k < 3; k++ {
			if v[k] < min[k] {
				min[k] = v[k]
			}
			if v[k] > max[k] {
				max[k] = v[k]
			}
		}
	}
	nPlates := len(plan.Plates)
	wantYMax := float32(float64(nPlates-1)*PitchMM + ThickMM)
	checkClose(t, "minX", min[0], 0)
	checkClose(t, "maxX", max[0], PlateWidthMM)
	checkClose(t, "minZ", min[2], 0)
	checkClose(t, "maxZ", max[2], PlateHeightMM)
	checkClose(t, "minY", min[1], 0)
	checkClose(t, "maxY", max[1], wantYMax)
}

func hexRGB(hex string) [3]uint8 {
	r, g, b := hexToUnit(hex)
	return [3]uint8{
		uint8(r*255 + 0.5),
		uint8(g*255 + 0.5),
		uint8(b*255 + 0.5),
	}
}

func checkClose(t *testing.T, name string, got, want float32) {
	t.Helper()
	if math.Abs(float64(got-want)) > 1e-3 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}
