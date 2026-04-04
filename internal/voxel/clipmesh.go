package voxel

import (
	"math"
	"sort"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// isOnCellBoundary checks whether coordinate v lies on a cell boundary
// for the given axis: minV + (N + 0.5) * step for some integer N.
func isOnCellBoundary(v float32, minV float32, step float32) bool {
	rel := float64(v-minV)/float64(step) - 0.5
	return math.Abs(rel-math.Round(rel)) < 1e-4
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

	// Per-cell data for face contour simplification.
	type cellData struct {
		frags      [][3][3]float32 // clipped fragment triangles
		normalSum  [3]float64      // area-weighted normal (for winding)
		assignment int32
	}
	var cellDataMap map[CellKey]*cellData
	if simplify {
		cellDataMap = make(map[CellKey]*cellData)
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
		if simplify {
			// Clip at every cell boundary the triangle crosses.
			ranges := [3][2]int{
				{colMin, colMax},
				{rowMin, rowMax},
				{layerMin, layerMax},
			}
			for axis := 0; axis < 3; axis++ {
				step := cellSteps[axis]
				lo, hi := ranges[axis][0], ranges[axis][1]
				for n := lo; n < hi; n++ {
					val := minV[axis] + (float32(n)+0.5)*step
					if val > tMin[axis] && val < tMax[axis] {
						if localAxisPlanes[axis] == nil {
							localAxisPlanes[axis] = make(map[float32]struct{})
						}
						localAxisPlanes[axis][val] = struct{}{}
					}
				}
			}
		} else {
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

		// Store fragments and accumulate normals for face contour simplification.
		for _, cf := range cellFrags {
			e1 := [3]float32{cf.tri[1][0] - cf.tri[0][0], cf.tri[1][1] - cf.tri[0][1], cf.tri[1][2] - cf.tri[0][2]}
			e2 := [3]float32{cf.tri[2][0] - cf.tri[0][0], cf.tri[2][1] - cf.tri[0][1], cf.tri[2][2] - cf.tri[0][2]}
			nx := float64(e1[1]*e2[2] - e1[2]*e2[1])
			ny := float64(e1[2]*e2[0] - e1[0]*e2[2])
			nz := float64(e1[0]*e2[1] - e1[1]*e2[0])
			if math.Sqrt(nx*nx+ny*ny+nz*nz)/2 < 1e-12 {
				continue
			}

			cd, ok := cellDataMap[cf.ck]
			if !ok {
				cd = &cellData{assignment: cf.assignment}
				cellDataMap[cf.ck] = cd
			}
			cd.frags = append(cd.frags, cf.tri)
			cd.normalSum[0] += nx
			cd.normalSum[1] += ny
			cd.normalSum[2] += nz
		}
	}

	// Pass 2: extract cell-edge vertices and emit watertight polygons.
	if simplify {
		type vertKey struct {
			x, y, z int64
		}
		snapCoord := func(v float32) int64 {
			return int64(math.Round(float64(v) * 1e4))
		}

		for _, cd := range cellDataMap {
			// Normalize the accumulated normal.
			nx, ny, nz := cd.normalSum[0], cd.normalSum[1], cd.normalSum[2]
			nLen := math.Sqrt(nx*nx + ny*ny + nz*nz)
			if nLen < 1e-12 {
				continue
			}
			normal := [3]float32{float32(nx / nLen), float32(ny / nLen), float32(nz / nLen)}

			// Extract boundary vertices: vertices where 1+ coordinates
			// lie on cell boundary planes. Vertices on boundaries are set
			// exactly by edgePlaneIntersect and are shared by all cells
			// touching that boundary, making the mesh watertight.
			// - 1 boundary coord (face vertex): shared by 2 cells
			// - 2 boundary coords (edge vertex): shared by 4 cells
			// - 3 boundary coords (corner vertex): shared by 8 cells
			seen := make(map[vertKey]bool)
			var polyVerts [][3]float32

			for _, tri := range cd.frags {
				for _, v := range tri {
					onBoundary := 0
					for axis := 0; axis < 3; axis++ {
						if isOnCellBoundary(v[axis], minV[axis], cellSteps[axis]) {
							onBoundary++
						}
					}
					if onBoundary < 1 {
						continue
					}
					vk := vertKey{snapCoord(v[0]), snapCoord(v[1]), snapCoord(v[2])}
					if seen[vk] {
						continue
					}
					seen[vk] = true
					polyVerts = append(polyVerts, v)
				}
			}

			if len(polyVerts) < 3 {
				// Fallback: emit fragments directly for cells where we
				// can't build a polygon from boundary vertices (e.g.
				// surface barely clips a corner or lies within one cell).
				for _, tri := range cd.frags {
					vi0 := vd.GetVertex(tri[0])
					vi1 := vd.GetVertex(tri[1])
					vi2 := vd.GetVertex(tri[2])
					if vi0 == vi1 || vi1 == vi2 || vi0 == vi2 {
						continue
					}
					faces = append(faces, [3]uint32{vi0, vi1, vi2})
					assignments = append(assignments, cd.assignment)
				}
				continue
			}

			// Project boundary vertices onto the surface plane and
			// compute convex hull to minimize triangle count.
			var u, vAxis [3]float32
			if normal[0]*normal[0] < 0.81 {
				u = Cross3f(normal, [3]float32{1, 0, 0})
			} else {
				u = Cross3f(normal, [3]float32{0, 1, 0})
			}
			uLen := float32(math.Sqrt(float64(u[0]*u[0] + u[1]*u[1] + u[2]*u[2])))
			if uLen < 1e-10 {
				continue
			}
			u[0] /= uLen; u[1] /= uLen; u[2] /= uLen
			vAxis = Cross3f(normal, u)

			pts2D := make([][2]float64, len(polyVerts))
			for i, p := range polyVerts {
				pts2D[i] = [2]float64{
					float64(p[0]*u[0] + p[1]*u[1] + p[2]*u[2]),
					float64(p[0]*vAxis[0] + p[1]*vAxis[1] + p[2]*vAxis[2]),
				}
			}

			hull := convexHull2D(pts2D)
			if len(hull) < 3 {
				continue
			}

			// Ensure winding matches the accumulated normal.
			p0 := polyVerts[hull[0]]
			p1 := polyVerts[hull[1]]
			p2 := polyVerts[hull[2]]
			polyNorm := TriNormal(p0, p1, p2)
			reverse := Dot3(polyNorm, normal) < 0

			// Fan-triangulate the convex hull.
			for i := 1; i < len(hull)-1; i++ {
				var a, b, c [3]float32
				if reverse {
					a = polyVerts[hull[0]]
					b = polyVerts[hull[len(hull)-i]]
					c = polyVerts[hull[len(hull)-i-1]]
				} else {
					a = polyVerts[hull[0]]
					b = polyVerts[hull[i]]
					c = polyVerts[hull[i+1]]
				}
				vi0 := vd.GetVertex(a)
				vi1 := vd.GetVertex(b)
				vi2 := vd.GetVertex(c)
				if vi0 == vi1 || vi1 == vi2 || vi0 == vi2 {
					continue
				}
				faces = append(faces, [3]uint32{vi0, vi1, vi2})
				assignments = append(assignments, cd.assignment)
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

// convexHull2D returns the convex hull of a set of 2D points as indices
// into the input slice, in counter-clockwise order. Uses Andrew's monotone chain.
func convexHull2D(pts [][2]float64) []int {
	n := len(pts)
	if n < 3 {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		return idx
	}

	// Sort indices by x, then y.
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool {
		pa, pb := pts[idx[a]], pts[idx[b]]
		if pa[0] != pb[0] {
			return pa[0] < pb[0]
		}
		return pa[1] < pb[1]
	})

	cross := func(o, a, b int) float64 {
		return (pts[a][0]-pts[o][0])*(pts[b][1]-pts[o][1]) -
			(pts[a][1]-pts[o][1])*(pts[b][0]-pts[o][0])
	}

	// Build lower hull.
	hull := make([]int, 0, n+1)
	for _, i := range idx {
		for len(hull) >= 2 && cross(hull[len(hull)-2], hull[len(hull)-1], i) <= 0 {
			hull = hull[:len(hull)-1]
		}
		hull = append(hull, i)
	}
	// Build upper hull.
	lower := len(hull) + 1
	for j := n - 2; j >= 0; j-- {
		i := idx[j]
		for len(hull) >= lower && cross(hull[len(hull)-2], hull[len(hull)-1], i) <= 0 {
			hull = hull[:len(hull)-1]
		}
		hull = append(hull, i)
	}
	return hull[:len(hull)-1] // remove last (duplicate of first)
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

