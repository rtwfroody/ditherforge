package pipeline

import (
	"context"
	"fmt"
	"math"

	"github.com/rtwfroody/ditherforge/internal/cellslicer"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// CellDiagRay is one jittered sample point's along-normal color ray in a
// CellDiagInfo. All coordinates are in the cell frame (pipeline-mm for
// unsplit models, bed-mm for split). ColorPoint is Point mapped into the
// color-model frame (differs from Point only for split halves); the ray
// is cast from there.
type CellDiagRay struct {
	Point      [3]float32 `json:"point"`
	ColorPoint [3]float32 `json:"colorPoint"`
	BVHUsed    bool       `json:"bvhUsed"`
	Origin     [3]float32 `json:"origin"`
	Dir        [3]float32 `json:"dir"`
	MaxT       float32    `json:"maxT"`
	Hit        bool       `json:"hit"`
	HitTri     int32      `json:"hitTri"`
	HitT       float32    `json:"hitT"`
	HitPoint   [3]float32 `json:"hitPoint"`
	Fallback   bool       `json:"fallback"`
	Color      [4]uint8   `json:"color"`
	Counted    bool       `json:"counted"`
}

// CellDiagInfo is the Select Cell debug payload: which cell the click
// resolved to plus the full per-ray color-sampling diagnostics,
// recomputed on demand from the cached Voxelize partition. StoredColor
// is the color the Voxelize run recorded for the cell; RecomputedColor
// is the same averaging redone here and should match it bit-for-bit.
type CellDiagInfo struct {
	Found   bool `json:"found"`
	SlabIdx int  `json:"slabIdx"`
	CellIdx int  `json:"cellIdx"`
	HalfIdx int  `json:"halfIdx"`
	Split   bool `json:"split"`

	SlabZBot float32    `json:"slabZBot"`
	SlabZTop float32    `json:"slabZTop"`
	Centroid [3]float32 `json:"centroid"`
	Normal   [3]float32 `json:"normal"`
	AreaMM2  float32    `json:"areaMM2"`

	StartBack float32 `json:"startBack"`
	Reach     float32 `json:"reach"`

	StoredColor     [3]uint8 `json:"storedColor"`
	StoredAlpha     bool     `json:"storedAlpha"`
	RecomputedColor [3]uint8 `json:"recomputedColor"`
	RecomputedAlpha bool     `json:"recomputedAlpha"`

	PreviewScale     float32    `json:"previewScale"`
	PickPointPreview [3]float32 `json:"pickPointPreview"`
	PickPointCell    [3]float32 `json:"pickPointCell"`
	MatchedByNearest bool       `json:"matchedByNearest"`

	Rays []CellDiagRay `json:"rays"`
}

// CellDiagnosticsAt resolves a picked output-mesh point (in preview-mm,
// the frame the frontend raycasts in) to a cell from the cached Voxelize
// partition and recomputes that cell's color-sampling rays with full
// tracing. Requires the pipeline to have run through Voxelize for opts.
//
// The expensive part — rebuilding the color BVH + per-face visibility —
// is redone on every call (a few seconds on large models); acceptable
// for an interactive debug click and it guarantees the rays match the
// run exactly.
func CellDiagnosticsAt(cache *StageCache, opts Options, pick [3]float32) (*CellDiagInfo, error) {
	cache.WaitForDiskWrites()
	pre := cache.getPreload(opts)
	if pre == nil {
		return nil, fmt.Errorf("pipeline has not run yet — run it first")
	}
	previewScale := pre.PreviewScale
	if previewScale == 0 {
		previewScale = 1
	}
	// Resolve size-relative opts to the absolute mm the run keyed its
	// Voxelize blob under (see CellsSlabSVG / ExportFile).
	opts = applyFractionalOptions(opts, float64(pre.ScaledMaxExtentMM))

	vo := cache.getVoxelize(opts)
	if vo == nil {
		return nil, fmt.Errorf("voxelize stage has not run yet — run the pipeline first")
	}
	lo := cache.getLoad(opts)
	if lo == nil || lo.ColorModel == nil {
		return nil, fmt.Errorf("load stage has not run yet — run the pipeline first")
	}

	// preview-mm → cell frame (pipeline-mm unsplit, bed-mm split).
	cellPt := [3]float32{pick[0] / previewScale, pick[1] / previewScale, pick[2] / previewScale}

	info := &CellDiagInfo{
		PreviewScale:     previewScale,
		PickPointPreview: pick,
		PickPointCell:    cellPt,
	}

	// Locate the cell: slab by Z, then XY point-in-Outer. Fall back to
	// the nearest cell centroid when the point lands just outside every
	// polygon (curved walls, off-grid clicks).
	slabIdx, cellIdx, ok := cellslicer.FindCellAt(vo.CellSlabs, cellPt[0], cellPt[1], cellPt[2], 1e-3)
	if !ok {
		slabIdx, cellIdx, ok = nearestCell(vo.CellSamples, cellPt)
		info.MatchedByNearest = ok
	}
	if !ok {
		return info, nil // Found stays false
	}
	if slabIdx < 0 || slabIdx >= len(vo.CellSlabs) {
		return info, nil
	}
	slab := &vo.CellSlabs[slabIdx]
	if cellIdx < 0 || cellIdx >= len(slab.Cells) {
		return info, nil
	}

	sample, haveSample := findSample(vo.CellSamples, slabIdx, cellIdx)

	info.Found = true
	info.SlabIdx = slabIdx
	info.CellIdx = cellIdx
	info.SlabZBot = slab.ZBot
	info.SlabZTop = slab.ZTop
	if haveSample {
		info.HalfIdx = int(sample.HalfIdx)
		info.Normal = sample.Normal
		info.AreaMM2 = sample.Area
		info.StoredColor = sample.Color
		info.StoredAlpha = sample.Alpha
	}

	// Rebuild the sampling inputs exactly as the Voxelize stage does.
	r := &pipelineRun{ctx: context.Background(), cache: cache, opts: opts, tracker: progress.NullTracker{}}
	colorModel := lo.ColorModel
	colorBVH, faceVis, err := computeFaceVisibility(r.ctx, colorModel, func(int, int) {})
	if err != nil {
		return nil, fmt.Errorf("rebuilding color visibility: %w", err)
	}
	spatial := voxel.NewSpatialIndex(colorModel, vo.CellSize)
	spatial.FaceVisible = faceVis

	override, _ := cache.baseColorOverride(
		opts.BaseColorMaterialX,
		opts.BaseColorMaterialXTileMM,
		opts.BaseColorMaterialXTriplanarSharpness,
		progress.NullTracker{},
	)
	colorCorrect, err := r.buildColorCorrect()
	if err != nil {
		return nil, fmt.Errorf("building color correction: %w", err)
	}

	var (
		stickerModel *loader.LoadedModel
		stickerSI    *voxel.SpatialIndex
		decals       []*voxel.StickerDecal
	)
	if stk, _ := r.Sticker(); stk != nil && len(stk.Decals) > 0 {
		stickerModel = stk.Model
		stickerSI = stk.ensureSI()
		decals = stk.Decals
	}

	var colorXform func([3]float32) [3]float32
	if spl, _ := r.Split(); spl != nil && spl.Enabled {
		info.Split = true
		h := info.HalfIdx
		if h >= 0 && h < len(spl.Xform) {
			colorXform = spl.Xform[h].ApplyInverse
		}
	}

	normal := info.Normal
	ct := cellslicer.SampleCellTrace(
		slab, slabIdx, cellIdx, normal,
		colorModel, spatial, vo.CellSize, 0,
		decals, stickerModel, stickerSI,
		override, colorXform, nil, nil, colorBVH, colorCorrect,
	)

	info.Centroid = ct.Centroid
	info.StartBack = ct.StartBack
	info.Reach = ct.Reach
	info.RecomputedColor = ct.AvgColor
	info.RecomputedAlpha = ct.AnyAlpha
	if !haveSample {
		info.Normal = ct.Normal
	}
	info.Rays = make([]CellDiagRay, 0, len(ct.Rays))
	for _, rr := range ct.Rays {
		info.Rays = append(info.Rays, CellDiagRay{
			Point:      rr.Point,
			ColorPoint: rr.ColorPoint,
			BVHUsed:    rr.Trace.BVHUsed,
			Origin:     rr.Trace.Origin,
			Dir:        rr.Trace.Dir,
			MaxT:       rr.Trace.MaxT,
			Hit:        rr.Trace.Hit,
			HitTri:     rr.Trace.HitTri,
			HitT:       rr.Trace.HitT,
			HitPoint:   rr.Trace.HitPoint,
			Fallback:   rr.Trace.Fallback,
			Color:      rr.Trace.Color,
			Counted:    rr.Counted,
		})
	}
	return info, nil
}

// findSample returns the CellSample for (slabIdx, cellIdx) from the
// flattened CellSamples list.
func findSample(samples []cellslicer.CellSample, slabIdx, cellIdx int) (cellslicer.CellSample, bool) {
	for i := range samples {
		if samples[i].SlabIdx == slabIdx && samples[i].CellIdx == cellIdx {
			return samples[i], true
		}
	}
	return cellslicer.CellSample{}, false
}

// nearestCell returns the slab/cell index of the CellSample whose
// centroid is closest to p, used when no cell polygon contains the
// picked point.
func nearestCell(samples []cellslicer.CellSample, p [3]float32) (slabIdx, cellIdx int, ok bool) {
	best := float32(math.MaxFloat32)
	for i := range samples {
		c := samples[i].Centroid
		dx, dy, dz := c[0]-p[0], c[1]-p[1], c[2]-p[2]
		d := dx*dx + dy*dy + dz*dz
		if d < best {
			best = d
			slabIdx = samples[i].SlabIdx
			cellIdx = samples[i].CellIdx
			ok = true
		}
	}
	return slabIdx, cellIdx, ok
}
