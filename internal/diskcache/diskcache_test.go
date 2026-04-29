package diskcache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type payload struct {
	Name string
	Data []int
}

// TestRoundTrip writes, reads, and verifies a value survives gob serialization
// through the cache.
func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	in := payload{Name: "x", Data: []int{1, 2, 3}}
	c.Set("test", "key1", in)
	var out payload
	if !c.Get("test", "key1", &out) {
		t.Fatal("Get returned false on existing entry")
	}
	if out.Name != in.Name || len(out.Data) != len(in.Data) || out.Data[2] != 3 {
		t.Errorf("round trip mismatch: %+v vs %+v", out, in)
	}
}

// TestGetMiss returns false on a missing entry without creating any file.
func TestGetMiss(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir)
	var p payload
	if c.Get("test", "doesnotexist", &p) {
		t.Error("Get returned true for missing entry")
	}
}

// TestCorruptFileIsRemoved: a non-gob file at the cache path should be
// removed and treated as a miss, so the next Set can succeed.
func TestCorruptFileIsRemoved(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir)
	stageDir := filepath.Join(dir, "test")
	os.MkdirAll(stageDir, 0o755)
	corrupt := filepath.Join(stageDir, "key.gob.zst")
	if err := os.WriteFile(corrupt, []byte("not zstd"), 0o644); err != nil {
		t.Fatal(err)
	}
	var p payload
	if c.Get("test", "key", &p) {
		t.Error("Get returned true for corrupted file")
	}
	if _, err := os.Stat(corrupt); !os.IsNotExist(err) {
		t.Error("corrupted file was not removed on decode failure")
	}
}

// TestGetTouchesMtime ensures a successful Get bumps the file's mtime so the
// LRU sweep treats it as a recent access.
func TestGetTouchesMtime(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir)
	c.Set("test", "k", payload{Name: "x"})
	p := c.pathFor("test", "k")
	old := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	var out payload
	if !c.Get("test", "k", &out) {
		t.Fatal("Get failed")
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().After(old) {
		t.Errorf("mtime not bumped: still %v", info.ModTime())
	}
}

// TestSweepAge: entries past maxAge are evicted; recent ones are kept.
func TestSweepAge(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir)
	c.Set("test", "old", payload{Name: "old"})
	c.Set("test", "new", payload{Name: "new"})
	oldPath := c.pathFor("test", "old")
	stale := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(oldPath, stale, stale); err != nil {
		t.Fatal(err)
	}
	stats, err := c.Sweep(7*24*time.Hour, 1<<40)
	if err != nil {
		t.Fatal(err)
	}
	if stats.AgeEvicted != 1 {
		t.Errorf("AgeEvicted = %d, want 1", stats.AgeEvicted)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("aged entry was not removed")
	}
	if _, err := os.Stat(c.pathFor("test", "new")); err != nil {
		t.Error("recent entry was incorrectly removed")
	}
}

// TestSweepLRU: with total size over the budget, oldest-by-mtime entries
// are evicted until we fit. Entries within the age cutoff but evicted by
// size should count as SizeEvicted.
func TestSweepLRU(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir)
	// Three entries with distinct mtimes, all within the age cutoff. Use
	// non-zero data so gob actually allocates bytes for each int.
	mkData := func(seed int) []int {
		d := make([]int, 4000)
		for i := range d {
			d[i] = seed*100000 + i
		}
		return d
	}
	c.Set("test", "a", payload{Name: "a", Data: mkData(1)})
	c.Set("test", "b", payload{Name: "b", Data: mkData(2)})
	c.Set("test", "c", payload{Name: "c", Data: mkData(3)})
	// Verify entries are non-trivial in size.
	infoA, _ := os.Stat(c.pathFor("test", "a"))
	if infoA.Size() < 1000 {
		t.Fatalf("test setup: payload too small (%d bytes)", infoA.Size())
	}
	now := time.Now()
	os.Chtimes(c.pathFor("test", "a"), now.Add(-3*time.Hour), now.Add(-3*time.Hour))
	os.Chtimes(c.pathFor("test", "b"), now.Add(-2*time.Hour), now.Add(-2*time.Hour))
	os.Chtimes(c.pathFor("test", "c"), now.Add(-1*time.Hour), now.Add(-1*time.Hour))
	// Cap the budget so at least one entry must be evicted: 1.5x one entry's size.
	cap := infoA.Size() + infoA.Size()/2
	stats, err := c.Sweep(7*24*time.Hour, cap)
	if err != nil {
		t.Fatal(err)
	}
	if stats.SizeEvicted == 0 {
		t.Error("expected at least one SizeEvicted")
	}
	// 'a' is oldest and should be the first to go.
	if _, err := os.Stat(c.pathFor("test", "a")); !os.IsNotExist(err) {
		t.Error("oldest entry 'a' was not LRU-evicted")
	}
	// 'c' is newest and should remain.
	if _, err := os.Stat(c.pathFor("test", "c")); err != nil {
		t.Error("newest entry 'c' was incorrectly evicted")
	}
}

