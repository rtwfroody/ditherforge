package cellslicer

import (
	"math"
	"os"
	"strconv"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// rayBackMargin and rayReach tune the along-normal color ray (added to
// the slab thickness for the outward start-back, and the inward search
// distance past the sample point). Defaults clear the wrap offset while
// staying local; env DF_RAY_BACK / DF_RAY_REACH override for tuning.
var (
	rayBackMargin = envFloat("DF_RAY_BACK", 0.6)
	rayReach      = envFloat("DF_RAY_REACH", 0.6)
)

func envFloat(key string, def float32) float32 {
	if s := os.Getenv(key); s != "" {
		if v, err := strconv.ParseFloat(s, 32); err == nil && v >= 0 {
			return float32(v)
		}
	}
	return def
}

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
	// HalfIdx tags which split half produced this cell (0 or 1).
	// Copied from the source Slab; 0 in the unsplit pipeline.
	HalfIdx byte
	// Normal is the printed-surface outward normal at this cell, in the
	// color model's coordinate frame (same frame cellOrient returns;
	// see SampleSlab). Zero when along-normal sampling was disabled for
	// the cell (no geom face in range). Used downstream to identify
	// split cut-face cells by comparing against the cut-plane normal.
	Normal [3]float32
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
		out = append(out, SampleSlab(&slabs[si_], si_, model, si, cellSize, searchRadius, decals, nil, nil, override, nil, buf, nil, nil, nil, nil, nil)...)
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
// colorXform, when non-nil, maps each sample point from the slab's
// (bed-space) coordinate frame back into the color model's coordinate
// frame before SampleNearestColor. The Split pipeline passes the
// per-half inverse layout transform so geometry can be sliced in bed
// coords while color is still read from the untouched original-coords
// ColorModel. nil = identity (the unsplit pipeline). See
// docs/split-cellslicer.md.
//
// stickerModel/stickerSI are the sticker substrate and its spatial
// index: base color is always read from model, while the decal is a
// second nearest-tri lookup against stickerModel (the decal TriUVs
// index into it, not into model). Pass nil for both when no stickers
// were placed. The same colorXform'd sample point feeds both lookups —
// stickerModel lives in the same original-mesh frame as model in every
// configuration (projection/unfold clone or alpha-wrap mesh). stickerBuf
// is the per-worker scratch buffer for the decal lookup; it must be
// sized for stickerModel.Faces (which can exceed model.Faces). Pass nil
// and SampleSlab allocates one itself — callers that sample many slabs
// should supply a reused buffer to avoid per-call allocation.
func SampleSlab(
	s *Slab,
	slabIdx int,
	model *loader.LoadedModel,
	si *voxel.SpatialIndex,
	cellSize float32,
	searchRadius float32,
	decals []*voxel.StickerDecal,
	stickerModel *loader.LoadedModel,
	stickerSI *voxel.SpatialIndex,
	override voxel.BaseColorOverride,
	colorXform func([3]float32) [3]float32,
	buf *voxel.SearchBuf,
	stickerBuf *voxel.SearchBuf,
	geom *loader.LoadedModel,
	geomSI *voxel.SpatialIndex,
	geomBuf *voxel.SearchBuf,
	colorBVH *voxel.RayBVH,
) []CellSample {
	searchRadius = resolveSearchRadius(searchRadius, cellSize)
	// The decal lookup indexes stickerModel's faces, which can outnumber
	// model's (subdivided clone / wrap), so its SearchBuf must be sized to
	// stickerModel. Allocate one when the caller didn't, rather than let
	// SampleNearestColorWithSticker reuse the undersized color buf and index
	// out of range.
	if stickerModel != nil && stickerBuf == nil {
		stickerBuf = voxel.NewSearchBuf(len(stickerModel.Faces))
	}
	// geomSI + colorBVH drive along-normal color sampling: each cell reads
	// its outward normal from the geom (printed-surface) mesh, then color
	// is taken from the first original-mesh face a ray finds going inward
	// along -normal (see voxel.SampleAlongNormal). Because alpha-wrap only
	// expands outward, that inward hit is the true surface under the skin,
	// so a thin perpendicular accent (a red module side-wall) can't bleed
	// onto the cap it abuts. geomBuf is sized to geom; allocate when the
	// caller didn't. When geom/geomSI or colorBVH is nil the sampler falls
	// back to legacy nearest-face for every cell.
	if geom != nil && geomSI != nil && geomBuf == nil {
		geomBuf = voxel.NewSearchBuf(len(geom.Faces))
	}
	// Along-normal ray geometry. The sample point sits at the slab's
	// mid-Z (inside the solid), so the ray starts startBack outward to
	// clear the skin — past half the slab thickness plus the wrap offset —
	// then searches reach past the sample point for the surface beneath.
	// reach is kept short so a cell over a deep bridged void falls back to
	// nearest-face rather than painting a far interior surface.
	slabThick := s.ZTop - s.ZBot
	startBack := slabThick + rayBackMargin
	reach := rayReach
	out := make([]CellSample, 0, len(s.Cells))
	midZ := 0.5 * (s.ZBot + s.ZTop)
	for ci := range s.Cells {
		c := &s.Cells[ci]
		cx, cy, area := polyCentroid(c.Outer)
		// Outward normal of the printed surface at this cell, read from
		// the geom mesh and mapped into the color model's frame. Computed
		// once per cell and reused for all its sample points. Zero (no
		// geom face in range) disables along-normal sampling for this cell
		// — SampleAlongNormal falls back to nearest-face.
		cellN := cellOrient(cx, cy, midZ, colorXform, geom, geomSI, searchRadius, geomBuf)
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
			if colorXform != nil {
				p = colorXform(p)
			}
			rgba := voxel.SampleAlongNormal(p, cellN, startBack, reach, model, colorBVH, si, searchRadius, buf, decals, stickerModel, stickerSI, stickerBuf, override)
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
			HalfIdx:  s.HalfIdx,
			Normal:   cellN,
		})
	}
	return out
}

