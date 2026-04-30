package voxel

import (
	"container/heap"
	"context"
	"math"
	"time"

	"github.com/rtwfroody/ditherforge/internal/plog"
	"github.com/rtwfroody/ditherforge/internal/progress"
)

// quadric is a symmetric 4×4 matrix stored as 10 upper-triangle elements.
// It measures the sum of squared distances from a point to a set of planes.
type quadric [10]float64

func quadricFromPlane(a, b, c, d float64) quadric {
	return quadric{
		a * a, a * b, a * c, a * d,
		b * b, b * c, b * d,
		c * c, c * d,
		d * d,
	}
}

func (q *quadric) add(r quadric) {
	for i := range q {
		q[i] += r[i]
	}
}

// eval returns v^T Q v for point (x, y, z, 1).
func (q *quadric) eval(x, y, z float64) float64 {
	return q[0]*x*x + 2*q[1]*x*y + 2*q[2]*x*z + 2*q[3]*x +
		q[4]*y*y + 2*q[5]*y*z + 2*q[6]*y +
		q[7]*z*z + 2*q[8]*z +
		q[9]
}

// optimalPos finds the position minimizing the quadric error.
// Falls back to the midpoint if the 3×3 system is singular.
func (q *quadric) optimalPos(v1, v2 [3]float32) [3]float32 {
	// Solve: [a2 ab ac; ab b2 bc; ac bc c2] x = [-ad; -bd; -cd]
	a := [3][3]float64{
		{q[0], q[1], q[2]},
		{q[1], q[4], q[5]},
		{q[2], q[5], q[7]},
	}
	rhs := [3]float64{-q[3], -q[6], -q[8]}

	det := a[0][0]*(a[1][1]*a[2][2]-a[1][2]*a[2][1]) -
		a[0][1]*(a[1][0]*a[2][2]-a[1][2]*a[2][0]) +
		a[0][2]*(a[1][0]*a[2][1]-a[1][1]*a[2][0])

	if math.Abs(det) < 1e-10 {
		// Singular — try endpoints and midpoint, pick best.
		mid := [3]float32{
			(v1[0] + v2[0]) / 2,
			(v1[1] + v2[1]) / 2,
			(v1[2] + v2[2]) / 2,
		}
		e1 := q.eval(float64(v1[0]), float64(v1[1]), float64(v1[2]))
		e2 := q.eval(float64(v2[0]), float64(v2[1]), float64(v2[2]))
		em := q.eval(float64(mid[0]), float64(mid[1]), float64(mid[2]))
		if e1 <= e2 && e1 <= em {
			return v1
		}
		if e2 <= em {
			return v2
		}
		return mid
	}

	inv := 1.0 / det
	x := ((a[1][1]*a[2][2]-a[1][2]*a[2][1])*rhs[0] +
		(a[0][2]*a[2][1]-a[0][1]*a[2][2])*rhs[1] +
		(a[0][1]*a[1][2]-a[0][2]*a[1][1])*rhs[2]) * inv
	y := ((a[1][2]*a[2][0]-a[1][0]*a[2][2])*rhs[0] +
		(a[0][0]*a[2][2]-a[0][2]*a[2][0])*rhs[1] +
		(a[0][2]*a[1][0]-a[0][0]*a[1][2])*rhs[2]) * inv
	z := ((a[1][0]*a[2][1]-a[1][1]*a[2][0])*rhs[0] +
		(a[0][1]*a[2][0]-a[0][0]*a[2][1])*rhs[1] +
		(a[0][0]*a[1][1]-a[0][1]*a[1][0])*rhs[2]) * inv

	return [3]float32{float32(x), float32(y), float32(z)}
}

type decimEdgeKey struct {
	v1, v2 uint32 // v1 < v2
}

func makeDecimEdgeKey(a, b uint32) decimEdgeKey {
	if a > b {
		a, b = b, a
	}
	return decimEdgeKey{a, b}
}

type collapseEntry struct {
	edge  decimEdgeKey
	cost  float64
	pos   [3]float32
	v1ver int
	v2ver int
	idx   int // heap index
}

type collapseHeap []*collapseEntry

func (h collapseHeap) Len() int           { return len(h) }
func (h collapseHeap) Less(i, j int) bool { return h[i].cost < h[j].cost }
func (h collapseHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].idx = i
	h[j].idx = j
}
func (h *collapseHeap) Push(x any) {
	c := x.(*collapseEntry)
	c.idx = len(*h)
	*h = append(*h, c)
}
func (h *collapseHeap) Pop() any {
	old := *h
	n := len(old)
	c := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return c
}

