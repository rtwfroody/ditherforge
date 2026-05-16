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
	for ci := range s.Cells {
		c := &s.Cells[ci]
		cx, cy, area := polyCentroid(c.Outer)
		// Span the full cell footprint with the jitter grid — the
		// ±off range below is the cell's half-width, not a quarter,
		// so a 5×5 grid lands evenly across the polygon. The old
		// quarter-width offset clustered all samples in the middle
		// 25% of the cell and could miss the rest of the surface
		// that contributes to the cell's volume.
		off := cellSize / 2
		if off <= 0 {
			// Per-cell fallback: bbox-derived offset.
			minX, minY, maxX, maxY := polyBounds(c.Outer)
			w := maxX - minX
			h := maxY - minY
			if w < h {
				off = w / 2
			} else {
				off = h / 2
			}
		}
		// 5×5 jitter grid spanning ±off in X and Y. The earlier 5-
		// point "+ pattern" was undersampling the texture: for the
		// production earth.glb at cellSize=0.4 mm, each cell
		// projects to ~2–3 texture pixels, and a single point
		// sample can land on an outlier texel.
		const grid = 5
		points := make([][3]float32, 0, grid*grid)
		for gj := 0; gj < grid; gj++ {
			for gi := 0; gi < grid; gi++ {
				gx := -off + 2*off*float32(gi)/float32(grid-1)
				gy := -off + 2*off*float32(gj)/float32(grid-1)
				points = append(points, [3]float32{cx + gx, cy + gy, midZ})
			}
		}
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
