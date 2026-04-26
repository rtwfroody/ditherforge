package voxel

import (
	"fmt"
	"math"

	sparse "github.com/james-bowman/sparse"
	"gonum.org/v1/gonum/mat"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// SolveLSCM computes a least-squares conformal parameterization (Lévy 2002)
// of the given triangle set, with two vertices pinned to fixed UV coords.
//
// Returns a per-vertex UV map (snapped 3D position → 2D UV) covering every
// vertex appearing in any triangle of triSet. The two pinned vertices keep
// their input UVs exactly.
//
// cgResidual is the final CG residual (relative to ‖b‖); a value much
// larger than the internal CG tolerance (1e-6) indicates the solver did
// not converge and the returned UVs may be visibly distorted. Callers
// should surface this to the user when above ~1e-5.
//
// LSCM minimizes ‖∂u/∂x − ∂v/∂y‖² + ‖∂u/∂y + ∂v/∂x‖² (the Cauchy-Riemann
// residual, integrated by triangle area), which is the L² conformal energy.
// On developable patches (zero Gaussian curvature, e.g. cylinders, cones)
// the minimum is the isometric unfold; on K≠0 surfaces it produces the
// minimum-angle-distortion map for the given pin constraints.
//
// Two pinned vertices remove the 4-dim conformal null space (translation,
// rotation, uniform scale) exactly, leaving a positive-definite system.
func SolveLSCM(
	model *loader.LoadedModel,
	triSet []int32,
	pinVert0, pinVert1 [3]float32, // snapped 3D positions
	pinUV0, pinUV1 [2]float32,
) (vertUV map[[3]float32][2]float32, cgResidual float64, err error) {
	if len(triSet) == 0 {
		return nil, 0, fmt.Errorf("LSCM: empty triangle set")
	}

	// 1. Build a compact decal-local vertex numbering, snapped.
	vertIdx := make(map[[3]float32]int)
	var vertPos [][3]float32
	addVert := func(p [3]float32) int {
		s := SnapPos(p)
		if i, ok := vertIdx[s]; ok {
			return i
		}
		i := len(vertPos)
		vertIdx[s] = i
		vertPos = append(vertPos, s)
		return i
	}

	type triRec struct {
		v          [3]int     // decal vertex indices
		refX       [3]float64 // 2D x in isometric reference frame
		refY       [3]float64 // 2D y
		twiceArea  float64
	}
	tris := make([]triRec, 0, len(triSet))
	for _, ti := range triSet {
		f := model.Faces[ti]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]
		i0 := addVert(v0)
		i1 := addVert(v1)
		i2 := addVert(v2)

		// Build isometric 2D ref frame: v0=(0,0), v1 along +x, v2 above x-axis.
		e01 := [3]float64{
			float64(v1[0] - v0[0]),
			float64(v1[1] - v0[1]),
			float64(v1[2] - v0[2]),
		}
		e02 := [3]float64{
			float64(v2[0] - v0[0]),
			float64(v2[1] - v0[1]),
			float64(v2[2] - v0[2]),
		}
		l01sq := e01[0]*e01[0] + e01[1]*e01[1] + e01[2]*e01[2]
		l01 := math.Sqrt(l01sq)
		if l01 < 1e-12 {
			continue // degenerate
		}
		dot := e01[0]*e02[0] + e01[1]*e02[1] + e01[2]*e02[2]
		x2 := dot / l01
		l02sq := e02[0]*e02[0] + e02[1]*e02[1] + e02[2]*e02[2]
		y2sq := l02sq - x2*x2
		if y2sq < 0 {
			y2sq = 0
		}
		y2 := math.Sqrt(y2sq)
		twiceA := l01 * y2 // 2 * triangle area in the ref frame
		if twiceA < 1e-8 {
			continue // degenerate
		}
		tris = append(tris, triRec{
			v:         [3]int{i0, i1, i2},
			refX:      [3]float64{0, l01, x2},
			refY:      [3]float64{0, 0, y2},
			twiceArea: twiceA,
		})
	}

	if len(tris) == 0 {
		return nil, 0, fmt.Errorf("LSCM: all triangles degenerate")
	}

	nV := len(vertPos)
	pin0Snap := SnapPos(pinVert0)
	pin1Snap := SnapPos(pinVert1)
	pin0Idx, ok0 := vertIdx[pin0Snap]
	pin1Idx, ok1 := vertIdx[pin1Snap]
	if !ok0 || !ok1 {
		return nil, 0, fmt.Errorf("LSCM: pinned vertex not in triangle set")
	}
	if pin0Idx == pin1Idx {
		return nil, 0, fmt.Errorf("LSCM: two pins resolved to the same vertex")
	}

	// 2. Build M^T M as a DOK accumulator. Dim = 2*nV (u,v interleaved per
	// vertex). Per triangle T, contribution to (M^T M)_{(2a+α, 2b+β)} for
	// vertices a,b ∈ T:
	//   (u_a, u_b): A * (wx_a wx_b + wy_a wy_b)
	//   (v_a, v_b): same
	//   (u_a, v_b): A * (wy_a wx_b - wx_a wy_b)
	//   (v_a, u_b): A * (wx_a wy_b - wy_a wx_b)
	// where wx_i = (y_{i+1} - y_{i+2}) / (2A), wy_i = (x_{i+2} - x_{i+1}) / (2A).
	dim := 2 * nV
	dok := sparse.NewDOK(dim, dim)
	addEntry := func(i, j int, v float64) {
		dok.Set(i, j, dok.At(i, j)+v)
	}

	for _, T := range tris {
		twiceA := T.twiceArea
		A := 0.5 * twiceA
		invTwiceA := 1.0 / twiceA
		wx := [3]float64{
			(T.refY[1] - T.refY[2]) * invTwiceA,
			(T.refY[2] - T.refY[0]) * invTwiceA,
			(T.refY[0] - T.refY[1]) * invTwiceA,
		}
		wy := [3]float64{
			(T.refX[2] - T.refX[1]) * invTwiceA,
			(T.refX[0] - T.refX[2]) * invTwiceA,
			(T.refX[1] - T.refX[0]) * invTwiceA,
		}
		for a := 0; a < 3; a++ {
			for b := 0; b < 3; b++ {
				ia := T.v[a]
				ib := T.v[b]
				uu := A * (wx[a]*wx[b] + wy[a]*wy[b])
				uv := A * (wy[a]*wx[b] - wx[a]*wy[b])
				vu := A * (wx[a]*wy[b] - wy[a]*wx[b])
				addEntry(2*ia, 2*ib, uu)
				addEntry(2*ia+1, 2*ib+1, uu)
				if uv != 0 {
					addEntry(2*ia, 2*ib+1, uv)
				}
				if vu != 0 {
					addEntry(2*ia+1, 2*ib, vu)
				}
			}
		}
	}

	// 3. Pin two vertices: build a free-only system A_ff x_f = -A_fp x_p.
	// We construct A_ff and the RHS by walking dok.DoNonZero and routing
	// each entry to either the free block or the pin contribution.
	pinned := map[int]bool{pin0Idx: true, pin1Idx: true}
	// Map original vertex index → free-block index (-1 if pinned).
	freeIdx := make([]int, nV)
	nFree := 0
	for i := 0; i < nV; i++ {
		if pinned[i] {
			freeIdx[i] = -1
		} else {
			freeIdx[i] = nFree
			nFree++
		}
	}
	if nFree == 0 {
		// Trivial: only the two pinned vertices exist.
		out := map[[3]float32][2]float32{}
		out[pin0Snap] = pinUV0
		out[pin1Snap] = pinUV1
		return out, 0, nil
	}

	freeDim := 2 * nFree
	aff := sparse.NewDOK(freeDim, freeDim)
	rhs := mat.NewVecDense(freeDim, nil)

	pinValue := func(vIdx, dim int) float64 {
		if vIdx == pin0Idx {
			if dim == 0 {
				return float64(pinUV0[0])
			}
			return float64(pinUV0[1])
		}
		if dim == 0 {
			return float64(pinUV1[0])
		}
		return float64(pinUV1[1])
	}

	dok.DoNonZero(func(i, j int, v float64) {
		vi := i / 2
		di := i % 2
		vj := j / 2
		dj := j % 2
		fi := freeIdx[vi]
		fj := freeIdx[vj]
		if fi >= 0 && fj >= 0 {
			aff.Set(2*fi+di, 2*fj+dj, aff.At(2*fi+di, 2*fj+dj)+v)
		} else if fi >= 0 && fj < 0 {
			rhs.SetVec(2*fi+di, rhs.AtVec(2*fi+di)-v*pinValue(vj, dj))
		}
		// fi<0 cases contribute to pinned rows (no equations).
	})

	// 4. Solve A x = rhs via Jacobi-preconditioned CG. Iterative because
	// the system can be very large (tens of thousands of DOFs); naive
	// sparse Cholesky without AMD reordering catastrophically fills in.
	csr := aff.ToCSR()
	rhsArr := make([]float64, freeDim)
	for i := 0; i < freeDim; i++ {
		rhsArr[i] = rhs.AtVec(i)
	}
	xArr, _, cgResid := solveCG(csr, rhsArr, 1e-6, 5000)

	// 5. Assemble per-vertex UVs.
	out := make(map[[3]float32][2]float32, nV)
	for s, vi := range vertIdx {
		var uv [2]float32
		if pinned[vi] {
			if vi == pin0Idx {
				uv = pinUV0
			} else {
				uv = pinUV1
			}
		} else {
			fi := freeIdx[vi]
			uv = [2]float32{
				float32(xArr[2*fi]),
				float32(xArr[2*fi+1]),
			}
		}
		out[s] = uv
	}
	return out, cgResid, nil
}

