package pipeline

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"

	"github.com/rtwfroody/ditherforge/internal/loader"
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

// buildInputMeshData creates a MeshData from a loaded model, including texture
// and UV data when available for proper texture-mapped rendering.
func buildInputMeshData(model *loader.LoadedModel) *MeshData {
	md := flattenMesh(model, func(fi int) (uint8, uint8, uint8) {
		return sampleFaceColor(model, fi)
	})

	// Include UVs if available.
	if model.UVs != nil {
		md.UVs = make([]float32, len(model.UVs)*2)
		for i, uv := range model.UVs {
			md.UVs[i*2] = uv[0]
			md.UVs[i*2+1] = uv[1]
		}
	}

	// Encode textures as base64 JPEG.
	if len(model.Textures) > 0 {
		md.Textures = make([]string, len(model.Textures))
		for i, img := range model.Textures {
			if img == nil {
				continue
			}
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to encode texture %d: %v\n", i, err)
				continue
			}
			md.Textures[i] = base64.StdEncoding.EncodeToString(buf.Bytes())
		}
	}

	// Include per-face texture index.
	if model.FaceTextureIdx != nil {
		md.FaceTextureIdx = make([]int32, len(model.FaceTextureIdx))
		for fi, idx := range model.FaceTextureIdx {
			// Mark faces without texture as -1.
			if model.NoTextureMask != nil && model.NoTextureMask[fi] {
				md.FaceTextureIdx[fi] = -1
			} else if int(idx) >= len(model.Textures) || model.Textures[idx] == nil {
				md.FaceTextureIdx[fi] = -1
			} else {
				md.FaceTextureIdx[fi] = idx
			}
		}
	}

	return md
}

// sampleFaceColor returns an RGB color for a face using vertex colors, base
// color, or a fallback gray. Used for non-textured faces and output previews.
func sampleFaceColor(model *loader.LoadedModel, fi int) (uint8, uint8, uint8) {
	face := model.Faces[fi]

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
