package cellslicer

import (
	"math"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// SliceMesh slices the model at each Z height in zPlanes and returns
// one Layer per plane. Loops within a layer are closed and oriented
// by signed area: SignedArea > 0 is CCW (outer boundary by
// convention); < 0 is CW (hole or inverted island).
//
// Triangle vertices that fall exactly on a slicing plane are nudged
// "above" by a small epsilon during classification, so each triangle
// contributes 0 or 1 segments — no degenerate co-planar handling.
// Triangles entirely on the plane (all three z's equal Z) are
// skipped, matching how a real slicer treats top/bottom faces.
func SliceMesh(model *loader.LoadedModel, zPlanes []float32) []Layer {
	return SliceMeshProgress(model, zPlanes, nil)
}

// SliceMeshProgress is SliceMesh with a per-plane progress callback.
// onPlane (may be nil) receives (planes done, total planes) after each
// plane is sliced; planes are processed sequentially.
func SliceMeshProgress(model *loader.LoadedModel, zPlanes []float32, onPlane func(done, total int)) []Layer {
	layers := make([]Layer, len(zPlanes))
	for i, z := range zPlanes {
		layers[i] = sliceAtZ(model, z, i)
		if onPlane != nil {
			onPlane(i+1, len(zPlanes))
		}
	}
	return layers
}

// PlanesForRange returns slicing Z planes spanning [zMin, zMax] with
// uniform spacing layerH. The first plane is at zMin + layerH/2 (the
// center of layer 0) and subsequent planes at zMin + (k+0.5)*layerH,
// with a sub-µm-scale per-plane offset (planeJitter) added so no
// plane lands exactly on a model vertex.
//
// Without the jitter, a plane passing through a high-valence vertex
// (e.g. where the cut surface meets the skin) makes many of that
// vertex's incident triangles produce segments ending at coincident
// XY positions, and the chainer joins them in arbitrary order —
// visible as occasional "messed up layers" of swapped colors in
// the output. The offset is well above float precision at typical
// model scales (~1e-5 mm) and far below any visible feature.
func PlanesForRange(zMin, zMax, layerH float32) []float32 {
	if layerH <= 0 || zMax <= zMin {
		return nil
	}
	span := zMax - zMin
	n := int(math.Floor(float64(span / layerH)))
	if n < 1 {
		n = 1
	}
	const planeJitter float32 = 1e-4 // mm; absolute, not relative
	planes := make([]float32, 0, n)
	for k := 0; k < n; k++ {
		z := zMin + (float32(k)+0.5)*layerH + planeJitter
		if z > zMax {
			break
		}
		planes = append(planes, z)
	}
	return planes
}

// segment is one triangle's intersection with a slicing plane.
type segment struct {
	a, b   Point2
	triIdx int32 // source triangle index in the model, or -1
}

// sliceAtZ slices the model at a single Z height.
func sliceAtZ(model *loader.LoadedModel, z float32, layerIdx int) Layer {
	// Per-layer crossing epsilon. The actual value isn't critical
	// because we only use it to disambiguate vertex-on-plane cases
	// (treat as "above"); after classification, real crossings are
	// computed by lerp using the original vertex z.
	const planeEps = 1e-7

	var segs []segment

	verts := model.Vertices
	for fi, f := range model.Faces {
		v0 := verts[f[0]]
		v1 := verts[f[1]]
		v2 := verts[f[2]]
		zs := [3]float32{v0[2], v1[2], v2[2]}
		// Quick AABB reject.
		zMin := minf32(zs[0], minf32(zs[1], zs[2]))
		zMax := maxf32(zs[0], maxf32(zs[1], zs[2]))
		if zMax < z || zMin > z {
			continue
		}
		// Classify above/below with on-plane treated as above.
		var above [3]bool
		for k := 0; k < 3; k++ {
			zk := zs[k]
			if absf32(zk-z) < planeEps {
				above[k] = true
			} else {
				above[k] = zk > z
			}
		}
		nAbove := 0
		for _, a := range above {
			if a {
				nAbove++
			}
		}
		if nAbove == 0 || nAbove == 3 {
			continue
		}
		// Degenerate-touch case: exactly one vertex is on the
		// slicing plane (zk ≈ z) and the other two are strictly
		// below it. We've classified the on-plane vertex as
		// "above," giving nAbove==1, but the triangle's actual
		// intersection with the plane is a single point (that
		// vertex), not a line segment. Both edge crossings
		// computed below would lerp to t∈{0,1} at the on-plane
		// vertex's XY, producing a zero-length segment that
		// pollutes the chain — at high-valence model vertices
		// where many triangles meet at the same Z, this leaks
		// many coincident-endpoint segments and the chainer
		// joins them in arbitrary order, scrambling the loop
		// topology for the whole layer. Skip.
		if nAbove == 1 {
			loneAbove := 0
			for k := 0; k < 3; k++ {
				if above[k] {
					loneAbove = k
					break
				}
			}
			if absf32(zs[loneAbove]-z) < planeEps {
				continue
			}
		}
		// Compute the two edge crossings. Walk edges in vertex order
		// (0→1, 1→2, 2→0) and pick the two whose endpoints differ.
		//
		// Each crossing is computed with the edge's two endpoints
		// canonicalized by vertex index (lower-index endpoint first),
		// so two triangles sharing an edge produce bit-identical
		// crossings regardless of their triangle-local vertex order.
		// Without this, float-noise drift between (a + t*(b-a)) and
		// (b + (1-t)*(a-b)) can land the two crossings in different
		// quantize buckets — chainSegments then fails to close the
		// loop and the layer is silently dropped.
		pts := [3][3]float32{v0, v1, v2}
		idx := [3]uint32{f[0], f[1], f[2]}
		var crossings [2]Point2
		ci := 0
		for k := 0; k < 3; k++ {
			j := (k + 1) % 3
			if above[k] != above[j] {
				if ci > 1 {
					// shouldn't happen by topology; bail out safely
					ci = 0
					break
				}
				lo, hi := pts[k], pts[j]
				if idx[k] > idx[j] {
					lo, hi = pts[j], pts[k]
				}
				crossings[ci] = lerpEdge(lo, hi, z)
				ci++
			}
		}
		if ci != 2 {
			continue
		}
		segs = append(segs, segment{a: crossings[0], b: crossings[1], triIdx: int32(fi)})
	}

	loops := chainSegments(segs, z)
	classifyHoles(loops)
	return Layer{
		Z:        z,
		LayerIdx: layerIdx,
		Loops:    loops,
	}
}

// classifyHoles sets IsHole on each loop using even-odd nesting:
// for each loop, count how many other loops in the same layer
// contain a vertex of the loop; odd count = hole.
//
// Uses a vertex (not the centroid) because the centroid of a
// concave polygon — including any outer loop with a cavity —
// can fall outside the loop itself, e.g. inside its own hole. A
// vertex sits exactly on the loop's boundary, which for two
// non-intersecting polygons in the slicer's output is
// unambiguously inside or outside any sibling polygon (not on
// the sibling's edge).
func classifyHoles(loops []Loop) {
	for i := range loops {
		pts := loops[i].Points
		if len(pts) < 3 {
			continue
		}
		x, y := pts[0][0], pts[0][1]
		depth := 0
		for j := range loops {
			if j == i {
				continue
			}
			if len(loops[j].Points) < 3 {
				continue
			}
			if loops[j].Contains(x, y) {
				depth++
			}
		}
		loops[i].IsHole = (depth & 1) == 1
	}
}

// lerpEdge returns the XY of the point on the line through pa and pb
// at z. pa.z and pb.z must straddle z (or one equals z within eps).
func lerpEdge(pa, pb [3]float32, z float32) Point2 {
	dz := pb[2] - pa[2]
	if dz == 0 {
		return Point2{pa[0], pa[1]}
	}
	t := (z - pa[2]) / dz
	return Point2{
		pa[0] + t*(pb[0]-pa[0]),
		pa[1] + t*(pb[1]-pa[1]),
	}
}

// chainSegments takes a flat list of unordered segments at a single
// Z and links them into closed loops by endpoint coincidence.
//
// Endpoints are quantized into integer keys to absorb floating-point
// noise: two endpoints within ~1e-5 mesh units are considered the
// same vertex. The quantization grid is fixed so adjacent triangles'
// shared-edge crossings (computed from the same lerp inputs) snap to
// identical keys.
func chainSegments(segs []segment, z float32) []Loop {
	if len(segs) == 0 {
		return nil
	}
	const quantize = 1e5 // 1 unit / 1e5 ≈ 10 µm at mm scale

	type key [2]int64
	pkey := func(p Point2) key {
		return key{
			int64(math.Round(float64(p[0]) * quantize)),
			int64(math.Round(float64(p[1]) * quantize)),
		}
	}

	// Adjacency: for each endpoint key, list of (segIdx, end) where
	// end=0 means seg.a matches, end=1 means seg.b matches.
	type endpoint struct {
		segIdx int
		end    int
	}
	adj := make(map[key][]endpoint, len(segs)*2)
	for i, s := range segs {
		adj[pkey(s.a)] = append(adj[pkey(s.a)], endpoint{segIdx: i, end: 0})
		adj[pkey(s.b)] = append(adj[pkey(s.b)], endpoint{segIdx: i, end: 1})
	}

	used := make([]bool, len(segs))
	var loops []Loop

	for startSeg := 0; startSeg < len(segs); startSeg++ {
		if used[startSeg] {
			continue
		}
		used[startSeg] = true
		s := segs[startSeg]
		points := []Point2{s.a, s.b}
		// edgeTris[i] = source triangle index of the edge
		// points[i] → points[i+1]. Same length as edges = points-1
		// for an open chain; for a closed loop the trailing edge
		// (points[n-1] → points[0]) also has an entry.
		edgeTris := []int32{s.triIdx}
		startK := pkey(s.a)
		curK := pkey(s.b)

		for curK != startK {
			next := -1
			nextOtherEnd := 0
			for _, e := range adj[curK] {
				if e.segIdx == startSeg || used[e.segIdx] {
					continue
				}
				next = e.segIdx
				nextOtherEnd = 1 - e.end
				break
			}
			if next == -1 {
				points = nil
				break
			}
			used[next] = true
			ns := segs[next]
			var nextPt Point2
			if nextOtherEnd == 1 {
				nextPt = ns.b
			} else {
				nextPt = ns.a
			}
			points = append(points, nextPt)
			edgeTris = append(edgeTris, ns.triIdx)
			curK = pkey(nextPt)
		}
		if len(points) < 3 {
			continue
		}
		// Drop the closing duplicate if present.
		last := len(points) - 1
		if pkey(points[last]) == pkey(points[0]) {
			points = points[:last]
			edgeTris = edgeTris[:last]
		}
		if len(points) < 3 {
			continue
		}
		points, edgeTris = collapseCollinearKeepTris(points, edgeTris)
		if len(points) < 3 {
			continue
		}
		_ = edgeTris // discarded: cellslicer doesn't need per-edge source-triangle provenance
		l := Loop{
			Points: points,
			Z:      z,
		}
		l.RefreshDerived()
		loops = append(loops, l)
	}

	return loops
}

// collapseCollinear removes interior points that lie on the line
// between their neighbors. It runs once around the loop with a
// relative cross-product tolerance.
func collapseCollinear(points []Point2) []Point2 {
	pts, _ := collapseCollinearKeepTris(points, nil)
	return pts
}

// collapseCollinearKeepTris is collapseCollinear with a parallel
// edgeTris slice updated to match. edgeTris[i] is the source
// triangle for the edge points[i] → points[(i+1)%n]; when point i
// is dropped, the surviving edge from points[i-1] → points[i+1]
// inherits edgeTris[i-1] (the "incoming" edge's triangle). Pass
// edgeTris == nil to just collapse points.
func collapseCollinearKeepTris(points []Point2, edgeTris []int32) ([]Point2, []int32) {
	n := len(points)
	if n < 3 {
		return points, edgeTris
	}
	// Relative-angle collinearity threshold. We drop a vertex when the
	// turning angle there is below ~sin⁻¹(1e-4) ≈ 0.006° — i.e. the
	// incoming and outgoing edges are essentially the same direction.
	//
	// Earlier this used an absolute cross-product threshold (1e-7).
	// That broke when a slice plane landed within ~10⁻⁴ of a vertex
	// row of a fine sphere mesh: the standard 720-point contour
	// degenerates into a zigzag of one short tangential edge and one
	// 100-nm radial edge per vertex, the cross product of those two
	// edges sits at ~4e-8 (below the absolute threshold) even though
	// the turning angle is ~0.7°. collapseCollinearKeepTris stripped
	// every vertex, chainSegments returned no loops, and the slab's
	// footprint silently lost its true outer contour — visible as a
	// concentric ring gap around the pole of the earth/top render.
	const sinEps2 = 1e-8 // sin² of the cutoff turning angle
	outPts := make([]Point2, 0, n)
	var outTris []int32
	if edgeTris != nil {
		outTris = make([]int32, 0, n)
	}
	for i := 0; i < n; i++ {
		prev := points[(i-1+n)%n]
		cur := points[i]
		next := points[(i+1)%n]
		dx0 := float64(cur[0] - prev[0])
		dy0 := float64(cur[1] - prev[1])
		dx1 := float64(next[0] - cur[0])
		dy1 := float64(next[1] - cur[1])
		cross := dx0*dy1 - dy0*dx1
		dot := dx0*dx1 + dy0*dy1
		// |sin(turn)|² = cross² / (|e0|² · |e1|²). Drop the vertex
		// only when sin² is below threshold (collinear) AND dot > 0
		// (edges point the same way — i.e. a flat continuation, not
		// a U-turn).
		l0sq := dx0*dx0 + dy0*dy0
		l1sq := dx1*dx1 + dy1*dy1
		dropped := dot > 0 && cross*cross < sinEps2*l0sq*l1sq
		if dropped {
			continue
		}
		outPts = append(outPts, cur)
		if edgeTris != nil {
			outTris = append(outTris, edgeTris[i])
		}
	}
	return outPts, outTris
}

// signedArea returns 2× the signed area of the closed polygon given
// by points (no repeated final vertex). Positive = CCW.
func signedArea(points []Point2) float32 {
	n := len(points)
	if n < 3 {
		return 0
	}
	var s float64
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		s += float64(points[i][0])*float64(points[j][1]) -
			float64(points[j][0])*float64(points[i][1])
	}
	return float32(s * 0.5)
}

func minf32(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}
func maxf32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
func absf32(a float32) float32 {
	if a < 0 {
		return -a
	}
	return a
}
