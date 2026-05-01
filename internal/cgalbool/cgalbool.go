// Package cgalbool computes boolean operations on closed triangle
// meshes using CGAL's Polygon_mesh_processing::corefine_and_compute_*.
//
// This package is a thin Go-facing wrapper around the CGO binding in
// internal/cgalbool/cgalbool. Like cgalclip it is geometry-only:
// inputs/outputs use loader.LoadedModel for shape, but only Vertices
// and Faces are read/written. UVs, vertex colors, and textures are
// not carried through — connector geometry inherits the surrounding
// half's appearance after the boolean lands.
//
// CGAL is required at build time. See cgalclip's package doc for the
// system-dependency story; both packages link against the same
// libraries.
//
// Numerical notes mirror cgalclip: EPIC kernel, exact predicates
// with float64 constructions. Results are watertight and
// topologically robust; vertex coordinates carry rounding error at
// the ULP scale (irrelevant for the printing pipeline downstream).
//
// Failure modes:
//
//   - Either input is non-orientable: surfaces as a clear error
//     before the boolean runs.
//   - Self-intersecting input or coplanar shared facets:
//     corefine_and_compute_* returns false and we surface an error.
//   - Empty or degenerate result: surfaces as an error rather than
//     returning a non-mesh.
package cgalbool

import (
	"fmt"

	"github.com/rtwfroody/ditherforge/internal/cgalbool/cgalbool"
	"github.com/rtwfroody/ditherforge/internal/loader"
)

// Union returns a ∪ b as a geometry-only LoadedModel.
func Union(a, b *loader.LoadedModel) (*loader.LoadedModel, error) {
	return run(a, b, cgalbool.Union)
}

// Difference returns a \ b as a geometry-only LoadedModel.
func Difference(a, b *loader.LoadedModel) (*loader.LoadedModel, error) {
	return run(a, b, cgalbool.Difference)
}

func run(a, b *loader.LoadedModel, op cgalbool.Op) (*loader.LoadedModel, error) {
	if a == nil || len(a.Faces) == 0 {
		return nil, fmt.Errorf("cgalbool: input A is empty")
	}
	if b == nil || len(b.Faces) == 0 {
		return nil, fmt.Errorf("cgalbool: input B is empty")
	}
	verts, faces, err := cgalbool.Compute(a.Vertices, a.Faces, b.Vertices, b.Faces, op)
	if err != nil {
		return nil, err
	}
	return &loader.LoadedModel{
		Vertices: verts,
		Faces:    faces,
	}, nil
}
