package pipeline

// Regression coverage for the ground-truth palette-search harness. Everything
// here is hermetic: no external model files, no golden images, and no
// statistical thresholds — the synthetic fixtures make every asserted ordering
// exact and deterministic.

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/cellslicer"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/voxel"
	"github.com/rtwfroody/ditherforge/tests/percep"
)

// --- fixtures ---------------------------------------------------------------

// synthVO builds a hermetic voxelizeOutput: an nx×ny grid of visible cells on
// the z=0 plane with a 4-neighbour adjacency graph. colorFn colors cell (x,y).
func synthVO(nx, ny int, colorFn func(x, y int) [3]uint8) *voxelizeOutput {
	n := nx * ny
	vo := &voxelizeOutput{
		Cells:         make([]voxel.ActiveCell, n),
		CellSamples:   make([]cellslicer.CellSample, n),
		Neighbors:     make([][]voxel.Neighbor, n),
		VisibleToCell: make([]int, n),
		LayerH:        0.2,
		CellSize:      1.0,
	}
	for y := 0; y < ny; y++ {
		for x := 0; x < nx; x++ {
			i := y*nx + x
			c := colorFn(x, y)
			vo.Cells[i] = voxel.ActiveCell{
				Cx: float32(x), Cy: float32(y), Cz: 0,
				Color: c, Area: 1.0,
			}
			vo.CellSamples[i] = cellslicer.CellSample{
				SlabIdx:  0,
				CellIdx:  i,
				Centroid: [3]float32{float32(x), float32(y), 0},
				Color:    c,
				Area:     1.0,
				Normal:   [3]float32{0, 0, 1},
			}
			vo.VisibleToCell[i] = i
			var nb []voxel.Neighbor
			if x > 0 {
				nb = append(nb, voxel.Neighbor{Idx: i - 1, Weight: 1})
			}
			if x < nx-1 {
				nb = append(nb, voxel.Neighbor{Idx: i + 1, Weight: 1})
			}
			if y > 0 {
				nb = append(nb, voxel.Neighbor{Idx: i - nx, Weight: 1})
			}
			if y < ny-1 {
				nb = append(nb, voxel.Neighbor{Idx: i + nx, Weight: 1})
			}
			vo.Neighbors[i] = nb
		}
	}
	return vo
}

// faceColorRenderer is a deterministic stand-in for the debugrender-backed
// renderer: it lays each face's color into a square image (one pixel per
// face). Identical mesh colors therefore yield byte-identical images, which is
// exactly the property the metric/determinism tests rely on. res is ignored.
func faceColorRenderer(mesh *MeshData, _ string, _ int) *image.RGBA {
	nf := len(mesh.FaceColors) / 3
	side := int(math.Ceil(math.Sqrt(float64(nf))))
	if side < 1 {
		side = 1
	}
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	for i := 0; i < nf; i++ {
		x, y := i%side, i/side
		img.SetRGBA(x, y, color.RGBA{
			R: uint8(mesh.FaceColors[3*i]),
			G: uint8(mesh.FaceColors[3*i+1]),
			B: uint8(mesh.FaceColors[3*i+2]),
			A: 255,
		})
	}
	return img
}

// --- 1. UNIT: pure logic ----------------------------------------------------

func TestCombinationsCountOrderDeterminism(t *testing.T) {
	got := combinations(5, 2)
	if len(got) != 10 { // C(5,2)
		t.Fatalf("C(5,2): got %d combinations, want 10", len(got))
	}
	// Lexicographic, ascending within each, first and last as expected.
	if !reflect.DeepEqual(got[0], []int{0, 1}) || !reflect.DeepEqual(got[9], []int{3, 4}) {
		t.Fatalf("unexpected bounds: first=%v last=%v", got[0], got[9])
	}
	prev := []int{-1, -1}
	for _, c := range got {
		if c[0] >= c[1] {
			t.Fatalf("combination not strictly ascending: %v", c)
		}
		if c[0] < prev[0] || (c[0] == prev[0] && c[1] <= prev[1]) {
			t.Fatalf("combinations not in lexicographic order: %v after %v", c, prev)
		}
		prev = c
	}
	// Determinism: a second call is identical.
	if !reflect.DeepEqual(got, combinations(5, 2)) {
		t.Fatal("combinations not deterministic across calls")
	}
	// Edge cases.
	if !reflect.DeepEqual(combinations(3, 0), [][]int{{}}) {
		t.Fatal("C(n,0) should be a single empty combination")
	}
	if combinations(2, 3) != nil {
		t.Fatal("C(n,k) with k>n should be nil")
	}
}

