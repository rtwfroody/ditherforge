package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rtwfroody/ditherforge/internal/collection"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
	"github.com/rtwfroody/ditherforge/internal/progress"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the Wails application backend.
type App struct {
	ctx      context.Context
	mu       sync.Mutex               // protects cache and lastOpts; held during pipeline execution and Export3MF
	cancelMu sync.Mutex               // protects cancel func; separate from mu so ProcessPipeline can cancel without blocking
	cancel   context.CancelFunc       // cancels in-flight pipeline work
	cache    *pipeline.StageCache     // per-stage cache across runs
	lastOpts pipeline.Options         // last successfully processed options
	pipeGen      atomic.Int64         // generation counter for pipeline requests
	meshes       *meshHandler         // serves binary mesh data over HTTP
	lastInputID   string              // mesh handler ID for last input mesh (protected by mu)
	lastOutputID  string              // mesh handler ID for last output mesh (protected by mu)
	reqCh         chan pipelineRequest    // buffered channel for pipeline requests; worker drains to latest
	collections   *collection.Manager    // filament collection manager
}

// pipelineRequest is sent from ProcessPipeline to the worker goroutine.
type pipelineRequest struct {
	opts pipeline.Options
	gen  int64
}

// pipelineEvent is emitted to the frontend via Wails events.
type pipelineEvent struct {
	Gen      int64   `json:"gen"`
	Duration float64 `json:"duration,omitempty"` // seconds, for pipeline-done
	Message  string  `json:"message,omitempty"`  // error text, for pipeline-error
	ExtentMM float32 `json:"extentMM,omitempty"` // model extent, for pipeline-needs-force
}

// meshEvent is the payload sent via Wails events for mesh data.
// URL points to a binary mesh endpoint served by meshHandler.
type meshEvent struct {
	Gen          int64   `json:"gen"`
	URL          string  `json:"url"`
	PreviewScale float32 `json:"previewScale,omitempty"`
}

