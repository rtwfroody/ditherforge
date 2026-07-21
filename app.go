package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"golang.org/x/image/draw"

	"github.com/rtwfroody/ditherforge/internal/collection"
	"github.com/rtwfroody/ditherforge/internal/diskcache"
	"github.com/rtwfroody/ditherforge/internal/ditherpreview"
	"github.com/rtwfroody/ditherforge/internal/export3mf"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/materialx"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
	"github.com/rtwfroody/ditherforge/internal/plog"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/settings"
	"github.com/rtwfroody/ditherforge/internal/swatch"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Settings and its sub-types now live in internal/settings so the CLI can
// share the exact same JSON format and Settings→Options conversion. This
// alias keeps app.go's existing Settings references (and the Wails method
// signatures bound to the frontend) working; Wails generates the sub-type
// bindings transitively from the Settings fields.
type Settings = settings.Settings

// App is the Wails application backend.
type App struct {
	ctx      context.Context
	mu       sync.Mutex           // protects cache and the last* mesh-handler IDs; held during pipeline execution and Export3MF
	cancelMu sync.Mutex           // protects cancel func; separate from mu so ProcessPipeline can cancel without blocking
	cancel   context.CancelFunc   // cancels in-flight pipeline work
	cache    *pipeline.StageCache // per-stage cache across runs
	// lastOpts is the last successfully processed Options. Uses
	// atomic.Pointer so SplitPreview (and other read-only Wails
	// methods) can snapshot it without blocking on `mu`, which the
	// pipeline worker holds for the entire duration of a run.
	lastOpts atomic.Pointer[pipeline.Options]
	// settingsPath is the path of the most recently loaded or saved settings
	// JSON file, tracked so exports can default their save dialog to the same
	// directory. atomic.Pointer so read-only Wails methods snapshot it without
	// taking mu. nil until a settings file is loaded or saved this session.
	settingsPath  atomic.Pointer[string]
	meshes        *meshHandler         // serves binary mesh data over HTTP
	lastInputID   string               // mesh handler ID for last input mesh (protected by mu)
	lastOverlayID string               // mesh handler ID for the alpha-wrap sticker overlay
	lastWrappedID string               // mesh handler ID for the alpha-wrapped geometry preview
	lastOutputID  string               // mesh handler ID for last output mesh (protected by mu)
	reqCh         chan pipelineRequest // buffered channel for pipeline requests; worker drains to latest
	collections   *collection.Manager  // filament collection manager
	sweepInFlight atomic.Bool          // true while a disk-cache sweep goroutine is running
}

// pipelineRequest is sent from ProcessPipeline to the worker goroutine.
// gen is the frontend-owned run id (the correlation id allocated
// synchronously in App.svelte's runPipeline before the IPC call). The
// worker echoes it on every event so the frontend can gate with an
// exact match — there is no lagging backend counter to race against.
type pipelineRequest struct {
	opts pipeline.Options
	gen  int64
}

// pipelineEvent is emitted to the frontend via Wails events.
type pipelineEvent struct {
	Gen        int64                 `json:"gen"`
	Duration   float64               `json:"duration,omitempty"`   // seconds, for pipeline-done
	Message    string                `json:"message,omitempty"`    // error text, for pipeline-error
	ExtentMM   float32               `json:"extentMM,omitempty"`   // model extent, for pipeline-needs-force
	ColorUsage []pipeline.ColorUsage `json:"colorUsage,omitempty"` // per-color output usage, for pipeline-done
}

// meshEvent is the payload sent via Wails events for mesh data.
// URL points to a binary mesh endpoint served by meshHandler.
type meshEvent struct {
	Gen          int64   `json:"gen"`
	URL          string  `json:"url"`
	PreviewScale float32 `json:"previewScale,omitempty"`
	ExtentMM     float32 `json:"extentMM,omitempty"` // native max extent in mm, for input-mesh
	// Per-axis bbox of the loaded model in original-mesh coords (mm,
	// post-scale, post-normalizeZ). Populated only on input-mesh
	// events. Used by the Split Settings panel to size the offset
	// slider's range and pick a sensible default offset (the bbox
	// midpoint along the chosen axis).
	BBoxMin [3]float32 `json:"bboxMin,omitempty"`
	BBoxMax [3]float32 `json:"bboxMax,omitempty"`
}

// NewApp creates a new App instance.
func NewApp() *App {
	cm, err := collection.NewManager()
	if err != nil {
		// Fatal: collections are required for the GUI to function.
		panic(fmt.Sprintf("failed to initialize collection manager: %v", err))
	}
	cache := pipeline.NewStageCache()
	if dir, err := diskcache.DefaultDir(); err == nil {
		if d, err := diskcache.Open(dir); err == nil {
			d.OnError = func(stage, op, key string, err error) {
				plog.Printf("disk cache %s %s key=%s: %v", stage, op, shortDiskKey(key), err)
			}
			d.OnEvict = func(stage, key, description, reason string, sizeBytes, costMs int64, mtime time.Time) {
				what := description
				if what == "" {
					what = stage
				}
				plog.Printf("disk cache evict (%s): %s key=%s — %s, %.1fs to generate, %s old",
					reason, what, shortDiskKey(key), humanSize(sizeBytes),
					float64(costMs)/1000, humanAge(time.Since(mtime)))
			}
			cache.SetDisk(d)
		} else {
			fmt.Fprintf(os.Stderr, "disk cache disabled: %v\n", err)
		}
	}
	return &App{
		cache:       cache,
		meshes:      newMeshHandler(),
		reqCh:       make(chan pipelineRequest, 1),
		collections: cm,
	}
}

const (
	diskCacheMaxAge   = 7 * 24 * time.Hour
	diskCacheMaxBytes = 1 << 31 // 2 GiB
)

// humanSize formats a byte count using KB / MB / GB units (1024-based).
// shortDiskKey returns the first 12 hex chars of a disk-cache key — the
// same prefix length plog uses for stage cache keys, so eviction logs
// and stage cache hit/miss logs can be correlated by key.
func shortDiskKey(key string) string {
	if key == "" {
		return "?"
	}
	if len(key) > 12 {
		return key[:12]
	}
	return key
}

// humanAge returns a short human-readable rendering of a Duration.
// Used by the disk-cache eviction log so the user can see at a glance
// whether a freshly-generated entry is being thrown away or an
// ancient one.
func humanAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%.0fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%.1fm", d.Minutes())
	case d < 24*time.Hour:
		return fmt.Sprintf("%.1fh", d.Hours())
	default:
		return fmt.Sprintf("%.1fd", d.Hours()/24)
	}
}

func humanSize(n int64) string {
	const k = 1024.0
	f := float64(n)
	switch {
	case f >= k*k*k:
		return fmt.Sprintf("%.1f GB", f/(k*k*k))
	case f >= k*k:
		return fmt.Sprintf("%.1f MB", f/(k*k))
	case f >= k:
		return fmt.Sprintf("%.1f KB", f/k)
	}
	return fmt.Sprintf("%d B", n)
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go a.pipelineWorker()
}

