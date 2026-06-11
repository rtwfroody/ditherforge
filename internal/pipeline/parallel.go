package pipeline

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
)

// runParallel fans n independent tasks out across workers goroutines.
// Each worker first calls newState (if non-nil) to allocate its own
// scratch buffer, then receives tasks from a shared queue and calls
// fn(i, state). Returns once every task has completed.
//
// Cancellation: each worker checks ctx before starting a task and
// stops picking up new ones once ctx is done. Already-started tasks
// run to completion (a task is one slab's worth of work — well under
// the 1s cancellation budget), so the output slots written so far are
// consistent but incomplete. Returns ctx.Err() when cancelled; the
// caller must treat the outputs as garbage in that case.
//
// Panic containment: a panic in fn (or newState) is captured as an
// error — with the stack preserved in the message — instead of
// crashing the process. A goroutine panic is otherwise unrecoverable
// by the stage-level recover in runStageCached, so without this a
// single bad slab would take down the whole app. The first panic wins;
// the remaining workers stop picking up tasks. When a panic and a
// cancellation coincide, the panic error is returned — it carries the
// stack of a real bug, while ctx.Err() carries nothing; the GUI still
// reports "cancelled" either way (processOne checks ctx, not the error
// value), so prioritizing the panic only improves the log.
//
// Pass newState == nil when the task body is fully self-contained
// (no per-goroutine buffers).
//
// The helper exists for the Voxelize stage's per-slab fan-out, where
// each slab's work needs its own voxel.SearchBuf and slabs touch
// disjoint output slots so no further synchronization is required.
func runParallel(ctx context.Context, workers, n int, newState func(workerID int) any, fn func(i int, state any)) error {
	if n <= 0 {
		return ctx.Err()
	}
	if workers < 1 {
		workers = 1
	}
	if workers > n {
		workers = n
	}
	jobs := make(chan int, n)
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)
	var (
		wg      sync.WaitGroup
		panicMu sync.Mutex
		panicE  error
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicMu.Lock()
					if panicE == nil {
						panicE = fmt.Errorf("runParallel worker panic: %v\n%s", r, debug.Stack())
					}
					panicMu.Unlock()
				}
			}()
			var state any
			if newState != nil {
				state = newState(workerID)
			}
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				panicMu.Lock()
				stop := panicE != nil
				panicMu.Unlock()
				if stop {
					return
				}
				fn(i, state)
			}
		}(w)
	}
	wg.Wait()
	if panicE != nil {
		return panicE
	}
	return ctx.Err()
}
