package voxel

import (
	"math"
	"sort"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// cellAccum accumulates area-weighted surface data for one cell.
type cellAccum struct {
	normalSum [3]float64 // area-weighted normal (float64 for precision)
	centroid  [3]float64 // area-weighted centroid accumulator
	totalArea float64
	assignment int32
}

// ClipTriangleByPlane clips a single triangle against an axis-aligned plane.
// Returns triangles on the negative side (<=) and positive side (>).
// Preserves winding order.
func ClipTriangleByPlane(
	v0, v1, v2 [3]float32,
	axis int,
	value float32,
) (neg, pos [][3][3]float32) {
	verts := [3][3]float32{v0, v1, v2}
	sides := [3]int{} // -1 = neg, +1 = pos
	for i := range verts {
		if verts[i][axis] <= value {
			sides[i] = -1
		} else {
			sides[i] = 1
		}
	}

	// Count vertices on each side.
	negCount := 0
	for _, s := range sides {
		if s < 0 {
			negCount++
		}
	}

	switch negCount {
	case 3:
		// All on negative side.
		return [][3][3]float32{{v0, v1, v2}}, nil
	case 0:
		// All on positive side.
		return nil, [][3][3]float32{{v0, v1, v2}}
	}

	// Find the lone vertex (the one on the side with 1 vertex).
	// Rotate so that verts[0] is the lone vertex.
	loneIsNeg := negCount == 1
	loneIdx := -1
	for i, s := range sides {
		if (loneIsNeg && s < 0) || (!loneIsNeg && s > 0) {
			loneIdx = i
			break
		}
	}
	// Rotate vertices so lone vertex is at index 0, preserving winding.
	for i := 0; i < loneIdx; i++ {
		verts[0], verts[1], verts[2] = verts[1], verts[2], verts[0]
	}

	// verts[0] is alone on one side; verts[1] and verts[2] are on the other.
	// Find intersection points on edges 0→1 and 0→2.
	m1 := edgePlaneIntersect(verts[0], verts[1], axis, value)
	m2 := edgePlaneIntersect(verts[0], verts[2], axis, value)

	// Lone side: 1 triangle (verts[0], m1, m2)
	loneTri := [3][3]float32{verts[0], m1, m2}

	// Other side: 2 triangles (m1, verts[1], verts[2]) and (m1, verts[2], m2)
	otherTri1 := [3][3]float32{m1, verts[1], verts[2]}
	otherTri2 := [3][3]float32{m1, verts[2], m2}

	if loneIsNeg {
		return [][3][3]float32{loneTri}, [][3][3]float32{otherTri1, otherTri2}
	}
	return [][3][3]float32{otherTri1, otherTri2}, [][3][3]float32{loneTri}
}

// edgePlaneIntersect returns the point where the edge from a to b
// crosses the axis-aligned plane at axis=value.
func edgePlaneIntersect(a, b [3]float32, axis int, value float32) [3]float32 {
	denom := b[axis] - a[axis]
	if denom == 0 {
		return a // edge parallel to plane; return either endpoint
	}
	t := (value - a[axis]) / denom
	var p [3]float32
	for i := 0; i < 3; i++ {
		if i == axis {
			p[i] = value // exact
		} else {
			p[i] = a[i] + t*(b[i]-a[i])
		}
	}
	return p
}

// ClipMeshByPatches clips the original model's triangles against patch
// boundary planes and assigns each fragment the palette color of its
// enclosing patch. Only clips against boundaries that are local to each
// triangle's footprint, avoiding unnecessary splits from distant color
// transitions.
func ClipMeshByPatches(
	model *loader.LoadedModel,
	patchMap map[CellKey]int,
	patchAssignment []int32,
	minV [3]float32,
	cellSize, layerH float32,
	simplify bool,
) ([][3]float32, [][3]uint32, []int32) {
	type cellFrag struct {
		tri        [3][3]float32
		ck         CellKey
		assignment int32
	}

	cellSteps := [3]float32{cellSize, cellSize, layerH}

	vd := NewVertexDedup()
	var faces [][3]uint32
	var assignments []int32

	emitDirect := func(frags []cellFrag) {
		for _, cf := range frags {
			vi0 := vd.GetVertex(cf.tri[0])
			vi1 := vd.GetVertex(cf.tri[1])
			vi2 := vd.GetVertex(cf.tri[2])
			if vi0 == vi1 || vi1 == vi2 || vi0 == vi2 {
				continue // degenerate after dedup
			}
			faces = append(faces, [3]uint32{vi0, vi1, vi2})
			assignments = append(assignments, cf.assignment)
		}
	}

	// Per-cell accumulators for single-plane simplification (pass 1).
	var cellAccumMap map[CellKey]*cellAccum
	if simplify {
		cellAccumMap = make(map[CellKey]*cellAccum)
	}

	for fi := range model.Faces {
		// Skip translucent faces.
		if FaceAlpha(fi, model) < 128 {
			continue
		}

		f := model.Faces[fi]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]

		// Triangle AABB.
		tMin := [3]float32{
			Minf(v0[0], Minf(v1[0], v2[0])),
			Minf(v0[1], Minf(v1[1], v2[1])),
			Minf(v0[2], Minf(v1[2], v2[2])),
		}
		tMax := [3]float32{
			Maxf(v0[0], Maxf(v1[0], v2[0])),
			Maxf(v0[1], Maxf(v1[1], v2[1])),
			Maxf(v0[2], Maxf(v1[2], v2[2])),
		}

		// Find cells overlapping this triangle's AABB.
		colMin, colMax, rowMin, rowMax, layerMin, layerMax := AABBCellRange(tMin, tMax, minV, cellSize, layerH)

		// Collect local boundary planes from cells in this region.
		var localAxisPlanes [3]map[float32]struct{}
		for col := colMin; col <= colMax; col++ {
			for row := rowMin; row <= rowMax; row++ {
				for layer := layerMin; layer <= layerMax; layer++ {
					ck := CellKey{col, row, layer}
					ci, ok := patchMap[ck]
					if !ok {
						continue
					}
					myAssign := patchAssignment[ci]

					// Check 3 positive neighbors.
					neighbors := [3]CellKey{
						{col + 1, row, layer},
						{col, row + 1, layer},
						{col, row, layer + 1},
					}
					for axis, nk := range neighbors {
						ni, ok := patchMap[nk]
						if !ok || patchAssignment[ni] == myAssign {
							continue
						}
						cellCoords := [3]int{col, row, layer}
						val := minV[axis] + (float32(cellCoords[axis])+0.5)*cellSteps[axis]
						if val <= tMin[axis] || val >= tMax[axis] {
							continue // plane outside triangle AABB
						}
						if localAxisPlanes[axis] == nil {
							localAxisPlanes[axis] = make(map[float32]struct{})
						}
						localAxisPlanes[axis][val] = struct{}{}
					}
				}
			}
		}

		// Sort local planes per axis.
		var sortedPlanes [3][]float32
		for axis := 0; axis < 3; axis++ {
			if len(localAxisPlanes[axis]) == 0 {
				continue
			}
			sorted := make([]float32, 0, len(localAxisPlanes[axis]))
			for v := range localAxisPlanes[axis] {
				sorted = append(sorted, v)
			}
			sort.Slice(sorted, func(a, b int) bool { return sorted[a] < sorted[b] })
			sortedPlanes[axis] = sorted
		}

		// Start with this triangle as the only fragment.
		fragments := [][3][3]float32{{v0, v1, v2}}

		// Clip against each axis's local planes.
		for axis := 0; axis < 3; axis++ {
			pvals := sortedPlanes[axis]
			if len(pvals) == 0 {
				continue
			}

			var next [][3][3]float32
			for _, tri := range fragments {
				lo := Minf(tri[0][axis], Minf(tri[1][axis], tri[2][axis]))
				hi := Maxf(tri[0][axis], Maxf(tri[1][axis], tri[2][axis]))

				iLo := sort.Search(len(pvals), func(i int) bool { return pvals[i] > lo })
				iHi := sort.Search(len(pvals), func(i int) bool { return pvals[i] >= hi })

				if iLo >= iHi {
					next = append(next, tri)
					continue
				}

				current := [][3][3]float32{tri}
				for pi := iLo; pi < iHi; pi++ {
					var remaining [][3][3]float32
					for _, t := range current {
						neg, pos := ClipTriangleByPlane(t[0], t[1], t[2], axis, pvals[pi])
						next = append(next, neg...)
						remaining = append(remaining, pos...)
					}
					current = remaining
				}
				next = append(next, current...)
			}
			fragments = next
		}

		// Assign each fragment a color by mapping its centroid to a cell.
		var cellFrags []cellFrag
		for _, tri := range fragments {
			if triArea(tri) < 1e-8 {
				continue
			}

			centroid := [3]float32{
				(tri[0][0] + tri[1][0] + tri[2][0]) / 3,
				(tri[0][1] + tri[1][1] + tri[2][1]) / 3,
				(tri[0][2] + tri[1][2] + tri[2][2]) / 3,
			}

			ck := CentroidToCell(centroid, minV, cellSize, layerH)
			var assignment int32
			if pid, ok := patchMap[ck]; ok {
				assignment = patchAssignment[pid]
			} else {
				a, found := nearestPatchAssignment(ck, patchMap, patchAssignment)
				if !found {
					continue
				}
				assignment = a
			}
			cellFrags = append(cellFrags, cellFrag{tri, ck, assignment})
		}

		if !simplify {
			emitDirect(cellFrags)
			continue
		}

		// Accumulate per-cell surface data for single-plane simplification.
		for _, cf := range cellFrags {
			// Unnormalized cross product — magnitude proportional to 2x area.
			e1 := [3]float32{cf.tri[1][0] - cf.tri[0][0], cf.tri[1][1] - cf.tri[0][1], cf.tri[1][2] - cf.tri[0][2]}
			e2 := [3]float32{cf.tri[2][0] - cf.tri[0][0], cf.tri[2][1] - cf.tri[0][1], cf.tri[2][2] - cf.tri[0][2]}
			nx := float64(e1[1]*e2[2] - e1[2]*e2[1])
			ny := float64(e1[2]*e2[0] - e1[0]*e2[2])
			nz := float64(e1[0]*e2[1] - e1[1]*e2[0])
			area := math.Sqrt(nx*nx+ny*ny+nz*nz) / 2
			if area < 1e-12 {
				continue
			}
			cx := float64(cf.tri[0][0]+cf.tri[1][0]+cf.tri[2][0]) / 3
			cy := float64(cf.tri[0][1]+cf.tri[1][1]+cf.tri[2][1]) / 3
			cz := float64(cf.tri[0][2]+cf.tri[1][2]+cf.tri[2][2]) / 3

			ca, ok := cellAccumMap[cf.ck]
			if !ok {
				ca = &cellAccum{assignment: cf.assignment}
				cellAccumMap[cf.ck] = ca
			}
			ca.normalSum[0] += nx
			ca.normalSum[1] += ny
			ca.normalSum[2] += nz
			ca.centroid[0] += cx * area
			ca.centroid[1] += cy * area
			ca.centroid[2] += cz * area
			ca.totalArea += area
		}
	}

	// Pass 2: compute shared edge vertices and emit watertight polygons.
	if simplify {
		steps := [3]float32{cellSize, cellSize, layerH}

		// Resolve each cell's plane (normal + centroid).
		type cellPlane struct {
			normal   [3]float32
			centroid [3]float32
		}
		cellPlanes := make(map[CellKey]cellPlane, len(cellAccumMap))
		for ck, ca := range cellAccumMap {
			if ca.totalArea < 1e-12 {
				continue
			}
			nx, ny, nz := ca.normalSum[0], ca.normalSum[1], ca.normalSum[2]
			nLen := math.Sqrt(nx*nx + ny*ny + nz*nz)
			if nLen < 1e-12 {
				continue
			}
			cellPlanes[ck] = cellPlane{
				normal: [3]float32{float32(nx / nLen), float32(ny / nLen), float32(nz / nLen)},
				centroid: [3]float32{
					float32(ca.centroid[0] / ca.totalArea),
					float32(ca.centroid[1] / ca.totalArea),
					float32(ca.centroid[2] / ca.totalArea),
				},
			}
		}

		// planeEdgeParam computes the parameter t ∈ [0,1] where a cell's
		// plane intersects a box edge. The edge varies along axis a from
		// aMin to aMin+edgeLen, fixed at bPos and cPos on the other axes.
		// Returns (t, true) if the plane crosses the edge, or (0, false) if
		// the plane is parallel to the edge.
		planeEdgeParam := func(cp cellPlane, a, b, c int, aMin, edgeLen, bPos, cPos float32) (float64, bool) {
			denom := float64(cp.normal[a]) * float64(edgeLen)
			if math.Abs(denom) < 1e-10 {
				return 0, false
			}
			numer := float64(cp.normal[a])*float64(cp.centroid[a]-aMin) +
				float64(cp.normal[b])*float64(cp.centroid[b]-bPos) +
				float64(cp.normal[c])*float64(cp.centroid[c]-cPos)
			return numer / denom, true
		}

		// Pre-compute shared vertices at voxel grid edges.
		// An edge is parallel to axis a, at grid lines bLine and cLine on the
		// other two axes. Up to 4 cells share each edge.
		type edgeKey struct {
			axis  int
			aCell int
			bLine int
			cLine int
		}
		// edgeVert stores a shared vertex and which cells contributed to it.
		type edgeVert struct {
			pos        [3]float32
			cells      [4]bool // which of the 4 neighbor cells contributed
		}
		sharedVerts := make(map[edgeKey]edgeVert)

		edgeNeighborCK := func(ek edgeKey, dbb, dcc int) CellKey {
			switch ek.axis {
			case 0:
				return CellKey{ek.aCell, ek.bLine - 1 + dbb, ek.cLine - 1 + dcc}
			case 1:
				return CellKey{ek.cLine - 1 + dcc, ek.aCell, ek.bLine - 1 + dbb}
			default:
				return CellKey{ek.bLine - 1 + dbb, ek.cLine - 1 + dcc, ek.aCell}
			}
		}

		for ck := range cellPlanes {
			coords := [3]int{ck.Col, ck.Row, ck.Layer}
			for a := 0; a < 3; a++ {
				b := (a + 1) % 3
				c := (a + 2) % 3
				for db := 0; db <= 1; db++ {
					for dc := 0; dc <= 1; dc++ {
						ek := edgeKey{
							axis:  a,
							aCell: coords[a],
							bLine: coords[b] + db,
							cLine: coords[c] + dc,
						}
						if _, ok := sharedVerts[ek]; ok {
							continue
						}

						// Edge endpoints: varies in axis a, fixed in b and c.
						bPos := minV[b] + float32(ek.bLine)*steps[b] - steps[b]/2
						cPos := minV[c] + float32(ek.cLine)*steps[c] - steps[c]/2
						aMin := minV[a] + float32(ek.aCell)*steps[a] - steps[a]/2
						edgeLen := steps[a]

						// Find up to 4 cells sharing this edge and average their
						// plane intersection parameters.
						var tSum float64
						var tCount int
						var contrib [4]bool
						for dbb := 0; dbb <= 1; dbb++ {
							for dcc := 0; dcc <= 1; dcc++ {
								nck := edgeNeighborCK(ek, dbb, dcc)
								cp, ok := cellPlanes[nck]
								if !ok {
									continue
								}
								t, ok := planeEdgeParam(cp, a, b, c, aMin, edgeLen, bPos, cPos)
								if !ok || t < 0 || t > 1 {
									continue
								}
								tSum += t
								tCount++
								contrib[dbb*2+dcc] = true
							}
						}
						if tCount == 0 {
							continue
						}
						tAvg := tSum / float64(tCount)
						if tAvg < 0 {
							tAvg = 0
						}
						if tAvg > 1 {
							tAvg = 1
						}
						var pos [3]float32
						pos[a] = aMin + float32(tAvg)*edgeLen
						pos[b] = bPos
						pos[c] = cPos
						sharedVerts[ek] = edgeVert{pos: pos, cells: contrib}
					}
				}
			}
		}

		// Build polygon for each cell from shared edge vertices.
		for ck, cp := range cellPlanes {
			coords := [3]int{ck.Col, ck.Row, ck.Layer}
			var polyVerts [][3]float32

			for a := 0; a < 3; a++ {
				b := (a + 1) % 3
				c := (a + 2) % 3
				for db := 0; db <= 1; db++ {
					for dc := 0; dc <= 1; dc++ {
						ek := edgeKey{
							axis:  a,
							aCell: coords[a],
							bLine: coords[b] + db,
							cLine: coords[c] + dc,
						}
						ev, ok := sharedVerts[ek]
						if !ok {
							continue
						}
						// Include this vertex if this cell contributed to it,
						// OR if this cell's plane crosses the edge (with relaxed
						// tolerance to account for averaging).
						//
						// This cell is at coords[b], coords[c]. The neighbor at
						// slot (dbb,dcc) is at (bLine-1+dbb, cLine-1+dcc).
						// Since bLine = coords[b]+db, this cell is at dbb=1-db,
						// dcc=1-dc → slot (1-db)*2+(1-dc).
						contributed := ev.cells[(1-db)*2+(1-dc)]

						if !contributed {
							// Cell didn't contribute. Check if its plane
							// crosses the edge with relaxed tolerance.
							bPos := minV[b] + float32(ek.bLine)*steps[b] - steps[b]/2
							cPos := minV[c] + float32(ek.cLine)*steps[c] - steps[c]/2
							aMin := minV[a] + float32(ek.aCell)*steps[a] - steps[a]/2
							t, ok := planeEdgeParam(cp, a, b, c, aMin, steps[a], bPos, cPos)
							if !ok || t < -0.1 || t > 1.1 {
								continue
							}
						}
						polyVerts = append(polyVerts, ev.pos)
					}
				}
			}

			if len(polyVerts) < 3 {
				continue
			}

			// Sort vertices by angle around centroid on the cell's plane.
			sortPolygonVerts(polyVerts, cp.normal)

			// Ensure winding matches the accumulated normal.
			polyNorm := TriNormal(polyVerts[0], polyVerts[1], polyVerts[2])
			if Dot3(polyNorm, cp.normal) < 0 {
				for i, j := 0, len(polyVerts)-1; i < j; i, j = i+1, j-1 {
					polyVerts[i], polyVerts[j] = polyVerts[j], polyVerts[i]
				}
			}

			assign := cellAccumMap[ck].assignment
			// Fan-triangulate.
			for i := 1; i < len(polyVerts)-1; i++ {
				vi0 := vd.GetVertex(polyVerts[0])
				vi1 := vd.GetVertex(polyVerts[i])
				vi2 := vd.GetVertex(polyVerts[i+1])
				if vi0 == vi1 || vi1 == vi2 || vi0 == vi2 {
					continue
				}
				faces = append(faces, [3]uint32{vi0, vi1, vi2})
				assignments = append(assignments, assign)
			}
		}
	}

	return vd.Verts, faces, assignments
}

