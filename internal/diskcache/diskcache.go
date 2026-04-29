// Package diskcache provides a small content-addressed cache stored as
// zstd-compressed gob files under a single directory. It is used to persist
// per-stage pipeline outputs across app restarts. Each entry is a data
// file (`<key>.gob.zst`) plus an optional sidecar metadata file
// (`<key>.meta.json`, see EntryMetadata) recording how long the data took
// to generate. Sweep evicts in two phases: anything past maxAge is dropped,
// then survivors are sorted by a value score combining generation cost,
// size, and recency, and the lowest-scoring entries are evicted until the
// total fits within a byte budget.
package diskcache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rtwfroody/ditherforge/internal/cacheblob"
	"github.com/rtwfroody/ditherforge/internal/cachepolicy"
)

// dataExt and metaExt are the file extensions for cache data files and
// their sidecar metadata. The metadata file is JSON (rather than gob or a
// fixed binary layout) so future fields can be added without bumping a
// format version.
const (
	dataExt = ".gob.zst"
	metaExt = ".meta.json"
)

// EntryMetadata is the JSON shape of a sidecar .meta.json file.
type EntryMetadata struct {
	// CostMs is how long the data file took to generate, in
	// milliseconds. Used by Sweep to score entries for eviction:
	// expensive-to-regenerate entries are kept longer.
	CostMs int64 `json:"costMs"`
	// Description is a short human-readable summary of what the
	// entry contains (e.g. "Load: foo.glb (alpha-wrap)"). Printed
	// during sweep so the operator can see what's being evicted.
	Description string `json:"description,omitempty"`
}


// Cache is rooted at a single directory. Each "stage" gets its own
// subdirectory and entries are zstd-compressed gob files named
// "<key>.gob.zst".
type Cache struct {
	Dir string
	// OnError, if non-nil, is called whenever Set or Get encounters an
	// error that the cache silently swallows (write/read/encode/decode
	// failures, atomic-rename failures, etc.). The cache is best-effort
	// and never returns these errors to callers, so this hook is the only
	// way to surface them. op is one of "open", "encode", "decode",
	// "write", "rename", "mkdir", "tempfile". Set OnError once at
	// construction; reassignment after the cache is in use is racy because
	// Set may invoke it from a goroutine.
	OnError func(stage, op, key string, err error)
	// OnEvict, if non-nil, is called for each entry Sweep removes,
	// before the files are deleted. reason is "age" (past maxAge) or
	// "size" (cost-aware eviction to fit the budget). Description is
	// the meta-recorded human-readable summary, or "" if absent.
	OnEvict func(stage, description, reason string, sizeBytes, costMs int64)
}

func (c *Cache) reportError(stage, op, key string, err error) {
	if c.OnError != nil {
		c.OnError(stage, op, key, err)
	}
}

func (c *Cache) reportEvict(stage, description, reason string, sizeBytes, costMs int64) {
	if c.OnEvict != nil {
		c.OnEvict(stage, description, reason, sizeBytes, costMs)
	}
}

// Open creates the cache directory if needed and returns a Cache handle.
func Open(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Cache{Dir: dir}, nil
}

// DefaultDir returns $UserCacheDir/ditherforge.
func DefaultDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "ditherforge"), nil
}

// Key composes a content-addressed cache key from its parts. Length-prefixing
// each part avoids ambiguity (e.g. "ab"+"c" vs "a"+"bc"). The result is the
// first 32 hex chars of sha256 — 128 bits, enough for any plausible cache.
func Key(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s\x00", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// HashFile returns the hex-encoded sha256 of a file's contents.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (c *Cache) pathFor(stage, key string) string {
	return filepath.Join(c.Dir, stage, key+dataExt)
}

// GetBlob reads the raw cacheblob bytes for (stage, key). Returns nil
// on miss. On success the file's mtime is bumped so the sweep treats
// this as a recent access. Decode errors are not detected here — the
// caller decides whether to decode the blob.
func (c *Cache) GetBlob(stage, key string) []byte {
	p := c.pathFor(stage, key)
	data, err := os.ReadFile(p)
	if err != nil {
		if !os.IsNotExist(err) {
			c.reportError(stage, "open", key, err)
		}
		return nil
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now)
	return data
}

