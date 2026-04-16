//go:build cgal

package alphawrap

import (
	"github.com/rtwfroody/ditherforge/internal/alphawrap/cgalwrap"
	"github.com/rtwfroody/ditherforge/internal/loader"
)

var hasCGAL = true

func doWrap(model *loader.LoadedModel, alpha, offset float32) (*loader.LoadedModel, error) {
	outVerts, outFaces, err := cgalwrap.AlphaWrap(model.Vertices, model.Faces, float64(alpha), float64(offset))
	if err != nil {
		return nil, err
	}
	return &loader.LoadedModel{
		Vertices: outVerts,
		Faces:    outFaces,
	}, nil
}
