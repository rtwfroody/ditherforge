// Package loader loads a GLB file and extracts mesh geometry, per-vertex UVs,
// and texture images.
package loader

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"

	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"
)

// LoadedModel holds all extracted data from a GLB file.
type LoadedModel struct {
	Vertices       [][3]float32  // world-space, already transformed + scaled
	Faces          [][3]uint32
	UVs            [][2]float32  // per-vertex, aligned to Vertices
	Textures       []image.Image
	FaceTextureIdx []int32       // index into Textures; len(Textures) is sentinel for no-texture faces
	FaceAlpha      []float32     // per-face material alpha (0=transparent, 1=opaque); nil if all opaque
	FaceBaseColor  [][4]uint8    // per-face material base color (RGBA)
	NoTextureMask  []bool        // nil if no texture-less faces; true = use palette[0]
	FaceMeshIdx    []int32       // which original mesh each face belongs to
	NumMeshes      int           // total number of original meshes
}

// mat4 is a column-major 4x4 float64 matrix.
type mat4 [16]float64

// identity returns the 4x4 identity matrix.
func identity() mat4 {
	return mat4{
		1, 0, 0, 0,
		0, 1, 0, 0,
		0, 0, 1, 0,
		0, 0, 0, 1,
	}
}

// mul multiplies two column-major 4x4 matrices: result = a * b.
func mul(a, b mat4) mat4 {
	var r mat4
	for col := 0; col < 4; col++ {
		for row := 0; row < 4; row++ {
			var sum float64
			for k := 0; k < 4; k++ {
				sum += a[k*4+row] * b[col*4+k]
			}
			r[col*4+row] = sum
		}
	}
	return r
}

// transformPoint applies a column-major 4x4 matrix to a 3D point (w=1).
func transformPoint(m mat4, p [3]float32) [3]float32 {
	x, y, z := float64(p[0]), float64(p[1]), float64(p[2])
	return [3]float32{
		float32(m[0]*x + m[4]*y + m[8]*z + m[12]),
		float32(m[1]*x + m[5]*y + m[9]*z + m[13]),
		float32(m[2]*x + m[6]*y + m[10]*z + m[14]),
	}
}

var identityMatrix64 = [16]float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}

// nodeMatrix builds the local transform matrix for a GLTF node.
func nodeMatrix(node *gltf.Node) mat4 {
	// GLTF spec: Matrix and TRS are mutually exclusive.
	// qmuntal/gltf defaults Matrix to identity when not explicitly set.
	// If Matrix is non-identity, use it directly. Otherwise compose from TRS.
	if node.Matrix != identityMatrix64 {
		return mat4(node.Matrix)
	}

	// Compose from TRS.
	tx := node.Translation[0]
	ty := node.Translation[1]
	tz := node.Translation[2]

	rot := node.RotationOrDefault()
	qx, qy, qz, qw := rot[0], rot[1], rot[2], rot[3]

	sc := node.ScaleOrDefault()
	sx, sy, sz := sc[0], sc[1], sc[2]

	// Build rotation matrix from quaternion.
	r00 := 1 - 2*(qy*qy+qz*qz)
	r10 := 2 * (qx*qy + qz*qw)
	r20 := 2 * (qx*qz - qy*qw)

	r01 := 2 * (qx*qy - qz*qw)
	r11 := 1 - 2*(qx*qx+qz*qz)
	r21 := 2 * (qy*qz + qx*qw)

	r02 := 2 * (qx*qz + qy*qw)
	r12 := 2 * (qy*qz - qx*qw)
	r22 := 1 - 2*(qx*qx+qy*qy)

	// TRS = T * R * S (column-major: each column is 4 elements)
	return mat4{
		r00 * sx, r10 * sx, r20 * sx, 0,
		r01 * sy, r11 * sy, r21 * sy, 0,
		r02 * sz, r12 * sz, r22 * sz, 0,
		tx, ty, tz, 1,
	}
}

// primitiveData holds extracted data for one GLTF primitive.
type primitiveData struct {
	positions  [][3]float32
	uvs        [][2]float32
	indices    []uint32
	textureIdx int     // index into accumulated texture list; -1 if no texture
	meshIdx    int     // which GLTF mesh this primitive belongs to
	matAlpha   float32  // material-level alpha (from BaseColorFactor + AlphaMode)
	baseColor  [4]uint8 // material base color factor (RGBA, 0-255)
}