func TestExcludeLockedAndDedupSort(t *testing.T) {
	inv := []palette.InventoryEntry{
		{Color: [3]uint8{0x22, 0x22, 0x22}, Label: "b"},
		{Color: [3]uint8{0xFF, 0xFF, 0xFF}, Label: "white-locked"},
		{Color: [3]uint8{0x11, 0x11, 0x11}, Label: "a"},
		{Color: [3]uint8{0x22, 0x22, 0x22}, Label: "dup-b"}, // duplicate color
	}
	locked := []palette.InventoryEntry{{Color: [3]uint8{0xFF, 0xFF, 0xFF}}}

	filtered := excludeLocked(inv, locked)
	for _, e := range filtered {
		if e.Color == [3]uint8{0xFF, 0xFF, 0xFF} {
			t.Fatal("excludeLocked left a locked color in the pool")
		}
	}
	if len(filtered) != 3 { // b, a, dup-b (dup not yet removed)
		t.Fatalf("excludeLocked: got %d, want 3", len(filtered))
	}

	eligible := dedupSortInventory(filtered)
	gotHexes := entryHexes(eligible)
	want := []string{"#111111", "#222222"} // deduped, hex-sorted
	if !reflect.DeepEqual(gotHexes, want) {
		t.Fatalf("dedupSortInventory: got %v, want %v", gotHexes, want)
	}
	// First-occurrence wins for the surviving duplicate.
	for _, e := range eligible {
		if e.Color == [3]uint8{0x22, 0x22, 0x22} && e.Label != "b" {
			t.Fatalf("dedup kept the wrong duplicate: label=%q", e.Label)
		}
	}
}

func TestWeightedMean(t *testing.T) {
	// Pixel-count weighting: a view with 3× the pixels counts 3× as much.
	if got := weightedMean([]float64{10, 20}, []float64{1, 3}); got != 17.5 {
		t.Fatalf("weightedMean = %v, want 17.5", got)
	}
	// Equal weights degenerate to a plain mean.
	if got := weightedMean([]float64{4, 8}, []float64{2, 2}); got != 6 {
		t.Fatalf("weightedMean equal weights = %v, want 6", got)
	}
	// Zero total weight is safe.
	if got := weightedMean([]float64{4, 8}, []float64{0, 0}); got != 0 {
		t.Fatalf("weightedMean zero weight = %v, want 0", got)
	}
}

func TestRankSigmaIndices(t *testing.T) {
	sigmas := []float64{0, 2, 4, 8, 16}
	got := rankSigmaIndices(sigmas, []float64{2, 8})
	if !reflect.DeepEqual(got, []int{1, 3}) {
		t.Fatalf("rankSigmaIndices = %v, want [1 3]", got)
	}
	// A rank sigma not present in the scored set is skipped, not an error.
	if got := rankSigmaIndices(sigmas, []float64{8, 99}); !reflect.DeepEqual(got, []int{3}) {
		t.Fatalf("rankSigmaIndices with missing sigma = %v, want [3]", got)
	}
}

func TestRegretMatchingByHexSet(t *testing.T) {
	// Order-insensitive: the same colors in any order share a key.
	if hexSetKey([]string{"#AAAAAA", "#BBBBBB"}) != hexSetKey([]string{"#BBBBBB", "#AAAAAA"}) {
		t.Fatal("hexSetKey should be order-insensitive")
	}
	if hexSetKey([]string{"#AAAAAA"}) == hexSetKey([]string{"#BBBBBB"}) {
		t.Fatal("distinct sets must have distinct keys")
	}

	eligible := []palette.InventoryEntry{
		{Color: [3]uint8{0x11, 0x11, 0x11}},
		{Color: [3]uint8{0x22, 0x22, 0x22}},
		{Color: [3]uint8{0x33, 0x33, 0x33}},
	}
	// Found: entries map back to their eligible indices (sorted).
	pick := []palette.InventoryEntry{{Color: [3]uint8{0x33, 0x33, 0x33}}, {Color: [3]uint8{0x11, 0x11, 0x11}}}
	if got := comboForEntries(eligible, pick); !reflect.DeepEqual(got, []int{0, 2}) {
		t.Fatalf("comboForEntries found = %v, want [0 2]", got)
	}
	// Not found: a color absent from eligible yields nil (→ caller appends it).
	missing := []palette.InventoryEntry{{Color: [3]uint8{0x99, 0x99, 0x99}}}
	if got := comboForEntries(eligible, missing); got != nil {
		t.Fatalf("comboForEntries not-found = %v, want nil", got)
	}
}