// kickDiskCacheSweep starts a background sweep goroutine if one isn't
// already running. Called at the end of every pipeline run so the cache
// can't grow between sweeps. The sweep itself is best-effort: any error
// is logged and ignored. Safe to call from any goroutine.
func (a *App) kickDiskCacheSweep() {
	if !a.sweepInFlight.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer a.sweepInFlight.Store(false)
		c := a.cache.Disk()
		if c == nil {
			return
		}
		// Wait for in-flight async writes to land their meta sidecars
		// before sweeping. Without this, a freshly-written data file
		// whose cost-stamp goroutine hasn't run yet appears to Sweep
		// with costMs=0 — top eviction bait — and gets dropped under
		// size pressure. The next ExportFile then misses on the
		// just-written entry and surfaces "pipeline has not been run yet".
		a.cache.WaitForDiskWrites()
		stats, err := c.Sweep(diskCacheMaxAge, diskCacheMaxBytes)
		if err != nil {
			fmt.Fprintf(os.Stderr, "disk cache sweep: %v\n", err)
			return
		}
		if stats.AgeEvicted > 0 || stats.SizeEvicted > 0 {
			fmt.Fprintf(os.Stderr, "disk cache sweep: removed %d aged + %d LRU entries (%.1f MB)\n",
				stats.AgeEvicted, stats.SizeEvicted, float64(stats.BytesFreed)/(1<<20))
		}
	}()
}

func (a *App) shutdown(ctx context.Context) {
	close(a.reqCh)
	// Wait for in-flight disk-cache writes to complete. Big payloads
	// (load entries can be hundreds of MB after zstd) take seconds to
	// encode and write; without this wait, the OS kills the writer
	// goroutines at process exit and the next session re-does the
	// expensive work it should have hit in the cache.
	//
	// Bounded by a generous timeout so a stuck FS doesn't trap the
	// user in the shutdown path.
	done := make(chan struct{})
	go func() {
		a.cache.WaitForDiskWrites()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		fmt.Fprintf(os.Stderr, "shutdown: gave up waiting for disk cache writes after 30s\n")
	}
}

// Quit terminates the application. Bound so the frontend's File > Exit
// menu item can trigger the same shutdown path as the window close button.
func (a *App) Quit() {
	wailsRuntime.Quit(a.ctx)
}

// CreateCollection creates a new empty user collection.
func (a *App) CreateCollection(name string) error {
	return a.collections.Create(name, nil)
}

// SaveCollectionColors replaces all colors in a user collection.
func (a *App) SaveCollectionColors(name string, colors []ColorEntry) error {
	entries := make([]palette.InventoryEntry, len(colors))
	for i, c := range colors {
		rgb, err := palette.ParsePalette([]string{c.Hex})
		if err != nil {
			return fmt.Errorf("invalid color %q: %w", c.Hex, err)
		}
		entries[i] = palette.InventoryEntry{Color: rgb[0], Label: c.Label, TD: tdOrDefault(c.TD)}
	}
	return a.collections.Save(name, entries)
}

// ResolveColor parses a color string (hex or CSS name) and returns a ColorEntry.
// CSS color names are used as the label.
func (a *App) ResolveColor(input string) (*ColorEntry, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty color input")
	}
	rgb, err := palette.ParsePalette([]string{input})
	if err != nil {
		return nil, err
	}
	hex := fmt.Sprintf("#%02X%02X%02X", rgb[0][0], rgb[0][1], rgb[0][2])
	label := ""
	if !strings.HasPrefix(input, "#") {
		label = input
	}
	return &ColorEntry{Hex: hex, Label: label, TD: palette.DefaultTD}, nil
}

// tdOrDefault treats a zero/unset TD (e.g. from an older client that never
// sent the field) as the opaque default rather than literal zero.
func tdOrDefault(td float32) float32 {
	if td <= 0 {
		return palette.DefaultTD
	}
	return td
}

// IsBusy returns true if the pipeline mutex is held (processing in progress).
func (a *App) IsBusy() bool {
	if a.mu.TryLock() {
		a.mu.Unlock()
		return false
	}
	return true
}

