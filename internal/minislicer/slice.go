package minislicer

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
	layers := make([]Layer, len(zPlanes))
	for i, z := range zPlanes {
		layers[i] = sliceAtZ(model, z, i)
	}
	return layers
}

// PlanesForRange returns slicing Z planes spanning [zMin, zMax] with
// uniform spacing layerH. The first plane is at zMin + layerH/2 (the
// center of layer 0) and subsequent planes at zMin + (k+0.5)*layerH.
// The last plane is included if its center is <= zMax.
func PlanesForRange(zMin, zMax, layerH float32) []float32 {
	if layerH <= 0 || zMax <= zMin {
		return nil
	}
	span := zMax - zMin
	n := int(math.Floor(float64(span / layerH)))
	if n < 1 {
		n = 1
	}
	planes := make([]float32, 0, n)
	for k := 0; k < n; k++ {
		z := zMin + (float32(k)+0.5)*layerH
		if z > zMax {
			break
		}
		planes = append(planes, z)
	}
	return planes
}

// segment is one triangle's intersection with a slicing plane.
type segment struct {
	a, b Point2
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
	for _, f := range model.Faces {
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
		segs = append(segs, segment{a: crossings[0], b: crossings[1]})
	}

	loops := chainSegments(segs, z)
	return Layer{
		Z:        z,
		LayerIdx: layerIdx,
		Loops:    loops,
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
		startK := pkey(s.a)
		curK := pkey(s.b)

		for curK != startK {
			// Find an unused segment that shares the current endpoint.
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
				// Open chain — drop it; topology should be closed.
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
			curK = pkey(nextPt)
		}
		if len(points) < 3 {
			continue
		}
		// Drop the closing duplicate if present.
		last := len(points) - 1
		if pkey(points[last]) == pkey(points[0]) {
			points = points[:last]
		}
		if len(points) < 3 {
			continue
		}
		points = collapseCollinear(points)
		if len(points) < 3 {
			continue
		}
		loops = append(loops, Loop{
			Points:     points,
			Z:          z,
			SignedArea: signedArea(points),
		})
	}

	return loops
}

// collapseCollinear removes interior points that lie on the line
// between their neighbors. It runs once around the loop with a
// relative cross-product tolerance.
func collapseCollinear(points []Point2) []Point2 {
	n := len(points)
	if n < 3 {
		return points
	}
	// Tolerance is in (mesh-units)² of the cross product. 1e-7 mm² is
	// well below any meaningful feature in this codebase's coordinate
	// scale (mm), and snaps duplicate corners that float through lerp.
	const crossEps = 1e-7
	out := make([]Point2, 0, n)
	for i := 0; i < n; i++ {
		prev := points[(i-1+n)%n]
		cur := points[i]
		next := points[(i+1)%n]
		dx0 := float64(cur[0] - prev[0])
		dy0 := float64(cur[1] - prev[1])
		dx1 := float64(next[0] - cur[0])
		dy1 := float64(next[1] - cur[1])
		cross := dx0*dy1 - dy0*dx1
		if math.Abs(cross) < crossEps && (dx0*dx1+dy0*dy1) > 0 {
			// Collinear and same direction — drop cur.
			continue
		}
		out = append(out, cur)
	}
	return out
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
