package palette

import (
	"context"
	"sync/atomic"
	"testing"
)

// benchEagleCloud builds a large cream/white/tan/brown/black cloud with fine
// perturbations so CellColorHistogram yields the full 5000-sample cap that a
// real model (e.g. orzel) produces — the cream-eagle fixture has only ~6
// distinct colors, so it does NOT represent the per-score sample cost that
// makes the "Building palette" stage slow.
func benchEagleCloud() [][3]uint8 {
	base := [][3]uint8{
		{0xEE, 0xD1, 0xA8}, // cream
		{0xF0, 0xEC, 0xE0}, // white
		{0xC8, 0xB0, 0x80}, // tan
		{0x55, 0x33, 0x1A}, // brown
		{0x3A, 0x24, 0x12}, // dark brown
		{0x10, 0x10, 0x12}, // black
	}
	weights := []int{40, 25, 15, 12, 6, 8}
	clamp := func(v int) uint8 {
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return uint8(v)
	}
	var out [][3]uint8
	// Deterministic LCG so the cloud is stable run-to-run.
	rng := uint32(0x9E3779B9)
	next := func() int {
		rng = rng*1664525 + 1013904223
		return int(rng>>24) - 128 // roughly [-128,127]
	}
	for bi, b := range base {
		// Enough perturbed copies of each base color that the histogram fills
		// well past the 5000-color cap.
		count := weights[bi] * 300
		for i := 0; i < count; i++ {
			out = append(out, [3]uint8{
				clamp(int(b[0]) + next()/12),
				clamp(int(b[1]) + next()/12),
				clamp(int(b[2]) + next()/12),
			})
		}
	}
	return out
}

// benchSampleCap sizes the sample cloud. The real cap is 5000, but a full
// 5000-sample exhaustive pass is ~5 min/run — impractical for benchmarking. A
// ~1500-sample cloud lands the baseline near the ~45s the user measured on
// orzel, so it faithfully stands in for that workload while iterating.
const benchSampleCap = 1500

// benchSelectInstance builds the real 28-choose-4 Panchroma inventory against a
// large sample cloud — the orzel-representative workload for the TD-aware
// exhaustive search (C(28,4) = 20475 subsets).
func benchSelectInstance() (scoreFunc, [][3]float64, []WeightedLabSample) {
	inv := panchromaBasicInventory()
	samples := CellColorHistogram(benchEagleCloud(), nil)
	ApplyChromaWeighting(samples)
	samples = topSamples(samples, benchSampleCap)
	scorer, invLab := tdAwareScorer(inv, samples)
	return scorer, invLab, samples
}

// BenchmarkExhaustiveSelect measures the full TD-aware exhaustive search over
// the real 28-choose-4 instance — the dominant cost of the "Building palette"
// stage on orzel-sized inventories.
func BenchmarkExhaustiveSelect(b *testing.B) {
	scorer, invLab, samples := benchSelectInstance()
	const n = 4
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var counter atomic.Int64
		if _, err := exhaustiveSearch(context.Background(), invLab, nil, samples, n, scorer, &counter); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMultiStartVNDSelect measures the deterministic multi-start VND
// fallback over the same instance, for the exhaustive-vs-VND cutoff decision.
func BenchmarkMultiStartVNDSelect(b *testing.B) {
	scorer, invLab, samples := benchSelectInstance()
	const n = 4
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, err := multiStartVND(context.Background(), invLab, nil, samples, n, scorer); err != nil {
			b.Fatal(err)
		}
	}
}