// --- 2. DISPATCH PARITY: the drift guard ------------------------------------

// TestDispatchParity proves the shared ditherAssignments helper routes every
// mode to the exact production kernel with the exact production parameters.
// Because runDither's sole dither logic is now a call to this same helper (the
// mode switch was removed), byte-identical output here means the harness and
// the production Dither stage cannot disagree.
func TestDispatchParity(t *testing.T) {
	ctx := context.Background()
	nx, ny := 20, 20 // a few hundred cells with a real neighbour graph
	vo := synthVO(nx, ny, func(x, y int) [3]uint8 {
		// A deterministic gradient so the error-diffusion modes actually work.
		return [3]uint8{uint8(x * 12), uint8(y * 12), uint8((x + y) * 6)}
	})
	pal := [][3]uint8{{0, 0, 0}, {255, 255, 255}, {200, 30, 30}, {30, 30, 200}}
	null := progress.NullTracker{}
	newModel := func() *voxel.DitherModel { return voxel.NewDitherModel(pal, nil, 0, 0, false) }

	cases := []struct {
		mode   string
		region bool
		want   func() ([]int32, error)
	}{
		{"dlc-d30-p7", false, func() ([]int32, error) {
			return voxel.DitherLocalCorrectedTuned(ctx, vo.Cells, pal, newModel(), vo.Neighbors, null, 0.3, dlcCorrectionPasses)
		}},
		{"floyd-steinberg", false, func() ([]int32, error) {
			return voxel.FloydSteinberg(ctx, vo.Cells, pal, newModel(), vo.Neighbors, null)
		}},
		{"riemersma", false, func() ([]int32, error) {
			return voxel.Riemersma(ctx, vo.Cells, pal, newModel(), vo.Neighbors, 0.85, null)
		}},
		{"bn-adapt-5", false, func() ([]int32, error) {
			return voxel.BlueNoiseAdaptive(ctx, vo.Cells, pal, newModel(), vo.Neighbors, 5, null)
		}},
		{"none", false, func() ([]int32, error) {
			return voxel.AssignColors(ctx, vo.Cells, pal)
		}},
		// RegionDither on: the helper must first cut cross-colour edges.
		{"floyd-steinberg", true, func() ([]int32, error) {
			nb := voxel.CutNeighborsByColor(vo.Cells, vo.Neighbors, 20)
			return voxel.FloydSteinberg(ctx, vo.Cells, pal, newModel(), nb, null)
		}},
	}

	for _, tc := range cases {
		opts := Options{Dither: tc.mode, RiemersmaInputBias: 0.85, RegionDither: tc.region, RegionDitherDeltaE: 20}
		got, err := ditherAssignments(ctx, opts, vo.Cells, pal, newModel(), vo.Neighbors, null)
		if err != nil {
			t.Fatalf("%s region=%v: helper error: %v", tc.mode, tc.region, err)
		}
		want, err := tc.want()
		if err != nil {
			t.Fatalf("%s region=%v: reference error: %v", tc.mode, tc.region, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s region=%v: helper assignments differ from production kernel", tc.mode, tc.region)
		}
	}
}

// --- 3. END-TO-END DETERMINISM ----------------------------------------------

func fiveColorInventory() []palette.InventoryEntry {
	return []palette.InventoryEntry{
		{Color: [3]uint8{0x00, 0x00, 0x00}, Label: "black", TD: palette.DefaultTD},
		{Color: [3]uint8{0xFF, 0x00, 0x00}, Label: "red", TD: palette.DefaultTD},
		{Color: [3]uint8{0x00, 0xFF, 0x00}, Label: "green", TD: palette.DefaultTD},
		{Color: [3]uint8{0x00, 0x00, 0xFF}, Label: "blue", TD: palette.DefaultTD},
		{Color: [3]uint8{0xFF, 0xFF, 0x00}, Label: "yellow", TD: palette.DefaultTD},
	}
}

func TestRunSearchOnCellsDeterministic(t *testing.T) {
	vo := synthVO(12, 12, func(x, y int) [3]uint8 {
		// A patchwork drawn from inventory colors so every candidate scores
		// something meaningful.
		switch (x + y) % 3 {
		case 0:
			return [3]uint8{0xFF, 0x00, 0x00}
		case 1:
			return [3]uint8{0x00, 0x00, 0xFF}
		default:
			return [3]uint8{0xFF, 0xFF, 0x00}
		}
	})
	opts := Options{NumColors: 2, Dither: "none", LayerHeight: 0.2}
	run := func(dir string) *PaletteSearchResult {
		cfg := PaletteSearchConfig{
			Inventory: fiveColorInventory(),
			Workers:   3,
			RenderTop: 2,
			OutDir:    dir,
		}
		res, err := runSearchOnCells(context.Background(), opts, cfg, vo, &splitOutput{}, faceColorRenderer)
		if err != nil {
			t.Fatalf("runSearchOnCells: %v", err)
		}
		return res
	}

	dir1, dir2 := t.TempDir(), t.TempDir()
	r1 := run(dir1)
	r2 := run(dir2)

	if r1.NumCandidates != 10 { // C(5,2)
		t.Fatalf("NumCandidates = %d, want 10", r1.NumCandidates)
	}
	// Identical rankings and per-σ scores across independent runs.
	if len(r1.Candidates) != len(r2.Candidates) {
		t.Fatalf("candidate count mismatch: %d vs %d", len(r1.Candidates), len(r2.Candidates))
	}
	for i := range r1.Candidates {
		a, b := r1.Candidates[i], r2.Candidates[i]
		if a.Rank != b.Rank || !reflect.DeepEqual(a.Hexes, b.Hexes) || !reflect.DeepEqual(a.ScoresBySigma, b.ScoresBySigma) || a.RankKey != b.RankKey {
			t.Fatalf("run mismatch at rank %d:\n %+v\n %+v", i+1, a, b)
		}
		// Every score must be finite.
		for _, s := range a.ScoresBySigma {
			if math.IsNaN(s) || math.IsInf(s, 0) {
				t.Fatalf("non-finite score at rank %d: %v", i+1, a.ScoresBySigma)
			}
		}
	}
	// Ascending rank key (ranking is a sort).
	for i := 1; i < len(r1.Candidates); i++ {
		if r1.Candidates[i].RankKey < r1.Candidates[i-1].RankKey {
			t.Fatalf("candidates not sorted by rank key at %d", i)
		}
	}
	// Regret is reportable.
	if r1.Production == nil {
		t.Fatal("production pick missing")
	}

	// The target render is identical across runs (hash the written PNG).
	t1, _ := os.ReadFile(filepath.Join(dir1, "target_front.png"))
	t2, _ := os.ReadFile(filepath.Join(dir2, "target_front.png"))
	if len(t1) == 0 || !reflect.DeepEqual(t1, t2) {
		t.Fatal("target render differs across runs")
	}

	// results.json round-trips and agrees with the in-memory result.
	raw, err := os.ReadFile(filepath.Join(dir1, "results.json"))
	if err != nil {
		t.Fatalf("read results.json: %v", err)
	}
	var decoded PaletteSearchResult
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("results.json unmarshal: %v", err)
	}
	if len(decoded.Candidates) != len(r1.Candidates) || decoded.Candidates[0].RankKey != r1.Candidates[0].RankKey {
		t.Fatal("results.json does not round-trip the ranking")
	}
	// results.csv has one header + one row per candidate.
	csvRaw, err := os.ReadFile(filepath.Join(dir1, "results.csv"))
	if err != nil {
		t.Fatalf("read results.csv: %v", err)
	}
	if lines := nonEmptyLines(string(csvRaw)); lines != len(r1.Candidates)+1 {
		t.Fatalf("results.csv has %d rows, want %d", lines, len(r1.Candidates)+1)
	}
}

