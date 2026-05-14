package pipeline

import (
	"fmt"

	"github.com/rtwfroody/ditherforge/internal/cellslicer"
)

// WriteCellsDebugPNGs writes one PNG per slab (slab_NNNN.png) into
// dir, colored by each cell's sampled RGB. Requires a successful
// Voxelize stage in the supplied StageCache for opts; returns an
// error if the stage hasn't run yet.
//
// The dump is read-only and uses the cellslicer.WriteDebugPNGs
// implementation directly so test scripts and the CLI's
// --debug-cells-dir flag share one code path.
func WriteCellsDebugPNGs(cache *StageCache, opts Options, dir string) error {
	cache.WaitForDiskWrites()
	vo := cache.getVoxelize(opts)
	if vo == nil {
		return fmt.Errorf("voxelize stage has not run yet")
	}
	return cellslicer.WriteDebugPNGs(vo.CellSlabs, vo.CellSamples, dir, cellslicer.DebugPNGOptions{
		CellSizeMM:          vo.CellSize,
		FillBackgroundWhite: true,
		DrawEdges:           true,
	})
}
