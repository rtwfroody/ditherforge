//go:build tbbcontrol

// This TBB-backed implementation is compiled only under the `tbbcontrol`
// build tag, which requires the process to link a TBB that Manifold's
// Boolean actually uses (i.e. Manifold built with MANIFOLD_PAR=ON). The
// default build (and the CI/release binaries, whose Manifold is built with
// MANIFOLD_PAR=OFF) uses the no-op in tbbctl_off.go, so no TBB headers or
// library are required to build.
package manifoldbool

/*
#cgo CXXFLAGS: -std=c++17
#cgo LDFLAGS: -ltbb
extern void df_set_tbb_max(int n);
*/
import "C"

import "sync"

// tbbMu serialises df_set_tbb_max, whose C++ side keeps the live
// tbb::global_control in a static it deletes and recreates on each call.
var tbbMu sync.Mutex

// SetMaxParallelism pins the total number of threads Manifold's internal
// TBB backend may use across the whole process. n <= 0 removes the cap
// (TBB reverts to its default, hardware-concurrency parallelism).
//
// The clip stage sets this to 1 while it runs its own worker pool over
// many independent booleans: with each boolean sequential, the Go pool
// supplies all the parallelism and TBB adds no per-op scheduling overhead
// or oversubscription. It does not affect output — Manifold's Booleans are
// deterministic regardless of thread count.
func SetMaxParallelism(n int) {
	tbbMu.Lock()
	defer tbbMu.Unlock()
	C.df_set_tbb_max(C.int(n))
}
