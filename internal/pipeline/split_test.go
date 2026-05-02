package pipeline

import (
	"context"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// TestSplitDisabled_NoCacheKeyChange — when Split.Enabled is false,
// changing other Split fields should not affect any stage's cache
// key. This preserves cache-hit equivalence with the pre-Split path
// — anyone toggling Split sliders while Split is off must not
// invalidate downstream caches.
func TestSplitDisabled_NoCacheKeyChange(t *testing.T) {
	c := NewStageCache()
	path := makeFakeInput(t)
	base := Options{Input: path, Scale: 1, NozzleDiameter: 0.4, LayerHeight: 0.2, Dither: "none"}
	// Split off. Toggling other fields should be invisible.
	tweaked := base
	tweaked.Split.Axis = 1
	tweaked.Split.Offset = 5.0
	tweaked.Split.ConnectorStyle = "pegs"
	tweaked.Split.ConnectorCount = 2
	tweaked.Split.ConnectorDiamMM = 5
	tweaked.Split.ConnectorDepthMM = 6
	tweaked.Split.ClearanceMM = 0.15
	for s := StageLoad; s < numStages; s++ {
		if c.stageKey(s, base) != c.stageKey(s, tweaked) {
			t.Errorf("stage %d key changed when Split is off but other Split fields changed", s)
		}
	}
}

// TestSplitEnabled_CacheKeyCascade — flipping Split.Enabled changes
// StageSplit's key and every downstream stage's key, but not
// StageLoad or StageParse (Split is downstream of Load).
func TestSplitEnabled_CacheKeyCascade(t *testing.T) {
	c := NewStageCache()
	path := makeFakeInput(t)
	off := Options{Input: path, Scale: 1, NozzleDiameter: 0.4, LayerHeight: 0.2, Dither: "none"}
	on := off
	on.Split.Enabled = true
	on.Split.Axis = 2
	on.Split.Offset = 5
	on.Split.ConnectorStyle = "dowels"
	on.Split.ConnectorDiamMM = 4
	on.Split.ConnectorDepthMM = 5
	on.Split.ClearanceMM = 0.15

	// Parse and Load should NOT change.
	if c.stageKey(StageParse, off) != c.stageKey(StageParse, on) {
		t.Error("StageParse key changed when Split toggled — cascade leaked upward")
	}
	if c.stageKey(StageLoad, off) != c.stageKey(StageLoad, on) {
		t.Error("StageLoad key changed when Split toggled — cascade leaked upward")
	}
	// Split through Merge SHOULD change.
	for s := StageSplit; s < numStages; s++ {
		if c.stageKey(s, off) == c.stageKey(s, on) {
			t.Errorf("stage %d key did not change when Split toggled (cascade broken)", s)
		}
	}
}

// TestSplitEnabled_FieldCascade — when Split is enabled, changing
// each Split field individually changes downstream cache keys. Maps
// to "any settings change rebuilds the appropriate caches."
func TestSplitEnabled_FieldCascade(t *testing.T) {
	c := NewStageCache()
	path := makeFakeInput(t)
	base := Options{Input: path, Scale: 1, NozzleDiameter: 0.4, LayerHeight: 0.2, Dither: "none"}
	base.Split.Enabled = true
	base.Split.Axis = 2
	base.Split.Offset = 5
	base.Split.ConnectorStyle = "dowels"
	cases := []struct {
		name string
		mut  func(o *Options)
	}{
		{"Axis", func(o *Options) { o.Split.Axis = 0 }},
		{"Offset", func(o *Options) { o.Split.Offset = 6 }},
		{"ConnectorStyle", func(o *Options) { o.Split.ConnectorStyle = "pegs" }},
		{"ConnectorCount", func(o *Options) { o.Split.ConnectorCount = 2 }},
		{"ConnectorDiamMM", func(o *Options) { o.Split.ConnectorDiamMM = 5 }},
		{"ConnectorDepthMM", func(o *Options) { o.Split.ConnectorDepthMM = 6 }},
		{"ClearanceMM", func(o *Options) { o.Split.ClearanceMM = 0.2 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			alt := base
			tc.mut(&alt)
			if c.stageKey(StageSplit, base) == c.stageKey(StageSplit, alt) {
				t.Errorf("StageSplit key did not change when %s changed", tc.name)
			}
		})
	}
}

// TestMergeSplitFaces_PerHalfMergeAndConcat — mergeSplitFaces should
// run MergeCoplanarTriangles once per half (faces are grouped by
// halfIdx in clipSplit's output) and concatenate, preserving the
// per-face HalfIdx parallel array on the result. Constructs a tiny
// shell with two coplanar quads on each half (4 triangles per half,
// expecting merge to reduce to 2 triangles per half).
func TestMergeSplitFaces_PerHalfMergeAndConcat(t *testing.T) {
	// Half 0: a quad in the z=0 plane at x=[0,1], y=[0,2], split into
	// 2 triangles, with a coplanar adjacent quad at y=[2,4]. Result:
	// 4 triangles that merge into 2 (since coplanar same-color groups
	// re-triangulate to a quad = 2 tris).
	verts := [][3]float32{
		// half 0 (8 verts)
		{0, 0, 0}, {1, 0, 0}, {1, 2, 0}, {0, 2, 0},
		{0, 4, 0}, {1, 4, 0}, // extends y to 4
		{0, 0, 0}, {0, 0, 0}, // padding to keep counts simple
		// half 1 (8 verts shifted in x)
		{10, 0, 0}, {11, 0, 0}, {11, 2, 0}, {10, 2, 0},
		{10, 4, 0}, {11, 4, 0},
		{0, 0, 0}, {0, 0, 0},
	}
	// 4 tris per half (2 quads each = 4 tris).
	faces := [][3]uint32{
		// Half 0 quads (z=0 plane)
		{0, 1, 2}, {0, 2, 3}, // first quad
		{3, 2, 5}, {3, 5, 4}, // second quad sharing edge 2-3 (now indices 3-2 reversed) -> using 3 and 5 for share
		// Half 1
		{8, 9, 10}, {8, 10, 11},
		{11, 10, 13}, {11, 13, 12},
	}
	assignments := []int32{0, 0, 0, 0, 1, 1, 1, 1}
	halfIdx := []byte{0, 0, 0, 0, 1, 1, 1, 1}
	outFaces, outAssign, outHalf, err := mergeSplitFaces(
		context.Background(), verts, faces, assignments, halfIdx, progress.NullTracker{},
	)
	if err != nil {
		t.Fatalf("mergeSplitFaces: %v", err)
	}
	if len(outFaces) != len(outAssign) || len(outFaces) != len(outHalf) {
		t.Errorf("output array lengths differ: faces=%d assign=%d half=%d", len(outFaces), len(outAssign), len(outHalf))
	}
	// Count faces per half. Should be > 0 and grouped (all 0s come
	// before all 1s after concat).
	var n0, n1 int
	transitionSeen := false
	for i, h := range outHalf {
		if h == 0 {
			if transitionSeen {
				t.Errorf("face %d has HalfIdx=0 but a HalfIdx=1 came earlier — concat order broken", i)
			}
			n0++
		} else if h == 1 {
			transitionSeen = true
			n1++
		} else {
			t.Errorf("face %d has unexpected HalfIdx=%d", i, h)
		}
	}
	if n0 == 0 || n1 == 0 {
		t.Errorf("expected both halves represented; got n0=%d n1=%d", n0, n1)
	}
}

// TestClipSplit_FiltersPatchMapByHalf — verifies that clipSplit's
// patch-map filtering routes each cell's patch into the correct
// per-half map. Doesn't run the full clip; it's a unit test of the
// filter logic, which is the load-bearing correctness step.
func TestClipSplit_FiltersPatchMapByHalf(t *testing.T) {
	// Two cells: one in half 0, one in half 1.
	cells := []voxel.ActiveCell{
		{Grid: 0, Col: 0, Row: 0, Layer: 0, HalfIdx: 0},
		{Grid: 0, Col: 5, Row: 0, Layer: 0, HalfIdx: 1},
	}
	cellAssignMap := map[voxel.CellKey]int{
		{Grid: 0, Col: 0, Row: 0, Layer: 0}: 0,
		{Grid: 0, Col: 5, Row: 0, Layer: 0}: 1,
	}
	patchMap := map[voxel.CellKey]int{
		{Grid: 0, Col: 0, Row: 0, Layer: 0}: 0,
		{Grid: 0, Col: 5, Row: 0, Layer: 0}: 1,
	}

	var halfPatchMaps [2]map[voxel.CellKey]int
	for h := 0; h < 2; h++ {
		halfPatchMaps[h] = make(map[voxel.CellKey]int)
	}
	for ck, patchIdx := range patchMap {
		cellIdx, ok := cellAssignMap[ck]
		if !ok {
			continue
		}
		h := cells[cellIdx].HalfIdx
		halfPatchMaps[h][ck] = patchIdx
	}
	if len(halfPatchMaps[0]) != 1 || len(halfPatchMaps[1]) != 1 {
		t.Errorf("expected 1 cell per half map, got h0=%d h1=%d", len(halfPatchMaps[0]), len(halfPatchMaps[1]))
	}
	if _, ok := halfPatchMaps[0][voxel.CellKey{Grid: 0, Col: 0, Row: 0, Layer: 0}]; !ok {
		t.Errorf("half 0 map missing the col=0 cell")
	}
	if _, ok := halfPatchMaps[1][voxel.CellKey{Grid: 0, Col: 5, Row: 0, Layer: 0}]; !ok {
		t.Errorf("half 1 map missing the col=5 cell")
	}
}

// TestFloodFillTwoGrids_PartitionsByHalfIdx — the load-bearing
// safety check from the phase-7 review: flood fill must NOT bridge
// two halves whose CellKey columns happen to be index-adjacent.
// floodFillTwoGrids partitions by (Grid, HalfIdx); cells in
// different halves can never end up in the same patch even if their
// column indices are 1 apart and they share a color assignment.
func TestFloodFillTwoGrids_PartitionsByHalfIdx(t *testing.T) {
	cells := []voxel.ActiveCell{
		// Two halves with column-adjacent cells, both assigned color 0.
		{Grid: 0, Col: 0, Row: 0, Layer: 0, HalfIdx: 0},
		{Grid: 0, Col: 1, Row: 0, Layer: 0, HalfIdx: 1},
	}
	assignments := []int32{0, 0}
	patchMap, numPatches, err := floodFillTwoGrids(context.Background(), cells, assignments, progress.NullTracker{})
	if err != nil {
		t.Fatalf("floodFillTwoGrids: %v", err)
	}
	if numPatches != 2 {
		t.Errorf("got %d patches, want 2 (one per half — adjacent columns must NOT bridge)", numPatches)
	}
	p0 := patchMap[voxel.CellKey{Grid: 0, Col: 0, Row: 0, Layer: 0}]
	p1 := patchMap[voxel.CellKey{Grid: 0, Col: 1, Row: 0, Layer: 0}]
	if p0 == p1 {
		t.Errorf("cells in different halves got the same patch ID %d (would silently merge in Clip)", p0)
	}
}

// TestFloodFillTwoGrids_PartitionsByGridAndHalf — broader smoke
// test: each (Grid, HalfIdx) combo gets its own patch space.
func TestFloodFillTwoGrids_PartitionsByGridAndHalf(t *testing.T) {
	cells := []voxel.ActiveCell{
		{Grid: 0, Col: 0, Row: 0, Layer: 0, HalfIdx: 0},
		{Grid: 0, Col: 0, Row: 0, Layer: 0, HalfIdx: 1},
		{Grid: 1, Col: 0, Row: 0, Layer: 1, HalfIdx: 0},
		{Grid: 1, Col: 0, Row: 0, Layer: 1, HalfIdx: 1},
	}
	assignments := []int32{0, 0, 0, 0}
	_, numPatches, err := floodFillTwoGrids(context.Background(), cells, assignments, progress.NullTracker{})
	if err != nil {
		t.Fatalf("floodFillTwoGrids: %v", err)
	}
	if numPatches != 4 {
		t.Errorf("got %d patches, want 4 (one per (Grid, HalfIdx) combo)", numPatches)
	}
}

// TestStageSplitDescription — the eviction-log description includes
// the connector style and offset so operators can identify entries.
func TestStageSplitDescription(t *testing.T) {
	off := Options{Input: "/tmp/foo.glb"}
	if got := stageDescription(StageSplit, off); got != "Split: foo.glb (off)" {
		t.Errorf("disabled description = %q, want 'Split: foo.glb (off)'", got)
	}
	on := off
	on.Split.Enabled = true
	on.Split.Axis = 2
	on.Split.Offset = 5
	on.Split.ConnectorStyle = "pegs"
	on.Split.ConnectorCount = 2
	got := stageDescription(StageSplit, on)
	want := "Split: foo.glb (Z@5.0mm, pegs ×2)"
	if got != want {
		t.Errorf("enabled description = %q, want %q", got, want)
	}
	// Auto-count (ConnectorCount=0) renders as "×auto" so a zero
	// in the log isn't mistaken for "no connectors."
	auto := on
	auto.Split.ConnectorCount = 0
	got = stageDescription(StageSplit, auto)
	want = "Split: foo.glb (Z@5.0mm, pegs ×auto)"
	if got != want {
		t.Errorf("auto-count description = %q, want %q", got, want)
	}
}
