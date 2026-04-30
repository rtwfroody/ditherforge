// Package cachepolicy holds the value-scoring formula and eviction
// ranking primitive used by the disk cache. Extracted from diskcache
// so the formula can be unit-tested independently of file I/O.
package cachepolicy

import (
	"math"
	"sort"
	"time"
)

// HalfLife is the recency decay halflife. A 1-hour halflife is sharp
// enough that just-generated entries dominate over even moderately
// stale ones (factor 1.0 vs 0.5 at one hour), preventing the
// "expensive result evicted seconds after it was written" failure
// mode where the prior 7-day halflife couldn't tell a few-seconds-old
// entry from a few-hours-old one. Tied to time-since-access (mtime),
// which the tiers bump on every cache hit.
const HalfLife = 1 * time.Hour

// RecencyFloor caps how far the recency factor can decay. Without it,
// a 1-hour halflife pushes day-old entries to 2^-24 ≈ 6e-8, making
// every old entry score essentially zero regardless of generation
// cost. The floor preserves a meaningful score among older entries so
// expensive ones (e.g. a 60s alpha-wrap from yesterday) still beat
// cheap ones (e.g. a 0.5s parse) when both fall outside the
// fresh-tier window.
const RecencyFloor = 0.05

// SizeFloor is the minimum size used in the score's sqrt denominator.
// Entries smaller than this cluster together, so absolute cost decides
// among "small enough that size barely matters" entries. Without a
// floor, a tiny-but-fresh entry can outrank a huge expensive one of
// similar density via compounding penalty at trivial sizes.
const SizeFloor = 64 * 1024

// Entry is the minimum a tier needs to expose for ranking. Each tier
// builds these from its underlying storage (file walks for disk, the
// live map for memory) and feeds them to FitToBudget.
type Entry struct {
	Stage       string
	Key         string
	Description string
	SizeBytes   int64
	CostMs      int64
	Mtime       time.Time
}

// RecencyFactor returns the multiplier in [RecencyFloor, 1] that age
// contributes to an entry's score. age <= 0 (clock skew) yields 1.0.
// The decay is exp(-age / HalfLife) clamped at RecencyFloor so old
// entries keep a non-zero contribution that lets cost differentiate
// among them.
func RecencyFactor(age time.Duration) float64 {
	if age <= 0 {
		return 1.0
	}
	f := math.Pow(0.5, age.Seconds()/HalfLife.Seconds())
	if f < RecencyFloor {
		return RecencyFloor
	}
	return f
}

// Score is the value an entry contributes to the cache. Higher is more
// valuable. The shape:
//
//	score = (costMs / sqrt(max(sizeBytes, SizeFloor))) * 2^(-age/HalfLife)
//
// Entries with no recorded cost (legacy / aborted writes) get score 0
// and fall to the front of the eviction queue.
func Score(e Entry, now time.Time) float64 {
	if e.SizeBytes <= 0 {
		return 0
	}
	size := float64(e.SizeBytes)
	if size < float64(SizeFloor) {
		size = float64(SizeFloor)
	}
	base := float64(e.CostMs) / math.Sqrt(size)
	return base * RecencyFactor(now.Sub(e.Mtime))
}

// FitToBudget returns indices into entries identifying which entries
// to evict so the survivors total at most maxBytes. Ranking is by
// Score ascending; ties break by oldest-mtime-first (preserving LRU
// semantics among legacy zero-cost entries). Returns nil if the input
// already fits.
//
// The caller is responsible for actually deleting. Returning indices
// (rather than copies of Entry) lets the caller index back into its
// own richer per-entry storage without a key-lookup map.
func FitToBudget(entries []Entry, maxBytes int64, now time.Time) []int {
	var total int64
	for _, e := range entries {
		total += e.SizeBytes
	}
	if total <= maxBytes {
		return nil
	}
	idx := make([]int, len(entries))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool {
		ea, eb := entries[idx[a]], entries[idx[b]]
		sa, sb := Score(ea, now), Score(eb, now)
		if sa != sb {
			return sa < sb
		}
		return ea.Mtime.Before(eb.Mtime)
	})
	var out []int
	for _, i := range idx {
		if total <= maxBytes {
			break
		}
		out = append(out, i)
		total -= entries[i].SizeBytes
	}
	return out
}