// NewApp creates a new App instance.
func NewApp() *App {
	cm, err := collection.NewManager()
	if err != nil {
		// Fatal: collections are required for the GUI to function.
		panic(fmt.Sprintf("failed to initialize collection manager: %v", err))
	}
	return &App{
		cache:       pipeline.NewStageCache(),
		meshes:      newMeshHandler(),
		reqCh:       make(chan pipelineRequest, 1),
		collections: cm,
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go a.pipelineWorker()
}

func (a *App) shutdown(ctx context.Context) {
	close(a.reqCh)
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
		entries[i] = palette.InventoryEntry{Color: rgb[0], Label: c.Label}
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
	return &ColorEntry{Hex: hex, Label: label}, nil
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

	defaultName := "output.3mf"
	if a.lastOpts.Input != "" {
		base := filepath.Base(a.lastOpts.Input)
		ext := filepath.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		if strings.EqualFold(ext, ".3mf") {
			defaultName = stem + "-df.3mf"
		} else {
			defaultName = stem + ".3mf"
		}
	}

	path, err := wailsRuntime.SaveFileDialog(a.ctx, wailsRuntime.SaveDialogOptions{
		Title:           "Save Output",
		DefaultFilename: defaultName,
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

	_, err = pipeline.ExportFile(a.cache, path, a.lastOpts.LayerHeight)
	if err != nil {
		return "", err
	}
	return path, nil
}

// ProcessPipeline enqueues a pipeline request and returns immediately.
// The single pipelineWorker goroutine processes only the latest request.
// Results are delivered via Wails events: pipeline-done, pipeline-error,
// pipeline-needs-force, input-mesh, output-mesh.
// Returns the generation number assigned to this request, which the frontend
// uses to filter stale events.
func (a *App) ProcessPipeline(opts pipeline.Options) int64 {
	gen := a.pipeGen.Add(1)

	// Cancel any in-flight pipeline immediately so it aborts early.
	a.cancelMu.Lock()
	if a.cancel != nil {
		a.cancel()
	}
	a.cancelMu.Unlock()

	// Replace any pending request in the channel. Drain first (non-blocking),
	// then send into the now-empty buffer-1 channel.
	req := pipelineRequest{opts: opts, gen: gen}
	select {
	case <-a.reqCh:
	default:
	}
	a.reqCh <- req

	return gen
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
	Status      string `json:"status"` // "running" or "done"
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

// Compile-time check that guiTracker implements progress.Tracker.
var _ progress.Tracker = (*guiTracker)(nil)

// processOne runs a single pipeline request under the mutex.
func (a *App) processOne(req pipelineRequest) {
	a.mu.Lock()
	defer a.mu.Unlock()

	ctx, cancel := context.WithCancel(a.ctx)
	a.cancelMu.Lock()
	a.cancel = cancel
	a.cancelMu.Unlock()

	result, err := pipeline.RunCached(ctx, a.cache, req.opts, &pipeline.Callbacks{
		OnInputMesh: func(mesh *pipeline.MeshData, pvScale float32) {
			// Input mesh available — emit immediately so the preview appears
			// before later pipeline stages finish.
			if a.lastInputID != "" {
				a.meshes.Remove(a.lastInputID)
			}
			a.lastInputID = a.meshes.Store(mesh)
			wailsRuntime.EventsEmit(a.ctx, "input-mesh", meshEvent{Gen: req.gen, URL: "/mesh/" + a.lastInputID, PreviewScale: pvScale})
		},
		OnPalette: func(pal [][3]uint8, labels []string) {
			colors := make([]map[string]string, len(pal))
			for i, c := range pal {
				label := ""
				if i < len(labels) {
					label = labels[i]
				}
				colors[i] = map[string]string{
					"hex":   fmt.Sprintf("#%02X%02X%02X", c[0], c[1], c[2]),
					"label": label,
				}
			}
			wailsRuntime.EventsEmit(a.ctx, "palette-resolved", map[string]any{
				"gen":    req.gen,
				"colors": colors,
			})
		},
		Progress: &guiTracker{appCtx: a.ctx, gen: req.gen},
	})
	if err != nil {
		if ctx.Err() != nil {
			fmt.Printf("Pipeline gen %d cancelled\n", req.gen)
			wailsRuntime.EventsEmit(a.ctx, "pipeline-cancelled", pipelineEvent{Gen: req.gen})
			return
		}
		wailsRuntime.EventsEmit(a.ctx, "pipeline-error", pipelineEvent{
			Gen:     req.gen,
			Message: err.Error(),
		})
		return
	}
	a.lastOpts = req.opts

	if result.OutputMesh != nil {
		if a.lastOutputID != "" {
			a.meshes.Remove(a.lastOutputID)
		}
		a.lastOutputID = a.meshes.Store(result.OutputMesh)
		wailsRuntime.EventsEmit(a.ctx, "output-mesh", meshEvent{Gen: req.gen, URL: "/mesh/" + a.lastOutputID})
	}

	if result.NeedsForce {
		wailsRuntime.EventsEmit(a.ctx, "pipeline-needs-force", pipelineEvent{
			Gen:      req.gen,
			ExtentMM: result.ModelExtentMM,
		})
	} else {
		wailsRuntime.EventsEmit(a.ctx, "pipeline-done", pipelineEvent{
			Gen:      req.gen,
			Duration: result.Duration.Seconds(),
		})
	}
}

// LogMessage prints a message from the frontend to stdout.
func (a *App) LogMessage(level, msg string) {
	fmt.Printf("[JS %s] %s\n", level, msg)
}

// Version returns the application version string.
func (a *App) Version() string {
	return pipeline.Version
}

// CollectionInfo describes a collection for the frontend.
type CollectionInfo struct {
	Name    string `json:"name"`
	Count   int    `json:"count"`
	BuiltIn bool   `json:"builtIn"`
}

// ColorEntry is a single color from a collection.
type ColorEntry struct {
	Hex   string `json:"hex"`
	Label string `json:"label"`
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

// RenameCollection renames a user collection.
func (a *App) RenameCollection(oldName, newName string) error {
	return a.collections.Rename(oldName, newName)
}

// OpenStickerImage opens a file dialog for selecting a PNG sticker image.
// Returns the selected path, or empty if cancelled.
func (a *App) OpenStickerImage() (string, error) {
	return wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Select Sticker Image",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "PNG Images (*.png)", Pattern: "*.png"},
		},
	})
}

// SettingsFile is the JSON structure written to/read from .json settings files.
type SettingsFile struct {
	DitherForge SettingsMeta `json:"_ditherforge"`
	Settings    Settings     `json:"settings"`
}

// SettingsMeta contains metadata about the settings file.
type SettingsMeta struct {
	URL     string `json:"url"`
	Version string `json:"version"`
}

// StickerSetting is the JSON representation of a sticker for settings persistence.
type StickerSetting struct {
	ImagePath string     `json:"imagePath"`
	Center    [3]float64 `json:"center"`
	Normal    [3]float64 `json:"normal"`
	Up        [3]float64 `json:"up"`
	Scale     float64    `json:"scale"`
	Rotation  float64    `json:"rotation"`
}

// WarpPinSetting is the JSON representation of a color warp pin.
type WarpPinSetting struct {
	SourceHex   string `json:"sourceHex"`
	TargetHex   string `json:"targetHex"`
	TargetLabel string `json:"targetLabel,omitempty"`
	Sigma       float64 `json:"sigma"`
}

// ColorSlotSetting is the JSON representation of a color slot.
type ColorSlotSetting struct {
	Hex        string `json:"hex"`
	Label      string `json:"label,omitempty"`
	Collection string `json:"collection,omitempty"`
}

// Settings contains all user-configurable settings.
type Settings struct {
	InputFile           string             `json:"inputFile,omitempty"`
	SizeMode            string             `json:"sizeMode"`
	SizeValue           string             `json:"sizeValue"`
	ScaleValue          string             `json:"scaleValue"`
	NozzleDiameter      string             `json:"nozzleDiameter"`
	LayerHeight         string             `json:"layerHeight"`
	ColorSlots          []*ColorSlotSetting `json:"colorSlots"`
	InventoryCollection string             `json:"inventoryCollection"`
	Brightness          float64            `json:"brightness"`
	Contrast            float64            `json:"contrast"`
	Saturation          float64            `json:"saturation"`
	WarpPins            []WarpPinSetting   `json:"warpPins"`
	Stickers            []StickerSetting   `json:"stickers,omitempty"`
	Dither              string             `json:"dither"`
	ColorSnap           float64            `json:"colorSnap"`
	NoMerge             bool               `json:"noMerge"`
	NoSimplify          bool               `json:"noSimplify"`
	Stats               bool               `json:"stats"`
}

// SaveSettings writes settings to the given path.
// If the file already exists and is not a DitherForge settings file, it refuses
// to overwrite it.
func (a *App) SaveSettings(path string, settings Settings) error {
	if data, err := os.ReadFile(path); err == nil {
		var existing SettingsFile
		if jsonErr := json.Unmarshal(data, &existing); jsonErr != nil || existing.DitherForge.URL == "" {
			return fmt.Errorf("refusing to overwrite %s: not a DitherForge settings file", filepath.Base(path))
		}
	}
	sf := SettingsFile{
		DitherForge: SettingsMeta{
			URL:     "https://github.com/rtwfroody/ditherforge",
			Version: pipeline.Version,
		},
		Settings: settings,
	}
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
}

// SaveSettingsDialog opens a save dialog and writes settings to the chosen path.
// Returns the saved path, or empty if the user cancelled.
func (a *App) SaveSettingsDialog(settings Settings) (string, error) {
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
	if err := a.SaveSettings(path, settings); err != nil {
		return "", err
	}
	return path, nil
}

// OpenFileDialog opens a file picker that accepts .json, .glb, and .3mf files.
// Returns the selected path, or empty if cancelled.
func (a *App) OpenFileDialog() (string, error) {
	return wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Open",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "All Supported (*.json, *.glb, *.3mf, *.stl)", Pattern: "*.json;*.glb;*.3mf;*.stl"},
			{DisplayName: "DitherForge Settings (*.json)", Pattern: "*.json"},
			{DisplayName: "3D Models (*.glb, *.3mf, *.stl)", Pattern: "*.glb;*.3mf;*.stl"},
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
func (a *App) LoadSettingsFile(path string) (*LoadSettingsResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	var sf SettingsFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("parse settings: %w", err)
	}
	if sf.DitherForge.URL == "" {
		return nil, fmt.Errorf("not a DitherForge settings file (missing _ditherforge metadata)")
	}
	return &LoadSettingsResult{
		Path:     path,
		Settings: sf.Settings,
	}, nil
}

// LoadSettingsResult is returned by LoadSettingsDialog/LoadSettingsFile.
type LoadSettingsResult struct {
	Path     string   `json:"path"`
	Settings Settings `json:"settings"`
}

// DefaultSettingsPath returns a .json path derived from the input file path.
func (a *App) DefaultSettingsPath(inputFile string) string {
	if inputFile == "" {
		return ""
	}
	ext := filepath.Ext(inputFile)
	return strings.TrimSuffix(inputFile, ext) + ".json"
}