// --- 4. METRIC INVARIANTS ---------------------------------------------------

func TestMetricInvariants(t *testing.T) {
	target := [3]uint8{0x30, 0x80, 0xC0}
	wrong := [3]uint8{0xC0, 0x40, 0x20}

	vo := synthVO(10, 10, func(x, y int) [3]uint8 { return target })
	geom := buildSplatGeometry(vo, nil)
	nVis := len(vo.VisibleToCell)
	fill := func(c [3]uint8) [][3]uint8 {
		out := make([][3]uint8, nVis)
		for i := range out {
			out[i] = c
		}
		return out
	}

	views := []string{"front"}
	sigmas := []float64{0, 2, 4, 8, 16}
	// Pre-blur the target exactly as runSearchOnCells does.
	targetMesh := geom.colorByVisible(fill(target))
	targetBase := percep.FromRGBA(faceColorRenderer(targetMesh, "front", 64))
	targetBlur := [][]*percep.Image{make([]*percep.Image, len(sigmas))}
	for si, s := range sigmas {
		targetBlur[0][si] = targetBase.Blur(s)
	}
	rankIdx := rankSigmaIndices(sigmas, []float64{2, 8})
	sigma2Idx := indexOfSigma(sigmas, 2)

	matchMesh := geom.colorByVisible(fill(target))
	wrongMesh := geom.colorByVisible(fill(wrong))
	matchCand := scoreMeshAgainstTarget(matchMesh, views, 64, sigmas, targetBlur, sigma2Idx, rankIdx, faceColorRenderer)
	wrongCand := scoreMeshAgainstTarget(wrongMesh, views, 64, sigmas, targetBlur, sigma2Idx, rankIdx, faceColorRenderer)

	// A perfectly matching candidate scores exactly ΔE=0 at every σ.
	for si, s := range sigmas {
		if matchCand.ScoresBySigma[si] != 0 {
			t.Fatalf("matching candidate σ=%v scored %v, want 0", s, matchCand.ScoresBySigma[si])
		}
	}
	// A uniformly-wrong candidate scores strictly worse at every σ.
	for si, s := range sigmas {
		if !(wrongCand.ScoresBySigma[si] > matchCand.ScoresBySigma[si]) {
			t.Fatalf("wrong candidate σ=%v scored %v, not strictly worse than %v",
				s, wrongCand.ScoresBySigma[si], matchCand.ScoresBySigma[si])
		}
	}
	if !(wrongCand.RankKey > matchCand.RankKey) {
		t.Fatalf("wrong rank key %v not worse than match %v", wrongCand.RankKey, matchCand.RankKey)
	}
}

