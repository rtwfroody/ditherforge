package pipeline

import (
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rtwfroody/ditherforge/internal/cacheblob"
	"github.com/rtwfroody/ditherforge/internal/diskcache"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/plog"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/split"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// StageID identifies a pipeline stage.
type StageID int

const (
	// StageParse parses the input file into a pristine *LoadedModel in
	// file units, with no transformations applied. Output is small and
	// only depends on (Input, ObjectIndex). Replaced what used
	// to be a separate "raw cache" living outside the stages array.
	StageParse StageID = iota
	// StageLoad transforms the parsed model into a usable loadOutput:
	// clones, scales, normalizes Z, optionally alpha-wraps, builds the
	// preview MeshData. Alpha-wrap (the slow part) is folded into this
	// stage's body, not a separate stage — so the on-disk cache for
	// StageLoad subsumes what used to be a separate alpha-wrap cache.
	StageLoad
	// StageSplit cuts the watertight loaded mesh in two and lays the
	// halves out side-by-side on the bed (see docs/SPLIT.md). The
	// Decimate, Voxelize, and downstream stages consume the split
	// output when Options.Split.Enabled is true. When disabled, the
	// stage is a passthrough.
	StageSplit
	StageDecimate
	StageSticker // builds decals from mesh, before voxelization
	StageVoxelize
	StageColorAdjust
	StageColorWarp
	StagePalette
	StageDither
	StageClip
	StageMerge
	numStages
)

// stageSubdir returns the on-disk subdirectory for a stage. Used as the
// "stage" argument to diskcache.Cache.{Get,Set}.
func stageSubdir(s StageID) string {
	switch s {
	case StageParse:
		return "parse"
	case StageLoad:
		return "load"
	case StageSplit:
		return "split"
	case StageDecimate:
		return "decimate"
	case StageSticker:
		return "sticker"
	case StageVoxelize:
		return "voxelize"
	case StageColorAdjust:
		return "coloradjust"
	case StageColorWarp:
		return "colorwarp"
	case StagePalette:
		return "palette"
	case StageDither:
		return "dither"
	case StageClip:
		return "clip"
	case StageMerge:
		return "merge"
	}
	return "unknown"
}

// stageDescription returns a short human-readable summary of what an
// entry for (stage, opts) contains. Stored in the disk-cache meta
// sidecar and printed during sweeps so the operator can see what's
// being evicted ("Load: foo.glb (alpha-wrap)" beats an opaque hash).
func stageDescription(stage StageID, opts Options) string {
	base := filepath.Base(opts.Input)
	switch stage {
	case StageParse:
		return fmt.Sprintf("Parse: %s", base)
	case StageLoad:
		s := fmt.Sprintf("Load: %s", base)
		if opts.AlphaWrap {
			s += " (alpha-wrap)"
		}
		return s
	case StageSplit:
		if !opts.Split.Enabled {
			return fmt.Sprintf("Split: %s (off)", base)
		}
		axisName := []string{"X", "Y", "Z"}[opts.Split.Axis]
		countStr := fmt.Sprintf("×%d", opts.Split.ConnectorCount)
		if opts.Split.ConnectorCount == 0 {
			countStr = "×auto"
		}
		return fmt.Sprintf("Split: %s (%s@%.1fmm, %s %s)",
			base, axisName, opts.Split.Offset, opts.Split.ConnectorStyle, countStr)
	case StageDecimate:
		return fmt.Sprintf("Decimate: %s @ %.2fmm", base, opts.NozzleDiameter)
	case StageSticker:
		return fmt.Sprintf("Stickers: %s (%d)", base, len(opts.Stickers))
	case StageVoxelize:
		return fmt.Sprintf("Voxelize: %s @ %.2f/%.2fmm", base, opts.NozzleDiameter, opts.LayerHeight)
	case StageColorAdjust:
		return fmt.Sprintf("Color adjust: %s (B%+.0f C%+.0f S%+.0f)",
			base, opts.Brightness, opts.Contrast, opts.Saturation)
	case StageColorWarp:
		return fmt.Sprintf("Color warp: %s (%d pins)", base, len(opts.WarpPins))
	case StagePalette:
		return fmt.Sprintf("Palette: %s (%d colors)", base, opts.NumColors)
	case StageDither:
		mode := opts.Dither
		if mode == "" {
			mode = "default"
		}
		return fmt.Sprintf("Dither: %s (%s)", base, mode)
	case StageClip:
		return fmt.Sprintf("Clip: %s", base)
	case StageMerge:
		return fmt.Sprintf("Merge: %s", base)
	}
	return base
}

// StageCache holds per-stage cached outputs as compressed cacheblob
// bytes on disk. There is no separate in-memory tier of compressed
// blobs: the OS page cache keeps recent reads resident and decode
// (zstd + gob) dominates hit latency anyway, so a process-local copy
// of the same compressed bytes earns very little. Within a single
// pipeline invocation, pipelineRun (run.go) memoizes the live decoded
// struct so a stage is decoded at most once per run.
type StageCache struct {
	// disk persists cacheblobs across app restarts. nil = caching
	// disabled (everything recomputes; tests use this).
	disk *diskcache.Cache

	// diskWrites tracks async disk-write goroutines so the app can wait
	// for them at shutdown. Without this, the OS kills mid-flight writes
	// (which take seconds for big payloads like a 400 MB load entry),
	// leaving the cache incomplete and the next session re-doing work
	// that should have hit the cache.
	diskWrites sync.WaitGroup

	// inputHash caches sha256 of the current input file's contents so we
	// don't re-hash on every key derivation within a session. Tracked by
	// (path, mtime, size); a change to any forces a re-hash.
	inputHashPath  string
	inputHashMtime time.Time
	inputHashSize  int64
	inputHash      string

	// invContents caches the inventory file's contents (used in
	// paletteSettings) so stageFnv doesn't re-read the file on every
	// cache lookup. Tracked by (path, mtime, size) like inputHash.
	invContentsPath  string
	invContentsMtime time.Time
	invContentsSize  int64
	invContents      string
}

// NewStageCache returns an empty stage cache with no disk persistence.
// Use SetDisk to attach a disk tier.
func NewStageCache() *StageCache {
	return &StageCache{}
}

// SetDisk attaches a disk cache. Call this once after NewStageCache; passing
// nil keeps persistence disabled.
func (c *StageCache) SetDisk(d *diskcache.Cache) {
	c.disk = d
}

// runStageCached is the canonical wrapper every pipeline stage uses. It:
//
//   - returns immediately on a cache hit, emitting a single "completed"
//     stage marker so the UI shows the stage as done;
//   - on a miss, times the body, lets body emit its own progress markers
//     (some stages are spinners, some have determinate progress bars from
//     inner functions like DecimateMesh / VoxelizeTwoGrids), and on
//     success calls stampCost to back-fill the disk meta sidecar with
//     the wall-clock generation time.
//
// body is responsible only for producing and persisting the stage's
// result. In normal use, callers reach this helper via runStage (in
// run.go), which wraps body to memoize the live pointer into
// pipelineRun and queue the async cache.set. After body returns
// successfully, runStageCached calls stampCost to back-fill the
// disk-side meta sidecar with description and wall-clock duration.
//
// Direct callers are rare; prefer runStage.
func runStageCached(
	cache *StageCache,
	stage StageID,
	opts Options,
	tracker progress.Tracker,
	body func() error,
) error {
	name := stageNames[stage]
	key := cache.stageKey(stage, opts)
	getStart := time.Now()
	v, src := cache.getWithSource(stage, opts)
	if v != nil {
		plog.Printf("%s: cache hit (%s, %s) key=%s", name,
			hitSourceLabel(src), time.Since(getStart).Round(time.Microsecond),
			shortKey(key))
		progress.BeginStage(tracker, name, false, 0).Done()
		return nil
	}
	plog.Printf("%s: starting (cache miss key=%s)", name, shortKey(key))
	start := time.Now()
	if err := body(); err != nil {
		// Errored runs don't record cost. The body may not have
		// written its result (or wrote a partial), so a meta
		// pointing at it would be misleading.
		plog.Printf("%s: failed after %s — %v", name,
			time.Since(start).Round(time.Millisecond), err)
		return err
	}
	plog.Printf("%s: done in %s", name,
		time.Since(start).Round(time.Millisecond))
	// Body wrote the blob via the typed setter. Stamp the disk
	// meta sidecar with description and wall-clock cost so the
	// next sweep can rank this entry correctly.
	cache.stampCost(stage, opts, time.Since(start))
	return nil
}

// shortKey returns the first 12 hex chars of a stage cache key — enough
// to disambiguate runs in console logs without dumping the full SHA. An
// empty key (input file unhashable) renders as "?".
func shortKey(key string) string {
	if key == "" {
		return "?"
	}
	if len(key) > 12 {
		return key[:12]
	}
	return key
}

// hitSourceLabel returns a short label for console messages.
func hitSourceLabel(s hitSource) string {
	if s == hitDisk {
		return "disk"
	}
	return "miss"
}

// WaitForDiskWrites blocks until all in-flight async disk writes have
// completed. Call from shutdown so a 400 MB compressed load entry
// doesn't get its goroutine killed mid-flight by process exit.
func (c *StageCache) WaitForDiskWrites() {
	c.diskWrites.Wait()
}

// Disk returns the attached disk cache, or nil if persistence is disabled.
func (c *StageCache) Disk() *diskcache.Cache {
	return c.disk
}

// inventoryContents returns the inventory file's contents, memoized within
// the session by (path, mtime, size). Returns "" if the file can't be read.
// Used by stageFnv for paletteSettings, which is called many times per run.
func (c *StageCache) inventoryContents(path string) string {
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if c.invContentsPath == path &&
		c.invContentsMtime.Equal(info.ModTime()) && c.invContentsSize == info.Size() {
		return c.invContents
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	c.invContents = string(data)
	c.invContentsPath = path
	c.invContentsMtime = info.ModTime()
	c.invContentsSize = info.Size()
	return c.invContents
}

// inputContentHash returns a sha256 of opts.Input's contents, memoized within
// the session by (path, mtime, size). Returns "" on stat or read failure;
// callers treat that as "no disk caching for this run".
func (c *StageCache) inputContentHash(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if c.inputHash != "" && c.inputHashPath == path &&
		c.inputHashMtime.Equal(info.ModTime()) && c.inputHashSize == info.Size() {
		return c.inputHash
	}
	h, err := diskcache.HashFile(path)
	if err != nil {
		return ""
	}
	c.inputHash = h
	c.inputHashPath = path
	c.inputHashMtime = info.ModTime()
	c.inputHashSize = info.Size()
	return h
}

// stageKey returns the unified cache key for a stage. It's a hex sha256 of
// the version, the input file's content hash, and the FNV hashes of every
// stage's individual settings up through (and including) `stage`. Two key
// properties:
//
//   - Cascade: changing an upstream stage's settings changes every
//     downstream stage's key. No explicit invalidation needed.
//   - Determinism: identical (file content, settings) on a different machine
//     or app restart yields the same key — disk hits work across sessions.
//
// Returns "" if the file can't be hashed; callers must treat that as "no
// caching for this run" and recompute every stage.
func (c *StageCache) stageKey(stage StageID, opts Options) string {
	fh := c.inputContentHash(opts.Input)
	if fh == "" {
		return ""
	}
	parts := make([]string, 0, 3+int(stage)+1)
	parts = append(parts, Version, fh)
	for s := StageID(0); s <= stage; s++ {
		parts = append(parts, fmt.Sprintf("%016x", c.stageFnv(s, opts)))
	}
	return diskcache.Key(parts...)
}


// sizeKeyPart formats opts.Size (*float32) into a stable key string. nil is
// distinct from any concrete value.
func sizeKeyPart(s *float32) string {
	if s == nil {
		return "nil"
	}
	return fmt.Sprintf("%g", *s)
}

// Per-stage output types.

type loadOutput struct {
	// Model is the geometry model used for voxelization, decimation, and
	// clip-shell construction. When alpha-wrap is enabled, Model is the
	// wrapped (cleaned) mesh; otherwise it aliases ColorModel.
	Model *loader.LoadedModel
	// ColorModel is the original loaded mesh, carrying UVs, textures, and
	// materials. Used for color sampling and sticker placement. When
	// alpha-wrap is disabled, Model == ColorModel.
	ColorModel *loader.LoadedModel
	// SampleModel is the mesh used for per-voxel color sampling. When the
	// geometry mesh (Model) has been grown by a step like alpha-wrap,
	// SampleModel is the original mesh inflated along vertex normals so its
	// surface roughly matches Model's. Otherwise SampleModel aliases
	// ColorModel.
	SampleModel  *loader.LoadedModel
	InputMesh    *MeshData
	PreviewScale float32 // scale factor to convert pipeline coords back to preview coords
	ExtentMM     float32 // native max bounding-box extent in mm (scale=1.0, size=unset)
	// appliedBaseColor / appliedBaseColorMaterialX{,TileMM,TriplanarSharpness}
	// track the base-color override currently baked into ColorModel /
	// SampleModel FaceBaseColor. The tuple is the cache key for the
	// in-place mutation: when any field diverges from the corresponding
	// opts.* value, applyBaseColor resets from the parse cache and re-bakes.
	// All empty/zero means pristine.
	appliedBaseColor                            string
	appliedBaseColorMaterialX                   string
	appliedBaseColorMaterialXTileMM             float64
	appliedBaseColorMaterialXTriplanarSharpness float64
}

type voxelizeOutput struct {
	Cells         []voxel.ActiveCell
	CellAssignMap map[voxel.CellKey]int
	MinV          [3]float32
	Layer0Size    float32
	UpperSize     float32
	LayerH        float32

	// neighbors caches the two-grid neighbor table. Voxel topology only
	// changes on StageVoxelize, so dither re-runs (same cells, different
	// dither mode) can reuse the table instead of rebuilding it. Valid for
	// the lifetime of this voxelizeOutput; never mutate Cells in place.
	// Unexported so gob skips it on the disk round-trip; rebuilt on demand
	// by getNeighbors().
	neighbors    [][]voxel.Neighbor
	neighborOnce sync.Once
}

// getNeighbors returns the two-grid neighbor table, building it on first
// call. sync.Once makes the lazy build safe even if a future change
// introduces concurrent readers — without it, a downstream reader and the
// disk-encode goroutine kicked off by setVoxelize would race on the
// neighbors field. (gob skips unexported fields so the encode goroutine
// doesn't touch this directly, but the invariant is easier to keep when
// the synchronization is explicit.)
func (vo *voxelizeOutput) getNeighbors() [][]voxel.Neighbor {
	vo.neighborOnce.Do(func() {
		vo.neighbors = voxel.BuildTwoGridNeighbors(vo.Cells, vo.Layer0Size, vo.UpperSize, vo.MinV)
	})
	return vo.neighbors
}

type stickerOutput struct {
	Decals []*voxel.StickerDecal
	// Model is the sticker substrate (scratch clone of either ColorModel or
	// the alpha-wrapped Model, depending on opts.AlphaWrap). The BFS may
	// have subdivided pathologically-large triangles in place. Decal TriUVs
	// index into THIS model's Faces, so downstream sampling and preview
	// rendering must use Model. nil when no stickers were built.
	Model *loader.LoadedModel
	// FromAlphaWrap is true when Model is a clone of the wrap mesh rather
	// than ColorModel. Voxelize uses this to decide whether to do a single
	// nearest-tri lookup (Model == sample model) or two separate lookups.
	FromAlphaWrap bool

	// si is the spatial index over Model. Seeded inside runSticker on a
	// fresh build; nil after a disk-cache decode (the field is unexported,
	// gob skips it). Rebuilt by ensureSI() on first access. sync.Once
	// makes the lazy build safe against the disk-encode goroutine
	// triggered by setSticker — gob doesn't touch unexported fields, but
	// keeping the synchronization explicit lets the cache contract
	// "outputs are immutable after set" survive future concurrency.
	si     *voxel.SpatialIndex
	siOnce sync.Once
}

// ensureSI returns so.si, building it on first call. Safe to call from
// multiple goroutines; in practice the single pipeline worker is the only
// caller (runVoxelize on the alpha-wrap branch).
func (so *stickerOutput) ensureSI() *voxel.SpatialIndex {
	so.siOnce.Do(func() {
		if so.si == nil && so.Model != nil {
			so.si = voxel.NewSpatialIndex(so.Model, 2)
		}
	})
	return so.si
}

type colorAdjustOutput struct {
	Cells []voxel.ActiveCell
}

type colorWarpOutput struct {
	Cells []voxel.ActiveCell
}

type decimateOutput struct {
	// DecimModel is populated for the unsplit path. nil when split is
	// enabled.
	DecimModel *loader.LoadedModel
	// Halves is populated for the split path: per-half decimated
	// laid-out meshes. Both indices nil when split is disabled.
	Halves [2]*loader.LoadedModel
}

// splitOutput is the result of cutting a watertight model in two and
// laying the halves out side-by-side on the bed. Halves are in bed
// coordinates (post-Layout); Xform[i] is the forward transform from
// original-mesh coords to bed coords for half i (Voxelize calls
// ApplyInverse to map cell centroids back to original coords for
// color sampling).
//
// When Options.Split.Enabled is false, splitOutput.Enabled is false
// and downstream stages take their non-split path.
//
// CONSUMERS MUST GATE ON `Enabled`, NEVER ON `Halves[i] == nil`.
// loader.LoadedModel.GobEncode handles nil receivers by encoding
// an empty model, which decodes as a non-nil zero LoadedModel. So
// after a disk-cache round-trip, Halves[0]/Halves[1] are non-nil
// even when Enabled is false. Only the Enabled bit is reliable.
type splitOutput struct {
	Enabled   bool
	Halves    [2]*loader.LoadedModel
	Xform     [2]split.Transform
	CutNormal [3]float64 // outward normal from half 0 toward half 1
	CutPlaneD float64
}

type paletteOutput struct {
	Palette       [][3]uint8
	PaletteLabels []string           // parallel to Palette; label from inventory (empty for locked/computed)
	Cells         []voxel.ActiveCell // copy with snapped colors
}

type ditherOutput struct {
	Assignments     []int32
	PatchMap        map[voxel.CellKey]int
	NumPatches      int
	PatchAssignment []int32
}

type clipOutput struct {
	ShellVerts       [][3]float32
	ShellFaces       [][3]uint32
	ShellAssignments []int32
	// ShellHalfIdx is parallel to ShellFaces; non-nil only when Split
	// is enabled, in which case each face is tagged with the half it
	// came from. Downstream Merge keeps it parallel through the
	// per-half merge pass; Export uses it (eventually) to emit one
	// 3MF <object> entry per half.
	ShellHalfIdx []byte
}

// mergeOutput has the same structure as clipOutput. When NoMerge is true,
// the slices alias the clip output (safe because nothing mutates them after
// caching).
type mergeOutput struct {
	ShellVerts       [][3]float32
	ShellFaces       [][3]uint32
	ShellAssignments []int32
	ShellHalfIdx     []byte // parallel to ShellFaces; nil when Split disabled
}

// --- Per-stage settings structs for cache key computation ---
//
// Each struct contains exactly the Options fields that affect that stage.
// stageFnv hashes the struct using binary.Write, so the key is type-safe and
// free of format-string ambiguities. The cumulative cascade is built by
// stageKey, which concatenates stageFnv values from StageLoad through the
// requested stage.

// parseSettings is what affects the parsed-from-file *LoadedModel.
// File-content invariants live elsewhere (the stageKey cascade adds the
// sha256 of the file's bytes, so identical bytes hit the same cache).
//
// ReloadSeq deliberately is NOT here. It's a frontend-only mechanism
// for re-triggering reactive $effects when the user re-selects the
// same input path; including it in the cache key would make cache
// hits depend on which UI gesture loaded the file (direct .glb open
// bumps reloadSeq; loading a .json settings file does not), even
// when the actual file content and pipeline settings are identical.
type parseSettings struct {
	Input       string
	ObjectIndex int
}

// loadSettings is what affects the post-parse loadOutput: scale,
// normalize, alpha-wrap. The cumulative cascade key for StageLoad
// includes parseSettings via stageFnv(StageParse), so changing Input
// also invalidates StageLoad.
type loadSettings struct {
	Scale           float32
	HasSize         bool
	Size            float32
	AlphaWrap       bool
	AlphaWrapAlpha  float32
	AlphaWrapOffset float32
}

// BaseColor lives on voxelizeSettings (not loadSettings) because it only
// affects voxel cell coloring. A cheap per-run step reapplies the override
// to the cached ColorModel before voxelize, so Load/Decimate caches
// survive base-color changes. Sticker is invalidated on base-color change
// because runSticker deep-clones ColorModel into so.Model and the per-run
// reapply step does not patch that scratch copy.
type voxelizeSettings struct {
	NozzleDiameter                       float32
	LayerHeight                          float32
	BaseColor                            string
	BaseColorMaterialX                   string  // path
	BaseColorMaterialXMTime              int64   // ns; 0 if file is missing/inaccessible
	BaseColorMaterialXSize               int64   // bytes; 0 if file is missing/inaccessible
	BaseColorMaterialXTileMM             float64
	BaseColorMaterialXTriplanarSharpness float64
}

type stickerSettings struct {
	Stickers []Sticker
	// BaseColor / BaseColorMaterialX{,MTime,Size,TileMM,TriplanarSharpness}
	// are included so any base-color change invalidates the sticker
	// stage. See voxelizeSettings doc above for the reason.
	BaseColor                            string
	BaseColorMaterialX                   string
	BaseColorMaterialXMTime              int64
	BaseColorMaterialXSize               int64
	BaseColorMaterialXTileMM             float64
	BaseColorMaterialXTriplanarSharpness float64
	// AlphaWrap toggling changes the sticker substrate (wrap mesh vs.
	// original mesh), so decals built for one substrate are invalid when
	// the toggle changes. AlphaWrapAlpha and AlphaWrapOffset live in
	// loadSettings; the cumulative stage key cascade picks them up.
	AlphaWrap bool
}

type colorAdjustSettings struct {
	Brightness float32
	Contrast   float32
	Saturation float32
}

type colorWarpSettings struct {
	WarpPins []WarpPin
}

type decimateSettings struct {
	NoSimplify     bool
	NozzleDiameter float32
	LayerHeight    float32
}

// splitSettings is what affects StageSplit's output. When Enabled is
// false, only the Enabled bit is hashed so a disabled-Split run
// produces the same downstream cache keys it would have produced
// before the Split feature shipped. Toggling other fields while
// Enabled=false does not invalidate the cache.
type splitSettings struct {
	Enabled         bool
	Axis            int
	Offset          float64
	ConnectorStyle  string
	ConnectorCount  int
	ConnectorDiamMM  float64
	ConnectorDepthMM float64
	ClearanceMM      float64
	Orientation      [2]string
}

type paletteSettings struct {
	NumColors         int
	LockedColors      string // joined for hashing
	InventoryFile     string
	InventoryContents string // file contents for hashing; empty if no file
	InventoryColors   [][3]uint8
	InventoryLabels   []string
	ColorSnap         float64
}

type ditherSettings struct {
	Dither string
}

// clipSettings has no fields: clip is invalidated only by dependency cascade.
type clipSettings struct{}

type mergeSettings struct {
	NoMerge bool
}

func writeString(h hash.Hash64, s string) {
	binary.Write(h, binary.LittleEndian, uint32(len(s)))
	h.Write([]byte(s))
}

func writeFloat32(h hash.Hash64, f float32) {
	binary.Write(h, binary.LittleEndian, math.Float32bits(f))
}

func writeFloat64(h hash.Hash64, f float64) {
	binary.Write(h, binary.LittleEndian, math.Float64bits(f))
}

func writeBool(h hash.Hash64, b bool) {
	v := byte(0)
	if b {
		v = 1
	}
	h.Write([]byte{v})
}

func writeInt(h hash.Hash64, i int) {
	binary.Write(h, binary.LittleEndian, int64(i))
}

func (c *StageCache) settingsForStage(stage StageID, opts Options) any {
	switch stage {
	case StageParse:
		return parseSettings{
			Input:       opts.Input,
			ObjectIndex: opts.ObjectIndex,
		}
	case StageLoad:
		s := loadSettings{
			Scale:           opts.Scale,
			AlphaWrap:       opts.AlphaWrap,
			AlphaWrapAlpha:  opts.AlphaWrapAlpha,
			AlphaWrapOffset: opts.AlphaWrapOffset,
		}
		if opts.Size != nil {
			s.HasSize = true
			s.Size = *opts.Size
		}
		return s
	case StageVoxelize:
		mtime, size := materialXFileStamp(opts.BaseColorMaterialX)
		return voxelizeSettings{
			NozzleDiameter:                       opts.NozzleDiameter,
			LayerHeight:                          opts.LayerHeight,
			BaseColor:                            opts.BaseColor,
			BaseColorMaterialX:                   opts.BaseColorMaterialX,
			BaseColorMaterialXMTime:              mtime,
			BaseColorMaterialXSize:               size,
			BaseColorMaterialXTileMM:             opts.BaseColorMaterialXTileMM,
			BaseColorMaterialXTriplanarSharpness: opts.BaseColorMaterialXTriplanarSharpness,
		}
	case StageSticker:
		mtime, size := materialXFileStamp(opts.BaseColorMaterialX)
		return stickerSettings{
			Stickers:                             opts.Stickers,
			BaseColor:                            opts.BaseColor,
			BaseColorMaterialX:                   opts.BaseColorMaterialX,
			BaseColorMaterialXMTime:              mtime,
			BaseColorMaterialXSize:               size,
			BaseColorMaterialXTileMM:             opts.BaseColorMaterialXTileMM,
			BaseColorMaterialXTriplanarSharpness: opts.BaseColorMaterialXTriplanarSharpness,
			AlphaWrap:                            opts.AlphaWrap,
		}
	case StageColorAdjust:
		return colorAdjustSettings{Brightness: opts.Brightness, Contrast: opts.Contrast, Saturation: opts.Saturation}
	case StageColorWarp:
		return colorWarpSettings{WarpPins: opts.WarpPins}
	case StageSplit:
		// When disabled, only the Enabled bit affects the key; this
		// preserves cache-hit equivalence with the pre-Split path.
		if !opts.Split.Enabled {
			return splitSettings{Enabled: false}
		}
		return splitSettings{
			Enabled:          true,
			Axis:             opts.Split.Axis,
			Offset:           opts.Split.Offset,
			ConnectorStyle:   opts.Split.ConnectorStyle,
			ConnectorCount:   opts.Split.ConnectorCount,
			ConnectorDiamMM:  opts.Split.ConnectorDiamMM,
			ConnectorDepthMM: opts.Split.ConnectorDepthMM,
			ClearanceMM:      opts.Split.ClearanceMM,
			Orientation:      opts.Split.Orientation,
		}
	case StageDecimate:
		return decimateSettings{NoSimplify: opts.NoSimplify, NozzleDiameter: opts.NozzleDiameter, LayerHeight: opts.LayerHeight}
	case StagePalette:
		return paletteSettings{
			NumColors:         opts.NumColors,
			LockedColors:      strings.Join(opts.LockedColors, ","),
			InventoryFile:     opts.InventoryFile,
			InventoryContents: c.inventoryContents(opts.InventoryFile),
			InventoryColors:   opts.InventoryColors,
			InventoryLabels:   opts.InventoryLabels,
			ColorSnap:         opts.ColorSnap,
		}
	case StageDither:
		return ditherSettings{Dither: opts.Dither}
	case StageClip:
		return clipSettings{}
	case StageMerge:
		return mergeSettings{NoMerge: opts.NoMerge}
	}
	return nil
}

// materialXFileStamp returns the mtime (ns) and size (bytes) of the
// MaterialX package file at path, or (0, 0) if the file is missing or
// inaccessible. Used to invalidate the voxelize/sticker stage caches
// when the .mtlx or .zip on disk changes.
func materialXFileStamp(path string) (mtime, size int64) {
	if path == "" {
		return 0, 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0
	}
	return info.ModTime().UnixNano(), info.Size()
}

// stageFnv hashes a single stage's settings to a uint64. Used as the
// per-stage component of the cumulative stageKey.
func (c *StageCache) stageFnv(stage StageID, opts Options) uint64 {
	h := fnv.New64a()
	s := c.settingsForStage(stage, opts)
	switch v := s.(type) {
	case parseSettings:
		writeString(h, v.Input)
		writeInt(h, v.ObjectIndex)
	case loadSettings:
		writeFloat32(h, v.Scale)
		writeBool(h, v.HasSize)
		writeFloat32(h, v.Size)
		writeBool(h, v.AlphaWrap)
		writeFloat32(h, v.AlphaWrapAlpha)
		writeFloat32(h, v.AlphaWrapOffset)
	case voxelizeSettings:
		writeFloat32(h, v.NozzleDiameter)
		writeFloat32(h, v.LayerHeight)
		writeString(h, v.BaseColor)
		writeString(h, v.BaseColorMaterialX)
		binary.Write(h, binary.LittleEndian, v.BaseColorMaterialXMTime)
		binary.Write(h, binary.LittleEndian, v.BaseColorMaterialXSize)
		writeFloat64(h, v.BaseColorMaterialXTileMM)
		writeFloat64(h, v.BaseColorMaterialXTriplanarSharpness)
	case stickerSettings:
		writeString(h, v.BaseColor)
		writeString(h, v.BaseColorMaterialX)
		binary.Write(h, binary.LittleEndian, v.BaseColorMaterialXMTime)
		binary.Write(h, binary.LittleEndian, v.BaseColorMaterialXSize)
		writeFloat64(h, v.BaseColorMaterialXTileMM)
		writeFloat64(h, v.BaseColorMaterialXTriplanarSharpness)
		writeBool(h, v.AlphaWrap)
		writeInt(h, len(v.Stickers))
		for _, s := range v.Stickers {
			writeString(h, s.ImagePath)
			// Include image file mod time so changes to the PNG invalidate cache.
			if info, err := os.Stat(s.ImagePath); err == nil {
				binary.Write(h, binary.LittleEndian, info.ModTime().UnixNano())
			}
			for _, c := range s.Center {
				writeFloat64(h, c)
			}
			for _, n := range s.Normal {
				writeFloat64(h, n)
			}
			for _, u := range s.Up {
				writeFloat64(h, u)
			}
			writeFloat64(h, s.Scale)
			writeFloat64(h, s.Rotation)
			writeFloat64(h, s.MaxAngle)
			writeString(h, s.Mode)
		}
	case colorAdjustSettings:
		writeFloat32(h, v.Brightness)
		writeFloat32(h, v.Contrast)
		writeFloat32(h, v.Saturation)
	case colorWarpSettings:
		writeInt(h, len(v.WarpPins))
		for _, p := range v.WarpPins {
			writeString(h, p.SourceHex)
			writeString(h, p.TargetHex)
			writeFloat64(h, p.Sigma)
		}
	case splitSettings:
		writeBool(h, v.Enabled)
		if v.Enabled {
			writeInt(h, v.Axis)
			writeFloat64(h, v.Offset)
			writeString(h, v.ConnectorStyle)
			writeInt(h, v.ConnectorCount)
			writeFloat64(h, v.ConnectorDiamMM)
			writeFloat64(h, v.ConnectorDepthMM)
			writeFloat64(h, v.ClearanceMM)
			writeString(h, v.Orientation[0])
			writeString(h, v.Orientation[1])
		}
	case decimateSettings:
		writeBool(h, v.NoSimplify)
		writeFloat32(h, v.NozzleDiameter)
		writeFloat32(h, v.LayerHeight)
	case paletteSettings:
		writeInt(h, v.NumColors)
		writeString(h, v.LockedColors)
		writeString(h, v.InventoryFile)
		writeString(h, v.InventoryContents)
		writeInt(h, len(v.InventoryColors))
		for _, c := range v.InventoryColors {
			h.Write(c[:])
		}
		// Labels are length-prefixed strings, so a shorter InventoryLabels
		// slice produces a different hash than a longer one even without an
		// explicit count. This is intentional — label count tracks color count.
		for _, l := range v.InventoryLabels {
			writeString(h, l)
		}
		writeFloat64(h, v.ColorSnap)
	case ditherSettings:
		writeString(h, v.Dither)
	case clipSettings:
		// No independent settings.
	case mergeSettings:
		writeBool(h, v.NoMerge)
	}
	return h.Sum64()
}

// allocOutput returns a fresh, zero-valued pointer of the right type for the
// given stage, suitable for gob-decoding into. Returning a typed pointer (not
// "any") matters because gob.Decode needs the concrete type behind any.
func allocOutput(stage StageID) any {
	switch stage {
	case StageParse:
		return &loader.LoadedModel{}
	case StageLoad:
		return &loadOutput{}
	case StageSplit:
		return &splitOutput{}
	case StageDecimate:
		return &decimateOutput{}
	case StageSticker:
		return &stickerOutput{}
	case StageVoxelize:
		return &voxelizeOutput{}
	case StageColorAdjust:
		return &colorAdjustOutput{}
	case StageColorWarp:
		return &colorWarpOutput{}
	case StagePalette:
		return &paletteOutput{}
	case StageDither:
		return &ditherOutput{}
	case StageClip:
		return &clipOutput{}
	case StageMerge:
		return &mergeOutput{}
	}
	return nil
}

// hitSource indicates where a cache hit came from. Currently only the
// disk tier produces hits (in-process compressed-byte caching was
// removed because the OS page cache + pipelineRun memoization already
// cover what it would have provided).
type hitSource int

const (
	hitMiss hitSource = iota
	hitDisk
)

// get returns the cached output for the given stage and opts, or nil on
// miss. Every stage is treated identically — there are no stages with
// special caching rules.
func (c *StageCache) get(stage StageID, opts Options) any {
	v, _ := c.getWithSource(stage, opts)
	return v
}

// getWithSource is get plus an indicator of where the hit came from.
// On a hit, decodes the blob into a freshly allocated output struct.
// A blob that fails to decode (corrupted file, format change) is
// deleted so the next access misses cleanly and recomputes.
func (c *StageCache) getWithSource(stage StageID, opts Options) (any, hitSource) {
	key := c.stageKey(stage, opts)
	if key == "" || c.disk == nil {
		return nil, hitMiss
	}
	subdir := stageSubdir(stage)
	blob := c.disk.GetBlob(subdir, key)
	if blob == nil {
		return nil, hitMiss
	}
	out := allocOutput(stage)
	if out == nil {
		return nil, hitMiss
	}
	if err := cacheblob.Decode(blob, out); err != nil {
		c.disk.Remove(subdir, key)
		return nil, hitMiss
	}
	return out, hitDisk
}

// set spawns a goroutine that encodes output and writes the resulting
// blob to disk. Description and cost are filled in by stampCost,
// which runStageCached calls after the body returns and the
// wall-clock duration is known.
//
// Encoding happens off the calling goroutine deliberately: encoding a
// multi-hundred-MB stage output allocates aggressively, and doing
// that synchronously on the pipeline worker thread piled on memory
// pressure right before CGO calls into native libraries (alpha-wrap,
// renderer). That timing reliably tripped a SIGSEGV in a C++ runtime
// signal handler that wasn't SA_ONSTACK-clean. Async encoding spreads
// the allocation pressure over time and keeps the calling goroutine
// thin.
//
// Lifetime: after set returns, the caller's local pointer is the
// only live decoded copy. The encoder goroutine reads it
// concurrently with downstream stages; concurrent reads of immutable
// data are race-free, but the caller must not mutate.
func (c *StageCache) set(stage StageID, opts Options, output any) {
	key := c.stageKey(stage, opts)
	if key == "" || c.disk == nil {
		return
	}
	subdir := stageSubdir(stage)
	c.diskWrites.Add(1)
	go func() {
		defer c.diskWrites.Done()
		blob, err := cacheblob.Encode(output)
		if err != nil {
			return
		}
		c.disk.SetBlob(subdir, key, blob)
	}()
}

// stampCost back-fills the disk-side meta sidecar with description and
// wall-clock cost for the entry the most recent typed setter wrote.
// Async; tracked by diskWrites so shutdown waits for it.
//
// Best-effort under same-key contention: if two pipeline runs produce
// the same key in quick succession, their stampCost goroutines may
// land out of order, leaving the meta with the wrong cost. The blob
// is still correct (last writer wins on the data file too) and an
// off-by-one cost only mildly skews future eviction scoring; not
// worth a per-key serializer.
func (c *StageCache) stampCost(stage StageID, opts Options, cost time.Duration) {
	key := c.stageKey(stage, opts)
	if key == "" || c.disk == nil {
		return
	}
	subdir := stageSubdir(stage)
	description := stageDescription(stage, opts)
	c.diskWrites.Add(1)
	go func() {
		defer c.diskWrites.Done()
		c.disk.RecordCost(subdir, key, description, cost)
	}()
}

// Typed getters — return the concrete output type for each stage.
// Used by callers outside the pipeline-run flow (e.g. pipeline.go's
// post-run consumers, applyBaseColor). The per-stage Run methods use
// runStage's generic c.get instead.

func (c *StageCache) getParse(opts Options) *loader.LoadedModel {
	v := c.get(StageParse, opts)
	if v == nil {
		return nil
	}
	return v.(*loader.LoadedModel)
}

func (c *StageCache) getLoad(opts Options) *loadOutput {
	v := c.get(StageLoad, opts)
	if v == nil {
		return nil
	}
	return v.(*loadOutput)
}

func (c *StageCache) getPalette(opts Options) *paletteOutput {
	v := c.get(StagePalette, opts)
	if v == nil {
		return nil
	}
	return v.(*paletteOutput)
}

func (c *StageCache) getMerge(opts Options) *mergeOutput {
	v := c.get(StageMerge, opts)
	if v == nil {
		return nil
	}
	return v.(*mergeOutput)
}