// Export3MF acquires the pipeline mutex, opens a native save dialog, and
// exports the 3MF file using cached pipeline results. Returns the saved
// path, or empty if the user cancelled.
func (a *App) Export3MF() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	last := a.lastOpts.Load()
	defaultName := "output.3mf"
	defaultDir := ""
	if last != nil && last.Input != "" {
		defaultDir = filepath.Dir(last.Input)
		base := filepath.Base(last.Input)
		ext := filepath.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		if strings.EqualFold(ext, ".3mf") {
			defaultName = stem + "-df.3mf"
		} else {
			defaultName = stem + ".3mf"
		}
	}
	if last == nil {
		return "", fmt.Errorf("no model has been processed yet")
	}

	path, err := wailsRuntime.SaveFileDialog(a.ctx, wailsRuntime.SaveDialogOptions{
		Title:            "Save Output",
		DefaultFilename:  defaultName,
		DefaultDirectory: defaultDir,
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "3MF Files (*.3mf)", Pattern: "*.3mf"},
		},
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}

	_, err = pipeline.ExportFile(a.cache, *last, path, export3mf.Options{
		PrinterID:      last.Printer,
		NozzleDiameter: last.NozzleDiameter,
		LayerHeight:    last.LayerHeight,
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

// --- Swatch calibration plates ---

// swatchManifest is written next to the exported 3MF as "<path>.swatch.json".
// It records exactly which filaments each plate mixes and the realized vs.
// nominal coverage per section, so a later photo-analysis step knows the ground
// truth for each plate/section.
type swatchManifest struct {
	AppVersion string  `json:"appVersion"`
	Printer    string  `json:"printer"`
	NozzleMM   float32 `json:"nozzleMM"`
	LayerMM    float32 `json:"layerMM"`
	// Pattern-block dimensions. Blocks are one voxel cell wide and one print
	// layer tall, so the pattern has layer-height granularity in Z.
	BlockWidthMM  float64               `json:"blockWidthMM"`  // block width = the exact (unsnapped) voxel cell width
	RowHeight0MM  float64               `json:"rowHeight0MM"`  // first-row (slab 0) height
	RowHeightUpMM float64               `json:"rowHeightUpMM"` // height of rows above the first
	RowCount      int                   `json:"rowCount"`      // number of pattern rows spanning the 10mm face
	// PatternRank is the shared void-and-cluster blue-noise ranking over ONE
	// section's block grid (rows outer, columns inner): PatternRank[row][col] holds
	// every value in [0, rowCount*perSection) exactly once, where perSection =
	// round(SectionMM/blockWidthMM). A block is filament B iff its rank <
	// round(realizedCoverage*N). Stored explicitly so swatchphoto reproduces the
	// exact printed pattern without re-running void-and-cluster in Python.
	PatternRank [][]int               `json:"patternRank"`
	Palette     []swatchManifestColor `json:"palette"`
	Infill      string                `json:"infill"`
	Plates      []swatchManifestPlate `json:"plates"`
}

type swatchManifestColor struct {
	Label string  `json:"label"`
	Hex   string  `json:"hex"`
	TD    float32 `json:"td"`
}

type swatchManifestPlate struct {
	Pair      [2]string               `json:"pair"` // [labelA, labelB]
	YOffsetMM float64                 `json:"yOffsetMM"`
	Sections  []swatchManifestSection `json:"sections"`
}

type swatchManifestSection struct {
	Index                 int     `json:"index"`
	NominalCoverage       float64 `json:"nominalCoverage"`
	RealizedCoverageFront float64 `json:"realizedCoverageFront"`
	RealizedCoverageBack  float64 `json:"realizedCoverageBack"`
	// Fraction of the section assigned to a color that is NEITHER of the plate's
	// two filaments. Must be 0 for a correct swatch; a nonzero value means a
	// third color contaminated the plate (see swatch.SnapToPairs). Measured on
	// the exported (snapped) assignments, so it is the honest per-section figure
	// for what actually prints.
	ForeignCoverageFront float64 `json:"foreignCoverageFront"`
	ForeignCoverageBack  float64 `json:"foreignCoverageBack"`
}

// swatchFilename builds the default export filename from the layer height and
// palette labels: "swatches-<layer>mm-<Label1>-<Label2>-...3mf" with the labels
// sanitized for filenames and sorted alphabetically (case-insensitively). The
// layer height matters because the pattern's rows are one print layer tall, so
// plates exported at different layer heights are different physical objects.
// A non-positive layer height omits that segment; no surviving labels omits
// the label segment ("swatches.3mf" when both are missing).
func swatchFilename(filaments []swatch.Filament, layerHeightMM float32) string {
	parts := []string{"swatches"}
	if layerHeightMM > 0 {
		parts = append(parts, strconv.FormatFloat(float64(layerHeightMM), 'g', -1, 32)+"mm")
	}
	var labels []string
	for _, f := range filaments {
		if part := sanitizeFilenamePart(f.Label); part != "" {
			labels = append(labels, part)
		}
	}
	sort.Slice(labels, func(i, j int) bool {
		return strings.ToLower(labels[i]) < strings.ToLower(labels[j])
	})
	return strings.Join(append(parts, labels...), "-") + ".3mf"
}

// sanitizeFilenamePart makes a label safe to embed in a filename on any common
// platform: it drops characters reserved by Windows/Unix filesystems
// (<>:"/\|?* and control chars) and removes whitespace so multi-word labels
// join without spaces.
func sanitizeFilenamePart(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsSpace(r), r < 0x20:
			continue
		case strings.ContainsRune(`<>:"/\|?*`, r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ExportSwatchPlates generates physical filament-calibration plates for the
// current palette (all C(n,2) pairs), runs them through an independent pipeline
// instance (so the GUI's cache/viewer are untouched), exports a 3MF, and writes
// a manifest of nominal vs. realized per-section coverage. Requires a completed
// run so the current palette is known. Returns the saved 3MF path, or empty if
// the user cancelled.
func (a *App) ExportSwatchPlates() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	last := a.lastOpts.Load()
	if last == nil {
		return "", fmt.Errorf("run the pipeline first")
	}
	pal, tds, labels, err := pipeline.ResolvedPalette(a.cache, *last)
	if err != nil || len(pal) == 0 {
		return "", fmt.Errorf("run the pipeline first")
	}

	// Build the swatch filament list in export order (infill at index 0).
	filaments := make([]swatch.Filament, len(pal))
	for i := range pal {
		lbl := ""
		if i < len(labels) {
			lbl = labels[i]
		}
		td := float32(palette.DefaultTD)
		if i < len(tds) && tds[i] > 0 {
			td = tds[i]
		}
		filaments[i] = swatch.Filament{
			Hex:   fmt.Sprintf("#%02X%02X%02X", pal[i][0], pal[i][1], pal[i][2]),
			TD:    td,
			Label: lbl,
		}
	}

	// Default the dialog to the directory of the currently loaded/saved
	// settings file (falling back to the OS default when none is loaded), and
	// name the file after the palette colors in alphabetical order.
	defaultDir := ""
	if sp := a.settingsPath.Load(); sp != nil {
		defaultDir = filepath.Dir(*sp)
	}
	path, err := wailsRuntime.SaveFileDialog(a.ctx, wailsRuntime.SaveDialogOptions{
		Title:            "Save Swatch Plates",
		DefaultFilename:  swatchFilename(filaments, last.LayerHeight),
		DefaultDirectory: defaultDir,
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "3MF Files (*.3mf)", Pattern: "*.3mf"},
		},
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}

	// Assemble swatch settings from defaults, copying the printer/geometry
	// knobs from the last run and locking the palette so color assignment is
	// deterministic (no selection, no swaps). Everything that would perturb the
	// sampled cell colors is disabled.
	s := settings.Default()
	s.Printer = last.Printer
	s.NozzleDiameter = strconv.FormatFloat(float64(last.NozzleDiameter), 'g', -1, 32)
	s.LayerHeight = strconv.FormatFloat(float64(last.LayerHeight), 'g', -1, 32)
	s.Layer0AdhesionXYScale = float64(last.Layer0AdhesionXYScale)
	s.UpperLayerXYScale = float64(last.UpperLayerXYScale)
	// Fixed 1:1 scale — the plates are already authored at their true mm size.
	s.SizeMode = "scale"
	s.ScaleValue = "1.0"
	s.Dither = "none"
	s.MeshRepair = "none"
	s.SplitEnabled = false
	s.Stickers = nil
	s.Brightness, s.Contrast, s.Saturation = 0, 0, 0
	s.WarpPins = nil
	s.ColorSnap = 0
	s.HonorTD = false
	s.BaseColor = nil
	s.BaseColorMode = "solid"
	// The pattern is a regular per-cell grid carried by a texture; color-aware
	// cell segmentation would re-cut cells along the sampled (bilinear) color
	// boundaries and bias the per-section coverage, so keep one cell per grid
	// block.
	s.ColorAwareCells = false
	s.ColorSlots = make([]*settings.ColorSlotSetting, len(filaments))
	for i, fil := range filaments {
		s.ColorSlots[i] = &settings.ColorSlotSetting{Hex: fil.Hex, TD: fil.TD, Label: fil.Label}
	}
	s.InfillFilament = filaments[0].Hex

	swatchOpts, err := settings.ToOptions(s, a.collections)
	if err != nil {
		return "", fmt.Errorf("swatch settings: %w", err)
	}

	// Block width = the upper-layer voxel cell width this run will use (snapped
	// so an integer number of blocks spans each 10mm section). Row heights come
	// from the slab grid: first row spans the initial-layer height, the rest the
	// layer height, so each pattern row is one print layer tall.
	blockWidthMM := float64(pipeline.UpperCellSizeMM(swatchOpts))
	if blockWidthMM <= 0 {
		blockWidthMM = 0.5
	}
	layer0Z, upperZ := pipeline.SlabHeightsMM(swatchOpts)
	plan := swatch.BuildPlan(filaments, blockWidthMM, float64(layer0Z), float64(upperZ))

	tmpDir, err := os.MkdirTemp("", "ditherforge-swatch-")
	if err != nil {
		return "", fmt.Errorf("swatch temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	objPath, err := swatch.WriteOBJ(plan, tmpDir)
	if err != nil {
		return "", err
	}
	s.InputFile = objPath
	swatchOpts, err = settings.ToOptions(s, a.collections)
	if err != nil {
		return "", fmt.Errorf("swatch settings: %w", err)
	}

	// Independent cache + no viewer callbacks: this run must not disturb the
	// GUI's a.cache, a.lastOpts, or the output viewer state. The stage cache is
	// disk-backed (stage outputs are only retained on disk), so give it its own
	// temp directory under tmpDir, cleaned up with the rest.
	freshCache := pipeline.NewStageCache()
	cacheDir := filepath.Join(tmpDir, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("swatch cache dir: %w", err)
	}
	disk, err := diskcache.Open(cacheDir)
	if err != nil {
		return "", fmt.Errorf("swatch cache: %w", err)
	}
	freshCache.SetDisk(disk)
	if _, err := pipeline.RunCached(a.ctx, freshCache, swatchOpts, nil); err != nil {
		return "", fmt.Errorf("swatch pipeline: %w", err)
	}

	// Snap every output face to the nearer of its plate's two filaments before
	// export. A voxel cell straddling a block boundary can be painted a blended
	// color the palette rounds to a THIRD filament (e.g. a White+Orange blend
	// landing on Beige); snapping removes that contamination so each plate shows
	// only its two colors. See swatch.SnapToPairs.
	verts, faces, rawAssign, _, err := pipeline.OutputFaceAssignments(freshCache, swatchOpts)
	if err != nil {
		return "", fmt.Errorf("swatch coverage: %w", err)
	}
	snapped := swatch.SnapToPairs(plan, verts, faces, rawAssign)
	if n := countChanged(rawAssign, snapped); n > 0 {
		plog.Printf("swatch: snapped %d/%d output faces off a foreign color onto the plate's pair",
			n, len(rawAssign))
	}

	if _, err := pipeline.ExportFileWithAssignments(freshCache, swatchOpts, path, export3mf.Options{
		PrinterID:      last.Printer,
		NozzleDiameter: last.NozzleDiameter,
		LayerHeight:    last.LayerHeight,
		AppVersion:     pipeline.VersionSemver,
	}, snapped); err != nil {
		return "", fmt.Errorf("swatch export: %w", err)
	}

	// Manifest from the exported (snapped) mesh: realized coverage and the
	// per-section foreign fraction (must be 0 now that we snap).
	manifest := buildSwatchManifest(swatchOpts, plan, verts, faces, snapped)
	if data, err := json.MarshalIndent(manifest, "", "  "); err == nil {
		if werr := os.WriteFile(path+".swatch.json", data, 0644); werr != nil {
			plog.Printf("swatch: writing manifest: %v", werr)
		}
	}

	return path, nil
}

// countChanged returns how many entries differ between two equal-length slices.
func countChanged(a, b []int32) int {
	n := 0
	for i := range a {
		if i < len(b) && a[i] != b[i] {
			n++
		}
	}
	return n
}

// buildSwatchManifest assembles the manifest from the exported output mesh and
// its per-face assignments (post-snap): realized B coverage and the foreign
// (non-pair) fraction per section.
func buildSwatchManifest(opts pipeline.Options, plan swatch.Plan, verts [][3]float32, faces [][3]uint32, assignments []int32) *swatchManifest {
	front, back := swatch.MeasureCoverage(plan, verts, faces, assignments)

	m := &swatchManifest{
		AppVersion:    pipeline.VersionSemver,
		Printer:       opts.Printer,
		NozzleMM:      opts.NozzleDiameter,
		LayerMM:       opts.LayerHeight,
		BlockWidthMM:  plan.BlockWidthMM,
		RowHeight0MM:  plan.Layer0ZMM,
		RowHeightUpMM: plan.UpperZMM,
		RowCount:      plan.Nrows(),
		PatternRank:   plan.Rank,
		Infill:        plan.Palette[0].Label,
	}
	for _, fil := range plan.Palette {
		m.Palette = append(m.Palette, swatchManifestColor{Label: fil.Label, Hex: fil.Hex, TD: fil.TD})
	}
	for p, plate := range plan.Plates {
		mp := swatchManifestPlate{
			Pair:      [2]string{plan.Palette[plate.A].Label, plan.Palette[plate.B].Label},
			YOffsetMM: plate.YOffsetMM,
		}
		for _, sec := range swatch.PlateSections() {
			i := sec.Index
			mp.Sections = append(mp.Sections, swatchManifestSection{
				Index:                 i,
				NominalCoverage:       sec.NominalCoverage,
				RealizedCoverageFront: front[p][i].B,
				RealizedCoverageBack:  back[p][i].B,
				ForeignCoverageFront:  front[p][i].Foreign,
				ForeignCoverageBack:   back[p][i].Foreign,
			})
		}
		m.Plates = append(m.Plates, mp)
	}
	return m
}

// ProcessPipeline enqueues a pipeline request and returns immediately.
// The single pipelineWorker goroutine processes only the latest request.
// Results are delivered via Wails events: pipeline-done, pipeline-error,
// pipeline-needs-force, input-mesh, output-mesh.
//
// runID is allocated by the frontend synchronously, before this call, and
// echoed on every event emitted for this run so the frontend gates events
// with an exact match against the run it currently owns. There is no
// backend generation counter to lag behind the frontend.
func (a *App) ProcessPipeline(s Settings, runID int64, force bool, reloadSeq int64) {
	// Convert the persisted settings to pipeline options through the same
	// shared path the CLI uses (settings.ToOptions), so the GUI and CLI
	// can never diverge. force / reloadSeq are runtime-only and layered on
	// after conversion. A conversion error (e.g. a missing inventory
	// collection) surfaces as a pipeline-error event for this run.
	opts, err := settings.ToOptions(s, a.collections)
	if err != nil {
		wailsRuntime.EventsEmit(a.ctx, "pipeline-error", pipelineEvent{Gen: runID, Message: err.Error()})
		return
	}
	opts.Force = force
	opts.ReloadSeq = reloadSeq

	// Cancel any in-flight pipeline immediately so it aborts early.
	a.cancelMu.Lock()
	if a.cancel != nil {
		a.cancel()
	}
	a.cancelMu.Unlock()

	// Replace any pending request in the channel. Drain first (non-blocking),
	// then send into the now-empty buffer-1 channel.
	req := pipelineRequest{opts: opts, gen: runID}
	select {
	case <-a.reqCh:
	default:
	}
	a.reqCh <- req
}

// pipelineWorker is the single goroutine that processes pipeline requests.
// It drains the channel to keep only the latest request, avoiding the
// goroutine pile-up that would occur if each Wails call ran the pipeline.
func (a *App) pipelineWorker() {
	for req := range a.reqCh {
		// Drain any queued requests, keeping only the latest.
		latest := req
	drain:
		for {
			select {
			case newer := <-a.reqCh:
				latest = newer
			default:
				break drain
			}
		}
		a.processOne(latest)
	}
}

// guiTracker implements progress.Tracker by emitting Wails events.
type guiTracker struct {
	appCtx   context.Context
	gen      int64
	mu       sync.Mutex
	lastEmit time.Time
}

type stageEvent struct {
	Gen         int64  `json:"gen"`
	Stage       string `json:"stage"`
	Status      string `json:"status"` // "running", "done", or "cached"
	HasProgress bool   `json:"hasProgress"`
	Total       int    `json:"total"`
}

type progressEvent struct {
	Gen     int64  `json:"gen"`
	Stage   string `json:"stage"`
	Current int    `json:"current"`
}

func (t *guiTracker) StageStart(stage string, hasProgress bool, total int) {
	wailsRuntime.EventsEmit(t.appCtx, "pipeline-stage", stageEvent{
		Gen: t.gen, Stage: stage, Status: "running",
		HasProgress: hasProgress, Total: total,
	})
}

func (t *guiTracker) StageProgress(stage string, current int) {
	t.mu.Lock()
	now := time.Now()
	if now.Sub(t.lastEmit) < 100*time.Millisecond {
		t.mu.Unlock()
		return
	}
	t.lastEmit = now
	t.mu.Unlock()
	wailsRuntime.EventsEmit(t.appCtx, "pipeline-progress", progressEvent{
		Gen: t.gen, Stage: stage, Current: current,
	})
}

func (t *guiTracker) StageDone(stage string) {
	wailsRuntime.EventsEmit(t.appCtx, "pipeline-stage", stageEvent{
		Gen: t.gen, Stage: stage, Status: "done",
	})
}

// StageCached implements progress.CacheAware: marks the named running
// stage as a disk-cache replay so the frontend labels it "cache".
func (t *guiTracker) StageCached(stage string) {
	wailsRuntime.EventsEmit(t.appCtx, "pipeline-stage", stageEvent{
		Gen: t.gen, Stage: stage, Status: "cached",
	})
}

type heartbeatEvent struct {
	Gen       int64  `json:"gen"`
	Stage     string `json:"stage"`
	ElapsedMS int64  `json:"elapsedMs"`
}

// StageHeartbeat implements progress.Heartbeater: a liveness tick for
// a running stage that has emitted no progress for a while (see
// progress.Monitor). The frontend uses it to mark a stage as stalled
// when even heartbeats stop arriving — without it, a hung backend is
// indistinguishable from a busy one.
func (t *guiTracker) StageHeartbeat(stage string, elapsed time.Duration) {
	wailsRuntime.EventsEmit(t.appCtx, "pipeline-heartbeat", heartbeatEvent{
		Gen: t.gen, Stage: stage, ElapsedMS: elapsed.Milliseconds(),
	})
}

type warnEvent struct {
	Gen int64 `json:"gen"`
	// Kind is a stable identifier (e.g. "materialx-base-color") that
	// lets the frontend route the warning structurally — see the
	// constants in package progress. Empty kind = generic status-bar
	// warning with no specific UI home.
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

func (t *guiTracker) Warn(kind, message string) {
	// Reuses the existing "pipeline-warning" event listener in
	// App.svelte; the frontend updates the status banner and routes
	// kind-specific warnings (e.g. MaterialX base-color failures) to
	// their adjacent inline banners.
	wailsRuntime.EventsEmit(t.appCtx, "pipeline-warning", warnEvent{
		Gen: t.gen, Kind: kind, Message: message,
	})
}

// Compile-time checks that guiTracker implements progress.Tracker and
// the optional Heartbeater extension (so progress.Monitor's runtime
// type assertion finds it — a silent regression here would disable
// heartbeats entirely).
var (
	_ progress.Tracker     = (*guiTracker)(nil)
	_ progress.Heartbeater = (*guiTracker)(nil)
	_ progress.CacheAware  = (*guiTracker)(nil)
)

// processOne runs a single pipeline request under the mutex.
func (a *App) processOne(req pipelineRequest) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Backstop panic containment: stage bodies and worker pools convert
	// their own panics to errors (runStageCached, runParallel,
	// runClipJobs), but a panic anywhere else on this goroutine (event
	// emit, mesh post-processing) would kill pipelineWorker and leave
	// the GUI permanently unresponsive with no indication why. Convert
	// it to a pipeline-error event; the app survives and the next
	// settings change starts a fresh run.
	defer func() {
		if r := recover(); r != nil {
			plog.Printf("Pipeline gen %d PANIC: %v\n%s", req.gen, r, debug.Stack())
			wailsRuntime.EventsEmit(a.ctx, "pipeline-error", pipelineEvent{
				Gen:     req.gen,
				Message: fmt.Sprintf("internal error: %v", r),
			})
		}
	}()

	plog.Printf("Pipeline gen %d starting: %s (reloadSeq=%d)",
		req.gen, req.opts.Input, req.opts.ReloadSeq)
	plog.Printf("Pipeline gen %d opts: %+v", req.gen, req.opts)

	ctx, cancel := context.WithCancel(a.ctx)
	a.cancelMu.Lock()
	a.cancel = cancel
	a.cancelMu.Unlock()

	// lastPreviewScale captures the pipeline's pvScale (unitScale /
	// totalScale) so the output-mesh emit below can convert the
	// pipeline-mm pr.OutputMesh back to the GUI's preview-mm frame.
	// Tests read pr.OutputMesh directly and want pipeline-mm, so the
	// pipeline returns it unscaled; the GUI scaling lives here.
	var lastPreviewScale float32 = 1
	result, err := pipeline.RunCached(ctx, a.cache, req.opts, &pipeline.Callbacks{
		OnInputMesh: func(mesh *pipeline.MeshData, pvScale float32, extentMM float32, bboxMin, bboxMax [3]float32) {
			// Input mesh available — emit immediately so the preview appears
			// before later pipeline stages finish.
			lastPreviewScale = pvScale
			if a.lastInputID != "" {
				a.meshes.Remove(a.lastInputID)
			}
			a.lastInputID = a.meshes.Store(mesh)
			wailsRuntime.EventsEmit(a.ctx, "input-mesh", meshEvent{
				Gen:          req.gen,
				URL:          "/mesh/" + a.lastInputID,
				PreviewScale: pvScale,
				ExtentMM:     extentMM,
				BBoxMin:      bboxMin,
				BBoxMax:      bboxMax,
			})
		},
		OnAlphaWrappedMesh: func(mesh *pipeline.MeshData, pvScale float32) {
			// Mirrors OnStickerOverlay: mesh=nil clears the wrapped
			// preview so the frontend can fall back to the input view
			// when alpha-wrap is toggled off.
			if a.lastWrappedID != "" {
				a.meshes.Remove(a.lastWrappedID)
				a.lastWrappedID = ""
			}
			url := ""
			if mesh != nil {
				a.lastWrappedID = a.meshes.Store(mesh)
				url = "/mesh/" + a.lastWrappedID
			}
			wailsRuntime.EventsEmit(a.ctx, "wrapped-mesh",
				meshEvent{Gen: req.gen, URL: url, PreviewScale: pvScale})
		},
		OnOutputPreviewMesh: func(mesh *pipeline.MeshData, pvScale float32) {
			// Grey, colour-free snapshot of the output geometry emitted
			// mid-pipeline (after decimation, alpha-wrap, split) so the
			// Output Model viewer fills in before the final coloured mesh
			// is ready. Reuses lastOutputID so each snapshot — and the
			// final output-mesh below — replaces the previous one in
			// storage, bounding memory to a single output-side mesh.
			if a.lastOutputID != "" {
				a.meshes.Remove(a.lastOutputID)
			}
			a.lastOutputID = a.meshes.Store(mesh)
			wailsRuntime.EventsEmit(a.ctx, "output-preview-mesh", meshEvent{
				Gen:          req.gen,
				URL:          "/mesh/" + a.lastOutputID,
				PreviewScale: pvScale,
			})
		},
		OnStickerOverlay: func(mesh *pipeline.MeshData, pvScale float32) {
			// Alpha-wrap mode: stickers are carried by a separate mesh
			// that renders just outside the input mesh. The pipeline
			// fires this with mesh=nil when the overlay should be
			// cleared (alpha-wrap off, or no stickers), so the frontend
			// can drop any stale overlay deterministically.
			if a.lastOverlayID != "" {
				a.meshes.Remove(a.lastOverlayID)
				a.lastOverlayID = ""
			}
			url := ""
			if mesh != nil {
				a.lastOverlayID = a.meshes.Store(mesh)
				url = "/mesh/" + a.lastOverlayID
			}
			wailsRuntime.EventsEmit(a.ctx, "input-overlay-mesh",
				meshEvent{Gen: req.gen, URL: url, PreviewScale: pvScale})
		},
		OnPalette: func(pal [][3]uint8, tds []float32, labels []string, eff [][3]uint8) {
			// eff holds the TD-effective colors (what each filament looks like
			// once the translucent shell washes it toward the infill
			// filament), computed by the pipeline — which knows the true
			// infill and the resolved shell thickness; pal here is GUI-ordered
			// so pal[0] is NOT the infill. Powers the output viewer's
			// "Simulate print translucency" toggle. Opaque entries come back
			// byte-identical, so the frontend can detect a no-op palette and
			// disable the toggle.
			colors := make([]map[string]any, len(pal))
			for i, c := range pal {
				label := ""
				if i < len(labels) {
					label = labels[i]
				}
				td := float32(palette.DefaultTD)
				if i < len(tds) && tds[i] > 0 {
					td = tds[i]
				}
				e := c
				if i < len(eff) {
					e = eff[i]
				}
				colors[i] = map[string]any{
					"hex":          fmt.Sprintf("#%02X%02X%02X", c[0], c[1], c[2]),
					"effectiveHex": fmt.Sprintf("#%02X%02X%02X", e[0], e[1], e[2]),
					"label":        label,
					"td":           td,
				}
			}
			wailsRuntime.EventsEmit(a.ctx, "palette-resolved", map[string]any{
				"gen":    req.gen,
				"colors": colors,
			})
		},
		OnWarning: func(kind, msg string) {
			wailsRuntime.EventsEmit(a.ctx, "pipeline-warning", map[string]any{
				"gen":     req.gen,
				"kind":    kind,
				"message": msg,
			})
		},
		Progress: &guiTracker{appCtx: a.ctx, gen: req.gen},
	})
	if err != nil {
		if ctx.Err() != nil {
			plog.Printf("Pipeline gen %d cancelled", req.gen)
			wailsRuntime.EventsEmit(a.ctx, "pipeline-cancelled", pipelineEvent{Gen: req.gen})
			return
		}
		wailsRuntime.EventsEmit(a.ctx, "pipeline-error", pipelineEvent{
			Gen:     req.gen,
			Message: err.Error(),
		})
		return
	}
	optsCopy := req.opts
	a.lastOpts.Store(&optsCopy)

	if result.OutputMesh != nil {
		if a.lastOutputID != "" {
			a.meshes.Remove(a.lastOutputID)
		}
		// Scale pipeline-mm OutputMesh back to the GUI's preview-mm
		// frame so it lines up with the input mesh (which is already
		// pre-scaled inside the pipeline; see pipeline.RunCached's
		// onInputMesh path). PreviewScale rides on the event so the
		// frontend can derive scale-dependent things — same pattern
		// as input-mesh / wrapped-mesh / input-overlay-mesh.
		scaled := pipeline.ScalePreviewMesh(result.OutputMesh, lastPreviewScale)
		a.lastOutputID = a.meshes.Store(scaled)
		plog.Printf("Pipeline gen %d output mesh: %d verts, %d faces",
			req.gen, len(scaled.Vertices)/3, len(scaled.Faces)/3)
		wailsRuntime.EventsEmit(a.ctx, "output-mesh", meshEvent{
			Gen:          req.gen,
			URL:          "/mesh/" + a.lastOutputID,
			PreviewScale: lastPreviewScale,
		})
	}

	if result.NeedsForce {
		wailsRuntime.EventsEmit(a.ctx, "pipeline-needs-force", pipelineEvent{
			Gen:      req.gen,
			ExtentMM: result.ModelExtentMM,
		})
	} else {
		wailsRuntime.EventsEmit(a.ctx, "pipeline-done", pipelineEvent{
			Gen:        req.gen,
			Duration:   result.Duration.Seconds(),
			ColorUsage: result.ColorUsage,
		})
	}
	// Pipeline completed successfully; the disk cache may have grown.
	// kickDiskCacheSweep is a no-op if a previous sweep is still running.
	a.kickDiskCacheSweep()
}

// LogMessage prints a message from the frontend to stdout.
func (a *App) LogMessage(level, msg string) {
	plog.Printf("[JS %s] %s", level, msg)
}

// DebugCellsSlabResult is the payload returned by DebugCellsSlabSVG:
// the SVG markup for one slab plus the total slab count so the
// frontend can bound its slab-index slider without a second call.
//
// SVG is preferred over PNG here because the per-slab payload is
// produced from cell polygons directly — no rasterize+encode pass,
// no base64 inflation, and the browser handles render+scaling. This
// is what makes the slab slider responsive while dragging.
type DebugCellsSlabResult struct {
	SVG       string `json:"svg"`
	SlabCount int    `json:"slabCount"`
	// MedianCellAreaMM2 is the median cell polygon area (mm²) for the
	// returned slab, summarizing cell size for the debug view. 0 when the
	// slab has no cells.
	MedianCellAreaMM2 float32 `json:"medianCellAreaMM2"`
}

// DebugCellsSlabSVG renders one slab's cell partition (colored by
// sampled RGB) and returns the SVG markup. Requires the pipeline to
// have run through Voxelize at least once for the currently loaded
// options; returns an error otherwise. An empty SVG with SlabCount
// set is returned for slabs that have no footprint geometry.
func (a *App) DebugCellsSlabSVG(slabIdx int) (*DebugCellsSlabResult, error) {
	last := a.lastOpts.Load()
	if last == nil {
		return nil, fmt.Errorf("no model loaded yet — run the pipeline first")
	}
	svg, slabCount, medianArea, err := pipeline.CellsSlabSVG(a.cache, *last, slabIdx)
	if err != nil {
		return nil, err
	}
	return &DebugCellsSlabResult{
		SVG:               svg,
		SlabCount:         slabCount,
		MedianCellAreaMM2: medianArea,
	}, nil
}

// SelectCellDiagnostics resolves a picked point on the output model (in
// preview-mm world coordinates, as the 3D viewer's raycaster reports it)
// to a Voxelize cell and returns that cell's color-sampling diagnostics:
// the cell's outward normal plus every jittered sample ray (origin,
// direction, what surface it hit, the color it read). Powers the Debug ▸
// Select Cell tool. Requires the pipeline to have run through Voxelize.
func (a *App) SelectCellDiagnostics(x, y, z float64) (*pipeline.CellDiagInfo, error) {
	last := a.lastOpts.Load()
	if last == nil {
		return nil, fmt.Errorf("no model loaded yet — run the pipeline first")
	}
	return pipeline.CellDiagnosticsAt(a.cache, *last, [3]float32{float32(x), float32(y), float32(z)})
}

// DitherModePreviews renders the GUI dither modes over a caller-supplied
// source image and returns one base64 PNG data URI per mode (keyed by the
// DITHER_OPTIONS mode value). It feeds the Appearance section's visual
// dither-mode picker with a live, image-space preview of the currently loaded
// model.
//
// It is strictly read-only: it decodes an image, runs the real internal/voxel
// dither implementations via internal/ditherpreview, and returns PNGs. It
// touches no pipeline state, cache, or settings and never mutates a.* fields.
//
// srcPNGBase64 is a small PNG snapshot of the input viewer, either bare base64
// or a "data:image/png;base64,..." data URI. paletteHex is the filament-slot
// palette as "#rrggbb" strings (malformed/empty entries are skipped).
// riemersmaBias, blueNoiseTol and colorSnap are the live tuning-slider values;
// colorSnap is the "Color similarity threshold" (ΔE) applied via the pipeline's
// real voxel.SnapColors before dithering. Brightness/contrast/saturation and
// colour pins are already baked into the snapshot by the viewer shader.
func (a *App) DitherModePreviews(srcPNGBase64 string, paletteHex []string, riemersmaBias, blueNoiseTol, colorSnap float64) (map[string]string, error) {
	payload := srcPNGBase64
	if i := strings.Index(payload, "base64,"); i >= 0 {
		payload = payload[i+len("base64,"):]
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("decode source image: %w", err)
	}
	src, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode source PNG: %w", err)
	}

	pal := parseHexPalette(paletteHex)
	if len(pal) < 2 {
		return nil, fmt.Errorf("need at least 2 palette colors, got %d", len(pal))
	}

	tuning := ditherpreview.Tuning{RiemersmaBias: riemersmaBias, BlueNoiseTol: blueNoiseTol, ColorSnap: colorSnap}
	out := make(map[string]string, len(ditherpreview.Modes))
	for _, mode := range ditherpreview.Modes {
		img, err := ditherpreview.DitherImage(context.Background(), src, pal, mode, tuning)
		if err != nil {
			return nil, fmt.Errorf("dither %s: %w", mode, err)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, fmt.Errorf("encode %s: %w", mode, err)
		}
		out[mode] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	}
	return out, nil
}

// parseHexPalette converts "#rrggbb" strings to RGB triples, silently skipping
// empty or malformed entries (the caller enforces the minimum-count policy).
func parseHexPalette(hexes []string) [][3]uint8 {
	pal := make([][3]uint8, 0, len(hexes))
	for _, h := range hexes {
		h = strings.TrimPrefix(strings.TrimSpace(h), "#")
		if len(h) != 6 {
			continue
		}
		v, err := strconv.ParseUint(h, 16, 32)
		if err != nil {
			continue
		}
		pal = append(pal, [3]uint8{uint8(v >> 16), uint8(v >> 8), uint8(v)})
	}
	return pal
}

// Version returns the application version string.
func (a *App) Version() string {
	return pipeline.Version
}

// SplitPreview returns the cut-plane geometry for the model that's
// currently in the cache, computed from the supplied SplitSettings
// (typically the live values from the Settings panel as the user
// drags the offset slider). The mesh used is the most recently
// loaded model — this method does NOT trigger a full pipeline run.
//
// Returns an error when the model isn't loaded yet (the user hasn't
// run the pipeline since startup).
//
// Does NOT take a.mu — the worker holds that for the entire
// duration of a pipeline run, and the slider drag rate (~60Hz)
// can't tolerate that. lastOpts is read via atomic.Pointer; the
// cache read is goroutine-safe (disk-backed, no shared in-memory
// pointer with the worker).
func (a *App) SplitPreview(s pipeline.SplitSettings) (*pipeline.SplitPreviewResult, error) {
	last := a.lastOpts.Load()
	if last == nil {
		return nil, fmt.Errorf("no model loaded yet")
	}
	return pipeline.ComputeSplitPreview(a.cache, *last, s)
}

// PrinterOption describes one printer + its nozzle/layer-height options for
// the frontend printer selector. Layer heights are in mm.
type PrinterOption struct {
	ID          string         `json:"id"`
	DisplayName string         `json:"displayName"`
	Nozzles     []NozzleOption `json:"nozzles"`
}

// NozzleOption lists the layer heights available for one nozzle variant.
type NozzleOption struct {
	Diameter     string    `json:"diameter"`
	LayerHeights []float32 `json:"layerHeights"`
}

// ListPrinters returns the supported printer profiles the frontend can offer.
func (a *App) ListPrinters() ([]PrinterOption, error) {
	printers, err := export3mf.Registry()
	if err != nil {
		return nil, err
	}
	out := make([]PrinterOption, 0, len(printers))
	for _, p := range printers {
		nozzles := make([]NozzleOption, 0, len(p.Nozzles))
		for _, n := range p.Nozzles {
			lhs := make([]float32, 0, len(n.Processes))
			seen := map[float32]struct{}{}
			for _, pp := range n.Processes {
				if _, ok := seen[pp.LayerHeight]; ok {
					continue
				}
				seen[pp.LayerHeight] = struct{}{}
				lhs = append(lhs, pp.LayerHeight)
			}
			nozzles = append(nozzles, NozzleOption{
				Diameter:     n.Diameter,
				LayerHeights: lhs,
			})
		}
		out = append(out, PrinterOption{
			ID:          p.ID,
			DisplayName: p.DisplayName,
			Nozzles:     nozzles,
		})
	}
	return out, nil
}

// CollectionInfo describes a collection for the frontend.
type CollectionInfo struct {
	Name    string `json:"name"`
	Count   int    `json:"count"`
	BuiltIn bool   `json:"builtIn"`
}

// ColorEntry is a single color from a collection.
type ColorEntry struct {
	Hex   string  `json:"hex"`
	Label string  `json:"label"`
	TD    float32 `json:"td"` // transmission distance in mm; higher = more translucent
}

// ListCollections returns all available filament collections.
func (a *App) ListCollections() []CollectionInfo {
	cols := a.collections.List()
	result := make([]CollectionInfo, len(cols))
	for i, c := range cols {
		result[i] = CollectionInfo{
			Name:    c.Name,
			Count:   len(c.Entries),
			BuiltIn: c.BuiltIn,
		}
	}
	return result
}

// GetCollectionColors returns the colors in a named collection.
func (a *App) GetCollectionColors(name string) []ColorEntry {
	col, ok := a.collections.Get(name)
	if !ok {
		return nil
	}
	result := make([]ColorEntry, len(col.Entries))
	for i, e := range col.Entries {
		result[i] = ColorEntry{
			Hex:   fmt.Sprintf("#%02X%02X%02X", e.Color[0], e.Color[1], e.Color[2]),
			Label: e.Label,
			TD:    e.TD,
		}
	}
	return result
}

// DeleteCollection removes a user collection by name.
func (a *App) DeleteCollection(name string) error {
	return a.collections.Delete(name)
}

// ImportCollection copies an inventory file into the user collections
// directory and returns the new collection name.
func (a *App) ImportCollection() (string, error) {
	path, err := wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Import Filament Collection",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "Text Files (*.txt)", Pattern: "*.txt"},
			{DisplayName: "All Files", Pattern: "*"},
		},
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	col, err := a.collections.Import(path)
	if err != nil {
		return "", err
	}
	return col.Name, nil
}

// ExportCollection opens a native save dialog and writes the named
// collection (built-in or user) to a text file in the same
// "#RRGGBB Label" format ImportCollection reads. Returns the saved
// path, or empty if the user cancelled.
func (a *App) ExportCollection(name string) (string, error) {
	path, err := wailsRuntime.SaveFileDialog(a.ctx, wailsRuntime.SaveDialogOptions{
		Title:           "Export Filament Collection",
		DefaultFilename: name + ".txt",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "Text Files (*.txt)", Pattern: "*.txt"},
			{DisplayName: "All Files", Pattern: "*"},
		},
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	if err := a.collections.Export(name, path); err != nil {
		return "", err
	}
	return path, nil
}

// RenameCollection renames a user collection.
func (a *App) RenameCollection(oldName, newName string) error {
	return a.collections.Rename(oldName, newName)
}

// OpenStickerImage opens a file dialog for selecting a sticker image
// (PNG or JPEG). Returns the selected path, or empty if cancelled.
func (a *App) OpenStickerImage() (string, error) {
	return wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Select Sticker Image",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "Images (*.png, *.jpg, *.jpeg)", Pattern: "*.png;*.jpg;*.jpeg"},
		},
	})
}