// --- 5. REGRET-TABLE LOOKUP (--regret-table mode) ---------------------------

// regretFixtureCSV writes a results.csv (via the real writer, so the parser is
// tested against the exact on-disk format, extra σ columns and all) and returns
// its path.
func regretFixtureCSV(t *testing.T) string {
	t.Helper()
	res := &PaletteSearchResult{
		Sigmas:     []float64{0, 2, 8},
		RankSigmas: []float64{2, 8},
		Candidates: []PaletteCandidate{
			{Rank: 1, Labels: []string{"black", "red"}, Hexes: []string{"#000000", "#FF0000"}, ScoresBySigma: []float64{1, 2, 3}, P99Sigma2: 9, RankKey: 2.5},
			{Rank: 2, Labels: []string{"black", "blue"}, Hexes: []string{"#000000", "#0000FF"}, ScoresBySigma: []float64{2, 3, 4}, P99Sigma2: 9, RankKey: 3.5},
			{Rank: 3, Labels: []string{"red", "blue"}, Hexes: []string{"#FF0000", "#0000FF"}, ScoresBySigma: []float64{3, 4, 5}, P99Sigma2: 9, RankKey: 4.5},
		},
	}
	path := filepath.Join(t.TempDir(), "results.csv")
	if err := writeCSV(path, res); err != nil {
		t.Fatalf("writeCSV: %v", err)
	}
	return path
}

