package voxel

import (
	"context"
	"math"
	"sort"

	"github.com/rtwfroody/ditherforge/internal/progress"
)

// Research-only dither algorithms. Not wired into the GUI or CLI;
// only callable from the ditherbench harness while we look for an
// algorithm that scores well across all metrics.
//
// All variants here build on Riemersma's tour + sliding-error window
// because those two pieces give us:
//
//   - low maxdircorr (no axis-aligned scanline)
//   - bounded global drift (window has DC gain 1)
//
// What they vary is *how* the per-cell palette pick is constrained,
// to attack Riemersma's main weakness: high wander_ΔE on flat or
// near-flat regions, where the window accumulator pushes the target
// past the gap to a far palette entry, producing visible clumps of
// far-from-input picks.

// RiemersmaKNearest restricts each palette pick to the K nearest
// palette entries to the cell's *input* color. Within that K-subset,
// it picks whichever best cancels accumulated window error.
//
// Intuition: even when the window's residual is large, the chosen
// palette entry is one of the K closest to input — so wander is
// bounded by the diameter of the K-nearest cluster. The residual
// energy that can't be cancelled by a K-nearest pick is left in the
// window and may carry over to subsequent cells; if it accumulates
// beyond what subsequent picks can absorb, we accept a small drift.
//
// K=2 is the minimum interesting value — gives a binary mix between
// the two nearest palette entries. K=3 gives more flexibility at the
// cost of slightly larger wander bound.
func RiemersmaKNearest(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, k int, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	if k < 1 {
		k = 1
	}
	if k > len(pal) {
		k = len(pal)
	}

	L := RiemersmaWindowSize
	weights := make([]float32, L)
	var total float32
	for j := 0; j < L; j++ {
		weights[j] = float32(math.Pow(RiemersmaDecayRatio, float64(j)/float64(L-1)))
		total += weights[j]
	}
	for j := range weights {
		weights[j] /= total
	}

	tour := buildRiemersmaTour(cells, neighbors)
	window := make([][3]float32, L)
	head := 0

	assigns := make([]int32, n)
	type palDist struct {
		idx  int
		dist float32
	}
	dI := make([]palDist, len(pal))
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}

		var eR, eG, eB float32
		for j := 0; j < L; j++ {
			slot := (head + L - 1 - j) % L
			eR += weights[j] * window[slot][0]
			eG += weights[j] * window[slot][1]
			eB += weights[j] * window[slot][2]
		}

		iR := float32(cells[idx].Color[0])
		iG := float32(cells[idx].Color[1])
		iB := float32(cells[idx].Color[2])
		r := iR + eR
		g := iG + eG
		b := iB + eB

		// K nearest to input.
		for pi, p := range pal {
			drI := iR - float32(p[0])
			dgI := iG - float32(p[1])
			dbI := iB - float32(p[2])
			dI[pi] = palDist{pi, drI*drI + dgI*dgI + dbI*dbI}
		}
		sort.Slice(dI, func(a, b int) bool { return dI[a].dist < dI[b].dist })

		// Among K nearest, pick the one minimizing dist²(target, palette).
		bestIdx := dI[0].idx
		bestDist := float32(math.MaxFloat32)
		for j := 0; j < k; j++ {
			p := pal[dI[j].idx]
			drT := r - float32(p[0])
			dgT := g - float32(p[1])
			dbT := b - float32(p[2])
			dT := drT*drT + dgT*dgT + dbT*dbT
			if dT < bestDist {
				bestDist = dT
				bestIdx = dI[j].idx
			}
		}
		assigns[idx] = int32(bestIdx)

		chosen := pal[bestIdx]
		window[head][0] = r - float32(chosen[0])
		window[head][1] = g - float32(chosen[1])
		window[head][2] = b - float32(chosen[2])
		head = (head + 1) % L
	}
	return assigns, nil
}

// RiemersmaResidualClipped clips the magnitude of the window's
// accumulated residual before it's added to the cell's input. The
// clipped-off portion is dropped (becomes drift, in expectation
// small).
//
// Intuition: Riemersma's worst case is a runaway oscillation where
// each far-palette pick injects large error, which the window then
// uses to justify the next far pick. Clipping breaks the feedback
// loop: target is at most clipMag from input, so the chosen palette
// is one of the entries within input + clipMag.
//
// clipMag in 8-bit RGB units (Euclidean). Sensible default: a
// fraction of typical palette spacing.
func RiemersmaResidualClipped(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, clipMag float32, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}

	L := RiemersmaWindowSize
	weights := make([]float32, L)
	var total float32
	for j := 0; j < L; j++ {
		weights[j] = float32(math.Pow(RiemersmaDecayRatio, float64(j)/float64(L-1)))
		total += weights[j]
	}
	for j := range weights {
		weights[j] /= total
	}

	tour := buildRiemersmaTour(cells, neighbors)
	window := make([][3]float32, L)
	head := 0

	assigns := make([]int32, n)
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}

		var eR, eG, eB float32
		for j := 0; j < L; j++ {
			slot := (head + L - 1 - j) % L
			eR += weights[j] * window[slot][0]
			eG += weights[j] * window[slot][1]
			eB += weights[j] * window[slot][2]
		}
		// Clip residual magnitude.
		mag := float32(math.Sqrt(float64(eR*eR + eG*eG + eB*eB)))
		if mag > clipMag && mag > 0 {
			s := clipMag / mag
			eR *= s
			eG *= s
			eB *= s
		}

		iR := float32(cells[idx].Color[0])
		iG := float32(cells[idx].Color[1])
		iB := float32(cells[idx].Color[2])
		r := iR + eR
		g := iG + eG
		b := iB + eB

		bestIdx := 0
		bestDist := float32(math.MaxFloat32)
		for pi, p := range pal {
			drT := r - float32(p[0])
			dgT := g - float32(p[1])
			dbT := b - float32(p[2])
			d := drT*drT + dgT*dgT + dbT*dbT
			if d < bestDist {
				bestDist = d
				bestIdx = pi
			}
		}
		assigns[idx] = int32(bestIdx)

		chosen := pal[bestIdx]
		window[head][0] = r - float32(chosen[0])
		window[head][1] = g - float32(chosen[1])
		window[head][2] = b - float32(chosen[2])
		head = (head + 1) % L
	}
	return assigns, nil
}

// RiemersmaAdaptiveK is K-nearest where K is computed per-cell from
// palette geometry: include any palette p such that
// dist(input, p) ≤ ratio · nearest_dist (and at least the single
// nearest one). When the second-closest palette is well-separated
// from the nearest, K=1 (snap, no wander). When several palettes
// are similarly close, all of them are mix candidates.
//
// This is the key trick: on uniform-grey input where one near-grey
// palette dominates and the next is far, snap; on textured input
// where multiple palettes are similarly close, dither freely.
func RiemersmaAdaptiveK(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, ratio float32, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	if ratio < 1 {
		ratio = 1
	}

	L := RiemersmaWindowSize
	weights := make([]float32, L)
	var total float32
	for j := 0; j < L; j++ {
		weights[j] = float32(math.Pow(RiemersmaDecayRatio, float64(j)/float64(L-1)))
		total += weights[j]
	}
	for j := range weights {
		weights[j] /= total
	}

	tour := buildRiemersmaTour(cells, neighbors)
	window := make([][3]float32, L)
	head := 0

	assigns := make([]int32, n)
	dI := make([]float32, len(pal))
	candidates := make([]int, 0, len(pal))
	ratio2 := ratio * ratio
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}

		var eR, eG, eB float32
		for j := 0; j < L; j++ {
			slot := (head + L - 1 - j) % L
			eR += weights[j] * window[slot][0]
			eG += weights[j] * window[slot][1]
			eB += weights[j] * window[slot][2]
		}

		iR := float32(cells[idx].Color[0])
		iG := float32(cells[idx].Color[1])
		iB := float32(cells[idx].Color[2])
		r := iR + eR
		g := iG + eG
		b := iB + eB

		// Build candidate list: palettes within ratio of nearest input dist.
		var minDI float32 = math.MaxFloat32
		for pi, p := range pal {
			drI := iR - float32(p[0])
			dgI := iG - float32(p[1])
			dbI := iB - float32(p[2])
			d := drI*drI + dgI*dgI + dbI*dbI
			dI[pi] = d
			if d < minDI {
				minDI = d
			}
		}
		// Compare squared distances to ratio²·minDI.
		threshold := minDI * ratio2
		candidates = candidates[:0]
		for pi := range pal {
			if dI[pi] <= threshold {
				candidates = append(candidates, pi)
			}
		}
		if len(candidates) == 0 {
			candidates = append(candidates, 0) // shouldn't happen since nearest itself is included
		}

		// Pick best within candidates by target distance.
		bestIdx := candidates[0]
		bestDist := float32(math.MaxFloat32)
		for _, pi := range candidates {
			p := pal[pi]
			drT := r - float32(p[0])
			dgT := g - float32(p[1])
			dbT := b - float32(p[2])
			dT := drT*drT + dgT*dgT + dbT*dbT
			if dT < bestDist {
				bestDist = dT
				bestIdx = pi
			}
		}
		assigns[idx] = int32(bestIdx)

		chosen := pal[bestIdx]
		window[head][0] = r - float32(chosen[0])
		window[head][1] = g - float32(chosen[1])
		window[head][2] = b - float32(chosen[2])
		head = (head + 1) % L
	}
	return assigns, nil
}

// RiemersmaBoundedWander is K-nearest where the candidate set is all
// palettes within (nearest_dist + budget) of input — additive budget
// instead of multiplicative ratio. Useful when palettes are absolutely
// far from input (saturated colors): ratio-based selection always
// gives K=1 there, but additive may include useful candidates.
//
// budget in 8-bit RGB units (Euclidean).
func RiemersmaBoundedWander(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, budget float32, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}

	L := RiemersmaWindowSize
	weights := make([]float32, L)
	var total float32
	for j := 0; j < L; j++ {
		weights[j] = float32(math.Pow(RiemersmaDecayRatio, float64(j)/float64(L-1)))
		total += weights[j]
	}
	for j := range weights {
		weights[j] /= total
	}

	tour := buildRiemersmaTour(cells, neighbors)
	window := make([][3]float32, L)
	head := 0

	assigns := make([]int32, n)
	dI := make([]float32, len(pal))
	candidates := make([]int, 0, len(pal))
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}

		var eR, eG, eB float32
		for j := 0; j < L; j++ {
			slot := (head + L - 1 - j) % L
			eR += weights[j] * window[slot][0]
			eG += weights[j] * window[slot][1]
			eB += weights[j] * window[slot][2]
		}

		iR := float32(cells[idx].Color[0])
		iG := float32(cells[idx].Color[1])
		iB := float32(cells[idx].Color[2])
		r := iR + eR
		g := iG + eG
		b := iB + eB

		var minDI float32 = math.MaxFloat32
		for pi, p := range pal {
			drI := iR - float32(p[0])
			dgI := iG - float32(p[1])
			dbI := iB - float32(p[2])
			d := drI*drI + dgI*dgI + dbI*dbI
			dI[pi] = d
			if d < minDI {
				minDI = d
			}
		}
		nearest := float32(math.Sqrt(float64(minDI)))
		threshold := (nearest + budget) * (nearest + budget)
		candidates = candidates[:0]
		for pi := range pal {
			if dI[pi] <= threshold {
				candidates = append(candidates, pi)
			}
		}
		if len(candidates) == 0 {
			candidates = append(candidates, 0)
		}

		bestIdx := candidates[0]
		bestDist := float32(math.MaxFloat32)
		for _, pi := range candidates {
			p := pal[pi]
			drT := r - float32(p[0])
			dgT := g - float32(p[1])
			dbT := b - float32(p[2])
			dT := drT*drT + dgT*dgT + dbT*dbT
			if dT < bestDist {
				bestDist = dT
				bestIdx = pi
			}
		}
		assigns[idx] = int32(bestIdx)

		chosen := pal[bestIdx]
		window[head][0] = r - float32(chosen[0])
		window[head][1] = g - float32(chosen[1])
		window[head][2] = b - float32(chosen[2])
		head = (head + 1) % L
	}
	return assigns, nil
}

