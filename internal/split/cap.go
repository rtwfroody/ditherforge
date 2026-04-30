package split

import (
	"fmt"
	"math"

	"github.com/rtwfroody/ditherforge/internal/plog"
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

		// Classify each loop into the outer/hole hierarchy by counting
		// how many other loops contain its first vertex. Even depth
		// (0, 2, 4, …) → outer of a region; odd depth → hole. For
		// each hole, the smallest-area enclosing loop at one shallower
		// depth is its outer parent.
		//
		// This handles three increasingly complex cases uniformly:
		//   1. Single-component cut: one outer, zero or more nested
		//      holes (e.g. cut through a torus).
		//   2. Multi-component cut: several disjoint outer regions
		//      (e.g. cut through an apollo capsule with thrusters
		//      sticking out — body + each thruster is its own region).
		//   3. Cavity-in-cavity: nested holes (rare from real
		//      watertight meshes, but the formula handles it).
		areas := make([]float64, len(loop2d))
		for i, pts := range loop2d {
			areas[i] = signedArea(pts)
		}
		depth := make([]int, len(loop2d))
		parent := make([]int, len(loop2d))
		for i := range loop2d {
			parent[i] = -1
			for j, other := range loop2d {
				if i == j {
					continue
				}
				if !pointInPolygon(loop2d[i][0], other) {
					continue
				}
				depth[i]++
				if parent[i] < 0 || math.Abs(areas[j]) < math.Abs(areas[parent[i]]) {
					parent[i] = j
				}
			}
		}

		// Group holes under their outer parent. Walk up the parent
		// chain to find the nearest even-depth ancestor; that's the
		// outer this hole belongs to.
		holesByOuter := make(map[int][]int)
		for i := range loop2d {
			if depth[i]%2 == 0 {
				// Outer: ensure CCW so triangulate() can run as-is.
				if areas[i] < 0 {
					reversePoly(loop2d[i], loopIdx[i])
					areas[i] = -areas[i]
				}
				if _, ok := holesByOuter[i]; !ok {
					holesByOuter[i] = nil
				}
				continue
			}
			// Hole: find the enclosing outer.
			outerIdx := parent[i]
			for outerIdx >= 0 && depth[outerIdx]%2 != 0 {
				outerIdx = parent[outerIdx]
			}
			if outerIdx < 0 {
				return 0, fmt.Errorf("triangulateCaps: half %d: hole loop has no enclosing outer (loop classification is inconsistent)", h)
			}
			// Hole must be CW in the cap's outward basis.
			if areas[i] > 0 {
				reversePoly(loop2d[i], loopIdx[i])
				areas[i] = -areas[i]
			}
			holesByOuter[outerIdx] = append(holesByOuter[outerIdx], i)
		}

		// Triangulate each (outer, holes) group independently. Cap
		// area accumulates the signed sum (outer area − hole areas)
		// across every group.
		//
		// If a single region fails to triangulate (typically a
		// genuinely self-intersecting polygon from bridge
		// interference or a non-manifold cut sequence), we log a
		// warning and skip that region rather than abort the entire
		// cut. The user gets a half that's non-watertight at the
		// skipped region but otherwise valid — enough to see whether
		// the cut location is worth pursuing. If every region fails
		// and the cap ends up empty, that does abort, since a half
		// with no cap at all is unusable.
		regionsTried := 0
		regionsCapped := 0
		var lastErr error
		for outerI, holeIdxs := range holesByOuter {
			regionsTried++
			holes := make([][]pt2, 0, len(holeIdxs))
			holeIxs := make([][]uint32, 0, len(holeIdxs))
			for _, hi := range holeIdxs {
				holes = append(holes, loop2d[hi])
				holeIxs = append(holeIxs, loopIdx[hi])
			}
			tris, err := triangulate(loop2d[outerI], loopIdx[outerI], holes, holeIxs)
			if err != nil {
				lastErr = err
				plog.Printf("  Split: half %d: skipping cap region (outer %d verts, %d hole(s)) — %v",
					h, len(loop2d[outerI]), len(holes), err)
				continue
			}
			regionsCapped++
			startFace := uint32(len(half.Faces))
			for _, t := range tris {
				b.appendFace(h, -1, t)
			}
			for fi := startFace; fi < uint32(len(half.Faces)); fi++ {
				b.capFaces[h] = append(b.capFaces[h], fi)
			}
			total += areas[outerI]
			for _, hi := range holeIdxs {
				total += areas[hi] // already negative
			}
		}
		if regionsCapped == 0 && regionsTried > 0 {
			return 0, fmt.Errorf("triangulateCaps: half %d: all %d cap regions failed to triangulate; last error: %w", h, regionsTried, lastErr)
		}
		if regionsCapped < regionsTried {
			plog.Printf("  Split: half %d: capped %d/%d regions (skipped regions leave the half non-watertight at those spots)",
				h, regionsCapped, regionsTried)
		}
	}
	return total, nil
}
