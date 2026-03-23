// Package subdivide implements adaptive midpoint mesh subdivision with UV and
// face-texture-index interpolation.
package subdivide

import (
	"fmt"
	"math"

	"github.com/rtwfroody/text2filament/internal/loader"
)

const MaxIter = 12
const MaxVertices = 1_000_000

// TooManyVerticesError is returned when the estimated vertex count would
// exceed MaxVertices.
type TooManyVerticesError struct {
	Estimated int
}

func (e *TooManyVerticesError) Error() string {
	return fmt.Sprintf("estimated %d vertices would exceed budget of %d", e.Estimated, MaxVertices)
}

// edgeKey returns a canonical edge key (smaller index first).
func edgeKey(a, b uint32) [2]uint32 {
	if a < b {
		return [2]uint32{a, b}
	}
	return [2]uint32{b, a}
}

// Subdivide adaptively subdivides the mesh until no edge exceeds maxEdgeMM.
// Returns a new LoadedModel (textures passed through unchanged).
func Subdivide(model *loader.LoadedModel, maxEdgeMM float32) (*loader.LoadedModel, error) {
	currentVerts := make([][3]float32, len(model.Vertices))
	copy(currentVerts, model.Vertices)

	currentFaces := make([][3]uint32, len(model.Faces))
	copy(currentFaces, model.Faces)

	currentUVs := make([][2]float32, len(model.UVs))
	copy(currentUVs, model.UVs)

	currentFaceTex := make([]int32, len(model.FaceTextureIdx))
	copy(currentFaceTex, model.FaceTextureIdx)

	// Stash completed faces across iterations.
	var done []batchData

	for iter := 0; iter <= MaxIter; iter++ {
		// Determine which faces have edges that are too long.
		tooLongMask := make([]bool, len(currentFaces))
		anyTooLong := false
		for fi, face := range currentFaces {
			v0 := currentVerts[face[0]]
			v1 := currentVerts[face[1]]
			v2 := currentVerts[face[2]]
			if edgeLen(v0, v1) > maxEdgeMM ||
				edgeLen(v1, v2) > maxEdgeMM ||
				edgeLen(v2, v0) > maxEdgeMM {
				tooLongMask[fi] = true
				anyTooLong = true
			}
		}

		// Partition into ok and too-long.
		var okFaces [][3]uint32
		var okFaceTex []int32
		var longFaces [][3]uint32
		var longFaceTex []int32
		for fi, face := range currentFaces {
			if tooLongMask[fi] {
				longFaces = append(longFaces, face)
				longFaceTex = append(longFaceTex, currentFaceTex[fi])
			} else {
				okFaces = append(okFaces, face)
				okFaceTex = append(okFaceTex, currentFaceTex[fi])
			}
		}

		// Compact ok faces into a batch (remap vertices).
		if len(okFaces) > 0 {
			b := compactBatch(currentVerts, currentUVs, okFaces, okFaceTex)
			done = append(done, b)
		}

		if !anyTooLong {
			break
		}

		if iter >= MaxIter {
			return nil, fmt.Errorf("subdivision did not converge after %d iterations; try a larger --resolution value", MaxIter)
		}

		// Estimate vertex count after this pass.
		doneCount := 0
		for _, b := range done {
			doneCount += len(b.verts)
		}
		estimated := doneCount + len(currentVerts) + 2*len(longFaces)
		if estimated > MaxVertices {
			return nil, &TooManyVerticesError{Estimated: estimated}
		}

		// Subdivide the too-long faces.
		midpointMap := map[[2]uint32]uint32{}
		newVerts := make([][3]float32, len(currentVerts))
		copy(newVerts, currentVerts)
		newUVs := make([][2]float32, len(currentUVs))
		copy(newUVs, currentUVs)

		getMidpoint := func(a, b uint32) uint32 {
			key := edgeKey(a, b)
			if mid, ok := midpointMap[key]; ok {
				return mid
			}
			mid := uint32(len(newVerts))
			va, vb := currentVerts[a], currentVerts[b]
			ua, ub := currentUVs[a], currentUVs[b]
			newVerts = append(newVerts, [3]float32{
				(va[0] + vb[0]) * 0.5,
				(va[1] + vb[1]) * 0.5,
				(va[2] + vb[2]) * 0.5,
			})
			newUVs = append(newUVs, [2]float32{
				(ua[0] + ub[0]) * 0.5,
				(ua[1] + ub[1]) * 0.5,
			})
			midpointMap[key] = mid
			return mid
		}

		var newFaces [][3]uint32
		var newFaceTex []int32
		for fi, face := range longFaces {
			v0, v1, v2 := face[0], face[1], face[2]
			m01 := getMidpoint(v0, v1)
			m12 := getMidpoint(v1, v2)
			m20 := getMidpoint(v2, v0)
			tex := longFaceTex[fi]
			// 4 sub-faces
			newFaces = append(newFaces,
				[3]uint32{v0, m01, m20},
				[3]uint32{v1, m12, m01},
				[3]uint32{v2, m20, m12},
				[3]uint32{m01, m12, m20},
			)
			newFaceTex = append(newFaceTex, tex, tex, tex, tex)
		}

		currentVerts = newVerts
		currentUVs = newUVs
		currentFaces = newFaces
		currentFaceTex = newFaceTex
	}

	// Concatenate all batches with vertex offsets.
	totalVerts := 0
	totalFaces := 0
	for _, b := range done {
		totalVerts += len(b.verts)
		totalFaces += len(b.faces)
	}

	finalVerts := make([][3]float32, 0, totalVerts)
	finalUVs := make([][2]float32, 0, totalVerts)
	finalFaces := make([][3]uint32, 0, totalFaces)
	finalFaceTex := make([]int32, 0, totalFaces)

	offset := uint32(0)
	for _, b := range done {
		finalVerts = append(finalVerts, b.verts...)
		finalUVs = append(finalUVs, b.uvs...)
		for _, f := range b.faces {
			finalFaces = append(finalFaces, [3]uint32{
				f[0] + offset,
				f[1] + offset,
				f[2] + offset,
			})
		}
		finalFaceTex = append(finalFaceTex, b.faceTex...)
		offset += uint32(len(b.verts))
	}

	nTex := len(model.Textures)
	var noTextureMask []bool
	if model.NoTextureMask != nil {
		noTextureMask = make([]bool, len(finalFaceTex))
		for i, ti := range finalFaceTex {
			if ti >= int32(nTex) {
				noTextureMask[i] = true
			}
		}
	}

	return &loader.LoadedModel{
		Vertices:       finalVerts,
		Faces:          finalFaces,
		UVs:            finalUVs,
		Textures:       model.Textures,
		FaceTextureIdx: finalFaceTex,
		NoTextureMask:  noTextureMask,
	}, nil
}