// RiemersmaKNearestAlpha combines K-nearest palette restriction
// (caps wander) with the existing α-bias (snap when input is near a
// palette). The score within the K-nearest candidate set is the same
// α-mix used by base Riemersma:
//
//   score = (1-α)·dist²(target, palette) + α·dist²(input, palette)
//
// where α = biasMax · max(0, 1 - nearestDist/biasRange).
//
// This gives K-nearest's bounded wander on flat regions where the
// window residual would otherwise force a far-palette pick, while
// also engaging α-bias to prefer the nearest palette when input is
// genuinely close to one. Should fix the regression we see in plain
// K-nearest where the per-cell choice in textured-but-near-palette
// inputs exhibits noisy wander.
func RiemersmaKNearestAlpha(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, k int, biasMax float64, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	if k < 1 {
		k = 1
	}
	if k > len(pal) {
		k = len(pal)
	}

	L := RiemersmaWindowSize
	weights := make([]float32, L)
	var total float32
	for j := 0; j < L; j++ {
		weights[j] = float32(math.Pow(RiemersmaDecayRatio, float64(j)/float64(L-1)))
		total += weights[j]
	}
	for j := range weights {
		weights[j] /= total
	}

	tour := buildRiemersmaTour(cells, neighbors)
	window := make([][3]float32, L)
	head := 0

	assigns := make([]int32, n)
	type palDist struct {
		idx  int
		dist float32
	}
	dI := make([]palDist, len(pal))
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}

		var eR, eG, eB float32
		for j := 0; j < L; j++ {
			slot := (head + L - 1 - j) % L
			eR += weights[j] * window[slot][0]
			eG += weights[j] * window[slot][1]
			eB += weights[j] * window[slot][2]
		}

		iR := float32(cells[idx].Color[0])
		iG := float32(cells[idx].Color[1])
		iB := float32(cells[idx].Color[2])
		r := iR + eR
		g := iG + eG
		b := iB + eB

		// Compute input distances and sort.
		for pi, p := range pal {
			drI := iR - float32(p[0])
			dgI := iG - float32(p[1])
			dbI := iB - float32(p[2])
			dI[pi] = palDist{pi, drI*drI + dgI*dgI + dbI*dbI}
		}
		sort.Slice(dI, func(a, b int) bool { return dI[a].dist < dI[b].dist })

		// α-bias from nearest input distance.
		nearDist := float32(math.Sqrt(float64(dI[0].dist)))
		alpha := float32(biasMax) * (1 - nearDist/RiemersmaInputBiasRange)
		if alpha < 0 {
			alpha = 0
		}
		wt := 1 - alpha
		wi := alpha

		// Pick best within K nearest.
		bestIdx := dI[0].idx
		bestDist := float32(math.MaxFloat32)
		for j := 0; j < k; j++ {
			pi := dI[j].idx
			p := pal[pi]
			drT := r - float32(p[0])
			dgT := g - float32(p[1])
			dbT := b - float32(p[2])
			dT := drT*drT + dgT*dgT + dbT*dbT
			d := wt*dT + wi*dI[j].dist
			if d < bestDist {
				bestDist = d
				bestIdx = pi
			}
		}
		assigns[idx] = int32(bestIdx)

		chosen := pal[bestIdx]
		window[head][0] = r - float32(chosen[0])
		window[head][1] = g - float32(chosen[1])
		window[head][2] = b - float32(chosen[2])
		head = (head + 1) % L
	}
	return assigns, nil
}

// RiemersmaMinKAdaptive: candidates = top-minK-nearest UNION
// (palettes within ratio · nearest_dist). Guarantees ≥ minK
// candidates always, plus extras if multiple palettes are similarly
// close to input. Picks within the candidate set by score with
// α-bias.
//
// The motivation: pure adaptive-K collapses to K=1 (snap, full
// drift) when no second palette is within ratio. minK floors that
// to a sensible level so dither still happens. Pure K-nearest can
// fail when the K-th palette doesn't bracket; adaptive expansion
// adds more candidates when geometry permits.
func RiemersmaMinKAdaptive(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, minK int, ratio float32, biasMax float64, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	if minK < 1 {
		minK = 1
	}
	if minK > len(pal) {
		minK = len(pal)
	}
	if ratio < 1 {
		ratio = 1
	}

	L := RiemersmaWindowSize
	weights := make([]float32, L)
	var total float32
	for j := 0; j < L; j++ {
		weights[j] = float32(math.Pow(RiemersmaDecayRatio, float64(j)/float64(L-1)))
		total += weights[j]
	}
	for j := range weights {
		weights[j] /= total
	}

	tour := buildRiemersmaTour(cells, neighbors)
	window := make([][3]float32, L)
	head := 0

	assigns := make([]int32, n)
	type palDist struct {
		idx  int
		dist float32
	}
	dI := make([]palDist, len(pal))
	candidates := make([]int, 0, len(pal))
	ratio2 := ratio * ratio
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}

		var eR, eG, eB float32
		for j := 0; j < L; j++ {
			slot := (head + L - 1 - j) % L
			eR += weights[j] * window[slot][0]
			eG += weights[j] * window[slot][1]
			eB += weights[j] * window[slot][2]
		}

		iR := float32(cells[idx].Color[0])
		iG := float32(cells[idx].Color[1])
		iB := float32(cells[idx].Color[2])
		r := iR + eR
		g := iG + eG
		b := iB + eB

		for pi, p := range pal {
			drI := iR - float32(p[0])
			dgI := iG - float32(p[1])
			dbI := iB - float32(p[2])
			dI[pi] = palDist{pi, drI*drI + dgI*dgI + dbI*dbI}
		}
		sort.Slice(dI, func(a, b int) bool { return dI[a].dist < dI[b].dist })

		minDI := dI[0].dist
		threshold := minDI * ratio2
		// Candidates: top-minK plus any extras within ratio.
		candidates = candidates[:0]
		for j := 0; j < len(pal); j++ {
			if j < minK || dI[j].dist <= threshold {
				candidates = append(candidates, dI[j].idx)
			} else {
				break
			}
		}

		nearDist := float32(math.Sqrt(float64(minDI)))
		alpha := float32(biasMax) * (1 - nearDist/RiemersmaInputBiasRange)
		if alpha < 0 {
			alpha = 0
		}
		wt := 1 - alpha
		wi := alpha

		bestIdx := candidates[0]
		bestDist := float32(math.MaxFloat32)
		for _, pi := range candidates {
			p := pal[pi]
			drT := r - float32(p[0])
			dgT := g - float32(p[1])
			dbT := b - float32(p[2])
			dT := drT*drT + dgT*dgT + dbT*dbT
			drI := iR - float32(p[0])
			dgI := iG - float32(p[1])
			dbI := iB - float32(p[2])
			dInput := drI*drI + dgI*dgI + dbI*dbI
			d := wt*dT + wi*dInput
			if d < bestDist {
				bestDist = d
				bestIdx = pi
			}
		}
		assigns[idx] = int32(bestIdx)

		chosen := pal[bestIdx]
		window[head][0] = r - float32(chosen[0])
		window[head][1] = g - float32(chosen[1])
		window[head][2] = b - float32(chosen[2])
		head = (head + 1) % L
	}
	return assigns, nil
}

// RiemersmaLeaky is Riemersma with per-step exponential decay applied
// to all window slots. After writing the current cell's residual, the
// entire window is multiplied by (1 - leak) so old errors fade faster
// than the natural geometric decay built into the weights.
//
// Effect: residuals can't accumulate to magnitudes large enough to
// drive far-palette picks. Trades a small drift (energy lost to leak)
// for bounded wander.
//
// leak ∈ [0, 1]. 0 = base Riemersma. Small (e.g. 0.05) gradually
// drains. Large (e.g. 0.5) is closer to FS-with-tiny-window. Watch
// drift on textured fixtures to find the sweet spot.
func RiemersmaLeaky(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, leak float32, biasMax float64, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	if leak < 0 {
		leak = 0
	}
	if leak > 1 {
		leak = 1
	}
	keep := 1 - leak

	L := RiemersmaWindowSize
	weights := make([]float32, L)
	var total float32
	for j := 0; j < L; j++ {
		weights[j] = float32(math.Pow(RiemersmaDecayRatio, float64(j)/float64(L-1)))
		total += weights[j]
	}
	for j := range weights {
		weights[j] /= total
	}

	tour := buildRiemersmaTour(cells, neighbors)
	window := make([][3]float32, L)
	head := 0

	assigns := make([]int32, n)
	dI := make([]float32, len(pal))
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}

		var eR, eG, eB float32
		for j := 0; j < L; j++ {
			slot := (head + L - 1 - j) % L
			eR += weights[j] * window[slot][0]
			eG += weights[j] * window[slot][1]
			eB += weights[j] * window[slot][2]
		}

		iR := float32(cells[idx].Color[0])
		iG := float32(cells[idx].Color[1])
		iB := float32(cells[idx].Color[2])
		r := iR + eR
		g := iG + eG
		b := iB + eB

		var minDI float32 = math.MaxFloat32
		for pi, p := range pal {
			drI := iR - float32(p[0])
			dgI := iG - float32(p[1])
			dbI := iB - float32(p[2])
			d := drI*drI + dgI*dgI + dbI*dbI
			dI[pi] = d
			if d < minDI {
				minDI = d
			}
		}
		nearDist := float32(math.Sqrt(float64(minDI)))
		alpha := float32(biasMax) * (1 - nearDist/RiemersmaInputBiasRange)
		if alpha < 0 {
			alpha = 0
		}
		wt := 1 - alpha
		wi := alpha

		bestIdx := 0
		bestDist := float32(math.MaxFloat32)
		for pi, p := range pal {
			drT := r - float32(p[0])
			dgT := g - float32(p[1])
			dbT := b - float32(p[2])
			dT := drT*drT + dgT*dgT + dbT*dbT
			d := wt*dT + wi*dI[pi]
			if d < bestDist {
				bestDist = d
				bestIdx = pi
			}
		}
		assigns[idx] = int32(bestIdx)

		chosen := pal[bestIdx]
		window[head][0] = r - float32(chosen[0])
		window[head][1] = g - float32(chosen[1])
		window[head][2] = b - float32(chosen[2])
		head = (head + 1) % L
		// Apply leak to all window slots.
		if leak > 0 {
			for j := 0; j < L; j++ {
				window[j][0] *= keep
				window[j][1] *= keep
				window[j][2] *= keep
			}
		}
	}
	return assigns, nil
}