// LoadGLB loads a GLB file and returns a LoadedModel.
func LoadGLB(path string, scale float32) (*LoadedModel, error) {
	doc, err := gltf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening GLB: %w", err)
	}

	// Decode all images referenced in the document.
	decodedImages := make([]image.Image, len(doc.Images))
	for i, img := range doc.Images {
		if img.BufferView == nil {
			continue
		}
		bvIdx := *img.BufferView
		bv := doc.BufferViews[bvIdx]
		buf := doc.Buffers[bv.Buffer]
		imgBytes := buf.Data[bv.ByteOffset : bv.ByteOffset+bv.ByteLength]
		decoded, _, err := image.Decode(bytes.NewReader(imgBytes))
		if err != nil {
			return nil, fmt.Errorf("decoding image %d: %w", i, err)
		}
		decodedImages[i] = decoded
	}

	// Map GLTF image index → our texture list index (deduplicate).
	var texList []image.Image
	imageToTex := map[int]int{} // gltf image index → texList index

	resolveTexture := func(matIdx *int) (int, bool) {
		if matIdx == nil {
			return 0, false
		}
		mat := doc.Materials[*matIdx]
		if mat.PBRMetallicRoughness == nil {
			return 0, false
		}
		bct := mat.PBRMetallicRoughness.BaseColorTexture
		if bct == nil {
			return 0, false
		}
		tex := doc.Textures[bct.Index]
		if tex.Source == nil {
			return 0, false
		}
		imgIdx := *tex.Source
		if decodedImages[imgIdx] == nil {
			return 0, false
		}
		if ti, ok := imageToTex[imgIdx]; ok {
			return ti, true
		}
		ti := len(texList)
		texList = append(texList, decodedImages[imgIdx])
		imageToTex[imgIdx] = ti
		return ti, true
	}

	// Walk the default scene's node graph, accumulating transforms.
	var texturedPrims []primitiveData
	var untexturedPrims []primitiveData
	meshCounter := 0 // counts distinct mesh instances (node+mesh pairs)

	var visitNode func(nodeIdx int, parentTransform mat4)
	visitNode = func(nodeIdx int, parentTransform mat4) {
		node := doc.Nodes[nodeIdx]
		localM := nodeMatrix(node)
		worldM := mul(parentTransform, localM)

		if node.Mesh != nil {
			mesh := doc.Meshes[*node.Mesh]
			for _, prim := range mesh.Primitives {
				meshIdx := meshCounter
				meshCounter++
				posAttrIdx, ok := prim.Attributes[gltf.POSITION]
				if !ok {
					continue
				}

				positions, err := modeler.ReadPosition(doc, doc.Accessors[posAttrIdx], nil)
				if err != nil || len(positions) == 0 {
					continue
				}

				// Apply world transform to positions.
				transformed := make([][3]float32, len(positions))
				for i, p := range positions {
					transformed[i] = transformPoint(worldM, p)
				}

				// Read UVs.
				var uvs [][2]float32
				if texCoordAttrIdx, ok := prim.Attributes[gltf.TEXCOORD_0]; ok {
					uvs, _ = modeler.ReadTextureCoord(doc, doc.Accessors[texCoordAttrIdx], nil)
				}
				if len(uvs) != len(positions) {
					uvs = make([][2]float32, len(positions))
				}

				// Read indices.
				var indices []uint32
				if prim.Indices != nil {
					rawIdx, err := modeler.ReadIndices(doc, doc.Accessors[*prim.Indices], nil)
					if err == nil {
						indices = rawIdx
					}
				}
				if len(indices) == 0 {
					// No index buffer: generate sequential indices.
					indices = make([]uint32, len(positions))
					for i := range indices {
						indices[i] = uint32(i)
					}
				}

				texIdx, hasTexture := resolveTexture(prim.Material)
				alpha := float32(1.0)
				baseColor := [4]uint8{255, 255, 255, 255}
				if prim.Material != nil {
					mat := doc.Materials[*prim.Material]
					if mat.AlphaMode == gltf.AlphaBlend {
						if pbr := mat.PBRMetallicRoughness; pbr != nil && pbr.BaseColorFactor != nil {
							alpha = float32(pbr.BaseColorFactor[3])
						}
					}
					if pbr := mat.PBRMetallicRoughness; pbr != nil && pbr.BaseColorFactor != nil {
						bc := pbr.BaseColorFactor
						baseColor = [4]uint8{
							uint8(bc[0] * 255),
							uint8(bc[1] * 255),
							uint8(bc[2] * 255),
							uint8(bc[3] * 255),
						}
					}
				}
				pd := primitiveData{
					positions:  transformed,
					uvs:        uvs,
					indices:    indices,
					textureIdx: texIdx,
					meshIdx:    meshIdx,
					matAlpha:   alpha,
					baseColor:  baseColor,
				}
				if hasTexture {
					texturedPrims = append(texturedPrims, pd)
				} else {
					pd.textureIdx = -1
					untexturedPrims = append(untexturedPrims, pd)
				}
			}
		}

		for _, child := range node.Children {
			visitNode(child, worldM)
		}
	}

	sceneIdx := 0
	if doc.Scene != nil {
		sceneIdx = *doc.Scene
	}
	if sceneIdx < len(doc.Scenes) {
		for _, rootNode := range doc.Scenes[sceneIdx].Nodes {
			visitNode(rootNode, identity())
		}
	}

	if len(texturedPrims) == 0 {
		return nil, fmt.Errorf("GLB contains no textured meshes")
	}
	if len(untexturedPrims) > 0 {
		fmt.Printf("  Warning: %d primitives have no texture; their faces will use palette index 0.\n", len(untexturedPrims))
	}

	nTex := len(texList) // sentinel value for untextured faces

	// Concatenate all primitives.
	var allVerts [][3]float32
	var allFaces [][3]uint32
	var allUVs [][2]float32
	var allFaceTex []int32
	var allFaceAlpha []float32
	var allFaceBaseColor [][4]uint8
	var allFaceMesh []int32
	hasNonOpaque := false

	appendPrim := func(pd primitiveData, texIdx int32) {
		offset := uint32(len(allVerts))
		allVerts = append(allVerts, pd.positions...)
		allUVs = append(allUVs, pd.uvs...)
		for i := 0; i+2 < len(pd.indices); i += 3 {
			allFaces = append(allFaces, [3]uint32{
				pd.indices[i] + offset,
				pd.indices[i+1] + offset,
				pd.indices[i+2] + offset,
			})
			allFaceTex = append(allFaceTex, texIdx)
			allFaceAlpha = append(allFaceAlpha, pd.matAlpha)
			allFaceBaseColor = append(allFaceBaseColor, pd.baseColor)
			allFaceMesh = append(allFaceMesh, int32(pd.meshIdx))
			if pd.matAlpha < 1.0 {
				hasNonOpaque = true
			}
		}
	}

	for _, pd := range texturedPrims {
		appendPrim(pd, int32(pd.textureIdx))
	}
	for _, pd := range untexturedPrims {
		appendPrim(pd, int32(nTex)) // sentinel
	}

	// Apply Y-up to Z-up transform and scale.
	// GLTF: Y-up; slicers: Z-up.
	// Transform: x'=x*scale, y'=-z*scale, z'=y*scale
	for i, v := range allVerts {
		allVerts[i] = [3]float32{
			v[0] * scale,
			-v[2] * scale,
			v[1] * scale,
		}
	}

	// Deduplicate vertices by (position, UV) pair. Vertices at UV seams share
	// the same position but have different UVs and must remain separate.
	{
		type posUV struct {
			pos [3]float32
			uv  [2]float32
		}
		keyToIdx := make(map[posUV]uint32, len(allVerts))
		remap := make([]uint32, len(allVerts))
		var newVerts [][3]float32
		var newUVs [][2]float32
		for i, v := range allVerts {
			key := posUV{pos: v, uv: allUVs[i]}
			if idx, ok := keyToIdx[key]; ok {
				remap[i] = idx
			} else {
				idx := uint32(len(newVerts))
				keyToIdx[key] = idx
				remap[i] = idx
				newVerts = append(newVerts, v)
				newUVs = append(newUVs, allUVs[i])
			}
		}
		for fi, f := range allFaces {
			allFaces[fi] = [3]uint32{remap[f[0]], remap[f[1]], remap[f[2]]}
		}
		allVerts = newVerts
		allUVs = newUVs
	}

	// Build NoTextureMask.
	var noTextureMask []bool
	if len(untexturedPrims) > 0 {
		noTextureMask = make([]bool, len(allFaces))
		for i, ti := range allFaceTex {
			if ti >= int32(nTex) {
				noTextureMask[i] = true
			}
		}
	}


	var faceAlpha []float32
	if hasNonOpaque {
		faceAlpha = allFaceAlpha
	}

	return &LoadedModel{
		Vertices:       allVerts,
		Faces:          allFaces,
		UVs:            allUVs,
		Textures:       texList,
		FaceTextureIdx: allFaceTex,
		FaceAlpha:      faceAlpha,
		FaceBaseColor:  allFaceBaseColor,
		NoTextureMask:  noTextureMask,
		FaceMeshIdx:    allFaceMesh,
		NumMeshes:      meshCounter,
	}, nil
}

