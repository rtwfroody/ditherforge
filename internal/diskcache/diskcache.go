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
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
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
	// milliseconds. Used by Sweep to make cost-aware eviction
	// decisions: entries with high cost-per-byte are kept, low-cost
	// or huge entries evict first.
	CostMs int64 `json:"costMs"`
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
}

func (c *Cache) reportError(stage, op, key string, err error) {
	if c.OnError != nil {
		c.OnError(stage, op, key, err)
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

// Get reads, zstd-decompresses, and gob-decodes the entry into out (a
// pointer). Returns false on miss; on any decode error the file is removed
// silently and false is returned. On success, the file's mtime is bumped so
// the LRU sweep treats it as a recent access.
func (c *Cache) Get(stage, key string, out any) bool {
	p := c.pathFor(stage, key)
	f, err := os.Open(p)
	if err != nil {
		if !os.IsNotExist(err) {
			c.reportError(stage, "open", key, err)
		}
		return false
	}
	zr, err := zstd.NewReader(f)
	if err != nil {
		f.Close()
		os.Remove(p)
		c.reportError(stage, "decode", key, err)
		return false
	}
	if err := gob.NewDecoder(zr).Decode(out); err != nil {
		zr.Close()
		f.Close()
		os.Remove(p)
		c.reportError(stage, "decode", key, err)
		return false
	}
	zr.Close()
	f.Close()
	now := time.Now()
	_ = os.Chtimes(p, now, now)
	return true
}

// Set gob-encodes val, zstd-compresses, and writes the result atomically
// (temp file + rename). All errors are silently swallowed: the cache is
// best-effort and a failed write must not break the pipeline. Errors are
// reported via OnError if set.
func (c *Cache) Set(stage, key string, val any) {
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
	zw, err := zstd.NewWriter(tmp, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		tmp.Close()
		os.Remove(tmpName)
		c.reportError(stage, "encode", key, err)
		return
	}
	if err := gob.NewEncoder(zw).Encode(val); err != nil {
		zw.Close()
		tmp.Close()
		os.Remove(tmpName)
		c.reportError(stage, "encode", key, err)
		return
	}
	if err := zw.Close(); err != nil {
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

// RecordCost writes a sidecar JSON file recording how long the data file
// at (stage, key) took to generate. Sweep uses this to make cost-aware
// eviction decisions: entries with high cost-per-byte are kept, low-cost
// or huge entries evict first. Best-effort like Set; errors go through
// OnError but never fail the caller.
func (c *Cache) RecordCost(stage, key string, cost time.Duration) {
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
	md := EntryMetadata{CostMs: cost.Milliseconds()}
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
	paths       []string
	totalSize   int64
	newestMtime time.Time
	costMs      int64
}

// recencyHalfLife is how fast an entry's "value" decays as time since
// last access grows. With one day, a freshly-touched entry counts at
// full weight, a day-old entry at 50%, a week-old entry at ~0.8%
// (which the maxAge cutoff handles separately). Tied to time-since-
// access (mtime), which Get bumps on every cache hit.
const recencyHalfLife = 24 * time.Hour

// recencyFactor returns the multiplier in (0, 1] that age contributes to
// an entry's eviction score. age <= 0 (clock skew) yields 1.0.
func recencyFactor(age time.Duration) float64 {
	if age <= 0 {
		return 1.0
	}
	return math.Pow(0.5, age.Seconds()/recencyHalfLife.Seconds())
}

// Sweep walks the cache directory and removes entries by two rules:
//
//  1. Age: any entry whose newest file is older than maxAge is deleted.
//  2. Value-aware size eviction: among the remaining entries, those
//     with the lowest score are deleted first until total size fits
//     within maxBytes. The score combines three factors:
//
//         score = (costMs / sizeBytes) * 2^(-age/halflife)
//
//     Higher cost = more valuable (proportional). Larger size = less
//     valuable per byte (proportional). Older = less valuable (decays
//     exponentially with halflife = 24h). Ties fall back to oldest-
//     mtime-first, which preserves LRU semantics for legacy entries
//     with no recorded cost.
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
			e = &cacheEntry{}
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
			for _, p := range e.paths {
				os.Remove(p)
			}
			stats.AgeEvicted++
			stats.BytesFreed += e.totalSize
			continue
		}
		survivors = append(survivors, e)
	}

	// Phase 2: cost-aware size eviction.
	var total int64
	for _, e := range survivors {
		total += e.totalSize
	}
	if total <= maxBytes {
		return stats, nil
	}
	now := time.Now()
	score := func(e *cacheEntry) float64 {
		if e.totalSize <= 0 {
			return 0
		}
		base := float64(e.costMs) / float64(e.totalSize)
		return base * recencyFactor(now.Sub(e.newestMtime))
	}
	sort.Slice(survivors, func(i, j int) bool {
		si, sj := score(survivors[i]), score(survivors[j])
		if si != sj {
			return si < sj
		}
		// Tie-break: older first. Only matters when scores are
		// exactly equal (typically zero-cost legacy entries).
		return survivors[i].newestMtime.Before(survivors[j].newestMtime)
	})
	for _, e := range survivors {
		if total <= maxBytes {
			break
		}
		for _, p := range e.paths {
			os.Remove(p)
		}
		stats.SizeEvicted++
		stats.BytesFreed += e.totalSize
		total -= e.totalSize
	}
	return stats, nil
}
