package voxel

import "math"

// BuildTwoGridNeighbors builds the full neighbor table for a two-grid voxel
// set. Within each grid, standard 26-connected adjacency applies. Across the
// seam (grid 0 at layer 0 ↔ grid 1 at layer 1), face-adjacent links
// are weighted by the fraction of the cell's Z-face that overlaps the
// neighbor's XY footprint.
func BuildTwoGridNeighbors(cells []ActiveCell, layer0Size, upperSize float32, minV [3]float32) [][]Neighbor {
	neighbors := BuildNeighbors(cells)

	cellMap := make(map[CellKey]int, len(cells))
	for i, c := range cells {
		cellMap[CellKey{Grid: c.Grid, Col: c.Col, Row: c.Row, Layer: c.Layer}] = i
	}

	for i, c := range cells {
		var crossSeam []Neighbor

		if c.Grid == 0 && c.Layer == 0 {
			crossSeam = overlapNeighbors(c, layer0Size, upperSize, minV, 1, 1, cellMap)
		} else if c.Grid == 1 && c.Layer == 1 {
			crossSeam = overlapNeighbors(c, upperSize, layer0Size, minV, 0, 0, cellMap)
		}

		if len(crossSeam) > 0 {
			neighbors[i] = append(neighbors[i], crossSeam...)
		}
	}

	return neighbors
}

// overlapNeighbors finds cells in the target grid whose XY footprints overlap
// the source cell's footprint. Returns neighbors weighted by shared area
// relative to the source cell's face area.
func overlapNeighbors(
	src ActiveCell,
	srcSize, tgtSize float32,
	minV [3]float32,
	tgtGrid uint8,
	tgtLayer int,
	cellMap map[CellKey]int,
) []Neighbor {
	// Source cell XY footprint.
	srcX0 := minV[0] + float32(src.Col)*srcSize - srcSize/2
	srcX1 := srcX0 + srcSize
	srcY0 := minV[1] + float32(src.Row)*srcSize - srcSize/2
	srcY1 := srcY0 + srcSize
	srcArea := srcSize * srcSize

	// Range of target grid columns/rows that could overlap.
	colLo := int(math.Floor(float64((srcX0 - minV[0]) / tgtSize)))
	colHi := int(math.Ceil(float64((srcX1 - minV[0]) / tgtSize)))
	rowLo := int(math.Floor(float64((srcY0 - minV[1]) / tgtSize)))
	rowHi := int(math.Ceil(float64((srcY1 - minV[1]) / tgtSize)))

	var nbrs []Neighbor
	for col := colLo; col <= colHi; col++ {
		tgtX0 := minV[0] + float32(col)*tgtSize - tgtSize/2
		tgtX1 := tgtX0 + tgtSize
		overlapX := min(srcX1, tgtX1) - max(srcX0, tgtX0)
		if overlapX <= 0 {
			continue
		}
		for row := rowLo; row <= rowHi; row++ {
			tgtY0 := minV[1] + float32(row)*tgtSize - tgtSize/2
			tgtY1 := tgtY0 + tgtSize
			overlapY := min(srcY1, tgtY1) - max(srcY0, tgtY0)
			if overlapY <= 0 {
				continue
			}
			k := CellKey{Grid: tgtGrid, Col: col, Row: row, Layer: tgtLayer}
			if j, ok := cellMap[k]; ok {
				w := (overlapX * overlapY) / srcArea
				nbrs = append(nbrs, Neighbor{Idx: j, Weight: w})
			}
		}
	}
	return nbrs
}