func TestLoadRegretTable(t *testing.T) {
	path := regretFixtureCSV(t)
	rows, err := loadRegretTable(path)
	if err != nil {
		t.Fatalf("loadRegretTable: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	// Fields are located by header name, so the extra σ columns between "hexes"
	// and "rank_key" do not shift the parse.
	got := rows[1]
	if got.rank != 2 || got.rankKey != 3.5 {
		t.Fatalf("row 2: rank=%d rankKey=%v, want 2 / 3.5", got.rank, got.rankKey)
	}
	if !reflect.DeepEqual(got.hexes, []string{"#000000", "#0000FF"}) {
		t.Fatalf("row 2 hexes = %v", got.hexes)
	}
	if !reflect.DeepEqual(got.labels, []string{"black", "blue"}) {
		t.Fatalf("row 2 labels = %v", got.labels)
	}
}

func TestLoadRegretTableRejectsForeignCSV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign.csv")
	if err := os.WriteFile(path, []byte("a,b,c\n1,2,3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRegretTable(path); err == nil {
		t.Fatal("expected an error for a CSV missing the palettesearch columns")
	}
}

func TestLookupRegret(t *testing.T) {
	path := regretFixtureCSV(t)
	ent := func(c [3]uint8, label string) palette.InventoryEntry {
		return palette.InventoryEntry{Color: c, Label: label}
	}

	// Order-insensitive hex-set match: the winner (rank 1) is {black,red}, given
	// here in reversed order. Regret against itself is zero.
	rep, err := lookupRegret(path, []palette.InventoryEntry{
		ent([3]uint8{0xFF, 0x00, 0x00}, "red"), ent([3]uint8{0x00, 0x00, 0x00}, "black"),
	}, nil)
	if err != nil {
		t.Fatalf("lookupRegret winner: %v", err)
	}
	if rep.Rank != 1 || rep.RankKey != 2.5 || rep.Total != 3 {
		t.Fatalf("winner: rank=%d rankKey=%v total=%d", rep.Rank, rep.RankKey, rep.Total)
	}
	if rep.WinnerRankKey != 2.5 || rep.Delta != 0 {
		t.Fatalf("winner delta: winnerRankKey=%v delta=%v", rep.WinnerRankKey, rep.Delta)
	}

	// A mid-table pick reports positive regret against the rank-1 winner.
	rep, err = lookupRegret(path, []palette.InventoryEntry{
		ent([3]uint8{0x00, 0x00, 0x00}, "black"), ent([3]uint8{0x00, 0x00, 0xFF}, "blue"),
	}, nil)
	if err != nil {
		t.Fatalf("lookupRegret mid: %v", err)
	}
	if rep.Rank != 2 || rep.RankKey != 3.5 {
		t.Fatalf("mid: rank=%d rankKey=%v, want 2 / 3.5", rep.Rank, rep.RankKey)
	}
	if math.Abs(rep.Delta-1.0) > 1e-9 || !reflect.DeepEqual(rep.WinnerHexes, []string{"#000000", "#FF0000"}) {
		t.Fatalf("mid: delta=%v winnerHexes=%v", rep.Delta, rep.WinnerHexes)
	}
}

func TestLookupRegretNotFound(t *testing.T) {
	path := regretFixtureCSV(t)
	_, err := lookupRegret(path, []palette.InventoryEntry{
		{Color: [3]uint8{0x00, 0xFF, 0x00}, Label: "green"},
		{Color: [3]uint8{0xFF, 0xFF, 0xFF}, Label: "white"},
	}, []palette.InventoryEntry{{Color: [3]uint8{0x12, 0x34, 0x56}}})
	if err == nil {
		t.Fatal("expected a not-found error for a pick absent from the table")
	}
	// The error must name both the pick and the locked config so a mismatched
	// inventory is diagnosable.
	msg := err.Error()
	for _, want := range []string{"#00FF00", "#123456", "different inventory"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("not-found error %q missing %q", msg, want)
		}
	}
}

// nonEmptyLines counts non-empty lines in s.
func nonEmptyLines(s string) int {
	n := 0
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			if i > start {
				n++
			}
			start = i + 1
		}
	}
	return n
}