func edgeLen(a, b [3]float32) float32 {
	dx := a[0] - b[0]
	dy := a[1] - b[1]
	dz := a[2] - b[2]
	return float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
}

// batchData holds one compacted batch of mesh data.
type batchData struct {
	verts   [][3]float32
	faces   [][3]uint32
	uvs     [][2]float32
	faceTex []int32
}

func compactBatch(
	verts [][3]float32,
	uvs [][2]float32,
	faces [][3]uint32,
	faceTex []int32,
) batchData {
	if len(faces) == 0 {
		return batchData{}
	}

	// Collect unique vertex indices in order of first appearance.
	seen := map[uint32]uint32{}
	var newVerts [][3]float32
	var newUVs [][2]float32

	remap := func(idx uint32) uint32 {
		if ni, ok := seen[idx]; ok {
			return ni
		}
		ni := uint32(len(newVerts))
		seen[idx] = ni
		newVerts = append(newVerts, verts[idx])
		newUVs = append(newUVs, uvs[idx])
		return ni
	}

	newFaces := make([][3]uint32, len(faces))
	for fi, f := range faces {
		newFaces[fi] = [3]uint32{remap(f[0]), remap(f[1]), remap(f[2])}
	}

	return batchData{
		verts:   newVerts,
		faces:   newFaces,
		uvs:     newUVs,
		faceTex: faceTex,
	}
}
