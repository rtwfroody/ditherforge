package cellslicer

import (
	"math"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// CellSample is the per-cell color sample produced by SampleCells.
// SlabIdx + CellIdx identify the source cell in slabs[SlabIdx].Cells.
// Centroid is in mesh coords; Area is the cell polygon's XY area in
// mm² (so downstream palette/dither code can weight cells by extent).
// Alpha is true when at least one sample point hit a visible surface
// (RGBA alpha ≥ 128); cells with Alpha == false are dropped before
// palette selection and dither.
type CellSample struct {
	SlabIdx  int
	CellIdx  int
	Centroid [3]float32
	Color    [3]uint8
	Alpha    bool
	Area     float32
}

// SampleCells colors every cell in slabs by averaging
// voxel.SampleNearestColor at a small jitter pattern inside each
// cell's prism (mid-Z, ±cellSize/4 in XY). model + si are the color
// source — typically the texture-bearing original mesh. decals and
// override are passed through to SampleNearestColor for sticker /
// MaterialX support.
//
// cellSize controls the offset radius; a 0 or negative value is
// treated as the cell's bbox short side ÷ 4 (per-cell adaptive).
// searchRadius is the radius passed to the spatial-index lookup; 0 or
// negative defaults to max(cellSize, 1.0).
func SampleCells(
	slabs []Slab,
	model *loader.LoadedModel,
	si *voxel.SpatialIndex,
	cellSize float32,
	searchRadius float32,
	decals []*voxel.StickerDecal,
	override voxel.BaseColorOverride,
) []CellSample {
	searchRadius = resolveSearchRadius(searchRadius, cellSize)
	buf := voxel.NewSearchBuf(len(model.Faces))
	out := []CellSample{}
	for si_ := range slabs {
		out = append(out, SampleSlab(&slabs[si_], si_, model, si, cellSize, searchRadius, decals, override, buf)...)
	}
	return out
}

// SampleSlab colors every cell in slab with the same algorithm
// SampleCells uses, but for a single slab and a caller-supplied
// SearchBuf. Exposed so the Voxelize pipeline can process each slab
// on its own goroutine with its own per-worker buffer — the slab
// reads the spatial index (immutable post-construction) and writes
// its own returned slice with no shared state.
//
// slabIdx is stamped into each emitted CellSample.SlabIdx so the
// caller can stitch per-slab results back together while preserving
// the global cell index used by the adjacency graph.
//
// searchRadius == 0 picks the same default as SampleCells. buf must
// be sized for model.Faces (NewSearchBuf(len(model.Faces))).
func SampleSlab(
	s *Slab,
	slabIdx int,
	model *loader.LoadedModel,
	si *voxel.SpatialIndex,
	cellSize float32,
	searchRadius float32,
	decals []*voxel.StickerDecal,
	override voxel.BaseColorOverride,
	buf *voxel.SearchBuf,
) []CellSample {
	searchRadius = resolveSearchRadius(searchRadius, cellSize)
	out := make([]CellSample, 0, len(s.Cells))
	midZ := 0.5 * (s.ZBot + s.ZTop)
	_ = cellSize // sample point count is polygon-driven; cellSize is kept for future per-cell tuning
	for ci := range s.Cells {
		c := &s.Cells[ci]
		cx, cy, area := polyCentroid(c.Outer)
		// Cell-interior sample points (Step 4 of the cleanup plan):
		// land every sample STRICTLY inside c.Outer so adjacent
		// geometry's colour can't bleed in. For convex cells the
		// centroid + 4 bbox-inset points usually all land inside;
		// for thin or L-shaped cells (diagonal partition corners),
		// the rejection-sampling fallback fills the budget with a
		// deterministic in-polygon walk.
		points := cellInteriorSamplePoints(c.Outer, cx, cy, midZ)
		var rSum, gSum, bSum, wSum float32
		anyAlpha := false
		for _, p := range points {
			rgba := voxel.SampleNearestColor(p, model, si, searchRadius, buf, decals, override)
			if rgba[3] < 128 {
				continue
			}
			anyAlpha = true
			w := float32(rgba[3]) / 255
			rSum += float32(rgba[0]) * w
			gSum += float32(rgba[1]) * w
			bSum += float32(rgba[2]) * w
			wSum += w
		}
		var color [3]uint8
		if wSum > 0 {
			color = [3]uint8{
				uint8(clampF(rSum/wSum, 0, 255) + 0.5),
				uint8(clampF(gSum/wSum, 0, 255) + 0.5),
				uint8(clampF(bSum/wSum, 0, 255) + 0.5),
			}
		}
		out = append(out, CellSample{
			SlabIdx:  slabIdx,
			CellIdx:  ci,
			Centroid: [3]float32{cx, cy, midZ},
			Color:    color,
			Alpha:    anyAlpha,
			Area:     area,
		})
	}
	return out
}

// cellInteriorSamplePoints returns a small set of (x, y, midZ) sample
// points that all lie strictly inside the polygon outer (CCW). Every
// returned point passes pointInPolygon(outer, x, y) — adjacent cells
// can't pull their colour into this cell's sample average.
//
// Strategy:
//
//   - Start with the signed-area centroid and four bbox-inset points
//     (corners pulled 30% toward the centroid). For convex / mostly-
//     convex cells, these are all inside and we're done.
//   - For non-convex cells (L-shapes from diagonal partition corners,
//     thin slivers), points that landed outside the polygon are
//     replaced by rejection-sampled grid points; we sweep a small
//     deterministic grid over the bbox, keeping points that pass
//     pointInPolygon, until we have at least minSamples or run out of
//     candidates.
//
// Degenerate fallback: if no candidate landed inside (extremely thin
// polygon where every sample misses), return just the centroid so the
// caller still gets a colour sample.
func cellInteriorSamplePoints(outer []Point2, cx, cy, midZ float32) [][3]float32 {
	const (
		minSamples = 5
		maxSamples = 9
	)
	if len(outer) < 3 {
		return [][3]float32{{cx, cy, midZ}}
	}
	minX, minY, maxX, maxY := polyBounds(outer)
	pts := make([][3]float32, 0, maxSamples)
	if pointInPolygon(outer, cx, cy) {
		pts = append(pts, [3]float32{cx, cy, midZ})
	}
	// Four bbox-inset candidates: bbox corners pulled 30% toward the
	// centroid. For convex cells they typically land well inside.
	const inset = 0.30
	bbCands := [...][2]float32{
		{minX + inset*(maxX-minX), minY + inset*(maxY-minY)},
		{maxX - inset*(maxX-minX), minY + inset*(maxY-minY)},
		{maxX - inset*(maxX-minX), maxY - inset*(maxY-minY)},
		{minX + inset*(maxX-minX), maxY - inset*(maxY-minY)},
	}
	for _, c := range bbCands {
		if pointInPolygon(outer, c[0], c[1]) {
			pts = append(pts, [3]float32{c[0], c[1], midZ})
		}
	}
	// Deterministic grid rejection sample to top up to minSamples on
	// non-convex cells. 5×5 grid over the bbox, snake-walked so a
	// thin cell that crosses the middle of the bbox still finds
	// in-polygon points quickly.
	if len(pts) < minSamples {
		const grid = 5
		for gj := 0; gj < grid && len(pts) < maxSamples; gj++ {
			for gi := 0; gi < grid && len(pts) < maxSamples; gi++ {
				tx := (float32(gi) + 0.5) / float32(grid)
				ty := (float32(gj) + 0.5) / float32(grid)
				x := minX + tx*(maxX-minX)
				y := minY + ty*(maxY-minY)
				if pointInPolygon(outer, x, y) {
					pts = append(pts, [3]float32{x, y, midZ})
				}
			}
		}
	}
	if len(pts) == 0 {
		// Polygon is so thin that the bbox grid found no interior
		// pixel-centre. Use the centroid as a last-resort sample even
		// if pointInPolygon rejects it (centroid sits on a vertex or
		// inside a tiny concavity).
		pts = append(pts, [3]float32{cx, cy, midZ})
	}
	return pts
}

func resolveSearchRadius(searchRadius, cellSize float32) float32 {
	if searchRadius > 0 {
		return searchRadius
	}
	if cellSize >= 1 {
		return cellSize
	}
	return 1
}

// polyCentroid returns the signed-area-weighted centroid of pts and
// the polygon's unsigned area. Falls back to bbox center for
// degenerate (zero-area) polygons.
func polyCentroid(pts []Point2) (cx, cy, area float32) {
	n := len(pts)
	if n < 3 {
		if n == 0 {
			return 0, 0, 0
		}
		var sx, sy float32
		for _, p := range pts {
			sx += p[0]
			sy += p[1]
		}
		return sx / float32(n), sy / float32(n), 0
	}
	var signed, cxSum, cySum float64
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		cross := float64(pts[i][0])*float64(pts[j][1]) - float64(pts[j][0])*float64(pts[i][1])
		signed += cross
		cxSum += (float64(pts[i][0]) + float64(pts[j][0])) * cross
		cySum += (float64(pts[i][1]) + float64(pts[j][1])) * cross
	}
	a := signed * 0.5
	if math.Abs(a) < 1e-9 {
		// Degenerate; fall back to bbox center.
		minX, minY, maxX, maxY := polyBounds(pts)
		return (minX + maxX) * 0.5, (minY + maxY) * 0.5, 0
	}
	cx = float32(cxSum / (6 * a))
	cy = float32(cySum / (6 * a))
	area = float32(math.Abs(a))
	return
}

func clampF(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
