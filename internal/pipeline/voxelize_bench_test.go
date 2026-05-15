package pipeline

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/rtwfroody/ditherforge/internal/progress"
)

// TestVoxelizeBench drives the Voxelize stage on each test model
// and prints wall-time plus the new substep timing log line. Run
// manually with:
//
//	go test ./internal/pipeline -run TestVoxelizeBench -v -count=1
//
// Skipped under `-short` so it stays out of the normal test loop.
// Uses a fresh in-memory StageCache per model to defeat caching
// — we want the *cold* cost of Voxelize each time.
func TestVoxelizeBench(t *testing.T) {
	if testing.Short() {
		t.Skip("benchmark (-short)")
	}

	models := []string{
		"earth.glb",
		"glyphid_praetorian.glb",
		"low_poly_building.glb",
	}

	// tests/objects/ lives at the repo root, two levels above this
	// test's package directory.
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	size := float32(50)
	for _, name := range models {
		t.Run(name, func(t *testing.T) {
			opts := Options{
				Input:                 filepath.Join(repoRoot, "tests", "objects", name),
				ObjectIndex:           -1,
				NumColors:             4,
				NozzleDiameter:        0.4,
				LayerHeight:           0.2,
				Scale:                 1,
				Size:                  &size,
				Force:                 true,
				Dither:                "riemersma",
				ColorSnap:             5,
				ShowSampledColors:     true,
				Layer0AdhesionXYScale: 2,
				UpperLayerXYScale:     1.25,
			}

			cache := NewStageCache()
			r := &pipelineRun{
				ctx:     context.Background(),
				cache:   cache,
				opts:    opts,
				tracker: progress.NullTracker{},
			}

			t0 := time.Now()
			vo, err := r.Voxelize()
			elapsed := time.Since(t0)
			if err != nil {
				t.Fatalf("voxelize: %v", err)
			}
			t.Logf("%-28s slabs=%d cells=%d wall=%.2fs",
				name, len(vo.CellSlabs), len(vo.Cells), elapsed.Seconds())
		})
	}
}
