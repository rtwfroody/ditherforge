package cellslicer

import (
	"math"

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