// MaterialXOpenResult is the result of OpenMaterialXFile. Path is
// empty when the user cancels.
type MaterialXOpenResult struct {
	Path string `json:"path"`
}

// ValidateMaterialX parses the file at path and tries to compile a
// base-color sampler from it, returning a human-readable warning when
// anything is wrong (file missing, malformed XML, unsupported node
// type, or — since image nodes load their texture during compile —
// a missing texture inside a .zip). Empty result means the file is
// usable. Called by the frontend right after the user picks a file
// (and at JSON settings load) so problems surface immediately, not
// after a full pipeline run.
func (a *App) ValidateMaterialX(path string) string {
	if path == "" {
		return ""
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("MaterialX file not found: %s. Re-pick or place the file at that path.", path)
		}
		return fmt.Sprintf("MaterialX file unreadable: %v", err)
	}
	doc, err := materialx.ParsePackage(path)
	if err != nil {
		return fmt.Sprintf("MaterialX parse failed: %v", err)
	}
	if _, err := doc.DefaultBaseColorSampler(); err != nil {
		return fmt.Sprintf("MaterialX base color unsupported: %v", err)
	}
	return ""
}

// OpenMaterialXFile opens a file dialog for selecting a MaterialX
// .mtlx file or a .zip archive containing one (with adjacent
// textures). The pipeline opens the file directly from the path at
// run time — there's no need to round-trip its content through the
// frontend.
func (a *App) OpenMaterialXFile() (*MaterialXOpenResult, error) {
	path, err := wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Select MaterialX File or Texture Pack",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "MaterialX (*.mtlx, *.zip)", Pattern: "*.mtlx;*.zip"},
		},
	})
	if err != nil || path == "" {
		return &MaterialXOpenResult{}, err
	}
	return &MaterialXOpenResult{Path: path}, nil
}

