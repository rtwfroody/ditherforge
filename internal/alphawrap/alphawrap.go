// Package alphawrap cleans up a triangle mesh by wrapping it with a
// watertight, orientable, manifold surface using CGAL's Alpha_wrap_3
// (Portaneri et al., 2022). When built with the "cgal" build tag the
// wrapping is done in-process via CGO; otherwise a Python sidecar
// (scripts/alpha_wrap.py) invoked via `uv run` is used as a fallback.
package alphawrap

import (
	"fmt"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// Wrap returns a geometry-only LoadedModel whose surface is the alpha-wrap
// of the input model. alpha and offset are in model coordinate units (mm
// after pipeline scaling). The returned model has only Vertices and Faces
// populated; UVs, colors, and textures are not carried through.
func Wrap(model *loader.LoadedModel, alpha, offset float32) (*loader.LoadedModel, error) {
	if alpha <= 0 || offset <= 0 {
		return nil, fmt.Errorf("alpha-wrap: alpha and offset must be positive (got alpha=%g offset=%g)", alpha, offset)
	}
	if len(model.Faces) == 0 {
		return nil, fmt.Errorf("alpha-wrap: input mesh has no faces")
	}
	return doWrap(model, alpha, offset)
}
