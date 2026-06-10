package cellslicer

import (
	"math"
	"runtime"
	"sync"

	clipper "github.com/ctessum/go.clipper"
	"github.com/rtwfroody/ditherforge/internal/loader"
)

// SlabBoundaryPlanes returns boundary planes for uniform layerH slabs
// covering [zMin, zMax]. Equivalent to SlabBoundaryPlanesFirst with the
// first slab the same height as the rest; kept for the prototype/tests
// that don't model a taller initial layer.
func SlabBoundaryPlanes(zMin, zMax, layerH float32) []float32 {
	return SlabBoundaryPlanesFirst(zMin, zMax, layerH, layerH)
}

// SlabBoundaryPlanesFirst returns Z planes partitioning [zMin, zMax] into
// slabs whose heights match the printer's real layer schedule: the first
// slab spans firstLayerH (the profile's initial-layer print height), and
// every slab above it spans layerH. This makes each mesh slab line up
// 1:1 with a print layer, so the slicer cuts through the MIDDLE of a slab
// (vertical cell walls) instead of landing on a horizontal slab seam.
//
// Why this matters: the slicer samples each layer's contour at its
// mid-height (print_z - height/2). With a uniform layerH grid starting at
// zMin but a taller first layer (e.g. Snapmaker U1: 0.2mm initial, 0.08mm
// upper), every upper layer's mid-height coincides exactly with a uniform
// 0.08mm slab boundary — i.e. the slicer slices ON the coincident
// top/bottom cap faces between two slabs. That degenerate slice drops
// whole layers (observed: empty layers 3/5/8 on a 0.08mm Benchy). Sizing
// the first slab to firstLayerH shifts every upper seam onto a print-layer
// boundary, moving the slice planes to safe slab interiors.
//
// A tiny per-plane offset shifts each plane off the integer slab grid so
// on-plane vertices don't fall exactly on ditherforge's own slicing plane.
// Plane 0 is pulled BELOW zMin by a small epsilon so the model's
// bottommost triangles (which sit exactly at z=zMin after loader
// normalization) are unambiguously inside slab 0. Without this, a
// flat-bottomed model (e.g. cube) loses its entire bottom face: every
// other plane has a positive nudge, so slab 0's ZBot would be > zMin and
// the bottom triangles' zMax (= zMin) falls outside every slab's range.
func SlabBoundaryPlanesFirst(zMin, zMax, firstLayerH, layerH float32) []float32 {
	if layerH <= 0 {
		layerH = 0.2
	}
	if firstLayerH <= 0 {
		firstLayerH = layerH
	}
	total := zMax - zMin
	// One firstLayerH slab at the bottom, then enough layerH slabs to
	// cover the rest. Ceil so the final plane reaches or passes zMax and
	// the top slab fully contains it.
	nUpper := 0
	if total > firstLayerH {
		nUpper = int(math.Ceil(float64((total - firstLayerH) / layerH)))
	}
	nSlabs := 1 + nUpper
	planes := make([]float32, nSlabs+1)
	for i := 0; i <= nSlabs; i++ {
		// Height of boundary i above zMin: 0, firstLayerH,
		// firstLayerH+layerH, … computed with a single multiply (no
		// repeated-addition drift across hundreds of slabs).
		var hgt float32
		if i >= 1 {
			hgt = firstLayerH + float32(i-1)*layerH
		}
		planes[i] = zMin + hgt + float32((i+1)*53)*1e-6
	}
	planes[0] = zMin - 53e-6
	return planes
}

// horizNormalZAbs is the |unit-normal.z| above which a triangle counts
// as "near-horizontal" for interior-face footprint projection.
// 0.9 ≈ cos(26°): flatter sheets are the ones that can lie wholly
// between two slab planes and vanish from the bounding-plane slices;
// steeper faces vary enough in Z that some plane cuts them, so the
// contour footprint already captures them. Keeping the filter tight
// also keeps the projected-polygon count (and Clipper load) small,
// which matters on finely tessellated curved meshes where many small
// near-vertical faces would otherwise project to zero-area slivers.
const horizNormalZAbs = 0.9