// RiemersmaWanderPenalized scores palettes with an extra wander
// penalty β·dist²(p, p_nearest_input). This pushes the score toward
// the input-nearest palette without restricting the candidate set.
// β controls how strongly wander is penalized:
//   - β = 0: identical to base Riemersma.
//   - β large: tends toward snap-to-nearest.
//
// Because the penalty is paid even when α=0 (input far from any
// palette), this also helps on saturated-magenta-style cases.
func RiemersmaWanderPenalized(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, beta float32, biasMax float64, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}

	L := RiemersmaWindowSize
	weights := make([]float32, L)
	var total float32
	for j := 0; j < L; j++ {
		weights[j] = float32(math.Pow(RiemersmaDecayRatio, float64(j)/float64(L-1)))
		total += weights[j]
	}
	for j := range weights {
		weights[j] /= total
	}

	tour := buildRiemersmaTour(cells, neighbors)
	window := make([][3]float32, L)
	head := 0

	assigns := make([]int32, n)
	dI := make([]float32, len(pal))
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}

		var eR, eG, eB float32
		for j := 0; j < L; j++ {
			slot := (head + L - 1 - j) % L
			eR += weights[j] * window[slot][0]
			eG += weights[j] * window[slot][1]
			eB += weights[j] * window[slot][2]
		}

		iR := float32(cells[idx].Color[0])
		iG := float32(cells[idx].Color[1])
		iB := float32(cells[idx].Color[2])
		r := iR + eR
		g := iG + eG
		b := iB + eB

		var minDI float32 = math.MaxFloat32
		var nearestPI int
		for pi, p := range pal {
			drI := iR - float32(p[0])
			dgI := iG - float32(p[1])
			dbI := iB - float32(p[2])
			d := drI*drI + dgI*dgI + dbI*dbI
			dI[pi] = d
			if d < minDI {
				minDI = d
				nearestPI = pi
			}
		}
		nearDist := float32(math.Sqrt(float64(minDI)))
		alpha := float32(biasMax) * (1 - nearDist/RiemersmaInputBiasRange)
		if alpha < 0 {
			alpha = 0
		}
		wt := 1 - alpha
		wi := alpha
		nearestPal := pal[nearestPI]

		bestIdx := 0
		bestDist := float32(math.MaxFloat32)
		for pi, p := range pal {
			drT := r - float32(p[0])
			dgT := g - float32(p[1])
			dbT := b - float32(p[2])
			dT := drT*drT + dgT*dgT + dbT*dbT
			drW := float32(p[0]) - float32(nearestPal[0])
			dgW := float32(p[1]) - float32(nearestPal[1])
			dbW := float32(p[2]) - float32(nearestPal[2])
			wanderSq := drW*drW + dgW*dgW + dbW*dbW
			d := wt*dT + wi*dI[pi] + beta*wanderSq
			if d < bestDist {
				bestDist = d
				bestIdx = pi
			}
		}
		assigns[idx] = int32(bestIdx)

		chosen := pal[bestIdx]
		window[head][0] = r - float32(chosen[0])
		window[head][1] = g - float32(chosen[1])
		window[head][2] = b - float32(chosen[2])
		head = (head + 1) % L
	}
	return assigns, nil
}

// RiemersmaDynamicK adapts the candidate-set size per cell from a
// floor of minK up to the full palette, growing as the window's
// residual magnitude grows. The rationale: when the K-nearest can
// bracket input, residual stays small (window absorbs naturally and
// alternation works). When the K-nearest cannot bracket, residual
// builds up unboundedly — that's the signal to relax K and let
// other palettes in.
//
// Concretely: K = minK + floor(|residual| / step). With step ≈ a
// fraction of typical palette spacing, the algorithm starts strict
// and only opens up the candidate set when something is structurally
// preventing the residual from cancelling.
//
// Within candidates, score uses the existing α-bias + wander penalty
// to discourage far picks when input is close to a palette.
func RiemersmaDynamicK(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, minK int, step float32, biasMax float64, beta float32, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	if minK < 1 {
		minK = 1
	}
	if minK > len(pal) {
		minK = len(pal)
	}
	if step <= 0 {
		step = 30
	}

	L := RiemersmaWindowSize
	weights := make([]float32, L)
	var total float32
	for j := 0; j < L; j++ {
		weights[j] = float32(math.Pow(RiemersmaDecayRatio, float64(j)/float64(L-1)))
		total += weights[j]
	}
	for j := range weights {
		weights[j] /= total
	}

	tour := buildRiemersmaTour(cells, neighbors)
	window := make([][3]float32, L)
	head := 0

	assigns := make([]int32, n)
	type palDist struct {
		idx  int
		dist float32
	}
	dI := make([]palDist, len(pal))
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}

		var eR, eG, eB float32
		for j := 0; j < L; j++ {
			slot := (head + L - 1 - j) % L
			eR += weights[j] * window[slot][0]
			eG += weights[j] * window[slot][1]
			eB += weights[j] * window[slot][2]
		}
		residMag := float32(math.Sqrt(float64(eR*eR + eG*eG + eB*eB)))

		iR := float32(cells[idx].Color[0])
		iG := float32(cells[idx].Color[1])
		iB := float32(cells[idx].Color[2])
		r := iR + eR
		g := iG + eG
		b := iB + eB

		for pi, p := range pal {
			drI := iR - float32(p[0])
			dgI := iG - float32(p[1])
			dbI := iB - float32(p[2])
			dI[pi] = palDist{pi, drI*drI + dgI*dgI + dbI*dbI}
		}
		sort.Slice(dI, func(a, b int) bool { return dI[a].dist < dI[b].dist })

		// K = minK + (residMag / step), capped at len(pal).
		extra := int(residMag / step)
		k := minK + extra
		if k > len(pal) {
			k = len(pal)
		}

		nearDist := float32(math.Sqrt(float64(dI[0].dist)))
		alpha := float32(biasMax) * (1 - nearDist/RiemersmaInputBiasRange)
		if alpha < 0 {
			alpha = 0
		}
		wt := 1 - alpha
		wi := alpha
		nearestPal := pal[dI[0].idx]

		bestIdx := dI[0].idx
		bestDist := float32(math.MaxFloat32)
		for j := 0; j < k; j++ {
			pi := dI[j].idx
			p := pal[pi]
			drT := r - float32(p[0])
			dgT := g - float32(p[1])
			dbT := b - float32(p[2])
			dT := drT*drT + dgT*dgT + dbT*dbT
			drW := float32(p[0]) - float32(nearestPal[0])
			dgW := float32(p[1]) - float32(nearestPal[1])
			dbW := float32(p[2]) - float32(nearestPal[2])
			wanderSq := drW*drW + dgW*dgW + dbW*dbW
			d := wt*dT + wi*dI[j].dist + beta*wanderSq
			if d < bestDist {
				bestDist = d
				bestIdx = pi
			}
		}
		assigns[idx] = int32(bestIdx)

		chosen := pal[bestIdx]
		window[head][0] = r - float32(chosen[0])
		window[head][1] = g - float32(chosen[1])
		window[head][2] = b - float32(chosen[2])
		head = (head + 1) % L
	}
	return assigns, nil
}

// RiemersmaPostClampWander runs Riemersma and then in a single pass
// replaces any cell whose chosen palette is more than budget Lab ΔE
// from the cell's nearest-input palette. The replacement is the
// nearest-input palette.
//
// Trades drift for wander — far-palette picks that Riemersma uses to
// balance chroma get clipped, leaving drift unfixed in those regions
// but never producing a far-from-input palette pick. The wander cap
// is hard (per-cell), so wander_ΔE_p99 is bounded by budget.
//
// budget in Lab ΔE units (consistent with the wander metric).
func RiemersmaPostClampWander(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, budget float64, biasMax float64, tracker progress.Tracker) ([]int32, error) {
	assigns, err := Riemersma(ctx, cells, pal, neighbors, biasMax, tracker)
	if err != nil {
		return nil, err
	}
	if budget <= 0 {
		return assigns, nil
	}

	// Precompute palette Lab.
	type palLab struct {
		L, A, B float64
	}
	pl := make([]palLab, len(pal))
	for k, p := range pal {
		L_, A, B := rgbToLabApprox(float64(p[0]), float64(p[1]), float64(p[2]))
		pl[k] = palLab{L_, A, B}
	}

	for i, c := range cells {
		// Nearest-input palette in Lab.
		cL, cA, cB := rgbToLabApprox(float64(c.Color[0]), float64(c.Color[1]), float64(c.Color[2]))
		nearest := 0
		var nearestD2 float64 = math.MaxFloat64
		for k, l := range pl {
			dL := cL - l.L
			dA := cA - l.A
			dB := cB - l.B
			d2 := dL*dL + dA*dA + dB*dB
			if d2 < nearestD2 {
				nearestD2 = d2
				nearest = k
			}
		}
		chosen := int(assigns[i])
		if chosen == nearest {
			continue
		}
		dL := pl[chosen].L - pl[nearest].L
		dA := pl[chosen].A - pl[nearest].A
		dB := pl[chosen].B - pl[nearest].B
		wander := math.Sqrt(dL*dL + dA*dA + dB*dB)
		if wander > budget {
			assigns[i] = int32(nearest)
		}
	}
	return assigns, nil
}