// solveCG solves A x = b for SPD A using Jacobi-preconditioned conjugate
// gradient. Returns the solution, number of iterations, and final relative
// residual ‖r‖/‖b‖. Stops when relative residual falls below tol or iter
// exceeds maxIter.
func solveCG(A *sparse.CSR, b []float64, tol float64, maxIter int) ([]float64, int, float64) {
	n := len(b)
	x := make([]float64, n)
	r := make([]float64, n)
	z := make([]float64, n)
	p := make([]float64, n)
	Ap := make([]float64, n)

	// Diagonal preconditioner M = diag(A). Stored as 1/A_ii.
	invDiag := make([]float64, n)
	for i := 0; i < n; i++ {
		d := A.At(i, i)
		if d == 0 {
			invDiag[i] = 1
		} else {
			invDiag[i] = 1 / d
		}
	}

	// r = b - A x (x=0, so r=b). z = M^-1 r. p = z.
	bNorm := 0.0
	for i := 0; i < n; i++ {
		r[i] = b[i]
		bNorm += b[i] * b[i]
	}
	bNorm = math.Sqrt(bNorm)
	if bNorm == 0 {
		return x, 0, 0
	}
	for i := 0; i < n; i++ {
		z[i] = invDiag[i] * r[i]
		p[i] = z[i]
	}
	rDotZ := 0.0
	for i := 0; i < n; i++ {
		rDotZ += r[i] * z[i]
	}

	for iter := 0; iter < maxIter; iter++ {
		// Ap = A p (MulVecTo accumulates; clear first).
		for i := 0; i < n; i++ {
			Ap[i] = 0
		}
		A.MulVecTo(Ap, false, p)
		pAp := 0.0
		for i := 0; i < n; i++ {
			pAp += p[i] * Ap[i]
		}
		if pAp == 0 {
			return x, iter, math.Sqrt(rDotZ) / bNorm
		}
		alpha := rDotZ / pAp
		// x += α p; r -= α Ap
		rNorm := 0.0
		for i := 0; i < n; i++ {
			x[i] += alpha * p[i]
			r[i] -= alpha * Ap[i]
			rNorm += r[i] * r[i]
		}
		rNorm = math.Sqrt(rNorm)
		if rNorm/bNorm < tol {
			return x, iter + 1, rNorm / bNorm
		}
		// z = M^-1 r
		newRDotZ := 0.0
		for i := 0; i < n; i++ {
			z[i] = invDiag[i] * r[i]
			newRDotZ += r[i] * z[i]
		}
		beta := newRDotZ / rDotZ
		for i := 0; i < n; i++ {
			p[i] = z[i] + beta*p[i]
		}
		rDotZ = newRDotZ
	}
	// Returned even if not converged; caller should check the residual.
	rNorm := 0.0
	for i := 0; i < n; i++ {
		rNorm += r[i] * r[i]
	}
	return x, maxIter, math.Sqrt(rNorm) / bNorm
}