// InteriorHorizontalFootprints returns, per slab, the XY projection of
// the model's near-horizontal triangles whose Z-range lies entirely
// within that one slab — i.e. the thin horizontal sheets that the
// bounding-plane slices (ComputeFootprint at planes[i]/planes[i+1])
// never intersect and therefore drop. planes holds nSlabs+1 ascending
// boundaries; the result has nSlabs entries, nil where a slab has no
// such faces. Union each into the corresponding slab footprint so cap
// detection has the sheet's surface to work with. Without this, a flat
// sheet thinner than the layer height (e.g. an alpha-wrapped single-
// surface roof, ~0.03 mm) that sits between two planes is represented
// in no slab and never gets a cap. A triangle that crosses a plane is
// skipped here because the contour footprint already owns it.
func InteriorHorizontalFootprints(model *loader.LoadedModel, planes []float32) []*Footprint {
	nSlabs := len(planes) - 1
	if nSlabs < 1 {
		return nil
	}
	perSlab := make([]clipper.Paths, nSlabs)
	for _, f := range model.Faces {
		a := model.Vertices[f[0]]
		b := model.Vertices[f[1]]
		c := model.Vertices[f[2]]
		zMin := minf32(a[2], minf32(b[2], c[2]))
		zMax := maxf32(a[2], maxf32(b[2], c[2]))
		ks := slabIndexForZ(planes, zMin)
		ke := slabIndexForZ(planes, zMax)
		if ks < 0 || ks != ke {
			continue // out of range, or crosses a plane (contour owns it)
		}
		if !nearHorizontal(a, b, c) {
			continue
		}
		perSlab[ks] = append(perSlab[ks], triPathCCW(a, b, c))
	}
	return unionPerSlabFootprints(perSlab)
}

// unionPerSlabFootprints unions each slab's accumulated CCW paths into a
// single Footprint, returning one entry per slab (nil where the slab has
// no paths or the union is empty). The slabs are independent, so the
// unions run on a worker pool — on a finely tessellated mesh a single
// slab can accumulate thousands of heavily overlapping triangle
// projections whose Clipper union is superlinear in overlap density
// (seconds per dense slab), and there are hundreds of slabs; serial this
// is a multi-minute stall (the golden_pheasant "stuck in voxelizing"
// hang). The result is identical to a serial loop — out[i] depends only
// on perSlab[i].
func unionPerSlabFootprints(perSlab []clipper.Paths) []*Footprint {
	out := make([]*Footprint, len(perSlab))
	nWorkers := runtime.NumCPU()
	if nWorkers > len(perSlab) {
		nWorkers = len(perSlab)
	}
	if nWorkers < 1 {
		nWorkers = 1
	}
	jobCh := make(chan int, len(perSlab))
	for i := range perSlab {
		if len(perSlab[i]) > 0 {
			jobCh <- i
		}
	}
	close(jobCh)
	var wg sync.WaitGroup
	for w := 0; w < nWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobCh {
				c := clipper.NewClipper(clipper.IoNone)
				c.AddPaths(perSlab[i], clipper.PtSubject, true)
				tree, ok := c.Execute2(clipper.CtUnion, clipper.PftNonZero, clipper.PftNonZero)
				if !ok || tree == nil {
					continue
				}
				fp := &Footprint{}
				for _, child := range tree.Childs() {
					collectFootprintLoops(child, fp)
				}
				if len(fp.Loops) > 0 {
					out[i] = fp
				}
			}
		}()
	}
	wg.Wait()
	return out
}

// SurfaceDropStats reports how many in-band triangle projections
// triBandXYPath discarded as degenerate near-vertical slivers, so the drop
// is observable rather than silent. Considered counts every projection
// evaluated; Dropped is the subset rejected by the thinness test;
// AreaSum/AreaMax are the summed and largest single discarded |XY area|
// (mm²). For a true vertical wall the discarded slices are essentially
// collinear, so AreaMax stays ~0; a pixel-scale AreaMax is the signal that
// the filter may have eaten real coverage (the caller flags it). See
// triBandXYPath.
type SurfaceDropStats struct {
	Considered int
	Dropped    int
	AreaSum    float32
	AreaMax    float32
}