// rgbToLabApprox converts sRGB (0..255) to Lab using a simplified
// piecewise-linear approximation suitable for local-comparison work.
// (This duplicates the bench's `toLab` to keep the voxel package
// self-contained.)
func rgbToLabApprox(r, g, b float64) (float64, float64, float64) {
	// Rough linearization.
	lin := func(c float64) float64 {
		c /= 255.0
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	rL := lin(r)
	gL := lin(g)
	bL := lin(b)
	// sRGB to XYZ (D65)
	X := rL*0.4124564 + gL*0.3575761 + bL*0.1804375
	Y := rL*0.2126729 + gL*0.7151522 + bL*0.0721750
	Z := rL*0.0193339 + gL*0.1191920 + bL*0.9503041
	// XYZ to Lab
	xn := 0.95047
	yn := 1.0
	zn := 1.08883
	f := func(t float64) float64 {
		if t > 216.0/24389.0 {
			return math.Pow(t, 1.0/3.0)
		}
		return (24389.0/27.0*t + 16) / 116
	}
	fx := f(X / xn)
	fy := f(Y / yn)
	fz := f(Z / zn)
	L := 116*fy - 16
	A := 500 * (fx - fy)
	B := 200 * (fy - fz)
	return L, A, B
}

// RiemersmaTextureAware uses a sliding window of recent input colors
// to detect "flat" regions on the tour. In flat regions (low input
// variance), the candidate set is restricted to top-flatK palettes —
// caps wander where uniformity is making large window swings produce
// far-palette picks. In textured regions (high variance), the full
// palette is allowed — matches base Riemersma's quality.
//
// inputStdThreshold in 8-bit RGB units (per-channel stddev sum, or
// vector magnitude of stddev vector). When recent-window stddev <
// threshold, treat as flat.
func RiemersmaTextureAware(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, flatK int, inputStdThreshold float32, biasMax float64, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	if flatK < 1 {
		flatK = 1
	}
	if flatK > len(pal) {
		flatK = len(pal)
	}

	L := RiemersmaWindowSize
	weights := make([]float32, L)
	var total float32
	for j := 0; j < L; j++ {
		weights[j] = float32(math.Pow(RiemersmaDecayRatio, float64(j)/float64(L-1)))
		total += weights[j]
	}
	for j := range weights {
		weights[j] /= total
	}

	tour := buildRiemersmaTour(cells, neighbors)
	window := make([][3]float32, L)
	head := 0

	// Sliding-window of recent INPUT colors (not residuals).
	const inputWindowSize = 16
	inputWin := make([][3]float32, inputWindowSize)
	inputWinHead := 0
	inputWinFilled := 0

	assigns := make([]int32, n)
	type palDist struct {
		idx  int
		dist float32
	}
	dI := make([]palDist, len(pal))
	threshold2 := inputStdThreshold * inputStdThreshold
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}

		var eR, eG, eB float32
		for j := 0; j < L; j++ {
			slot := (head + L - 1 - j) % L
			eR += weights[j] * window[slot][0]
			eG += weights[j] * window[slot][1]
			eB += weights[j] * window[slot][2]
		}

		iR := float32(cells[idx].Color[0])
		iG := float32(cells[idx].Color[1])
		iB := float32(cells[idx].Color[2])
		r := iR + eR
		g := iG + eG
		b := iB + eB

		// Compute input-window stddev.
		var stdSq float32
		if inputWinFilled >= 2 {
			var meanR, meanG, meanB float32
			for j := 0; j < inputWinFilled; j++ {
				meanR += inputWin[j][0]
				meanG += inputWin[j][1]
				meanB += inputWin[j][2]
			}
			invN := 1 / float32(inputWinFilled)
			meanR *= invN
			meanG *= invN
			meanB *= invN
			var varR, varG, varB float32
			for j := 0; j < inputWinFilled; j++ {
				dr := inputWin[j][0] - meanR
				dg := inputWin[j][1] - meanG
				db := inputWin[j][2] - meanB
				varR += dr * dr
				varG += dg * dg
				varB += db * db
			}
			stdSq = (varR + varG + varB) * invN
		}
		flat := stdSq < threshold2

		// Sort palettes by input distance.
		for pi, p := range pal {
			drI := iR - float32(p[0])
			dgI := iG - float32(p[1])
			dbI := iB - float32(p[2])
			dI[pi] = palDist{pi, drI*drI + dgI*dgI + dbI*dbI}
		}
		sort.Slice(dI, func(a, b int) bool { return dI[a].dist < dI[b].dist })

		k := len(pal)
		if flat {
			k = flatK
		}

		nearDist := float32(math.Sqrt(float64(dI[0].dist)))
		alpha := float32(biasMax) * (1 - nearDist/RiemersmaInputBiasRange)
		if alpha < 0 {
			alpha = 0
		}
		wt := 1 - alpha
		wi := alpha

		bestIdx := dI[0].idx
		bestDist := float32(math.MaxFloat32)
		for j := 0; j < k; j++ {
			pi := dI[j].idx
			p := pal[pi]
			drT := r - float32(p[0])
			dgT := g - float32(p[1])
			dbT := b - float32(p[2])
			dT := drT*drT + dgT*dgT + dbT*dbT
			d := wt*dT + wi*dI[j].dist
			if d < bestDist {
				bestDist = d
				bestIdx = pi
			}
		}
		assigns[idx] = int32(bestIdx)

		chosen := pal[bestIdx]
		window[head][0] = r - float32(chosen[0])
		window[head][1] = g - float32(chosen[1])
		window[head][2] = b - float32(chosen[2])
		head = (head + 1) % L

		// Update input window.
		inputWin[inputWinHead][0] = iR
		inputWin[inputWinHead][1] = iG
		inputWin[inputWinHead][2] = iB
		inputWinHead = (inputWinHead + 1) % inputWindowSize
		if inputWinFilled < inputWindowSize {
			inputWinFilled++
		}
	}
	return assigns, nil
}

// RiemersmaTuned is base Riemersma exposed with biasMax and biasRange
// as parameters (instead of the package constants), to let bench
// sweep them cheaply.
func RiemersmaTuned(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, biasMax float64, biasRange float32, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	L := RiemersmaWindowSize
	weights := make([]float32, L)
	var total float32
	for j := 0; j < L; j++ {
		weights[j] = float32(math.Pow(RiemersmaDecayRatio, float64(j)/float64(L-1)))
		total += weights[j]
	}
	for j := range weights {
		weights[j] /= total
	}
	tour := buildRiemersmaTour(cells, neighbors)
	window := make([][3]float32, L)
	head := 0
	assigns := make([]int32, n)
	dI := make([]float32, len(pal))
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}
		var eR, eG, eB float32
		for j := 0; j < L; j++ {
			slot := (head + L - 1 - j) % L
			eR += weights[j] * window[slot][0]
			eG += weights[j] * window[slot][1]
			eB += weights[j] * window[slot][2]
		}
		iR := float32(cells[idx].Color[0])
		iG := float32(cells[idx].Color[1])
		iB := float32(cells[idx].Color[2])
		r := iR + eR
		g := iG + eG
		b := iB + eB
		var minDI float32 = math.MaxFloat32
		for pi, p := range pal {
			drI := iR - float32(p[0])
			dgI := iG - float32(p[1])
			dbI := iB - float32(p[2])
			d := drI*drI + dgI*dgI + dbI*dbI
			dI[pi] = d
			if d < minDI {
				minDI = d
			}
		}
		nearDist := float32(math.Sqrt(float64(minDI)))
		alpha := float32(biasMax) * (1 - nearDist/biasRange)
		if alpha < 0 {
			alpha = 0
		}
		wt := 1 - alpha
		wi := alpha
		bestIdx := 0
		bestDist := float32(math.MaxFloat32)
		for pi, p := range pal {
			drT := r - float32(p[0])
			dgT := g - float32(p[1])
			dbT := b - float32(p[2])
			dT := drT*drT + dgT*dgT + dbT*dbT
			d := wt*dT + wi*dI[pi]
			if d < bestDist {
				bestDist = d
				bestIdx = pi
			}
		}
		assigns[idx] = int32(bestIdx)
		chosen := pal[bestIdx]
		window[head][0] = r - float32(chosen[0])
		window[head][1] = g - float32(chosen[1])
		window[head][2] = b - float32(chosen[2])
		head = (head + 1) % L
	}
	return assigns, nil
}

// RiemersmaTunedKNearestAlpha is RiemersmaKNearestAlpha with biasRange
// also exposed.
func RiemersmaTunedKNearestAlpha(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, k int, biasMax float64, biasRange float32, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	if k < 1 {
		k = 1
	}
	if k > len(pal) {
		k = len(pal)
	}
	L := RiemersmaWindowSize
	weights := make([]float32, L)
	var total float32
	for j := 0; j < L; j++ {
		weights[j] = float32(math.Pow(RiemersmaDecayRatio, float64(j)/float64(L-1)))
		total += weights[j]
	}
	for j := range weights {
		weights[j] /= total
	}
	tour := buildRiemersmaTour(cells, neighbors)
	window := make([][3]float32, L)
	head := 0
	assigns := make([]int32, n)
	type palDist struct {
		idx  int
		dist float32
	}
	dI := make([]palDist, len(pal))
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}
		var eR, eG, eB float32
		for j := 0; j < L; j++ {
			slot := (head + L - 1 - j) % L
			eR += weights[j] * window[slot][0]
			eG += weights[j] * window[slot][1]
			eB += weights[j] * window[slot][2]
		}
		iR := float32(cells[idx].Color[0])
		iG := float32(cells[idx].Color[1])
		iB := float32(cells[idx].Color[2])
		r := iR + eR
		g := iG + eG
		b := iB + eB
		for pi, p := range pal {
			drI := iR - float32(p[0])
			dgI := iG - float32(p[1])
			dbI := iB - float32(p[2])
			dI[pi] = palDist{pi, drI*drI + dgI*dgI + dbI*dbI}
		}
		sort.Slice(dI, func(a, b int) bool { return dI[a].dist < dI[b].dist })
		nearDist := float32(math.Sqrt(float64(dI[0].dist)))
		alpha := float32(biasMax) * (1 - nearDist/biasRange)
		if alpha < 0 {
			alpha = 0
		}
		wt := 1 - alpha
		wi := alpha
		bestIdx := dI[0].idx
		bestDist := float32(math.MaxFloat32)
		for j := 0; j < k; j++ {
			pi := dI[j].idx
			p := pal[pi]
			drT := r - float32(p[0])
			dgT := g - float32(p[1])
			dbT := b - float32(p[2])
			dT := drT*drT + dgT*dgT + dbT*dbT
			d := wt*dT + wi*dI[j].dist
			if d < bestDist {
				bestDist = d
				bestIdx = pi
			}
		}
		assigns[idx] = int32(bestIdx)
		chosen := pal[bestIdx]
		window[head][0] = r - float32(chosen[0])
		window[head][1] = g - float32(chosen[1])
		window[head][2] = b - float32(chosen[2])
		head = (head + 1) % L
	}
	return assigns, nil
}

