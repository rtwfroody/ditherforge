package loader

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"

	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"
	"github.com/rtwfroody/ditherforge/internal/render"
)

// ObjectInfo describes a single object within a multi-object file.
type ObjectInfo struct {
	Index     int    `json:"index"`
	Name      string `json:"name"`
	TriCount  int    `json:"triCount"`
	Thumbnail string `json:"thumbnail"` // data:image/png;base64,...
}

// renderThumbnail renders a small orthographic thumbnail of a mesh and returns
// it as a data URI.
func renderThumbnail(vertices [][3]float32, faces [][3]uint32) string {
	if len(faces) == 0 {
		return ""
	}
	const (
		azimuth    = 30.0
		elevation  = 20.0
		resolution = 128
	)
	bounds := render.ProjectedBounds(vertices, azimuth, elevation)
	colorFn := func(faceIdx int, baryU, baryV float64) [3]uint8 {
		return [3]uint8{200, 200, 200}
	}
	img := render.RenderColor(vertices, faces, azimuth, elevation, resolution, bounds, colorFn)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img.ToRGBA()); err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// Enumerate3MFObjects parses a 3MF file and returns info about each non-empty
// object, including a rendered thumbnail.
func Enumerate3MFObjects(path string) ([]ObjectInfo, error) {
	parsed, err := parse3MFModels(path)
	if err != nil {
		return nil, err
	}
	parsed.zr.Close()

	// Collect non-empty objects.
	type objEntry struct {
		obj   object3mf
		verts [][3]float32
		faces [][3]uint32
	}
	var entries []objEntry
	for _, m := range parsed.models {
		for _, obj := range m.Resources.Objects {
			mesh := obj.Mesh
			if len(mesh.Vertices.Vertex) == 0 || len(mesh.Triangles.Triangle) == 0 {
				continue
			}
			verts := make([][3]float32, len(mesh.Vertices.Vertex))
			for i, v := range mesh.Vertices.Vertex {
				verts[i] = [3]float32{v.X, v.Y, v.Z}
			}
			faces := make([][3]uint32, len(mesh.Triangles.Triangle))
			for i, tri := range mesh.Triangles.Triangle {
				faces[i] = [3]uint32{tri.V1, tri.V2, tri.V3}
			}
			entries = append(entries, objEntry{obj: obj, verts: verts, faces: faces})
		}
	}

	// Skip thumbnail rendering if there's only one object (caller won't show picker).
	if len(entries) <= 1 {
		return nil, nil
	}

	result := make([]ObjectInfo, len(entries))
	for i, e := range entries {
		name := e.obj.Name
		if name == "" {
			name = fmt.Sprintf("Object %d", e.obj.ID)
		}
		result[i] = ObjectInfo{
			Index:     i,
			Name:      name,
			TriCount:  len(e.faces),
			Thumbnail: renderThumbnail(e.verts, e.faces),
		}
	}

	return result, nil
}

// EnumerateGLBObjects parses a GLB file and returns info about each mesh,
// including a rendered thumbnail.
func EnumerateGLBObjects(path string) ([]ObjectInfo, error) {
	doc, err := gltf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening GLB: %w", err)
	}

	// Walk the scene graph to collect per-mesh geometry with transforms applied.
	// Objects are indexed by GLTF Mesh (not by primitive), so a multi-material
	// mesh with several primitives appears as one object in the picker.
	type meshData struct {
		name  string
		verts [][3]float32
		faces [][3]uint32
	}
	collected := make(map[int]*meshData) // meshCounter → data

	meshCounter := 0

	var visitNode func(nodeIdx int, parentTransform mat4)
	visitNode = func(nodeIdx int, parentTransform mat4) {
		node := doc.Nodes[nodeIdx]
		localM := nodeMatrix(node)
		worldM := mul(parentTransform, localM)

		if node.Mesh != nil {
			mesh := doc.Meshes[*node.Mesh]
			meshIdx := meshCounter
			meshCounter++
			for _, prim := range mesh.Primitives {
				if _, ok := prim.Attributes[gltf.POSITION]; !ok {
					continue
				}

				var positions [][3]float32
				var indices []uint32

				// Try Draco first.
				if dracoPositions, _, _, dracoIndices, ok := decodeDraco(doc, prim); ok {
					positions = dracoPositions
					indices = dracoIndices
				} else if prim.Extensions["KHR_draco_mesh_compression"] != nil {
					continue
				} else {
					posAccessor := doc.Accessors[prim.Attributes[gltf.POSITION]]
					if posAccessor.BufferView == nil {
						continue
					}
					positions, err = modeler.ReadPosition(doc, posAccessor, nil)
					if err != nil || len(positions) == 0 {
						continue
					}
					if prim.Indices != nil {
						rawIdx, err := modeler.ReadIndices(doc, doc.Accessors[*prim.Indices], nil)
						if err == nil {
							indices = rawIdx
						}
					}
				}

				if len(positions) == 0 {
					continue
				}

				// Apply world transform.
				transformed := make([][3]float32, len(positions))
				for i, p := range positions {
					transformed[i] = transformPoint(worldM, p)
				}

				if len(indices) == 0 {
					indices = make([]uint32, len(positions))
					for i := range indices {
						indices[i] = uint32(i)
					}
				}

				md, ok := collected[meshIdx]
				if !ok {
					name := mesh.Name
					if name == "" {
						name = fmt.Sprintf("Mesh %d", meshIdx)
					}
					md = &meshData{name: name}
					collected[meshIdx] = md
				}

				offset := uint32(len(md.verts))
				md.verts = append(md.verts, transformed...)
				for i := 0; i+2 < len(indices); i += 3 {
					md.faces = append(md.faces, [3]uint32{
						indices[i] + offset,
						indices[i+1] + offset,
						indices[i+2] + offset,
					})
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

	// Count non-empty meshes first.
	var nonEmptyKeys []int
	for i := 0; i < meshCounter; i++ {
		if md, ok := collected[i]; ok && len(md.faces) > 0 {
			nonEmptyKeys = append(nonEmptyKeys, i)
		}
	}

	// Skip thumbnail rendering if there's only one mesh (caller won't show picker).
	if len(nonEmptyKeys) <= 1 {
		return nil, nil
	}

	var result []ObjectInfo
	for _, i := range nonEmptyKeys {
		md := collected[i]

		// Apply Y-up to Z-up for consistent thumbnail rendering.
		verts := make([][3]float32, len(md.verts))
		for j, v := range md.verts {
			verts[j] = [3]float32{v[0], -v[2], v[1]}
		}

		result = append(result, ObjectInfo{
			Index:     i,
			Name:      md.name,
			TriCount:  len(md.faces),
			Thumbnail: renderThumbnail(verts, md.faces),
		})
	}

	return result, nil
}
