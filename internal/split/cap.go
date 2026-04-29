package split

import (
	"fmt"
	"math"
)

// triangulateCaps closes off both halves' open cut-faces with planar
// fans of triangles. Each half's cap normal points in the direction
// that keeps the half's surface oriented outward:
//
//   - Half 0 (negative side): cap normal is +plane.Normal.
//   - Half 1 (positive side): cap normal is -plane.Normal.
//
// Returns the total cap area summed across both halves; the caller
// uses it to detect tangent-plane cases (vanishing area).
func (b *cutBuilder) triangulateCaps(loops [2][][]uint32, plane Plane) (float64, error) {
	var total float64
	for h := 0; h < 2; h++ {
		// Cap normal in 3D.
		var capNormal [3]float64
		if h == 0 {
			capNormal = plane.Normal
		} else {
			capNormal = [3]float64{-plane.Normal[0], -plane.Normal[1], -plane.Normal[2]}
		}
		u, v := planeBasis(capNormal)

		// Project all of this half's loop vertices to 2D.
		half := b.halves[h]
		var loop2d [][]pt2
		var loopIdx [][]uint32
		for _, loop := range loops[h] {
			pts := make([]pt2, len(loop))
			ix := make([]uint32, len(loop))
			for k, vi := range loop {
				pts[k] = project3Dto2D(half.Vertices[vi], u, v)
				ix[k] = vi
			}
			loop2d = append(loop2d, pts)
			loopIdx = append(loopIdx, ix)
		}

		// Classify outer vs holes by signed area in this 2D basis.
		// In our basis, u × v = capNormal, so a CCW polygon in 2D
		// matches the cap's outward winding. The largest-area CCW loop
		// is the outer; any other loop (CW or smaller-area CCW) is a
		// hole. (For a simple non-nested cut polygon every loop will
		// itself be CCW in the cap's outward basis; "holes" arise only
		// for nested cavities, which this branch handles too.)
		areas := make([]float64, len(loop2d))
		for i, pts := range loop2d {
			areas[i] = signedArea(pts)
		}
		outerI := -1
		var bestArea float64
		for i, a := range areas {
			if math.Abs(a) > bestArea {
				bestArea = math.Abs(a)
				outerI = i
			}
		}
		if outerI < 0 {
			return 0, fmt.Errorf("triangulateCaps: half %d has no loops", h)
		}
		// If the outer's signed area is negative, reverse it so it's
		// CCW (and reverse the corresponding 3D index list to match).
		if areas[outerI] < 0 {
			reversePoly(loop2d[outerI], loopIdx[outerI])
			areas[outerI] = -areas[outerI]
		}
		// Verify each non-outer loop is actually nested inside the
		// outer (a true hole) rather than a separate connected
		// component (which Phase 1 doesn't support — see
		// docs/SPLIT.md "Known limitations: more than two connected
		// components per side"). A simple check: pick any hole
		// vertex and test if it's inside the outer polygon.
		for i, pts := range loop2d {
			if i == outerI {
				continue
			}
			if !pointInPolygon(pts[0], loop2d[outerI]) {
				return 0, fmt.Errorf("triangulateCaps: half %d: cut produced multiple disconnected components in the cap (cut plane intersects the model in two or more separate regions); choose a cut that passes through one connected piece", h)
			}
		}

		// Holes must be CW in this basis.
		var holes [][]pt2
		var holeIxs [][]uint32
		for i := range loop2d {
			if i == outerI {
				continue
			}
			if areas[i] > 0 {
				reversePoly(loop2d[i], loopIdx[i])
				areas[i] = -areas[i]
			}
			holes = append(holes, loop2d[i])
			holeIxs = append(holeIxs, loopIdx[i])
		}

		// Triangulate.
		tris, err := triangulate(loop2d[outerI], loopIdx[outerI], holes, holeIxs)
		if err != nil {
			return 0, fmt.Errorf("triangulateCaps: half %d: %w", h, err)
		}

		// Emit each triangle as a cap face on this half. Triangles
		// from triangulate() are CCW in 2D (outward in 3D for this
		// half's cap normal), so we can append them as-is.
		startFace := uint32(len(half.Faces))
		for _, t := range tris {
			b.appendFace(h, -1, t)
		}
		for fi := startFace; fi < uint32(len(half.Faces)); fi++ {
			b.capFaces[h] = append(b.capFaces[h], fi)
		}

		// Accumulate cap area in 2D (= 3D area, since the basis is
		// orthonormal). After classification, the outer's signed area
		// is positive (= |outer|) and each hole's is negative
		// (= -|hole|), so summing gives outer − holes — the actual
		// annular cap area.
		for _, a := range areas {
			total += a
		}
	}
	return total, nil
}
