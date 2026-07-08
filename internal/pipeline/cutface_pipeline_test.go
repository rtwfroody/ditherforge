package pipeline

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/progress"
)

// TestSplitCutFaceFlatFill drives the real split pipeline on a watertight
// cube and verifies the flat cut-face fill end to end:
//
//   - Voxelize flags cut-face cells over the GLOBAL cell set, and most of
//     them are HIDDEN (the cut exposes an interior surface, so visibility
//     drops them) — this is the population the fill targets;
//   - after Clip, every output face sourced from a hidden cut-face cell
//     carries a single palette index (the one flat filament), instead of
//     the noisy multi-colour join the user reported.
func TestSplitCutFaceFlatFill(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (-short): alpha-wrap + manifold clip")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	cubePath := filepath.Join(repoRoot, "tests", "objects", "cube.stl")

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
		Dither:         "floyd-steinberg",
		MeshRepair:     RepairAlphaWrap,
	}

	cache := NewStageCache()
	r0 := &pipelineRun{ctx: context.Background(), cache: cache, opts: base, tracker: progress.NullTracker{}}
	lo, err := r0.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	zMin, zMax := modelZRange(lo.Model)
	zMid := 0.5 * (zMin + zMax)

	opts := base
	opts.Split = SplitSettings{
		Enabled:        true,
		Axis:           2, // Z
		Offset:         float64(zMid),
		ConnectorStyle: "none",
	}

	r := &pipelineRun{ctx: context.Background(), cache: cache, opts: opts, tracker: progress.NullTracker{}}
	vo, err := r.Voxelize()
	if err != nil {
		t.Fatalf("voxelize: %v", err)
	}
	if len(vo.CutFace) != len(vo.CellSamples) {
		t.Fatalf("CutFace len %d != CellSamples len %d (must be global)", len(vo.CutFace), len(vo.CellSamples))
	}

	visible := make([]bool, len(vo.CellSamples))
	for _, gi := range vo.VisibleToCell {
		visible[gi] = true
	}
	nCut, nHidden := 0, 0
	for gi, c := range vo.CutFace {
		if c {
			nCut++
			if !visible[gi] {
				nHidden++
			}
		}
	}
	t.Logf("cut-face cells: %d (%d hidden) of %d global", nCut, nHidden, len(vo.CellSamples))
	if nCut == 0 {
		t.Fatal("no cut-face cells detected")
	}
	if nHidden == 0 {
		t.Fatal("no hidden cut-face cells — the interior cap should be hidden; nothing to flat-fill")
	}

	co, err := r.Clip()
	if err != nil {
		t.Fatalf("clip: %v", err)
	}

	// Every face from a hidden cut-face cell must use the one flat filament.
	hiddenColors := map[int32]int{}
	for i, gi := range co.ShellSectionIdx {
		if gi < 0 || int(gi) >= len(vo.CutFace) {
			continue
		}
		if vo.CutFace[gi] && !visible[gi] {
			hiddenColors[co.ShellAssignments[i]]++
		}
	}
	t.Logf("hidden cut-face faces span %d distinct palette indices: %v", len(hiddenColors), hiddenColors)
	if len(hiddenColors) == 0 {
		t.Fatal("no output faces sourced from hidden cut-face cells")
	}
	if len(hiddenColors) != 1 {
		t.Fatalf("hidden cut face uses %d filaments, want exactly 1 (flat fill failed)", len(hiddenColors))
	}
}
