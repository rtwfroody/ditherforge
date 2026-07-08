package fwnrepair

import "math"

// vec3 is a float64 point/vector. All winding-number math runs in
// float64 for accuracy; the float32 model coordinates are widened on
// the way in and narrowed only when emitting output vertices.
type vec3 = [3]float64

func sub(a, b vec3) vec3 { return vec3{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func add(a, b vec3) vec3 { return vec3{a[0] + b[0], a[1] + b[1], a[2] + b[2]} }
func dot(a, b vec3) float64 {
	return a[0]*b[0] + a[1]*b[1] + a[2]*b[2]
}
func cross(a, b vec3) vec3 {
	return vec3{
		a[1]*b[2] - a[2]*b[1],
		a[2]*b[0] - a[0]*b[2],
		a[0]*b[1] - a[1]*b[0],
	}
}
func norm(a vec3) float64 { return math.Sqrt(dot(a, a)) }

// tri is one input triangle with the quantities the winding-number
// evaluator needs precomputed: the area-weighted normal Aᵢnᵢ (the
// "dipole" of the fast-winding-number expansion), its area, and its
// geometric centroid.
type tri struct {
	a, b, c  vec3
	an       vec3    // area-weighted normal = ½·(b−a)×(c−a); |an| = area
	area     float64 // = |an|
	centroid vec3    // (a+b+c)/3
}

func newTri(a, b, c vec3) tri {
	an := cross(sub(b, a), sub(c, a))
	an = vec3{an[0] * 0.5, an[1] * 0.5, an[2] * 0.5}
	return tri{
		a:        a,
		b:        b,
		c:        c,
		an:       an,
		area:     norm(an),
		centroid: vec3{(a[0] + b[0] + c[0]) / 3, (a[1] + b[1] + c[1]) / 3, (a[2] + b[2] + c[2]) / 3},
	}
}

// solidAngle returns the signed solid angle (in steradians) subtended
// by triangle t at query point q, via the Van Oosterom & Strackee
// (1983) formula. Summed over an outward-oriented closed mesh and
// divided by 4π this yields +1 inside and 0 outside. Degenerate
// (zero-area / query-on-vertex) triangles fall out to atan2(0,·) = 0
// rather than NaN.
func solidAngle(t tri, q vec3) float64 {
	av := sub(t.a, q)
	bv := sub(t.b, q)
	cv := sub(t.c, q)
	la := norm(av)
	lb := norm(bv)
	lc := norm(cv)
	num := dot(av, cross(bv, cv))
	den := la*lb*lc + dot(av, bv)*lc + dot(bv, cv)*la + dot(cv, av)*lb
	return 2 * math.Atan2(num, den)
}

// bvhNode is one node of the winding-number BVH. Internal nodes hold
// child indices (left/right ≥ 0); leaves hold a [start,start+count)
// range into the tri-index permutation. Every node caches the
// aggregate dipole p = Σ Aᵢnᵢ, the area-weighted expansion centre c,
// and a radius r bounding all its triangle vertices around c — the
// data the first-order far-field approximation needs.
type bvhNode struct {
	min, max     vec3
	left, right  int // child node indices; both −1 for a leaf
	start, count int
	p            vec3    // dipole Σ Aᵢnᵢ
	c            vec3    // area-weighted centroid
	area         float64 // Σ Aᵢ (for combining centroids up the tree)
	r            float64 // max |vertex − c| over contained triangles
}

func (n *bvhNode) leaf() bool { return n.left < 0 }

// bvh is a median-split bounding volume hierarchy over the input
// triangles, evaluating the generalized winding number via the fast
// approximation of Barill et al. (2018).
type bvh struct {
	tris  []tri
	idx   []int // permutation of tri indices, grouped per leaf
	nodes []bvhNode
	beta  float64 // far-field acceptance factor (larger = more exact, slower)
}

const (
	bvhLeafSize = 8
	bvhBeta     = 2.0
)

// buildBVH constructs the hierarchy. tris with zero area are kept
// (they simply contribute nothing) so indices stay dense.
func buildBVH(tris []tri) *bvh {
	b := &bvh{
		tris: tris,
		idx:  make([]int, len(tris)),
		beta: bvhBeta,
	}
	for i := range b.idx {
		b.idx[i] = i
	}
	if len(tris) > 0 {
		b.build(0, len(tris))
	}
	return b
}

// build recursively partitions idx[start:start+count] and returns the
// index of the created node in b.nodes.
func (b *bvh) build(start, count int) int {
	ni := len(b.nodes)
	b.nodes = append(b.nodes, bvhNode{left: -1, right: -1, start: start, count: count})

	// Bounds over triangle vertices (for the leaf/aggregate radius) and
	// over centroids (for choosing the split axis).
	var cmin, cmax vec3
	for d := 0; d < 3; d++ {
		cmin[d] = math.Inf(1)
		cmax[d] = math.Inf(-1)
	}
	for i := start; i < start+count; i++ {
		ct := b.tris[b.idx[i]].centroid
		for d := 0; d < 3; d++ {
			cmin[d] = math.Min(cmin[d], ct[d])
			cmax[d] = math.Max(cmax[d], ct[d])
		}
	}

	if count <= bvhLeafSize {
		b.finalizeLeaf(ni)
		return ni
	}

	// Split along the widest centroid axis at the median.
	axis := 0
	best := cmax[0] - cmin[0]
	for d := 1; d < 3; d++ {
		if w := cmax[d] - cmin[d]; w > best {
			best = w
			axis = d
		}
	}
	if best == 0 {
		// All centroids coincide; can't split meaningfully.
		b.finalizeLeaf(ni)
		return ni
	}
	sub := b.idx[start : start+count]
	sortByAxis(sub, b.tris, axis)
	mid := count / 2

	left := b.build(start, mid)
	right := b.build(start+mid, count-mid)
	// b.nodes may have been reallocated by the recursive appends; index
	// back into it rather than holding a pointer across the calls.
	b.nodes[ni].left = left
	b.nodes[ni].right = right
	b.finalizeInternal(ni, left, right)
	return ni
}

// finalizeLeaf computes the aggregate dipole, centroid, radius and bbox
// for a leaf node from its triangles.
func (b *bvh) finalizeLeaf(ni int) {
	n := &b.nodes[ni]
	var p, wc vec3
	var area float64
	var bmin, bmax vec3
	for d := 0; d < 3; d++ {
		bmin[d] = math.Inf(1)
		bmax[d] = math.Inf(-1)
	}
	for i := n.start; i < n.start+n.count; i++ {
		t := b.tris[b.idx[i]]
		p = add(p, t.an)
		area += t.area
		wc = add(wc, vec3{t.centroid[0] * t.area, t.centroid[1] * t.area, t.centroid[2] * t.area})
		for _, v := range [3]vec3{t.a, t.b, t.c} {
			for d := 0; d < 3; d++ {
				bmin[d] = math.Min(bmin[d], v[d])
				bmax[d] = math.Max(bmax[d], v[d])
			}
		}
	}
	n.p = p
	n.area = area
	n.min = bmin
	n.max = bmax
	if area > 0 {
		n.c = vec3{wc[0] / area, wc[1] / area, wc[2] / area}
	} else {
		n.c = vec3{(bmin[0] + bmax[0]) / 2, (bmin[1] + bmax[1]) / 2, (bmin[2] + bmax[2]) / 2}
	}
	// Radius bounds every triangle vertex around the expansion centre.
	var r float64
	for i := n.start; i < n.start+n.count; i++ {
		t := b.tris[b.idx[i]]
		for _, v := range [3]vec3{t.a, t.b, t.c} {
			if d := norm(sub(v, n.c)); d > r {
				r = d
			}
		}
	}
	n.r = r
}

// finalizeInternal combines two already-finalized children into node ni.
func (b *bvh) finalizeInternal(ni, li, ri int) {
	l := b.nodes[li]
	r := b.nodes[ri]
	n := &b.nodes[ni]
	for d := 0; d < 3; d++ {
		n.min[d] = math.Min(l.min[d], r.min[d])
		n.max[d] = math.Max(l.max[d], r.max[d])
	}
	n.p = add(l.p, r.p)
	n.area = l.area + r.area
	if n.area > 0 {
		n.c = vec3{
			(l.c[0]*l.area + r.c[0]*r.area) / n.area,
			(l.c[1]*l.area + r.c[1]*r.area) / n.area,
			(l.c[2]*l.area + r.c[2]*r.area) / n.area,
		}
	} else {
		n.c = vec3{(n.min[0] + n.max[0]) / 2, (n.min[1] + n.max[1]) / 2, (n.min[2] + n.max[2]) / 2}
	}
	// Bound the radius by the distance from the expansion centre to the
	// farthest bbox corner. Every triangle vertex lies inside the bbox,
	// so this is valid — and much tighter than the additive
	// child-centre/child-radius bound, which would push the far-field
	// threshold out and needlessly force exact evaluation of distant
	// nodes.
	n.r = radiusFromBBox(n.min, n.max, n.c)
}

// radiusFromBBox returns the distance from c to the farthest corner of
// the axis-aligned box [min,max]. Since every contained triangle vertex
// lies within the box, this bounds them all around c.
func radiusFromBBox(min, max, c vec3) float64 {
	var r2 float64
	for _, x := range [2]float64{min[0], max[0]} {
		for _, y := range [2]float64{min[1], max[1]} {
			for _, z := range [2]float64{min[2], max[2]} {
				d := sub(vec3{x, y, z}, c)
				if s := dot(d, d); s > r2 {
					r2 = s
				}
			}
		}
	}
	return math.Sqrt(r2)
}

// sortByAxis sorts a triangle-index slice by triangle centroid along
// the given axis (insertion/quick hybrid via the stdlib is overkill for
// the branch factor; a simple sort keeps the build allocation-free).
func sortByAxis(idx []int, tris []tri, axis int) {
	// Straightforward Lomuto quickselect-free sort; counts are small at
	// the split points relative to the O(n log n) build, and this keeps
	// the code dependency-free.
	insertionThreshold := 32
	if len(idx) <= insertionThreshold {
		for i := 1; i < len(idx); i++ {
			for j := i; j > 0 && tris[idx[j-1]].centroid[axis] > tris[idx[j]].centroid[axis]; j-- {
				idx[j-1], idx[j] = idx[j], idx[j-1]
			}
		}
		return
	}
	quicksortAxis(idx, tris, axis)
}

func quicksortAxis(idx []int, tris []tri, axis int) {
	if len(idx) <= 32 {
		for i := 1; i < len(idx); i++ {
			for j := i; j > 0 && tris[idx[j-1]].centroid[axis] > tris[idx[j]].centroid[axis]; j-- {
				idx[j-1], idx[j] = idx[j], idx[j-1]
			}
		}
		return
	}
	pivot := tris[idx[len(idx)/2]].centroid[axis]
	lo, hi := 0, len(idx)-1
	for lo <= hi {
		for tris[idx[lo]].centroid[axis] < pivot {
			lo++
		}
		for tris[idx[hi]].centroid[axis] > pivot {
			hi--
		}
		if lo <= hi {
			idx[lo], idx[hi] = idx[hi], idx[lo]
			lo++
			hi--
		}
	}
	if hi > 0 {
		quicksortAxis(idx[:hi+1], tris, axis)
	}
	if lo < len(idx) {
		quicksortAxis(idx[lo:], tris, axis)
	}
}

// winding evaluates the generalized winding number w(q). A node whose
// contents lie far enough from q (|q−c| > β·r) is summarised by its
// dipole; otherwise leaves are evaluated exactly and internal nodes are
// descended.
func (b *bvh) winding(q vec3) float64 {
	if len(b.nodes) == 0 {
		return 0
	}
	const fourPi = 4 * math.Pi
	var sum float64
	// Iterative traversal with a small stack to avoid recursion cost on
	// the hot per-sample path.
	var stack [128]int
	sp := 0
	stack[sp] = 0
	sp++
	for sp > 0 {
		sp--
		n := &b.nodes[stack[sp]]
		cq := sub(n.c, q)
		dist := norm(cq)
		if n.r > 0 && dist > b.beta*n.r {
			// First-order far-field dipole approximation.
			sum += dot(cq, n.p) / (dist * dist * dist)
			continue
		}
		if n.leaf() {
			for i := n.start; i < n.start+n.count; i++ {
				sum += solidAngle(b.tris[b.idx[i]], q)
			}
			continue
		}
		stack[sp] = n.left
		sp++
		stack[sp] = n.right
		sp++
	}
	return sum / fourPi
}

// windingExact evaluates w(q) by direct summation over every triangle,
// bypassing the BVH approximation. Used by tests to validate the fast
// path and as a reference.
func windingExact(tris []tri, q vec3) float64 {
	const fourPi = 4 * math.Pi
	var sum float64
	for i := range tris {
		sum += solidAngle(tris[i], q)
	}
	return sum / fourPi
}
