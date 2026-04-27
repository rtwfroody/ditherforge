package pipeline

import (
	"os"
	"path/filepath"
	"testing"
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

// TestStageMapFIFO: a stageMap evicts the oldest entry once cap is exceeded.
func TestStageMapFIFO(t *testing.T) {
	m := newStageMap(3)
	m.put("a", 1)
	m.put("b", 2)
	m.put("c", 3)
	m.put("d", 4) // pushes out 'a'
	if m.get("a") != nil {
		t.Error("oldest entry 'a' was not evicted")
	}
	if m.get("d") == nil {
		t.Error("newest entry 'd' is missing")
	}
}

// TestStageMapCapTwoToggleAB: at the production cap of 2, A↔B↔A↔B keeps
// both entries resident — the toggle case the unified cache is designed
// around.
func TestStageMapCapTwoToggleAB(t *testing.T) {
	m := newStageMap(2)
	m.put("A", "vA")
	m.put("B", "vB")
	if m.get("A") != "vA" || m.get("B") != "vB" {
		t.Fatal("setup: both entries should be present")
	}
	// Re-touching A and B (no new keys introduced) must not evict either.
	m.put("A", "vA2")
	m.put("B", "vB2")
	if m.get("A") != "vA2" {
		t.Errorf("A evicted by re-put cycle, got %v", m.get("A"))
	}
	if m.get("B") != "vB2" {
		t.Errorf("B evicted by re-put cycle, got %v", m.get("B"))
	}
}

// TestStageMapUpdate: putting the same key twice replaces the value but
// does not consume an extra slot.
func TestStageMapUpdate(t *testing.T) {
	m := newStageMap(2)
	m.put("a", 1)
	m.put("a", 99)
	m.put("b", 2)
	if m.get("a") != 99 {
		t.Errorf("a = %v, want 99 (update)", m.get("a"))
	}
	if m.get("b") != 2 {
		t.Errorf("b should still be present after update of a")
	}
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

// TestCacheAToggleBToggleAHitsMemory: the A↔B↔A scenario the user actually
// cares about. After computing for A, then B, then A again, the second
// "A" lookup must come from in-memory cache (no recompute).
func TestCacheAToggleBToggleAHitsMemory(t *testing.T) {
	c := NewStageCache()
	path := makeFakeInput(t)
	// Two opts that differ only in LayerHeight.
	optsA := Options{Input: path, Scale: 1, NozzleDiameter: 0.4, LayerHeight: 0.2, Dither: "none"}
	optsB := optsA
	optsB.LayerHeight = 0.12

	// Pretend we just computed each stage's output for A.
	doA := &decimateOutput{}
	c.set(StageDecimate, optsA, doA)
	voA := &voxelizeOutput{}
	c.set(StageVoxelize, optsA, voA)

	// Compute for B.
	doB := &decimateOutput{}
	c.set(StageDecimate, optsB, doB)
	voB := &voxelizeOutput{}
	c.set(StageVoxelize, optsB, voB)

	// Toggle back to A — must return the original instances.
	if got := c.get(StageDecimate, optsA); got != doA {
		t.Errorf("Decimate A→B→A: got different instance, expected memory hit on original")
	}
	if got := c.get(StageVoxelize, optsA); got != voA {
		t.Errorf("Voxelize A→B→A: got different instance, expected memory hit on original")
	}
	// And B's entries are still there too.
	if got := c.get(StageDecimate, optsB); got != doB {
		t.Errorf("Decimate B is missing from memory after toggle")
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
