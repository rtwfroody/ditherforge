// Color warp using Gaussian RBF interpolation with hard cutoff in CIELAB space.
// The frontend has a real-time GLSL preview that must match this logic.
// If you change the RBF kernel, Lab conversion, or sigma scaling here,
// update the JS/GLSL mirror in frontend/src/lib/components/ModelViewer.svelte.
package voxel

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"sort"
	"sync"

	colorful "github.com/lucasb-eyer/go-colorful"
)

// ColorWarpPin maps a source color to a target color in Lab space.
type ColorWarpPin struct {
	Source [3]uint8
	Target [3]uint8
	Sigma  float64 // falloff in standard delta-E units; 0 = auto
}

// rbfSystem holds precomputed RBF weights for color warping.
type rbfSystem struct {
	sourceLab  [][3]float64 // pin source colors in Lab
	weights    [][3]float64 // solved weights per pin (L, a, b)
	sigmas     []float64    // per-pin scaled sigma (go-colorful scale)
}

// rbfKernel evaluates a Gaussian with hard cutoff at the reach radius.
// phi(r) = exp(-4.5 * r²) for r < 1, else 0, where r = dist/sigma.
// This is equivalent to a Gaussian with effective sigma = reach/3,
// hard-clipped at 3 sigma (~1% value — invisible discontinuity).
func rbfKernel(distSq, sigma float64) float64 {
	rSq := distSq / (sigma * sigma)
	if rSq >= 1 {
		return 0
	}
	return math.Exp(-4.5 * rSq)
}

// solveRBF builds the RBF interpolation system with per-pin sigmas.
// Sources, deltas, and sigmas are in go-colorful Lab scale (standard CIELAB / 100).
// Sigma acts as a hard cutoff radius: colors beyond sigma distance are unaffected.
func solveRBF(sources [][3]float64, deltas [][3]float64, sigmas []float64) (*rbfSystem, error) {
	n := len(sources)
	if n == 0 {
		return nil, fmt.Errorf("no warp pins")
	}
	if n != len(deltas) || n != len(sigmas) {
		return nil, fmt.Errorf("sources/deltas/sigmas length mismatch")
	}

	// Build the n×n Phi matrix.
	// Each basis function j uses its own sigma, so the matrix is NOT symmetric
	// when pins have different sigmas. Gaussian elimination handles it.
	phi := make([][]float64, n)
	for i := range n {
		phi[i] = make([]float64, n)
		for j := range n {
			dL := sources[i][0] - sources[j][0]
			da := sources[i][1] - sources[j][1]
			db := sources[i][2] - sources[j][2]
			distSq := dL*dL + da*da + db*db
			phi[i][j] = rbfKernel(distSq, sigmas[j])
		}
	}

	// Solve Phi * W = D for each of L, a, b channels using Gaussian elimination.
	weights := make([][3]float64, n)
	for ch := range 3 {
		rhs := make([]float64, n)
		for i := range n {
			rhs[i] = deltas[i][ch]
		}
		w, err := gaussElim(phi, rhs)
		if err != nil {
			return nil, fmt.Errorf("RBF solve channel %d: %w", ch, err)
		}
		for i := range n {
			weights[i][ch] = w[i]
		}
	}

	return &rbfSystem{
		sourceLab: sources,
		weights:   weights,
		sigmas:    sigmas,
	}, nil
}

// eval computes the warped Lab color for a given input Lab color.
func (sys *rbfSystem) eval(L, a, b float64) (float64, float64, float64) {
	var sumL, sumA, sumB float64
	for i, src := range sys.sourceLab {
		dL := L - src[0]
		dA := a - src[1]
		dB := b - src[2]
		distSq := dL*dL + dA*dA + dB*dB
		phi := rbfKernel(distSq, sys.sigmas[i])
		sumL += sys.weights[i][0] * phi
		sumA += sys.weights[i][1] * phi
		sumB += sys.weights[i][2] * phi
	}
	return L + sumL, a + sumA, b + sumB
}

// gaussElim solves Ax = b using Gaussian elimination with partial pivoting.
// The input matrix A is copied internally. Returns the solution vector.
func gaussElim(A [][]float64, b []float64) ([]float64, error) {
	n := len(b)
	// Augmented matrix [A|b].
	aug := make([][]float64, n)
	for i := range n {
		aug[i] = make([]float64, n+1)
		copy(aug[i], A[i])
		aug[i][n] = b[i]
	}

	for col := range n {
		// Partial pivoting.
		maxRow := col
		maxVal := math.Abs(aug[col][col])
		for row := col + 1; row < n; row++ {
			if v := math.Abs(aug[row][col]); v > maxVal {
				maxVal = v
				maxRow = row
			}
		}
		if maxVal < 1e-12 {
			return nil, fmt.Errorf("singular matrix at column %d", col)
		}
		aug[col], aug[maxRow] = aug[maxRow], aug[col]

		// Eliminate below.
		pivot := aug[col][col]
		for row := col + 1; row < n; row++ {
			factor := aug[row][col] / pivot
			for j := col; j <= n; j++ {
				aug[row][j] -= factor * aug[col][j]
			}
		}
	}

	// Back-substitute.
	x := make([]float64, n)
	for i := n - 1; i >= 0; i-- {
		x[i] = aug[i][n]
		for j := i + 1; j < n; j++ {
			x[i] -= aug[i][j] * x[j]
		}
		x[i] /= aug[i][i]
	}
	return x, nil
}

