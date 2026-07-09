package pipeline

import (
	"context"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// TestCellSplatPreviewEmitted drives the real pipeline on a cube cold (forced
// cache miss) with an OnOutputPreviewMesh callback and asserts that at least
// one preview mesh carrying more than one distinct face color arrives — i.e.
// the instant colored splat preview fires before Clip + Merge finish. The
// grey silhouette previews from earlier stages are a single color, so a
// multi-color preview can only be the dithered splat cloud.
func TestCellSplatPreviewEmitted(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (-short): alpha-wrap + manifold clip")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	cubePath := filepath.Join(repoRoot, "tests", "objects", "cube.stl")

	size := float32(20)
	opts := Options{
		Input:          cubePath,
		ObjectIndex:    -1,
		NumColors:      4,
		NozzleDiameter: 0.4,
		LayerHeight:    0.2,
		Scale:          1,
		Size:           &size,
		Force:          true, // cold: force a cache miss so stage bodies run
		Dither:         "floyd-steinberg",
		MeshRepair:     RepairAlphaWrap,
	}

	var (
		mu           sync.Mutex
		maxDistinct  int
		previewCount int
	)
	cb := &Callbacks{
		OnOutputPreviewMesh: func(md *MeshData, _ float32) {
			mu.Lock()
			defer mu.Unlock()
			previewCount++
			seen := map[[3]uint16]struct{}{}
			for i := 0; i+2 < len(md.FaceColors); i += 3 {
				seen[[3]uint16{md.FaceColors[i], md.FaceColors[i+1], md.FaceColors[i+2]}] = struct{}{}
			}
			if len(seen) > maxDistinct {
				maxDistinct = len(seen)
			}
		},
	}

	if _, err := RunCached(context.Background(), NewStageCache(), opts, cb); err != nil {
		t.Fatalf("RunCached: %v", err)
	}

	if previewCount == 0 {
		t.Fatal("no output preview meshes were emitted")
	}
	if maxDistinct < 2 {
		t.Fatalf("no multi-color preview emitted (max distinct face colors = %d); "+
			"expected the colored cell-splat preview before clip", maxDistinct)
	}
}