// SampleSurfaceColor returns the printed-surface colour at a single
// slab-plane point (x, y, z), using the same along-normal logic as
// SampleSlab applies per sample point. It exists so the colour-aware
// partition (PartitionSlabAnalyticColor) can build a slab colour field
// BEFORE cells exist; the geometry partition stays colour-free by
// receiving this as an injected func(x,y)->colour closure.
//
// ok is false when no surface was found at that point (along-normal ray
// missed and the nearest-face fallback returned alpha < 128) — the
// caller treats that as a distinct "miss" label that merges away.
// slabThick sets the ray's outward start-back exactly as SampleSlab does.
func SampleSurfaceColor(
	x, y, z, slabThick, cellSize float32,
	model *loader.LoadedModel,
	si *voxel.SpatialIndex,
	searchRadius float32,
	decals []*voxel.StickerDecal,
	stickerModel *loader.LoadedModel,
	stickerSI *voxel.SpatialIndex,
	stickerBuf *voxel.SearchBuf,
	override voxel.BaseColorOverride,
	colorXform func([3]float32) [3]float32,
	buf *voxel.SearchBuf,
	geom *loader.LoadedModel,
	geomSI *voxel.SpatialIndex,
	geomBuf *voxel.SearchBuf,
	colorBVH *voxel.RayBVH,
) ([3]uint8, bool) {
	searchRadius = resolveSearchRadius(searchRadius, cellSize)
	startBack := slabThick + rayBackMargin
	reach := rayReach
	cellN := cellOrient(x, y, z, colorXform, geom, geomSI, searchRadius, geomBuf)
	p := [3]float32{x, y, z}
	if colorXform != nil {
		p = colorXform(p)
	}
	rgba := voxel.SampleAlongNormal(p, cellN, startBack, reach, model, colorBVH, si, searchRadius, buf, decals, stickerModel, stickerSI, stickerBuf, override)
	if rgba[3] < 128 {
		return [3]uint8{}, false
	}
	return [3]uint8{rgba[0], rgba[1], rgba[2]}, true
}