// ReadStickerThumbnail reads a sticker image and returns a base64 data URL
// thumbnail (max 64x64, preserving aspect ratio).
func (a *App) ReadStickerThumbnail(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open sticker: %w", err)
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return "", fmt.Errorf("decode sticker: %w", err)
	}

	const maxDim = 64
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w > maxDim || h > maxDim {
		if w >= h {
			h = h * maxDim / w
			w = maxDim
		} else {
			w = w * maxDim / h
			h = maxDim
		}
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}

	thumb := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.BiLinear.Scale(thumb, thumb.Bounds(), img, bounds, draw.Over, nil)

	var buf bytes.Buffer
	if err := png.Encode(&buf, thumb); err != nil {
		return "", fmt.Errorf("encode thumbnail: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// SaveSettings writes settings to the given path. The format, the
// overwrite guard, and the on-disk-asset path relativisation all live in
// internal/settings so the CLI shares them verbatim.
func (a *App) SaveSettings(path string, s Settings) error {
	if err := settings.Save(path, s); err != nil {
		return err
	}
	a.rememberSettingsPath(path)
	return nil
}

// rememberSettingsPath records path as the current settings file (empty is
// ignored), so exports can default their save dialog to its directory.
func (a *App) rememberSettingsPath(path string) {
	if path == "" {
		return
	}
	a.settingsPath.Store(&path)
}

// SaveSettingsDialog opens a save dialog and writes settings to the chosen path.
// Returns the saved path, or empty if the user cancelled.
func (a *App) SaveSettingsDialog(s Settings) (string, error) {
	path, err := wailsRuntime.SaveFileDialog(a.ctx, wailsRuntime.SaveDialogOptions{
		Title:           "Save",
		DefaultFilename: "settings.json",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "DitherForge Settings (*.json)", Pattern: "*.json"},
		},
	})
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	if err := a.SaveSettings(path, s); err != nil {
		return "", err
	}
	// SaveSettings already recorded the path; keep this explicit for clarity.
	a.rememberSettingsPath(path)
	return path, nil
}

