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
	// Pegs places a solid cylindrical peg on half 0's cap (the
	// low-coordinate side of the cut) and a matching cylindrical pocket
	// on half 1's cap. Female radius = peg radius + clearance.
	Pegs
	// PegsHigh is Pegs with the male/female sides swapped: the peg sits
	// on half 1's cap (the high-coordinate side) and the pocket on
	// half 0's cap.
	PegsHigh
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
	ClearanceMM float64 // per-side clearance for female features, applied both radially (pocket diameter) and axially (pocket depth)
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

// AxisBasis returns the right-handed in-plane basis (u, v) for the
// principal axis (0=X, 1=Y, 2=Z), with u × v equal to the axis unit
// normal. The convention is fixed per axis so the basis is stable as
// the user toggles axes, and is mirrored in
// pipeline.computeSplitPreviewFromVertices and App.svelte's
// cutPlanePreview. Invalid axis values fall back to Z.
func AxisBasis(axis int) (u, v [3]float64) {
	switch axis {
	case 0: // normal = +X → u=+Y, v=+Z
		return [3]float64{0, 1, 0}, [3]float64{0, 0, 1}
	case 1: // normal = +Y → u=+Z, v=+X
		return [3]float64{0, 0, 1}, [3]float64{1, 0, 0}
	default: // axis == 2, normal = +Z → u=+X, v=+Y
		return [3]float64{1, 0, 0}, [3]float64{0, 1, 0}
	}
}

// TiltedFrame returns the unit normal and right-handed in-plane basis
// (u, v) of a plane whose normal is the principal `axis` rotated by
// tiltADeg about the base U axis and then tiltBDeg about the resulting
// V axis. tiltADeg = tiltBDeg = 0 reproduces the axis-aligned frame
// exactly (normal = axis unit vector, basis = AxisBasis(axis)), so the
// split is bit-identical to the un-tilted path when both angles are 0.
//
// The whole orthonormal frame is rotated together, so (u, v) continue
// to span the tilted plane with u × v = normal. Mirrored in the
// frontend preview — keep the rotation order in sync.
func TiltedFrame(axis int, tiltADeg, tiltBDeg float64) (normal, u, v [3]float64) {
	var n [3]float64
	a := axis
	if a < 0 || a > 2 {
		a = 2
	}
	n[a] = 1
	u, v = AxisBasis(a)
	if tiltADeg == 0 && tiltBDeg == 0 {
		return n, u, v
	}
	// Rotate about the base U axis, then about the resulting V axis.
	ar := tiltADeg * math.Pi / 180
	br := tiltBDeg * math.Pi / 180
	n = rotateAboutAxis(n, u, ar)
	v = rotateAboutAxis(v, u, ar)
	n = rotateAboutAxis(n, v, br)
	u = rotateAboutAxis(u, v, br)
	return normalize3(n), normalize3(u), normalize3(v)
}

// PlaneThrough builds the Plane with the given unit normal passing
// through pivot: a point p lies on it iff Normal·p == Normal·pivot.
func PlaneThrough(normal, pivot [3]float64) Plane {
	return Plane{Normal: normal, D: dot3(normal, pivot)}
}

// FrameAlignRotation returns the row-major 3×3 rotation that maps the
// tilted cut frame produced by TiltedFrame(axis, tiltADeg, tiltBDeg)
// back to the axis-aligned base frame: it sends the tilted normal to
// the +axis unit vector and the tilted (u, v) to AxisBasis(axis). Layout
// applies it before a "cut face up/down" orientation so the tilted cut
// face seats flat on the bed. It is the identity for a zero tilt, so
// the un-tilted layout is unchanged.
func FrameAlignRotation(axis int, tiltADeg, tiltBDeg float64) [9]float64 {
	a := axis
	if a < 0 || a > 2 {
		a = 2
	}
	var n0 [3]float64
	n0[a] = 1
	u0, v0 := AxisBasis(a)
	n, u, v := TiltedFrame(a, tiltADeg, tiltBDeg)
	// R = n0·nᵀ + u0·uᵀ + v0·vᵀ. Then R·n = n0, R·u = u0, R·v = v0
	// (the frames are orthonormal). Row-major.
	var R [9]float64
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			R[3*r+c] = n0[r]*n[c] + u0[r]*u[c] + v0[r]*v[c]
		}
	}
	return R
}