// SlabSurfaceFootprints returns, per slab, the XY projection of the
// model surface clipped to that slab's Z-band [planes[i], planes[i+1]].
// Unlike the two bounding-plane slice contours (ComputeFootprint) — which
// only sample the surface at planes[i] and planes[i+1] — this captures
// the surface's true XY extent *between* the planes. Where a wall bulges
// radially outward mid-slab (a convex Z-edge, e.g. a base-rim slope
// change), or a coarse triangle spans the slab, the bulge projects
// outside the two-plane footprint and would otherwise be dropped by the
// per-cell clip, leaving a hole. Unioning this into the slab footprint
// makes the cells (and their clip prisms) cover the actual surface,
// independent of triangle size.
//
// Wholly-in-band near-horizontal triangles are skipped here: they are
// already recovered by InteriorHorizontalFootprints, and the caller
// unions both into the coverage footprint. planes holds nSlabs+1
// ascending boundaries; the result has nSlabs entries, nil where a slab
// has no surface. The second return accounts for the degenerate slivers
// the projection discarded (see SurfaceDropStats).
func SlabSurfaceFootprints(model *loader.LoadedModel, planes []float32) ([]*Footprint, SurfaceDropStats) {
	var drop SurfaceDropStats
	nSlabs := len(planes) - 1
	if nSlabs < 1 {
		return nil, drop
	}
	zLo, zHi := planes[0], planes[nSlabs]
	perSlab := make([]clipper.Paths, nSlabs)
	for _, f := range model.Faces {
		a := model.Vertices[f[0]]
		b := model.Vertices[f[1]]
		c := model.Vertices[f[2]]
		zMin := minf32(a[2], minf32(b[2], c[2]))
		zMax := maxf32(a[2], maxf32(b[2], c[2]))
		if zMax <= zLo || zMin >= zHi {
			continue // entirely outside the sliced range
		}
		// Wholly within one slab and near-horizontal: InteriorHorizontal-
		// Footprints already owns it (avoids duplicate projection work).
		ks := slabIndexForZ(planes, zMin)
		ke := slabIndexForZ(planes, zMax)
		if ks >= 0 && ks == ke && nearHorizontal(a, b, c) {
			continue
		}
		// Iterate only the slabs the triangle spans, not all nSlabs. The
		// zMax<=zLo / zMin>=zHi reject above guarantees overlap, so a -1
		// (vertex past a sliced-range end) clamps to the first/last slab.
		if ks < 0 {
			ks = 0
		}
		if ke < 0 {
			ke = nSlabs - 1
		}
		for si := ks; si <= ke; si++ {
			zb, zt := planes[si], planes[si+1]
			p, ok, slivArea := triBandXYPath(a, b, c, zb, zt)
			drop.Considered++
			if ok {
				perSlab[si] = append(perSlab[si], p)
			} else if slivArea >= 0 {
				// slivArea >= 0 marks a thinness reject (its discarded area,
				// which is exactly 0 for an axis-aligned wall); the empty
				// reject returns -1 and is not a sliver. Gating on >= 0, not
				// > 0, keeps exactly-collinear drops in the count so the log
				// is not silent on box-like geometry.
				drop.Dropped++
				drop.AreaSum += slivArea
				if slivArea > drop.AreaMax {
					drop.AreaMax = slivArea
				}
			}
		}
	}
	return unionPerSlabFootprints(perSlab), drop
}

