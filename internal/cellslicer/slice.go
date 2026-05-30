package cellslicer

import (
	"math"

	clipper "github.com/ctessum/go.clipper"
	"github.com/rtwfroody/ditherforge/internal/loader"
)

// SlabBoundaryPlanes returns nSlabs+1 Z planes at uniform layerH
// spacing covering [zMin, zMax]. A tiny per-plane offset shifts each
// plane off the integer slab grid so on-plane vertices don't fall
// exactly on a slicing plane (matches the prototype's nudge).
//
// Plane 0 is pulled BELOW zMin by a small epsilon so the model's
// bottommost triangles (which sit exactly at z=zMin after loader
// normalization) are unambiguously inside slab 0. Without this,
// a flat-bottomed model (e.g. cube) loses its entire bottom face:
// every other plane has a positive nudge, so slab 0's ZBot would
// be > zMin and the bottom triangles' zMax (= zMin) falls outside
// every slab's [ZBot, ZTop] range.
func SlabBoundaryPlanes(zMin, zMax, layerH float32) []float32 {
	nSlabs := int(math.Ceil(float64((zMax - zMin) / layerH)))
	if nSlabs < 1 {
		nSlabs = 1
	}
	planes := make([]float32, nSlabs+1)
	for i := 0; i <= nSlabs; i++ {
		planes[i] = zMin + float32(i)*layerH + float32((i+1)*53)*1e-6
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
	out := make([]*Footprint, nSlabs)
	for i := range perSlab {
		if len(perSlab[i]) == 0 {
			continue
		}
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
	return out
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
