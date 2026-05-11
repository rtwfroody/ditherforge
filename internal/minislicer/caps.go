package minislicer

import "math"

// capTileEpsilon is how far outside the slab face cap tiles sit
// (above for KindCapTop, below for KindCapBottom). The earcut
// fallback cap is emitted at the exact slab face Z; the tiny
// outward offset on tiles makes them win the depth test from
// outside the model so the dithered tile color shows through
// instead of the earcut's flat fallback. 1µm is well above the
// float32 z-buffer's discrimination threshold for mm-scale models
// and far below any visible feature.
const capTileEpsilon = 1e-3

// PartitionTopCap tiles the top face of `layer` wherever it's
// exposed — solid in `layer` and air in `neighborAbove` (the layer
// directly above). Pass neighborAbove == nil for the topmost layer
// (no layer above → all-air → every tile inside layer is exposed).
//
// `layerH` is the slab thickness; the tile's Z is at the slab's
// upper face + capTileEpsilon so tiles depth-beat the earcut cap.
//
// loopIdxBase shifts the per-loop LoopIdx for cap sections so they
// don't collide with ribbon-section loop indices.
func PartitionTopCap(layer Layer, neighborAbove *Layer, layerH, cellSize float32, loopIdxBase int) []Section {
	return partitionCap(layer, neighborAbove, layerH/2+capTileEpsilon, cellSize, loopIdxBase, KindCapTop)
}

// PartitionBottomCap is the bottom-face counterpart. Pass
// neighborBelow == nil for the bottommost layer.
func PartitionBottomCap(layer Layer, neighborBelow *Layer, layerH, cellSize float32, loopIdxBase int) []Section {
	return partitionCap(layer, neighborBelow, -layerH/2-capTileEpsilon, cellSize, loopIdxBase, KindCapBottom)
}

// insideSolid uses even-odd nesting: a point is inside the solid
// region of `layer` iff an odd number of layer.Loops contain it.
// Returns false when layer == nil (treating "no layer" as all-air,
// so all tiles in the calling layer are exposed at that face).
func insideSolid(layer *Layer, x, y float32) bool {
	if layer == nil {
		return false
	}
	count := 0
	for i := range layer.Loops {
		if len(layer.Loops[i].Points) < 3 {
			continue
		}
		if pointInPolygon(layer.Loops[i].Points, x, y) {
			count++
		}
	}
	return (count & 1) == 1
}

