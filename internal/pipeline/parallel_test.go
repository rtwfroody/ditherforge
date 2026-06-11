package pipeline

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestRunParallelPanicBecomesError: a panicking task must surface as
// an error (with the panic message preserved) instead of crashing the
// process — goroutine panics are unrecoverable anywhere else.
func TestRunParallelPanicBecomesError(t *testing.T) {
	err := runParallel(context.Background(), 4, 16, nil, func(i int, _ any) {
		if i == 7 {
			panic("boom on task 7")
		}
	})
	if err == nil {
		t.Fatal("expected an error from a panicking task")
	}
	if !strings.Contains(err.Error(), "boom on task 7") {
		t.Errorf("error should carry the panic message, got: %v", err)
	}
}

// TestRunParallelCancellation: workers must stop picking up tasks once
// the context is cancelled, and the call must report ctx.Err().
func TestRunParallelCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var ran atomic.Int64
	started := make(chan struct{})
	var once atomic.Bool
	err := runParallel(ctx, 2, 1000, nil, func(i int, _ any) {
		ran.Add(1)
		if once.CompareAndSwap(false, true) {
			close(started)
			cancel()
		}
		// Give cancellation a moment to be observed by the other
		// worker between tasks.
		<-started
		time.Sleep(time.Millisecond)
	})
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	// 1000 tasks queued; with 2 workers and a cancel on the first
	// task, only a handful can have started.
	if n := ran.Load(); n > 10 {
		t.Errorf("expected only in-flight tasks to run after cancel, got %d", n)
	}
}

// TestRunParallelCompletes: the happy path runs every task exactly once.
func TestRunParallelCompletes(t *testing.T) {
	var ran atomic.Int64
	if err := runParallel(context.Background(), 4, 100, nil, func(i int, _ any) {
		ran.Add(1)
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ran.Load() != 100 {
		t.Errorf("ran %d tasks, want 100", ran.Load())
	}
}