// TestSweepEvictsArbitraryFiles: anything in the cache directory is
// fair game — Sweep applies its rules uniformly without filtering by
// extension or name. Aged-out files (including stale .tmp- leftovers
// and entries from older cache file-name schemes) get removed once
// they cross the age cutoff.
func TestSweepEvictsArbitraryFiles(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir)
	stageDir := filepath.Join(dir, "test")
	os.MkdirAll(stageDir, 0o755)
	stale := filepath.Join(stageDir, ".tmp-foo-12345") // crashed-Set leftover
	old := filepath.Join(stageDir, "key.gob")          // pre-zstd format
	os.WriteFile(stale, []byte("partial"), 0o644)
	os.WriteFile(old, []byte("legacy"), 0o644)
	pastCutoff := time.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(stale, pastCutoff, pastCutoff)
	os.Chtimes(old, pastCutoff, pastCutoff)
	stats, err := c.Sweep(7*24*time.Hour, 1<<40)
	if err != nil {
		t.Fatal(err)
	}
	if stats.AgeEvicted != 2 {
		t.Errorf("AgeEvicted = %d, want 2", stats.AgeEvicted)
	}
	for _, p := range []string{stale, old} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("file %s was not removed by Sweep", filepath.Base(p))
		}
	}
}

// TestSweepKeepsInFlightTempFiles: an active Set goroutine has a temp
// file in the cache directory while it's mid-write. Sweep must not
// delete it — if it does, the writer's os.Rename fails ("no such file
// or directory") and the cache write is silently lost. Recent .tmp-*
// files (within the age cutoff) are off-limits.
func TestSweepKeepsInFlightTempFiles(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir)
	stageDir := filepath.Join(dir, "test")
	os.MkdirAll(stageDir, 0o755)
	// Simulate an in-flight write by creating a fresh .tmp-* file
	// that's "huge" — Sweep would otherwise be tempted to evict it
	// to fit a tight byte budget.
	tmp := filepath.Join(stageDir, ".tmp-key-789")
	os.WriteFile(tmp, make([]byte, 100_000), 0o644)
	if _, err := c.Sweep(7*24*time.Hour, 1024); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Error("in-flight temp file was incorrectly removed by sweep")
	}
}

// TestKeyStable: same parts produce the same key; different ordering or
// content produces a different key.
func TestKeyStable(t *testing.T) {
	a := Key("v1", "raw", "abc", "0")
	b := Key("v1", "raw", "abc", "0")
	if a != b {
		t.Errorf("Key not deterministic: %q vs %q", a, b)
	}
	c := Key("v1", "raw", "abc", "1")
	if a == c {
		t.Error("Key collided on differing parts")
	}
	// Ambiguity check: ("ab", "c") must differ from ("a", "bc").
	if Key("ab", "c") == Key("a", "bc") {
		t.Error("Key collision on length-prefix ambiguity")
	}
}

// TestHashFile: identical contents produce identical hashes.
func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	os.WriteFile(a, []byte("hello"), 0o644)
	os.WriteFile(b, []byte("hello"), 0o644)
	ha, err := HashFile(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := HashFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Errorf("identical contents hashed differently: %s vs %s", ha, hb)
	}
}

// TestAtomicWrite: a Set should never leave a partial file at the
// final path even if the process is interrupted mid-write. We can't
// simulate a true crash, but we can verify no .tmp- files leak after a
// successful Set.
func TestAtomicWriteNoLeftovers(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir)
	c.Set("test", "k", payload{Name: "x"})
	stageDir := filepath.Join(dir, "test")
	entries, _ := os.ReadDir(stageDir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".gob.zst") {
			t.Errorf("non-cache file left in cache dir: %s", e.Name())
		}
	}
}

