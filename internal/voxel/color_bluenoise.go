package voxel

import (
	"context"
	"math"

	"github.com/rtwfroody/ditherforge/internal/progress"
)

// BlueNoiseAdaptiveTolDefault is the default per-cell projection-error
// tolerance for the BlueNoiseAdaptive dither, in 8-bit RGB Euclidean
// units. At tol=20 the algorithm prefers 2-vertex (pair) bracketing on
// uniform-ish regions — which is what fixes Riemersma's high uniform-
// region wander — and only escalates to 3- or 4-vertex simplices when
// no pair brackets the input within the tolerance. Empirically the
// best balance across our fixture set; see tests/ditherbench/RESEARCH.md.
const BlueNoiseAdaptiveTolDefault = 20.0

// BlueNoiseAdaptive picks the smallest-K simplex (K=1, 2, 3, or 4)
// whose perpendicular projection error from the cell's input is below
// tol RGB units, then dithers within that simplex via a 1D low-
// discrepancy threshold (golden-ratio sequence on the cell's tour
// position). The candidate sets are:
//
//	K=1: just the nearest palette to input.
//	K=2: best pair (smallest perpendicular distance to the pair line).
//	K=3: best triangle (smallest perpendicular distance to triangle plane).
//	K=4: full palette (clipped & renormalized barycentric).
//
// Picking the smallest K that fits gives:
//   - bounded wander: K=2 caps wander at the pair gap, K=3 at triangle
//     diameter, etc.
//   - tight bracketing: smallest K means the chosen palette is rarely
//     far from input (the K vertices each form the tightest containing
//     simplex for that cell).
//
// No error diffusion — this is a pure ordered-dither / blue-noise-
// threshold algorithm. Per-cell drift is bounded by tol; global drift
// accumulates from cells whose simplices don't bracket exactly. In
// exchange, uniform regions get K=2 picks (bounded wander), unlike
// Riemersma which can accumulate window residual large enough to push
// to far palettes.
//
// tol is the knob: smaller forces higher K (better drift, worse
// wander); larger keeps K low (better wander, more drift).
func BlueNoiseAdaptive(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, tol float64, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	K := len(pal)
	if K == 0 {
		return nil, nil
	}
	tol2 := tol * tol

	type pair struct{ i, j int }
	var pairs []pair
	for i := 0; i < K; i++ {
		for j := i + 1; j < K; j++ {
			pairs = append(pairs, pair{i, j})
		}
	}
	type triangle struct{ i, j, k int }
	var triangles []triangle
	for i := 0; i < K; i++ {
		for j := i + 1; j < K; j++ {
			for k := j + 1; k < K; k++ {
				triangles = append(triangles, triangle{i, j, k})
			}
		}
	}

	tour := buildRiemersmaTour(cells, neighbors)
	tourPos := make([]int, n)
	for ti, idx := range tour {
		tourPos[idx] = ti
	}

	assigns := make([]int32, n)
	const golden = 0.61803398875
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}
		iR := float64(cells[idx].Color[0])
		iG := float64(cells[idx].Color[1])
		iB := float64(cells[idx].Color[2])

		// K=1: nearest palette.
		var nearest int
		var nearestD2 float64 = math.MaxFloat64
		for k, p := range pal {
			dR := iR - float64(p[0])
			dG := iG - float64(p[1])
			dB := iB - float64(p[2])
			d2 := dR*dR + dG*dG + dB*dB
			if d2 < nearestD2 {
				nearestD2 = d2
				nearest = k
			}
		}

		f := float64(tourPos[idx]) * golden
		theta := f - math.Floor(f)

		if nearestD2 <= tol2 {
			assigns[idx] = int32(nearest)
			continue
		}

		// K=2: best pair.
		var bestPair pair
		var bestPairAlpha float64
		var bestPairErr float64 = math.MaxFloat64
		for _, pp := range pairs {
			vR := float64(pal[pp.j][0]) - float64(pal[pp.i][0])
			vG := float64(pal[pp.j][1]) - float64(pal[pp.i][1])
			vB := float64(pal[pp.j][2]) - float64(pal[pp.i][2])
			vSq := vR*vR + vG*vG + vB*vB
			if vSq == 0 {
				continue
			}
			dR := iR - float64(pal[pp.i][0])
			dG := iG - float64(pal[pp.i][1])
			dB := iB - float64(pal[pp.i][2])
			t := (dR*vR + dG*vG + dB*vB) / vSq
			clipped := t
			if clipped < 0 {
				clipped = 0
			}
			if clipped > 1 {
				clipped = 1
			}
			projR := float64(pal[pp.i][0]) + clipped*vR
			projG := float64(pal[pp.i][1]) + clipped*vG
			projB := float64(pal[pp.i][2]) + clipped*vB
			eR := iR - projR
			eG := iG - projG
			eB := iB - projB
			errSq := eR*eR + eG*eG + eB*eB
			if errSq < bestPairErr {
				bestPairErr = errSq
				bestPair = pp
				bestPairAlpha = clipped
			}
		}
		if bestPairErr <= tol2 {
			var pick int
			if theta < bestPairAlpha {
				pick = bestPair.j
			} else {
				pick = bestPair.i
			}
			assigns[idx] = int32(pick)
			continue
		}

		// K=3: best triangle.
		var bestTri triangle
		var bestTriW [3]float64
		var bestTriErr float64 = math.MaxFloat64
		for _, tri := range triangles {
			ax := float64(pal[tri.i][0]) - float64(pal[tri.k][0])
			ay := float64(pal[tri.i][1]) - float64(pal[tri.k][1])
			az := float64(pal[tri.i][2]) - float64(pal[tri.k][2])
			bx := float64(pal[tri.j][0]) - float64(pal[tri.k][0])
			by := float64(pal[tri.j][1]) - float64(pal[tri.k][1])
			bz := float64(pal[tri.j][2]) - float64(pal[tri.k][2])
			tx := iR - float64(pal[tri.k][0])
			ty := iG - float64(pal[tri.k][1])
			tz := iB - float64(pal[tri.k][2])
			aa := ax*ax + ay*ay + az*az
			ab := ax*bx + ay*by + az*bz
			bb := bx*bx + by*by + bz*bz
			at := ax*tx + ay*ty + az*tz
			bt := bx*tx + by*ty + bz*tz
			det := aa*bb - ab*ab
			if det == 0 {
				continue
			}
			wi := (bb*at - ab*bt) / det
			wj := (aa*bt - ab*at) / det
			wk := 1 - wi - wj
			w := [3]float64{wi, wj, wk}
			for q := 0; q < 3; q++ {
				if w[q] < 0 {
					w[q] = 0
				}
			}
			s := w[0] + w[1] + w[2]
			if s <= 0 {
				continue
			}
			w[0] /= s
			w[1] /= s
			w[2] /= s
			pR := w[0]*float64(pal[tri.i][0]) + w[1]*float64(pal[tri.j][0]) + w[2]*float64(pal[tri.k][0])
			pG := w[0]*float64(pal[tri.i][1]) + w[1]*float64(pal[tri.j][1]) + w[2]*float64(pal[tri.k][1])
			pB := w[0]*float64(pal[tri.i][2]) + w[1]*float64(pal[tri.j][2]) + w[2]*float64(pal[tri.k][2])
			eR := iR - pR
			eG := iG - pG
			eB := iB - pB
			errSq := eR*eR + eG*eG + eB*eB
			if errSq < bestTriErr {
				bestTriErr = errSq
				bestTri = tri
				bestTriW = w
			}
		}
		if bestTriErr <= tol2 || K < 4 {
			var pick int
			switch {
			case theta < bestTriW[0]:
				pick = bestTri.i
			case theta < bestTriW[0]+bestTriW[1]:
				pick = bestTri.j
			default:
				pick = bestTri.k
			}
			assigns[idx] = int32(pick)
			continue
		}

		// K=4: full simplex (4 palettes spanning 3D RGB exactly).
		w := make([]float64, K)
		blueNoiseSimplexBarycentric(pal, iR, iG, iB, w)
		blueNoiseClipAndRenormalize(w)
		var cum float64
		pick := 0
		for j := 0; j < K; j++ {
			cum += w[j]
			if theta < cum {
				pick = j
				break
			}
			pick = j
		}
		assigns[idx] = int32(pick)
	}
	return assigns, nil
}

