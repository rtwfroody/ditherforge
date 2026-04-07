package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/rtwfroody/ditherforge/internal/collection"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the Wails application backend.
type App struct {
	ctx      context.Context
	mu       sync.Mutex               // protects cache and lastOpts; held during pipeline execution and SaveFile
	cancelMu sync.Mutex               // protects cancel func; separate from mu so ProcessPipeline can cancel without blocking
	cancel   context.CancelFunc       // cancels in-flight pipeline work
	cache    *pipeline.StageCache     // per-stage cache across runs
	lastOpts pipeline.Options         // last successfully processed options
	pipeGen      atomic.Int64         // generation counter for pipeline requests
	meshes       *meshHandler         // serves binary mesh data over HTTP
	lastInputID   string              // mesh handler ID for last input mesh (protected by mu)
	lastOutputID  string              // mesh handler ID for last output mesh (protected by mu)
	lastPreviewID string              // mesh handler ID for last preview mesh (protected by mu)
	hasPreview    atomic.Bool              // true after LoadModelPreview; read by processOne without mu
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
	Gen int64  `json:"gen"`
	URL string `json:"url"`
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

// SelectInputFile opens a native file picker for .glb/.3mf files.
func (a *App) SelectInputFile() (string, error) {
	return wailsRuntime.OpenFileDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Select Input Model",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "3D Models (*.glb, *.3mf)", Pattern: "*.glb;*.3mf"},
		},
	})
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

// SaveFile acquires the pipeline mutex, opens a native save dialog, and
// exports the 3MF file using cached pipeline results. Returns the saved
// path, or empty if the user cancelled.
func (a *App) SaveFile() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	path, err := wailsRuntime.SaveFileDialog(a.ctx, wailsRuntime.SaveDialogOptions{
		Title:           "Save Output",
		DefaultFilename: "output.3mf",
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

// processOne runs a single pipeline request under the mutex.
func (a *App) processOne(req pipelineRequest) {
	a.mu.Lock()
	defer a.mu.Unlock()

	ctx, cancel := context.WithCancel(a.ctx)
	a.cancelMu.Lock()
	a.cancel = cancel
	a.cancelMu.Unlock()

	result, err := pipeline.RunCached(ctx, a.cache, req.opts, func(pal [][3]uint8, labels []string) {
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

	// Don't replace the input viewer's mesh — the preview is already
	// showing the input at its native scale, and the pipeline's input
	// mesh is rescaled. The output mesh vertices are converted to
	// preview scale in the pipeline so both viewers use the same
	// coordinate space.
	if result.InputMesh != nil && !a.hasPreview.Load() {
		// No preview exists (shouldn't happen in GUI, but be safe).
		if a.lastInputID != "" {
			a.meshes.Remove(a.lastInputID)
		}
		a.lastInputID = a.meshes.Store(result.InputMesh)
		wailsRuntime.EventsEmit(a.ctx, "input-mesh", meshEvent{Gen: req.gen, URL: "/mesh/" + a.lastInputID})
	}
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

// LoadModelPreview loads a model file and sends a binary mesh URL via the
// "input-mesh" event. Does not acquire the pipeline mutex so the preview
// appears while the pipeline is still running.
func (a *App) LoadModelPreview(path string) error {
	mesh, err := pipeline.LoadPreview(path)
	if err != nil {
		return err
	}
	a.mu.Lock()
	if a.lastPreviewID != "" {
		a.meshes.Remove(a.lastPreviewID)
	}
	a.lastPreviewID = a.meshes.Store(mesh)
	a.mu.Unlock()
	a.hasPreview.Store(true)
	gen := a.pipeGen.Load()
	wailsRuntime.EventsEmit(a.ctx, "input-mesh", meshEvent{Gen: gen, URL: "/mesh/" + a.lastPreviewID})
	return nil
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
