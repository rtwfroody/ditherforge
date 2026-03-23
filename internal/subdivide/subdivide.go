// Package subdivide implements adaptive mesh subdivision using longest-edge
// bisection: only the longest edge of each too-long face is split per
// iteration, avoiding unnecessary refinement of short edges.
package subdivide

import (
	"fmt"
	"math"

	"github.com/rtwfroody/text2filament/internal/loader"
)

const MaxIter = 12

// TooManyVerticesError is returned when the estimated vertex count would
// exceed the caller's budget.
type TooManyVerticesError struct {
	Estimated int
}

func (e *TooManyVerticesError) Error() string {
	return fmt.Sprintf("estimated %d vertices would exceed budget", e.Estimated)
}

func makeEdge(a, b uint32) [2]uint32 {
	if a < b {
		return [2]uint32{a, b}
	}
	return [2]uint32{b, a}
}

func edgeLen(verts [][3]float32, a, b uint32) float32 {
	dx := verts[a][0] - verts[b][0]
	dy := verts[a][1] - verts[b][1]
	dz := verts[a][2] - verts[b][2]
	return float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
}

// Subdivide adaptively subdivides the mesh until no edge exceeds maxEdgeMM,
// using longest-edge bisection. Returns a new LoadedModel with textures
// passed through unchanged.
func Subdivide(model *loader.LoadedModel, maxEdgeMM float32, maxVertices int) (*loader.LoadedModel, error) {
	verts := append([][3]float32(nil), model.Vertices...)
	faces := append([][3]uint32(nil), model.Faces...)
	uvs := append([][2]float32(nil), model.UVs...)
	faceTex := append([]int32(nil), model.FaceTextureIdx...)

	// Stash done faces across iterations (those with all edges ≤ maxEdgeMM).
	// T-junctions at stash boundaries are acceptable for our use case.
	var doneVerts [][3]float32
	var doneUVs [][2]float32
	var doneFaces [][3]uint32
	var doneFaceTex []int32

	for iter := 0; iter <= MaxIter; iter++ {
		// Partition faces into ok (all edges short) and too-long.
		var okFaces [][3]uint32
		var okFaceTex []int32
		var longFaces [][3]uint32
		var longFaceTex []int32

		for fi, face := range faces {
			tooLong := edgeLen(verts, face[0], face[1]) > maxEdgeMM ||
				edgeLen(verts, face[1], face[2]) > maxEdgeMM ||
				edgeLen(verts, face[2], face[0]) > maxEdgeMM
			if tooLong {
				longFaces = append(longFaces, face)
				longFaceTex = append(longFaceTex, faceTex[fi])
			} else {
				okFaces = append(okFaces, face)
				okFaceTex = append(okFaceTex, faceTex[fi])
			}
		}

		// Compact and stash done faces.
		if len(okFaces) > 0 {
			b := compactBatch(verts, uvs, okFaces, okFaceTex)
			doneVerts = append(doneVerts, b.verts...)
			doneUVs = append(doneUVs, b.uvs...)
			offset := uint32(len(doneVerts) - len(b.verts))
			for _, f := range b.faces {
				doneFaces = append(doneFaces, [3]uint32{f[0] + offset, f[1] + offset, f[2] + offset})
			}
			doneFaceTex = append(doneFaceTex, b.faceTex...)
		}

		if len(longFaces) == 0 {
			break
		}
		if iter >= MaxIter {
			return nil, fmt.Errorf("subdivision did not converge after %d iterations; try a larger --resolution value", MaxIter)
		}

		// Mark only the longest edge of each too-long face.
		markedEdges := make(map[[2]uint32]bool, len(longFaces))
		for _, face := range longFaces {
			var bestLen float32
			var bestEdge [2]uint32
			for e := 0; e < 3; e++ {
				a, b := face[e], face[(e+1)%3]
				if l := edgeLen(verts, a, b); l > bestLen {
					bestLen = l
					bestEdge = makeEdge(a, b)
				}
			}
			markedEdges[bestEdge] = true
		}

		// Budget check: each marked edge produces exactly one new vertex.
		estimated := len(doneVerts) + len(verts) + len(markedEdges)
		if estimated > maxVertices {
			return nil, &TooManyVerticesError{Estimated: estimated}
		}

		// Create midpoint vertices for each marked edge.
		edgeToMid := make(map[[2]uint32]uint32, len(markedEdges))
		for e := range markedEdges {
			mid := uint32(len(verts))
			edgeToMid[e] = mid
			a, b := e[0], e[1]
			verts = append(verts, [3]float32{
				(verts[a][0] + verts[b][0]) * 0.5,
				(verts[a][1] + verts[b][1]) * 0.5,
				(verts[a][2] + verts[b][2]) * 0.5,
			})
			uvs = append(uvs, [2]float32{
				(uvs[a][0] + uvs[b][0]) * 0.5,
				(uvs[a][1] + uvs[b][1]) * 0.5,
			})
		}

		// Rebuild the too-long faces. Each face is split based on how many
		// of its edges were marked (1→2 tris, 2→3 tris, 3→4 tris).
		newFaces := make([][3]uint32, 0, len(longFaces)*2)
		newFaceTex := make([]int32, 0, len(longFaceTex)*2)

		for fi, face := range longFaces {
			tex := longFaceTex[fi]
			edges := [3][2]uint32{
				makeEdge(face[0], face[1]),
				makeEdge(face[1], face[2]),
				makeEdge(face[2], face[0]),
			}

			var splitMask [3]bool
			splitCount := 0
			for e := 0; e < 3; e++ {
				if markedEdges[edges[e]] {
					splitMask[e] = true
					splitCount++
				}
			}

			switch splitCount {
			case 1:
				// Bisect: split into 2 triangles along the marked edge.
				for e := 0; e < 3; e++ {
					if splitMask[e] {
						va, vb, vc := face[e], face[(e+1)%3], face[(e+2)%3]
						mid := edgeToMid[edges[e]]
						newFaces = append(newFaces,
							[3]uint32{va, mid, vc},
							[3]uint32{mid, vb, vc},
						)
						newFaceTex = append(newFaceTex, tex, tex)
						break
					}
				}

			case 2:
				// Split into 3 triangles. Find the one unsplit edge.
				for e := 0; e < 3; e++ {
					if !splitMask[e] {
						// e is unsplit; (e+1)%3 and (e+2)%3 are split.
						va := face[e]
						vb := face[(e+1)%3]
						vc := face[(e+2)%3]
						m1 := edgeToMid[edges[(e+1)%3]] // midpoint of vb–vc
						m2 := edgeToMid[edges[(e+2)%3]] // midpoint of vc–va
						newFaces = append(newFaces,
							[3]uint32{va, vb, m1},
							[3]uint32{va, m1, m2},
							[3]uint32{m1, vc, m2},
						)
						newFaceTex = append(newFaceTex, tex, tex, tex)
						break
					}
				}

			case 3:
				// All edges marked: standard midpoint subdivision into 4 triangles.
				m01 := edgeToMid[edges[0]]
				m12 := edgeToMid[edges[1]]
				m20 := edgeToMid[edges[2]]
				newFaces = append(newFaces,
					[3]uint32{face[0], m01, m20},
					[3]uint32{m01, face[1], m12},
					[3]uint32{m20, m12, face[2]},
					[3]uint32{m01, m12, m20},
				)
				newFaceTex = append(newFaceTex, tex, tex, tex, tex)
			}
		}

		faces = newFaces
		faceTex = newFaceTex
	}

	// Assemble final mesh: done batches + remaining (all-ok) faces.
	finalVerts := doneVerts
	finalUVs := doneUVs
	finalFaces := doneFaces
	finalFaceTex := doneFaceTex

	// Append the last working-set faces (compacted).
	if len(faces) > 0 {
		b := compactBatch(verts, uvs, faces, faceTex)
		offset := uint32(len(finalVerts))
		finalVerts = append(finalVerts, b.verts...)
		finalUVs = append(finalUVs, b.uvs...)
		for _, f := range b.faces {
			finalFaces = append(finalFaces, [3]uint32{f[0] + offset, f[1] + offset, f[2] + offset})
		}
		finalFaceTex = append(finalFaceTex, b.faceTex...)
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
