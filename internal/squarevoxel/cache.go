package squarevoxel

import (
	"crypto/sha256"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

const (
	cacheFormatVersion = "ditherforge-cache-v2"
	cacheMaxAge        = 7 * 24 * time.Hour
)

// CacheData holds the intermediate state after voxelization,
// enabling fast re-runs with different palette/dithering options.
type CacheData struct {
	Version       string
	KeyHash       [32]byte
	Cells         []voxel.ActiveCell
	CellAssignMap map[voxel.CellKey]int
	MinV          [3]float32
	CellSize      float32
	LayerH        float32
}

// CacheOptions identifies an input file and config for cache lookup.
type CacheOptions struct {
	InputPath  string   // path to the input .glb file (for content hashing)
	ConfigHash [32]byte // hash of all cache-relevant args + version
}

// LoadCache attempts to load cached voxelization data. Returns nil if
// no valid cache exists.
func LoadCache(opts CacheOptions) *CacheData {
	if opts.InputPath == "" {
		return nil
	}
	key, err := computeCacheKey(opts.InputPath, opts.ConfigHash)
	if err != nil {
		return nil
	}
	path, err := cachePath(opts.InputPath, key)
	if err != nil {
		return nil
	}
	cd, err := loadCacheFile(path, key)
	if err != nil {
		return nil
	}
	fmt.Printf("  Using cached voxelization (%d cells)\n", len(cd.Cells))
	return cd
}

// SaveCache writes the cache data to disk and cleans up old entries.
func SaveCache(data *CacheData, opts CacheOptions) {
	if opts.InputPath == "" {
		return
	}
	key, err := computeCacheKey(opts.InputPath, opts.ConfigHash)
	if err != nil {
		return
	}
	path, err := cachePath(opts.InputPath, key)
	if err != nil {
		return
	}
	data.Version = cacheFormatVersion
	data.KeyHash = key
	if err := saveCacheFile(path, data); err == nil {
		info, _ := os.Stat(path)
		if info != nil {
			fmt.Printf("  Saved cache (%.1f MB)\n", float64(info.Size())/(1024*1024))
		}
	}
	go cleanCache()
}

func computeCacheKey(inputPath string, configHash [32]byte) ([32]byte, error) {
	f, err := os.Open(inputPath)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, err
	}
	h.Write(configHash[:])

	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key, nil
}

func cacheDir() (string, error) {
	if dir := os.Getenv("DITHERFORGE_CACHE_DIR"); dir != "" {
		return dir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "ditherforge"), nil
}

func cachePath(inputPath string, key [32]byte) (string, error) {
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	base := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
	return filepath.Join(dir, fmt.Sprintf("%s.%x.dfcache", base, key[:4])), nil
}


func saveCacheFile(path string, data *CacheData) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w, err := zstd.NewWriter(f)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(w).Encode(data); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

func loadCacheFile(path string, expectedKey [32]byte) (*CacheData, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r, err := zstd.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var data CacheData
	if err := gob.NewDecoder(r).Decode(&data); err != nil {
		return nil, err
	}
	if data.Version != cacheFormatVersion {
		return nil, fmt.Errorf("cache version mismatch: %s != %s", data.Version, cacheFormatVersion)
	}
	if data.KeyHash != expectedKey {
		return nil, fmt.Errorf("cache key mismatch")
	}
	return &data, nil
}

func cleanCache() {
	dir, err := cacheDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-cacheMaxAge)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".dfcache") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
