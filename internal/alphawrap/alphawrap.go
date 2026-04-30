// Package alphawrap cleans up a triangle mesh by wrapping it with a
// watertight, orientable, manifold surface using CGAL's Alpha_wrap_3
// (Portaneri et al., 2022). The wrapping is done in-process via CGO;
// CGAL is required at build time (system package on Linux/Windows,
// homebrew on macOS — see the release workflow for details).
package alphawrap

import (
	"fmt"

	"github.com/rtwfroody/ditherforge/internal/alphawrap/cgalwrap"
	"github.com/rtwfroody/ditherforge/internal/loader"
)

// Wrap returns a geometry-only LoadedModel whose surface is the
// alpha-wrap of the input model. alpha and offset are in model
// coordinate units (mm after pipeline scaling). The returned model
// has only Vertices and Faces populated; UVs, colors, and textures
// are not carried through.
func Wrap(model *loader.LoadedModel, alpha, offset float32) (*loader.LoadedModel, error) {
	if alpha <= 0 || offset <= 0 {
		return nil, fmt.Errorf("alpha-wrap: alpha and offset must be positive (got alpha=%g offset=%g)", alpha, offset)
	}
	if len(model.Faces) == 0 {
		return nil, fmt.Errorf("alpha-wrap: input mesh has no faces")
	}
	verts, faces, err := cgalwrap.AlphaWrap(model.Vertices, model.Faces, float64(alpha), float64(offset))
	if err != nil {
		return nil, err
	}
	return &loader.LoadedModel{
		Vertices: verts,
		Faces:    faces,
	}, nil
}
