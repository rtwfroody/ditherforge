package pipeline

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

const defaultGray = 180

// loadModel dispatches to the correct loader based on file extension and applies
// the given scale factor.
func loadModel(path string, scale float32) (*loader.LoadedModel, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".glb":
		return loader.LoadGLB(path, scale)
	case ".3mf":
		return loader.Load3MF(path, scale)
	default:
		return nil, fmt.Errorf("unsupported format %q (use .glb or .3mf)", ext)
	}
}

// unitScaleForExt returns the base unit scale for a given file extension.
// GLB files are in meters and need to be converted to mm.
func unitScaleForExt(ext string) float32 {
	if ext == ".glb" {
		return 1000.0
	}
	return 1.0
}

// LoadPreview loads a model using ditherforge's Go loader and returns a
// MeshData suitable for 3D preview rendering.
func LoadPreview(path string) (*MeshData, error) {
	ext := strings.ToLower(filepath.Ext(path))
	scale := unitScaleForExt(ext)
	model, err := loadModel(path, scale)
	if err != nil {
		return nil, err
	}
	return buildInputMeshData(model), nil
}

// flattenMesh extracts flat vertex and face arrays from a loaded model, and
// calls colorFn for each face to determine its RGB color.
func flattenMesh(model *loader.LoadedModel, colorFn func(fi int) (uint8, uint8, uint8)) *MeshData {
	nVerts := len(model.Vertices)
	nFaces := len(model.Faces)

	vertices := make([]float32, nVerts*3)
	for i, v := range model.Vertices {
		vertices[i*3] = v[0]
		vertices[i*3+1] = v[1]
		vertices[i*3+2] = v[2]
	}

	faces := make([]uint32, nFaces*3)
	faceColors := make([]uint16, nFaces*3)

	for fi, face := range model.Faces {
		faces[fi*3] = face[0]
		faces[fi*3+1] = face[1]
		faces[fi*3+2] = face[2]

		r, g, b := colorFn(fi)
		faceColors[fi*3] = uint16(r)
		faceColors[fi*3+1] = uint16(g)
		faceColors[fi*3+2] = uint16(b)
	}

	return &MeshData{
		Vertices:   vertices,
		Faces:      faces,
		FaceColors: faceColors,
	}
}

// buildInputMeshData creates a MeshData from a loaded model, sampling per-face
// colors from textures, vertex colors, or base colors.
func buildInputMeshData(model *loader.LoadedModel) *MeshData {
	return flattenMesh(model, func(fi int) (uint8, uint8, uint8) {
		return sampleFaceColor(model, fi)
	})
}

// sampleFaceColor returns an RGB color for a face by sampling the texture at
// the face centroid UV, averaging vertex colors, or using the base color.
func sampleFaceColor(model *loader.LoadedModel, fi int) (uint8, uint8, uint8) {
	face := model.Faces[fi]

	// Try texture sampling at face centroid UV.
	// Use NoTextureMask to skip faces that don't have texture data.
	if model.FaceTextureIdx != nil && model.UVs != nil &&
		(model.NoTextureMask == nil || !model.NoTextureMask[fi]) {
		texIdx := model.FaceTextureIdx[fi]
		if int(texIdx) < len(model.Textures) && model.Textures[texIdx] != nil {
			uv0 := model.UVs[face[0]]
			uv1 := model.UVs[face[1]]
			uv2 := model.UVs[face[2]]
			cu := (uv0[0] + uv1[0] + uv2[0]) / 3
			cv := (uv0[1] + uv1[1] + uv2[1]) / 3
			c := voxel.BilinearSample(model.Textures[texIdx], cu, cv)
			return c[0], c[1], c[2]
		}
	}

	// Try vertex colors (average the 3 vertices).
	if model.VertexColors != nil &&
		int(face[0]) < len(model.VertexColors) &&
		int(face[1]) < len(model.VertexColors) &&
		int(face[2]) < len(model.VertexColors) {
		c0 := model.VertexColors[face[0]]
		c1 := model.VertexColors[face[1]]
		c2 := model.VertexColors[face[2]]
		r := (uint16(c0[0]) + uint16(c1[0]) + uint16(c2[0])) / 3
		g := (uint16(c0[1]) + uint16(c1[1]) + uint16(c2[1])) / 3
		b := (uint16(c0[2]) + uint16(c1[2]) + uint16(c2[2])) / 3
		return uint8(r), uint8(g), uint8(b)
	}

	// Try per-face base color (alpha channel intentionally ignored for preview).
	if model.FaceBaseColor != nil && fi < len(model.FaceBaseColor) {
		c := model.FaceBaseColor[fi]
		return c[0], c[1], c[2]
	}

	return defaultGray, defaultGray, defaultGray
}

// buildMeshData creates a MeshData from remesh output (model + palette assignments).
func buildMeshData(model *loader.LoadedModel, assignments []int32, paletteRGB [][3]uint8) *MeshData {
	return flattenMesh(model, func(fi int) (uint8, uint8, uint8) {
		if fi < len(assignments) {
			idx := int(assignments[fi])
			if idx >= 0 && idx < len(paletteRGB) {
				c := paletteRGB[idx]
				return c[0], c[1], c[2]
			}
		}
		return defaultGray, defaultGray, defaultGray
	})
}
