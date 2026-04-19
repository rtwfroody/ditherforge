package voxel

import (
	"context"
	"math"

	"github.com/rtwfroody/ditherforge/internal/progress"
)

// MergeCoplanarTriangles reduces triangle count by finding connected groups of
// coplanar same-material triangles, extracting each group's boundary polygon,
// and re-triangulating with ear clipping.
//
// Emits two stages: "Grouping faces" (BFS over faces) and "Merging" (per-group
// ear clipping). The caller should not also emit StageStart/StageDone for
// these stages.
func MergeCoplanarTriangles(ctx context.Context, verts [][3]float32, faces [][3]uint32, assignments []int32, tracker progress.Tracker) ([][3]uint32, []int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	nFaces := len(faces)

	type edgeKey struct{ a, b uint32 }
	makeEdge := func(a, b uint32) edgeKey {
		if a > b {
			a, b = b, a
		}
		return edgeKey{a, b}
	}
	edgeFaces := make(map[edgeKey][]int32, nFaces*3)
	for fi, f := range faces {
		for i := 0; i < 3; i++ {
			e := makeEdge(f[i], f[(i+1)%3])
			edgeFaces[e] = append(edgeFaces[e], int32(fi))
		}
	}

	normals := make([][3]float32, nFaces)
	for fi, f := range faces {
		normals[fi] = TriNormal(verts[f[0]], verts[f[1]], verts[f[2]])
	}

	const cosThresh float32 = 0.9999
	groupID := make([]int, nFaces)
	for i := range groupID {
		groupID[i] = -1
	}
	var groups [][]int

	grouping := progress.BeginStage(tracker, "Grouping faces", true, nFaces)
	defer grouping.Done()

	for fi := 0; fi < nFaces; fi++ {
		if fi%1000 == 0 {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			grouping.Progress(fi)
		}
		if groupID[fi] >= 0 {
			continue
		}
		gid := len(groups)
		group := []int{fi}
		groupID[fi] = gid

		queue := []int{fi}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			f := faces[cur]
			for i := 0; i < 3; i++ {
				e := makeEdge(f[i], f[(i+1)%3])
				for _, nfi := range edgeFaces[e] {
					if int(nfi) == cur || groupID[nfi] >= 0 {
						continue
					}
					if assignments[nfi] != assignments[fi] {
						continue
					}
					dot := normals[fi][0]*normals[nfi][0] + normals[fi][1]*normals[nfi][1] + normals[fi][2]*normals[nfi][2]
					if dot < cosThresh {
						continue
					}
					groupID[nfi] = gid
					group = append(group, int(nfi))
					queue = append(queue, int(nfi))
				}
			}
		}
		groups = append(groups, group)
	}
	grouping.Done()

	newFaces := make([][3]uint32, 0, nFaces)
	newAssignments := make([]int32, 0, nFaces)
	replaced := make([]bool, nFaces)

	merging := progress.BeginStage(tracker, "Merging", true, len(groups))
	defer merging.Done()

	for gi, group := range groups {
		if gi%1000 == 0 {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			merging.Progress(gi)
		}
		if len(group) < 2 {
			continue
		}

		groupSet := make(map[int]bool, len(group))
		for _, fi := range group {
			groupSet[fi] = true
		}

		type dirEdge struct{ from, to uint32 }
		var boundary []dirEdge

		for _, fi := range group {
			f := faces[fi]
			for i := 0; i < 3; i++ {
				a, b := f[i], f[(i+1)%3]
				e := makeEdge(a, b)
				isBoundary := true
				for _, nfi := range edgeFaces[e] {
					if int(nfi) != fi && groupSet[int(nfi)] {
						isBoundary = false
						break
					}
				}
				if isBoundary {
					boundary = append(boundary, dirEdge{a, b})
				}
			}
		}

		if len(boundary) < 3 {
			continue
		}

		nextMap := make(map[uint32]uint32, len(boundary))
		valid := true
		for _, de := range boundary {
			if _, exists := nextMap[de.from]; exists {
				valid = false
				break
			}
			nextMap[de.from] = de.to
		}
		if !valid {
			continue
		}

		start := boundary[0].from
		loop := []uint32{start}
		cur := nextMap[start]
		loopOK := true
		for cur != start {
			loop = append(loop, cur)
			next, ok := nextMap[cur]
			if !ok || len(loop) > len(boundary)+1 {
				loopOK = false
				break
			}
			cur = next
		}
		if !loopOK || len(loop) != len(boundary) {
			continue
		}

		if len(loop)-2 >= len(group) {
			continue
		}

		n := normals[group[0]]
		var u, v [3]float32
		if n[0]*n[0] < 0.81 {
			u = Cross3f(n, [3]float32{1, 0, 0})
		} else {
			u = Cross3f(n, [3]float32{0, 1, 0})
		}
		uLen := float32(math.Sqrt(float64(u[0]*u[0] + u[1]*u[1] + u[2]*u[2])))
		u[0] /= uLen
		u[1] /= uLen
		u[2] /= uLen
		v = Cross3f(n, u)

		pts := make([][2]float64, len(loop))
		for i, vi := range loop {
			p := verts[vi]
			pts[i] = [2]float64{
				float64(p[0]*u[0] + p[1]*u[1] + p[2]*u[2]),
				float64(p[0]*v[0] + p[1]*v[1] + p[2]*v[2]),
			}
		}

		earTris := earClip(pts)
		if earTris == nil || len(earTris) >= len(group) {
			continue
		}

		for _, fi := range group {
			replaced[fi] = true
		}
		for _, tri := range earTris {
			v0, v1, v2 := loop[tri[0]], loop[tri[1]], loop[tri[2]]
			if v0 == v1 || v1 == v2 || v0 == v2 {
				continue
			}
			if verts[v0] == verts[v1] || verts[v1] == verts[v2] || verts[v0] == verts[v2] {
				continue
			}
			newFaces = append(newFaces, [3]uint32{v0, v1, v2})
			newAssignments = append(newAssignments, assignments[group[0]])
		}
	}

	for fi, f := range faces {
		if !replaced[fi] {
			newFaces = append(newFaces, f)
			newAssignments = append(newAssignments, assignments[fi])
		}
	}

	return newFaces, newAssignments, nil
}