// BlueNoiseThresholdSimplex is an ordered-dither algorithm: for each
// cell, pick the pair of palette entries that best brackets the
// input (smallest perpendicular distance from input to the pair
// line, with clipped projection coefficient α ∈ [0,1]). Then choose
// between the two palette entries based on a per-cell low-
// discrepancy threshold. No error feedback — drift per cell is
// exactly the projection error.
//
// Why this is fundamentally different from Riemersma:
//   - No window/memory: each cell's choice is independent.
//   - Bounded wander by construction: chosen ∈ {p_i, p_j} where the
//     pair has minimal perpendicular distance to input.
//   - Drift is per-cell rounding error, integrating to whatever
//     proportion the LDS sequence produces.
//   - Spatial structure depends on the LDS sequence — golden-ratio
//     1D gives near-blue-noise.
//
// For inputs that aren't well-bracketed by any palette pair (e.g.
// out-of-gamut inputs), the projection clips at the segment endpoint
// and drift = |input - chosen-endpoint| accumulates.
func BlueNoiseThresholdSimplex(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	if len(pal) < 2 {
		// Trivial case: only one palette.
		assigns := make([]int32, n)
		return assigns, nil
	}

	// Build a tour just to give a stable cell ordering for the LDS.
	// For pure ordered dither we don't really need the tour structure
	// but we want deterministic per-run results that match the cell
	// neighborhood behaviour — using the tour pos as the LDS index
	// keeps spatially-adjacent cells from getting adjacent θ values.
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
		iR := float32(cells[idx].Color[0])
		iG := float32(cells[idx].Color[1])
		iB := float32(cells[idx].Color[2])

		// Find the pair (i, j) with smallest projection error.
		bestI, bestJ := 0, 1
		var bestAlpha float32 = 0
		var bestErrSq float32 = math.MaxFloat32
		for i := 0; i < len(pal); i++ {
			for j := i + 1; j < len(pal); j++ {
				vR := float32(pal[j][0]) - float32(pal[i][0])
				vG := float32(pal[j][1]) - float32(pal[i][1])
				vB := float32(pal[j][2]) - float32(pal[i][2])
				vSq := vR*vR + vG*vG + vB*vB
				if vSq == 0 {
					continue
				}
				dR := iR - float32(pal[i][0])
				dG := iG - float32(pal[i][1])
				dB := iB - float32(pal[i][2])
				t := (dR*vR + dG*vG + dB*vB) / vSq
				clipped := t
				if clipped < 0 {
					clipped = 0
				}
				if clipped > 1 {
					clipped = 1
				}
				// Projection point: p_i + clipped·v
				projR := float32(pal[i][0]) + clipped*vR
				projG := float32(pal[i][1]) + clipped*vG
				projB := float32(pal[i][2]) + clipped*vB
				eR := iR - projR
				eG := iG - projG
				eB := iB - projB
				errSq := eR*eR + eG*eG + eB*eB
				if errSq < bestErrSq {
					bestErrSq = errSq
					bestI = i
					bestJ = j
					bestAlpha = clipped
				}
			}
		}

		// 1D LDS threshold based on tour position.
		f := float32(tourPos[idx]) * golden
		theta := f - float32(int(f))

		var pick int
		if theta < bestAlpha {
			pick = bestJ
		} else {
			pick = bestI
		}
		assigns[idx] = int32(pick)
	}
	return assigns, nil
}

// BlueNoiseSimplexFull is the K-simplex variant of the blue-noise
// threshold approach. It computes barycentric weights of the input
// w.r.t. the full palette (treated as a tetrahedron when there are 4
// palettes, more generally a simplex). Negative weights — meaning the
// input lies outside the simplex hull — are clipped to 0 and the
// remaining weights renormalized. The result is a probability vector
// over palette entries; per-cell choice draws from it via a 1D LDS
// (golden-ratio sequence on the cell's tour position).
//
// Compared to BlueNoiseThresholdSimplex (K=2 pair only), this should
// give zero drift on inputs that lie inside the palette's convex
// hull (true for most real-image cells when palette colors are
// well-distributed) and only the unavoidable projection error on
// out-of-gamut inputs.
//
// Implementation note: for K palettes in 3D RGB space, only ≤ 4
// palettes can form a non-degenerate simplex spanning 3-space. With
// K > 4 we'd ideally split into multiple tetrahedra; here we use a
// least-squares solve of "w·p = input, sum(w)=1, w ≥ 0" via the
// projected-iteration approach (compute unconstrained, clip negatives
// and renormalize, repeat once or twice). This is good enough for
// typical small palettes.
func BlueNoiseSimplexFull(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	K := len(pal)
	if K < 1 {
		return nil, nil
	}
	if K == 1 {
		return make([]int32, n), nil
	}

	tour := buildRiemersmaTour(cells, neighbors)
	tourPos := make([]int, n)
	for ti, idx := range tour {
		tourPos[idx] = ti
	}

	assigns := make([]int32, n)
	const golden = 0.61803398875
	weights := make([]float64, K)
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

		// Initial unconstrained weights via least-squares: minimize
		// |Σ w_i p_i - input|² subject to Σ w_i = 1.
		// For K=4 in 3D this is exact (tetrahedron barycentric); for
		// K>4 it's a min-norm solution which may have negative
		// weights even for in-hull inputs but they'll get clipped.
		simplexBarycentric(pal, iR, iG, iB, weights)
		// Clip negatives, renormalize. One round of projected
		// iteration. For most palettes this is good enough.
		clipAndRenormalize(weights)

		// Pick via LDS-driven cumulative.
		f := float64(tourPos[idx]) * golden
		theta := f - math.Floor(f)
		var cum float64
		pick := 0
		for j := 0; j < K; j++ {
			cum += weights[j]
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


// BlueNoiseTriangle is between BlueNoiseThresholdSimplex (K=2 pair)
// and BlueNoiseSimplexFull (full simplex). For each cell, find the
// best triangle (3-element subset) of palettes — minimum perpendicular
// distance from input to the triangle — and compute barycentric
// weights inside that triangle. Negative weights are clipped and
// renormalized. Then LDS-driven cumulative pick.
//
// For 4-palette case there are C(4,3) = 4 triangles to enumerate per
// cell. Triangles brace 2D subspaces of 3D color space, so most
// inputs in the palette convex hull will lie close to at least one
// triangle, giving low drift.
//
// Wander is bounded by the diameter of the chosen triangle (max
// distance among its 3 vertices), which in turn is bounded by the
// palette diameter — but typically smaller because the algorithm
// picks the tightest containing triangle.
func BlueNoiseTriangle(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	K := len(pal)
	if K < 3 {
		// Fall back to pair version.
		return BlueNoiseThresholdSimplex(ctx, cells, pal, neighbors, tracker)
	}

	// Enumerate all 3-subsets up front.
	type triangle struct {
		i, j, k int
	}
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

		// Find the triangle with minimum projection error.
		bestTri := triangles[0]
		var bestW [3]float64
		var bestErrSq float64 = math.MaxFloat64
		for _, tri := range triangles {
			// Solve for w_i, w_j, w_k such that
			// w_i p_i + w_j p_j + w_k p_k = input, w_i+w_j+w_k=1.
			// 3 equations + 1 constraint = 4 equations, 3 unknowns
			// → least squares for an over-determined system. Use the
			// "input - p_k = w_i (p_i - p_k) + w_j (p_j - p_k)" form
			// and least-squares for w_i, w_j; w_k = 1 - w_i - w_j.
			ax := float64(pal[tri.i][0]) - float64(pal[tri.k][0])
			ay := float64(pal[tri.i][1]) - float64(pal[tri.k][1])
			az := float64(pal[tri.i][2]) - float64(pal[tri.k][2])
			bx := float64(pal[tri.j][0]) - float64(pal[tri.k][0])
			by := float64(pal[tri.j][1]) - float64(pal[tri.k][1])
			bz := float64(pal[tri.j][2]) - float64(pal[tri.k][2])
			tx := iR - float64(pal[tri.k][0])
			ty := iG - float64(pal[tri.k][1])
			tz := iB - float64(pal[tri.k][2])
			// Normal equations: [a·a a·b; a·b b·b] [wi wj]^T = [a·t; b·t].
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
			// Clip negatives, renormalize.
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
			// Compute projection point and error.
			pR := w[0]*float64(pal[tri.i][0]) + w[1]*float64(pal[tri.j][0]) + w[2]*float64(pal[tri.k][0])
			pG := w[0]*float64(pal[tri.i][1]) + w[1]*float64(pal[tri.j][1]) + w[2]*float64(pal[tri.k][1])
			pB := w[0]*float64(pal[tri.i][2]) + w[1]*float64(pal[tri.j][2]) + w[2]*float64(pal[tri.k][2])
			eR := iR - pR
			eG := iG - pG
			eB := iB - pB
			errSq := eR*eR + eG*eG + eB*eB
			if errSq < bestErrSq {
				bestErrSq = errSq
				bestTri = tri
				bestW = w
			}
		}

		// LDS-driven cumulative pick from best triangle.
		f := float64(tourPos[idx]) * golden
		theta := f - math.Floor(f)
		var pick int
		switch {
		case theta < bestW[0]:
			pick = bestTri.i
		case theta < bestW[0]+bestW[1]:
			pick = bestTri.j
		default:
			pick = bestTri.k
		}
		assigns[idx] = int32(pick)
	}
	return assigns, nil
}

// BlueNoisePairDiffused is BlueNoiseThresholdSimplex (K=2 best-pair
// LDS dither) with Riemersma-style error diffusion of the per-cell
// projection error: the gap from input to the chosen pair's
// projection plane. This bridges the two regimes:
//
//   - On uniform input, the best pair brackets well (zero projection
//     error), so the diffusion contributes nothing and the algorithm
//     reduces to plain bn-pair: wander bounded by pair gap.
//
//   - On textured input where the best pair doesn't bracket every
//     cell perfectly (per-cell projection error > 0), the diffusion
//     spreads that error to neighbors. Future cells see a shifted
//     target, may pick a different pair, balancing global drift.
//
// Diffusion uses Riemersma's existing sliding window with exponential
// decay. The window stores the projection residual from each cell
// (not the LDS noise — that's by design uncorrelated and would
// pollute the blue-noise property if diffused).
func BlueNoisePairDiffused(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	if len(pal) < 2 {
		return make([]int32, n), nil
	}

	L := RiemersmaWindowSize
	weights := make([]float32, L)
	var total float32
	for j := 0; j < L; j++ {
		weights[j] = float32(math.Pow(RiemersmaDecayRatio, float64(j)/float64(L-1)))
		total += weights[j]
	}
	for j := range weights {
		weights[j] /= total
	}

	tour := buildRiemersmaTour(cells, neighbors)
	tourPos := make([]int, n)
	for ti, idx := range tour {
		tourPos[idx] = ti
	}
	window := make([][3]float32, L)
	head := 0

	assigns := make([]int32, n)
	const golden = 0.61803398875
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}

		// Diffused residual from prior projection errors.
		var eR, eG, eB float32
		for j := 0; j < L; j++ {
			slot := (head + L - 1 - j) % L
			eR += weights[j] * window[slot][0]
			eG += weights[j] * window[slot][1]
			eB += weights[j] * window[slot][2]
		}

		iR := float32(cells[idx].Color[0])
		iG := float32(cells[idx].Color[1])
		iB := float32(cells[idx].Color[2])
		tR := iR + eR
		tG := iG + eG
		tB := iB + eB

		// Find best pair for the (residual-adjusted) target.
		bestI, bestJ := 0, 1
		var bestAlpha float32 = 0
		var bestErrSq float32 = math.MaxFloat32
		var bestProjR, bestProjG, bestProjB float32
		for i := 0; i < len(pal); i++ {
			for j := i + 1; j < len(pal); j++ {
				vR := float32(pal[j][0]) - float32(pal[i][0])
				vG := float32(pal[j][1]) - float32(pal[i][1])
				vB := float32(pal[j][2]) - float32(pal[i][2])
				vSq := vR*vR + vG*vG + vB*vB
				if vSq == 0 {
					continue
				}
				dR := tR - float32(pal[i][0])
				dG := tG - float32(pal[i][1])
				dB := tB - float32(pal[i][2])
				t := (dR*vR + dG*vG + dB*vB) / vSq
				clipped := t
				if clipped < 0 {
					clipped = 0
				}
				if clipped > 1 {
					clipped = 1
				}
				projR := float32(pal[i][0]) + clipped*vR
				projG := float32(pal[i][1]) + clipped*vG
				projB := float32(pal[i][2]) + clipped*vB
				eRr := tR - projR
				eGg := tG - projG
				eBb := tB - projB
				errSq := eRr*eRr + eGg*eGg + eBb*eBb
				if errSq < bestErrSq {
					bestErrSq = errSq
					bestI = i
					bestJ = j
					bestAlpha = clipped
					bestProjR = projR
					bestProjG = projG
					bestProjB = projB
				}
			}
		}

		f := float32(tourPos[idx]) * golden
		theta := f - float32(int(f))
		var pick int
		if theta < bestAlpha {
			pick = bestJ
		} else {
			pick = bestI
		}
		assigns[idx] = int32(pick)

		// Diffuse the projection error (target - projection_point) only.
		// The LDS-quantization-noise (chosen - projection_point) is left
		// out: it averages to zero by design, and diffusing it would
		// corrupt the blue-noise property.
		window[head][0] = tR - bestProjR
		window[head][1] = tG - bestProjG
		window[head][2] = tB - bestProjB
		head = (head + 1) % L
	}
	return assigns, nil
}

