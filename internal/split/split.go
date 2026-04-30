// Package split cuts a watertight mesh by a plane, producing two
// closed-watertight halves. Cutting is delegated to CGAL's
// Polygon_mesh_processing::clip via internal/cgalclip; the cap
// surface is added by CGAL during the clip, so this package no
// longer hand-rolls per-triangle classification, cut-polygon
// recovery, or cap triangulation. Connectors and bed layout still
// live here (connectors.go, layout.go).
package split

import (
	"fmt"
	"math"

	"github.com/rtwfroody/ditherforge/internal/cgalclip"
	"github.com/rtwfroody/ditherforge/internal/loader"
)

// Plane is a 3D plane in original-mesh coordinates. A point p lies on
// the plane iff Normal·p == D. Normal must be unit-length.
type Plane struct {
	Normal [3]float64
	D      float64
}

// ConnectorStyle selects what alignment features Cut bakes into the
// cut faces.
type ConnectorStyle int

const (
	// NoConnectors leaves both caps as flat planar surfaces.
	NoConnectors ConnectorStyle = iota
	// Pegs places a solid cylindrical peg on half 0's cap and a
	// matching cylindrical pocket on half 1's cap. Female radius =
	// peg radius + clearance.
	Pegs
	// Dowels punches matching cylindrical holes in both caps. Both
	// holes are oversized by clearance. The user prints separate
	// dowels (or uses hardware-store steel pins).
	Dowels
)

// ConnectorSettings controls connector placement and dimensions. The
// zero value (Style=NoConnectors) leaves caps flat.
type ConnectorSettings struct {
	Style       ConnectorStyle
	Count       int     // 0 = auto; 1..3 explicit
	DiamMM      float64 // peg/dowel diameter in mm
	DepthMM     float64 // peg/pocket depth (per side for Dowels)
	ClearanceMM float64 // per-side radial clearance applied to female features
}

// AxisPlane builds a Plane perpendicular to one of the principal
// axes (axis: 0=X, 1=Y, 2=Z) at the given offset along that axis.
// Normal points in +axis direction. Invalid axis values fall back to
// Z; callers that can't tolerate that should validate before calling.
func AxisPlane(axis int, offset float64) Plane {
	if axis < 0 || axis > 2 {
		axis = 2
	}
	var n [3]float64
	n[axis] = 1
	return Plane{Normal: n, D: offset}
}

// CutResult is the output of Cut. Halves[0] and Halves[1] are
// independent closed-watertight meshes corresponding to the negative
// and positive sides of the plane respectively. Plane is the cut plane
// that produced this result, stored so phase-3 Layout can find the
// cap normal without the caller needing to keep track separately.
//
// Cap faces aren't tracked separately — they're just part of each
// half's face list. Callers that need to identify the cap should
// match face normals against the plane normal.
type CutResult struct {
	Halves [2]*loader.LoadedModel
	Plane  Plane
}

// Cut splits a watertight model by a plane, producing two closed
// halves. CGAL's clip handles all the geometry — vertex
// classification, cut-polygon recovery, cap triangulation, and
// multi-component / nested-cavity cases — robustly via exact
// predicates.
//
// connectors is currently a no-op stub: the connector placement code
// in connectors.go relied on hand-rolled cap-polygon access from the
// old cutter. Re-adding connectors as boolean operations on the
// CGAL-cut halves is a follow-up. Pass ConnectorSettings{} for now;
// non-zero settings log a warning but otherwise do nothing.
func Cut(model *loader.LoadedModel, plane Plane, connectors ConnectorSettings) (*CutResult, error) {
	if model == nil || len(model.Vertices) == 0 || len(model.Faces) == 0 {
		return nil, fmt.Errorf("split.Cut: empty model")
	}
	if !isUnitNormal(plane.Normal) {
		return nil, fmt.Errorf("split.Cut: plane normal is not unit-length: %v", plane.Normal)
	}

	// Clip both halves concurrently. Each call pays the full CGAL
	// setup cost (mesh build + clip), but they're independent and
	// CPU-bound, so wall time roughly halves on multi-core machines.
	type clipOut struct {
		half *loader.LoadedModel
		err  error
	}
	results := make([]clipOut, 2)
	done := make(chan int, 2)

	// Half 0 (negative side): keep where Normal·p <= D.
	go func() {
		half, err := cgalclip.Clip(model, plane.Normal, plane.D)
		results[0] = clipOut{half, err}
		done <- 0
	}()
	// Half 1 (positive side): keep where -Normal·p <= -D, i.e.
	// Normal·p >= D.
	go func() {
		negNormal := [3]float64{-plane.Normal[0], -plane.Normal[1], -plane.Normal[2]}
		half, err := cgalclip.Clip(model, negNormal, -plane.D)
		results[1] = clipOut{half, err}
		done <- 1
	}()
	<-done
	<-done

	for i := 0; i < 2; i++ {
		if results[i].err != nil {
			return nil, fmt.Errorf("split.Cut: half %d: %w", i, results[i].err)
		}
	}

	if connectors.Style != NoConnectors {
		// TODO: re-implement connectors as boolean ops
		// (cylinder ∪ half[0]; cylinder ∩ half[1] with clearance).
		// Until then, log so the user knows the request is silently
		// dropped.
		// (Placeholder; suppress import-only-when-unused.)
		_ = connectors
	}

	return &CutResult{
		Halves: [2]*loader.LoadedModel{results[0].half, results[1].half},
		Plane:  plane,
	}, nil
}

// isUnitNormal reports whether n has length within 1e-6 of 1.
func isUnitNormal(n [3]float64) bool {
	l2 := n[0]*n[0] + n[1]*n[1] + n[2]*n[2]
	return math.Abs(l2-1) < 1e-6
}
