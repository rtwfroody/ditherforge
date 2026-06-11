package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/rtwfroody/ditherforge/internal/progress"
)

// cancelLatencyBudget is the hard ceiling on how long a pipeline stage
// may keep running after its context is cancelled. The GUI cancels the
// in-flight run on every settings change; anything slower than this
// shows up as dead air before the new run's progress appears.
const cancelLatencyBudget = 1 * time.Second

// stageProgressSignal is a Tracker that closes ch on the FIRST
// StageProgress tick of the target stage — i.e. when the stage is not
// just started but measurably mid-work. Progress ticks arrive from
// concurrent workers, hence the once.
type stageProgressSignal struct {
	progress.NullTracker
	target string
	ch     chan struct{}
	once   sync.Once
}

func (t *stageProgressSignal) StageProgress(stage string, _ int) {
	if stage == t.target {
		t.once.Do(func() { close(t.ch) })
	}
}

// TestCancellationLatency guards the 1-second interruptibility
// contract on the pipeline's two slowest stages. For each stage it
// starts a cold-cache run, waits until the stage reports progress
// (mid-work), cancels the context, and asserts the run returns within
// cancelLatencyBudget.
//
// If this fails, some loop in the stage's call tree lost its ctx
// check — see runParallel, runClipJobs, SliceMeshProgress,
// SlabSurfaceFootprints, splitSrcBySlabs.
func TestCancellationLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (-short)")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	size := float32(50)
	opts := Options{
		Input:          filepath.Join(repoRoot, "tests", "objects", "earth.glb"),
		ObjectIndex:    -1,
		NumColors:      4,
		NozzleDiameter: 0.4,
		LayerHeight:    0.2,
		Scale:          1,
		Size:           &size,
		Force:          true,
		Dither:         "floyd-steinberg",
		ColorSnap:      5,
	}

	cases := []struct {
		stage StageID
		// run drives the pipeline far enough to execute the stage.
		run func(r *pipelineRun) error
	}{
		{StageVoxelize, func(r *pipelineRun) error { _, err := r.Voxelize(); return err }},
		{StageClip, func(r *pipelineRun) error { _, err := r.Clip(); return err }},
	}

	for _, tc := range cases {
		t.Run(stageNames[tc.stage], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sig := &stageProgressSignal{
				target: stageNames[tc.stage],
				ch:     make(chan struct{}),
			}
			r := &pipelineRun{
				ctx:     ctx,
				cache:   NewStageCache(), // cold: the stage must really run
				opts:    opts,
				tracker: sig,
			}

			done := make(chan error, 1)
			go func() { done <- tc.run(r) }()

			// Wait until the stage is measurably mid-work, then let it
			// get some distance in before pulling the plug.
			select {
			case <-sig.ch:
			case err := <-done:
				t.Fatalf("pipeline finished (err=%v) before %s reported progress — fixture too light for this test", err, stageNames[tc.stage])
			case <-time.After(5 * time.Minute):
				t.Fatalf("timed out waiting for %s to start", stageNames[tc.stage])
			}
			time.Sleep(200 * time.Millisecond)

			cancelAt := time.Now()
			cancel()
			select {
			case err := <-done:
				latency := time.Since(cancelAt)
				if err == nil {
					t.Fatalf("stage completed despite cancellation (raced to the finish?); latency measurement meaningless")
				}
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected context.Canceled, got: %v", err)
				}
				t.Logf("%s: cancel→return latency = %v", stageNames[tc.stage], latency)
				if latency > cancelLatencyBudget {
					t.Fatalf("%s took %v to honor cancellation; budget is %v", stageNames[tc.stage], latency, cancelLatencyBudget)
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("%s never returned after cancellation", stageNames[tc.stage])
			}
		})
	}
}