type decimator struct {
	verts        [][3]float32
	faces        [][3]uint32
	quadrics     []quadric
	vertFaces    [][]uint32 // vertex → face indices (may include dead faces)
	faceAlive    []bool
	vertAlive    []bool
	vertBoundary []bool // true if vertex is on a mesh boundary (open edge)
	vertVersion  []int
	activeFaces  int
	cellSize     float64 // used to scale cost by edge length
	h            collapseHeap
}

// Decimate reduces mesh face count using QEM edge collapse.
// Preserves topology (manifoldness/watertightness). Stops at targetFaces
// or when no more safe collapses exist. cellSize is used to prioritize
// collapsing edges shorter than a voxel — their cost is scaled down so
// sub-voxel detail is removed first regardless of QEM error.
//
// If tracker is non-nil, it receives periodic StageProgress("Decimating")
// updates measured in faces removed. The caller is responsible for the
// corresponding StageStart/StageDone.
func Decimate(ctx context.Context, verts [][3]float32, faces [][3]uint32, targetFaces int, cellSize float64, tracker progress.Tracker) ([][3]float32, [][3]uint32, error) {
	if len(faces) <= targetFaces {
		return verts, faces, nil
	}
	if tracker == nil {
		tracker = progress.NullTracker{}
	}

	tStart := time.Now()
	initialFaces := len(faces)

	d := &decimator{
		verts:        make([][3]float32, len(verts)),
		faces:        make([][3]uint32, len(faces)),
		quadrics:     make([]quadric, len(verts)),
		vertFaces:    make([][]uint32, len(verts)),
		faceAlive:    make([]bool, len(faces)),
		vertAlive:    make([]bool, len(verts)),
		vertBoundary: make([]bool, len(verts)),
		vertVersion:  make([]int, len(verts)),
		activeFaces:  len(faces),
		cellSize:     cellSize,
	}
	copy(d.verts, verts)
	copy(d.faces, faces)
	for i := range d.faceAlive {
		d.faceAlive[i] = true
	}
	for i := range d.vertAlive {
		d.vertAlive[i] = true
	}

	// Build vertex→face adjacency and initial quadrics.
	for fi, f := range d.faces {
		for _, vi := range f {
			d.vertFaces[vi] = append(d.vertFaces[vi], uint32(fi))
		}
		q := d.faceQuadric(uint32(fi))
		for _, vi := range f {
			d.quadrics[vi].add(q)
		}
	}

	// Mark boundary vertices (on edges with only 1 adjacent face).
	edgeFaceCount := make(map[decimEdgeKey]int)
	for _, f := range d.faces {
		for i := 0; i < 3; i++ {
			ek := makeDecimEdgeKey(f[i], f[(i+1)%3])
			edgeFaceCount[ek]++
		}
	}
	for ek, count := range edgeFaceCount {
		if count == 1 {
			d.vertBoundary[ek.v1] = true
			d.vertBoundary[ek.v2] = true
		}
	}

	// Build initial edge collapse heap.
	seen := make(map[decimEdgeKey]bool)
	for _, f := range d.faces {
		for i := 0; i < 3; i++ {
			ek := makeDecimEdgeKey(f[i], f[(i+1)%3])
			if !seen[ek] {
				seen[ek] = true
				d.pushEdge(ek)
			}
		}
	}
	heap.Init(&d.h)

	// Collapse edges.
	collapseCount := 0
	for d.activeFaces > targetFaces && d.h.Len() > 0 {
		if collapseCount%1000 == 0 {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			tracker.StageProgress("Decimating", initialFaces-d.activeFaces)
		}
		collapseCount++
		c := heap.Pop(&d.h).(*collapseEntry)
		if !d.vertAlive[c.edge.v1] || !d.vertAlive[c.edge.v2] {
			continue
		}
		if d.vertVersion[c.edge.v1] != c.v1ver || d.vertVersion[c.edge.v2] != c.v2ver {
			continue
		}
		if !d.canCollapse(c.edge, c.pos) {
			continue
		}
		d.doCollapse(c.edge, c.pos)
	}

	outVerts, outFaces := d.compact()
	plog.Printf("  Decimated %d -> %d faces in %.1fs",
		len(faces), len(outFaces), time.Since(tStart).Seconds())
	return outVerts, outFaces, nil
}