func earClip(pts [][2]float64) [][3]int {
	n := len(pts)
	if n < 3 {
		return nil
	}
	if n == 3 {
		return [][3]int{{0, 1, 2}}
	}

	area := 0.0
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		area += pts[i][0]*pts[j][1] - pts[j][0]*pts[i][1]
	}

	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	if area < 0 {
		for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
			idx[i], idx[j] = idx[j], idx[i]
		}
	}

	var result [][3]int
	rem := make([]int, len(idx))
	copy(rem, idx)

	for len(rem) > 3 {
		found := false
		for i := 0; i < len(rem); i++ {
			prev := (i + len(rem) - 1) % len(rem)
			next := (i + 1) % len(rem)

			a := pts[rem[prev]]
			b := pts[rem[i]]
			c := pts[rem[next]]

			cross := (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0])
			if cross < 1e-10 {
				continue
			}

			isEar := true
			for j := 0; j < len(rem); j++ {
				if j == prev || j == i || j == next {
					continue
				}
				if pointInTri2D(pts[rem[j]], a, b, c) {
					isEar = false
					break
				}
			}
			if !isEar {
				continue
			}

			result = append(result, [3]int{rem[prev], rem[i], rem[next]})
			rem = append(rem[:i], rem[i+1:]...)
			found = true
			break
		}
		if !found {
			return nil
		}
	}

	if len(rem) == 3 {
		result = append(result, [3]int{rem[0], rem[1], rem[2]})
	}
	return result
}

func pointInTri2D(p, a, b, c [2]float64) bool {
	d1 := (p[0]-b[0])*(a[1]-b[1]) - (a[0]-b[0])*(p[1]-b[1])
	d2 := (p[0]-c[0])*(b[1]-c[1]) - (b[0]-c[0])*(p[1]-c[1])
	d3 := (p[0]-a[0])*(c[1]-a[1]) - (c[0]-a[0])*(p[1]-a[1])
	hasNeg := d1 < 0 || d2 < 0 || d3 < 0
	hasPos := d1 > 0 || d2 > 0 || d3 > 0
	return !(hasNeg && hasPos)
}