// partitionCap is the shared body of PartitionTopCap and
// PartitionBottomCap. zOffset is added to layer.Z to get the cap
// surface's 3D Z (positive for top, negative for bottom).
//
// Iterates outer loops only (loop.IsHole == false); for each outer
// loop, tiles whose centers fall outside the loop OR inside any
// hole loop in the same layer are dropped. Tiles partially clipped
// by the loop boundary are emitted at full cellSize anyway (the
// resulting prism geometry overhangs the contour by a fraction of a
// cell, which is acceptable for the prototype).
func partitionCap(layer Layer, neighbor *Layer, zOffset, cellSize float32, loopIdxBase int, kind SectionKind) []Section {
	if cellSize <= 0 {
		return nil
	}
	z := layer.Z + zOffset
	var out []Section
	// Cache the layer's hole loops for the per-tile exclusion test.
	var holes []*Loop
	for k := range layer.Loops {
		if layer.Loops[k].IsHole {
			holes = append(holes, &layer.Loops[k])
		}
	}
	for li, loop := range layer.Loops {
		if loop.IsHole {
			continue
		}
		// Tight loop bbox.
		xMin, yMin := float32(math.Inf(1)), float32(math.Inf(1))
		xMax, yMax := float32(math.Inf(-1)), float32(math.Inf(-1))
		for _, p := range loop.Points {
			if p[0] < xMin {
				xMin = p[0]
			}
			if p[0] > xMax {
				xMax = p[0]
			}
			if p[1] < yMin {
				yMin = p[1]
			}
			if p[1] > yMax {
				yMax = p[1]
			}
		}
		cols := int(math.Ceil(float64((xMax - xMin) / cellSize)))
		rows := int(math.Ceil(float64((yMax - yMin) / cellSize)))
		if cols < 1 {
			cols = 1
		}
		if rows < 1 {
			rows = 1
		}
		idx := 0
		capLoopIdx := loopIdxBase + li
		for j := 0; j < rows; j++ {
			for i := 0; i < cols; i++ {
				x0 := xMin + float32(i)*cellSize
				y0 := yMin + float32(j)*cellSize
				x1 := x0 + cellSize
				y1 := y0 + cellSize
				if x1 > xMax {
					x1 = xMax
				}
				if y1 > yMax {
					y1 = yMax
				}
				cx := (x0 + x1) * 0.5
				cy := (y0 + y1) * 0.5
				if !pointInPolygon(loop.Points, cx, cy) {
					continue
				}
				inHole := false
				for _, h := range holes {
					if pointInPolygon(h.Points, cx, cy) {
						inHole = true
						break
					}
				}
				if inHole {
					continue
				}
				// Exposure test: tile is exposed at this Z face
				// only if the adjacent layer is air at (cx, cy).
				if insideSolid(neighbor, cx, cy) {
					continue
				}
				out = append(out, Section{
					LayerIdx:    layer.LayerIdx,
					LoopIdx:     capLoopIdx,
					Index:       idx,
					Kind:        kind,
					Mid:         Point2{cx, cy},
					Z:           z,
					CapBoundsXY: [4]float32{x0, y0, x1, y1},
					TileCol:     i,
					TileRow:     j,
					// Cap tile color comes from the nearest loop edge's
					// source triangle. Without this, SrcTriIdx=-1 would
					// route to SampleNearestColor's global spatial-index
					// search, which can pull in triangles from a nearby
					// but unrelated mesh region — e.g. a salmon cut-
					// surface triangle being picked for a blue dome
					// cap, producing horizontal salmon "streaks" in the
					// rendered output. Anchoring the cap to the same
					// mesh component its loop traces avoids that leak.
					SrcTriIdx: nearestEdgeTri(&loop, cx, cy),
				})
				idx++
			}
		}
	}
	return out
}

// nearestEdgeTri returns the source triangle index of the loop edge
// whose midpoint is geometrically closest (squared XY distance) to
// (x, y). Returns -1 if the loop has no EdgeTris or has fewer than
// 2 points.
//
// Used by partitionCap to anchor each cap tile to a triangle from
// the same connected mesh region as its bounding loop. The cap
// tile sits inside the polygon rather than on any one triangle's
// surface, so SampleByTriangle then samples that triangle's
// closest-point-on-triangle for (cx, cy, tileZ) — which lives on
// the same mesh component the loop traces. This avoids the global
// nearest-tri search picking up unrelated nearby geometry (e.g. a
// cut surface inside a fish dome, or a cutting board next to a
// fish footprint).
func nearestEdgeTri(loop *Loop, x, y float32) int32 {
	n := len(loop.Points)
	if n < 2 || len(loop.EdgeTris) == 0 {
		return -1
	}
	best := int32(-1)
	bestSq := float32(math.MaxFloat32)
	for i := 0; i < n; i++ {
		if i >= len(loop.EdgeTris) {
			break
		}
		j := (i + 1) % n
		mx := (loop.Points[i][0] + loop.Points[j][0]) * 0.5
		my := (loop.Points[i][1] + loop.Points[j][1]) * 0.5
		dx := mx - x
		dy := my - y
		d := dx*dx + dy*dy
		if d < bestSq {
			bestSq = d
			best = loop.EdgeTris[i]
		}
	}
	return best
}

// pointInPolygon does even-odd ray casting along +X. The polygon is
// closed (last point connects back to first); does not include
// special handling for points exactly on edges (rare for tile
// centers offset by cellSize/2).
func pointInPolygon(points []Point2, x, y float32) bool {
	inside := false
	n := len(points)
	if n < 3 {
		return false
	}
	j := n - 1
	for i := 0; i < n; i++ {
		yi := points[i][1]
		yj := points[j][1]
		if (yi > y) != (yj > y) {
			t := (y - yi) / (yj - yi)
			xCross := points[i][0] + t*(points[j][0]-points[i][0])
			if xCross > x {
				inside = !inside
			}
		}
		j = i
	}
	return inside
}