// CentroidToCell maps a 3D point to the nearest voxel grid cell.
func CentroidToCell(p [3]float32, minV [3]float32, cellSize, layerH float32) CellKey {
	col := int(math.Round(float64(p[0]-minV[0]) / float64(cellSize)))
	row := int(math.Round(float64(p[1]-minV[1]) / float64(cellSize)))
	layer := int(math.Round(float64(p[2]-minV[2]) / float64(layerH)))
	return CellKey{Col: col, Row: row, Layer: layer}
}

// nearestPatchAssignment searches neighboring cells for the nearest occupied
// cell and returns its palette assignment.
func nearestPatchAssignment(ck CellKey, patchMap map[CellKey]int, patchAssignment []int32) (int32, bool) {
	bestDist := int32(math.MaxInt32)
	bestAssign := int32(0)
	found := false
	for r := 1; r <= 3; r++ {
		if int32(r*r) > bestDist {
			break // can't improve
		}
		for dc := -r; dc <= r; dc++ {
			for dr := -r; dr <= r; dr++ {
				for dl := -r; dl <= r; dl++ {
					dist := int32(dc*dc + dr*dr + dl*dl)
					if dist == 0 || dist >= bestDist {
						continue
					}
					nk := CellKey{ck.Col + dc, ck.Row + dr, ck.Layer + dl}
					if pid, ok := patchMap[nk]; ok {
						bestDist = dist
						bestAssign = patchAssignment[pid]
						found = true
					}
				}
			}
		}
	}
	return bestAssign, found
}