// cellInteriorSamplePoints returns up to maxSamples (x, y, midZ)
// sample points that all lie strictly inside the polygon outer (CCW).
// Every returned point passes pointInPolygon(outer, x, y), so
// adjacent cells can't pull their colour into this cell's sample
// average.
//
// Candidate order (deterministic; first hit wins on bucket equality):
//
//  1. Signed-area centroid.
//  2. Four bbox-inset points (corners pulled 30% toward the centroid)
//     — convex cells typically take all four inside.
//  3. 5×5 grid over the bbox — covers non-convex / L-shaped cells
//     whose inset corners landed outside the polygon.
//
// Candidates are deduped by their 1µm int2D bucket so an inset point
// that happens to coincide with a grid point isn't sampled twice.
// Degenerate fallback: if no candidate landed inside (extremely thin
// polygon where every test misses), return just the centroid so the
// caller still gets a colour sample.
func cellInteriorSamplePoints(outer []Point2, cx, cy, midZ float32) [][3]float32 {
	const maxSamples = 9
	if len(outer) < 3 {
		return [][3]float32{{cx, cy, midZ}}
	}
	minX, minY, maxX, maxY := polyBounds(outer)

	pts := make([][3]float32, 0, maxSamples)
	seen := make(map[int2D]struct{}, maxSamples)
	tryAdd := func(x, y float32) bool {
		if len(pts) >= maxSamples {
			return false
		}
		if !pointInPolygon(outer, x, y) {
			return false
		}
		key := int2DOf(Point2{x, y})
		if _, dup := seen[key]; dup {
			return false
		}
		seen[key] = struct{}{}
		pts = append(pts, [3]float32{x, y, midZ})
		return true
	}

	tryAdd(cx, cy)
	const inset = 0.30
	for _, c := range [...][2]float32{
		{minX + inset*(maxX-minX), minY + inset*(maxY-minY)},
		{maxX - inset*(maxX-minX), minY + inset*(maxY-minY)},
		{maxX - inset*(maxX-minX), maxY - inset*(maxY-minY)},
		{minX + inset*(maxX-minX), maxY - inset*(maxY-minY)},
	} {
		tryAdd(c[0], c[1])
	}
	// 5×5 grid over the bbox. Bucket-deduped against earlier
	// candidates, so for a convex cell where every inset point hit
	// this just adds 4–5 new fill-in samples; for a thin cell where
	// the inset points missed, the grid carries the budget.
	const grid = 5
	for gj := 0; gj < grid; gj++ {
		for gi := 0; gi < grid; gi++ {
			tx := (float32(gi) + 0.5) / float32(grid)
			ty := (float32(gj) + 0.5) / float32(grid)
			tryAdd(minX+tx*(maxX-minX), minY+ty*(maxY-minY))
		}
	}
	if len(pts) == 0 {
		// Polygon is so thin that every candidate missed. Use the
		// centroid even if pointInPolygon rejects it (centroid sits
		// on a vertex or inside a tiny concavity) so the caller
		// always gets at least one sample.
		pts = append(pts, [3]float32{cx, cy, midZ})
	}
	return pts
}

// cellOrient reads the printed-surface outward normal at (cx,cy,midZ)
// from the geom mesh, in the color model's coordinate frame. Returns
// the zero vector (along-normal sampling off; falls back to nearest)
// when geomSI is nil or no geom face is in range. When colorXform is
// non-nil (the Split pipeline samples color in the original-coords frame
// while geometry is in bed coords), the normal is rotated by the same
// transform via a finite difference so it matches the frame the color
// faces — and the ray cast against them — live in.
func cellOrient(cx, cy, midZ float32, colorXform func([3]float32) [3]float32, geom *loader.LoadedModel, geomSI *voxel.SpatialIndex, searchRadius float32, geomBuf *voxel.SearchBuf) [3]float32 {
	if geomSI == nil || geom == nil {
		return [3]float32{}
	}
	n, ok := voxel.NearestFaceNormal([3]float32{cx, cy, midZ}, geom, geomSI, searchRadius, geomBuf)
	if !ok {
		return [3]float32{}
	}
	if colorXform != nil {
		const e = 0.1
		base := colorXform([3]float32{cx, cy, midZ})
		tip := colorXform([3]float32{cx + n[0]*e, cy + n[1]*e, midZ + n[2]*e})
		d := [3]float32{tip[0] - base[0], tip[1] - base[1], tip[2] - base[2]}
		l := float32(math.Sqrt(float64(d[0]*d[0] + d[1]*d[1] + d[2]*d[2])))
		if l < 1e-9 {
			return [3]float32{}
		}
		n = [3]float32{d[0] / l, d[1] / l, d[2] / l}
	}
	return n
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
