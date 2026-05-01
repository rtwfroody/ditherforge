package split

import (
	"fmt"
	"math"
	"sort"
)

// placePegs picks N peg-center positions inside the given polygon
// (with holes), spaced reasonably far apart. The polygon is in 2D
// plane-basis coordinates (the same basis recoverCapPolygons emits).
//
// boundaryClearance is the minimum distance every peg center must
// keep from the polygon boundary (outer loop and any hole). The
// caller passes the peg diameter so a circle of 2× the peg diameter
// can fit fully inside the polygon around each peg center, leaving
// peg-radius worth of wall around every peg. Pixels closer than
// boundaryClearance to the boundary are excluded from candidacy.
//
// Algorithm: rasterize the polygon into a binary mask at fixed
// resolution, run a multi-source BFS distance transform from the
// outside-mask to find each inside pixel's distance to the boundary,
// then place pegs greedily — first at the inside pixel closest to
// the polygon centroid, and each subsequent peg at the inside pixel
// maximally far from all previously placed pegs (subject to
// boundary-clearance).
//
// Returns peg centers in polygon coordinates. Returns an error if no
// inside pixels survive the clearance erosion (polygon too small for
// the requested clearance, or polygon too thin to fit a peg).
//
// For polygons-with-multiple-components callers should call this
// once per polygon, dividing N proportionally to area.
func placePegs(poly capPolygon, count int, minSpacing, boundaryClearance float64) ([][2]float64, error) {
	if count <= 0 {
		return nil, fmt.Errorf("placePegs: count must be positive, got %d", count)
	}
	if len(poly.outer) < 3 {
		return nil, fmt.Errorf("placePegs: outer loop has < 3 vertices")
	}

	// Bbox.
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, p := range poly.outer {
		if p[0] < minX {
			minX = p[0]
		}
		if p[0] > maxX {
			maxX = p[0]
		}
		if p[1] < minY {
			minY = p[1]
		}
		if p[1] > maxY {
			maxY = p[1]
		}
	}
	dx := maxX - minX
	dy := maxY - minY
	if dx <= 0 || dy <= 0 {
		return nil, fmt.Errorf("placePegs: degenerate bounding box")
	}

	// Resolution: 200 pixels along the longer axis. The grid is padded
	// by one pixel of guaranteed-outside on every side so the
	// chamfer distance transform always has seed pixels — without
	// that pad, a polygon that fills its bbox (e.g. an axis-aligned
	// rectangle) starts the scan with zero outside cells, the
	// propagation never reaches the inside pixels, and every inside
	// pixel ends up with a fictitious "very large" distance.
	const targetRes = 200
	step := math.Max(dx, dy) / targetRes
	innerW := int(math.Ceil(dx/step)) + 1
	innerH := int(math.Ceil(dy/step)) + 1
	W := innerW + 2
	H := innerH + 2

	// Rasterize: pixel (i, j) of the padded grid corresponds to world
	// (minX + (i-1)*step, minY + (j-1)*step). The 1-pixel border
	// (i==0, i==W-1, j==0, j==H-1) is always mask=false.
	mask := make([]bool, W*H)
	for j := 1; j < H-1; j++ {
		y := minY + float64(j-1)*step
		for i := 1; i < W-1; i++ {
			x := minX + float64(i-1)*step
			p := [2]float64{x, y}
			if !pointInPolygon2D(p, poly.outer) {
				continue
			}
			inHole := false
			for _, h := range poly.holes {
				if pointInPolygon2D(p, h) {
					inHole = true
					break
				}
			}
			if inHole {
				continue
			}
			mask[j*W+i] = true
		}
	}

	// Distance transform: dist[idx] = euclidean distance from pixel
	// idx to the nearest non-mask (outside or hole) pixel. Multi-
	// source BFS using chamfer 3-4 distances (a cheap approximation
	// of the L2 distance, exact to a few percent).
	dist := computeDistanceTransform(mask, W, H, step)

	// Build the eligible-set: inside pixels whose distance-to-
	// boundary >= boundaryClearance. If the clearance erodes
	// everything, fall back to the unrestricted inside-set so we
	// don't silently produce zero pegs on a small polygon.
	insideIdx := make([]int, 0, W*H/2)
	for idx, ok := range mask {
		if !ok {
			continue
		}
		if dist[idx] < boundaryClearance {
			continue
		}
		insideIdx = append(insideIdx, idx)
	}
	if len(insideIdx) == 0 {
		// Polygon too small for the requested clearance — fall back
		// to "any inside pixel" so the user gets at least one peg
		// placed in the most-interior location, rather than a
		// silent no-op.
		for idx, ok := range mask {
			if !ok {
				continue
			}
			insideIdx = append(insideIdx, idx)
		}
	}
	if len(insideIdx) == 0 {
		return nil, fmt.Errorf("placePegs: polygon mask is empty (polygon too small for resolution %d)", targetRes)
	}

	pixelToWorld := func(idx int) [2]float64 {
		i := idx % W
		j := idx / W
		// Padded grid: pixel (i, j) ↔ world (minX + (i-1)*step, minY + (j-1)*step).
		return [2]float64{minX + float64(i-1)*step, minY + float64(j-1)*step}
	}

	// Centroid of the polygon (use mask centroid for robustness with holes).
	var cx, cy float64
	for _, idx := range insideIdx {
		p := pixelToWorld(idx)
		cx += p[0]
		cy += p[1]
	}
	cx /= float64(len(insideIdx))
	cy /= float64(len(insideIdx))

	// First peg: inside pixel closest to centroid.
	first := insideIdx[0]
	bestDist2 := math.Inf(1)
	for _, idx := range insideIdx {
		p := pixelToWorld(idx)
		d2 := (p[0]-cx)*(p[0]-cx) + (p[1]-cy)*(p[1]-cy)
		if d2 < bestDist2 {
			bestDist2 = d2
			first = idx
		}
	}
	placed := []int{first}
	pegs := [][2]float64{pixelToWorld(first)}

	// Subsequent pegs: greedy farthest-point.
	for k := 1; k < count; k++ {
		bestIdx := -1
		bestMinD2 := -1.0
		for _, idx := range insideIdx {
			p := pixelToWorld(idx)
			minD2 := math.Inf(1)
			for _, pidx := range placed {
				q := pixelToWorld(pidx)
				d2 := (p[0]-q[0])*(p[0]-q[0]) + (p[1]-q[1])*(p[1]-q[1])
				if d2 < minD2 {
					minD2 = d2
				}
			}
			if minD2 > bestMinD2 {
				bestMinD2 = minD2
				bestIdx = idx
			}
		}
		// Reject if minimum spacing would be violated (only for
		// minSpacing > 0; placement is best-effort otherwise).
		if minSpacing > 0 && bestMinD2 < minSpacing*minSpacing {
			break
		}
		if bestIdx < 0 {
			break
		}
		placed = append(placed, bestIdx)
		pegs = append(pegs, pixelToWorld(bestIdx))
	}

	return pegs, nil
}

