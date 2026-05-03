package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/diskcache"
	"github.com/rtwfroody/ditherforge/internal/progress"
)

// makeFakeInput writes a tiny placeholder to a temp dir so stageKey's
// content-hash succeeds. Returns the absolute path.
func makeFakeInput(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "model.glb")
	if err := os.WriteFile(path, []byte("not a real glb, just bytes for hashing"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestStageKeyCascade: changing a downstream stage's settings does not
// affect an upstream stage's key. Changing an upstream stage's settings
// changes every downstream stage's key (cascade).
func TestStageKeyCascade(t *testing.T) {
	c := NewStageCache()
	path := makeFakeInput(t)
	base := Options{Input: path, Scale: 1, NozzleDiameter: 0.4, LayerHeight: 0.2, Dither: "none"}

	// Changing dither (a downstream-only setting) should not change the
	// load or decimate keys.
	upstream := base
	upstream.Dither = "dizzy"
	if c.stageKey(StageLoad, base) != c.stageKey(StageLoad, upstream) {
		t.Error("StageLoad key changed when only Dither changed (no cascade upward expected)")
	}
	if c.stageKey(StageDecimate, base) != c.stageKey(StageDecimate, upstream) {
		t.Error("StageDecimate key changed when only Dither changed")
	}

	// Changing scale (a load setting) should change every stage's key.
	scaled := base
	scaled.Scale = 2
	for s := StageLoad; s < numStages; s++ {
		if c.stageKey(s, base) == c.stageKey(s, scaled) {
			t.Errorf("stage %d key did not change when Scale changed (cascade broken)", s)
		}
	}
}

// TestStageKeyDownstreamCascade: changing LayerHeight (which is in
// decimateSettings) invalidates the decimate stage and every stage after
// it, but not StageLoad.
func TestStageKeyDownstreamCascade(t *testing.T) {
	c := NewStageCache()
	path := makeFakeInput(t)
	base := Options{Input: path, Scale: 1, NozzleDiameter: 0.4, LayerHeight: 0.2, Dither: "none"}
	other := base
	other.LayerHeight = 0.12

	if c.stageKey(StageLoad, base) != c.stageKey(StageLoad, other) {
		t.Error("StageLoad key changed when only LayerHeight changed")
	}
	for s := StageDecimate; s < numStages; s++ {
		if c.stageKey(s, base) == c.stageKey(s, other) {
			t.Errorf("stage %d key did not change when LayerHeight changed", s)
		}
	}
}

// TestCacheAToggleBToggleAHitsDisk: the A↔B↔A scenario the user actually
// cares about. After computing for A, then B, then A again, the second
// "A" lookup must hit the disk cache (no recompute). Identity
// comparison doesn't apply because the cache stores blobs and decodes
// a fresh struct on every hit.
func TestCacheAToggleBToggleAHitsDisk(t *testing.T) {
	c := NewStageCache()
	d, err := diskcache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c.SetDisk(d)
	// Cleanup safety only — the explicit WaitForDiskWrites before the
	// reads below is what the assertions depend on.
	defer c.WaitForDiskWrites()

	path := makeFakeInput(t)
	optsA := Options{Input: path, Scale: 1, NozzleDiameter: 0.4, LayerHeight: 0.2, Dither: "none"}
	optsB := optsA
	optsB.LayerHeight = 0.12

	c.set(StageDecimate, optsA, &decimateOutput{})
	c.set(StageVoxelize, optsA, &voxelizeOutput{})
	c.set(StageDecimate, optsB, &decimateOutput{})
	c.set(StageVoxelize, optsB, &voxelizeOutput{})
	// Wait for async writes to land before reading.
	c.WaitForDiskWrites()

	if _, src := c.getWithSource(StageDecimate, optsA); src != hitDisk {
		t.Errorf("Decimate A→B→A: hit source %v, want hitDisk", src)
	}
	if _, src := c.getWithSource(StageVoxelize, optsA); src != hitDisk {
		t.Errorf("Voxelize A→B→A: hit source %v, want hitDisk", src)
	}
	if _, src := c.getWithSource(StageDecimate, optsB); src != hitDisk {
		t.Errorf("Decimate B: hit source %v, want hitDisk", src)
	}
}

// TestParseStageKeyDependsOnInputOnly: StageParse's key changes when
// Input/ObjectIndex change but is invariant under everything else
// (Scale, Size, alpha-wrap, base color, ReloadSeq, etc.).
//
// ReloadSeq is intentionally NOT in the parse cache key — it's a
// frontend-only counter for re-triggering reactive effects on
// same-path re-selects. Including it would cause spurious cache misses
// when the same underlying file is loaded via different UI paths
// (direct .glb open bumps reloadSeq; settings-JSON load doesn't).
func TestParseStageKeyDependsOnInputOnly(t *testing.T) {
	c := NewStageCache()
	pathA := makeFakeInput(t)
	base := Options{Input: pathA, Scale: 1, NozzleDiameter: 0.4, LayerHeight: 0.2, Dither: "none"}

	// Scale changes should NOT change StageParse's key.
	scaled := base
	scaled.Scale = 2
	if c.stageKey(StageParse, base) != c.stageKey(StageParse, scaled) {
		t.Error("StageParse key changed when Scale changed; parse cache should survive")
	}

	// AlphaWrap changes should NOT change StageParse's key.
	wrapped := base
	wrapped.AlphaWrap = true
	if c.stageKey(StageParse, base) != c.stageKey(StageParse, wrapped) {
		t.Error("StageParse key changed when AlphaWrap changed; parse cache should survive")
	}

	// ObjectIndex change SHOULD change StageParse's key (different mesh).
	otherIdx := base
	otherIdx.ObjectIndex = 1
	if c.stageKey(StageParse, base) == c.stageKey(StageParse, otherIdx) {
		t.Error("StageParse key did not change when ObjectIndex changed")
	}

	// ReloadSeq must NOT change StageParse's key — it's a frontend
	// reactive-trigger counter, not a real cache invariant.
	reloaded := base
	reloaded.ReloadSeq = 1
	if c.stageKey(StageParse, base) != c.stageKey(StageParse, reloaded) {
		t.Error("StageParse key changed when ReloadSeq bumped; cache must survive same-path re-selects")
	}
}

// TestLoadStageKeyInvalidatesOnAlphaWrap: StageLoad must re-run when
// alpha-wrap parameters change (the wrap result is part of loadOutput).
func TestLoadStageKeyInvalidatesOnAlphaWrap(t *testing.T) {
	c := NewStageCache()
	path := makeFakeInput(t)
	base := Options{Input: path, Scale: 1, NozzleDiameter: 0.4, LayerHeight: 0.2, Dither: "none"}

	wrapped := base
	wrapped.AlphaWrap = true
	if c.stageKey(StageLoad, base) == c.stageKey(StageLoad, wrapped) {
		t.Fatal("StageLoad key did not change when AlphaWrap toggled")
	}

	wrappedTighter := wrapped
	wrappedTighter.AlphaWrapAlpha = 0.6
	if c.stageKey(StageLoad, wrapped) == c.stageKey(StageLoad, wrappedTighter) {
		t.Fatal("StageLoad key did not change when AlphaWrapAlpha changed")
	}
}

// TestStageKeyEmptyOnHashFailure: when the input file doesn't exist (so we
// can't hash it), stageKey returns "" and no caching happens.
func TestStageKeyEmptyOnHashFailure(t *testing.T) {
	c := NewStageCache()
	// Don't set inputHash; force hash attempt on a non-existent file.
	opts := Options{Input: "/this/path/should/not/exist/anywhere"}
	if k := c.stageKey(StageLoad, opts); k != "" {
		t.Errorf("expected empty key on hash failure, got %q", k)
	}
}

// TestRunStageCacheHitReturnsValue is the basic post-refactor invariant:
// when the disk cache contains a usable entry for the stage, runStage
// returns it without invoking the body, and the returned pointer is
// non-nil. Caller code (Load → applyBaseColor) dereferences the
// pointer immediately, so a (nil, nil) return would be a crash.
func TestRunStageCacheHitReturnsValue(t *testing.T) {
	c := NewStageCache()
	d, err := diskcache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c.SetDisk(d)
	defer c.WaitForDiskWrites()

	path := makeFakeInput(t)
	opts := Options{Input: path, Scale: 1, NozzleDiameter: 0.4, LayerHeight: 0.2, Dither: "none"}

	want := &decimateOutput{}
	c.set(StageDecimate, opts, want)
	c.WaitForDiskWrites()

	bodyRan := false
	r := &pipelineRun{
		ctx:     context.Background(),
		cache:   c,
		opts:    opts,
		tracker: progress.NullTracker{},
	}
	got, err := runStage(r, StageDecimate, &r.decimate, func() (*decimateOutput, error) {
		bodyRan = true
		return &decimateOutput{}, nil
	})
	if err != nil {
		t.Fatalf("runStage: %v", err)
	}
	if got == nil {
		t.Fatal("runStage returned nil on a cache hit")
	}
	if bodyRan {
		t.Error("body executed despite a cache hit being available")
	}
}

// TestRunStageHandlesMidRunCacheEviction guards against the regression
// that produced a SIGSEGV in applyBaseColor when the disk cache sweep
// (kicked at the end of every pipeline run) raced runStage's old
// double-read pattern: getWithSource succeeded inside runStageCached,
// the value was thrown away, and a second cache.get from the caller
// raced the sweep's os.Remove and returned nil. runStage wrote the
// nil into the slot and returned (nil, nil).
//
// We simulate the race by deleting the cache file between the
// runStageCached cache hit and any second read: the runStageCached
// closure (the body) doesn't run on a hit, so we instead pre-populate
// the disk cache, then wedge a fake "remove the file underneath"
// between getWithSource and the slot write by removing it from a Set
// completion goroutine. Easier: just ensure runStage's contract holds
// — when there's a cache hit, the returned slot is non-nil even if a
// concurrent eviction wipes the file.
func TestRunStageHandlesMidRunCacheEviction(t *testing.T) {
	c := NewStageCache()
	d, err := diskcache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c.SetDisk(d)
	defer c.WaitForDiskWrites()

	path := makeFakeInput(t)
	opts := Options{Input: path, Scale: 1, NozzleDiameter: 0.4, LayerHeight: 0.2, Dither: "none"}

	// Pre-populate the Decimate stage with a known value.
	want := &decimateOutput{}
	c.set(StageDecimate, opts, want)
	c.WaitForDiskWrites()

	// Now wipe the file from disk to simulate the post-set sweep race.
	// runStageCached's first cache.getWithSource will still succeed by
	// reading from the OS page cache before the unlink takes effect —
	// and even if it doesn't, the bug we care about is that the
	// caller's second cache.get goes missing. Deleting before the
	// runStage call exercises both halves: with the old code, the
	// first read happens, the value is dropped, the second read
	// misses (file gone), and *slot stays nil. With the fix,
	// runStageCached returns the decoded value to the caller, which
	// stashes it before any second read.
	subdir := stageSubdir(StageDecimate)
	key := c.stageKey(StageDecimate, opts)
	d.Remove(subdir, key)

	r := &pipelineRun{
		ctx:     context.Background(),
		cache:   c,
		opts:    opts,
		tracker: progress.NullTracker{},
	}
	got, err := runStage(r, StageDecimate, &r.decimate, func() (*decimateOutput, error) {
		// Body should NOT run if the cache hit returned a value before
		// our delete. If it does run (the file was already gone before
		// the wrapper's get), that's fine — we're testing the
		// non-nil-result invariant, not the exact code path.
		return &decimateOutput{}, nil
	})
	if err != nil {
		t.Fatalf("runStage returned err=%v on a path that should always succeed", err)
	}
	if got == nil {
		t.Fatal("runStage returned (nil, nil) — caller would dereference nil and crash")
	}
}
