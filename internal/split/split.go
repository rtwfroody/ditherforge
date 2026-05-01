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
	"sync"

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
// When connectors.Style is Pegs or Dowels, applyConnectors recovers
// the cap polygon, places connector centers, builds peg/pocket
// cylinders, and applies CGAL boolean operations to bake them into
// the halves. Per-connector failures isolate: any one failure logs a
// warning and the rest of the pipeline continues. Total connector
// failure leaves the halves with flat caps.
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
	var (
		halves [2]*loader.LoadedModel
		errs   [2]error
		wg     sync.WaitGroup
	)
	wg.Add(2)
	// Half 0 (negative side): keep where Normal·p <= D.
	go func() {
		defer wg.Done()
		halves[0], errs[0] = cgalclip.Clip(model, plane.Normal, plane.D)
	}()
	// Half 1 (positive side): pass the flipped plane, so CGAL keeps
	// where -Normal·p <= -D (equivalently Normal·p >= D).
	go func() {
		defer wg.Done()
		negNormal := [3]float64{-plane.Normal[0], -plane.Normal[1], -plane.Normal[2]}
		halves[1], errs[1] = cgalclip.Clip(model, negNormal, -plane.D)
	}()
	wg.Wait()

	for i := range errs {
		if errs[i] != nil {
			return nil, fmt.Errorf("split.Cut: half %d: %w", i, errs[i])
		}
	}

	halves = applyConnectors(halves, plane, connectors)

	return &CutResult{
		Halves: halves,
		Plane:  plane,
	}, nil
}

// isUnitNormal reports whether n has length within 1e-6 of 1.
func isUnitNormal(n [3]float64) bool {
	l2 := n[0]*n[0] + n[1]*n[1] + n[2]*n[2]
	return math.Abs(l2-1) < 1e-6
}
