package voxel

import (
	"math"
	"sort"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// SurfaceHit records where a cell column center intersects the original mesh.
type SurfaceHit struct {
	Z       float32
	MeshIdx int32
}

// VoxelizeColumn performs Z-ray voxelization at a single column center (cx, cy),
// returning a set of active layer indices.
func VoxelizeColumn(cx, cy float32, model *loader.LoadedModel, si *SpatialIndex, layerH, minZ float32, nLayers int) map[int]struct{} {
	cands := si.Candidates(cx, cy)
	var hits []SurfaceHit
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
		hits = append(hits, SurfaceHit{Z: z, MeshIdx: model.FaceMeshIdx[ti]})
	}

	if len(hits) == 0 {
		return nil
	}

	activeLayers := make(map[int]struct{})
	meshHits := make(map[int32][]float32)
	for _, h := range hits {
		meshHits[h.MeshIdx] = append(meshHits[h.MeshIdx], h.Z)
	}
	for _, mHits := range meshHits {
		sort.Slice(mHits, func(i, j int) bool { return mHits[i] < mHits[j] })
		deduped := mHits[:1]
		for i := 1; i < len(mHits); i++ {
			if mHits[i]-deduped[len(deduped)-1] > layerH/2 {
				deduped = append(deduped, mHits[i])
			}
		}
		if len(deduped)%2 != 0 {
			deduped = deduped[:len(deduped)-1]
		}
		for p := 0; p+1 < len(deduped); p += 2 {
			enterZ := deduped[p]
			exitZ := deduped[p+1]
			layMin := int(math.Ceil(float64(enterZ-minZ) / float64(layerH)))
			layMax := int(math.Floor(float64(exitZ-minZ) / float64(layerH)))
			if layMin < 0 {
				layMin = 0
			}
			if layMax >= nLayers {
				layMax = nLayers - 1
			}
			for layer := layMin; layer <= layMax; layer++ {
				activeLayers[layer] = struct{}{}
			}
		}
	}

	// Also activate layers at each hit Z (for thin/non-watertight features).
	for _, h := range hits {
		layer := int(math.Round(float64(h.Z-minZ) / float64(layerH)))
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