// SetBlob writes a pre-encoded cacheblob to disk atomically (temp file
// + rename). Errors are silently swallowed and routed through OnError.
func (c *Cache) SetBlob(stage, key string, blob []byte) {
	dir := filepath.Join(c.Dir, stage)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.reportError(stage, "mkdir", key, err)
		return
	}
	final := filepath.Join(dir, key+dataExt)
	tmp, err := os.CreateTemp(dir, ".tmp-"+key+"-*")
	if err != nil {
		c.reportError(stage, "tempfile", key, err)
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		c.reportError(stage, "write", key, err)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		c.reportError(stage, "write", key, err)
		return
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		c.reportError(stage, "rename", key, err)
	}
}

// Remove deletes the data file (and meta sidecar, if any) for
// (stage, key). Errors are routed through OnError. Used by callers
// that decoded the blob themselves and discovered it was corrupt;
// removing the bad file means the next access misses cleanly and
// recomputes instead of silently failing forever.
func (c *Cache) Remove(stage, key string) {
	if err := os.Remove(c.pathFor(stage, key)); err != nil && !os.IsNotExist(err) {
		c.reportError(stage, "remove", key, err)
	}
	metaPath := filepath.Join(c.Dir, stage, key+metaExt)
	if err := os.Remove(metaPath); err != nil && !os.IsNotExist(err) {
		c.reportError(stage, "remove", key, err)
	}
}

// Get reads, zstd-decompresses, and gob-decodes the entry into out (a
// pointer). Returns false on miss; on any decode error the file is
// removed silently and false is returned.
func (c *Cache) Get(stage, key string, out any) bool {
	blob := c.GetBlob(stage, key)
	if blob == nil {
		return false
	}
	if err := cacheblob.Decode(blob, out); err != nil {
		os.Remove(c.pathFor(stage, key))
		c.reportError(stage, "decode", key, err)
		return false
	}
	return true
}

// Set encodes val with cacheblob and writes the result atomically.
// Errors are silently swallowed and routed through OnError.
func (c *Cache) Set(stage, key string, val any) {
	blob, err := cacheblob.Encode(val)
	if err != nil {
		c.reportError(stage, "encode", key, err)
		return
	}
	c.SetBlob(stage, key, blob)
}

// RecordCost writes a sidecar JSON file recording how long the data file
// at (stage, key) took to generate, and a short human-readable description
// of what the entry contains. Sweep uses cost to score entries for
// eviction; description is shown in the sweep printout so the operator
// can see what's being removed. Best-effort like Set; errors go through
// OnError but never fail the caller.
func (c *Cache) RecordCost(stage, key, description string, cost time.Duration) {
	dir := filepath.Join(c.Dir, stage)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.reportError(stage, "mkdir", key, err)
		return
	}
	final := filepath.Join(dir, key+metaExt)
	tmp, err := os.CreateTemp(dir, ".tmp-meta-"+key+"-*")
	if err != nil {
		c.reportError(stage, "tempfile", key, err)
		return
	}
	tmpName := tmp.Name()
	md := EntryMetadata{CostMs: cost.Milliseconds(), Description: description}
	if err := json.NewEncoder(tmp).Encode(md); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		c.reportError(stage, "encode", key, err)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		c.reportError(stage, "write", key, err)
		return
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		c.reportError(stage, "rename", key, err)
	}
}

// SweepStats summarizes a Sweep run. Counts are per logical cache entry
// (a data file plus its optional meta sidecar = one entry).
type SweepStats struct {
	AgeEvicted  int
	SizeEvicted int
	BytesFreed  int64
}

// cacheEntry groups the on-disk files that belong to one logical cache
// entry: a `<key>.gob.zst` data file plus its optional `<key>.meta.json`
// sidecar. Files in the cache directory that don't fit either suffix
// (stale .tmp- leftovers, files from older formats) are also tracked as
// single-file entries with no cost.
type cacheEntry struct {
	stage       string
	paths       []string
	totalSize   int64
	newestMtime time.Time
	costMs      int64
	description string
}