// triBandXYPath clips triangle a,b,c to the Z-slab [zBot,zTop] and
// returns its XY projection as a CCW Clipper path. ok is false when the
// in-band portion is empty (fewer than 3 projected vertices) or its XY
// projection is degenerately thin. CCW winding makes every projected
// polygon add +1 under the PftNonZero union, matching triPathCCW.
//
// The thinness reject is essential for near-vertical surfaces (e.g. a
// cylinder wall). A vertical wall's between-plane XY silhouette is
// identical to its silhouette AT the planes, which capFp already holds,
// so the band slice of such a triangle projects to a numerically
// near-collinear sliver that contributes no real coverage. Left in, those
// slivers union into dozens of isolated micro-loops on the perimeter, each
// of which seeds a degenerate Voronoi cell and warps its neighbours — the
// "uneven ring cells" failure. We reject by aspect (area vs. longest
// edge²), which is scale-invariant: a genuine bulge patch projects with
// O(1) aspect and survives; only essentially-collinear slices are dropped.
//
// The third return distinguishes the two reject reasons so the caller can
// account for what the filter removed: a thinness reject returns the
// discarded |XY area| (>= 0 — exactly 0 for an axis-aligned wall, whose
// projection is perfectly collinear); the empty reject (fewer than 3
// projected vertices) returns -1; an accepted triangle returns 0 with
// ok=true. A vertical wall's discarded area is ~0; a non-trivial value is
// the breadcrumb that the filter touched something with real extent.
func triBandXYPath(a, b, c [3]float32, zBot, zTop float32) (clipper.Path, bool, float32) {
	poly := []([3]float32){a, b, c}
	poly = clipPolyZHalf(poly, zBot, true)  // keep z >= zBot
	poly = clipPolyZHalf(poly, zTop, false) // keep z <= zTop
	if len(poly) < 3 {
		return nil, false, -1
	}
	pts := make([]Point2, len(poly))
	for i, p := range poly {
		pts[i] = Point2{p[0], p[1]}
	}
	sa := signedArea(pts)
	area := absf32(sa)
	var maxEdge2 float32
	for i := range pts {
		j := (i + 1) % len(pts)
		dx := pts[j][0] - pts[i][0]
		dy := pts[j][1] - pts[i][1]
		if e := dx*dx + dy*dy; e > maxEdge2 {
			maxEdge2 = e
		}
	}
	// area/maxEdge2 is the dimensionless aspect (≈0.43 for equilateral,
	// →0 for a sliver). 1e-3 keeps every real surface patch and drops only
	// near-collinear vertical-wall projection noise.
	if maxEdge2 == 0 || area < 1e-3*maxEdge2 {
		return nil, false, area
	}
	if sa < 0 {
		for i, j := 0, len(pts)-1; i < j; i, j = i+1, j-1 {
			pts[i], pts[j] = pts[j], pts[i]
		}
	}
	return pointsToClipperPath(pts), true, 0
}

// clipPolyZHalf clips a 3D polygon against a horizontal half-space using
// Sutherland-Hodgman. keepAbove keeps vertices with z >= zCut; otherwise
// z <= zCut. The polygon is treated as closed (last vertex wraps to
// first). A convex input stays convex, so a triangle clipped by both
// slab planes yields at most a pentagon.
func clipPolyZHalf(poly [][3]float32, zCut float32, keepAbove bool) [][3]float32 {
	if len(poly) == 0 {
		return nil
	}
	inside := func(p [3]float32) bool {
		if keepAbove {
			return p[2] >= zCut
		}
		return p[2] <= zCut
	}
	n := len(poly)
	out := make([][3]float32, 0, n+1)
	for i := 0; i < n; i++ {
		cur := poly[i]
		prev := poly[(i+n-1)%n]
		ci, pi := inside(cur), inside(prev)
		if ci {
			if !pi {
				out = append(out, lerpZ(prev, cur, zCut))
			}
			out = append(out, cur)
		} else if pi {
			out = append(out, lerpZ(prev, cur, zCut))
		}
	}
	return out
}