// faceQuadric returns the quadric for a face's plane.
func (d *decimator) faceQuadric(fi uint32) quadric {
	f := d.faces[fi]
	v0, v1, v2 := d.verts[f[0]], d.verts[f[1]], d.verts[f[2]]
	e1 := [3]float64{float64(v1[0] - v0[0]), float64(v1[1] - v0[1]), float64(v1[2] - v0[2])}
	e2 := [3]float64{float64(v2[0] - v0[0]), float64(v2[1] - v0[1]), float64(v2[2] - v0[2])}
	nx := e1[1]*e2[2] - e1[2]*e2[1]
	ny := e1[2]*e2[0] - e1[0]*e2[2]
	nz := e1[0]*e2[1] - e1[1]*e2[0]
	nLen := math.Sqrt(nx*nx + ny*ny + nz*nz)
	if nLen < 1e-15 {
		return quadric{}
	}
	nx /= nLen
	ny /= nLen
	nz /= nLen
	dd := -(nx*float64(v0[0]) + ny*float64(v0[1]) + nz*float64(v0[2]))
	return quadricFromPlane(nx, ny, nz, dd)
}

// pushEdge computes the collapse cost for an edge and adds it to the heap.
// Cost is scaled by min(edgeLength/cellSize, 1) so that edges shorter than
// a voxel cell are collapsed first regardless of QEM error.
func (d *decimator) pushEdge(ek decimEdgeKey) {
	var q quadric
	q = d.quadrics[ek.v1]
	q.add(d.quadrics[ek.v2])
	pos := q.optimalPos(d.verts[ek.v1], d.verts[ek.v2])
	cost := q.eval(float64(pos[0]), float64(pos[1]), float64(pos[2]))
	if cost < 0 {
		cost = 0
	}
	// Scale cost by edge length relative to cell size.
	v1, v2 := d.verts[ek.v1], d.verts[ek.v2]
	dx := float64(v2[0] - v1[0])
	dy := float64(v2[1] - v1[1])
	dz := float64(v2[2] - v1[2])
	edgeLen := math.Sqrt(dx*dx + dy*dy + dz*dz)
	// Zero-length edges get scale=0, making them free to collapse (they're
	// degenerate and collapsing them is always beneficial).
	scale := edgeLen / d.cellSize
	if scale > 1 {
		scale = 1
	}
	cost *= scale
	heap.Push(&d.h, &collapseEntry{
		edge:  ek,
		cost:  cost,
		pos:   pos,
		v1ver: d.vertVersion[ek.v1],
		v2ver: d.vertVersion[ek.v2],
	})
}

// canCollapse checks the link condition and triangle inversion.
func (d *decimator) canCollapse(ek decimEdgeKey, newPos [3]float32) bool {
	v1, v2 := ek.v1, ek.v2

	// Never collapse edges touching boundary vertices. Moving boundary
	// vertices creates or widens holes, producing visible seams.
	if d.vertBoundary[v1] || d.vertBoundary[v2] {
		return false
	}

	// Link condition: count vertices adjacent to both v1 and v2.
	// Must equal the number of faces sharing the edge.
	edgeFaces := 0
	for _, fi := range d.vertFaces[v1] {
		if d.faceAlive[fi] && d.faceHasVert(d.faces[fi], v2) {
			edgeFaces++
		}
	}
	if edgeFaces == 0 {
		return false
	}

	// Build v2's neighbor set (typically small).
	v2n := make(map[uint32]bool, 8)
	for _, fi := range d.vertFaces[v2] {
		if !d.faceAlive[fi] {
			continue
		}
		for _, u := range d.faces[fi] {
			if u != v2 {
				v2n[u] = true
			}
		}
	}
	shared := 0
	for _, fi := range d.vertFaces[v1] {
		if !d.faceAlive[fi] {
			continue
		}
		for _, u := range d.faces[fi] {
			if u != v1 && u != v2 && v2n[u] {
				v2n[u] = false // count each only once
				shared++
			}
		}
	}
	if shared > edgeFaces {
		return false
	}

	// Triangle inversion check.
	for _, fi := range d.vertFaces[v1] {
		if !d.faceAlive[fi] || d.faceHasVert(d.faces[fi], v2) {
			continue
		}
		if d.normalFlips(d.faces[fi], v1, newPos) {
			return false
		}
	}
	for _, fi := range d.vertFaces[v2] {
		if !d.faceAlive[fi] || d.faceHasVert(d.faces[fi], v1) {
			continue
		}
		if d.normalFlips(d.faces[fi], v2, newPos) {
			return false
		}
	}

	return true
}

