package voxel

import "context"

// FloodFillPatches groups cells by dithered palette assignment using
// 6-connected BFS. Two adjacent cells join the same patch if they have
// the same palette assignment index.
// Returns a map from CellKey to patch ID (0-based), and the total patch count.
func FloodFillPatches(ctx context.Context, cells []ActiveCell, assignments []int32) (patchMap map[CellKey]int, numPatches int, err error) {
	cellIdx := make(map[CellKey]int, len(cells))
	for i, c := range cells {
		cellIdx[CellKey{Grid: c.Grid, Col: c.Col, Row: c.Row, Layer: c.Layer}] = i
	}

	patchMap = make(map[CellKey]int, len(cells))
	visited := make(map[CellKey]bool, len(cells))
	patchID := 0

	cellCount := 0
	for i, c := range cells {
		k := CellKey{Grid: c.Grid, Col: c.Col, Row: c.Row, Layer: c.Layer}
		if visited[k] {
			continue
		}

		if cellCount%1000 == 0 && ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		cellCount++

		// BFS from this cell.
		color := assignments[i]
		queue := []CellKey{k}
		visited[k] = true
		patchMap[k] = patchID

		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, nk := range [6]CellKey{
				{Grid: cur.Grid, Col: cur.Col + 1, Row: cur.Row, Layer: cur.Layer},
				{Grid: cur.Grid, Col: cur.Col - 1, Row: cur.Row, Layer: cur.Layer},
				{Grid: cur.Grid, Col: cur.Col, Row: cur.Row + 1, Layer: cur.Layer},
				{Grid: cur.Grid, Col: cur.Col, Row: cur.Row - 1, Layer: cur.Layer},
				{Grid: cur.Grid, Col: cur.Col, Row: cur.Row, Layer: cur.Layer + 1},
				{Grid: cur.Grid, Col: cur.Col, Row: cur.Row, Layer: cur.Layer - 1},
			} {
				if visited[nk] {
					continue
				}
				ni, ok := cellIdx[nk]
				if !ok {
					continue
				}
				if assignments[ni] != color {
					continue
				}
				visited[nk] = true
				patchMap[nk] = patchID
				queue = append(queue, nk)
			}
		}
		patchID++
	}
	return patchMap, patchID, nil
}
