package voxel

// FloodFillPatches groups cells by dithered palette assignment using
// 6-connected BFS. Two adjacent cells join the same patch if they have
// the same palette assignment index.
// Returns a map from CellKey to patch ID (0-based), and the total patch count.
func FloodFillPatches(cells []ActiveCell, assignments []int32) (patchMap map[CellKey]int, numPatches int) {
	cellIdx := make(map[CellKey]int, len(cells))
	for i, c := range cells {
		cellIdx[CellKey{c.Col, c.Row, c.Layer}] = i
	}

	patchMap = make(map[CellKey]int, len(cells))
	visited := make(map[CellKey]bool, len(cells))
	patchID := 0

	for i, c := range cells {
		k := CellKey{c.Col, c.Row, c.Layer}
		if visited[k] {
			continue
		}

		// BFS from this cell.
		color := assignments[i]
		queue := []CellKey{k}
		visited[k] = true
		patchMap[k] = patchID

		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, nk := range [6]CellKey{
				{cur.Col + 1, cur.Row, cur.Layer},
				{cur.Col - 1, cur.Row, cur.Layer},
				{cur.Col, cur.Row + 1, cur.Layer},
				{cur.Col, cur.Row - 1, cur.Layer},
				{cur.Col, cur.Row, cur.Layer + 1},
				{cur.Col, cur.Row, cur.Layer - 1},
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
	return patchMap, patchID
}
