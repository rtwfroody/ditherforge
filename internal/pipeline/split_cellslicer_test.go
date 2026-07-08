package pipeline

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// TestSplitCellslicer_TwoHalves drives the cellslicer pipeline with
// Split enabled on a watertight cube and checks the structural
// invariants the downstream Merge/Export path relies on:
//
//   - the merge output carries a per-face HalfIdx parallel to the
//     faces, with both halves present;
//   - half-0 faces are emitted before half-1 faces (mergeSplitFaces
//     assumes that contiguity);
//   - the two halves share a single vertex table by disjoint index
//     ranges (every face index is in range);
//   - both halves carry a non-trivial amount of geometry.
//
// Color correctness is NOT asserted here — that's the colorXform path,
// exercised separately. See docs/split-cellslicer.md.
func TestSplitCellslicer_TwoHalves(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (-short): alpha-wrap + manifold clip")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	cubePath := filepath.Join(repoRoot, "tests", "objects", "cube.stl")

	// 20mm keeps both halves well above the 100-face floor asserted below
	// while cutting the cell count (∝ size²) — and thus the per-half
	// voxelize+manifold-clip time — to roughly a sixth of the 50mm cost.
	size := float32(20)
	base := Options{
		Input:          cubePath,
		ObjectIndex:    -1,
		NumColors:      4,
		NozzleDiameter: 0.4,
		LayerHeight:    0.2,
		Scale:          1,
		Size:           &size,
		Force:          true,
		Dither:         "none",
		MeshRepair:     RepairAlphaWrap,
	}

	cache := NewStageCache()

	// First load to find the model's Z range so we cut through the
	// middle. Load rests the model on z=0, so the cut offset is the
	// mid-height.
	r0 := &pipelineRun{ctx: context.Background(), cache: cache, opts: base, tracker: progress.NullTracker{}}
	lo, err := r0.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	zMin, zMax := modelZRange(lo.Model)
	zMid := 0.5 * (zMin + zMax)
	t.Logf("cube Z range [%.2f, %.2f], cutting Z@%.2f", zMin, zMax, zMid)

	opts := base
	opts.Split = SplitSettings{
		Enabled:        true,
		Axis:           2, // Z
		Offset:         float64(zMid),
		ConnectorStyle: "none",
	}

	r := &pipelineRun{ctx: context.Background(), cache: cache, opts: opts, tracker: progress.NullTracker{}}
	mo, err := r.Merge()
	if err != nil {
		t.Fatalf("merge (split): %v", err)
	}

	if mo.ShellHalfIdx == nil {
		t.Fatal("ShellHalfIdx is nil — split halves were not tagged")
	}
	if len(mo.ShellHalfIdx) != len(mo.ShellFaces) {
		t.Fatalf("ShellHalfIdx len %d != ShellFaces len %d", len(mo.ShellHalfIdx), len(mo.ShellFaces))
	}

	// Count per-half faces and verify half 0 precedes half 1.
	var nHalf [2]int
	sawOne := false
	for i, h := range mo.ShellHalfIdx {
		if h > 1 {
			t.Fatalf("face %d has HalfIdx %d (expected 0 or 1)", i, h)
		}
		nHalf[h]++
		if h == 1 {
			sawOne = true
		} else if sawOne {
			t.Fatalf("half-0 face %d appears after a half-1 face — mergeSplitFaces requires contiguous halves", i)
		}
	}
	t.Logf("output faces: half0=%d half1=%d total=%d verts=%d", nHalf[0], nHalf[1], len(mo.ShellFaces), len(mo.ShellVerts))

	if nHalf[0] < 100 || nHalf[1] < 100 {
		t.Fatalf("a half has too few faces (half0=%d half1=%d) — cut likely failed", nHalf[0], nHalf[1])
	}

	// Every face index must be in range (unified vertex table).
	nv := uint32(len(mo.ShellVerts))
	for i, f := range mo.ShellFaces {
		if f[0] >= nv || f[1] >= nv || f[2] >= nv {
			t.Fatalf("face %d references vertex out of range (%v, nv=%d)", i, f, nv)
		}
	}

	// ClipMeshToCellsManifold emits a colored surface shell (prism
	// walls filtered), so open edges along the cut/cap are expected in
	// both the split and unsplit outputs — exact watertightness is not
	// the signal. Splitting legitimately adds some: the two cap
	// perimeters along the cut plane plus the seam where the halves'
	// shells meet. What we guard against is a *blow-up* — a regression
	// that drops whole slabs or fails to re-integrate a half would
	// multiply the open-edge population several-fold.
	wt := voxel.CheckWatertight(mo.ShellFaces)
	t.Logf("split output: %s", wt.String())

	// Compare against the unsplit run on the same model.
	rNo := &pipelineRun{ctx: context.Background(), cache: cache, opts: base, tracker: progress.NullTracker{}}
	moNo, err := rNo.Merge()
	if err != nil {
		t.Fatalf("merge (unsplit): %v", err)
	}
	if moNo.ShellHalfIdx != nil {
		t.Fatal("unsplit run produced a non-nil ShellHalfIdx")
	}
	wtNo := voxel.CheckWatertight(moNo.ShellFaces)
	t.Logf("unsplit output: %s", wtNo.String())

	// The cap-perimeter overhead is modest (measured ~1.26× boundary
	// edges at the time of writing). The 2× ceiling is a deliberately
	// generous catastrophe detector, not a tight bound: it tolerates
	// the legitimate cut overhead while still failing loudly if a half
	// is dropped or mis-reintegrated and the shell opens up wholesale.
	splitBoundary := len(wt.BoundaryEdges)
	unsplitBoundary := len(wtNo.BoundaryEdges)
	if splitBoundary > 2*unsplitBoundary {
		t.Errorf("split introduced too many boundary edges: split=%d unsplit=%d (>2× — a slab/half was likely dropped)",
			splitBoundary, unsplitBoundary)
	}
}
