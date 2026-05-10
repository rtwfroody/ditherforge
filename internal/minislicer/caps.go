package minislicer

import "math"

// PartitionTopCap tiles the top face of `layer` with cellSize ×
// cellSize cap tiles, producing one Section per tile whose center
// falls inside one of the layer's outer (CCW) loops.
//
// `layerH` is the slab thickness; the tile's Z is at the slab's
// upper face (layer.Z + layerH/2).
//
// loopIdxBase shifts the per-loop LoopIdx for cap sections so they
// don't collide with ribbon-section loop indices. Pass it as
// len(layer.Loops) when ribbons use 0..len-1.
func PartitionTopCap(layer Layer, layerH, cellSize float32, loopIdxBase int) []Section {
	return partitionCap(layer, layerH/2, cellSize, loopIdxBase, KindCapTop)
}

// PartitionBottomCap is the bottom-face counterpart to PartitionTopCap.
func PartitionBottomCap(layer Layer, layerH, cellSize float32, loopIdxBase int) []Section {
	return partitionCap(layer, -layerH/2, cellSize, loopIdxBase, KindCapBottom)
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
func partitionCap(layer Layer, zOffset, cellSize float32, loopIdxBase int, kind SectionKind) []Section {
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
				})
				idx++
			}
		}
	}
	return out
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