// lerpZ returns the point on segment a→b at height z. Callers guarantee
// a[2] != b[2] (the segment crosses the cut plane).
func lerpZ(a, b [3]float32, z float32) [3]float32 {
	t := (z - a[2]) / (b[2] - a[2])
	return [3]float32{a[0] + t*(b[0]-a[0]), a[1] + t*(b[1]-a[1]), z}
}

// slabIndexForZ returns the index i of the slab [planes[i], planes[i+1])
// containing z, or -1 if z is outside [planes[0], planes[nSlabs]).
// planes must be ascending.
func slabIndexForZ(planes []float32, z float32) int {
	if z < planes[0] || z >= planes[len(planes)-1] {
		return -1
	}
	// largest i with planes[i] <= z
	lo, hi := 0, len(planes)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if planes[mid] <= z {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// nearHorizontal reports whether triangle a,b,c has |unit-normal.z|
// above horizNormalZAbs. Degenerate (zero-area) triangles return false.
// The normal is computed in float64 for the same robustness reason
// signedArea uses float64 — thin sliver triangles can underflow the
// cross product in float32.
func nearHorizontal(a, b, c [3]float32) bool {
	ux, uy, uz := float64(b[0]-a[0]), float64(b[1]-a[1]), float64(b[2]-a[2])
	vx, vy, vz := float64(c[0]-a[0]), float64(c[1]-a[1]), float64(c[2]-a[2])
	nx := uy*vz - uz*vy
	ny := uz*vx - ux*vz
	nz := ux*vy - uy*vx
	l := math.Sqrt(nx*nx + ny*ny + nz*nz)
	if l == 0 {
		return false
	}
	nzAbs := nz / l
	if nzAbs < 0 {
		nzAbs = -nzAbs
	}
	return nzAbs > horizNormalZAbs
}

// triPathCCW projects triangle a,b,c to XY and returns it as a Clipper
// path wound CCW (positive area), so every projected triangle adds the
// same +1 winding under PftNonZero union regardless of its 3-D facing.
func triPathCCW(a, b, c [3]float32) clipper.Path {
	area := (b[0]-a[0])*(c[1]-a[1]) - (c[0]-a[0])*(b[1]-a[1])
	pts := []Point2{{a[0], a[1]}, {b[0], b[1]}, {c[0], c[1]}}
	if area < 0 {
		pts[1], pts[2] = pts[2], pts[1]
	}
	return pointsToClipperPath(pts)
}

// PartitionModel slices model at uniform layerH Z spacing and
// partitions each slab into cells of target size cellSize. The
// returned slabs alias references into the slicer's per-Z layers, so
// the slice is valid as long as the caller doesn't mutate them.
//
// Slabs with no geometry at either Z (empty footprint) are still
// returned, but with Cells == nil and Footprint.Loops empty — caller
// can skip them.
func PartitionModel(model *loader.LoadedModel, layerH, cellSize float32) []Slab {
	zMin, zMax := modelZRange(model)
	if zMax <= zMin {
		return nil
	}
	planes := SlabBoundaryPlanes(zMin, zMax, layerH)
	layers := SliceMesh(model, planes)
	nSlabs := len(layers) - 1
	if nSlabs < 1 {
		return nil
	}
	slabs := make([]Slab, nSlabs)
	for i := 0; i < nSlabs; i++ {
		bot := &layers[i]
		top := &layers[i+1]
		cells, fp := PartitionSlab(bot.Loops, top.Loops, cellSize)
		slabs[i] = Slab{
			Index:     i,
			ZBot:      planes[i],
			ZTop:      planes[i+1],
			BotLayer:  bot,
			TopLayer:  top,
			Footprint: fp,
			Cells:     cells,
		}
	}
	return slabs
}

func modelZRange(m *loader.LoadedModel) (float32, float32) {
	if len(m.Vertices) == 0 {
		return 0, 0
	}
	zMin, zMax := m.Vertices[0][2], m.Vertices[0][2]
	for _, v := range m.Vertices[1:] {
		if v[2] < zMin {
			zMin = v[2]
		}
		if v[2] > zMax {
			zMax = v[2]
		}
	}
	return zMin, zMax
}