// autoSigma computes a default sigma from pairwise distances between source
// colors. Uses median pairwise distance, or a fixed default for a single pin.
func autoSigma(sources [][3]float64) float64 {
	if len(sources) <= 1 {
		// Single pin: use 30 standard delta-E units = 0.3 in go-colorful scale.
		return 0.3
	}
	var dists []float64
	for i := range sources {
		for j := i + 1; j < len(sources); j++ {
			dL := sources[i][0] - sources[j][0]
			da := sources[i][1] - sources[j][1]
			db := sources[i][2] - sources[j][2]
			dists = append(dists, math.Sqrt(dL*dL+da*da+db*db))
		}
	}
	sort.Float64s(dists)
	median := dists[len(dists)/2]
	if median < 0.05 {
		median = 0.05 // floor to avoid near-zero sigma
	}
	return median / 2
}

// rgbToLab converts an RGB triplet to go-colorful Lab values.
func rgbToLab(rgb [3]uint8) (float64, float64, float64) {
	c := colorful.Color{
		R: float64(rgb[0]) / 255.0,
		G: float64(rgb[1]) / 255.0,
		B: float64(rgb[2]) / 255.0,
	}
	return c.Lab()
}

// labToRGB converts go-colorful Lab values to a clamped RGB triplet.
func labToRGB(L, a, b float64) [3]uint8 {
	c := colorful.Lab(L, a, b).Clamped()
	return [3]uint8{
		uint8(math.Round(c.R * 255)),
		uint8(math.Round(c.G * 255)),
		uint8(math.Round(c.B * 255)),
	}
}

// WarpCellColors applies Gaussian RBF color warping to a slice of cells.
// Each pin maps a source color to a target color; the warp is exact at pin
// colors and decays smoothly with distance in CIELAB space.
// Each pin's Sigma controls its falloff in standard delta-E units; 0 means
// auto-compute from median pairwise distance between pin sources.
// Returns a new slice; the input is not modified.
func WarpCellColors(ctx context.Context, cells []ActiveCell, pins []ColorWarpPin) ([]ActiveCell, error) {
	if len(pins) == 0 {
		out := make([]ActiveCell, len(cells))
		copy(out, cells)
		return out, nil
	}

	// Convert pins to Lab.
	// go-colorful uses Lab values scaled by 1/100 relative to standard CIELAB.
	sources := make([][3]float64, len(pins))
	deltas := make([][3]float64, len(pins))
	for i, p := range pins {
		sL, sa, sb := rgbToLab(p.Source)
		tL, ta, tb := rgbToLab(p.Target)
		sources[i] = [3]float64{sL, sa, sb}
		deltas[i] = [3]float64{tL - sL, ta - sa, tb - sb}
	}

	// Build per-pin sigmas. Use auto for any pin with Sigma == 0.
	defaultSigma := autoSigma(sources)
	sigmas := make([]float64, len(pins))
	for i, p := range pins {
		if p.Sigma > 0 {
			sigmas[i] = p.Sigma / 100.0 // scale from standard delta-E to go-colorful
		} else {
			sigmas[i] = defaultSigma
		}
	}

	sys, err := solveRBF(sources, deltas, sigmas)
	if err != nil {
		return nil, fmt.Errorf("color warp: %w", err)
	}

	// Apply warp in parallel.
	out := make([]ActiveCell, len(cells))
	n := len(cells)
	numWorkers := runtime.NumCPU()
	if numWorkers > n {
		numWorkers = n
	}
	if numWorkers < 1 {
		numWorkers = 1
	}
	chunkSize := (n + numWorkers - 1) / numWorkers

	var wg sync.WaitGroup
	for w := range numWorkers {
		lo := w * chunkSize
		hi := lo + chunkSize
		if hi > n {
			hi = n
		}
		if lo >= hi {
			continue
		}
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			for i := start; i < end; i++ {
				if (i-start)%10000 == 0 && ctx.Err() != nil {
					return
				}
				out[i] = cells[i]
				L, a, b := rgbToLab(cells[i].Color)
				wL, wa, wb := sys.eval(L, a, b)
				out[i].Color = labToRGB(wL, wa, wb)
			}
		}(lo, hi)
	}
	wg.Wait()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out, nil
}
