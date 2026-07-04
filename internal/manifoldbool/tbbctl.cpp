//go:build tbbcontrol

// Process-wide TBB parallelism control. Manifold's Boolean uses TBB
// internally; when many independent booleans run concurrently from a Go
// worker pool, letting each one also fan out across TBB oversubscribes the
// cores and the scheduling overhead dominates. df_set_tbb_max lets the
// caller pin total TBB parallelism (e.g. to 1, so each boolean runs
// sequentially and the Go pool supplies all the parallelism).
//
// The tbb::global_control object must outlive the region it governs, so it
// is heap-allocated and kept in a static. Callers serialise entry (see the
// Go-side mutex in SetMaxParallelism), so the static needs no lock here.
#include <tbb/global_control.h>
#include <cstddef>

extern "C" void df_set_tbb_max(int n) {
  static tbb::global_control *gc = nullptr;
  if (gc) {
    delete gc;
    gc = nullptr;
  }
  if (n > 0) {
    gc = new tbb::global_control(
        tbb::global_control::max_allowed_parallelism, (std::size_t)n);
  }
}