// TestRecordCostRoundTrip: writing then reading a meta file gives back
// the same cost.
func TestRecordCostRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir)
	c.Set("test", "k", payload{Name: "x"})
	c.RecordCost("test", "k", "round-trip", 1234*time.Millisecond)
	metaPath := filepath.Join(dir, "test", "k.meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"costMs":1234`) {
		t.Errorf("unexpected meta content: %s", data)
	}
}

// TestSweepCostAwareEviction: among entries within the age cutoff, the
// one with the lowest cost-per-byte is evicted first when the budget
// forces an eviction.
func TestSweepCostAwareEviction(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir)
	mkData := func(seed int) []int {
		d := make([]int, 4000)
		for i := range d {
			d[i] = seed*100000 + i
		}
		return d
	}
	c.Set("test", "cheap", payload{Name: "cheap", Data: mkData(1)})
	c.Set("test", "midwa", payload{Name: "midway", Data: mkData(2)})
	c.Set("test", "spend", payload{Name: "expensive", Data: mkData(3)})

	// Roughly equal sizes (same payload shape). Costs differ by 1000x:
	// the cheap one took 1 ms, the middle took 100 ms, the expensive
	// one took 10000 ms.
	c.RecordCost("test", "cheap", "cheap entry", 1*time.Millisecond)
	c.RecordCost("test", "midwa", "midway entry", 100*time.Millisecond)
	c.RecordCost("test", "spend", "expensive entry", 10000*time.Millisecond)

	// Make 'cheap' the freshest by mtime so LRU alone would *keep* it
	// over 'spend'. Cost-awareness must override.
	now := time.Now()
	os.Chtimes(c.pathFor("test", "spend"), now.Add(-3*time.Hour), now.Add(-3*time.Hour))
	os.Chtimes(c.pathFor("test", "midwa"), now.Add(-2*time.Hour), now.Add(-2*time.Hour))
	os.Chtimes(c.pathFor("test", "cheap"), now.Add(-1*time.Hour), now.Add(-1*time.Hour))
	// And touch the meta files to match so the entry's "newest mtime"
	// matches the data file's.
	os.Chtimes(filepath.Join(dir, "test", "spend.meta.json"), now.Add(-3*time.Hour), now.Add(-3*time.Hour))
	os.Chtimes(filepath.Join(dir, "test", "midwa.meta.json"), now.Add(-2*time.Hour), now.Add(-2*time.Hour))
	os.Chtimes(filepath.Join(dir, "test", "cheap.meta.json"), now.Add(-1*time.Hour), now.Add(-1*time.Hour))

	// Budget fits ~1.5 entries (data + meta together).
	infoCheap, _ := os.Stat(c.pathFor("test", "cheap"))
	infoCheapMeta, _ := os.Stat(filepath.Join(dir, "test", "cheap.meta.json"))
	entrySize := infoCheap.Size() + infoCheapMeta.Size()
	cap := entrySize * 3 / 2
	stats, err := c.Sweep(7*24*time.Hour, cap)
	if err != nil {
		t.Fatal(err)
	}
	if stats.SizeEvicted == 0 {
		t.Fatal("expected at least one SizeEvicted")
	}
	// 'cheap' has the lowest cost/size, must be the first evicted.
	if _, err := os.Stat(c.pathFor("test", "cheap")); !os.IsNotExist(err) {
		t.Error("cheap (low-cost) entry was not the first evicted")
	}
	if _, err := os.Stat(c.pathFor("test", "spend")); err != nil {
		t.Error("expensive (high-cost) entry was incorrectly evicted")
	}
	// The data file's meta sidecar should follow it out.
	if _, err := os.Stat(filepath.Join(dir, "test", "cheap.meta.json")); !os.IsNotExist(err) {
		t.Error("evicted entry's meta sidecar was left behind")
	}
}

