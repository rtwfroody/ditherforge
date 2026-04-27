package loader

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"image"
	"image/png"
)

// modelOnDisk is the on-disk gob representation of LoadedModel. It mirrors
// LoadedModel's fields except Textures, which is replaced by PNG-encoded
// byte slices because gob can't serialize image.Image directly.
type modelOnDisk struct {
	Vertices       [][3]float32
	Faces          [][3]uint32
	UVs            [][2]float32
	VertexColors   [][4]uint8
	TexturesPNG    [][]byte
	FaceTextureIdx []int32
	FaceAlpha      []float32
	FaceBaseColor  [][4]uint8
	NoTextureMask  []bool
	FaceMeshIdx    []int32
	NumMeshes      int
}

// GobEncode lets gob serialize a LoadedModel. Textures are PNG-encoded.
func (m *LoadedModel) GobEncode() ([]byte, error) {
	od := modelOnDisk{
		Vertices:       m.Vertices,
		Faces:          m.Faces,
		UVs:            m.UVs,
		VertexColors:   m.VertexColors,
		FaceTextureIdx: m.FaceTextureIdx,
		FaceAlpha:      m.FaceAlpha,
		FaceBaseColor:  m.FaceBaseColor,
		NoTextureMask:  m.NoTextureMask,
		FaceMeshIdx:    m.FaceMeshIdx,
		NumMeshes:      m.NumMeshes,
	}
	if len(m.Textures) > 0 {
		od.TexturesPNG = make([][]byte, len(m.Textures))
		for i, t := range m.Textures {
			if t == nil {
				continue
			}
			var buf bytes.Buffer
			if err := png.Encode(&buf, t); err != nil {
				return nil, fmt.Errorf("encode texture %d: %w", i, err)
			}
			od.TexturesPNG[i] = buf.Bytes()
		}
	}
	var out bytes.Buffer
	if err := gob.NewEncoder(&out).Encode(od); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// GobDecode lets gob deserialize a LoadedModel. PNG textures are decoded back
// into image.Image (concrete type as returned by png.Decode).
func (m *LoadedModel) GobDecode(data []byte) error {
	var od modelOnDisk
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&od); err != nil {
		return err
	}
	m.Vertices = od.Vertices
	m.Faces = od.Faces
	m.UVs = od.UVs
	m.VertexColors = od.VertexColors
	m.FaceTextureIdx = od.FaceTextureIdx
	m.FaceAlpha = od.FaceAlpha
	m.FaceBaseColor = od.FaceBaseColor
	m.NoTextureMask = od.NoTextureMask
	m.FaceMeshIdx = od.FaceMeshIdx
	m.NumMeshes = od.NumMeshes
	if len(od.TexturesPNG) > 0 {
		m.Textures = make([]image.Image, len(od.TexturesPNG))
		for i, b := range od.TexturesPNG {
			if len(b) == 0 {
				continue
			}
			img, err := png.Decode(bytes.NewReader(b))
			if err != nil {
				return fmt.Errorf("decode texture %d: %w", i, err)
			}
			m.Textures[i] = img
		}
	}
	return nil
}
