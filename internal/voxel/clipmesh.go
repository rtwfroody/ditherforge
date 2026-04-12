package voxel

import (
	"context"
	"math"
	"runtime"
	"sort"
	"sync"

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
//
// TODO: refactor to share the common worker/clip/dedup logic with
// ClipMeshByPatchesTwoGrid — the two functions are ~80% identical.
func ClipMeshByPatches(
	ctx context.Context,
	model *loader.LoadedModel,
	patchMap map[CellKey]int,
	patchAssignment []int32,
	minV [3]float32,
	cellSize, layerH float32,
) ([][3]float32, [][3]uint32, []int32, error) {
	cellSteps := [3]float32{cellSize, cellSize, layerH}

	// Precompute sorted global boundary planes per axis.
	// A plane at coordinate val on axis A is a boundary if ANY pair of adjacent
	// cells across that plane has different color assignments.
	var globalPlanes [3][]float32
	{
		var globalPlaneSets [3]map[float32]struct{}
		for ck, ci := range patchMap {
			myAssign := patchAssignment[ci]
			neighbors := [3]CellKey{
				{Grid: ck.Grid, Col: ck.Col + 1, Row: ck.Row, Layer: ck.Layer},
				{Grid: ck.Grid, Col: ck.Col, Row: ck.Row + 1, Layer: ck.Layer},
				{Grid: ck.Grid, Col: ck.Col, Row: ck.Row, Layer: ck.Layer + 1},
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

	// Process faces in parallel, each worker with its own VertexDedup.
	type workerResult struct {
		vd          *VertexDedup
		faces       [][3]uint32
		assignments []int32
	}

	nFaces := len(model.Faces)
	numWorkers := runtime.NumCPU()
	if numWorkers > nFaces {
		numWorkers = nFaces
	}
	if numWorkers < 1 {
		numWorkers = 1
	}
	results := make([]workerResult, numWorkers)
	chunkSize := (nFaces + numWorkers - 1) / numWorkers

	var wg sync.WaitGroup
	for w := range numWorkers {
		lo := w * chunkSize
		hi := lo + chunkSize
		if hi > nFaces {
			hi = nFaces
		}
		if lo >= hi {
			continue
		}
		wg.Add(1)
		go func(workerIdx, faceStart, faceEnd int) {
			defer wg.Done()
			wvd := NewVertexDedup()
			estFaces := (faceEnd - faceStart) * 4
			wFaces := make([][3]uint32, 0, estFaces)
			wAssign := make([]int32, 0, estFaces)

			for fi := faceStart; fi < faceEnd; fi++ {
				if (fi-faceStart)%1000 == 0 && ctx.Err() != nil {
					return
				}
				if FaceAlpha(fi, model) < 128 {
					continue
				}

				f := model.Faces[fi]
				v0 := model.Vertices[f[0]]
				v1 := model.Vertices[f[1]]
				v2 := model.Vertices[f[2]]

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

				var sortedPlanes [3][]float32
				for axis := 0; axis < 3; axis++ {
					gp := globalPlanes[axis]
					iLo := sort.Search(len(gp), func(i int) bool { return gp[i] > tMin[axis] })
					iHi := sort.Search(len(gp), func(i int) bool { return gp[i] >= tMax[axis] })
					if iLo < iHi {
						sortedPlanes[axis] = gp[iLo:iHi]
					}
				}

				fragments := [][][3]float32{{v0, v1, v2}}

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

					vi0 := wvd.GetVertex(poly[0])
					for i := 1; i < len(poly)-1; i++ {
						vi1 := wvd.GetVertex(poly[i])
						vi2 := wvd.GetVertex(poly[i+1])
						if vi0 == vi1 || vi1 == vi2 || vi0 == vi2 {
							continue
						}
						wFaces = append(wFaces, [3]uint32{vi0, vi1, vi2})
						wAssign = append(wAssign, assignment)
					}
				}
			}

			results[workerIdx] = workerResult{vd: wvd, faces: wFaces, assignments: wAssign}
		}(w, lo, hi)
	}
	wg.Wait()

	if ctx.Err() != nil {
		return nil, nil, nil, ctx.Err()
	}

	// Merge worker results: remap per-worker vertex indices into a global dedup.
	globalVD := NewVertexDedup()
	var allFaces [][3]uint32
	var allAssignments []int32
	for _, r := range results {
		if r.vd == nil {
			continue
		}
		// Build remap table: worker vertex index -> global vertex index.
		remap := make([]uint32, len(r.vd.Verts))
		for i, v := range r.vd.Verts {
			remap[i] = globalVD.GetVertex(v)
		}
		for _, f := range r.faces {
			allFaces = append(allFaces, [3]uint32{remap[f[0]], remap[f[1]], remap[f[2]]})
		}
		allAssignments = append(allAssignments, r.assignments...)
	}

	return globalVD.Verts, allFaces, allAssignments, nil
}

// CentroidToCell maps a 3D point to the nearest voxel grid cell.
func CentroidToCell(p [3]float32, minV [3]float32, cellSize, layerH float32) CellKey {
	col := int(math.Round(float64(p[0]-minV[0]) / float64(cellSize)))
	row := int(math.Round(float64(p[1]-minV[1]) / float64(cellSize)))
	layer := int(math.Round(float64(p[2]-minV[2]) / float64(layerH)))
	return CellKey{Grid: 0, Col: col, Row: row, Layer: layer}
}

// TwoGridConfig holds parameters for two-grid clipping.
type TwoGridConfig struct {
	MinV       [3]float32
	Layer0Size float32
	UpperSize  float32
	LayerH     float32
	SeamZ      float32 // Z boundary between layer 0 and layer 1
}

// CentroidToCellTwoGrid maps a 3D point to the correct grid cell.
func CentroidToCellTwoGrid(p [3]float32, cfg TwoGridConfig) CellKey {
	if p[2] < cfg.SeamZ {
		col := int(math.Round(float64(p[0]-cfg.MinV[0]) / float64(cfg.Layer0Size)))
		row := int(math.Round(float64(p[1]-cfg.MinV[1]) / float64(cfg.Layer0Size)))
		layer := int(math.Round(float64(p[2]-cfg.MinV[2]) / float64(cfg.LayerH)))
		return CellKey{Grid: 0, Col: col, Row: row, Layer: layer}
	}
	col := int(math.Round(float64(p[0]-cfg.MinV[0]) / float64(cfg.UpperSize)))
	row := int(math.Round(float64(p[1]-cfg.MinV[1]) / float64(cfg.UpperSize)))
	layer := int(math.Round(float64(p[2]-cfg.MinV[2]) / float64(cfg.LayerH)))
	return CellKey{Grid: 1, Col: col, Row: row, Layer: layer}
}

// ClipMeshByPatchesTwoGrid clips the mesh using two different XY grids.
//
// TODO: refactor to share the common worker/clip/dedup logic with
// ClipMeshByPatches — the two functions are ~80% identical.
func ClipMeshByPatchesTwoGrid(
	ctx context.Context,
	model *loader.LoadedModel,
	patchMap map[CellKey]int,
	patchAssignment []int32,
	cfg TwoGridConfig,
) ([][3]float32, [][3]uint32, []int32, error) {
	// Build boundary planes per grid.
	// Z planes are global (shared by both grids). X/Y planes are per-grid.
	zPlaneSet := make(map[float32]struct{})
	var xyPlaneSets [2][2]map[float32]struct{} // [grid][axis 0=X, 1=Y]

	for ck, ci := range patchMap {
		myAssign := patchAssignment[ci]
		cellSize := cfg.Layer0Size
		if ck.Grid == 1 {
			cellSize = cfg.UpperSize
		}

		// X neighbor
		nk := CellKey{Grid: ck.Grid, Col: ck.Col + 1, Row: ck.Row, Layer: ck.Layer}
		if ni, ok := patchMap[nk]; ok && patchAssignment[ni] != myAssign {
			val := cfg.MinV[0] + (float32(ck.Col)+0.5)*cellSize
			if xyPlaneSets[ck.Grid][0] == nil {
				xyPlaneSets[ck.Grid][0] = make(map[float32]struct{})
			}
			xyPlaneSets[ck.Grid][0][val] = struct{}{}
		}
		// Y neighbor
		nk = CellKey{Grid: ck.Grid, Col: ck.Col, Row: ck.Row + 1, Layer: ck.Layer}
		if ni, ok := patchMap[nk]; ok && patchAssignment[ni] != myAssign {
			val := cfg.MinV[1] + (float32(ck.Row)+0.5)*cellSize
			if xyPlaneSets[ck.Grid][1] == nil {
				xyPlaneSets[ck.Grid][1] = make(map[float32]struct{})
			}
			xyPlaneSets[ck.Grid][1][val] = struct{}{}
		}
		// Z neighbor (within same grid)
		nk = CellKey{Grid: ck.Grid, Col: ck.Col, Row: ck.Row, Layer: ck.Layer + 1}
		if ni, ok := patchMap[nk]; ok && patchAssignment[ni] != myAssign {
			val := cfg.MinV[2] + (float32(ck.Layer)+0.5)*cfg.LayerH
			zPlaneSet[val] = struct{}{}
		}
	}

	// The seam Z is always a boundary (patches don't span grids).
	zPlaneSet[cfg.SeamZ] = struct{}{}

	// Sort plane lists.
	zPlanes := sortedKeys(zPlaneSet)
	var xyPlanes [2][2][]float32
	for g := 0; g < 2; g++ {
		for a := 0; a < 2; a++ {
			xyPlanes[g][a] = sortedKeys(xyPlaneSets[g][a])
		}
	}

	// Process faces in parallel.
	type workerResult struct {
		vd          *VertexDedup
		faces       [][3]uint32
		assignments []int32
	}

	nFaces := len(model.Faces)
	numWorkers := runtime.NumCPU()
	if numWorkers > nFaces {
		numWorkers = nFaces
	}
	if numWorkers < 1 {
		numWorkers = 1
	}
	results := make([]workerResult, numWorkers)
	chunkSize := (nFaces + numWorkers - 1) / numWorkers

	var wg sync.WaitGroup
	for w := range numWorkers {
		lo := w * chunkSize
		hi := lo + chunkSize
		if hi > nFaces {
			hi = nFaces
		}
		if lo >= hi {
			continue
		}
		wg.Add(1)
		go func(workerIdx, faceStart, faceEnd int) {
			defer wg.Done()
			wvd := NewVertexDedup()
			estFaces := (faceEnd - faceStart) * 4
			wFaces := make([][3]uint32, 0, estFaces)
			wAssign := make([]int32, 0, estFaces)

			for fi := faceStart; fi < faceEnd; fi++ {
				if (fi-faceStart)%1000 == 0 && ctx.Err() != nil {
					return
				}
				if FaceAlpha(fi, model) < 128 {
					continue
				}

				f := model.Faces[fi]
				v0 := model.Vertices[f[0]]
				v1 := model.Vertices[f[1]]
				v2 := model.Vertices[f[2]]

				// Step 1: clip by Z planes (including seam).
				fragments := clipByPlanes([][][3]float32{{v0, v1, v2}}, 2, zPlanes)

				// Step 2: for each Z-clipped fragment, determine grid and
				// clip by that grid's X/Y planes.
				var final [][][3]float32
				for _, poly := range fragments {
					centroid := polyCentroid(poly)
					grid := 1
					if centroid[2] < cfg.SeamZ {
						grid = 0
					}
					xyFrags := clipByPlanes([][][3]float32{poly}, 0, xyPlanes[grid][0])
					xyFrags = clipByPlanes(xyFrags, 1, xyPlanes[grid][1])
					final = append(final, xyFrags...)
				}

				for _, poly := range final {
					if polyArea(poly) < 1e-8 {
						continue
					}

					centroid := polyCentroid(poly)
					ck := CentroidToCellTwoGrid(centroid, cfg)
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

					vi0 := wvd.GetVertex(poly[0])
					for i := 1; i < len(poly)-1; i++ {
						vi1 := wvd.GetVertex(poly[i])
						vi2 := wvd.GetVertex(poly[i+1])
						if vi0 == vi1 || vi1 == vi2 || vi0 == vi2 {
							continue
						}
						wFaces = append(wFaces, [3]uint32{vi0, vi1, vi2})
						wAssign = append(wAssign, assignment)
					}
				}
			}

			results[workerIdx] = workerResult{vd: wvd, faces: wFaces, assignments: wAssign}
		}(w, lo, hi)
	}
	wg.Wait()

	if ctx.Err() != nil {
		return nil, nil, nil, ctx.Err()
	}

	globalVD := NewVertexDedup()
	var allFaces [][3]uint32
	var allAssignments []int32
	for _, r := range results {
		if r.vd == nil {
			continue
		}
		remap := make([]uint32, len(r.vd.Verts))
		for i, v := range r.vd.Verts {
			remap[i] = globalVD.GetVertex(v)
		}
		for _, f := range r.faces {
			allFaces = append(allFaces, [3]uint32{remap[f[0]], remap[f[1]], remap[f[2]]})
		}
		allAssignments = append(allAssignments, r.assignments...)
	}

	return globalVD.Verts, allFaces, allAssignments, nil
}

// clipByPlanes clips a set of polygons against sorted planes on a single axis.
func clipByPlanes(fragments [][][3]float32, axis int, planes []float32) [][][3]float32 {
	if len(planes) == 0 {
		return fragments
	}
	var result [][][3]float32
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

		iLo := sort.Search(len(planes), func(i int) bool { return planes[i] > lo })
		iHi := sort.Search(len(planes), func(i int) bool { return planes[i] >= hi })

		if iLo >= iHi {
			result = append(result, poly)
			continue
		}

		current := [][][3]float32{poly}
		for pi := iLo; pi < iHi; pi++ {
			var remaining [][][3]float32
			for _, p := range current {
				neg, pos := ClipPolygonByPlane(p, axis, planes[pi])
				if len(neg) >= 3 {
					result = append(result, neg)
				}
				if len(pos) >= 3 {
					remaining = append(remaining, pos)
				}
			}
			current = remaining
		}
		for _, p := range current {
			if len(p) >= 3 {
				result = append(result, p)
			}
		}
	}
	return result
}

func sortedKeys(m map[float32]struct{}) []float32 {
	if len(m) == 0 {
		return nil
	}
	s := make([]float32, 0, len(m))
	for v := range m {
		s = append(s, v)
	}
	sort.Slice(s, func(a, b int) bool { return s[a] < s[b] })
	return s
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
					nk := CellKey{Grid: ck.Grid, Col: ck.Col + dc, Row: ck.Row + dr, Layer: ck.Layer + dl}
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

