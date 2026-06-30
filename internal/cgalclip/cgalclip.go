// Package cgalclip cuts a triangle mesh against a plane using CGAL's
// Polygon_mesh_processing::clip. The kept half is closed-watertight
// by construction (the cap surface is added automatically), replacing
// the hand-rolled triangle-classification + ear-clip pipeline that
// used to live in internal/split/.
//
// CGAL is required at build time. The release workflow installs it
// via the system package manager (apt/brew/pacman); dev machines need
// the same.
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
//   - Self-intersecting input. Clip first repairs self-intersections
//     in place (CGAL's local remove_self_intersections) because the
//     plane corefinement otherwise aborts with "Unauthorized
//     intersections of constraints" where the plane crosses an
//     intersecting region. Alpha-wrapped meshes are intersection-free,
//     but the post-wrap QEM decimation can reintroduce a few hundred
//     pairs in thin regions; the repair clears them while keeping the
//     mesh a valid 2-manifold. Truly garbage input that still self-
//     intersects after the repair surfaces a CGAL exception via
//     throw_on_self_intersection(true).
//   - Plane misses the input (no triangles cross). The clipped half
//     is empty and Clip returns an error.
//   - Plane lies tangent to a face. CGAL is strict; one half
//     ends up empty and Clip returns an error. The previous
//     hand-rolled cut "snapped" tangent vertices off the plane to
//     produce a sliver — that hack is gone.
package cgalclip

import (
	"fmt"

	"github.com/rtwfroody/ditherforge/internal/cgalclip/cgalclip"
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
