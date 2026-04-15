package loader

import (
	"fmt"
	"image"
	"image/color"

	"github.com/hschendel/stl"
)

// LoadSTL loads an STL file and returns a LoadedModel.
// STL files contain only geometry (no textures or colors), so all faces
// are assigned a default gray base color.
// objectIndex is ignored for STL files (always a single object).
func LoadSTL(path string, objectIndex int) (*LoadedModel, error) {
	solid, err := stl.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading STL: %w", err)
	}

	nTri := len(solid.Triangles)
	if nTri == 0 {
		return nil, fmt.Errorf("STL contains no triangles")
	}

	faces := make([][3]uint32, nTri)
	faceBaseColor := make([][4]uint8, nTri)

	// Deduplicate vertices by position. STL stores 3 vertices per triangle
	// with no sharing, so identical positions at shared edges are duplicated.
	posToIdx := make(map[[3]float32]uint32, nTri) // rough initial capacity
	var dedupVerts [][3]float32

	for i, tri := range solid.Triangles {
		var fi [3]uint32
		for j := 0; j < 3; j++ {
			pos := [3]float32{
				tri.Vertices[j][0],
				tri.Vertices[j][1],
				tri.Vertices[j][2],
			}
			if idx, ok := posToIdx[pos]; ok {
				fi[j] = idx
			} else {
				idx := uint32(len(dedupVerts))
				posToIdx[pos] = idx
				dedupVerts = append(dedupVerts, pos)
				fi[j] = idx
			}
		}
		faces[i] = fi
		faceBaseColor[i] = [4]uint8{200, 200, 200, 255}
	}

	nVerts := len(dedupVerts)

	// Build LoadedModel. STL has no textures or UVs.
	placeholder := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	placeholder.SetNRGBA(0, 0, color.NRGBA{128, 128, 128, 255})

	uvs := make([][2]float32, nVerts)
	faceTexIdx := make([]int32, nTri)
	noTextureMask := make([]bool, nTri)
	faceMeshIdx := make([]int32, nTri)
	for i := range faceTexIdx {
		faceTexIdx[i] = 1 // sentinel: len(Textures) = 1
		noTextureMask[i] = true
	}

	return &LoadedModel{
		Vertices:       dedupVerts,
		Faces:          faces,
		UVs:            uvs,
		Textures:       []image.Image{placeholder},
		FaceTextureIdx: faceTexIdx,
		FaceBaseColor:  faceBaseColor,
		NoTextureMask:  noTextureMask,
		FaceMeshIdx:    faceMeshIdx,
		NumMeshes:      1,
	}, nil
}