// blueNoiseSimplexBarycentric computes the min-norm weights w such that
// Σ w_i p_i = (target_R, target_G, target_B) and Σ w_i = 1. For K=4 in
// 3D this is the exact barycentric (4 unknowns, 3 equations + sum=1).
// For other K, it's a min-norm least-squares solution; weights may be
// negative for inputs outside the palette convex hull.
func blueNoiseSimplexBarycentric(pal [][3]uint8, tR, tG, tB float64, w []float64) {
	K := len(pal)
	if K == 4 {
		var M [4][4]float64
		var rhs [4]float64
		for j := 0; j < 4; j++ {
			M[0][j] = float64(pal[j][0])
			M[1][j] = float64(pal[j][1])
			M[2][j] = float64(pal[j][2])
			M[3][j] = 1
		}
		rhs[0] = tR
		rhs[1] = tG
		rhs[2] = tB
		rhs[3] = 1
		blueNoiseSolve4x4(M, rhs, w)
		return
	}
	last := K - 1
	A := make([][3]float64, last)
	for j := 0; j < last; j++ {
		A[j][0] = float64(pal[j][0]) - float64(pal[last][0])
		A[j][1] = float64(pal[j][1]) - float64(pal[last][1])
		A[j][2] = float64(pal[j][2]) - float64(pal[last][2])
	}
	b := [3]float64{tR - float64(pal[last][0]), tG - float64(pal[last][1]), tB - float64(pal[last][2])}
	M := make([][]float64, last)
	for i := 0; i < last; i++ {
		M[i] = []float64{A[i][0], A[i][1], A[i][2]}
	}
	AAt := make([][]float64, last)
	rhs := make([]float64, last)
	for i := 0; i < last; i++ {
		AAt[i] = make([]float64, last)
		for j := 0; j < last; j++ {
			AAt[i][j] = M[i][0]*M[j][0] + M[i][1]*M[j][1] + M[i][2]*M[j][2]
		}
		rhs[i] = M[i][0]*b[0] + M[i][1]*b[1] + M[i][2]*b[2]
	}
	wprime := make([]float64, last)
	blueNoiseGaussSolve(AAt, rhs, wprime)
	for i := 0; i < last; i++ {
		w[i] = wprime[i]
	}
	w[last] = 1
	for i := 0; i < last; i++ {
		w[last] -= wprime[i]
	}
}

