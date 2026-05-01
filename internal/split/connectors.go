package split

import (
	"github.com/rtwfroody/ditherforge/internal/cgalbool"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/plog"
)

// applyConnectors adds peg or dowel features to the cap surfaces of
// the two halves and returns possibly-mutated halves. On any failure
// for an individual connector, applyConnectors logs a warning and
// continues — failures isolate per-connector. If every connector
// fails, both halves come back unchanged (flat caps).
//
// Convention: cgalclip.Clip leaves half 0's cap with outward normal
// equal to +plane.Normal and half 1's cap with outward normal equal
// to -plane.Normal. Pegs (Style==Pegs) protrude from half 0 along
// +plane.Normal and matching pockets carve into half 1 from the
// -plane.Normal side. Dowels punch matching pockets in both halves.
func applyConnectors(halves [2]*loader.LoadedModel, plane Plane, settings ConnectorSettings) [2]*loader.LoadedModel {
	if settings.Style == NoConnectors {
		return halves
	}
	if settings.DiamMM <= 0 || settings.DepthMM <= 0 {
		plog.Printf("  Split: connectors requested but dimensions are zero (diam=%.3f, depth=%.3f); using flat caps", settings.DiamMM, settings.DepthMM)
		return halves
	}
	// Count == 0 means "auto"; pick a sensible default. count < 0 is
	// treated the same.
	count := settings.Count
	if count <= 0 {
		count = 2
	}

	// Recover cap polygons from half 0 (cap normal = +plane.Normal).
	// We use half 0 to plan placement, then mirror the same XY
	// positions onto half 1 — guarantees pegs and pockets line up.
	polys, err := recoverCapPolygons(halves[0], plane.Normal, plane.D)
	if err != nil {
		plog.Printf("  Split: cap polygon recovery failed (%v); using flat caps", err)
		return halves
	}

	// Spacing heuristic: at least 2.5× the connector diameter so pegs
	// don't touch each other. Best-effort — placePegs may yield fewer
	// pegs than requested if the polygon is small.
	minSpacing := 2.5 * settings.DiamMM
	centers2D, err := placePegsInPolygons(polys, count, minSpacing)
	if err != nil {
		plog.Printf("  Split: peg placement failed (%v); using flat caps", err)
		return halves
	}

	// Lift placement points back to 3D on the cut plane.
	uBasis, vBasis := perpBasis(plane.Normal)
	centers3D := make([][3]float64, len(centers2D))
	for i, c2 := range centers2D {
		centers3D[i] = [3]float64{
			c2[0]*uBasis[0] + c2[1]*vBasis[0] + plane.D*plane.Normal[0],
			c2[0]*uBasis[1] + c2[1]*vBasis[1] + plane.D*plane.Normal[1],
			c2[0]*uBasis[2] + c2[1]*vBasis[2] + plane.D*plane.Normal[2],
		}
	}

	// Determine cylinder dimensions per half.
	// Pegs: half 0 gets a male cylinder (radius = DiamMM/2);
	//       half 1 gets a female cylinder (radius = DiamMM/2 + Clearance).
	// Dowels: both halves get female cylinders (radius = DiamMM/2 + Clearance).
	// Cylinder height: 2*DepthMM total (so the cylinder straddles the
	// plane; the plane sits at the midpoint and each half intersects
	// it for DepthMM along the inward direction).
	maleR := settings.DiamMM / 2
	femaleR := settings.DiamMM/2 + settings.ClearanceMM
	halfHeight := settings.DepthMM

	type opKind int
	const (
		opUnion opKind = iota
		opDifference
	)
	type pendingOp struct {
		idx     int // which connector
		halfIdx int // 0 or 1
		radius  float64
		op      opKind // Union for peg into half 0, Difference for pockets
	}
	var ops []pendingOp
	switch settings.Style {
	case Pegs:
		for i := range centers3D {
			ops = append(ops, pendingOp{i, 0, maleR, opUnion})
			ops = append(ops, pendingOp{i, 1, femaleR, opDifference})
		}
	case Dowels:
		for i := range centers3D {
			ops = append(ops, pendingOp{i, 0, femaleR, opDifference})
			ops = append(ops, pendingOp{i, 1, femaleR, opDifference})
		}
	}

	const segments = 32

	// Apply ops sequentially. Each op rebuilds one half. Failure of a
	// single op skips that op only — if pegs is selected and the half-1
	// difference fails for connector i but the half-0 union succeeded,
	// half 0 ends up with a peg that has no pocket. Acceptable as a
	// degraded fallback (the user sees the warning and decides).
	plog.Printf("  Split: applying %d %s connector(s) at %d location(s)", len(ops), connectorStyleName(settings.Style), len(centers3D))

	out := halves
	for _, op := range ops {
		cyl, err := buildCylinder(plane.Normal, op.radius, halfHeight, segments)
		if err != nil {
			plog.Printf("  Split: cylinder build for connector %d failed (%v); skipping", op.idx, err)
			continue
		}
		cyl = translateMesh(cyl, centers3D[op.idx])
		var result *loader.LoadedModel
		var berr error
		switch op.op {
		case opUnion:
			result, berr = cgalbool.Union(out[op.halfIdx], cyl)
		case opDifference:
			result, berr = cgalbool.Difference(out[op.halfIdx], cyl)
		}
		if berr != nil {
			styleName := connectorStyleName(settings.Style)
			plog.Printf("  Split: %s connector %d on half %d failed (%v); using flat cap for this connector", styleName, op.idx, op.halfIdx, berr)
			continue
		}
		// Booleans drop UVs/colors/textures — re-attach the half's
		// pre-existing parallel arrays would be wrong because vertex
		// indexing has changed. Halves coming out of cgalclip.Clip are
		// already geometry-only (UVs/colors not populated), so this is
		// fine: the boolean output stays geometry-only.
		out[op.halfIdx] = result
	}

	return out
}

func connectorStyleName(s ConnectorStyle) string {
	switch s {
	case Pegs:
		return "peg"
	case Dowels:
		return "dowel"
	default:
		return "connector"
	}
}
