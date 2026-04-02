package voxel

import (
	"math"
	"sort"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// BoundaryPlane represents an axis-aligned clipping plane between two patches.
type BoundaryPlane struct {
	Axis  int     // 0=X, 1=Y, 2=Z
	Value float32 // coordinate of the plane
}

// FindBoundaryPlanes returns the set of unique axis-aligned planes where
// adjacent cells belong to different patches (different palette assignments).
func FindBoundaryPlanes(
	cells []ActiveCell,
	assignments []int32,
	cellAssignMap map[CellKey]int,
	minV [3]float32,
	cellSize, layerH float32,
) []BoundaryPlane {
	seen := make(map[BoundaryPlane]struct{})
	for i, c := range cells {
		k := CellKey{c.Col, c.Row, c.Layer}
		myColor := assignments[i]

		// Check 3 positive neighbors (avoid duplicate checks).
		neighbors := [3]CellKey{
			{k.Col + 1, k.Row, k.Layer},
			{k.Col, k.Row + 1, k.Layer},
			{k.Col, k.Row, k.Layer + 1},
		}
		for axis, nk := range neighbors {
			ni, ok := cellAssignMap[nk]
			if !ok || assignments[ni] == myColor {
				continue
			}
			var val float32
			switch axis {
			case 0: // X boundary between col and col+1
				val = minV[0] + (float32(k.Col)+0.5)*cellSize
			case 1: // Y boundary between row and row+1
				val = minV[1] + (float32(k.Row)+0.5)*cellSize
			case 2: // Z boundary between layer and layer+1
				val = minV[2] + (float32(k.Layer)+0.5)*layerH
			}
			bp := BoundaryPlane{Axis: axis, Value: val}
			seen[bp] = struct{}{}
		}
	}

	planes := make([]BoundaryPlane, 0, len(seen))
	for bp := range seen {
		planes = append(planes, bp)
	}
	return planes
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
// enclosing patch.
func ClipMeshByPatches(
	model *loader.LoadedModel,
	planes []BoundaryPlane,
	patchMap map[CellKey]int,
	patchAssignment []int32,
	minV [3]float32,
	cellSize, layerH float32,
) ([][3]float32, [][3]uint32, []int32) {
	// Sort planes by axis, then by value, for efficient per-triangle filtering.
	axisPlanes := [3][]float32{} // sorted plane values per axis
	for _, bp := range planes {
		axisPlanes[bp.Axis] = append(axisPlanes[bp.Axis], bp.Value)
	}
	for i := range axisPlanes {
		sort.Slice(axisPlanes[i], func(a, b int) bool {
			return axisPlanes[i][a] < axisPlanes[i][b]
		})
	}

	vd := NewVertexDedup()
	var faces [][3]uint32
	var assignments []int32

	for fi := range model.Faces {
		f := model.Faces[fi]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]

		// Start with this triangle as the only fragment.
		fragments := [][3][3]float32{{v0, v1, v2}}

		// Clip against each axis's planes.
		for axis := 0; axis < 3; axis++ {
			pvals := axisPlanes[axis]
			if len(pvals) == 0 {
				continue
			}

			var next [][3][3]float32
			for _, tri := range fragments {
				// Find AABB of this fragment on this axis.
				lo := Minf(tri[0][axis], Minf(tri[1][axis], tri[2][axis]))
				hi := Maxf(tri[0][axis], Maxf(tri[1][axis], tri[2][axis]))

				// Find planes that intersect this range.
				iLo := sort.Search(len(pvals), func(i int) bool { return pvals[i] > lo })
				iHi := sort.Search(len(pvals), func(i int) bool { return pvals[i] >= hi })

				if iLo >= iHi {
					// No planes intersect this fragment.
					next = append(next, tri)
					continue
				}

				// Clip sequentially against each intersecting plane.
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
				// Remaining fragments are on the positive side of all planes.
				next = append(next, current...)
			}
			fragments = next
		}

		// Assign each fragment to a patch by centroid lookup.
		for _, tri := range fragments {
			// Filter degenerate triangles.
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
				// Fallback: search nearby cells. If none found,
				// the fragment is in a transparent region — drop it.
				a, found := nearestPatchAssignment(ck, patchMap, patchAssignment)
				if !found {
					continue
				}
				assignment = a
			}

			vi0 := vd.GetVertex(tri[0])
			vi1 := vd.GetVertex(tri[1])
			vi2 := vd.GetVertex(tri[2])
			if vi0 == vi1 || vi1 == vi2 || vi0 == vi2 {
				continue // degenerate after dedup
			}
			faces = append(faces, [3]uint32{vi0, vi1, vi2})
			assignments = append(assignments, assignment)
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

// triArea returns the area of a triangle defined by 3 vertices.
func triArea(tri [3][3]float32) float32 {
	e1 := [3]float32{tri[1][0] - tri[0][0], tri[1][1] - tri[0][1], tri[1][2] - tri[0][2]}
	e2 := [3]float32{tri[2][0] - tri[0][0], tri[2][1] - tri[0][1], tri[2][2] - tri[0][2]}
	cx := e1[1]*e2[2] - e1[2]*e2[1]
	cy := e1[2]*e2[0] - e1[0]*e2[2]
	cz := e1[0]*e2[1] - e1[1]*e2[0]
	return float32(math.Sqrt(float64(cx*cx+cy*cy+cz*cz))) / 2
}