// BlueNoiseTriangleDiffused is the K=3 simplex variant with the same
// Riemersma-window projection-error diffusion as BlueNoisePairDiffused.
// Trades wander vs drift somewhere between bn-pair-diffused (K=2,
// tightest wander) and bn-simplex-diffused (K=4, lowest projection
// error before diffusion).
func BlueNoiseTriangleDiffused(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	K := len(pal)
	if K < 3 {
		return BlueNoisePairDiffused(ctx, cells, pal, neighbors, tracker)
	}

	type triangle struct {
		i, j, k int
	}
	var triangles []triangle
	for i := 0; i < K; i++ {
		for j := i + 1; j < K; j++ {
			for k := j + 1; k < K; k++ {
				triangles = append(triangles, triangle{i, j, k})
			}
		}
	}

	L := RiemersmaWindowSize
	wts := make([]float32, L)
	var total float32
	for j := 0; j < L; j++ {
		wts[j] = float32(math.Pow(RiemersmaDecayRatio, float64(j)/float64(L-1)))
		total += wts[j]
	}
	for j := range wts {
		wts[j] /= total
	}

	tour := buildRiemersmaTour(cells, neighbors)
	tourPos := make([]int, n)
	for ti, idx := range tour {
		tourPos[idx] = ti
	}
	window := make([][3]float32, L)
	head := 0

	assigns := make([]int32, n)
	const golden = 0.61803398875
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}
		var eR, eG, eB float32
		for j := 0; j < L; j++ {
			slot := (head + L - 1 - j) % L
			eR += wts[j] * window[slot][0]
			eG += wts[j] * window[slot][1]
			eB += wts[j] * window[slot][2]
		}
		iR := float64(cells[idx].Color[0])
		iG := float64(cells[idx].Color[1])
		iB := float64(cells[idx].Color[2])
		tR := iR + float64(eR)
		tG := iG + float64(eG)
		tB := iB + float64(eB)

		bestTri := triangles[0]
		var bestW [3]float64
		var bestErrSq float64 = math.MaxFloat64
		var bestProjR, bestProjG, bestProjB float64
		for _, tri := range triangles {
			ax := float64(pal[tri.i][0]) - float64(pal[tri.k][0])
			ay := float64(pal[tri.i][1]) - float64(pal[tri.k][1])
			az := float64(pal[tri.i][2]) - float64(pal[tri.k][2])
			bx := float64(pal[tri.j][0]) - float64(pal[tri.k][0])
			by := float64(pal[tri.j][1]) - float64(pal[tri.k][1])
			bz := float64(pal[tri.j][2]) - float64(pal[tri.k][2])
			tx := tR - float64(pal[tri.k][0])
			ty := tG - float64(pal[tri.k][1])
			tz := tB - float64(pal[tri.k][2])
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
			eR := tR - pR
			eG := tG - pG
			eB := tB - pB
			errSq := eR*eR + eG*eG + eB*eB
			if errSq < bestErrSq {
				bestErrSq = errSq
				bestTri = tri
				bestW = w
				bestProjR = pR
				bestProjG = pG
				bestProjB = pB
			}
		}

		f := float64(tourPos[idx]) * golden
		theta := f - math.Floor(f)
		var pick int
		switch {
		case theta < bestW[0]:
			pick = bestTri.i
		case theta < bestW[0]+bestW[1]:
			pick = bestTri.j
		default:
			pick = bestTri.k
		}
		assigns[idx] = int32(pick)

		window[head][0] = float32(tR - bestProjR)
		window[head][1] = float32(tG - bestProjG)
		window[head][2] = float32(tB - bestProjB)
		head = (head + 1) % L
	}
	return assigns, nil
}


// DBS (Direct Binary Search) — iterative pixel-flip halftoning that
// minimizes the squared error of an HVS-filtered (output - input)
// signal in Lab. Initialize from a starting halftone (here:
// Riemersma's output). Each sweep visits every cell and tries each
// palette index; accepts the flip if it reduces total filtered
// squared error. Repeats until convergence or maxSweeps.
//
// The filter is a normalized low-pass over each cell's 1-hop neighbor
// graph: self weight 0.5, neighbor weight 0.5/deg. This approximates
// human contrast sensitivity at small spatial scales.
//
// Cost per flip: O(footprint size) update of filtered-error grid.
// Each sweep: O(N · K · footprint). For N=500K, K=4, footprint~10
// that's ~20M ops/sweep. Typically converges in 3-8 sweeps.
//
// Complexity: ≥10× FS but well within budget. The result is the
// closest-to-perceptually-optimal halftone we can achieve at this
// palette + filter configuration.
func DBS(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, maxSweeps int, biasMax float64, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	K := len(pal)

	// Initialize from Riemersma.
	assigns, err := Riemersma(ctx, cells, pal, neighbors, biasMax, tracker)
	if err != nil {
		return nil, err
	}

	// Precompute palette in Lab.
	type lab struct {
		L, A, B float64
	}
	palLab := make([]lab, K)
	for k, p := range pal {
		l, a, b := rgbToLabApprox(float64(p[0]), float64(p[1]), float64(p[2]))
		palLab[k] = lab{l, a, b}
	}
	// Per-cell input Lab.
	inputLab := make([]lab, n)
	for i, c := range cells {
		l, a, b := rgbToLabApprox(float64(c.Color[0]), float64(c.Color[1]), float64(c.Color[2]))
		inputLab[i] = lab{l, a, b}
	}
	// Per-cell error: pal[assigns[i]] - input (in Lab).
	errLab := make([]lab, n)
	for i := range cells {
		p := palLab[assigns[i]]
		errLab[i] = lab{p.L - inputLab[i].L, p.A - inputLab[i].A, p.B - inputLab[i].B}
	}

	// Build filter: weight per (cell, neighbor) link.
	// self weight 0.5, neighbor weight 0.5/deg.
	selfW := 0.5
	type fEntry struct {
		idx int
		w   float64
	}
	filter := make([][]fEntry, n)
	for i := range cells {
		nbs := neighbors[i]
		deg := len(nbs)
		filter[i] = make([]fEntry, 0, deg+1)
		filter[i] = append(filter[i], fEntry{i, selfW})
		if deg > 0 {
			nw := (1 - selfW) / float64(deg)
			for _, nb := range nbs {
				filter[i] = append(filter[i], fEntry{nb.Idx, nw})
			}
		}
	}

	// Filtered error: f_err[i] = sum_j filter[i][.].w * errLab[filter[i][.].idx]
	fErr := make([]lab, n)
	for i := range cells {
		var L, A, B float64
		for _, fe := range filter[i] {
			L += fe.w * errLab[fe.idx].L
			A += fe.w * errLab[fe.idx].A
			B += fe.w * errLab[fe.idx].B
		}
		fErr[i] = lab{L, A, B}
	}

	// Reverse filter: for each cell c, what (cell, weight) pairs use
	// errLab[c] in their filter? When we flip cell c, errLab[c]
	// changes; we need to update fErr at every cell that has c in its
	// filter list.
	// Build: revFilter[c] = list of (i, w) where filter[i] contains
	// (c, w). Since filter is symmetric in our construction (1-hop,
	// equal weighting), this is roughly the same set of cells.
	// We construct it explicitly to handle uneven degrees.
	revFilter := make([][]fEntry, n)
	for i := range cells {
		for _, fe := range filter[i] {
			revFilter[fe.idx] = append(revFilter[fe.idx], fEntry{i, fe.w})
		}
	}

	// Total energy. We don't need the absolute value, just track
	// changes per flip.

	for sweep := 0; sweep < maxSweeps; sweep++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		tracker.StageProgress("DBS sweep", sweep)
		flips := 0
		for i := 0; i < n; i++ {
			cur := assigns[i]
			oldP := palLab[cur]
			oldErr := lab{oldP.L - inputLab[i].L, oldP.A - inputLab[i].A, oldP.B - inputLab[i].B}
			bestK := cur
			var bestDelta float64 = 0
			// Pre-fetch reverse filter list once.
			rl := revFilter[i]
			// Try each candidate palette.
			for k := 0; k < K; k++ {
				if int32(k) == cur {
					continue
				}
				newP := palLab[k]
				newErr := lab{newP.L - inputLab[i].L, newP.A - inputLab[i].A, newP.B - inputLab[i].B}
				dE := lab{newErr.L - oldErr.L, newErr.A - oldErr.A, newErr.B - oldErr.B}
				// Compute change in total fErr squared for affected cells.
				// For each (j, w) in revFilter[i]:
				//   newF[j] = fErr[j] + w · dE
				//   |newF[j]|² - |fErr[j]|² = 2·w·(fErr[j]·dE) + w²·|dE|²
				dESq := dE.L*dE.L + dE.A*dE.A + dE.B*dE.B
				var delta float64
				for _, fe := range rl {
					f := fErr[fe.idx]
					delta += 2*fe.w*(f.L*dE.L+f.A*dE.A+f.B*dE.B) + fe.w*fe.w*dESq
				}
				if delta < bestDelta {
					bestDelta = delta
					bestK = int32(k)
				}
			}
			if bestK != cur {
				// Accept flip. Update errLab[i] and fErr[j] for all j in revFilter[i].
				newP := palLab[bestK]
				newErr := lab{newP.L - inputLab[i].L, newP.A - inputLab[i].A, newP.B - inputLab[i].B}
				dE := lab{newErr.L - oldErr.L, newErr.A - oldErr.A, newErr.B - oldErr.B}
				errLab[i] = newErr
				for _, fe := range rl {
					fErr[fe.idx].L += fe.w * dE.L
					fErr[fe.idx].A += fe.w * dE.A
					fErr[fe.idx].B += fe.w * dE.B
				}
				assigns[i] = bestK
				flips++
			}
		}
		if flips == 0 {
			break
		}
	}
	return assigns, nil
}

