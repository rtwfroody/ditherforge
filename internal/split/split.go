// Package split implements the geometry primitives for the Split feature:
// cutting a watertight mesh by a plane, capping each half with a planar
// triangulation, and (in later phases) baking connector pegs/pockets into
// the cut faces and laying the halves out side-by-side on the bed.
//
// This file (and the rest of phase 1) covers Cut + cap triangulation only.
// Connectors and layout live in connectors.go and layout.go (added in
// later phases).
package split

import (
	"fmt"
	"math"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/plog"
)

// Plane is a 3D plane in original-mesh coordinates. A point p lies on the
// plane iff Normal·p == D. Normal must be unit-length.
type Plane struct {
	Normal [3]float64
	D      float64
}

// ConnectorStyle selects what alignment features Cut bakes into the cut
// faces.
type ConnectorStyle int

const (
	// NoConnectors leaves both caps as flat planar surfaces.
	NoConnectors ConnectorStyle = iota
	// Pegs places a solid cylindrical peg on half 0's cap and a matching
	// cylindrical pocket on half 1's cap. Female radius = peg radius +
	// clearance.
	Pegs
	// Dowels punches matching cylindrical holes in both caps. Both holes
	// are oversized by clearance. The user prints separate dowels (or
	// uses hardware-store steel pins).
	Dowels
)

// ConnectorSettings controls connector placement and dimensions. The
// zero value (Style=NoConnectors) leaves caps flat.
type ConnectorSettings struct {
	Style       ConnectorStyle
	Count       int     // 0 = auto (heuristic on inscribed-circle radius); 1..3 explicit
	DiamMM      float64 // peg/dowel diameter in mm
	DepthMM     float64 // peg/pocket depth (per side for Dowels)
	ClearanceMM float64 // per-side radial clearance applied to female features
}

// AxisPlane builds a Plane perpendicular to one of the principal axes
// (axis: 0=X, 1=Y, 2=Z) at the given offset along that axis. Normal points
// in +axis direction. Invalid axis values fall back to Z; callers that
// can't tolerate that should validate before calling.
func AxisPlane(axis int, offset float64) Plane {
	if axis < 0 || axis > 2 {
		axis = 2
	}
	var n [3]float64
	n[axis] = 1
	return Plane{Normal: n, D: offset}
}

// signedDistance returns Normal·p - D. p is on the negative half when this
// is < 0, on the positive half when > 0.
func (p Plane) signedDistance(v [3]float32) float64 {
	return p.Normal[0]*float64(v[0]) +
		p.Normal[1]*float64(v[1]) +
		p.Normal[2]*float64(v[2]) - p.D
}

// CutResult is the output of Cut. Halves[0] and Halves[1] are independent
// closed-watertight meshes corresponding to the negative and positive
// sides of the plane respectively. CapFaces[i] lists the indices in
// Halves[i].Faces of the triangles that make up that half's cap (the
// planar fan that closed off the cut surface). Phase-2 connector code
// uses CapFaces to find the cap polygon to place pegs/pockets on.
//
// Plane is the cut plane that produced this result, stored so phase-3
// Layout can find the cap normal without the caller needing to keep
// track of the plane separately.
type CutResult struct {
	Halves   [2]*loader.LoadedModel
	CapFaces [2][]uint32
	Plane    Plane
}