// OpenFileDialog opens a file picker for DitherForge settings JSON files.
// The input model is now picked separately via OpenModelDialog (it is a
// regular setting in the left panel, not a File > Open target). Returns the
// selected path, or empty if cancelled.
func (a *App) OpenFileDialog() (string, error) {
	return wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Open Settings",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "DitherForge Settings (*.json)", Pattern: "*.json"},
		},
	})
}

// OpenModelDialog opens a file picker for the input 3D model. Kept separate
// from OpenFileDialog so the input model can be changed independently as a
// setting without going through the File menu.
func (a *App) OpenModelDialog() (string, error) {
	return wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Open Model",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "3D Models (*.glb, *.3mf, *.stl, *.obj, *.dae, *.zip)", Pattern: "*.glb;*.3mf;*.stl;*.obj;*.dae;*.zip"},
		},
	})
}

// EnumerateObjects returns the list of objects in a multi-object 3MF or GLB file.
// Returns nil for formats that don't support multiple objects (e.g. STL).
func (a *App) EnumerateObjects(path string) ([]loader.ObjectInfo, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".3mf":
		return loader.Enumerate3MFObjects(path)
	case ".glb":
		return loader.EnumerateGLBObjects(path)
	default:
		return nil, nil
	}
}

// LoadSettingsFile reads settings from the given path.
//
// Asset paths (input model, MaterialX file, sticker images) are
// resolved against the JSON file's directory if stored relative — the
// inverse of SaveSettings's relativisation. Absolute paths in the
// JSON pass through unchanged, so settings files that pre-date the
// relative-path feature still load correctly.
func (a *App) LoadSettingsFile(path string) (*LoadSettingsResult, error) {
	plog.Printf("Opening settings file: %s", path)
	s, legacyAbsoluteUnits, err := settings.Load(path)
	if err != nil {
		return nil, err
	}
	a.rememberSettingsPath(path)
	return &LoadSettingsResult{Path: path, Settings: s, LegacyAbsoluteUnits: legacyAbsoluteUnits}, nil
}

// LoadSettingsResult is returned by LoadSettingsDialog/LoadSettingsFile.
type LoadSettingsResult struct {
	Path     string   `json:"path"`
	Settings Settings `json:"settings"`
	// LegacyAbsoluteUnits is true when the loaded file predates the
	// fraction-of-extent format and stores the size-relative fields
	// (split offset, sticker center/scale, MaterialX tile size) as absolute
	// mm. The frontend uses it to convert those values to fractions once the
	// model extent is known, rather than treating them as fractions directly.
	LegacyAbsoluteUnits bool `json:"legacyAbsoluteUnits"`
}

// DefaultSettingsPath returns a .json path derived from the input file path.
func (a *App) DefaultSettingsPath(inputFile string) string {
	if inputFile == "" {
		return ""
	}
	ext := filepath.Ext(inputFile)
	return strings.TrimSuffix(inputFile, ext) + ".json"
}