// DBS2Hop is DBS with a 2-hop Gaussian-weighted filter footprint
// (self + 1-hop + 2-hop). Wider footprint reduces directional
// structure that the narrow 1-hop filter leaves in the output.
//
// Filter weights: self 0.30, 1-hop ring 0.50 spread, 2-hop ring 0.20
// spread. Normalized within each cell.
func DBS2Hop(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, maxSweeps int, biasMax float64, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	K := len(pal)

	// Initialize from Riemersma.
	assigns, err := Riemersma(ctx, cells, pal, neighbors, biasMax, tracker)
	if err != nil {
		return nil, err
	}

	type lab struct{ L, A, B float64 }
	palLab := make([]lab, K)
	for k, p := range pal {
		l, a, b := rgbToLabApprox(float64(p[0]), float64(p[1]), float64(p[2]))
		palLab[k] = lab{l, a, b}
	}
	inputLab := make([]lab, n)
	for i, c := range cells {
		l, a, b := rgbToLabApprox(float64(c.Color[0]), float64(c.Color[1]), float64(c.Color[2]))
		inputLab[i] = lab{l, a, b}
	}
	errLab := make([]lab, n)
	for i := range cells {
		p := palLab[assigns[i]]
		errLab[i] = lab{p.L - inputLab[i].L, p.A - inputLab[i].A, p.B - inputLab[i].B}
	}

	// Build 2-hop filter via BFS depth 2.
	type fEntry struct {
		idx int
		w   float64
	}
	filter := make([][]fEntry, n)
	const w0 = 0.30
	const w1 = 0.50
	const w2 = 0.20
	visited := make([]int, n)
	for i := range cells {
		// BFS from i with depth 2.
		visited[i] = i + 1 // mark as visited with stamp
		var ring1, ring2 []int
		for _, nb := range neighbors[i] {
			if visited[nb.Idx] != i+1 {
				visited[nb.Idx] = i + 1
				ring1 = append(ring1, nb.Idx)
			}
		}
		for _, j := range ring1 {
			for _, nb := range neighbors[j] {
				if visited[nb.Idx] != i+1 {
					visited[nb.Idx] = i + 1
					ring2 = append(ring2, nb.Idx)
				}
			}
		}
		entries := make([]fEntry, 0, 1+len(ring1)+len(ring2))
		entries = append(entries, fEntry{i, w0})
		if len(ring1) > 0 {
			pw := w1 / float64(len(ring1))
			for _, j := range ring1 {
				entries = append(entries, fEntry{j, pw})
			}
		}
		if len(ring2) > 0 {
			pw := w2 / float64(len(ring2))
			for _, j := range ring2 {
				entries = append(entries, fEntry{j, pw})
			}
		}
		// Renormalize to sum to 1 (handles boundary cells with missing rings).
		var sum float64
		for _, e := range entries {
			sum += e.w
		}
		if sum > 0 {
			inv := 1.0 / sum
			for k := range entries {
				entries[k].w *= inv
			}
		}
		filter[i] = entries
	}

	fErr := make([]lab, n)
	for i := range cells {
		var L, A, B float64
		for _, fe := range filter[i] {
			L += fe.w * errLab[fe.idx].L
			A += fe.w * errLab[fe.idx].A
			B += fe.w * errLab[fe.idx].B
		}
		fErr[i] = lab{L, A, B}
	}

	revFilter := make([][]fEntry, n)
	for i := range cells {
		for _, fe := range filter[i] {
			revFilter[fe.idx] = append(revFilter[fe.idx], fEntry{i, fe.w})
		}
	}

	for sweep := 0; sweep < maxSweeps; sweep++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		tracker.StageProgress("DBS sweep", sweep)
		flips := 0
		for i := 0; i < n; i++ {
			cur := assigns[i]
			oldErr := errLab[i]
			rl := revFilter[i]
			bestK := cur
			var bestDelta float64 = 0
			for k := 0; k < K; k++ {
				if int32(k) == cur {
					continue
				}
				newP := palLab[k]
				newErr := lab{newP.L - inputLab[i].L, newP.A - inputLab[i].A, newP.B - inputLab[i].B}
				dE := lab{newErr.L - oldErr.L, newErr.A - oldErr.A, newErr.B - oldErr.B}
				dESq := dE.L*dE.L + dE.A*dE.A + dE.B*dE.B
				var delta float64
				for _, fe := range rl {
					f := fErr[fe.idx]
					delta += 2*fe.w*(f.L*dE.L+f.A*dE.A+f.B*dE.B) + fe.w*fe.w*dESq
				}
				if delta < bestDelta {
					bestDelta = delta
					bestK = int32(k)
				}
			}
			if bestK != cur {
				newP := palLab[bestK]
				newErr := lab{newP.L - inputLab[i].L, newP.A - inputLab[i].A, newP.B - inputLab[i].B}
				dE := lab{newErr.L - oldErr.L, newErr.A - oldErr.A, newErr.B - oldErr.B}
				errLab[i] = newErr
				for _, fe := range rl {
					fErr[fe.idx].L += fe.w * dE.L
					fErr[fe.idx].A += fe.w * dE.A
					fErr[fe.idx].B += fe.w * dE.B
				}
				assigns[i] = bestK
				flips++
			}
		}
		if flips == 0 {
			break
		}
	}
	return assigns, nil
}

// DBSFromBN is DBS initialized from a BlueNoiseAdaptive output rather
// than Riemersma. Different starting point may converge to a
// different local minimum with less directional structure.
func DBSFromBN(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, tol float64, maxSweeps int, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}
	K := len(pal)

	// Initialize from blue-noise adaptive.
	assigns, err := BlueNoiseAdaptive(ctx, cells, pal, neighbors, tol, tracker)
	if err != nil {
		return nil, err
	}

	type lab struct{ L, A, B float64 }
	palLab := make([]lab, K)
	for k, p := range pal {
		l, a, b := rgbToLabApprox(float64(p[0]), float64(p[1]), float64(p[2]))
		palLab[k] = lab{l, a, b}
	}
	inputLab := make([]lab, n)
	for i, c := range cells {
		l, a, b := rgbToLabApprox(float64(c.Color[0]), float64(c.Color[1]), float64(c.Color[2]))
		inputLab[i] = lab{l, a, b}
	}
	errLab := make([]lab, n)
	for i := range cells {
		p := palLab[assigns[i]]
		errLab[i] = lab{p.L - inputLab[i].L, p.A - inputLab[i].A, p.B - inputLab[i].B}
	}

	type fEntry struct {
		idx int
		w   float64
	}
	filter := make([][]fEntry, n)
	const w0 = 0.30
	const w1 = 0.50
	const w2 = 0.20
	visited := make([]int, n)
	for i := range cells {
		visited[i] = i + 1
		var ring1, ring2 []int
		for _, nb := range neighbors[i] {
			if visited[nb.Idx] != i+1 {
				visited[nb.Idx] = i + 1
				ring1 = append(ring1, nb.Idx)
			}
		}
		for _, j := range ring1 {
			for _, nb := range neighbors[j] {
				if visited[nb.Idx] != i+1 {
					visited[nb.Idx] = i + 1
					ring2 = append(ring2, nb.Idx)
				}
			}
		}
		entries := make([]fEntry, 0, 1+len(ring1)+len(ring2))
		entries = append(entries, fEntry{i, w0})
		if len(ring1) > 0 {
			pw := w1 / float64(len(ring1))
			for _, j := range ring1 {
				entries = append(entries, fEntry{j, pw})
			}
		}
		if len(ring2) > 0 {
			pw := w2 / float64(len(ring2))
			for _, j := range ring2 {
				entries = append(entries, fEntry{j, pw})
			}
		}
		var sum float64
		for _, e := range entries {
			sum += e.w
		}
		if sum > 0 {
			inv := 1.0 / sum
			for k := range entries {
				entries[k].w *= inv
			}
		}
		filter[i] = entries
	}
	fErr := make([]lab, n)
	for i := range cells {
		var L, A, B float64
		for _, fe := range filter[i] {
			L += fe.w * errLab[fe.idx].L
			A += fe.w * errLab[fe.idx].A
			B += fe.w * errLab[fe.idx].B
		}
		fErr[i] = lab{L, A, B}
	}
	revFilter := make([][]fEntry, n)
	for i := range cells {
		for _, fe := range filter[i] {
			revFilter[fe.idx] = append(revFilter[fe.idx], fEntry{i, fe.w})
		}
	}

	for sweep := 0; sweep < maxSweeps; sweep++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		tracker.StageProgress("DBS sweep", sweep)
		flips := 0
		for i := 0; i < n; i++ {
			cur := assigns[i]
			oldErr := errLab[i]
			rl := revFilter[i]
			bestK := cur
			var bestDelta float64 = 0
			for k := 0; k < K; k++ {
				if int32(k) == cur {
					continue
				}
				newP := palLab[k]
				newErr := lab{newP.L - inputLab[i].L, newP.A - inputLab[i].A, newP.B - inputLab[i].B}
				dE := lab{newErr.L - oldErr.L, newErr.A - oldErr.A, newErr.B - oldErr.B}
				dESq := dE.L*dE.L + dE.A*dE.A + dE.B*dE.B
				var delta float64
				for _, fe := range rl {
					f := fErr[fe.idx]
					delta += 2*fe.w*(f.L*dE.L+f.A*dE.A+f.B*dE.B) + fe.w*fe.w*dESq
				}
				if delta < bestDelta {
					bestDelta = delta
					bestK = int32(k)
				}
			}
			if bestK != cur {
				newP := palLab[bestK]
				newErr := lab{newP.L - inputLab[i].L, newP.A - inputLab[i].A, newP.B - inputLab[i].B}
				dE := lab{newErr.L - oldErr.L, newErr.A - oldErr.A, newErr.B - oldErr.B}
				errLab[i] = newErr
				for _, fe := range rl {
					fErr[fe.idx].L += fe.w * dE.L
					fErr[fe.idx].A += fe.w * dE.A
					fErr[fe.idx].B += fe.w * dE.B
				}
				assigns[i] = bestK
				flips++
			}
		}
		if flips == 0 {
			break
		}
	}
	return assigns, nil
}

