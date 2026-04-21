package voxel

import (
	"math"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// arapRegion holds a decal region (list of 3D triangles with shared vertices)
// ready for ARAP parameterization — as-rigid-as-possible relaxation of a 2D
// UV layout that is initially seeded from a DEM-style BFS unfold.
//
// The algorithm is Liu/Zhou/Wang 2008 "A Local/Global Approach to Mesh
// Parameterization." Each iteration alternates:
//
//   - Local: for every triangle, find the 2D rotation best aligning its
//     reference isometric embedding to the current UV layout.
//   - Global: solve a sparse cotangent-Laplacian system whose RHS is built
//     from those rotated reference edges.
//
// The cotangent Laplacian L is the same across all ARAP iterations, and is
// SPD after Dirichlet pinning of the seed triangle's three vertices, so a
// Jacobi-preconditioned conjugate-gradient solver converges well.
type arapRegion struct {
	nV      int              // number of decal vertices
	vertPos [][3]float32     // decal-local index → snapped 3D position
	tris    [][3]int32       // per-tri decal-local vertex indices
	refX    [][3][2]float32  // per-tri local 2D isometric embedding of the 3D triangle
	wTri    [][3]float32     // per-tri cotangent weight for edge k (k=0 → edge (v0,v1))
	pin     []bool           // pinned (Dirichlet) vertex mask
	uv      [][2]float32     // current 2D UVs (tangent-plane coords, world units)
	lapDiag []float32        // Laplacian diagonal
	lapNbrs [][]lapEntry     // Laplacian off-diagonals (symmetric, stored both sides)
}

type lapEntry struct {
	j int32
	v float32
}

// buildArapRegion collects the decal triangles into a compact per-region
// vertex numbering, builds isometric 2D reference frames, cotangent weights,
// the Laplacian, and initial UVs from the DEM vertUV map. The seed triangle's
// three vertices are marked pinned so the final layout keeps the sticker's
// position, orientation and scale.
func buildArapRegion(
	model *loader.LoadedModel,
	acceptedTris []int32,
	vertUV map[[3]float32][2]float32,
	seedTri int32,
) *arapRegion {
	vertIdx := make(map[[3]float32]int32)
	var vertPos [][3]float32
	addVert := func(pos [3]float32) int32 {
		snap := SnapPos(pos)
		if i, ok := vertIdx[snap]; ok {
			return i
		}
		i := int32(len(vertPos))
		vertIdx[snap] = i
		vertPos = append(vertPos, snap)
		return i
	}

	region := &arapRegion{
		tris: make([][3]int32, len(acceptedTris)),
		refX: make([][3][2]float32, len(acceptedTris)),
		wTri: make([][3]float32, len(acceptedTris)),
	}

	for i, triIdx := range acceptedTris {
		f := model.Faces[triIdx]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]

		region.tris[i] = [3]int32{addVert(v0), addVert(v1), addVert(v2)}

		// 2D isometric embedding: v0=(0,0), v1=(|e01|,0), v2 chosen so
		// |e02| and |e12| match 3D lengths.
		e01 := [3]float32{v1[0] - v0[0], v1[1] - v0[1], v1[2] - v0[2]}
		e02 := [3]float32{v2[0] - v0[0], v2[1] - v0[1], v2[2] - v0[2]}
		l01sq := e01[0]*e01[0] + e01[1]*e01[1] + e01[2]*e01[2]
		l01 := float32(math.Sqrt(float64(l01sq)))
		if l01 < 1e-10 {
			continue
		}
		dot := e01[0]*e02[0] + e01[1]*e02[1] + e01[2]*e02[2]
		x2 := dot / l01
		l02sq := e02[0]*e02[0] + e02[1]*e02[1] + e02[2]*e02[2]
		y2sq := l02sq - x2*x2
		if y2sq < 0 {
			y2sq = 0
		}
		y2 := float32(math.Sqrt(float64(y2sq)))
		region.refX[i] = [3][2]float32{{0, 0}, {l01, 0}, {x2, y2}}

		// Cotangent weights at each vertex of the 2D reference triangle.
		// cot(A) = (AB·AC) / |AB×AC| etc. With A=(0,0), B=(l01,0), C=(x2,y2):
		//   area2 = l01*y2 (twice the signed area; positive by construction).
		area2 := l01 * y2
		if area2 < 1e-12 {
			continue
		}
		cotA := x2 / y2
		cotB := (l01 - x2) / y2
		cotC := (l02sq - x2*l01) / area2

		// Clamp negative cotangents (obtuse angles) to 0 so the Laplacian
		// stays positive semi-definite. Without clamping, CG can diverge on
		// meshes with many obtuse triangles and leave NaN UVs behind, which
		// the downstream bounds check treats as out-of-rectangle and rejects
		// the entire decal.
		if cotA < 0 {
			cotA = 0
		}
		if cotB < 0 {
			cotB = 0
		}
		if cotC < 0 {
			cotC = 0
		}

		// Edge k of tri (v0,v1,v2) uses the cotangent of the vertex OPPOSITE
		// that edge: edge 0 = (v0,v1) → opposite v2 → cotC, etc.
		region.wTri[i] = [3]float32{cotC, cotA, cotB}
	}

	region.nV = len(vertPos)
	region.vertPos = vertPos
	region.uv = make([][2]float32, region.nV)
	for i, pos := range vertPos {
		region.uv[i] = vertUV[pos]
	}

	region.pin = make([]bool, region.nV)
	seedFace := model.Faces[seedTri]
	for _, vi := range seedFace {
		if i, ok := vertIdx[SnapPos(model.Vertices[vi])]; ok {
			region.pin[i] = true
		}
	}

	region.lapDiag = make([]float32, region.nV)
	type key struct{ i, j int32 }
	offDiag := make(map[key]float32)
	for ti, tri := range region.tris {
		w := region.wTri[ti]
		for k := 0; k < 3; k++ {
			wk := w[k]
			if wk == 0 {
				continue
			}
			a := tri[k]
			b := tri[(k+1)%3]
			region.lapDiag[a] += wk
			region.lapDiag[b] += wk
			if a < b {
				offDiag[key{a, b}] -= wk
			} else {
				offDiag[key{b, a}] -= wk
			}
		}
	}
	region.lapNbrs = make([][]lapEntry, region.nV)
	for k, v := range offDiag {
		region.lapNbrs[k.i] = append(region.lapNbrs[k.i], lapEntry{k.j, v})
		region.lapNbrs[k.j] = append(region.lapNbrs[k.j], lapEntry{k.i, v})
	}
	return region
}