// orientationAxis returns the principal axis (0=X, 1=Y, 2=Z) that the
// orientation points up, so Layout can tell whether an orientation is a
// "cut face up/down" choice (its axis equals the cut axis).
func orientationAxis(o Orientation) int {
	switch o {
	case OrientXUp, OrientXDown:
		return 0
	case OrientYUp, OrientYDown:
		return 1
	default: // OrientZUp, OrientZDown
		return 2
	}
}

// matMul3 returns the row-major product A·B of two 3×3 matrices.
func matMul3(a, b [9]float64) [9]float64 {
	var m [9]float64
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			m[3*r+c] = a[3*r]*b[c] + a[3*r+1]*b[3+c] + a[3*r+2]*b[6+c]
		}
	}
	return m
}

// rotateAboutAxis rotates vec around the unit-length axis by theta
// radians (Rodrigues' rotation formula).
func rotateAboutAxis(vec, axis [3]float64, theta float64) [3]float64 {
	c, s := math.Cos(theta), math.Sin(theta)
	cross := cross3(axis, vec)
	d := dot3(axis, vec) * (1 - c)
	return [3]float64{
		vec[0]*c + cross[0]*s + axis[0]*d,
		vec[1]*c + cross[1]*s + axis[1]*d,
		vec[2]*c + cross[2]*s + axis[2]*d,
	}
}

// Orientation selects which model-space axis Layout points "up" (+Z on
// the build plate) for a half before placing it. The user picks one per
// half independently. The choice is independent of the cut plane — it
// is a fixed re-mapping of the half's authored axes. The remaining spin
// about the vertical (yaw) is resolved deterministically so the other
// two axes stay as close to their authored orientation as possible.
type Orientation int

const (
	// OrientZUp keeps the model's +Z axis pointing up (identity
	// rotation). Default zero value; matches the legacy "original"
	// behaviour. Only the bbox-min-z=0 shift and side-by-side
	// translation are then applied.
	OrientZUp Orientation = iota
	// OrientZDown points the model's −Z axis up.
	OrientZDown
	// OrientXUp points the model's +X axis up.
	OrientXUp
	// OrientXDown points the model's −X axis up.
	OrientXDown
	// OrientYUp points the model's +Y axis up.
	OrientYUp
	// OrientYDown points the model's −Y axis up.
	OrientYDown
)

// CutResult is the output of Cut. Halves[0] and Halves[1] are
// independent closed-watertight meshes corresponding to the negative
// and positive sides of the plane respectively. Plane is the cut plane
// that produced this result, stored so phase-3 Layout can find the
// cap normal without the caller needing to keep track separately.
//
// Orientation[h] selects the per-half rotation applied by Layout. See
// the Orientation constants for semantics. The default zero value
// (OrientZUp) leaves the half in its authored orientation.
//
// Cap faces aren't tracked separately — they're just part of each
// half's face list. Callers that need to identify the cap should
// match face normals against the plane normal.
type CutResult struct {
	Halves      [2]*loader.LoadedModel
	Plane       Plane
	Orientation [2]Orientation
	// Axis is the principal cut axis (0=X, 1=Y, 2=Z) the plane was
	// tilted off, or -1 when unknown (a caller that built the plane
	// directly rather than via the tilt path). Layout uses it to decide
	// which orientations are "cut face up/down" — those whose up-axis
	// equals Axis seat the half on the cut face, so they are corrected
	// by CapAlign. -1 disables that correction.
	Axis int
	// CapAlign is the row-major 3×3 rotation that maps the tilted cut
	// frame back to the axis-aligned frame (cut normal → +Axis). Layout
	// applies it before a cut-face orientation so the tilted cap seats
	// flat on the bed. The identity for an un-tilted cut.
	CapAlign [9]float64
}

// Cut splits a watertight model by a plane, producing two closed
// halves. CGAL's clip handles all the geometry — vertex
// classification, cut-polygon recovery, cap triangulation, and
// multi-component / nested-cavity cases — robustly via exact
// predicates.
//
// When connectors.Style is Pegs, PegsHigh, or Dowels, applyConnectors recovers
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
		Halves:   halves,
		Plane:    plane,
		Axis:     -1, // unknown unless the caller sets it (see runSplit)
		CapAlign: IdentityTransform.Rotation,
	}, nil
}

// isUnitNormal reports whether n has length within 1e-6 of 1.
func isUnitNormal(n [3]float64) bool {
	l2 := n[0]*n[0] + n[1]*n[1] + n[2]*n[2]
	return math.Abs(l2-1) < 1e-6
}
