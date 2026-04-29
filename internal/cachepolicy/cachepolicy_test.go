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