// TestSweepKeepsFreshOrphanMeta: a meta file with no data sibling can
// transiently exist when the meta-write goroutine wins the race against
// the data-write goroutine. Sweep must NOT delete it eagerly, because
// the data is about to land and we'd lose the cost record. It ages out
// at maxAge like everything else.
func TestSweepKeepsFreshOrphanMeta(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir)
	stageDir := filepath.Join(dir, "test")
	os.MkdirAll(stageDir, 0o755)
	orphan := filepath.Join(stageDir, "ghost.meta.json")
	if err := os.WriteFile(orphan, []byte(`{"costMs":100}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sweep(7*24*time.Hour, 1<<40); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Error("fresh orphan meta was incorrectly removed")
	}

	// Now age it past the cutoff — should be removed.
	stale := time.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(orphan, stale, stale)
	if _, err := c.Sweep(7*24*time.Hour, 1<<40); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("aged-out orphan meta was not removed")
	}
}

// TestSweepRecencyDominatesEvictionAtEqualCost: two entries with
// identical recorded cost and size differ only in age. The older one
// should evict first, since the recency factor decays its score.
func TestSweepRecencyDominatesEvictionAtEqualCost(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir)
	mkData := func(seed int) []int {
		d := make([]int, 4000)
		for i := range d {
			d[i] = seed*100000 + i
		}
		return d
	}
	c.Set("test", "fresh", payload{Name: "fresh", Data: mkData(1)})
	c.Set("test", "stale", payload{Name: "stale", Data: mkData(2)})
	c.RecordCost("test", "fresh", "fresh", 500*time.Millisecond)
	c.RecordCost("test", "stale", "stale", 500*time.Millisecond)
	now := time.Now()
	// Make 'stale' a day old so recency factor halves it; 'fresh'
	// stays roughly current.
	freshT := now.Add(-1 * time.Minute)
	staleT := now.Add(-24 * time.Hour)
	os.Chtimes(c.pathFor("test", "fresh"), freshT, freshT)
	os.Chtimes(filepath.Join(dir, "test", "fresh.meta.json"), freshT, freshT)
	os.Chtimes(c.pathFor("test", "stale"), staleT, staleT)
	os.Chtimes(filepath.Join(dir, "test", "stale.meta.json"), staleT, staleT)

	infoFresh, _ := os.Stat(c.pathFor("test", "fresh"))
	infoFreshMeta, _ := os.Stat(filepath.Join(dir, "test", "fresh.meta.json"))
	entrySize := infoFresh.Size() + infoFreshMeta.Size()
	cap := entrySize * 3 / 2 // room for ~1 entry
	if _, err := c.Sweep(7*24*time.Hour, cap); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.pathFor("test", "stale")); !os.IsNotExist(err) {
		t.Error("stale entry was not the first evicted (recency factor must lower its score)")
	}
	if _, err := os.Stat(c.pathFor("test", "fresh")); err != nil {
		t.Error("fresh entry was incorrectly evicted")
	}
}

// TestSweepLargeExpensiveBeatsSmallCheap: the user's stated rule —
// 1KB that took 1s should NOT be kept over 1000KB that took 1000s.
// With cost-per-byte alone the two would tie. The score formula's
// sub-linear size penalty (sqrt) breaks the tie in favor of the
// entry whose absolute cost is higher.
func TestSweepLargeExpensiveBeatsSmallCheap(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir)
	// Non-compressible data so on-disk size scales with logical size:
	// hash chains produce essentially random bytes that zstd can't shrink.
	mkRandomish := func(n int) []int {
		d := make([]int, n)
		x := uint64(n)*2654435761 + 0x9E3779B97F4A7C15
		for i := range d {
			x ^= x << 13
			x ^= x >> 7
			x ^= x << 17
			d[i] = int(x)
		}
		return d
	}
	// Small entry: small payload, modest cost.
	c.Set("test", "small", payload{Name: "small", Data: mkRandomish(64)})
	// Large entry: ~1000× larger payload, ~1000× more cost.
	c.Set("test", "large", payload{Name: "large", Data: mkRandomish(64000)})
	c.RecordCost("test", "small", "small/cheap", 1*time.Second)
	c.RecordCost("test", "large", "large/expensive", 1000*time.Second)

	// Both entries equally fresh, so recency doesn't pick the winner.
	now := time.Now().Add(-1 * time.Minute)
	for _, k := range []string{"small", "large"} {
		os.Chtimes(c.pathFor("test", k), now, now)
		os.Chtimes(filepath.Join(dir, "test", k+".meta.json"), now, now)
	}

	// Budget that fits the large entry (with its meta) but not also
	// the small one. Setting cap = large total + 1 forces sweep to
	// drop a single entry — the small one if scoring is correct.
	largeTotal := func() int64 {
		di, _ := os.Stat(c.pathFor("test", "large"))
		mi, _ := os.Stat(filepath.Join(dir, "test", "large.meta.json"))
		return di.Size() + mi.Size()
	}()
	cap := largeTotal + 1

	if _, err := c.Sweep(7*24*time.Hour, cap); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.pathFor("test", "small")); !os.IsNotExist(err) {
		t.Error("small/cheap entry should have been evicted: a 1000× cheaper entry must lose to a 1000× more expensive one even when it's smaller")
	}
	if _, err := os.Stat(c.pathFor("test", "large")); err != nil {
		t.Error("large/expensive entry should have survived")
	}
}

// TestSweepHugeExpensiveBeatsTinyCheap: a fresh 5KB / 0.5s Parse
// entry must NOT outrank a fresh 500KB / 60s Load entry. Without the
// size floor, the sqrt denominator's penalty against the large entry
// would invert the ranking even though the Load is 120× more
// expensive in absolute terms.
func TestSweepHugeExpensiveBeatsTinyCheap(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir)
	mkRandomish := func(n int) []int {
		d := make([]int, n)
		x := uint64(n)*2654435761 + 0x9E3779B97F4A7C15
		for i := range d {
			x ^= x << 13
			x ^= x >> 7
			x ^= x << 17
			d[i] = int(x)
		}
		return d
	}
	c.Set("test", "tiny", payload{Name: "tiny", Data: mkRandomish(64)})
	c.Set("test", "huge", payload{Name: "huge", Data: mkRandomish(64000)})
	c.RecordCost("test", "tiny", "tiny/cheap", 500*time.Millisecond)
	c.RecordCost("test", "huge", "huge/expensive", 60*time.Second)

	now := time.Now().Add(-1 * time.Minute)
	for _, k := range []string{"tiny", "huge"} {
		os.Chtimes(c.pathFor("test", k), now, now)
		os.Chtimes(filepath.Join(dir, "test", k+".meta.json"), now, now)
	}

	hugeTotal := func() int64 {
		di, _ := os.Stat(c.pathFor("test", "huge"))
		mi, _ := os.Stat(filepath.Join(dir, "test", "huge.meta.json"))
		return di.Size() + mi.Size()
	}()
	cap := hugeTotal + 1

	if _, err := c.Sweep(7*24*time.Hour, cap); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.pathFor("test", "tiny")); !os.IsNotExist(err) {
		t.Error("tiny/cheap should have been evicted: a 120× cheaper entry must lose to a 120× more expensive one even though it's smaller")
	}
	if _, err := os.Stat(c.pathFor("test", "huge")); err != nil {
		t.Error("huge/expensive should have survived")
	}
}

// TestSweepFallbackToLRUWhenNoCosts: when no entries have recorded
// costs (legacy / pre-cost-tracking entries), eviction falls back to
// oldest-mtime-first, matching the previous LRU behavior.
func TestSweepFallbackToLRUWhenNoCosts(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir)
	mkData := func(seed int) []int {
		d := make([]int, 4000)
		for i := range d {
			d[i] = seed*100000 + i
		}
		return d
	}
	c.Set("test", "a", payload{Name: "a", Data: mkData(1)})
	c.Set("test", "b", payload{Name: "b", Data: mkData(2)})
	c.Set("test", "c", payload{Name: "c", Data: mkData(3)})
	// No RecordCost calls — all entries have zero cost.
	now := time.Now()
	os.Chtimes(c.pathFor("test", "a"), now.Add(-3*time.Hour), now.Add(-3*time.Hour))
	os.Chtimes(c.pathFor("test", "b"), now.Add(-2*time.Hour), now.Add(-2*time.Hour))
	os.Chtimes(c.pathFor("test", "c"), now.Add(-1*time.Hour), now.Add(-1*time.Hour))
	infoA, _ := os.Stat(c.pathFor("test", "a"))
	cap := infoA.Size() + infoA.Size()/2
	if _, err := c.Sweep(7*24*time.Hour, cap); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.pathFor("test", "a")); !os.IsNotExist(err) {
		t.Error("LRU-fallback: oldest entry 'a' should have been evicted")
	}
}
