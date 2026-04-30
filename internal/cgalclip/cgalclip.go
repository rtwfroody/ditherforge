// Package cgalclip cuts a triangle mesh against a plane using CGAL's
// Polygon_mesh_processing::clip. The kept half is closed-watertight
// by construction (the cap surface is added automatically), replacing
// the hand-rolled triangle-classification + ear-clip pipeline that
// used to live in internal/split/.
//
// Built into the binary via CGO when the `cgal` build tag is set
// (which is the default in the release workflow and dev builds).
// Without the tag, Clip returns an error — there is no fallback,
// because the previous naive cut wasn't reliable enough to be useful.
//
// Numerical kernel: CGAL's EPIC kernel — exact predicates with
// inexact (float64) constructions. Cuts are topologically robust
// (every triangle is unambiguously above/below/on the plane), but
// the resulting cap-vertex coordinates are float64 with rounding
// error. For two halves of the same cut, cap vertex positions match
// up to a few ULPs but not bit-exactly. This is fine for the printing
// pipeline downstream — alpha-wrap, voxelize, and merge tolerate
// micron-scale jitter — but downstream code should not assume cap
// vertex equality across halves.
//
// Failure modes worth knowing about:
//
//   - Self-intersecting input. Clip is configured with
//     throw_on_self_intersection(true) so a non-watertight input
//     surfaces a CGAL exception ("Self_intersection_exception")
//     rather than producing garbage. Alpha-wrapped meshes are
//     supposed to be self-intersection-free; if you hit this,
//     re-run alpha-wrap with a tighter offset.
//   - Plane misses the input (no triangles cross). The clipped half
//     is empty and Clip returns an error.
//   - Plane lies tangent to a face. CGAL is strict; one half
//     ends up empty and Clip returns an error. The previous
//     hand-rolled cut "snapped" tangent vertices off the plane to
//     produce a sliver — that hack is gone.
package cgalclip

import (
	"fmt"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// Clip returns the half of model on the negative side of the plane
// (where normal·p <= d). To get the other half, flip both normal and
// d.
//
// The plane normal must be unit-length; CGAL's clip is robust to
// non-unit normals but the kernel runs faster with normalised input
// and downstream code sometimes assumes |normal|=1.
//
// Returns a geometry-only LoadedModel: only Vertices and Faces are
// populated. UVs, vertex colors, and textures are not carried through —
// the cap geometry has no source UVs, and the surrounding pipeline
// re-derives color information from the original mesh after the cut.
func Clip(model *loader.LoadedModel, normal [3]float64, d float64) (*loader.LoadedModel, error) {
	if model == nil || len(model.Faces) == 0 {
		return nil, fmt.Errorf("cgalclip: input mesh is empty")
	}
	return doClip(model, normal, d)
}
