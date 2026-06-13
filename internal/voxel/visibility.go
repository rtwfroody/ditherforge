package voxel

// Exterior-visibility classification for color sampling.
//
// SampleNearestColorWithSticker picks the nearest triangle to each
// sample point with no notion of which surfaces can actually be seen
// from outside the model. Interior geometry that hugs the visible skin
// — most acutely the flood-fill "pocket" caps that sit 0.02–0.2mm
// beneath painted surfaces — therefore wins the nearest-tri race about
// half the time and bleeds its (usually default/base) color into cells
// on the visible surface. No search-radius tuning can fix that: at
// those gaps the hidden and visible surfaces are coincident at
// sampling scale.
//
// The fix is a per-face precompute: a face is "exterior visible" when
// at least one of a fixed set of rays cast from its centroid escapes
// the mesh without hitting anything. The sampler then prefers the
// nearest *visible* face and only falls back to hidden faces when no
// visible face is inside the search radius — so fully enclosed regions
// (a car interior behind window glass) keep sampling their own colors,
// while regions where hidden and visible surfaces compete resolve to
// the visible one.

import (
	"context"
	"math"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// visRayDirs is the fixed direction set used by FaceVisible: a
// spherical Fibonacci spiral, which spreads directions near-uniformly
// over the sphere so a handful of rays covers every escape route a
// face might have. Deterministic — visibility results are stable
// across runs and safe to cache.
var visRayDirs = sphereFibonacci(32)

func sphereFibonacci(n int) [][3]float32 {
	dirs := make([][3]float32, n)
	golden := math.Pi * (3 - math.Sqrt(5))
	for i := 0; i < n; i++ {
		z := 1 - 2*(float64(i)+0.5)/float64(n)
		r := math.Sqrt(1 - z*z)
		th := golden * float64(i)
		dirs[i] = [3]float32{
			float32(r * math.Cos(th)),
			float32(r * math.Sin(th)),
			float32(z),
		}
	}
	return dirs
}

// RayBVH is an axis-aligned bounding-volume hierarchy over a model's
// faces, built for the any-hit ray queries FaceVisible runs. Read-only
// after construction; safe for concurrent use from many goroutines.
type RayBVH struct {
	model *loader.LoadedModel
	nodes []bvhNode
	tris  []int32 // face indices referenced by leaf nodes
	eps   float32 // ray-origin offset off the face plane
}

// bvhNode is one BVH node. Leaves have count > 0 and reference
// tris[start : start+count]. Inner nodes have count == 0; their left
// child is the next node in depth-first order (nodeIdx+1) and `start`
// holds the right child's index.
type bvhNode struct {
	min, max [3]float32
	start    int32
	count    int32
}

const bvhLeafSize = 4

// bvhBuildCtxCheckEvery bounds how much work BuildRayBVH does between
// context checks, keeping it inside the pipeline's 1s-cancel contract.
const bvhBuildCtxCheckEvery = 4096

// BuildRayBVH builds a RayBVH over model's faces. Periodically checks
// ctx during construction and returns ctx.Err() once cancelled.
func BuildRayBVH(ctx context.Context, model *loader.LoadedModel) (*RayBVH, error) {
	n := len(model.Faces)
	b := &RayBVH{model: model}
	if n == 0 {
		return b, nil
	}

	// Per-face bounds and centroids, used only during the build (node
	// bounds are unioned from triMin/triMax; splits compare centroids).
	triMin := make([][3]float32, n)
	triMax := make([][3]float32, n)
	cent := make([][3]float32, n)
	var sceneMin, sceneMax [3]float32
	for k := 0; k < 3; k++ {
		sceneMin[k] = float32(math.Inf(1))
		sceneMax[k] = float32(math.Inf(-1))
	}
	for fi, f := range model.Faces {
		v0, v1, v2 := model.Vertices[f[0]], model.Vertices[f[1]], model.Vertices[f[2]]
		for k := 0; k < 3; k++ {
			lo := Minf(v0[k], Minf(v1[k], v2[k]))
			hi := Maxf(v0[k], Maxf(v1[k], v2[k]))
			triMin[fi][k] = lo
			triMax[fi][k] = hi
			cent[fi][k] = (v0[k] + v1[k] + v2[k]) / 3
			sceneMin[k] = Minf(sceneMin[k], lo)
			sceneMax[k] = Maxf(sceneMax[k], hi)
		}
	}
	maxExtent := Maxf(sceneMax[0]-sceneMin[0],
		Maxf(sceneMax[1]-sceneMin[1], sceneMax[2]-sceneMin[2]))
	// Offset ray origins a hair off the face plane so rays don't
	// immediately re-hit coplanar neighbors of the source face. Must
	// stay well below the smallest skin-to-pocket gap (~1e-5 of the
	// model extent in practice would be too tight; measured gaps are
	// ≥4e-4 of extent, and the source face itself is excluded by
	// index, so 1e-5 of extent is comfortable).
	b.eps = 1e-5 * maxExtent

	b.tris = make([]int32, n)
	for i := range b.tris {
		b.tris[i] = int32(i)
	}
	// A binary tree over n leaves of ≥1 tri has < 2n nodes.
	b.nodes = make([]bvhNode, 0, 2*n)

	work := 0
	// build emits the subtree for b.tris[lo:hi) and returns its node
	// index. Midpoint split on the longest centroid axis; falls back
	// to an even index split when all centroids land on one side
	// (coincident geometry), which keeps the tree balanced there.
	//
	// Past bvhMaxFreeDepth every split is forced to the even index
	// split, so total tree depth is bounded by bvhMaxFreeDepth +
	// log2(n) — anyHit's fixed traversal stack relies on that bound.
	const bvhMaxFreeDepth = 32
	var build func(lo, hi, depth int) (int32, error)
	build = func(lo, hi, depth int) (int32, error) {
		work++
		if work%bvhBuildCtxCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		nodeIdx := int32(len(b.nodes))
		b.nodes = append(b.nodes, bvhNode{})
		var bbMin, bbMax, cMin, cMax [3]float32
		for k := 0; k < 3; k++ {
			bbMin[k] = float32(math.Inf(1))
			bbMax[k] = float32(math.Inf(-1))
			cMin[k] = float32(math.Inf(1))
			cMax[k] = float32(math.Inf(-1))
		}
		for i := lo; i < hi; i++ {
			ti := b.tris[i]
			for k := 0; k < 3; k++ {
				bbMin[k] = Minf(bbMin[k], triMin[ti][k])
				bbMax[k] = Maxf(bbMax[k], triMax[ti][k])
				cMin[k] = Minf(cMin[k], cent[ti][k])
				cMax[k] = Maxf(cMax[k], cent[ti][k])
			}
		}
		nd := &b.nodes[nodeIdx]
		nd.min, nd.max = bbMin, bbMax
		if hi-lo <= bvhLeafSize {
			nd.start = int32(lo)
			nd.count = int32(hi - lo)
			return nodeIdx, nil
		}
		axis := 0
		if cMax[1]-cMin[1] > cMax[axis]-cMin[axis] {
			axis = 1
		}
		if cMax[2]-cMin[2] > cMax[axis]-cMin[axis] {
			axis = 2
		}
		mid := lo
		if depth < bvhMaxFreeDepth {
			split := 0.5 * (cMin[axis] + cMax[axis])
			for i := lo; i < hi; i++ {
				if cent[b.tris[i]][axis] < split {
					b.tris[mid], b.tris[i] = b.tris[i], b.tris[mid]
					mid++
				}
			}
		}
		if mid == lo || mid == hi {
			mid = (lo + hi) / 2
		}
		// Left child is emitted next (nodeIdx+1); record the right
		// child's index in start once known.
		if _, err := build(lo, mid, depth+1); err != nil {
			return 0, err
		}
		right, err := build(mid, hi, depth+1)
		if err != nil {
			return 0, err
		}
		b.nodes[nodeIdx].start = right
		return nodeIdx, nil
	}
	if _, err := build(0, n, 0); err != nil {
		return nil, err
	}
	return b, nil
}

// FaceVisible reports whether face fi can see open space: true when at
// least one of the fixed ray directions, cast from the face centroid
// (offset off the plane on the side the ray departs to), escapes the
// mesh without hitting another face. Degenerate (zero-area) faces are
// reported visible so their sampling behavior is unchanged from the
// pre-visibility code.
func (b *RayBVH) FaceVisible(fi int) bool {
	model := b.model
	f := model.Faces[fi]
	v0, v1, v2 := model.Vertices[f[0]], model.Vertices[f[1]], model.Vertices[f[2]]
	c := [3]float32{
		(v0[0] + v1[0] + v2[0]) / 3,
		(v0[1] + v1[1] + v2[1]) / 3,
		(v0[2] + v1[2] + v2[2]) / 3,
	}
	nrm := FaceNormal(fi, model)
	if nrm == ([3]float32{}) {
		return true
	}
	for _, d := range visRayDirs {
		off := b.eps
		if d[0]*nrm[0]+d[1]*nrm[1]+d[2]*nrm[2] < 0 {
			off = -off
		}
		o := [3]float32{c[0] + nrm[0]*off, c[1] + nrm[1]*off, c[2] + nrm[2]*off}
		if !b.anyHit(o, d, int32(fi)) {
			return true
		}
	}
	return false
}

// anyHit reports whether the ray from o along unit direction d hits
// any face other than skip.
func (b *RayBVH) anyHit(o, d [3]float32, skip int32) bool {
	if len(b.nodes) == 0 {
		return false
	}
	// Precompute inverse direction for the slab test, replacing zero
	// components with a huge finite value so 0*inf NaNs can't poison
	// the min/max comparisons.
	var inv [3]float32
	for k := 0; k < 3; k++ {
		dk := d[k]
		if dk > -1e-30 && dk < 1e-30 {
			if dk < 0 {
				dk = -1e-30
			} else {
				dk = 1e-30
			}
		}
		inv[k] = 1 / dk
	}
	// Stack capacity covers the build's depth bound (bvhMaxFreeDepth +
	// log2(n) forced-even levels) with margin.
	model := b.model
	var stack [96]int32
	sp := 0
	stack[sp] = 0
	sp++
	for sp > 0 {
		sp--
		nd := &b.nodes[stack[sp]]
		// Slab test against [0, +inf).
		tLo := float32(0)
		tHi := float32(math.Inf(1))
		hit := true
		for k := 0; k < 3; k++ {
			t1 := (nd.min[k] - o[k]) * inv[k]
			t2 := (nd.max[k] - o[k]) * inv[k]
			if t1 > t2 {
				t1, t2 = t2, t1
			}
			tLo = Maxf(tLo, t1)
			tHi = Minf(tHi, t2)
			if tLo > tHi {
				hit = false
				break
			}
		}
		if !hit {
			continue
		}
		if nd.count > 0 {
			for i := nd.start; i < nd.start+nd.count; i++ {
				ti := b.tris[i]
				if ti == skip {
					continue
				}
				f := model.Faces[ti]
				if rayTriHit(o, d, model.Vertices[f[0]], model.Vertices[f[1]], model.Vertices[f[2]]) {
					return true
				}
			}
			continue
		}
		// Inner node: children at nodeIdx+1 and nd.start.
		stack[sp] = stack[sp] + 1 // left (stack[sp] still holds nodeIdx)
		sp++
		stack[sp] = nd.start
		sp++
	}
	return false
}

// rayTriHit is a two-sided Möller–Trumbore any-hit test for the ray
// from o along d against triangle (v0, v1, v2), accepting hits at any
// t > 0.
func rayTriHit(o, d, v0, v1, v2 [3]float32) bool {
	e1 := [3]float32{v1[0] - v0[0], v1[1] - v0[1], v1[2] - v0[2]}
	e2 := [3]float32{v2[0] - v0[0], v2[1] - v0[1], v2[2] - v0[2]}
	px := d[1]*e2[2] - d[2]*e2[1]
	py := d[2]*e2[0] - d[0]*e2[2]
	pz := d[0]*e2[1] - d[1]*e2[0]
	det := e1[0]*px + e1[1]*py + e1[2]*pz
	if det > -1e-12 && det < 1e-12 {
		return false
	}
	invDet := 1 / det
	tx := o[0] - v0[0]
	ty := o[1] - v0[1]
	tz := o[2] - v0[2]
	u := (tx*px + ty*py + tz*pz) * invDet
	if u < 0 || u > 1 {
		return false
	}
	qx := ty*e1[2] - tz*e1[1]
	qy := tz*e1[0] - tx*e1[2]
	qz := tx*e1[1] - ty*e1[0]
	v := (d[0]*qx + d[1]*qy + d[2]*qz) * invDet
	if v < 0 || u+v > 1 {
		return false
	}
	t := (e2[0]*qx + e2[1]*qy + e2[2]*qz) * invDet
	return t > 0
}