// doCollapse merges v2 into v1 at the given position.
func (d *decimator) doCollapse(ek decimEdgeKey, newPos [3]float32) {
	v1, v2 := ek.v1, ek.v2

	// Remove faces shared by v1 and v2.
	for _, fi := range d.vertFaces[v2] {
		if !d.faceAlive[fi] {
			continue
		}
		f := d.faces[fi]
		if d.faceHasVert(f, v1) {
			d.faceAlive[fi] = false
			d.activeFaces--
		}
	}

	// In v2's remaining faces, replace v2 with v1.
	for _, fi := range d.vertFaces[v2] {
		if !d.faceAlive[fi] {
			continue
		}
		for j := 0; j < 3; j++ {
			if d.faces[fi][j] == v2 {
				d.faces[fi][j] = v1
			}
		}
		d.vertFaces[v1] = append(d.vertFaces[v1], fi)
	}
	d.vertFaces[v2] = nil // free memory

	// Periodically compact v1's face list to remove dead entries.
	if len(d.vertFaces[v1]) > 32 {
		alive := d.vertFaces[v1][:0]
		for _, fi := range d.vertFaces[v1] {
			if d.faceAlive[fi] {
				alive = append(alive, fi)
			}
		}
		d.vertFaces[v1] = alive
	}

	// Update position and quadric.
	d.verts[v1] = newPos
	d.quadrics[v1].add(d.quadrics[v2])
	d.vertAlive[v2] = false
	d.vertVersion[v1]++

	// Push new collapse costs for edges touching v1.
	edges := make(map[decimEdgeKey]bool)
	for _, fi := range d.vertFaces[v1] {
		if !d.faceAlive[fi] {
			continue
		}
		f := d.faces[fi]
		for i := 0; i < 3; i++ {
			if f[i] == v1 || f[(i+1)%3] == v1 {
				ek := makeDecimEdgeKey(f[i], f[(i+1)%3])
				if !edges[ek] {
					edges[ek] = true
					d.pushEdge(ek)
				}
			}
		}
	}
}

func (d *decimator) faceHasVert(f [3]uint32, v uint32) bool {
	return f[0] == v || f[1] == v || f[2] == v
}

// normalFlips checks if replacing oldV with newPos in face f flips the normal.
func (d *decimator) normalFlips(f [3]uint32, oldV uint32, newPos [3]float32) bool {
	var p [3][3]float32
	for i, vi := range f {
		if vi == oldV {
			p[i] = newPos
		} else {
			p[i] = d.verts[vi]
		}
	}
	// Original normal.
	o0, o1, o2 := d.verts[f[0]], d.verts[f[1]], d.verts[f[2]]
	ox := float64((o1[1]-o0[1])*(o2[2]-o0[2]) - (o1[2]-o0[2])*(o2[1]-o0[1]))
	oy := float64((o1[2]-o0[2])*(o2[0]-o0[0]) - (o1[0]-o0[0])*(o2[2]-o0[2]))
	oz := float64((o1[0]-o0[0])*(o2[1]-o0[1]) - (o1[1]-o0[1])*(o2[0]-o0[0]))
	// New normal.
	nx := float64((p[1][1]-p[0][1])*(p[2][2]-p[0][2]) - (p[1][2]-p[0][2])*(p[2][1]-p[0][1]))
	ny := float64((p[1][2]-p[0][2])*(p[2][0]-p[0][0]) - (p[1][0]-p[0][0])*(p[2][2]-p[0][2]))
	nz := float64((p[1][0]-p[0][0])*(p[2][1]-p[0][1]) - (p[1][1]-p[0][1])*(p[2][0]-p[0][0]))

	dot := ox*nx + oy*ny + oz*nz
	// Also reject if the new triangle becomes degenerate.
	newArea2 := nx*nx + ny*ny + nz*nz
	if newArea2 < 1e-20 {
		return true
	}
	return dot <= 0
}

// compact returns the surviving vertices and faces with remapped indices.
func (d *decimator) compact() ([][3]float32, [][3]uint32) {
	remap := make([]uint32, len(d.verts))
	var outVerts [][3]float32
	for i, alive := range d.vertAlive {
		if alive {
			remap[i] = uint32(len(outVerts))
			outVerts = append(outVerts, d.verts[i])
		}
	}
	var outFaces [][3]uint32
	for fi, alive := range d.faceAlive {
		if alive {
			f := d.faces[fi]
			outFaces = append(outFaces, [3]uint32{remap[f[0]], remap[f[1]], remap[f[2]]})
		}
	}
	return outVerts, outFaces
}