// placePegsInPolygons distributes count pegs across multiple polygon
// components, allocating count proportionally to area. Each component
// gets at least 1 if count >= number of components; otherwise the
// largest components get a peg first.
func placePegsInPolygons(polys []capPolygon, count int, minSpacing, boundaryClearance float64) ([][2]float64, error) {
	if len(polys) == 0 {
		return nil, fmt.Errorf("placePegsInPolygons: no polygons")
	}
	if count <= 0 {
		return nil, fmt.Errorf("placePegsInPolygons: count must be positive")
	}
	if len(polys) == 1 {
		return placePegs(polys[0], count, minSpacing, boundaryClearance)
	}

	// Score each polygon by net area (outer minus holes).
	type polyArea struct {
		idx  int
		area float64
	}
	areas := make([]polyArea, len(polys))
	totalArea := 0.0
	for i, p := range polys {
		a := math.Abs(signedArea2D(p.outer))
		for _, h := range p.holes {
			a -= math.Abs(signedArea2D(h))
		}
		if a < 0 {
			a = 0
		}
		areas[i] = polyArea{i, a}
		totalArea += a
	}
	if totalArea <= 0 {
		return nil, fmt.Errorf("placePegsInPolygons: all polygons have zero area")
	}

	// Allocate counts by largest-remainder method.
	allocs := make([]int, len(polys))
	type remainder struct {
		idx  int
		frac float64
	}
	rems := make([]remainder, 0, len(polys))
	used := 0
	for i, pa := range areas {
		exact := float64(count) * pa.area / totalArea
		whole := int(math.Floor(exact))
		allocs[i] = whole
		used += whole
		rems = append(rems, remainder{i, exact - float64(whole)})
	}
	sort.Slice(rems, func(i, j int) bool { return rems[i].frac > rems[j].frac })
	for k := 0; used < count && k < len(rems); k++ {
		allocs[rems[k].idx]++
		used++
	}

	var out [][2]float64
	for i, n := range allocs {
		if n == 0 {
			continue
		}
		pegs, err := placePegs(polys[i], n, minSpacing, boundaryClearance)
		if err != nil {
			// Skip this polygon; others still contribute.
			continue
		}
		out = append(out, pegs...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("placePegsInPolygons: no pegs placed")
	}
	return out, nil
}

// computeDistanceTransform returns, for each pixel in the W×H grid, its
// Euclidean distance to the nearest non-mask (false) pixel. mask[idx] ==
// true means "inside the polygon"; the returned distance is in world
// units (multiplied by `step`).
//
// Implementation: chamfer 3-4 distance transform — two raster passes
// (forward, then backward) using neighbor offsets {3, 4} for 4- and
// 8-connected neighbors respectively, scaled by step/3. This is exact
// enough for placement (a few percent off true L2) and runs in O(W·H).
func computeDistanceTransform(mask []bool, W, H int, step float64) []float64 {
	const inf = math.MaxFloat64
	dist := make([]float64, W*H)
	// Initialize: outside pixels = 0, inside pixels = +inf.
	for i, ok := range mask {
		if ok {
			dist[i] = inf
		} else {
			dist[i] = 0
		}
	}
	// Chamfer offsets in pixel-distance units; scale by step/3 to
	// recover world-distance.
	const a = 3.0 // 4-connected
	const b = 4.0 // 8-connected (diagonal)
	scale := step / 3.0

	// Forward pass: top-left to bottom-right, neighbors above/left.
	for j := 0; j < H; j++ {
		for i := 0; i < W; i++ {
			idx := j*W + i
			if dist[idx] == 0 {
				continue
			}
			best := dist[idx]
			if j > 0 {
				if v := dist[(j-1)*W+i] + a*scale; v < best {
					best = v
				}
				if i > 0 {
					if v := dist[(j-1)*W+(i-1)] + b*scale; v < best {
						best = v
					}
				}
				if i < W-1 {
					if v := dist[(j-1)*W+(i+1)] + b*scale; v < best {
						best = v
					}
				}
			}
			if i > 0 {
				if v := dist[j*W+(i-1)] + a*scale; v < best {
					best = v
				}
			}
			dist[idx] = best
		}
	}
	// Backward pass: bottom-right to top-left, neighbors below/right.
	for j := H - 1; j >= 0; j-- {
		for i := W - 1; i >= 0; i-- {
			idx := j*W + i
			if dist[idx] == 0 {
				continue
			}
			best := dist[idx]
			if j < H-1 {
				if v := dist[(j+1)*W+i] + a*scale; v < best {
					best = v
				}
				if i > 0 {
					if v := dist[(j+1)*W+(i-1)] + b*scale; v < best {
						best = v
					}
				}
				if i < W-1 {
					if v := dist[(j+1)*W+(i+1)] + b*scale; v < best {
						best = v
					}
				}
			}
			if i < W-1 {
				if v := dist[j*W+(i+1)] + a*scale; v < best {
					best = v
				}
			}
			dist[idx] = best
		}
	}
	// Clamp residual +inf (entirely-inside polygon, no nearby outside)
	// to a large finite value so callers don't see NaN.
	for i := range dist {
		if dist[i] == inf {
			dist[i] = math.Max(float64(W), float64(H)) * step
		}
	}
	return dist
}
