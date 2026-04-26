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

// loadModel dispatches to the correct loader based on file extension. Returned
// vertices are in file units — apply loader.ScaleModel to convert to mm.
func loadModel(path string, objectIndex int) (*loader.LoadedModel, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".glb":
		return loader.LoadGLB(path, objectIndex)
	case ".3mf":
		return loader.Load3MF(path, objectIndex)
	case ".stl":
		return loader.LoadSTL(path, objectIndex)
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

// scalePreviewMesh applies a uniform scale to a MeshData's vertices.
// scale==1 returns the mesh unchanged; otherwise a shallow copy with a
// scaled Vertices slice is returned (other slices are shared).
func scalePreviewMesh(mesh *MeshData, scale float32) *MeshData {
	if mesh == nil || scale == 1 {
		return mesh
	}
	scaled := *mesh
	scaled.Vertices = make([]float32, len(mesh.Vertices))
	copy(scaled.Vertices, mesh.Vertices)
	for i := range scaled.Vertices {
		scaled.Vertices[i] *= scale
	}
	return &scaled
}

// stickerAtlasInfo bundles the encoded atlas plus the per-decal regions
// and the global atlas dimensions, so callers can reuse one atlas pack
// across multiple mesh-emission paths without re-encoding.
type stickerAtlasInfo struct {
	atlasStr string
	regions  []atlasRegion
	totalW   float32
	totalH   float32
}

// packStickerAtlas builds the atlas, encodes it, and returns nil when
// the atlas would be empty (no decals or zero-area output).
func packStickerAtlas(decals []*voxel.StickerDecal) *stickerAtlasInfo {
	if len(decals) == 0 {
		return nil
	}
	atlas, regions := buildStickerAtlas(decals)
	atlasStr := encodeAtlasTexture(atlas)
	if atlasStr == "" {
		return nil
	}
	totalW := float32(atlas.Bounds().Dx())
	totalH := float32(atlas.Bounds().Dy())
	if totalW == 0 || totalH == 0 {
		return nil
	}
	return &stickerAtlasInfo{atlasStr: atlasStr, regions: regions, totalW: totalW, totalH: totalH}
}

// remapDecalToAtlas converts one decal triangle's per-vertex UVs (in the
// decal's local [0,1]² space) plus its atlas region into the absolute-UV
// pair (atlasUVs, atlasBounds) the shader needs. atlasBounds is shared
// across the three vertices of the triangle.
func (info *stickerAtlasInfo) remapDecalToAtlas(triUV [3][2]float32, region atlasRegion) (atlasUVs [6]float32, atlasBounds [4]float32) {
	rX := float32(region.x)
	rW := float32(region.w)
	rH := float32(region.h)
	atlasBounds[0] = rX / info.totalW
	atlasBounds[1] = (rX + rW) / info.totalW
	atlasBounds[2] = 0
	atlasBounds[3] = rH / info.totalH
	for v := 0; v < 3; v++ {
		atlasUVs[v*2] = (rX + triUV[v][0]*rW) / info.totalW
		atlasUVs[v*2+1] = (1 - triUV[v][1]) * rH / info.totalH
	}
	return
}

// buildStickerOverlayMesh produces a preview mesh that contains ONLY the
// sticker-bearing triangles from `model`, with sticker atlas UVs attached.
// Used for the alpha-wrap overlay path: the wrap mesh has no native
// textures or relevant face colors, and rendering its full surface would
// hide the textured input mesh underneath. Returning just the sticker tris
// produces a mesh that's transparent everywhere except where stickers sit.
//
// Returns nil if no decals are non-empty.
func buildStickerOverlayMesh(model *loader.LoadedModel, decals []*voxel.StickerDecal) *MeshData {
	info := packStickerAtlas(decals)
	if info == nil {
		return nil
	}

	// Collect (face, decalIdx, triUV) tuples. When multiple decals claim
	// the same triangle, the last one wins, matching attachStickerOverlay.
	type ownedTri struct {
		decalIx int
		uvs     [3][2]float32
	}
	owners := make(map[int32]ownedTri)
	for di, d := range decals {
		for triIdx, triUV := range d.TriUVs {
			if int(triIdx) >= len(model.Faces) {
				continue
			}
			owners[triIdx] = ownedTri{decalIx: di, uvs: triUV}
		}
	}
	if len(owners) == 0 {
		return nil
	}

	nFaces := len(owners)
	vertices := make([]float32, 0, nFaces*9)
	faces := make([]uint32, 0, nFaces*3)
	faceColors := make([]uint16, 0, nFaces*3)
	stickerUVs := make([]float32, 0, nFaces*6)
	stickerMask := make([]uint8, 0, nFaces)
	stickerBounds := make([]float32, 0, nFaces*4)

	vertCount := uint32(0)
	for triIdx, ot := range owners {
		f := model.Faces[triIdx]
		for k := 0; k < 3; k++ {
			v := model.Vertices[f[k]]
			vertices = append(vertices, v[0], v[1], v[2])
		}
		faces = append(faces, vertCount, vertCount+1, vertCount+2)
		vertCount += 3
		// Face color is irrelevant — overlay only shows stickered area —
		// but must be set so flattenMesh-style shaders don't NaN.
		faceColors = append(faceColors, 0, 0, 0)

		uvs, bounds := info.remapDecalToAtlas(ot.uvs, info.regions[ot.decalIx])
		stickerUVs = append(stickerUVs, uvs[:]...)
		stickerBounds = append(stickerBounds, bounds[:]...)
		stickerMask = append(stickerMask, 1)
	}

	return &MeshData{
		Vertices:        vertices,
		Faces:           faces,
		FaceColors:      faceColors,
		StickerUVs:      stickerUVs,
		StickerFaceMask: stickerMask,
		StickerBounds:   stickerBounds,
		StickerAtlas:    info.atlasStr,
	}
}

// attachStickerOverlay returns a copy of mesh with sticker atlas overlay
// data attached. Used for the alpha-wrap-OFF path where the input mesh
// itself carries decals; faces without stickers retain their natural face
// color.
//
// NOTE: When multiple decals claim the same triangle, the last decal
// wins. The voxelizer composites all layers via alpha blending, so
// overlapping stickers can look different in the input preview vs. the
// output. Supporting true multi-layer compositing would require multiple
// atlas samples per fragment.
func attachStickerOverlay(mesh *MeshData, decals []*voxel.StickerDecal) *MeshData {
	info := packStickerAtlas(decals)
	if info == nil {
		return mesh
	}
	nFaces := len(mesh.Faces) / 3
	stickerUVs := make([]float32, nFaces*6)
	stickerMask := make([]uint8, nFaces)
	stickerBounds := make([]float32, nFaces*4)

	for di, d := range decals {
		region := info.regions[di]
		for triIdx, triUV := range d.TriUVs {
			if int(triIdx) >= nFaces {
				continue
			}
			uvs, bounds := info.remapDecalToAtlas(triUV, region)
			off := int(triIdx) * 6
			copy(stickerUVs[off:off+6], uvs[:])
			bOff := int(triIdx) * 4
			copy(stickerBounds[bOff:bOff+4], bounds[:])
			stickerMask[triIdx] = 1
		}
	}

	out := *mesh // shallow copy
	out.StickerUVs = stickerUVs
	out.StickerFaceMask = stickerMask
	out.StickerBounds = stickerBounds
	out.StickerAtlas = info.atlasStr
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
