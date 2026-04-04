//go:build cgo

package loader

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/qmuntal/draco-go/draco"
	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"
)

// dracoExt is the parsed KHR_draco_mesh_compression extension data.
type dracoExt struct {
	BufferView int            `json:"bufferView"`
	Attributes map[string]int `json:"attributes"`
}

// decodeDraco attempts to decode a Draco-compressed primitive.
// Returns positions, UVs, vertex colors, indices, and true if successful.
func decodeDraco(doc *gltf.Document, prim *gltf.Primitive) ([][3]float32, [][2]float32, [][4]uint8, []uint32, bool) {
	extRaw, ok := prim.Extensions["KHR_draco_mesh_compression"]
	if !ok {
		return nil, nil, nil, nil, false
	}
	// The extension data may be raw JSON or already unmarshalled.
	var ext dracoExt
	switch v := extRaw.(type) {
	case json.RawMessage:
		if err := json.Unmarshal(v, &ext); err != nil {
			return nil, nil, nil, nil, false
		}
	default:
		// Try re-marshalling and unmarshalling.
		data, err := json.Marshal(v)
		if err != nil {
			return nil, nil, nil, nil, false
		}
		if err := json.Unmarshal(data, &ext); err != nil {
			return nil, nil, nil, nil, false
		}
	}

	if ext.BufferView < 0 || ext.BufferView >= len(doc.BufferViews) {
		fmt.Fprintf(os.Stderr, "Warning: Draco bufferView %d out of range\n", ext.BufferView)
		return nil, nil, nil, nil, false
	}
	bv := doc.BufferViews[ext.BufferView]
	bvData, err := modeler.ReadBufferView(doc, bv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Draco buffer read failed: %v\n", err)
		return nil, nil, nil, nil, false
	}

	if draco.GetEncodedGeometryType(bvData) != draco.EGT_TRIANGULAR_MESH {
		fmt.Fprintf(os.Stderr, "Warning: Draco geometry is not a triangle mesh\n")
		return nil, nil, nil, nil, false
	}

	m := draco.NewMesh()
	d := draco.NewDecoder()
	if err := d.DecodeMesh(m, bvData); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Draco decode failed: %v\n", err)
		return nil, nil, nil, nil, false
	}

	// Read positions.
	posID, ok := ext.Attributes["POSITION"]
	if !ok {
		return nil, nil, nil, nil, false
	}
	posAttr := m.AttrByUniqueID(uint32(posID))
	if posAttr == nil {
		return nil, nil, nil, nil, false
	}
	nPoints := m.NumPoints()
	posFlat := make([]float32, nPoints*3)
	if _, ok := m.AttrData(posAttr, posFlat); !ok {
		fmt.Fprintf(os.Stderr, "Warning: Draco position attribute read failed\n")
		return nil, nil, nil, nil, false
	}
	positions := make([][3]float32, nPoints)
	for i := uint32(0); i < nPoints; i++ {
		positions[i] = [3]float32{posFlat[i*3], posFlat[i*3+1], posFlat[i*3+2]}
	}

	// Read UVs (optional).
	var uvs [][2]float32
	if uvID, ok := ext.Attributes["TEXCOORD_0"]; ok {
		uvAttr := m.AttrByUniqueID(uint32(uvID))
		if uvAttr != nil {
			uvFlat := make([]float32, nPoints*2)
			if _, ok := m.AttrData(uvAttr, uvFlat); ok {
				uvs = make([][2]float32, nPoints)
				for i := uint32(0); i < nPoints; i++ {
					uvs[i] = [2]float32{uvFlat[i*2], uvFlat[i*2+1]}
				}
			}
		}
	}

	// Read vertex colors (optional). Handles both uint8 and float32 storage,
	// and both VEC3 (RGB) and VEC4 (RGBA) component counts.
	var vertColors [][4]uint8
	if colorID, ok := ext.Attributes["COLOR_0"]; ok {
		colorAttr := m.AttrByUniqueID(uint32(colorID))
		if colorAttr != nil {
			nComp := int(colorAttr.NumComponents())
			if nComp < 3 {
				nComp = 3
			}
			if colorAttr.DataType() == draco.DT_FLOAT32 {
				colorFlat := make([]float32, nPoints*uint32(nComp))
				if _, ok := m.AttrData(colorAttr, colorFlat); ok {
					vertColors = make([][4]uint8, nPoints)
					for i := uint32(0); i < nPoints; i++ {
						off := i * uint32(nComp)
						vertColors[i][0] = uint8(clampF32(colorFlat[off]*255, 0, 255))
						vertColors[i][1] = uint8(clampF32(colorFlat[off+1]*255, 0, 255))
						vertColors[i][2] = uint8(clampF32(colorFlat[off+2]*255, 0, 255))
						if nComp >= 4 {
							vertColors[i][3] = uint8(clampF32(colorFlat[off+3]*255, 0, 255))
						} else {
							vertColors[i][3] = 255
						}
					}
				}
			} else {
				colorFlat := make([]uint8, nPoints*uint32(nComp))
				if _, ok := m.AttrData(colorAttr, colorFlat); ok {
					vertColors = make([][4]uint8, nPoints)
					for i := uint32(0); i < nPoints; i++ {
						off := i * uint32(nComp)
						vertColors[i][0] = colorFlat[off]
						vertColors[i][1] = colorFlat[off+1]
						vertColors[i][2] = colorFlat[off+2]
						if nComp >= 4 {
							vertColors[i][3] = colorFlat[off+3]
						} else {
							vertColors[i][3] = 255
						}
					}
				}
			}
		}
	}

	// Read face indices. Note: m.Faces() has a bug where the returned
	// slice is truncated to NumFaces() instead of NumFaces()*3.
	// Pre-allocate the correct size and ignore the return value.
	nFaces := m.NumFaces()
	indices := make([]uint32, nFaces*3)
	m.Faces(indices)

	return positions, uvs, vertColors, indices, true
}
