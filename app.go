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
}

// NewApp creates a new App instance.
func NewApp() *App {
	return &App{
		cache: pipeline.NewStageCache(),
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

// ProcessPipeline runs the full pipeline with per-stage caching.
// Only stages whose settings changed are re-executed.
// The mutex is held for the entire call to prevent concurrent access to the
// stage cache. The previous run is cancelled first so it returns quickly.
func (a *App) ProcessPipeline(opts pipeline.Options) (*pipeline.ProcessResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.cancel = cancel

	result, err := pipeline.RunCached(ctx, a.cache, opts)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Println("ProcessPipeline cancelled")
		}
		return nil, err
	}
	a.lastOpts = opts
	return result, nil
}

// LoadModelPreview loads a model file and returns mesh data for 3D preview.
func (a *App) LoadModelPreview(path string) (*pipeline.MeshData, error) {
	return pipeline.LoadPreview(path)
}

// LogMessage prints a message from the frontend to stdout.
func (a *App) LogMessage(level, msg string) {
	fmt.Printf("[JS %s] %s\n", level, msg)
}

// Version returns the application version string.
func (a *App) Version() string {
	return pipeline.Version
}
