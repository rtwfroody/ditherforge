package cellslicer

import (
	clipper "github.com/ctessum/go.clipper"
)

// CellOverlap reports a single pair of cells within one slab whose
// Outer polygons overlap by more than the caller's tolerance. Cells in
// a slab are meant to tile the footprint without overlap (sharing only
// edges), so any positive-area intersection is a partition bug.
type CellOverlap struct {
	// I, J are the overlapping cells' indices within the slab's Cells
	// slice (I < J).
	I, J int
	// KindI, KindJ are the two cells' kinds, so a report can say
	// whether the overlap is ring-ring, ring-hex, or hex-hex.
	KindI, KindJ CellKind
	// AreaMM2 is the exact intersection area in mm².
	AreaMM2 float32
}

// DetectCellOverlaps returns every pair of cells in `cells` whose Outer
// polygons overlap by more than minAreaMM2. It bbox-prefilters pairs,
// then runs an exact Clipper intersection on the survivors and sums the
// result area. minAreaMM2 should be set a bit above the Clipper integer
// grid's rounding noise (≈edge-length × 1µm) so two cells that merely
// share an edge don't register; a real overlap is on the order of a
// whole cell area.
//
// O(n²) bbox tests plus one Clipper boolean per bbox-overlapping pair.
// Intended for diagnostic runs, not the hot path.
func DetectCellOverlaps(cells []Cell, minAreaMM2 float32) []CellOverlap {
	n := len(cells)
	type bbox struct{ minX, minY, maxX, maxY float32 }
	boxes := make([]bbox, n)
	for i := range cells {
		minX, minY, maxX, maxY := polyBounds(cells[i].Outer)
		boxes[i] = bbox{minX, minY, maxX, maxY}
	}
	var out []CellOverlap
	for i := 0; i < n; i++ {
		if len(cells[i].Outer) < 3 {
			continue
		}
		bi := boxes[i]
		for j := i + 1; j < n; j++ {
			if len(cells[j].Outer) < 3 {
				continue
			}
			bj := boxes[j]
			// Separating-axis bbox reject (touching boxes don't overlap).
			if bi.maxX <= bj.minX || bj.maxX <= bi.minX ||
				bi.maxY <= bj.minY || bj.maxY <= bi.minY {
				continue
			}
			area := polygonIntersectionArea(cells[i].Outer, cells[j].Outer)
			if area > minAreaMM2 {
				out = append(out, CellOverlap{
					I: i, J: j,
					KindI: cells[i].Kind, KindJ: cells[j].Kind,
					AreaMM2: area,
				})
			}
		}
	}
	return out
}

// polygonIntersectionArea returns the total area (mm², always
// non-negative) of the Boolean intersection of polygons a and b.
// Returns 0 when they don't overlap or the clip fails.
func polygonIntersectionArea(a, b []Point2) float32 {
	c := clipper.NewClipper(clipper.IoNone)
	c.AddPaths(clipper.Paths{pointsToClipperPath(a)}, clipper.PtSubject, true)
	c.AddPaths(clipper.Paths{pointsToClipperPath(b)}, clipper.PtClip, true)
	result, ok := c.Execute1(clipper.CtIntersection, clipper.PftNonZero, clipper.PftNonZero)
	if !ok {
		return 0
	}
	var area float32
	for _, path := range result {
		pts := clipperPathToPoints(path)
		if len(pts) < 3 {
			continue
		}
		ar := signedArea(pts)
		if ar < 0 {
			ar = -ar
		}
		area += ar
	}
	return area
}
