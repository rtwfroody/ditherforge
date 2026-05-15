package pipeline

import "sync"

// runParallel fans n independent tasks out across workers goroutines.
// Each worker first calls newState (if non-nil) to allocate its own
// scratch buffer, then receives tasks from a shared queue and calls
// fn(i, state). Returns once every task has completed.
//
// Pass newState == nil when the task body is fully self-contained
// (no per-goroutine buffers).
//
// The helper exists for the Voxelize stage's per-slab fan-out, where
// each slab's work needs its own voxel.SearchBuf and slabs touch
// disjoint output slots so no further synchronization is required.
func runParallel(workers, n int, newState func(workerID int) any, fn func(i int, state any)) {
	if n <= 0 {
		return
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
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			var state any
			if newState != nil {
				state = newState(workerID)
			}
			for i := range jobs {
				fn(i, state)
			}
		}(w)
	}
	wg.Wait()
}
