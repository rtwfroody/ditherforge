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

// Representative slab heights (Snapmaker U1, 0.4 nozzle, 0.20mm layer): a
// taller first layer then uniform upper layers.
const (
	testLayer0Z = 0.25
	testUpperZ  = 0.20
)

// blueNoiseGrids returns a couple of representative (block width, layer0, upper)
// configs spanning the layer heights the swatch pattern is printed at.
func blueNoiseGrids() []struct {
	name                  string
	blockWidth, l0, upper float64
} {
	return []struct {
		name                  string
		blockWidth, l0, upper float64
	}{
		{"0.20mm-layer", 0.5, 0.25, 0.20},
		{"0.08mm-layer", 0.4, 0.10, 0.08},
	}
}

// TestBlueNoiseRankComplete verifies the shared void-and-cluster ranking is a
// permutation of [0, N): every value 0..N-1 appears exactly once over the
// perSection x Nrows grid.
func TestBlueNoiseRankComplete(t *testing.T) {
	for _, g := range blueNoiseGrids() {
		plan := BuildPlan(testPalette, g.blockWidth, g.l0, g.upper)
		perSection := plan.Nx / Sections
		nrows := plan.Nrows()
		n := perSection * nrows
		if len(plan.Rank) != nrows {
			t.Fatalf("%s: Rank has %d rows, want %d", g.name, len(plan.Rank), nrows)
		}
		seen := make([]bool, n)
		for j := 0; j < nrows; j++ {
			if len(plan.Rank[j]) != perSection {
				t.Fatalf("%s: Rank[%d] has %d cols, want %d", g.name, j, len(plan.Rank[j]), perSection)
			}
			for i := 0; i < perSection; i++ {
				v := plan.Rank[j][i]
				if v < 0 || v >= n {
					t.Fatalf("%s: Rank[%d][%d]=%d out of range [0,%d)", g.name, j, i, v, n)
				}
				if seen[v] {
					t.Fatalf("%s: rank value %d appears more than once", g.name, v)
				}
				seen[v] = true
			}
		}
	}
}

// TestBlueNoiseCoverageExactness verifies that for each nominal coverage p=k/8,
// each section carries exactly round(k/8*N) filament-B blocks — the property
// that makes the endpoint sections exactly solid and the interior sections carry
// a known, even coverage. Counted through the real blockIsB threshold.
func TestBlueNoiseCoverageExactness(t *testing.T) {
	for _, g := range blueNoiseGrids() {
		plan := BuildPlan(testPalette, g.blockWidth, g.l0, g.upper)
		perSection := plan.Nx / Sections
		nrows := plan.Nrows()
		n := perSection * nrows
		for k := 0; k <= 8; k++ {
			s := k // section index == k, coverage k/8
			count := 0
			// Columns of section s span [s*perSection, (s+1)*perSection).
			for i := s * perSection; i < (s+1)*perSection; i++ {
				for j := 0; j < nrows; j++ {
					if plan.blockIsB(i, j, s) {
						count++
					}
				}
			}
			want := int(math.Round(Coverage(k) * float64(n)))
			if count != want {
				t.Errorf("%s coverage %d/8: got %d B blocks, want %d (round(%d/8*%d))",
					g.name, k, count, want, k, n)
			}
		}
	}
}

// TestBlueNoiseDeterminism verifies the ranking is byte-identical across two
// independent BuildPlan calls with the same geometry — a hard requirement so a
// printed plate's manifest reproduces the exact pattern.
func TestBlueNoiseDeterminism(t *testing.T) {
	for _, g := range blueNoiseGrids() {
		a := BuildPlan(testPalette, g.blockWidth, g.l0, g.upper)
		b := BuildPlan(testPalette, g.blockWidth, g.l0, g.upper)
		if len(a.Rank) != len(b.Rank) {
			t.Fatalf("%s: rank row counts differ %d vs %d", g.name, len(a.Rank), len(b.Rank))
		}
		for j := range a.Rank {
			for i := range a.Rank[j] {
				if a.Rank[j][i] != b.Rank[j][i] {
					t.Fatalf("%s: Rank[%d][%d] differs %d vs %d", g.name, j, i, a.Rank[j][i], b.Rank[j][i])
				}
			}
		}
	}
}

