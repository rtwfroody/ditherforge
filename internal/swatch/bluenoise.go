package swatch

import "math"

// blueNoiseRank builds a deterministic void-and-cluster blue-noise ranking over
// one section's block grid: perSection columns (X) by nrows rows (Z). The result
// rank[row][col] holds every integer in [0, N) exactly once (N = perSection*nrows).
//
// A block is filament B iff its rank is below round(coverage*N), so the LOWEST
// ranks are the first cells to turn B as coverage rises. Void-and-cluster orders
// the ranks so that thresholding at ANY coverage yields a maximally-spread
// (blue-noise) minority set — the whole point of preferring this over an ordered
// Bayer tile, which prints as a visibly regular grid.
//
// Kernel distances are PHYSICAL millimeters because blocks are anisotropic (one
// voxel cell wide in X, one print layer tall in Z): dx = Δcol*blockWidthMM,
// dz = Δrow*upperZMM. The energy kernel is an isotropic Gaussian in that physical
// space with sigma ≈ 1.5*sqrt(blockWidthMM*upperZMM). Both axes wrap toroidally
// (the per-axis delta uses the shorter of the two wrapped distances), so the
// pattern tiles seamlessly across the repeated sections and stacked rows.
//
// Determinism is a hard requirement: the same (perSection, nrows, blockWidthMM,
// upperZMM) always yields a byte-identical ranking. The seed pattern comes from
// an inlined splitmix64 (frozen sequence, independent of math/rand or Go version)
// and every argmax/argmin breaks ties by lowest linear index.
func blueNoiseRank(perSection, nrows int, blockWidthMM, upperZMM float64) [][]int {
	if perSection < 1 {
		perSection = 1
	}
	if nrows < 1 {
		nrows = 1
	}
	n := perSection * nrows
	rank := make([][]int, nrows)
	for r := range rank {
		rank[r] = make([]int, perSection)
		for c := range rank[r] {
			rank[r][c] = -1 // sentinel: unranked
		}
	}
	if n == 1 {
		rank[0][0] = 0
		return rank
	}

	// Precompute the toroidal Gaussian kernel keyed by (Δrow, Δcol). kernel[dr][dc]
	// is the energy a placed minority cell contributes to a cell offset (dr,dc)
	// away; symmetric under wrap so it can be indexed by a raw modular delta.
	sigma := 1.5 * math.Sqrt(blockWidthMM*upperZMM)
	if !(sigma > 0) {
		sigma = 1.0
	}
	inv2s2 := 1.0 / (2.0 * sigma * sigma)
	kernel := make([][]float64, nrows)
	for dr := 0; dr < nrows; dr++ {
		kernel[dr] = make([]float64, perSection)
		wdr := dr
		if nrows-dr < wdr {
			wdr = nrows - dr
		}
		dz := float64(wdr) * upperZMM
		for dc := 0; dc < perSection; dc++ {
			wdc := dc
			if perSection-dc < wdc {
				wdc = perSection - dc
			}
			dx := float64(wdc) * blockWidthMM
			kernel[dr][dc] = math.Exp(-(dx*dx + dz*dz) * inv2s2)
		}
	}

	// energy is the accumulated kernel field from all currently-placed minority
	// cells. place/clear update it incrementally in O(N).
	energy := make([]float64, n)
	on := make([]bool, n)
	stamp := func(pr, pc int, sign float64) {
		for cr := 0; cr < nrows; cr++ {
			dr := cr - pr
			if dr < 0 {
				dr += nrows
			}
			krow := kernel[dr]
			base := cr * perSection
			for cc := 0; cc < perSection; cc++ {
				dc := cc - pc
				if dc < 0 {
					dc += perSection
				}
				energy[base+cc] += sign * krow[dc]
			}
		}
	}
	place := func(idx int) {
		stamp(idx/perSection, idx%perSection, +1)
		on[idx] = true
	}
	clear := func(idx int) {
		stamp(idx/perSection, idx%perSection, -1)
		on[idx] = false
	}

	// tightestCluster: placed cell of maximum energy (densest neighbourhood).
	tightestCluster := func() int {
		best, bestE := -1, math.Inf(-1)
		for i := 0; i < n; i++ {
			if on[i] && energy[i] > bestE {
				best, bestE = i, energy[i]
			}
		}
		return best
	}
	// largestVoid: empty cell of minimum energy (emptiest neighbourhood).
	largestVoid := func() int {
		best, bestE := -1, math.Inf(1)
		for i := 0; i < n; i++ {
			if !on[i] && energy[i] < bestE {
				best, bestE = i, energy[i]
			}
		}
		return best
	}

	// Seed ~10% of cells via a frozen splitmix64 partial Fisher-Yates shuffle.
	ones := int(0.1*float64(n) + 0.5)
	if ones < 1 {
		ones = 1
	}
	if ones > n-1 {
		ones = n - 1
	}
	perm := make([]int, n)
	for i := range perm {
		perm[i] = i
	}
	state := uint64(0x1d17f0e5c0ffee11) ^ (uint64(perSection) << 32) ^ uint64(nrows)
	nextRand := func() uint64 {
		state += 0x9e3779b97f4a7c15
		z := state
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		return z ^ (z >> 31)
	}
	for i := 0; i < ones; i++ {
		j := i + int(nextRand()%uint64(n-i))
		perm[i], perm[j] = perm[j], perm[i]
		place(perm[i])
	}

	// Relax the seed pattern: repeatedly move the tightest cluster's cell into the
	// largest void until the vacated cell IS the largest void (stable), or a
	// generous iteration cap trips. The loop top always holds exactly `ones`
	// placed cells, so breaking on the cap leaves a valid prototype.
	maxSwaps := 4 * n
	for iter := 0; iter < maxSwaps; iter++ {
		tc := tightestCluster()
		clear(tc)
		lv := largestVoid()
		if lv == tc {
			place(tc) // stable: restore and stop
			break
		}
		place(lv)
	}

	// Phase 1 — rank the prototype's cells 0..ones-1. Remove tightest clusters one
	// at a time; the first (most redundant) removed takes the highest rank in this
	// range and the last (most isolated) takes rank 0, so the lowest ranks are the
	// most evenly-spread cells.
	count := ones
	for count > 0 {
		tc := tightestCluster()
		count--
		rank[tc/perSection][tc%perSection] = count
		clear(tc)
	}

	// Rebuild the RELAXED prototype for phase 2 (phase 1 emptied it). Relaxation
	// moved cells via place/clear, so the prototype is not perm[:ones]; but phase 1
	// assigned a rank (0..ones-1) to exactly the relaxed prototype's cells, leaving
	// every other cell at the -1 sentinel. So a non-negative rank marks a prototype
	// cell.
	for r := 0; r < nrows; r++ {
		for c := 0; c < perSection; c++ {
			if rank[r][c] >= 0 {
				place(r*perSection + c)
			}
		}
	}

	// Phase 2 — rank the remaining cells ones..N-1 by repeatedly filling the
	// largest void, keeping every intermediate density well-spread.
	count = ones
	for count < n {
		lv := largestVoid()
		rank[lv/perSection][lv%perSection] = count
		count++
		place(lv)
	}

	return rank
}
