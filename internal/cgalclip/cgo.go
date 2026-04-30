//go:build cgal

package cgalclip

import (
	"github.com/rtwfroody/ditherforge/internal/cgalclip/cgalclip"
	"github.com/rtwfroody/ditherforge/internal/loader"
)

// HasCGAL reports whether the binary was built with CGAL support (the
// `cgal` build tag). Tests that need a real clip should skip when
// this is false.
const HasCGAL = true

func doClip(model *loader.LoadedModel, normal [3]float64, d float64) (*loader.LoadedModel, error) {
	verts, faces, err := cgalclip.Clip(model.Vertices, model.Faces,
		normal[0], normal[1], normal[2], d)
	if err != nil {
		return nil, err
	}
	return &loader.LoadedModel{
		Vertices: verts,
		Faces:    faces,
	}, nil
}
