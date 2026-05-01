package split

import (
	"fmt"
	"math"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// capPolygon represents one component of a half's cap (the flat
// surface added by CGAL's clip on the cut plane). outer is the CCW
// outer boundary loop in 2D plane-basis coordinates; holes are the
// CW inner boundary loops (cavities). Both loops are closed but the
// closing edge is implicit (loop[0] connects to loop[len-1]).
type capPolygon struct {
	outer [][2]float64
	holes [][][2]float64
}

// recoverCapPolygons walks the half's faces, finds those lying on
// the cut plane (cap faces), traces their boundary, and returns one
// capPolygon per connected component.
//
// The plane's normal must point in the cap's outward direction —
// after cgalclip.Clip(model, plane.Normal, plane.D), half 0's cap
// outward normal equals +plane.Normal, and half 1's cap outward
// normal equals -plane.Normal. Callers pass the normal that matches
// the half being analyzed.
func recoverCapPolygons(half *loader.LoadedModel, capNormal [3]float64, planeD float64) ([]capPolygon, error) {
	if half == nil || len(half.Faces) == 0 {
		return nil, fmt.Errorf("recoverCapPolygons: empty half")
	}

	bbox := bboxDiag(half.Vertices)
	planeEps := math.Max(1e-6, 1e-6*bbox)

	// 1. Identify cap faces.
	capFaces := make([]int, 0)
	for fi, f := range half.Faces {
		v0 := vec3(half.Vertices[f[0]])
		v1 := vec3(half.Vertices[f[1]])
		v2 := vec3(half.Vertices[f[2]])
		// Centroid on plane?
		cz := (dot3(v0, capNormal) + dot3(v1, capNormal) + dot3(v2, capNormal)) / 3
		if math.Abs(cz-planeD) > planeEps {
			continue
		}
		// Outward normal aligned with capNormal? Use cross of edges,
		// don't bother normalizing — sign-of-dot is enough.
		e1 := sub3(v1, v0)
		e2 := sub3(v2, v0)
		n := cross3(e1, e2)
		// Allow a small slack — CGAL kernel rounds vertex positions,
		// so cap faces' computed normals can wobble a few ULPs off
		// from capNormal. Require cos > 0.99 (≈8°) to be safe.
		nl := math.Sqrt(n[0]*n[0] + n[1]*n[1] + n[2]*n[2])
		if nl == 0 {
			continue
		}
		cos := dot3(n, capNormal) / nl
		if cos < 0.99 {
			continue
		}
		capFaces = append(capFaces, fi)
	}
	if len(capFaces) == 0 {
		return nil, fmt.Errorf("recoverCapPolygons: no cap faces found")
	}

	// 2. Build edge map: undirected edge -> incident cap-face count.
	type edgeKey struct{ a, b uint32 }
	mkEdge := func(a, b uint32) edgeKey {
		if a < b {
			return edgeKey{a, b}
		}
		return edgeKey{b, a}
	}
	edgeCount := make(map[edgeKey]int)
	for _, fi := range capFaces {
		f := half.Faces[fi]
		edgeCount[mkEdge(f[0], f[1])]++
		edgeCount[mkEdge(f[1], f[2])]++
		edgeCount[mkEdge(f[2], f[0])]++
	}

	// 3. Boundary edges: count == 1. Build adjacency so we can walk
	// loops by following the unique successor at each endpoint.
	type adjEntry struct {
		other uint32
	}
	adj := make(map[uint32][]adjEntry)
	for ek, n := range edgeCount {
		if n != 1 {
			continue
		}
		adj[ek.a] = append(adj[ek.a], adjEntry{ek.b})
		adj[ek.b] = append(adj[ek.b], adjEntry{ek.a})
	}
	if len(adj) == 0 {
		return nil, fmt.Errorf("recoverCapPolygons: no boundary edges")
	}

	// 4. Walk loops. With a manifold cap, every boundary vertex has
	// exactly 2 boundary neighbors. T-junctions or non-manifold caps
	// would show degree > 2; we pick the unvisited neighbor at each
	// step and warn on degree > 2.
	visitedEdge := make(map[edgeKey]bool)
	var loops [][][2]float64

	// 2D basis for projecting plane points.
	uBasis, vBasis := perpBasis(capNormal)

	project2D := func(p [3]float64) [2]float64 {
		return [2]float64{dot3(p, uBasis), dot3(p, vBasis)}
	}

	for startVert, neigh := range adj {
		if len(neigh) == 0 {
			continue
		}
		// Find an unvisited edge starting here.
		var firstNext uint32
		found := false
		for _, e := range neigh {
			if !visitedEdge[mkEdge(startVert, e.other)] {
				firstNext = e.other
				found = true
				break
			}
		}
		if !found {
			continue
		}

		// Walk the loop.
		loop := make([]uint32, 0)
		loop = append(loop, startVert)
		prev := startVert
		cur := firstNext
		visitedEdge[mkEdge(prev, cur)] = true
		for cur != startVert {
			loop = append(loop, cur)
			// Find next neighbor of cur that isn't prev (and edge unvisited).
			next := uint32(math.MaxUint32)
			for _, e := range adj[cur] {
				if e.other == prev {
					continue
				}
				if visitedEdge[mkEdge(cur, e.other)] {
					continue
				}
				next = e.other
				break
			}
			if next == math.MaxUint32 {
				return nil, fmt.Errorf("recoverCapPolygons: loop did not close (open chain at vertex %d)", cur)
			}
			visitedEdge[mkEdge(cur, next)] = true
			prev = cur
			cur = next
		}

		// Project to 2D.
		pts := make([][2]float64, len(loop))
		for i, vi := range loop {
			pts[i] = project2D(vec3(half.Vertices[vi]))
		}
		loops = append(loops, pts)
	}

	if len(loops) == 0 {
		return nil, fmt.Errorf("recoverCapPolygons: no closed loops recovered")
	}

	// 5. Classify loops by enclosure depth. A loop is outer if no
	// other loop encloses it; a hole if enclosed by exactly one outer
	// loop. (The boundary walk doesn't preserve cap-face winding
	// direction, so loop signed-area sign is unreliable for outer/hole
	// classification — use point-in-polygon depth instead.) Loops
	// with degenerate (near-zero) area are skipped.
	type loopInfo struct {
		pts     [][2]float64
		absArea float64
		depth   int
		parent  int // index of nearest enclosing loop, -1 if outer
	}
	infos := make([]loopInfo, 0, len(loops))
	for _, lp := range loops {
		a := math.Abs(signedArea2D(lp))
		if a < 1e-12 {
			continue
		}
		infos = append(infos, loopInfo{pts: lp, absArea: a, parent: -1})
	}
	if len(infos) == 0 {
		return nil, fmt.Errorf("recoverCapPolygons: all loops degenerate")
	}

	// For each loop i, count how many other loops enclose it. The
	// nearest enclosing loop (smallest area among enclosers) is its
	// parent. Even depth = outer loop; odd depth = hole.
	for i := range infos {
		probe := infos[i].pts[0]
		bestArea := math.Inf(1)
		bestParent := -1
		for j := range infos {
			if i == j {
				continue
			}
			if !pointInPolygon2D(probe, infos[j].pts) {
				continue
			}
			infos[i].depth++
			if infos[j].absArea < bestArea {
				bestArea = infos[j].absArea
				bestParent = j
			}
		}
		infos[i].parent = bestParent
	}

	// Normalize windings: outer = CCW (positive signed area), hole =
	// CW (negative signed area). Reverse if needed.
	for i := range infos {
		want := 1.0
		if infos[i].depth%2 == 1 {
			want = -1.0
		}
		if math.Copysign(1, signedArea2D(infos[i].pts)) != want {
			infos[i].pts = reverseLoop(infos[i].pts)
		}
	}

	// Assemble polygons: one capPolygon per outer (depth 0) loop;
	// holes attach to their nearest-enclosing outer parent.
	outerIdxOf := make(map[int]int)
	var polygons []capPolygon
	for i, info := range infos {
		if info.depth%2 == 0 {
			outerIdxOf[i] = len(polygons)
			polygons = append(polygons, capPolygon{outer: info.pts})
		}
	}
	if len(polygons) == 0 {
		return nil, fmt.Errorf("recoverCapPolygons: no outer loops found")
	}
	for i, info := range infos {
		if info.depth%2 == 1 {
			parent := info.parent
			// Walk up to the nearest depth-0 (outer) ancestor.
			for parent >= 0 && infos[parent].depth%2 == 1 {
				parent = infos[parent].parent
			}
			if parent < 0 {
				// Shouldn't happen; an odd-depth loop must be enclosed.
				continue
			}
			polygons[outerIdxOf[parent]].holes = append(polygons[outerIdxOf[parent]].holes, info.pts)
			_ = i
		}
	}

	return polygons, nil
}

func vec3(v [3]float32) [3]float64 {
	return [3]float64{float64(v[0]), float64(v[1]), float64(v[2])}
}

func dot3(a, b [3]float64) float64 {
	return a[0]*b[0] + a[1]*b[1] + a[2]*b[2]
}

func sub3(a, b [3]float64) [3]float64 {
	return [3]float64{a[0] - b[0], a[1] - b[1], a[2] - b[2]}
}

func bboxDiag(verts [][3]float32) float64 {
	if len(verts) == 0 {
		return 0
	}
	mn := verts[0]
	mx := verts[0]
	for _, v := range verts[1:] {
		for i := 0; i < 3; i++ {
			if v[i] < mn[i] {
				mn[i] = v[i]
			}
			if v[i] > mx[i] {
				mx[i] = v[i]
			}
		}
	}
	d := [3]float64{
		float64(mx[0] - mn[0]),
		float64(mx[1] - mn[1]),
		float64(mx[2] - mn[2]),
	}
	return math.Sqrt(d[0]*d[0] + d[1]*d[1] + d[2]*d[2])
}

func signedArea2D(loop [][2]float64) float64 {
	if len(loop) < 3 {
		return 0
	}
	var a float64
	for i := range loop {
		j := (i + 1) % len(loop)
		a += loop[i][0]*loop[j][1] - loop[j][0]*loop[i][1]
	}
	return 0.5 * a
}

func pointInPolygon2D(p [2]float64, loop [][2]float64) bool {
	inside := false
	n := len(loop)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		xi, yi := loop[i][0], loop[i][1]
		xj, yj := loop[j][0], loop[j][1]
		if (yi > p[1]) != (yj > p[1]) {
			t := (p[1] - yi) / (yj - yi)
			x := xi + t*(xj-xi)
			if p[0] < x {
				inside = !inside
			}
		}
	}
	return inside
}

func reverseLoop(loop [][2]float64) [][2]float64 {
	r := make([][2]float64, len(loop))
	for i, p := range loop {
		r[len(loop)-1-i] = p
	}
	return r
}
