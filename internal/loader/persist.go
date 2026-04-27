package loader

import (
	"bytes"
	"encoding/gob"
	"image"

	"github.com/rtwfroody/ditherforge/internal/imageraw"
)

// modelOnDisk is the on-disk gob representation of LoadedModel. Textures
// are stored as raw NRGBA bytes; the diskcache zstd layer handles
// compression.
type modelOnDisk struct {
	Vertices       [][3]float32
	Faces          [][3]uint32
	UVs            [][2]float32
	VertexColors   [][4]uint8
	Textures       []imageraw.Tex
	FaceTextureIdx []int32
	FaceAlpha      []float32
	FaceBaseColor  [][4]uint8
	NoTextureMask  []bool
	FaceMeshIdx    []int32
	NumMeshes      int
}

// GobEncode lets gob serialize a LoadedModel.
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
		od.Textures = make([]imageraw.Tex, len(m.Textures))
		for i, t := range m.Textures {
			od.Textures[i] = imageraw.FromImage(t)
		}
	}
	var out bytes.Buffer
	if err := gob.NewEncoder(&out).Encode(od); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// GobDecode lets gob deserialize a LoadedModel. Textures come back as
// concrete *image.NRGBA values.
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
	if len(od.Textures) > 0 {
		m.Textures = make([]image.Image, len(od.Textures))
		for i, t := range od.Textures {
			m.Textures[i] = imageraw.ToImage(t)
		}
	}
	return nil
}
