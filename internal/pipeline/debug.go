package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"

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
	if pre := cache.getPreload(opts); pre != nil {
		opts = applyFractionalOptions(opts, float64(pre.ScaledMaxExtentMM))
	}
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

// CellsSlabSVG renders a single slab's cell partition (colored by
// each cell's sampled RGB) to an SVG document. Returns the total
// slab count alongside the markup so the GUI can drive a Z slider
// without a separate round-trip. The Voxelize stage for opts must
// have run; otherwise returns an error. Empty slabs return an empty
// string with no error — the slabCount return tells callers which
// indices have geometry.
func CellsSlabSVG(cache *StageCache, opts Options, slabIdx int) (svg string, slabCount int, medianAreaMM2 float32, err error) {
	cache.WaitForDiskWrites()
	// Resolve size-relative opts to the absolute mm the run keyed its blobs
	// under (see ExportFile); without this the Voxelize lookup misses.
	if pre := cache.getPreload(opts); pre != nil {
		opts = applyFractionalOptions(opts, float64(pre.ScaledMaxExtentMM))
	}
	vo := cache.getVoxelize(opts)
	if vo == nil {
		return "", 0, 0, fmt.Errorf("voxelize stage has not run yet — run the pipeline first")
	}
	slabCount = len(vo.CellSlabs)
	if slabIdx >= 0 && slabIdx < len(vo.CellSlabs) {
		medianAreaMM2 = vo.CellSlabs[slabIdx].MedianCellAreaMM2()
	}
	svg = cellslicer.RenderSlabDebugSVG(vo.CellSlabs, vo.CellSamples, slabIdx, cellslicer.DebugSVGOptions{
		CellSizeMM:          vo.CellSize,
		FillBackgroundWhite: true,
		DrawEdges:           true,
		DrawFootprint:       true,
		DrawContours:        true,
		HighlightUncovered:  true,
	})
	dumpSlabIfRequested(vo.CellSlabs, slabIdx)
	return svg, slabCount, medianAreaMM2, nil
}

// dumpSlabIfRequested writes a JSON snapshot of slab `slabIdx` to the
// path given by env $CELLSLICER_DUMP_PATH, but only when the env
// $CELLSLICER_DUMP_SLAB matches the current slabIdx as a decimal int.
// Used to capture polar slabs from the GUI for isolated unit tests.
func dumpSlabIfRequested(slabs []cellslicer.Slab, slabIdx int) {
	want := os.Getenv("CELLSLICER_DUMP_SLAB")
	if want == "" {
		return
	}
	var wantIdx int
	if _, err := fmt.Sscanf(want, "%d", &wantIdx); err != nil || wantIdx != slabIdx {
		return
	}
	if slabIdx < 0 || slabIdx >= len(slabs) {
		return
	}
	out := os.Getenv("CELLSLICER_DUMP_PATH")
	if out == "" {
		out = filepath.Join(os.TempDir(), fmt.Sprintf("slab_%d.json", slabIdx))
	}
	s := slabs[slabIdx]
	type loopJSON struct {
		Points [][2]float32 `json:"points"`
		IsHole bool         `json:"is_hole"`
		Z      float32      `json:"z,omitempty"`
	}
	type fpJSON struct {
		Loops []loopJSON `json:"loops"`
	}
	dumpLoops := func(loops []cellslicer.Loop) []loopJSON {
		ls := make([]loopJSON, 0, len(loops))
		for _, lp := range loops {
			ll := loopJSON{IsHole: lp.IsHole, Z: lp.Z}
			ll.Points = make([][2]float32, len(lp.Points))
			for i, p := range lp.Points {
				ll.Points[i] = [2]float32{p[0], p[1]}
			}
			ls = append(ls, ll)
		}
		return ls
	}
	dumpFP := func(fp *cellslicer.Footprint) *fpJSON {
		if fp == nil {
			return nil
		}
		out := &fpJSON{}
		for _, lp := range fp.Loops {
			ll := loopJSON{IsHole: lp.IsHole}
			ll.Points = make([][2]float32, len(lp.Points))
			for i, p := range lp.Points {
				ll.Points[i] = [2]float32{p[0], p[1]}
			}
			out.Loops = append(out.Loops, ll)
		}
		return out
	}
	payload := struct {
		SlabIndex int        `json:"slab_index"`
		ZBot      float32    `json:"z_bot"`
		ZTop      float32    `json:"z_top"`
		BotLoops  []loopJSON `json:"bot_loops"`
		TopLoops  []loopJSON `json:"top_loops"`
		Footprint *fpJSON    `json:"footprint"`
		FpBelow   *fpJSON    `json:"fp_below"`
		FpAbove   *fpJSON    `json:"fp_above"`
	}{
		SlabIndex: s.Index,
		ZBot:      s.ZBot,
		ZTop:      s.ZTop,
		Footprint: dumpFP(s.Footprint),
	}
	if s.BotLayer != nil {
		payload.BotLoops = dumpLoops(s.BotLayer.Loops)
	}
	if s.TopLayer != nil {
		payload.TopLoops = dumpLoops(s.TopLayer.Loops)
	}
	if slabIdx > 0 && slabs[slabIdx-1].Footprint != nil {
		payload.FpBelow = dumpFP(slabs[slabIdx-1].Footprint)
	}
	if slabIdx+1 < len(slabs) && slabs[slabIdx+1].Footprint != nil {
		payload.FpAbove = dumpFP(slabs[slabIdx+1].Footprint)
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	_ = os.WriteFile(out, data, 0644)
}