// arapLocal fits a 2D rotation per triangle that best aligns its isometric
// reference edges to the current UV-space edges, weighted by cotangents.
//
// In 2D, the optimizer has a closed form: given M = Σ w_k * x_k ⊗ e_k
// (sum over edges, where x_k is a reference edge and e_k its current UV
// image), the optimal rotation angle is atan2(M[0,1]-M[1,0], M[0,0]+M[1,1]).
func (r *arapRegion) arapLocal() [][2][2]float32 {
	rots := make([][2][2]float32, len(r.tris))
	for ti, tri := range r.tris {
		ref := r.refX[ti]
		w := r.wTri[ti]
		var m00, m01, m10, m11 float32
		for k := 0; k < 3; k++ {
			k1 := (k + 1) % 3
			refEdge := [2]float32{ref[k1][0] - ref[k][0], ref[k1][1] - ref[k][1]}
			a := tri[k]
			b := tri[k1]
			uvEdge := [2]float32{r.uv[b][0] - r.uv[a][0], r.uv[b][1] - r.uv[a][1]}
			wk := w[k]
			m00 += wk * refEdge[0] * uvEdge[0]
			m01 += wk * refEdge[0] * uvEdge[1]
			m10 += wk * refEdge[1] * uvEdge[0]
			m11 += wk * refEdge[1] * uvEdge[1]
		}
		theta := math.Atan2(float64(m01-m10), float64(m00+m11))
		c := float32(math.Cos(theta))
		s := float32(math.Sin(theta))
		rots[ti] = [2][2]float32{{c, -s}, {s, c}}
	}
	return rots
}

// arapGlobal assembles the per-coordinate RHS from the fitted rotations and
// solves L*u = b for the unpinned UVs. The pinned vertices stay at their
// initial (DEM) UVs; their contributions are moved to the RHS via standard
// Dirichlet elimination.
func (r *arapRegion) arapGlobal(rots [][2][2]float32, cgInnerIters int) {
	bx := make([]float32, r.nV)
	by := make([]float32, r.nV)
	for ti, tri := range r.tris {
		w := r.wTri[ti]
		ref := r.refX[ti]
		R := rots[ti]
		for k := 0; k < 3; k++ {
			wk := w[k]
			if wk == 0 {
				continue
			}
			k1 := (k + 1) % 3
			a := tri[k]
			b := tri[k1]
			dx := ref[k][0] - ref[k1][0]
			dy := ref[k][1] - ref[k1][1]
			rx := R[0][0]*dx + R[0][1]*dy
			ry := R[1][0]*dx + R[1][1]*dy
			bx[a] += wk * rx
			by[a] += wk * ry
			bx[b] -= wk * rx
			by[b] -= wk * ry
		}
	}

	// Compress unpinned rows/cols; fold pinned contributions into RHS.
	oldToNew := make([]int32, r.nV)
	newToOld := make([]int32, 0, r.nV)
	for i := int32(0); i < int32(r.nV); i++ {
		if r.pin[i] {
			oldToNew[i] = -1
		} else {
			oldToNew[i] = int32(len(newToOld))
			newToOld = append(newToOld, i)
		}
	}
	N := len(newToOld)
	if N == 0 {
		return
	}

	compDiag := make([]float32, N)
	compNbrs := make([][]lapEntry, N)
	compBX := make([]float32, N)
	compBY := make([]float32, N)
	for newI, oldI := range newToOld {
		compDiag[newI] = r.lapDiag[oldI]
		compBX[newI] = bx[oldI]
		compBY[newI] = by[oldI]
		for _, e := range r.lapNbrs[oldI] {
			newJ := oldToNew[e.j]
			if newJ < 0 {
				compBX[newI] -= e.v * r.uv[e.j][0]
				compBY[newI] -= e.v * r.uv[e.j][1]
			} else {
				compNbrs[newI] = append(compNbrs[newI], lapEntry{newJ, e.v})
			}
		}
	}

	x0 := make([]float32, N)
	y0 := make([]float32, N)
	for newI, oldI := range newToOld {
		x0[newI] = r.uv[oldI][0]
		y0[newI] = r.uv[oldI][1]
	}

	xSol := cgSolve(compDiag, compNbrs, compBX, x0, cgInnerIters)
	ySol := cgSolve(compDiag, compNbrs, compBY, y0, cgInnerIters)

	for newI, oldI := range newToOld {
		r.uv[oldI] = [2]float32{xSol[newI], ySol[newI]}
	}
}