// Sweep walks the cache directory and removes entries by two rules:
//
//  1. Age: any entry whose newest file is older than maxAge is deleted.
//  2. Value-aware size eviction: among the remaining entries, those
//     with the lowest cachepolicy.Score are deleted first until total
//     size fits within maxBytes. The score balances generation cost
//     (more valuable), size (less valuable per byte, sqrt-shaped so
//     huge expensive entries still beat tiny cheap ones), and recency
//     (decays exponentially over cachepolicy.HalfLife).
//
// Errors on individual files are ignored so a single unreadable file
// doesn't abort the sweep.
func (c *Cache) Sweep(maxAge time.Duration, maxBytes int64) (SweepStats, error) {
	var stats SweepStats
	cutoff := time.Now().Add(-maxAge)

	// Group files by (directory, base-key). The directory part keeps
	// entries from different stages distinct even if they share a key.
	entries := map[string]*cacheEntry{}
	err := filepath.WalkDir(c.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		base := filepath.Base(path)
		dir := filepath.Dir(path)
		// In-flight write temp files: an active Set goroutine is
		// renaming this file into place. Sweep must not touch it
		// or the rename fails ("no such file or directory") and the
		// cache write is silently lost. Past the age cutoff they're
		// crash leftovers and safe to delete.
		if strings.HasPrefix(base, ".tmp-") {
			if info.ModTime().Before(cutoff) {
				if rmErr := os.Remove(path); rmErr == nil {
					stats.AgeEvicted++
					stats.BytesFreed += info.Size()
				}
			}
			return nil
		}
		var groupID string
		var isMeta bool
		switch {
		case strings.HasSuffix(base, dataExt):
			groupID = dir + "/" + strings.TrimSuffix(base, dataExt)
		case strings.HasSuffix(base, metaExt):
			groupID = dir + "/" + strings.TrimSuffix(base, metaExt)
			isMeta = true
		default:
			// Unrecognized file: treat as its own single-file entry
			// with cost 0. It'll age out at maxAge or be evicted
			// early (cost=0 wins ties for "lowest value").
			groupID = path
		}
		e, ok := entries[groupID]
		if !ok {
			e = &cacheEntry{stage: filepath.Base(dir)}
			entries[groupID] = e
		}
		e.paths = append(e.paths, path)
		e.totalSize += info.Size()
		if info.ModTime().After(e.newestMtime) {
			e.newestMtime = info.ModTime()
		}
		if isMeta {
			// Read failures here silently leave costMs=0, which
			// makes the entry top eviction bait. Surface via
			// OnError so a real corruption (vs. just "file missing")
			// is visible to the operator.
			stage := filepath.Base(dir)
			key := strings.TrimSuffix(base, metaExt)
			if data, err := os.ReadFile(path); err != nil {
				c.reportError(stage, "decode", key, err)
			} else {
				var md EntryMetadata
				if err := json.Unmarshal(data, &md); err != nil {
					c.reportError(stage, "decode", key, err)
				} else {
					e.costMs = md.CostMs
					e.description = md.Description
				}
			}
		}
		return nil
	})
	if err != nil {
		return stats, err
	}

	// Phase 1: age cutoff. Entries past maxAge are deleted regardless
	// of cost.
	//
	// Orphan metas (a meta sidecar with no data sibling) are NOT
	// dropped eagerly here. They can briefly exist between the
	// data-write goroutine and the meta-write goroutine racing to
	// land their files: if the meta lands first and a sweep runs in
	// between, eagerly evicting the meta would lose the cost record
	// for an entry whose data was about to be written. Metas are
	// tiny (tens of bytes) and harmless to keep around; they age
	// out at maxAge like everything else.
	survivors := make([]*cacheEntry, 0, len(entries))
	for _, e := range entries {
		if e.newestMtime.Before(cutoff) {
			c.reportEvict(e.stage, e.description, "age", e.totalSize, e.costMs)
			for _, p := range e.paths {
				os.Remove(p)
			}
			stats.AgeEvicted++
			stats.BytesFreed += e.totalSize
			continue
		}
		survivors = append(survivors, e)
	}

	// Phase 2: cost-aware size eviction. Delegate ranking to
	// cachepolicy; the returned indices line up with survivors.
	policyEntries := make([]cachepolicy.Entry, len(survivors))
	for i, e := range survivors {
		policyEntries[i] = cachepolicy.Entry{
			Stage:       e.stage,
			Description: e.description,
			SizeBytes:   e.totalSize,
			CostMs:      e.costMs,
			Mtime:       e.newestMtime,
		}
	}
	for _, idx := range cachepolicy.FitToBudget(policyEntries, maxBytes, time.Now()) {
		e := survivors[idx]
		c.reportEvict(e.stage, e.description, "size", e.totalSize, e.costMs)
		for _, p := range e.paths {
			os.Remove(p)
		}
		stats.SizeEvicted++
		stats.BytesFreed += e.totalSize
	}
	return stats, nil
}
