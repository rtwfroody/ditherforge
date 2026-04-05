package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/rtwfroody/ditherforge/internal/pipeline"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the Wails application backend.
type App struct {
	ctx      context.Context
	mu       sync.Mutex
	cancel   context.CancelFunc       // cancels in-flight pipeline work
	cache    *pipeline.StageCache     // per-stage cache across runs
	lastOpts pipeline.Options         // last successfully processed options
	pipeGen     int                   // incremented per ProcessPipeline call
	meshes      *meshHandler          // serves binary mesh data over HTTP
	lastInputID  string               // mesh handler ID for last input mesh
	lastOutputID string               // mesh handler ID for last output mesh
}

// NewApp creates a new App instance.
func NewApp() *App {
	return &App{
		cache:  pipeline.NewStageCache(),
		meshes: newMeshHandler(),
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

func (a *App) shutdown(ctx context.Context) {
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

// meshEvent is the payload sent via Wails events for mesh data.
// URL points to a binary mesh endpoint served by meshHandler.
type meshEvent struct {
	Gen int    `json:"gen"`
	URL string `json:"url"`
}

// ProcessPipeline runs the full pipeline with per-stage caching.
// Only stages whose settings changed are re-executed.
// The mutex is held for the entire call to prevent concurrent access to the
// stage cache. The previous run is cancelled first so it returns quickly.
// Mesh data is stored in the mesh handler and a URL is sent via events,
// so the frontend can fetch binary data without JSON serialization overhead.
func (a *App) ProcessPipeline(opts pipeline.Options) (*pipeline.ProcessResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.cancel = cancel
	a.pipeGen++
	gen := a.pipeGen

	result, err := pipeline.RunCached(ctx, a.cache, opts)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Println("ProcessPipeline cancelled")
		}
		return nil, err
	}
	a.lastOpts = opts

	if result.InputMesh != nil {
		if a.lastInputID != "" {
			a.meshes.Remove(a.lastInputID)
		}
		a.lastInputID = a.meshes.Store(result.InputMesh)
		wailsRuntime.EventsEmit(a.ctx, "input-mesh", meshEvent{Gen: gen, URL: "/mesh/" + a.lastInputID})
	}
	if result.OutputMesh != nil {
		if a.lastOutputID != "" {
			a.meshes.Remove(a.lastOutputID)
		}
		a.lastOutputID = a.meshes.Store(result.OutputMesh)
		wailsRuntime.EventsEmit(a.ctx, "output-mesh", meshEvent{Gen: gen, URL: "/mesh/" + a.lastOutputID})
	}

	return result, nil
}

// LoadModelPreview loads a model file and sends a binary mesh URL via the
// "input-mesh" event.
func (a *App) LoadModelPreview(path string) error {
	mesh, err := pipeline.LoadPreview(path)
	if err != nil {
		return err
	}
	a.mu.Lock()
	gen := a.pipeGen
	if a.lastInputID != "" {
		a.meshes.Remove(a.lastInputID)
	}
	a.lastInputID = a.meshes.Store(mesh)
	a.mu.Unlock()
	wailsRuntime.EventsEmit(a.ctx, "input-mesh", meshEvent{Gen: gen, URL: "/mesh/" + a.lastInputID})
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