// TestBlueNoiseSpread is a coarse blue-noise sanity check: at low coverage (1/8)
// the B blocks are well-spread, so the minimum PHYSICAL distance between any two
// (measured toroidally, since the rank tiles across sections and stacked rows)
// stays a healthy fraction of the ideal blue-noise spacing sqrt(cellArea/p). A
// clustered or random pattern would place near-touching pairs and fall far below
// this bound. Blocks are anisotropic, so distance must be in millimeters, not
// grid steps — grid adjacency in the fine (Z) axis is still 0.08–0.2mm and would
// falsely flag. The bound (0.4) is conservative: measured ratios are ~0.56–0.63.
func TestBlueNoiseSpread(t *testing.T) {
	for _, g := range blueNoiseGrids() {
		plan := BuildPlan(testPalette, g.blockWidth, g.l0, g.upper)
		perSection := plan.Nx / Sections
		nrows := plan.Nrows()
		n := perSection * nrows
		bw, up := plan.BlockWidthMM, g.upper
		extX, extZ := float64(perSection)*bw, float64(nrows)*up
		thr := int(math.Round((1.0 / 8.0) * float64(n)))

		var pts [][2]float64
		for j := 0; j < nrows; j++ {
			for i := 0; i < perSection; i++ {
				if plan.Rank[j][i] < thr {
					pts = append(pts, [2]float64{float64(i) * bw, float64(j) * up})
				}
			}
		}
		minD := math.Inf(1)
		for a := 0; a < len(pts); a++ {
			for b := a + 1; b < len(pts); b++ {
				dx := math.Abs(pts[a][0] - pts[b][0])
				if extX-dx < dx {
					dx = extX - dx
				}
				dz := math.Abs(pts[a][1] - pts[b][1])
				if extZ-dz < dz {
					dz = extZ - dz
				}
				if d := math.Hypot(dx, dz); d < minD {
					minD = d
				}
			}
		}
		ideal := math.Sqrt(bw * up / (1.0 / 8.0))
		if minD < 0.4*ideal {
			t.Errorf("%s: min B-B distance %.4fmm at 1/8 coverage < 0.4*ideal (%.4fmm); pattern not blue-noise-spread",
				g.name, minD, 0.4*ideal)
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
	plan := BuildPlan(testPalette, 0.5, testLayer0Z, testUpperZ)
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

// TestBlockSnap checks the block WIDTH snaps to an integer count per 10mm
// section (so block boundaries align with section boundaries) and that the
// rows are slab-tall: first row = layer0Z, upper rows = upperZ, final row
// clamped to the 10mm face top (may be partial).
func TestBlockSnap(t *testing.T) {
	plan := BuildPlan(testPalette, 0.525, testLayer0Z, testUpperZ)
	if plan.Nx < 1 {
		t.Fatalf("Nx=%d", plan.Nx)
	}
	// Width: integer blocks tile each section, and the whole 90mm width.
	if plan.Nx%Sections != 0 {
		t.Errorf("Nx=%d not a multiple of Sections=%d", plan.Nx, Sections)
	}
	if spanX := plan.BlockWidthMM * float64(plan.Nx); math.Abs(spanX-PlateWidthMM) > 1e-9 {
		t.Errorf("blocks span %.6f mm in X, want %.6f", spanX, PlateWidthMM)
	}
	perSection := plan.Nx / Sections
	if math.Abs(plan.BlockWidthMM*float64(perSection)-SectionMM) > 1e-9 {
		t.Errorf("blocks don't tile a 10mm section: width %.6f x %d", plan.BlockWidthMM, perSection)
	}

	// Rows: aligned to the slab grid.
	edges := plan.RowEdges
	if len(edges) != plan.Nrows()+1 {
		t.Fatalf("RowEdges len %d, want Nrows+1 = %d", len(edges), plan.Nrows()+1)
	}
	if edges[0] != 0 {
		t.Errorf("first edge = %v, want 0", edges[0])
	}
	if math.Abs(edges[len(edges)-1]-PlateHeightMM) > 1e-9 {
		t.Errorf("last edge = %v, want %v", edges[len(edges)-1], PlateHeightMM)
	}
	if h0 := edges[1] - edges[0]; math.Abs(h0-testLayer0Z) > 1e-9 {
		t.Errorf("first row height = %v, want %v (layer0)", h0, testLayer0Z)
	}
	for j := 1; j < plan.Nrows()-1; j++ { // interior rows are full upper layers
		if h := edges[j+1] - edges[j]; math.Abs(h-testUpperZ) > 1e-9 {
			t.Errorf("row %d height = %v, want %v (upper)", j, h, testUpperZ)
		}
	}
	if last := edges[plan.Nrows()] - edges[plan.Nrows()-1]; last > testUpperZ+1e-9 || last <= 0 {
		t.Errorf("last row height = %v, want (0, %v]", last, testUpperZ)
	}
	// 10mm face at ~0.2mm rows should be ~50 rows.
	if plan.Nrows() < 40 || plan.Nrows() > 60 {
		t.Errorf("Nrows=%d, want ~50", plan.Nrows())
	}
}

// TestWatertight verifies that every directed edge of the built mesh is matched
// by exactly one anti-parallel directed edge (closed, consistently-oriented
// manifold). Because plates are disjoint, this holds per plate as well.
func TestWatertight(t *testing.T) {
	plan := BuildPlan(testPalette, 0.525, testLayer0Z, testUpperZ) // realistic block width
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

// TestLoadRoundTrip writes the OBJ+MTL+PNGs and loads them back through the real
// OBJ loader, checking the chamfered box face count, that the pattern is carried
// by textures with UVs (not per-face colors), and that the bounding box matches
// the expected 90x10x2-per-plate layout after the Y-up -> Z-up conversion.
func TestLoadRoundTrip(t *testing.T) {
	plan := BuildPlan(testPalette, 0.525, testLayer0Z, testUpperZ) // realistic block width
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

	// Chamfered box: 44 triangles per plate (6 inset faces=12, 12 bands=24,
	// 8 corner tris=8).
	if want := 44 * len(plan.Plates); len(model.Faces) != want {
		t.Errorf("loaded %d faces, want %d (44 per plate)", len(model.Faces), want)
	}
	_, wantTris := BuildMesh(plan)
	if len(model.Faces) != len(wantTris) {
		t.Errorf("loaded %d faces, want BuildMesh's %d", len(model.Faces), len(wantTris))
	}

	// The pattern is a texture, so the mesh must carry UVs and per-plate
	// textures, and every face must reference a valid texture (the OBJ loader
	// rejects meshes mixing textured and untextured faces).
	if len(model.Textures) != len(plan.Plates) {
		t.Errorf("loaded %d textures, want %d (one per plate)", len(model.Textures), len(plan.Plates))
	}
	if len(model.UVs) != len(model.Vertices) {
		t.Fatalf("UVs len %d != vertices len %d", len(model.UVs), len(model.Vertices))
	}
	for i, ti := range model.FaceTextureIdx {
		if ti < 0 || int(ti) >= len(model.Textures) {
			t.Fatalf("face %d texture index %d out of range [0,%d)", i, ti, len(model.Textures))
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

// TestPatternImageCoverage verifies the generated per-plate pattern texture
// encodes the intended B-fraction per section (this is the color field the
// pipeline samples per cell, so it must match the nominal coverage). Uses a
// two-color palette so a texel is B iff its red channel is high.
func TestPatternImageCoverage(t *testing.T) {
	pal := []Filament{{Hex: "#000000", Label: "A"}, {Hex: "#FFFFFF", Label: "B"}}
	plan := BuildPlan(pal, 0.525, testLayer0Z, testUpperZ)
	img := plan.patternImage(plan.Plates[0])
	k := patternUpsample
	htex := img.Bounds().Dy()
	// Count B texels over each section's block-column centers across all texel
	// rows. Texel rows are proportional to physical slab-row heights, so this is
	// an area-weighted B-fraction — it should match the nominal coverage (up to a
	// small skew from the single taller first row).
	for s := 0; s < Sections; s++ {
		bCount, tot := 0, 0
		for col := 0; col < plan.Nx; col++ {
			if SectionIndex((float64(col)+0.5)*plan.BlockWidthMM) != s {
				continue
			}
			for ty := 0; ty < htex; ty++ {
				r, _, _, _ := img.At(col*k+k/2, ty).RGBA()
				if r>>8 > 128 {
					bCount++
				}
				tot++
			}
		}
		got := float64(bCount) / float64(tot)
		if math.Abs(got-Coverage(s)) > 0.03 {
			t.Errorf("section %d texture B-fraction = %.3f, want ~%.3f", s, got, Coverage(s))
		}
	}
}

// TestChamferGeometry verifies the chamfered-plate topology: exact vertex/face
// counts, watertight 2-manifoldness, that NO vertex sits at an original sharp
// box corner (every corner is cut back by the Y+Z chamfer), that the chamfer
// insets Z by ChamferMM while leaving X un-inset (see plateVertices), and that
// the outer bounding box is unchanged (chamfer only removes material inward from
// the edges).
func TestChamferGeometry(t *testing.T) {
	plan := BuildPlan(testPalette, 0.525, testLayer0Z, testUpperZ)
	verts, tris := BuildMesh(plan)
	nP := len(plan.Plates)
	chamfer := plateChamferMM(plan.BlockWidthMM)

	// 24 verts, 44 tris per plate.
	if want := 24 * nP; len(verts) != want {
		t.Errorf("got %d verts, want %d (24 per plate)", len(verts), want)
	}
	if want := 44 * nP; len(tris) != want {
		t.Errorf("got %d tris, want %d (44 per plate)", len(tris), want)
	}

	// Watertight & 2-manifold: every directed edge matched by exactly one
	// anti-parallel twin.
	type edge struct{ a, b int }
	dir := make(map[edge]int)
	for _, tr := range tris {
		dir[edge{tr.A, tr.B}]++
		dir[edge{tr.B, tr.C}]++
		dir[edge{tr.C, tr.A}]++
	}
	for e, count := range dir {
		if count != 1 {
			t.Errorf("directed edge (%d->%d) used %d times, want 1", e.a, e.b, count)
		}
		if dir[edge{e.b, e.a}] != 1 {
			t.Errorf("edge (%d,%d) lacks exactly one anti-parallel twin", e.a, e.b)
		}
	}

	// Per plate: no vertex at an original sharp corner, correct bounding box,
	// and the chamfer inset equals ChamferMM.
	const eps = 1e-9
	for p, plate := range plan.Plates {
		y0 := plate.YOffsetMM
		y1 := y0 + ThickMM
		sharp := [8][3]float64{
			{0, y0, 0}, {PlateWidthMM, y0, 0}, {PlateWidthMM, y0, PlateHeightMM}, {0, y0, PlateHeightMM},
			{0, y1, 0}, {PlateWidthMM, y1, 0}, {PlateWidthMM, y1, PlateHeightMM}, {0, y1, PlateHeightMM},
		}
		var lo, hi [3]float64
		for k := 0; k < 3; k++ {
			lo[k], hi[k] = math.MaxFloat64, -math.MaxFloat64
		}
		for i := 0; i < 24; i++ {
			v := verts[p*24+i]
			for _, s := range sharp {
				if math.Abs(v[0]-s[0]) < eps && math.Abs(v[1]-s[1]) < eps && math.Abs(v[2]-s[2]) < eps {
					t.Errorf("plate %d: vertex %v sits at a sharp box corner", p, v)
				}
			}
			for k := 0; k < 3; k++ {
				if v[k] < lo[k] {
					lo[k] = v[k]
				}
				if v[k] > hi[k] {
					hi[k] = v[k]
				}
			}
		}
		// Outer extents unchanged: the extreme inset faces still reach the box.
		wantLo := [3]float64{0, y0, 0}
		wantHi := [3]float64{PlateWidthMM, y1, PlateHeightMM}
		for k := 0; k < 3; k++ {
			if math.Abs(lo[k]-wantLo[k]) > eps || math.Abs(hi[k]-wantHi[k]) > eps {
				t.Errorf("plate %d axis %d bounds [%.4f,%.4f], want [%.4f,%.4f]", p, k, lo[k], hi[k], wantLo[k], wantHi[k])
			}
		}
	}

	// Chamfer insets: on plate 0 the front-face (Y=y0) corner vertex sits
	// `chamfer` inside the box in Z but flush (X=0) in X — the textured face
	// keeps its full X extent so the pattern's cell/block phase is preserved.
	if math.Abs(chamfer-ChamferMM) > eps {
		t.Errorf("realized chamfer %.4f, want %.2f", chamfer, ChamferMM)
	}
	pv := plateVertices(plan.Plates[0].YOffsetMM, chamfer)
	corner := pv[chamferVertIdx(0, 0, 0, 1)] // Y-pinned vertex of corner (0,0,0)
	if math.Abs(corner[0]-0) > eps {
		t.Errorf("front corner X = %.4f, want 0 (X must not be inset)", corner[0])
	}
	if math.Abs(corner[2]-chamfer) > eps {
		t.Errorf("front corner Z = %.4f, want %.4f (Z inset by chamfer)", corner[2], chamfer)
	}
}

func checkClose(t *testing.T, name string, got, want float32) {
	t.Helper()
	if math.Abs(float64(got-want)) > 1e-3 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}