// RiemersmaLab is base Riemersma with all distance computations and
// the error window operating in CIELAB color space instead of sRGB.
// Hypothesis from Damera-Venkata & Evans 2003: vector error diffusion
// in a perceptually uniform space gives more visually-correct
// dithering since the score and α-bias use perceptual distances
// rather than RGB distances.
func RiemersmaLab(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, biasMax float64, biasRange float32, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}

	// Precompute palette in Lab.
	type lab struct {
		L, A, B float32
	}
	pl := make([]lab, len(pal))
	for k, p := range pal {
		L_, A, B := rgbToLabApprox(float64(p[0]), float64(p[1]), float64(p[2]))
		pl[k] = lab{float32(L_), float32(A), float32(B)}
	}

	L := RiemersmaWindowSize
	weights := make([]float32, L)
	var total float32
	for j := 0; j < L; j++ {
		weights[j] = float32(math.Pow(RiemersmaDecayRatio, float64(j)/float64(L-1)))
		total += weights[j]
	}
	for j := range weights {
		weights[j] /= total
	}

	tour := buildRiemersmaTour(cells, neighbors)
	window := make([][3]float32, L)
	head := 0

	assigns := make([]int32, n)
	dI := make([]float32, len(pal))
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}

		var eL, eA, eB float32
		for j := 0; j < L; j++ {
			slot := (head + L - 1 - j) % L
			eL += weights[j] * window[slot][0]
			eA += weights[j] * window[slot][1]
			eB += weights[j] * window[slot][2]
		}

		il, ia, ib := rgbToLabApprox(float64(cells[idx].Color[0]), float64(cells[idx].Color[1]), float64(cells[idx].Color[2]))
		iL := float32(il)
		iA := float32(ia)
		iB := float32(ib)
		tL := iL + eL
		tA := iA + eA
		tB := iB + eB

		var minDI float32 = math.MaxFloat32
		for pi, p := range pl {
			dL := iL - p.L
			dA := iA - p.A
			dB := iB - p.B
			d := dL*dL + dA*dA + dB*dB
			dI[pi] = d
			if d < minDI {
				minDI = d
			}
		}
		nearDist := float32(math.Sqrt(float64(minDI)))
		alpha := float32(biasMax) * (1 - nearDist/biasRange)
		if alpha < 0 {
			alpha = 0
		}
		wt := 1 - alpha
		wi := alpha
		bestIdx := 0
		bestDist := float32(math.MaxFloat32)
		for pi, p := range pl {
			dL := tL - p.L
			dA := tA - p.A
			dB := tB - p.B
			dT := dL*dL + dA*dA + dB*dB
			d := wt*dT + wi*dI[pi]
			if d < bestDist {
				bestDist = d
				bestIdx = pi
			}
		}
		assigns[idx] = int32(bestIdx)

		chosen := pl[bestIdx]
		window[head][0] = tL - chosen.L
		window[head][1] = tA - chosen.A
		window[head][2] = tB - chosen.B
		head = (head + 1) % L
	}
	return assigns, nil
}

// RiemersmaSnapFlat behaves like Riemersma except that when the
// input cell is within snapThreshold (Lab-ish in 8-bit RGB units) of
// some palette entry, it hard-snaps to that nearest palette and
// SKIPS the window update. This trades a small drift (bounded by
// snapThreshold) in flat regions for zero wander there. In textured
// regions (input far from any palette), it falls through to regular
// Riemersma so chroma is preserved by error diffusion.
//
// Why this differs from the existing α-bias: α-bias still picks
// among ALL palettes weighted by score, so a large window residual
// can still push the pick to a far palette. Hard-snap simply ignores
// the residual entirely for cells we've decided are "flat."
//
// Skipping the window update means: the per-cell residual in flat
// regions is dropped (becomes drift) instead of being carried into
// neighbors. That's the deal — accept drift ≤ snapThreshold in flat
// regions, kill wander.
func RiemersmaSnapFlat(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, snapThreshold float32, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}

	L := RiemersmaWindowSize
	weights := make([]float32, L)
	var total float32
	for j := 0; j < L; j++ {
		weights[j] = float32(math.Pow(RiemersmaDecayRatio, float64(j)/float64(L-1)))
		total += weights[j]
	}
	for j := range weights {
		weights[j] /= total
	}

	tour := buildRiemersmaTour(cells, neighbors)
	window := make([][3]float32, L)
	head := 0

	assigns := make([]int32, n)
	thresh2 := snapThreshold * snapThreshold
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}

		iR := float32(cells[idx].Color[0])
		iG := float32(cells[idx].Color[1])
		iB := float32(cells[idx].Color[2])

		// Check nearest-input distance first.
		var nearest int
		var nearD2 float32 = math.MaxFloat32
		for pi, p := range pal {
			drI := iR - float32(p[0])
			dgI := iG - float32(p[1])
			dbI := iB - float32(p[2])
			d := drI*drI + dgI*dgI + dbI*dbI
			if d < nearD2 {
				nearD2 = d
				nearest = pi
			}
		}

		if nearD2 <= thresh2 {
			// Flat region: snap, skip window update.
			assigns[idx] = int32(nearest)
			// Advance head with a zero entry to keep ring rotation
			// consistent — the window decays naturally.
			window[head] = [3]float32{0, 0, 0}
			head = (head + 1) % L
			continue
		}

		// Textured region: regular Riemersma.
		var eR, eG, eB float32
		for j := 0; j < L; j++ {
			slot := (head + L - 1 - j) % L
			eR += weights[j] * window[slot][0]
			eG += weights[j] * window[slot][1]
			eB += weights[j] * window[slot][2]
		}
		r := iR + eR
		g := iG + eG
		b := iB + eB

		bestIdx := 0
		bestDist := float32(math.MaxFloat32)
		for pi, p := range pal {
			drT := r - float32(p[0])
			dgT := g - float32(p[1])
			dbT := b - float32(p[2])
			d := drT*drT + dgT*dgT + dbT*dbT
			if d < bestDist {
				bestDist = d
				bestIdx = pi
			}
		}
		assigns[idx] = int32(bestIdx)

		chosen := pal[bestIdx]
		window[head][0] = r - float32(chosen[0])
		window[head][1] = g - float32(chosen[1])
		window[head][2] = b - float32(chosen[2])
		head = (head + 1) % L
	}
	return assigns, nil
}

// RiemersmaKNearestRefined runs RiemersmaKNearest then does a small
// number of local-swap refinement passes. Each refinement pass walks
// the cells and, for each one, tries swapping its assignment to each
// of its K-nearest palette entries; accepts the swap if a small
// neighborhood objective decreases.
//
// Objective per cell: per-cell ΔE² + λ·|sum-of-neighborhood-residual|².
// First term penalizes wander; second term penalizes regional drift.
func RiemersmaKNearestRefined(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, k int, refinePasses int, lambda float32, tracker progress.Tracker) ([]int32, error) {
	assigns, err := RiemersmaKNearest(ctx, cells, pal, neighbors, k, tracker)
	if err != nil || refinePasses <= 0 {
		return assigns, err
	}
	n := len(cells)
	if n == 0 {
		return assigns, nil
	}

	// Precompute K-nearest palette candidates per cell (in input
	// distance), so refinement also stays K-bounded for wander.
	candidates := make([][]int32, n)
	{
		type palDist struct {
			idx  int
			dist float32
		}
		dI := make([]palDist, len(pal))
		for i, c := range cells {
			iR := float32(c.Color[0])
			iG := float32(c.Color[1])
			iB := float32(c.Color[2])
			for pi, p := range pal {
				drI := iR - float32(p[0])
				dgI := iG - float32(p[1])
				dbI := iB - float32(p[2])
				dI[pi] = palDist{pi, drI*drI + dgI*dgI + dbI*dbI}
			}
			sort.Slice(dI, func(a, b int) bool { return dI[a].dist < dI[b].dist })
			cand := make([]int32, k)
			for j := 0; j < k; j++ {
				cand[j] = int32(dI[j].idx)
			}
			candidates[i] = cand
		}
	}

	// Per-cell residual: pal[assigns[i]] - input[i].
	residual := func(i int) (float32, float32, float32) {
		c := cells[i]
		p := pal[assigns[i]]
		return float32(p[0]) - float32(c.Color[0]),
			float32(p[1]) - float32(c.Color[1]),
			float32(p[2]) - float32(c.Color[2])
	}

	for pass := 0; pass < refinePasses; pass++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		improved := 0
		for i := 0; i < n; i++ {
			// Build the i-and-neighbors region (≤ ~5).
			region := []int{i}
			for _, nb := range neighbors[i] {
				if len(region) >= 5 {
					break
				}
				region = append(region, nb.Idx)
			}
			// Current objective.
			curObj := func() float32 {
				var sumQ float32
				var sR, sG, sB float32
				for _, ci := range region {
					c := cells[ci]
					p := pal[assigns[ci]]
					rR := float32(p[0]) - float32(c.Color[0])
					rG := float32(p[1]) - float32(c.Color[1])
					rB := float32(p[2]) - float32(c.Color[2])
					sumQ += rR*rR + rG*rG + rB*rB
					sR += rR
					sG += rG
					sB += rB
				}
				return sumQ + lambda*(sR*sR+sG*sG+sB*sB)
			}()
			// Try each candidate for cell i; keep best.
			origAssign := assigns[i]
			bestAssign := origAssign
			bestObj := curObj
			for _, ca := range candidates[i] {
				if ca == origAssign {
					continue
				}
				assigns[i] = ca
				var sumQ float32
				var sR, sG, sB float32
				for _, ci := range region {
					c := cells[ci]
					p := pal[assigns[ci]]
					rR := float32(p[0]) - float32(c.Color[0])
					rG := float32(p[1]) - float32(c.Color[1])
					rB := float32(p[2]) - float32(c.Color[2])
					sumQ += rR*rR + rG*rG + rB*rB
					sR += rR
					sG += rG
					sB += rB
				}
				obj := sumQ + lambda*(sR*sR+sG*sG+sB*sB)
				if obj < bestObj {
					bestObj = obj
					bestAssign = ca
				}
			}
			assigns[i] = bestAssign
			if bestAssign != origAssign {
				improved++
			}
		}
		_ = improved
	}
	_ = residual
	return assigns, nil
}
