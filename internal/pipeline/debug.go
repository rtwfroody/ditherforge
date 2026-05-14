package pipeline

import (
	"bytes"
	"fmt"
	"image"
	"image/png"

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

// CellsSlabPNG renders a single slab's cell partition (colored by
// each cell's sampled RGB) to a PNG-encoded byte slice. Returns the
// total slab count alongside the image so the GUI can drive a Z
// slider without needing a separate round-trip. The Voxelize stage
// for opts must have run; otherwise returns an error.
//
// Slabs with no footprint geometry return a 1×1 transparent PNG;
// callers can use the slab-count return value to know which indices
// are valid.
func CellsSlabPNG(cache *StageCache, opts Options, slabIdx int) (data []byte, slabCount int, err error) {
	cache.WaitForDiskWrites()
	vo := cache.getVoxelize(opts)
	if vo == nil {
		return nil, 0, fmt.Errorf("voxelize stage has not run yet — run the pipeline first")
	}
	slabCount = len(vo.CellSlabs)
	img := cellslicer.RenderSlabDebugImage(vo.CellSlabs, vo.CellSamples, slabIdx, cellslicer.DebugPNGOptions{
		CellSizeMM:          vo.CellSize,
		FillBackgroundWhite: true,
		DrawEdges:           true,
	})
	if img == nil {
		// Empty slab — encode a 1×1 transparent placeholder so the
		// GUI image element doesn't break.
		img = image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, slabCount, err
	}
	return buf.Bytes(), slabCount, nil
}