// Solve runs the local/global ARAP iterations. cgInnerIters caps the inner
// CG iterations per global step; the outer loop runs arapOuterIters times.
// In practice, a dozen outer iterations reach a visually stable layout and
// CG converges well under 50 iters for a few-thousand-vertex decal. If
// onIter is non-nil it is called after each outer iteration with the
// 0-based iteration index — useful for progress reporting.
func (r *arapRegion) Solve(arapOuterIters, cgInnerIters int, onIter func(i int)) {
	for i := 0; i < arapOuterIters; i++ {
		rots := r.arapLocal()
		r.arapGlobal(rots, cgInnerIters)
		if onIter != nil {
			onIter(i)
		}
	}
}

// cgSolve solves A*x = b with A = (diag + nbrs) using Jacobi-preconditioned
// conjugate gradient. A is assumed symmetric positive-definite; for the ARAP
// pinned cotangent Laplacian that holds in practice (weights may go slightly
// negative for obtuse triangles but the full system is still positive
// definite after Dirichlet pinning). maxIter caps iterations; the loop also
// terminates early when the residual norm falls below a fixed tolerance.
func cgSolve(diag []float32, nbrs [][]lapEntry, b, x0 []float32, maxIter int) []float32 {
	N := len(diag)
	x := make([]float32, N)
	copy(x, x0)

	matvec := func(in, out []float32) {
		for i := 0; i < N; i++ {
			s := diag[i] * in[i]
			for _, e := range nbrs[i] {
				s += e.v * in[e.j]
			}
			out[i] = s
		}
	}

	r := make([]float32, N)
	matvec(x, r)
	for i := range r {
		r[i] = b[i] - r[i]
	}

	invDiag := make([]float32, N)
	for i := range invDiag {
		if diag[i] > 1e-20 {
			invDiag[i] = 1 / diag[i]
		} else {
			invDiag[i] = 1
		}
	}

	z := make([]float32, N)
	for i := range z {
		z[i] = invDiag[i] * r[i]
	}
	p := make([]float32, N)
	copy(p, z)

	var rz float32
	for i := range r {
		rz += r[i] * z[i]
	}

	ap := make([]float32, N)

	var bNorm float32
	for _, v := range b {
		bNorm += v * v
	}
	tol := bNorm * 1e-12

	for iter := 0; iter < maxIter; iter++ {
		matvec(p, ap)
		var pap float32
		for i := range p {
			pap += p[i] * ap[i]
		}
		if pap <= 0 {
			break
		}
		alpha := rz / pap
		for i := range x {
			x[i] += alpha * p[i]
			r[i] -= alpha * ap[i]
		}
		var rr float32
		for i := range r {
			rr += r[i] * r[i]
		}
		if rr < tol {
			break
		}
		for i := range z {
			z[i] = invDiag[i] * r[i]
		}
		var newRZ float32
		for i := range r {
			newRZ += r[i] * z[i]
		}
		if rz == 0 {
			break
		}
		beta := newRZ / rz
		rz = newRZ
		for i := range p {
			p[i] = z[i] + beta*p[i]
		}
	}
	return x
}

// writeBack copies the relaxed per-vertex UVs back into the vertUV map so
// callers can rebuild per-triangle UVs using the decal's existing
// vertex-position → UV lookup. If any relaxed UV is non-finite (ill-
// conditioned CG) the map is left untouched — the caller's existing DEM
// seed values remain in place as a fallback.
func (r *arapRegion) writeBack(vertUV map[[3]float32][2]float32) {
	for _, u := range r.uv {
		if !isFinite32(u[0]) || !isFinite32(u[1]) {
			return
		}
	}
	for i, pos := range r.vertPos {
		vertUV[pos] = r.uv[i]
	}
}

func isFinite32(f float32) bool {
	return !math.IsNaN(float64(f)) && !math.IsInf(float64(f), 0)
}
