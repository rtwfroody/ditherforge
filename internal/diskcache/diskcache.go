// Package diskcache provides a small content-addressed cache stored as
// zstd-compressed gob files under a single directory. It is used to persist
// per-stage pipeline outputs across app restarts. Eviction is mtime-driven:
// a startup sweep removes entries past a maximum age and then evicts the
// oldest entries until the total size fits within a budget.
package diskcache

import (
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/klauspost/compress/zstd"
)


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
	return filepath.Join(c.Dir, stage, key+".gob.zst")
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
	final := filepath.Join(dir, key+".gob.zst")
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

// SweepStats summarizes a Sweep run.
type SweepStats struct {
	AgeEvicted  int
	SizeEvicted int
	BytesFreed  int64
}

// Sweep walks the cache directory and removes entries:
//
//  1. Files with mtime older than maxAge.
//  2. Oldest-by-mtime files until total size <= maxBytes.
//
// Every file under c.Dir is a candidate — there's no name- or
// extension-specific filtering. The cache directory is dedicated to this
// cache, so anything in it is owned by us and safe to evict. This also
// means stale temp files from interrupted writes, and any leftovers from
// older cache file-name schemes, get cleaned up by the same rules.
//
// Errors on individual entries are ignored so a single unreadable file
// doesn't abort the sweep.
func (c *Cache) Sweep(maxAge time.Duration, maxBytes int64) (SweepStats, error) {
	var stats SweepStats
	type entry struct {
		path  string
		size  int64
		mtime time.Time
	}
	var keep []entry
	cutoff := time.Now().Add(-maxAge)
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
		if info.ModTime().Before(cutoff) {
			if rmErr := os.Remove(path); rmErr == nil {
				stats.AgeEvicted++
				stats.BytesFreed += info.Size()
			}
			return nil
		}
		keep = append(keep, entry{path, info.Size(), info.ModTime()})
		return nil
	})
	if err != nil {
		return stats, err
	}
	var total int64
	for _, e := range keep {
		total += e.size
	}
	if total <= maxBytes {
		return stats, nil
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].mtime.Before(keep[j].mtime) })
	for _, e := range keep {
		if total <= maxBytes {
			break
		}
		if rmErr := os.Remove(e.path); rmErr == nil {
			stats.SizeEvicted++
			stats.BytesFreed += e.size
			total -= e.size
		}
	}
	return stats, nil
}
