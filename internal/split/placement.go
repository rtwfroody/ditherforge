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
// Algorithm: rasterize the polygon into a binary mask at fixed
// resolution, then place pegs greedily — the first at the inside
// pixel closest to the polygon centroid, and each subsequent peg at
// the inside pixel maximally far from all previously placed pegs.
//
// Returns peg centers in polygon coordinates. Returns an error if
// no inside pixels exist (polygon too small to rasterize at this
// resolution) or count <= 0.
//
// For polygons-with-multiple-components callers should call this
// once per polygon, dividing N proportionally to area.
func placePegs(poly capPolygon, count int, minSpacing float64) ([][2]float64, error) {
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

	// Resolution: 200 pixels along the longer axis.
	const targetRes = 200
	step := math.Max(dx, dy) / targetRes
	W := int(math.Ceil(dx/step)) + 1
	H := int(math.Ceil(dy/step)) + 1

	// Rasterize: pixel (i, j) at world (minX + i*step, minY + j*step).
	mask := make([]bool, W*H)
	insideIdx := make([]int, 0, W*H/2)
	for j := 0; j < H; j++ {
		y := minY + float64(j)*step
		for i := 0; i < W; i++ {
			x := minX + float64(i)*step
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
			insideIdx = append(insideIdx, j*W+i)
		}
	}
	if len(insideIdx) == 0 {
		return nil, fmt.Errorf("placePegs: polygon mask is empty (polygon too small for resolution %d)", targetRes)
	}

	pixelToWorld := func(idx int) [2]float64 {
		i := idx % W
		j := idx / W
		return [2]float64{minX + float64(i)*step, minY + float64(j)*step}
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
func placePegsInPolygons(polys []capPolygon, count int, minSpacing float64) ([][2]float64, error) {
	if len(polys) == 0 {
		return nil, fmt.Errorf("placePegsInPolygons: no polygons")
	}
	if count <= 0 {
		return nil, fmt.Errorf("placePegsInPolygons: count must be positive")
	}
	if len(polys) == 1 {
		return placePegs(polys[0], count, minSpacing)
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
		pegs, err := placePegs(polys[i], n, minSpacing)
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
