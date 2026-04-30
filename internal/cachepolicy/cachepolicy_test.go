package cachepolicy

import (
	"testing"
	"time"
)

func TestScoreShape(t *testing.T) {
	now := time.Now()
	// 1KB / 1s vs 1000KB / 1000s: large-expensive must win even
	// though it's bigger. With sqrt size penalty the 1000s entry
	// wins by ~32×.
	tiny := Entry{SizeBytes: 1024, CostMs: 1000, Mtime: now}
	huge := Entry{SizeBytes: 1024 * 1000, CostMs: 1000 * 1000, Mtime: now}
	if Score(tiny, now) >= Score(huge, now) {
		t.Errorf("expected huge-expensive to outscore tiny-cheap; got tiny=%.3f huge=%.3f",
			Score(tiny, now), Score(huge, now))
	}
}

func TestSizeFloorPreventsInversion(t *testing.T) {
	now := time.Now()
	// 5KB / 0.5s Parse-like vs 600MB / 60s alpha-wrap Load. Without
	// the floor, the tiny entry's lower size penalty would outrank
	// the much-more-expensive Load.
	parse := Entry{SizeBytes: 5 * 1024, CostMs: 500, Mtime: now}
	load := Entry{SizeBytes: 600 * 1024 * 1024, CostMs: 60 * 1000, Mtime: now}
	if Score(parse, now) >= Score(load, now) {
		t.Errorf("size floor failed: parse=%.3f load=%.3f", Score(parse, now), Score(load, now))
	}
}

func TestRecencyFactor(t *testing.T) {
	if got := RecencyFactor(0); got != 1.0 {
		t.Errorf("zero age must yield 1.0, got %v", got)
	}
	if got := RecencyFactor(HalfLife); got > 0.51 || got < 0.49 {
		t.Errorf("one-halflife age must yield ~0.5, got %v", got)
	}
}

func TestFitToBudgetSelectsLowestScore(t *testing.T) {
	now := time.Now()
	// Three entries, identical size, costs 1/100/10000 ms. Cap fits
	// only two — the cheapest must be evicted.
	entries := []Entry{
		{Key: "cheap", SizeBytes: 1000, CostMs: 1, Mtime: now},
		{Key: "mid", SizeBytes: 1000, CostMs: 100, Mtime: now},
		{Key: "expensive", SizeBytes: 1000, CostMs: 10000, Mtime: now},
	}
	got := FitToBudget(entries, 2500, now)
	if len(got) != 1 || entries[got[0]].Key != "cheap" {
		t.Errorf("expected eviction of [cheap], got indices %v", got)
	}
}

func TestFitToBudgetNoOpWhenWithinBudget(t *testing.T) {
	now := time.Now()
	entries := []Entry{
		{Key: "a", SizeBytes: 100, CostMs: 1, Mtime: now},
		{Key: "b", SizeBytes: 100, CostMs: 1, Mtime: now},
	}
	if got := FitToBudget(entries, 1000, now); len(got) != 0 {
		t.Errorf("expected no eviction, got %v", got)
	}
}

// TestFitToBudgetFreshBeatsStaleHigherCost reproduces the user's
// real-world scenario: a just-completed Clip output (25.4s, 68 MB)
// vs an hours-old Clip output (6.2s, 23 MB) used to evict the fresh
// one because the recency-factor difference was negligible on a
// 7-day half-life. With the 1-hour half-life the fresh entry's
// recency factor stays at 1.0 while the stale one drops to 0.5,
// so cost*recency favors the fresh entry even though its raw cost
// per sqrt-byte is lower than the older one's.
func TestFitToBudgetFreshBeatsStaleHigherCost(t *testing.T) {
	now := time.Now()
	staleMtime := now.Add(-3 * HalfLife) // recency factor ~0.125
	entries := []Entry{
		{Key: "fresh-clip", SizeBytes: 68 << 20, CostMs: 25400, Mtime: now},
		{Key: "stale-merge", SizeBytes: 22 << 20, CostMs: 12300, Mtime: staleMtime},
	}
	// Cumulative size > 80 MiB; budget = 70 MiB forces one eviction.
	got := FitToBudget(entries, 70<<20, now)
	if len(got) != 1 {
		t.Fatalf("expected one eviction, got %v", got)
	}
	if entries[got[0]].Key != "stale-merge" {
		t.Errorf("expected stale-merge to evict, got %s", entries[got[0]].Key)
	}
}

// TestRecencyFloorKeepsAgedEntriesRanked verifies that very old
// entries don't all collapse to score 0. A high-cost ancient entry
// must still outrank a cheap ancient one of the same size.
func TestRecencyFloorKeepsAgedEntriesRanked(t *testing.T) {
	now := time.Now()
	old := now.Add(-30 * 24 * time.Hour) // many half-lives — clamped to floor
	entries := []Entry{
		{Key: "old-cheap", SizeBytes: 1000, CostMs: 100, Mtime: old},
		{Key: "old-expensive", SizeBytes: 1000, CostMs: 60000, Mtime: old},
	}
	got := FitToBudget(entries, 1500, now)
	if len(got) != 1 || entries[got[0]].Key != "old-cheap" {
		t.Errorf("expected old-cheap to evict first, got %v", got)
	}
}
