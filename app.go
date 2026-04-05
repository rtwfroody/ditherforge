package main

import (
	"context"
	"fmt"

	"github.com/rtwfroody/ditherforge/internal/pipeline"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the Wails application backend.
type App struct {
	ctx      context.Context
	prepared *pipeline.PreparedModel // cached between Prepare and Render calls
}

// NewApp creates a new App instance.
func NewApp() *App {
	return &App{}
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

// SelectOutputFile opens a native save dialog.
func (a *App) SelectOutputFile() (string, error) {
	return wailsRuntime.SaveFileDialog(a.ctx, wailsRuntime.SaveDialogOptions{
		Title:           "Save Output",
		DefaultFilename: "output.3mf",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "3MF Files (*.3mf)", Pattern: "*.3mf"},
		},
	})
}

// PreparePipeline loads and voxelizes the model. The result is cached
// so that subsequent RenderPipeline calls skip the expensive geometry work.
func (a *App) PreparePipeline(opts pipeline.Options) (*pipeline.PrepareResult, error) {
	a.prepared = nil
	pm, result, err := pipeline.Prepare(opts)
	if err != nil {
		return nil, err
	}
	if result.NeedsForce {
		return result, nil
	}
	a.prepared = pm
	return result, nil
}

// RenderPipeline applies color options to the previously prepared model
// and exports the result. Requires a prior PreparePipeline call.
func (a *App) RenderPipeline(opts pipeline.Options) (*pipeline.Result, error) {
	if a.prepared == nil {
		return nil, fmt.Errorf("no prepared model; call PreparePipeline first")
	}
	return pipeline.Render(a.prepared, opts)
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