// Cut splits a watertight model by a plane and caps each half with a
// triangulated planar surface, producing two closed-watertight halves.
// Optional alignment connectors (pegs or dowel holes) can be baked into
// the cut faces via the connectors parameter; pass ConnectorSettings{}
// for plain caps.
//
// The input model must be watertight (every edge has exactly two
// incident faces). If it is not, the output halves will not be watertight
// either; the caller is responsible for running Cut on the alpha-wrap
// output, not the raw input.
//
// Returns an error when:
//   - the cut plane misses the mesh entirely (no intersected triangles),
//   - the recovered cut polygon has degenerate or non-closed loops,
//   - cap triangulation fails (e.g. self-intersecting boundary),
//   - the cut produces multiple disconnected components per side,
//   - any model vertex lies exactly on the cut plane.
//
// On error, neither half is returned — splitting must succeed atomically.
func Cut(model *loader.LoadedModel, plane Plane, connectors ConnectorSettings) (*CutResult, error) {
	if model == nil || len(model.Vertices) == 0 || len(model.Faces) == 0 {
		return nil, fmt.Errorf("split.Cut: empty model")
	}
	if !isUnitNormal(plane.Normal) {
		return nil, fmt.Errorf("split.Cut: plane normal is not unit-length: %v", plane.Normal)
	}

	bbDiag := bboxDiag(model.Vertices)
	// eps is the half-width of the "on-plane" zone. It must be large
	// enough to absorb numerical instability in `signedDistance`
	// (dot(n,v)-D suffers catastrophic cancellation near zero, with
	// residual error ~ULP(bbDiag) ≈ 1.2e-7·bbDiag) AND the
	// downstream midpoint computation t = -d0/(d1-d0), which becomes
	// ill-conditioned when both endpoints are within a few ULPs of
	// zero — risking a midpoint that snaps onto an existing vertex,
	// the very degeneracy this check exists to prevent. So eps must
	// be at least 3-4 ULPs. We pick 4e-7·bbDiag (~3.4 ULPs).
	eps := 4e-7 * bbDiag
	if eps < 1e-9 {
		eps = 1e-9
	}

	// 1. Classify each vertex into -1 / 0 / +1 by signed distance.
	//    Vertices that land within ±eps of the plane (|d| <= eps) are
	//    snapped slightly off-plane along plane.Normal so every vertex
	//    has an unambiguous side. The cap-polygon walker assumes each
	//    cut-graph node has degree exactly 2; an on-plane vertex can
	//    appear in the cut polygon multiple times (a fan of triangles
	//    around it can straddle the plane in 4+ places), creating a
	//    figure-eight or self-touching loop the walker can't recover.
	//
	//    Snapping happens on a shallow clone of the model so the
	//    caller's mesh is unmodified. The displacement (2·eps,
	//    sub-micron on typical models) is far below user-perceptible
	//    drift and only touches the offending vertices.
	side := make([]int8, len(model.Vertices))
	var onPlaneIdx []int
	for i, v := range model.Vertices {
		d := plane.signedDistance(v)
		switch {
		case d < -eps:
			side[i] = -1
		case d > eps:
			side[i] = +1
		default:
			side[i] = 0
			onPlaneIdx = append(onPlaneIdx, i)
		}
	}
	if len(onPlaneIdx) > 0 {
		snapped := make([][3]float32, len(model.Vertices))
		copy(snapped, model.Vertices)
		shift := float32(2 * eps)
		nx := float32(plane.Normal[0])
		ny := float32(plane.Normal[1])
		nz := float32(plane.Normal[2])
		for _, i := range onPlaneIdx {
			snapped[i][0] += shift * nx
			snapped[i][1] += shift * ny
			snapped[i][2] += shift * nz
			side[i] = +1
		}
		clone := *model
		clone.Vertices = snapped
		model = &clone
		plog.Printf("  Split: snapped %d on-plane vertex(es) by %.3g mm along plane normal",
			len(onPlaneIdx), 2*eps)
	}

	// 2. Build the per-half mesh by splitting crossing triangles. cutEdges
	//    records the pairs of post-cut vertex indices (in each half's vertex
	//    array) that lie along the cut polygon — used in step 3 to walk
	//    closed loops.
	bld := newCutBuilder(model, plane)
	if err := bld.processFaces(side); err != nil {
		return nil, err
	}

	// 3. Walk cut edges into closed loops in the plane.
	loops, err := bld.recoverLoops()
	if err != nil {
		return nil, err
	}
	if len(loops[0]) == 0 || len(loops[1]) == 0 {
		// One side has no cap loop — the plane misses the mesh, or the
		// mesh sits entirely on one side. We treat this as an error so
		// the caller surfaces a clear "cut plane misses model" message.
		return nil, fmt.Errorf("split.Cut: cut plane does not intersect the mesh")
	}

	// 4. Place connectors and add their cap-circle "hole" loops to
	//    each half. Done before triangulation so the cap polygons
	//    naturally exclude the connector regions.
	placements := bld.placeConnectors(loops, plane, connectors)
	if len(placements) > 0 {
		bld.addConnectorHoles(&loops, plane, placements)
	}

	// 5. Cap each half by triangulating its loops. Each half's cap normal
	//    points away from the interior of that half: half 0 (negative
	//    side) has cap normal +plane.Normal; half 1 has -plane.Normal.
	capArea, err := bld.triangulateCaps(loops, plane)
	if err != nil {
		return nil, err
	}
	if capArea < eps*eps {
		return nil, fmt.Errorf("split.Cut: cap area below %g (cut plane is tangent to the surface; choose a different offset)", eps*eps)
	}

	// 6. Generate cylindrical body geometry for each placed connector
	//    (peg cylinder on male side, pocket walls + floor on the other
	//    half). Each body closes the corresponding cap hole, so the
	//    halves remain watertight.
	if len(placements) > 0 {
		bld.addConnectorBodies(plane, placements, connectors)
	}

	res := &CutResult{
		Halves:   bld.halves,
		CapFaces: bld.capFaces,
		Plane:    plane,
	}
	return res, nil
}

// isUnitNormal reports whether n has length within 1e-6 of 1.
func isUnitNormal(n [3]float64) bool {
	l2 := n[0]*n[0] + n[1]*n[1] + n[2]*n[2]
	return math.Abs(l2-1) < 1e-6
}

// bboxDiag returns the diagonal length of the model's bounding box in
// world units. Used to scale epsilons.
func bboxDiag(verts [][3]float32) float64 {
	if len(verts) == 0 {
		return 0
	}
	var lo, hi [3]float64
	for c := 0; c < 3; c++ {
		lo[c] = math.Inf(1)
		hi[c] = math.Inf(-1)
	}
	for _, v := range verts {
		for c := 0; c < 3; c++ {
			x := float64(v[c])
			if x < lo[c] {
				lo[c] = x
			}
			if x > hi[c] {
				hi[c] = x
			}
		}
	}
	dx := hi[0] - lo[0]
	dy := hi[1] - lo[1]
	dz := hi[2] - lo[2]
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}