func blueNoiseClipAndRenormalize(w []float64) {
	var sum float64
	for i := range w {
		if w[i] < 0 {
			w[i] = 0
		}
		sum += w[i]
	}
	if sum <= 0 {
		w[0] = 1
		for i := 1; i < len(w); i++ {
			w[i] = 0
		}
		return
	}
	inv := 1.0 / sum
	for i := range w {
		w[i] *= inv
	}
}

func blueNoiseSolve4x4(M [4][4]float64, rhs [4]float64, out []float64) {
	A := M
	b := rhs
	for col := 0; col < 4; col++ {
		maxRow := col
		maxVal := math.Abs(A[col][col])
		for r := col + 1; r < 4; r++ {
			if math.Abs(A[r][col]) > maxVal {
				maxVal = math.Abs(A[r][col])
				maxRow = r
			}
		}
		if maxRow != col {
			A[col], A[maxRow] = A[maxRow], A[col]
			b[col], b[maxRow] = b[maxRow], b[col]
		}
		for r := col + 1; r < 4; r++ {
			factor := A[r][col] / A[col][col]
			for c := col; c < 4; c++ {
				A[r][c] -= factor * A[col][c]
			}
			b[r] -= factor * b[col]
		}
	}
	for r := 3; r >= 0; r-- {
		s := b[r]
		for c := r + 1; c < 4; c++ {
			s -= A[r][c] * out[c]
		}
		out[r] = s / A[r][r]
	}
}

func blueNoiseGaussSolve(M [][]float64, b []float64, out []float64) {
	n := len(b)
	A := make([][]float64, n)
	rhs := make([]float64, n)
	for i := range M {
		A[i] = make([]float64, n)
		copy(A[i], M[i])
		rhs[i] = b[i]
	}
	for col := 0; col < n; col++ {
		maxRow := col
		maxVal := math.Abs(A[col][col])
		for r := col + 1; r < n; r++ {
			if math.Abs(A[r][col]) > maxVal {
				maxVal = math.Abs(A[r][col])
				maxRow = r
			}
		}
		if maxRow != col {
			A[col], A[maxRow] = A[maxRow], A[col]
			rhs[col], rhs[maxRow] = rhs[maxRow], rhs[col]
		}
		for r := col + 1; r < n; r++ {
			factor := A[r][col] / A[col][col]
			for c := col; c < n; c++ {
				A[r][c] -= factor * A[col][c]
			}
			rhs[r] -= factor * rhs[col]
		}
	}
	for r := n - 1; r >= 0; r-- {
		s := rhs[r]
		for c := r + 1; c < n; c++ {
			s -= A[r][c] * out[c]
		}
		out[r] = s / A[r][r]
	}
}
