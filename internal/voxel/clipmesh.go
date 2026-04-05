package voxel

import (
	"context"
	"math"
	"sort"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// ClipPolygonByPlane clips a convex polygon against an axis-aligned plane.
// Returns the polygon on the negative side (<= value) and the polygon on the
// positive side (> value). Preserves winding order.
func ClipPolygonByPlane(
	poly [][3]float32,
	axis int,
	value float32,
) (neg, pos [][3]float32) {
	n := len(poly)
	if n < 3 {
		return poly, nil
	}

	for i := 0; i < n; i++ {
		a := poly[i]
		b := poly[(i+1)%n]
		aNeg := a[axis] <= value
		bNeg := b[axis] <= value

		if aNeg && bNeg {
			// Both on neg side: add B to neg.
			neg = append(neg, b)
		} else if !aNeg && !bNeg {
			// Both on pos side: add B to pos.
			pos = append(pos, b)
		} else {
			// Crossing: compute intersection, add to both sides.
			m := edgePlaneIntersect(a, b, axis, value)
			if aNeg {
				// A neg, B pos: finish neg with m, start pos with m then B.
				neg = append(neg, m)
				pos = append(pos, m, b)
			} else {
				// A pos, B neg: finish pos with m, start neg with m then B.
				pos = append(pos, m)
				neg = append(neg, m, b)
			}
		}
	}
	return neg, pos
}

// edgePlaneIntersect returns the point where the edge from a to b
// crosses the axis-aligned plane at axis=value.
// The interpolation is canonicalized so that the same edge always produces
// the same intersection point regardless of vertex order (a,b vs b,a).
func edgePlaneIntersect(a, b [3]float32, axis int, value float32) [3]float32 {
	denom := b[axis] - a[axis]
	if denom == 0 {
		// Edge parallel to plane — should not normally be called.
		// Return the canonicalized endpoint for consistency.
		if a[axis] > b[axis] {
			return b
		}
		return a
	}
	// Canonicalize: always interpolate from the vertex with the smaller
	// coordinate on the clipping axis. This ensures identical floating-point
	// results for shared edges between adjacent triangles.
	lo, hi := a, b
	if lo[axis] > hi[axis] {
		lo, hi = hi, lo
	}
	t := (value - lo[axis]) / (hi[axis] - lo[axis])
	var p [3]float32
	for i := 0; i < 3; i++ {
		if i == axis {
			p[i] = value // exact
		} else {
			p[i] = lo[i] + t*(hi[i]-lo[i])
		}
	}
	return p
}

// ClipMeshByPatches clips the original model's triangles against patch
// boundary planes and assigns each fragment the palette color of its
// enclosing patch. Clips against all global color boundary planes to
// ensure adjacent triangles sharing an edge get identical clip points.
func ClipMeshByPatches(
	ctx context.Context,
	model *loader.LoadedModel,
	patchMap map[CellKey]int,
	patchAssignment []int32,
	minV [3]float32,
	cellSize, layerH float32,
) ([][3]float32, [][3]uint32, []int32, error) {
	cellSteps := [3]float32{cellSize, cellSize, layerH}

	vd := NewVertexDedup()
	var faces [][3]uint32
	var assignments []int32

	// Precompute sorted global boundary planes per axis.
	// A plane at coordinate val on axis A is a boundary if ANY pair of adjacent
	// cells across that plane has different color assignments.
	var globalPlanes [3][]float32
	{
		var globalPlaneSets [3]map[float32]struct{}
		for ck, ci := range patchMap {
			myAssign := patchAssignment[ci]
			neighbors := [3]CellKey{
				{ck.Col + 1, ck.Row, ck.Layer},
				{ck.Col, ck.Row + 1, ck.Layer},
				{ck.Col, ck.Row, ck.Layer + 1},
			}
			for axis, nk := range neighbors {
				ni, ok := patchMap[nk]
				if !ok || patchAssignment[ni] == myAssign {
					continue
				}
				cellCoords := [3]int{ck.Col, ck.Row, ck.Layer}
				val := minV[axis] + (float32(cellCoords[axis])+0.5)*cellSteps[axis]
				if globalPlaneSets[axis] == nil {
					globalPlaneSets[axis] = make(map[float32]struct{})
				}
				globalPlaneSets[axis][val] = struct{}{}
			}
		}
		for axis := 0; axis < 3; axis++ {
			planes := make([]float32, 0, len(globalPlaneSets[axis]))
			for v := range globalPlaneSets[axis] {
				planes = append(planes, v)
			}
			sort.Slice(planes, func(a, b int) bool { return planes[a] < planes[b] })
			globalPlanes[axis] = planes
		}
	}

	for fi := range model.Faces {
		if fi%1000 == 0 && ctx.Err() != nil {
			return nil, nil, nil, ctx.Err()
		}
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

		// Filter global boundary planes to this triangle's AABB.
		var sortedPlanes [3][]float32
		for axis := 0; axis < 3; axis++ {
			gp := globalPlanes[axis]
			iLo := sort.Search(len(gp), func(i int) bool { return gp[i] > tMin[axis] })
			iHi := sort.Search(len(gp), func(i int) bool { return gp[i] >= tMax[axis] })
			if iLo < iHi {
				sortedPlanes[axis] = gp[iLo:iHi]
			}
		}

		// Start with this triangle as the only fragment (polygon).
		fragments := [][][3]float32{{v0, v1, v2}}

		// Clip against each axis's planes.
		for axis := 0; axis < 3; axis++ {
			pvals := sortedPlanes[axis]
			if len(pvals) == 0 {
				continue
			}

			var next [][][3]float32
			for _, poly := range fragments {
				lo, hi := poly[0][axis], poly[0][axis]
				for _, v := range poly[1:] {
					if v[axis] < lo {
						lo = v[axis]
					}
					if v[axis] > hi {
						hi = v[axis]
					}
				}

				iLo := sort.Search(len(pvals), func(i int) bool { return pvals[i] > lo })
				iHi := sort.Search(len(pvals), func(i int) bool { return pvals[i] >= hi })

				if iLo >= iHi {
					next = append(next, poly)
					continue
				}

				current := [][][3]float32{poly}
				for pi := iLo; pi < iHi; pi++ {
					var remaining [][][3]float32
					for _, p := range current {
						neg, pos := ClipPolygonByPlane(p, axis, pvals[pi])
						if len(neg) >= 3 {
							next = append(next, neg)
						}
						if len(pos) >= 3 {
							remaining = append(remaining, pos)
						}
					}
					current = remaining
				}
				for _, p := range current {
					if len(p) >= 3 {
						next = append(next, p)
					}
				}
			}
			fragments = next
		}

		// Assign each fragment a color and fan-triangulate.
		for _, poly := range fragments {
			if polyArea(poly) < 1e-8 {
				continue
			}

			centroid := polyCentroid(poly)

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

			// Fan-triangulate the convex polygon.
			vi0 := vd.GetVertex(poly[0])
			for i := 1; i < len(poly)-1; i++ {
				vi1 := vd.GetVertex(poly[i])
				vi2 := vd.GetVertex(poly[i+1])
				if vi0 == vi1 || vi1 == vi2 || vi0 == vi2 {
					continue // degenerate after dedup
				}
				faces = append(faces, [3]uint32{vi0, vi1, vi2})
				assignments = append(assignments, assignment)
			}
		}
	}

	return vd.Verts, faces, assignments, nil
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

// polyArea returns the area of a convex polygon using the triangle fan method.
func polyArea(poly [][3]float32) float32 {
	if len(poly) < 3 {
		return 0
	}
	var cx, cy, cz float64
	for i := 1; i < len(poly)-1; i++ {
		e1 := [3]float64{
			float64(poly[i][0] - poly[0][0]),
			float64(poly[i][1] - poly[0][1]),
			float64(poly[i][2] - poly[0][2]),
		}
		e2 := [3]float64{
			float64(poly[i+1][0] - poly[0][0]),
			float64(poly[i+1][1] - poly[0][1]),
			float64(poly[i+1][2] - poly[0][2]),
		}
		cx += e1[1]*e2[2] - e1[2]*e2[1]
		cy += e1[2]*e2[0] - e1[0]*e2[2]
		cz += e1[0]*e2[1] - e1[1]*e2[0]
	}
	return float32(math.Sqrt(cx*cx+cy*cy+cz*cz)) / 2
}

// polyCentroid returns the vertex-average position of a polygon.
// This is not the area-weighted centroid, but for convex polygons it is
// always interior, which is sufficient for cell assignment.
func polyCentroid(poly [][3]float32) [3]float32 {
	var sx, sy, sz float32
	for _, v := range poly {
		sx += v[0]
		sy += v[1]
		sz += v[2]
	}
	n := float32(len(poly))
	return [3]float32{sx / n, sy / n, sz / n}
}

