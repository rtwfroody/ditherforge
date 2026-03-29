package voxel

import (
	"math"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// VoxelizeColumn performs Z-ray voxelization at a single column center (cx, cy),
// returning a set of active layer indices. Only surface layers are activated;
// the slicer handles infill for the interior.
func VoxelizeColumn(cx, cy float32, model *loader.LoadedModel, si *SpatialIndex, layerH, minZ float32, nLayers int) map[int]struct{} {
	cands := si.Candidates(cx, cy)
	var hitZs []float32
	for _, ti := range cands {
		f := model.Faces[ti]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]

		inside, bary := PointInTriangleXY(cx, cy, v0, v1, v2)
		if !inside {
			continue
		}

		z := bary[0]*v0[2] + bary[1]*v1[2] + bary[2]*v2[2]
		hitZs = append(hitZs, z)
	}

	if len(hitZs) == 0 {
		return nil
	}

	activeLayers := make(map[int]struct{})
	for _, z := range hitZs {
		layer := int(math.Round(float64(z-minZ) / float64(layerH)))
		if layer >= 0 && layer < nLayers {
			activeLayers[layer] = struct{}{}
		}
	}

	return activeLayers
}

// DeduplicateCells removes duplicate cells by CellKey.
func DeduplicateCells(cells []ActiveCell) []ActiveCell {
	seen := make(map[CellKey]int, len(cells))
	var result []ActiveCell
	for _, c := range cells {
		k := CellKey{c.Col, c.Row, c.Layer}
		if _, ok := seen[k]; !ok {
			seen[k] = len(result)
			result = append(result, c)
		}
	}
	return result
}
