package diskcache

import (
	"os"
	"path/filepath"
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
	corrupt := filepath.Join(stageDir, "key.gob")
	if err := os.WriteFile(corrupt, []byte("not gob"), 0o644); err != nil {
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

// TestSweepRemovesStaleTempFiles: leftover .tmp- files from interrupted
// writes should be cleaned up by Sweep.
func TestSweepRemovesStaleTempFiles(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir)
	stageDir := filepath.Join(dir, "test")
	os.MkdirAll(stageDir, 0o755)
	tmp := filepath.Join(stageDir, ".tmp-foo-12345")
	os.WriteFile(tmp, []byte("partial"), 0o644)
	if _, err := c.Sweep(7*24*time.Hour, 1<<40); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("stale temp file was not removed by Sweep")
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

// TestAtomicWrite: a Set should never leave a partial .gob file at the
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
		if filepath.Ext(e.Name()) != ".gob" {
			t.Errorf("non-.gob file left in cache dir: %s", e.Name())
		}
	}
}
