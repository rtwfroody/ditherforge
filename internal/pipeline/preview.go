package pipeline

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

const defaultGray = 180

// loadModel dispatches to the correct loader based on file extension and applies
// the given scale factor.
func loadModel(path string, scale float32, objectIndex int) (*loader.LoadedModel, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".glb":
		return loader.LoadGLB(path, scale, objectIndex)
	case ".3mf":
		return loader.Load3MF(path, scale, objectIndex)
	case ".stl":
		return loader.LoadSTL(path, scale, objectIndex)
	default:
		return nil, fmt.Errorf("unsupported format %q (use .glb, .3mf, or .stl)", ext)
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

	// Encode textures for the frontend preview. Large textures are
	// compressed to JPEG; small ones (under 128x128) are kept as PNG
	// since compression saves little and some models rely on precise
	// sampling of low-res textures.
	if len(model.Textures) > 0 {
		md.Textures = make([]string, len(model.Textures))
		for i, img := range model.Textures {
			if img == nil {
				continue
			}
			var buf bytes.Buffer
			bounds := img.Bounds()
			if bounds.Dx() >= 128 && bounds.Dy() >= 128 {
				if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode texture %d: %v\n", i, err)
					continue
				}
				md.Textures[i] = "jpeg:" + base64.StdEncoding.EncodeToString(buf.Bytes())
			} else {
				if err := png.Encode(&buf, img); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode texture %d: %v\n", i, err)
					continue
				}
				md.Textures[i] = "png:" + base64.StdEncoding.EncodeToString(buf.Bytes())
			}
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

// atlasRegion records where a single decal image was placed in the atlas.
type atlasRegion struct {
	x, w, h int // pixel offset and dimensions in the atlas
}

// buildStickerAtlas packs decal images into a single horizontal-strip atlas.
// Returns the atlas image and per-decal region info for UV remapping.
func buildStickerAtlas(decals []*voxel.StickerDecal) (image.Image, []atlasRegion) {
	regions := make([]atlasRegion, len(decals))
	totalW, maxH := 0, 0
	for i, d := range decals {
		b := d.Image.Bounds()
		regions[i] = atlasRegion{x: totalW, w: b.Dx(), h: b.Dy()}
		totalW += b.Dx()
		if b.Dy() > maxH {
			maxH = b.Dy()
		}
	}
	atlas := image.NewNRGBA(image.Rect(0, 0, totalW, maxH))
	for i, d := range decals {
		r := regions[i]
		draw.Draw(atlas, image.Rect(r.x, 0, r.x+r.w, r.h), d.Image, d.Image.Bounds().Min, draw.Src)
	}
	return atlas, regions
}

// encodeAtlasTexture encodes an atlas image as a base64 PNG string.
// Always uses PNG (not JPEG) because sticker images have alpha channels.
func encodeAtlasTexture(img image.Image) string {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return ""
	}
	return "png:" + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// attachStickerOverlay returns a copy of mesh with sticker atlas overlay data
// attached. The decals' TriUVs are remapped to atlas coordinates.
func attachStickerOverlay(mesh *MeshData, decals []*voxel.StickerDecal) *MeshData {
	nFaces := len(mesh.Faces) / 3

	atlas, regions := buildStickerAtlas(decals)
	atlasStr := encodeAtlasTexture(atlas)
	if atlasStr == "" {
		return mesh
	}
	totalW := float32(atlas.Bounds().Dx())
	totalH := float32(atlas.Bounds().Dy())
	if totalW == 0 || totalH == 0 {
		return mesh
	}

	stickerUVs := make([]float32, nFaces*6)
	stickerMask := make([]uint8, nFaces)
	stickerBounds := make([]float32, nFaces*4) // per-face [minU, maxU, minV, maxV] for shader clamping

	// NOTE: When multiple decals claim the same triangle, the last decal wins.
	// The voxelizer composites all layers via alpha blending, so overlapping
	// stickers may look different in the input preview vs. the output. Supporting
	// true multi-layer compositing would require multiple atlas samples per fragment.
	for di, d := range decals {
		r := regions[di]
		rX := float32(r.x)
		rW := float32(r.w)
		rH := float32(r.h)
		for triIdx, triUV := range d.TriUVs {
			if int(triIdx) >= nFaces {
				continue
			}
			stickerMask[triIdx] = 1
			off := int(triIdx) * 6
			// Atlas bounds for this decal region (used for per-fragment clamping).
			minU := rX / totalW
			maxU := (rX + rW) / totalW
			minV := float32(0)
			maxV := rH / totalH
			bOff := int(triIdx) * 4
			stickerBounds[bOff] = minU
			stickerBounds[bOff+1] = maxU
			stickerBounds[bOff+2] = minV
			stickerBounds[bOff+3] = maxV

			for v := 0; v < 3; v++ {
				localU := triUV[v][0]
				localV := triUV[v][1]
				stickerUVs[off+v*2] = (rX + localU*rW) / totalW
				stickerUVs[off+v*2+1] = (1 - localV) * rH / totalH
			}
		}
	}

	out := *mesh // shallow copy
	out.StickerUVs = stickerUVs
	out.StickerFaceMask = stickerMask
	out.StickerBounds = stickerBounds
	out.StickerAtlas = atlasStr
	return &out
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