// sortPolygonVerts sorts polygon vertices by angle around their centroid,
// projected onto the plane defined by the given normal.
func sortPolygonVerts(verts [][3]float32, normal [3]float32) {
	if len(verts) < 3 {
		return
	}
	// Build orthonormal basis on the plane.
	var u [3]float32
	if normal[0]*normal[0] < 0.81 {
		u = Cross3f(normal, [3]float32{1, 0, 0})
	} else {
		u = Cross3f(normal, [3]float32{0, 1, 0})
	}
	uLen := float32(math.Sqrt(float64(u[0]*u[0] + u[1]*u[1] + u[2]*u[2])))
	u[0] /= uLen
	u[1] /= uLen
	u[2] /= uLen
	v := Cross3f(normal, u)

	// Compute centroid.
	var cx, cy, cz float32
	for _, p := range verts {
		cx += p[0]
		cy += p[1]
		cz += p[2]
	}
	n := float32(len(verts))
	cx /= n
	cy /= n
	cz /= n

	// Sort by angle.
	sort.Slice(verts, func(i, j int) bool {
		di := [3]float32{verts[i][0] - cx, verts[i][1] - cy, verts[i][2] - cz}
		dj := [3]float32{verts[j][0] - cx, verts[j][1] - cy, verts[j][2] - cz}
		ai := math.Atan2(float64(Dot3(di, v)), float64(Dot3(di, u)))
		aj := math.Atan2(float64(Dot3(dj, v)), float64(Dot3(dj, u)))
		return ai < aj
	})
}

// triArea returns the area of a triangle defined by 3 vertices.
func triArea(tri [3][3]float32) float32 {
	e1 := [3]float32{tri[1][0] - tri[0][0], tri[1][1] - tri[0][1], tri[1][2] - tri[0][2]}
	e2 := [3]float32{tri[2][0] - tri[0][0], tri[2][1] - tri[0][1], tri[2][2] - tri[0][2]}
	cx := e1[1]*e2[2] - e1[2]*e2[1]
	cy := e1[2]*e2[0] - e1[0]*e2[2]
	cz := e1[0]*e2[1] - e1[1]*e2[0]
	return float32(math.Sqrt(float64(cx*cx+cy*cy+cz*cz))) / 2
}

