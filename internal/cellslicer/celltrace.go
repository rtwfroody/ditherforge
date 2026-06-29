package cellslicer

import (
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// CellSampleRay records one jittered sample point's along-normal color
// ray for the Select Cell debug tool: the interior sample point (in the
// slab's own coordinate frame), the same point after colorXform (the
// frame the ray is actually cast in — they differ only for split
// halves), the ray geometry + hit (voxel.RayTrace), and whether it
// counted toward the cell's averaged color (RGBA alpha ≥ 128).
type CellSampleRay struct {
	Point      [3]float32
	ColorPoint [3]float32
	Trace      voxel.RayTrace
	Counted    bool
}

// CellTrace is the full color-sampling diagnostic for one cell,
// recomputed on demand by SampleCellTrace. AvgColor reproduces that
// cell's CellSample.Color exactly (same alpha-weighted average over the
// counted rays, same once-per-cell colorCorrect), so the debug dialog
// can show how the individual per-ray hits combined into the stored
// cell color. StartBack/Reach are the along-normal ray tuning the
// sampler used for this slab.
type CellTrace struct {
	SlabIdx   int
	CellIdx   int
	HalfIdx   byte
	Centroid  [3]float32
	Normal    [3]float32
	StartBack float32
	Reach     float32
	Rays      []CellSampleRay
	AnyAlpha  bool
	AvgColor  [3]uint8
}

// SampleCellTrace recomputes the color sampling for a single cell with
// full per-ray tracing. It mirrors SampleSlab's per-cell inner loop
// exactly — same interior sample points, same startBack/reach, same
// alpha-weighted average and once-per-cell colorCorrect — but uses
// voxel.SampleAlongNormalTrace to capture each ray, and takes the cell's
// outward normal directly (from the stored CellSample.Normal) rather
// than re-deriving it from the geom mesh. The result's AvgColor matches
// the cell's CellSample.Color produced by the original Voxelize run.
//
// buf/stickerBuf may be nil (allocated internally). cellSize feeds the
// search-radius default exactly as SampleSlab does.
func SampleCellTrace(
	slab *Slab, slabIdx, cellIdx int,
	normal [3]float32,
	model *loader.LoadedModel, si *voxel.SpatialIndex, cellSize, searchRadius float32,
	decals []*voxel.StickerDecal, stickerModel *loader.LoadedModel, stickerSI *voxel.SpatialIndex,
	override voxel.BaseColorOverride, colorXform func([3]float32) [3]float32,
	buf, stickerBuf *voxel.SearchBuf, colorBVH *voxel.RayBVH,
	colorCorrect func([3]uint8) [3]uint8,
) CellTrace {
	searchRadius = resolveSearchRadius(searchRadius, cellSize)
	if buf == nil {
		buf = voxel.NewSearchBuf(len(model.Faces))
	}
	if stickerModel != nil && stickerBuf == nil {
		stickerBuf = voxel.NewSearchBuf(len(stickerModel.Faces))
	}

	c := &slab.Cells[cellIdx]
	cx, cy, _ := polyCentroid(c.Outer)
	midZ := 0.5 * (slab.ZBot + slab.ZTop)
	startBack := (slab.ZTop - slab.ZBot) + rayBackMargin
	reach := rayReach
	points := cellInteriorSamplePoints(c.Outer, cx, cy, midZ)

	ct := CellTrace{
		SlabIdx:   slabIdx,
		CellIdx:   cellIdx,
		HalfIdx:   slab.HalfIdx,
		Centroid:  [3]float32{cx, cy, midZ},
		Normal:    normal,
		StartBack: startBack,
		Reach:     reach,
	}
	var rSum, gSum, bSum, wSum float32
	for _, p := range points {
		cp := p
		if colorXform != nil {
			cp = colorXform(p)
		}
		rgba, tr := voxel.SampleAlongNormalTrace(cp, normal, startBack, reach, model, colorBVH, si, searchRadius, buf, decals, stickerModel, stickerSI, stickerBuf, override)
		counted := rgba[3] >= 128
		if counted {
			ct.AnyAlpha = true
			w := float32(rgba[3]) / 255
			rSum += float32(rgba[0]) * w
			gSum += float32(rgba[1]) * w
			bSum += float32(rgba[2]) * w
			wSum += w
		}
		ct.Rays = append(ct.Rays, CellSampleRay{Point: p, ColorPoint: cp, Trace: tr, Counted: counted})
	}
	if wSum > 0 {
		ct.AvgColor = [3]uint8{
			uint8(clampF(rSum/wSum, 0, 255) + 0.5),
			uint8(clampF(gSum/wSum, 0, 255) + 0.5),
			uint8(clampF(bSum/wSum, 0, 255) + 0.5),
		}
		if colorCorrect != nil {
			ct.AvgColor = colorCorrect(ct.AvgColor)
		}
	}
	return ct
}

// FindCellAt locates the cell whose prism contains the point (x,y,z) in
// the cells' own coordinate frame (pipeline-mm unsplit, bed-mm split).
// It picks the slab whose [ZBot,ZTop] spans z, then the cell whose Outer
// polygon contains (x,y). For split models both halves share Z ranges,
// so the XY test disambiguates the half. Returns slabIdx, cellIdx and
// ok=false when no cell contains the point. zEps widens the Z span test
// so a click landing exactly on a slab boundary still resolves.
func FindCellAt(slabs []Slab, x, y, z, zEps float32) (slabIdx, cellIdx int, ok bool) {
	for si := range slabs {
		s := &slabs[si]
		if z < s.ZBot-zEps || z > s.ZTop+zEps {
			continue
		}
		for ci := range s.Cells {
			if pointInPolygon(s.Cells[ci].Outer, x, y) {
				return si, ci, true
			}
		}
	}
	return 0, 0, false
}
