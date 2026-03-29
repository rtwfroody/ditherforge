package voxel

// GenerateBoundaryFaces emits axis-aligned quad faces (2 triangles each) at
// the boundary of cellSet. For each cell in the set, each face-adjacent
// neighbor that is NOT in the set produces one outward-facing quad.
// If neighborSet is non-nil, only faces adjacent to cells IN neighborSet are
// emitted (useful for generating only the inner boundary of a shell).
// Returns faces and a parallel slice of CellKeys identifying which cell owns
// each face (for color assignment).
// Vertices are added to the supplied VertexDedup so callers can share a
// vertex pool with other geometry (e.g. marching-cubes output).
func GenerateBoundaryFaces(
	cellSet map[CellKey]struct{},
	neighborSet map[CellKey]struct{},
	minV [3]float32, cellSize, layerH float32,
	vd *VertexDedup,
) ([][3]uint32, []CellKey) {
	halfC := cellSize / 2
	halfH := layerH / 2

	// 6 face directions: delta col/row/layer, then 4 corner offsets for
	// the quad, wound so the outward normal points away from the cell.
	type faceDir struct {
		dc, dr, dl int
		corners    [4][3]float32 // offsets from cell center
	}
	dirs := [6]faceDir{
		// +X  (col+1)
		{1, 0, 0, [4][3]float32{
			{+halfC, -halfC, -halfH},
			{+halfC, +halfC, -halfH},
			{+halfC, +halfC, +halfH},
			{+halfC, -halfC, +halfH},
		}},
		// -X  (col-1)
		{-1, 0, 0, [4][3]float32{
			{-halfC, +halfC, -halfH},
			{-halfC, -halfC, -halfH},
			{-halfC, -halfC, +halfH},
			{-halfC, +halfC, +halfH},
		}},
		// +Y  (row+1)
		{0, 1, 0, [4][3]float32{
			{+halfC, +halfC, -halfH},
			{-halfC, +halfC, -halfH},
			{-halfC, +halfC, +halfH},
			{+halfC, +halfC, +halfH},
		}},
		// -Y  (row-1)
		{0, -1, 0, [4][3]float32{
			{-halfC, -halfC, -halfH},
			{+halfC, -halfC, -halfH},
			{+halfC, -halfC, +halfH},
			{-halfC, -halfC, +halfH},
		}},
		// +Z  (layer+1)
		{0, 0, 1, [4][3]float32{
			{-halfC, -halfC, +halfH},
			{+halfC, -halfC, +halfH},
			{+halfC, +halfC, +halfH},
			{-halfC, +halfC, +halfH},
		}},
		// -Z  (layer-1)
		{0, 0, -1, [4][3]float32{
			{-halfC, +halfC, -halfH},
			{+halfC, +halfC, -halfH},
			{+halfC, -halfC, -halfH},
			{-halfC, -halfC, -halfH},
		}},
	}

	var faces [][3]uint32
	var owners []CellKey
	for k := range cellSet {
		cx := minV[0] + float32(k.Col)*cellSize
		cy := minV[1] + float32(k.Row)*cellSize
		cz := minV[2] + float32(k.Layer)*layerH

		for _, d := range dirs {
			nk := CellKey{Col: k.Col + d.dc, Row: k.Row + d.dr, Layer: k.Layer + d.dl}
			if _, ok := cellSet[nk]; ok {
				continue // neighbor is in set, no boundary face
			}
			if neighborSet != nil {
				if _, ok := neighborSet[nk]; !ok {
					continue // neighbor not in required set
				}
			}
			// Emit two triangles for this quad.
			var vi [4]uint32
			for i, off := range d.corners {
				vi[i] = vd.GetVertex([3]float32{
					cx + off[0],
					cy + off[1],
					cz + off[2],
				})
			}
			faces = append(faces, [3]uint32{vi[0], vi[1], vi[2]})
			faces = append(faces, [3]uint32{vi[0], vi[2], vi[3]})
			owners = append(owners, k, k)
		}
	}
	return faces, owners
}
