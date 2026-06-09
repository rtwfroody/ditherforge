package pipeline

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/rtwfroody/ditherforge/internal/progress"
)

// TestInteriorFaceFootprintTiming A/B-times the Voxelize stage with the
// interior-horizontal-face footprint augmentation on vs. off, on the
// low-poly building. Load (alpha-wrap) is shared via one StageCache so
// only the Voxelize delta is measured. Run with:
//
//	DF_FP_TIMING=1 go test ./internal/pipeline/ -run TestInteriorFaceFootprintTiming -v -count=1
func TestInteriorFaceFootprintTiming(t *testing.T) {
	if os.Getenv("DF_FP_TIMING") == "" {
		t.Skip("set DF_FP_TIMING=1 to run")
	}
	size := float32(50)
	base := Options{
		Input:          "../../tests/objects/low_poly_building.glb",
		ObjectIndex:    -1,
		NumColors:      6,
		NozzleDiameter: 0.4,
		LayerHeight:    0.2,
		Dither:         "riemersma",
		Force:          true,
		Scale:          1,
		Size:           &size,
		AlphaWrap:      true,
	}
	cache := NewStageCache()

	voxelizeOnce := func(noInterior bool) (time.Duration, int) {
		opts := base
		opts.NoInteriorFaceFootprint = noInterior
		r := &pipelineRun{ctx: context.Background(), cache: cache, opts: opts, tracker: progress.NullTracker{}}
		if _, err := r.Load(); err != nil { // warm shared Load cache
			t.Fatalf("Load: %v", err)
		}
		t0 := time.Now()
		vo, err := r.Voxelize()
		if err != nil {
			t.Fatalf("Voxelize: %v", err)
		}
		return time.Since(t0), len(vo.CellSamples)
	}

	const reps = 3
	for i := 0; i < reps; i++ {
		// Fresh cache per rep so Voxelize is a real miss each time; Load is
		// recomputed once per rep but excluded from the timed region.
		cache = NewStageCache()
		offDur, offN := voxelizeOnce(true)
		cache = NewStageCache()
		onDur, onN := voxelizeOnce(false)
		t.Logf("rep %d: OFF %6.0fms (%d samples) | ON %6.0fms (%d samples) | delta %+.0fms (%+.1f%%), +%d samples",
			i, offDur.Seconds()*1000, offN, onDur.Seconds()*1000, onN,
			(onDur-offDur).Seconds()*1000, 100*(onDur.Seconds()-offDur.Seconds())/offDur.Seconds(), onN-offN)
	}
}
