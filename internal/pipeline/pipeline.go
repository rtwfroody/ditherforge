// Package pipeline implements the core ditherforge processing pipeline:
// load model, validate, remesh, and export to 3MF.
package pipeline

import (
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rtwfroody/ditherforge/internal/alphawrap"
	"github.com/rtwfroody/ditherforge/internal/export3mf"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/squarevoxel"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// Options controls the pipeline behavior. Mirrors CLI flags.
type Options struct {
	Input          string
	NumColors      int
	LockedColors   []string
	Scale          float32
	Output         string
	BaseColor      string // hex color for untextured faces (e.g. "#FF0000"); empty = use model default
	NozzleDiameter float32
	LayerHeight    float32
	// Printer is the printer profile ID (e.g. "snapmaker_u1") used when
	// writing the 3MF project settings. Empty = export3mf.DefaultPrinterID.
	// NozzleDiameter selects the matching nozzle variant within that printer.
	Printer string
	InventoryFile    string
	InventoryColors  [][3]uint8 `json:"InventoryColors,omitempty"`
	InventoryLabels  []string   `json:"InventoryLabels,omitempty"` // parallel to InventoryColors
	Brightness     float32
	Contrast       float32
	Saturation     float32
	Dither         string
	NoMerge        bool
	NoSimplify     bool
	Size           *float32
	Force          bool
	ReloadSeq      int64 // bumped to force re-read of the same input file
	Stats          bool
	ColorSnap      float64
	WarpPins       []WarpPin `json:"WarpPins,omitempty"`
	Stickers       []Sticker `json:"Stickers,omitempty"`
	ObjectIndex    int       `json:"ObjectIndex"` // -1 = all objects, >=0 = specific object
	AlphaWrap       bool    // enable CGAL Alpha_wrap_3 post-load mesh cleanup
	AlphaWrapAlpha  float32 // mm; 0 = auto (5 × NozzleDiameter)
	AlphaWrapOffset float32 // mm; 0 = auto (alpha / 30)
}

// Sticker defines a PNG image to apply onto the voxelized mesh surface.
type Sticker struct {
	ImagePath string     `json:"ImagePath"`
	Center    [3]float64 `json:"Center"`    // world-space placement point
	Normal    [3]float64 `json:"Normal"`    // surface normal at placement
	Up        [3]float64 `json:"Up"`        // camera up vector at placement time
	Scale    float64 `json:"Scale"`    // world-unit width of sticker
	Rotation float64 `json:"Rotation"` // degrees, around surface normal
	MaxAngle float64 `json:"MaxAngle"` // max inter-triangle angle (degrees) for flood-fill; 0 = no limit
	Mode     string  `json:"Mode"`     // "projection" (default) or "unfold"
}

// WarpPin maps a source image color to a target filament color for RBF warping.
type WarpPin struct {
	SourceHex string  `json:"sourceHex"` // e.g. "#FF0000"
	TargetHex string  `json:"targetHex"` // e.g. "#00FF00"
	Sigma     float64 `json:"sigma"`     // falloff in delta-E units; 0 = auto
}

// Callbacks groups optional callbacks for a pipeline run.
type Callbacks struct {
	OnInputMesh func(*MeshData, float32, float32) // mesh, preview scale, native extent mm
	// OnStickerOverlay is fired when stickers are placed on a mesh
	// distinct from the input mesh — i.e. the alpha-wrap surface. The
	// overlay should be rendered on top of the input mesh, biased
	// slightly toward the camera to avoid z-fighting. nil call when
	// alpha-wrap is off (the overlay is already baked into the input
	// mesh's StickerUVs in that case).
	OnStickerOverlay func(*MeshData, float32)
	OnPalette        func([][3]uint8, []string)
	// OnWarning is called for non-fatal user-facing notices (e.g. an
	// LSCM solve that didn't converge cleanly). The frontend should
	// surface these via a non-blocking toast or status line.
	OnWarning func(string)
	Progress  progress.Tracker
}

// stageNames maps StageID to a human-readable name for progress reporting.
var stageNames = map[StageID]string{
	StageParse:       "Parsing",
	StageLoad:        "Loading",
	StageVoxelize:    "Voxelizing",
	StageSticker:     "Applying stickers",
	StageDecimate:    "Decimating",
	StageColorAdjust: "Adjusting colors",
	StageColorWarp:   "Warping colors",
	StagePalette:     "Building palette",
	StageDither:      "Dithering",
	StageClip:        "Clipping",
	StageMerge:       "Merging",
}

// MeshData holds flat arrays for 3D preview rendering.
type MeshData struct {
	Vertices       []float32 `json:"Vertices"`                 // flat [x,y,z, x,y,z, ...]
	Faces          []uint32  `json:"Faces"`                    // flat [i,j,k, i,j,k, ...]
	FaceColors     []uint16  `json:"FaceColors"`               // flat [r,g,b, r,g,b, ...] per face (uint16 to avoid base64 JSON encoding of []uint8)
	UVs            []float32 `json:"UVs,omitempty"`            // flat [u,v, u,v, ...] per vertex, optional
	Textures       []string  `json:"Textures,omitempty"`       // base64 JPEG images, optional
	FaceTextureIdx []int32   `json:"FaceTextureIdx,omitempty"` // per-face texture index; -1 = use FaceColors

	// Sticker overlay: per-face sticker UVs referencing a combined atlas texture.
	StickerUVs      []float32 `json:"StickerUVs,omitempty"`      // flat [u,v, u,v, ...] per face-vertex (nFaces*6), nil if no stickers
	StickerFaceMask []uint8   `json:"StickerFaceMask,omitempty"` // 1 per face: 1=has sticker, 0=none, nil if no stickers
	StickerBounds   []float32 `json:"StickerBounds,omitempty"`   // flat [minU,maxU,minV,maxV, ...] per face (nFaces*4), atlas sub-region for shader clamping
	StickerAtlas    string    `json:"StickerAtlas,omitempty"`     // base64 encoded atlas image, empty if no stickers
}

// ProcessResult summarizes a completed pipeline run (stages 0–6, no file export).
type ProcessResult struct {
	NeedsForce    bool
	ModelExtentMM float32
	OutputMesh    *MeshData `json:"-"` // sent async via events, not in JSON response
	Duration      time.Duration
}

// PrepareResult summarizes the Prepare phase (kept for CLI backward compat).
type PrepareResult struct {
	NeedsForce    bool    // true if model exceeds size limit and Force was not set
	ModelExtentMM float32 // actual extent when NeedsForce is true
}

// Result summarizes a completed pipeline run (kept for CLI backward compat).
type Result struct {
	OutputPath string
	FaceCount  int
	Duration   time.Duration
	OutputMesh *MeshData
}

// RunCached executes the pipeline using per-stage caching. Only stages whose
// settings changed (or whose dependencies changed) are re-executed.
// The optional cb.OnPalette callback is called with the resolved palette colors
// on every run (including when the palette stage is served from cache),
// allowing callers to update the UI before later stages finish.
func RunCached(ctx context.Context, cache *StageCache, opts Options, cb *Callbacks) (*ProcessResult, error) {
	// Validate inputs before any expensive work.
	switch opts.Dither {
	case "none", "dizzy":
	default:
		return nil, fmt.Errorf("invalid --dither %q: must be none or dizzy", opts.Dither)
	}

	// Extract callbacks, using safe defaults for nil.
	var onInputMesh func(*MeshData, float32, float32)
	var onStickerOverlay func(*MeshData, float32)
	var onPalette func([][3]uint8, []string)
	var onWarning func(string)
	var tracker progress.Tracker = progress.NullTracker{}
	if cb != nil {
		onInputMesh = cb.OnInputMesh
		onStickerOverlay = cb.OnStickerOverlay
		onPalette = cb.OnPalette
		onWarning = cb.OnWarning
		if cb.Progress != nil {
			tracker = cb.Progress
		}
	}

	start := time.Now()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Parse — read the input file into a *LoadedModel.
	if err := runParse(ctx, cache, opts, tracker); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Load — scale, normalize, optionally alpha-wrap, build the
	// preview MeshData. Alpha-wrap (slow) is folded into this stage's body
	// so its result is gob-cached as part of the loadOutput on disk.
	if err := runLoad(ctx, cache, opts, tracker); err != nil {
		return nil, err
	}
	lo := cache.getLoad(opts)

	// Apply base color override on top of the (possibly cached) load output.
	// Cheap and idempotent: runs every invocation so load/decimate/sticker
	// caches don't need to invalidate on base-color changes.
	applyBaseColor(cache, lo, opts)

	// Force check (between load and voxelize). Use the original (unwrapped)
	// mesh — the wrap inflates extents by `offset` on every side.
	if !opts.Force {
		ext := modelMaxExtent(lo.ColorModel)
		if ext > 300 {
			return &ProcessResult{
				NeedsForce:    true,
				ModelExtentMM: ext,
			}, nil
		}
	}

	// Decimate (only depends on geometry + grid params, not stickers)
	// DecimateMesh emits its own "Decimating" stage events (with a progress
	// bar), so don't double-emit here. runDecimate checks the cache and
	// short-circuits on a hit.
	if err := runDecimate(ctx, cache, opts, lo, tracker); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Sticker (builds decals from mesh, before voxelization).
	// runSticker checks the cache internally; on hit it just emits a stage
	// marker for the UI.
	if err := runSticker(ctx, cache, opts, lo, tracker, onWarning); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	so := cache.getSticker(opts)

	// Send input mesh (with sticker overlay) to the preview after stickers
	// are built. Two cases:
	//
	//   alpha-wrap OFF: stickers live on so.Model (a clone of ColorModel
	//     with subdivided faces). Bake decals into that preview so face
	//     indices line up. No separate overlay.
	//
	//   alpha-wrap ON: input mesh is lo.ColorModel (textured original);
	//     decals key into so.Model (the wrap). Emit two meshes via
	//     separate callbacks so the frontend can layer them.
	if onInputMesh != nil && lo.ColorModel != nil {
		previewModel := lo.ColorModel
		var bakedDecals []*voxel.StickerDecal
		if so != nil && so.Model != nil && !so.FromAlphaWrap {
			// Sticker scratch is the textured model — bake decals into the
			// single mesh as before.
			previewModel = so.Model
			bakedDecals = so.Decals
		}
		mesh := buildInputMeshData(previewModel)
		if len(bakedDecals) > 0 {
			mesh = attachStickerOverlay(mesh, bakedDecals)
		}
		mesh = scalePreviewMesh(mesh, lo.PreviewScale)
		onInputMesh(mesh, lo.PreviewScale, lo.ExtentMM)

		// Alpha-wrap case: emit only the sticker-bearing wrap triangles
		// as a separate overlay so the textured input mesh underneath
		// remains visible everywhere there isn't a sticker. Always call
		// the callback (with nil mesh in the no-overlay case) so the
		// frontend can clear any stale overlay from a previous run
		// without racing against the input-mesh event.
		if onStickerOverlay != nil {
			var overlay *MeshData
			if so != nil && so.FromAlphaWrap && len(so.Decals) > 0 {
				overlay = buildStickerOverlayMesh(so.Model, so.Decals)
				if overlay != nil {
					overlay = scalePreviewMesh(overlay, lo.PreviewScale)
				}
			}
			onStickerOverlay(overlay, lo.PreviewScale)
		}
	}

	// Voxelize (uses decals from sticker stage)
	// VoxelizeTwoGrids emits its own "Voxelizing" and "Coloring cells" stages
	// so the two phases appear as distinct steps in the UI instead of
	// overlapping. runVoxelize checks the cache internally.
	if err := runVoxelize(ctx, cache, opts, lo, so, tracker); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	vo := cache.getVoxelize(opts)

	// Color adjustment
	if err := runColorAdjust(ctx, cache, opts, vo, tracker); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	cao := cache.getColorAdjust(opts)

	// Color warp (RBF-based color space warping)
	if err := runColorWarp(ctx, cache, opts, cao, tracker); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	cwo := cache.getColorWarp(opts)

	// Palette + snap colors
	if err := runPalette(ctx, cache, opts, cwo, tracker); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	po := cache.getPalette(opts)

	if onPalette != nil {
		onPalette(po.Palette, po.PaletteLabels)
	}

	// Dither + flood fill
	// runDither emits its own "Dithering" and "Flood fill" stages so the two
	// phases each get their own progress bar.
	if err := runDither(ctx, cache, opts, po, vo, tracker); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	do := cache.getDither(opts)

	// Clip
	// ClipMeshByPatchesTwoGrid emits its own "Clipping" stage with a
	// progress bar fed by worker counters.
	if err := runClip(ctx, cache, opts, do, cache.getDecimate(opts), vo, tracker); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Merge
	// MergeCoplanarTriangles emits its own "Merging" stage. The NoMerge
	// path emits an instant start+done from runMerge.
	if err := runMerge(ctx, cache, opts, tracker); err != nil {
		return nil, err
	}

	mo := cache.getMerge(opts)

	// Build output preview mesh from merge result + palette.
	// Scale vertices to match the preview's coordinate space so both
	// viewers use the same scale.
	// Use ColorModel as the texture source — shell geometry comes from mo;
	// only Textures is read from the source, and the wrapped mesh has none.
	outModel := buildOutputModel(lo.ColorModel, mo)
	outputMesh := buildMeshData(outModel, mo.ShellAssignments, po.Palette)
	if lo.PreviewScale != 1 {
		for i := range outputMesh.Vertices {
			outputMesh.Vertices[i] *= lo.PreviewScale
		}
	}

	if opts.Stats {
		printStats(mo.ShellAssignments, po.Palette)
	}

	return &ProcessResult{
		OutputMesh: outputMesh,
		Duration:   time.Since(start),
	}, nil
}

// Run executes the full pipeline with a fresh cache, then exports the file.
// Convenience wrapper for CLI.
func Run(ctx context.Context, opts Options) (*PrepareResult, *Result, error) {
	// Validate output before doing any work.
	outputExt := strings.ToLower(filepath.Ext(opts.Output))
	if outputExt != ".3mf" {
		return nil, nil, fmt.Errorf("output must be .3mf, got %q", outputExt)
	}

	cache := NewStageCache()
	pr, err := RunCached(ctx, cache, opts, &Callbacks{
		Progress: progress.NewCLITracker(),
	})
	if err != nil {
		return nil, nil, err
	}
	if pr.NeedsForce {
		return &PrepareResult{
			NeedsForce:    true,
			ModelExtentMM: pr.ModelExtentMM,
		}, nil, nil
	}

	faceCount, err := ExportFile(cache, opts, opts.Output, export3mf.Options{
		PrinterID:      opts.Printer,
		NozzleDiameter: opts.NozzleDiameter,
		LayerHeight:    opts.LayerHeight,
		AppVersion:     VersionSemver,
	})
	if err != nil {
		return nil, nil, err
	}

	prepResult := &PrepareResult{}
	result := &Result{
		OutputPath: opts.Output,
		FaceCount:  faceCount,
		Duration:   pr.Duration,
		OutputMesh: pr.OutputMesh,
	}
	return prepResult, result, nil
}

// ExportFile writes a 3MF file using cached pipeline results. The opts must
// be the same Options object used in the most recent successful RunCached
// call so the cache lookups hit.
// Returns the number of faces in the output.
func ExportFile(cache *StageCache, opts Options, outputPath string, exportOpts export3mf.Options) (int, error) {
	lo := cache.getLoad(opts)
	po := cache.getPalette(opts)
	mo := cache.getMerge(opts)
	if lo == nil || po == nil || mo == nil {
		return 0, fmt.Errorf("pipeline has not been run yet")
	}

	outModel := buildOutputModel(lo.ColorModel, mo)

	fmt.Printf("Exporting %s...", outputPath)
	tExport := time.Now()
	if err := export3mf.Export(outModel, mo.ShellAssignments, outputPath, po.Palette, exportOpts); err != nil {
		return 0, fmt.Errorf("exporting 3MF: %w", err)
	}
	fmt.Printf(" done in %.1fs\n", time.Since(tExport).Seconds())

	return len(outModel.Faces), nil
}

// buildOutputModel constructs a LoadedModel from merge output, suitable for
// export or preview mesh building.
func buildOutputModel(srcModel *loader.LoadedModel, mo *mergeOutput) *loader.LoadedModel {
	placeholder := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	placeholder.SetNRGBA(0, 0, color.NRGBA{128, 128, 128, 255})
	var textures []image.Image
	if len(srcModel.Textures) > 0 {
		textures = srcModel.Textures[:1]
	} else {
		textures = []image.Image{placeholder}
	}

	return &loader.LoadedModel{
		Vertices:       mo.ShellVerts,
		Faces:          mo.ShellFaces,
		UVs:            make([][2]float32, len(mo.ShellVerts)),
		Textures:       textures,
		FaceTextureIdx: make([]int32, len(mo.ShellFaces)),
	}
}

// --- Per-stage helpers ---

// runParse parses the input file into a pristine *LoadedModel. Cheap to
// gob-serialize (one mesh, no derived state), so it persists to disk for
// instant restarts.
func runParse(ctx context.Context, cache *StageCache, opts Options, tracker progress.Tracker) error {
	return runStageCached(cache, StageParse, opts, tracker, func() error {
		stage := progress.BeginStage(tracker, stageNames[StageParse], false, 0)
		defer stage.Done()
		fmt.Printf("Parsing %s...", opts.Input)
		t := time.Now()
		loaded, err := loadModel(opts.Input, opts.ObjectIndex)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", filepath.Ext(opts.Input), err)
		}
		fmt.Printf(" %d vertices, %d faces in %.1fs\n",
			len(loaded.Vertices), len(loaded.Faces), time.Since(t).Seconds())
		cache.setParse(opts, loaded)
		return nil
	})
}

// runLoad transforms the parsed model into a usable loadOutput: clone +
// scale + normalize-Z + (optional alpha-wrap) + build preview MeshData.
// Alpha-wrap (the slow part) is folded into this stage's body — it's not
// a separate stage and doesn't get a separate progress marker. The whole
// loadOutput is gob-cached on disk so cross-session toggles of any
// load-affecting setting (Scale, Size, AlphaWrap, …) hit instantly.
func runLoad(ctx context.Context, cache *StageCache, opts Options, tracker progress.Tracker) error {
	return runStageCached(cache, StageLoad, opts, tracker, func() error {
		stage := progress.BeginStage(tracker, stageNames[StageLoad], false, 0)
		defer stage.Done()

		raw := cache.getParse(opts)
		if raw == nil {
			return fmt.Errorf("load: parse output missing (runParse must run first)")
		}

		inputExt := strings.ToLower(filepath.Ext(opts.Input))
		unitScale := unitScaleForExt(inputExt)
		scale := unitScale * opts.Scale

		// Work on a clone so scale/normalize/base-color don't mutate the cached raw.
		model := loader.CloneForEdit(raw)

		// Track the total scale applied so we can convert output mesh
		// vertices back to preview scale (which uses unitScale only).
		totalScale := scale
		if opts.Size != nil {
			ext := modelMaxExtent(model) * scale
			if ext != *opts.Size {
				totalScale = scale * (*opts.Size / ext)
			}
		}
		if totalScale != 1 {
			loader.ScaleModel(model, totalScale)
		}

		// Normalize Z so the model bottom sits at z=0. This ensures the
		// first voxel layer aligns with grid layer 0.
		normalizeZ(model)

		ex := modelExtents(model)
		fmt.Printf("  Extent: %.1f x %.1f x %.1f mm\n", ex[0], ex[1], ex[2])

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Native extent in mm: modelMaxExtent(model) = nativeExtentFile * totalScale,
		// so nativeExtentMM = modelMaxExtent / totalScale * unitScale.
		nativeExtentMM := modelMaxExtent(model) * unitScale / totalScale

		// Alpha-wrap is the slow part of this stage. Folding it inline
		// instead of a separate stage means: one cache key (the StageLoad
		// key cascades through both Parse and Load settings, so a
		// cross-session reload with the same alpha-wrap settings hits the
		// gob-encoded loadOutput on disk and skips the CGAL call entirely).
		geomModel := model
		if opts.AlphaWrap {
			alpha := opts.AlphaWrapAlpha
			if alpha <= 0 {
				alpha = opts.NozzleDiameter
			}
			offset := opts.AlphaWrapOffset
			if offset <= 0 {
				offset = alpha / 30
			}
			fmt.Printf("  Alpha-wrap: alpha=%.3f mm, offset=%.3f mm...", alpha, offset)
			tWrap := time.Now()
			wrapped, err := alphawrap.Wrap(model, alpha, offset)
			if err != nil {
				return fmt.Errorf("alpha-wrap: %w", err)
			}
			fmt.Printf(" %d vertices, %d faces in %.1fs\n",
				len(wrapped.Vertices), len(wrapped.Faces), time.Since(tWrap).Seconds())
			geomModel = wrapped
		}

		// If the geometry mesh grew relative to the original (e.g.
		// alpha-wrap with positive offset), inflate the original along
		// vertex normals so its surface roughly matches. The sample mesh
		// is used only for per-voxel color lookup.
		sampleModel := model
		if geomModel != model {
			origExt := modelMaxExtent(model)
			geomExt := modelMaxExtent(geomModel)
			inflateOffset := (geomExt - origExt) / 2
			if inflateOffset > 1e-4 {
				fmt.Printf("  Inflating color-sample mesh by %.3f mm\n", inflateOffset)
				sampleModel = loader.InflateAlongNormals(model, inflateOffset)
			}
		}

		cache.setLoad(opts, &loadOutput{
			Model:        geomModel,
			ColorModel:   model,
			SampleModel:  sampleModel,
			InputMesh:    buildInputMeshData(model),
			PreviewScale: unitScale / totalScale,
			ExtentMM:     nativeExtentMM,
		})
		return nil
	})
}

// applyBaseColorOverride sets the base color for all untextured faces to the
// given hex color (e.g. "#FF0000"). If no NoTextureMask exists (all faces are
// untextured, as in STL files), all faces are updated.
func applyBaseColorOverride(model *loader.LoadedModel, hexColor string) {
	hex := strings.TrimPrefix(hexColor, "#")
	if len(hex) != 6 {
		log.Printf("Warning: ignoring invalid base color %q (expected 6-digit hex like #FF0000)", hexColor)
		return
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		log.Printf("Warning: ignoring invalid base color %q: %v", hexColor, err)
		return
	}
	rgba := [4]uint8{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}

	for i := range model.FaceBaseColor {
		if model.NoTextureMask == nil || model.NoTextureMask[i] {
			model.FaceBaseColor[i] = rgba
		}
	}
}

// applyBaseColor resets lo.ColorModel / lo.SampleModel FaceBaseColor from the
// pristine raw model and reapplies opts.BaseColor, then rebuilds lo.InputMesh
// so the preview reflects the new colors. Idempotent — a no-op when
// lo.appliedBaseColor already matches opts.BaseColor.
//
// This intentionally violates the cache's "outputs are immutable after set"
// contract for loadOutput: ColorModel.FaceBaseColor and SampleModel.FaceBaseColor
// are mutated in place every run. Safe today because (a) the pipeline runs
// single-threaded under app.pipelineWorker, so no other reader is active when
// this runs; (b) BaseColor is excluded from loadSettings, so multiple cached
// loadOutput entries don't exist for the same load key with different colors;
// and (c) this runs before any disk-encode goroutine for downstream stages.
// The disk-encode goroutine for StageLoad itself (if any) reads its own
// fields, not lo.ColorModel.FaceBaseColor — applyBaseColor mutates the
// FaceBaseColor slice, but the goroutine has already encoded the slice
// header by the time we reach this function in the next run.
// If concurrency is ever introduced into the pipeline worker, factor this
// into a per-run shallow clone of lo.ColorModel/SampleModel.
//
// Invariant: whenever lo is present, parse output is too. runParse always
// runs before runLoad in RunCached's stage sequence.
func applyBaseColor(cache *StageCache, lo *loadOutput, opts Options) {
	if lo.appliedBaseColor == opts.BaseColor {
		return
	}
	raw := cache.getParse(opts)
	if raw == nil {
		panic("applyBaseColor: parse output missing but load cache present")
	}
	// Reset to pristine. CloneForEdit / InflateAlongNormals preserve
	// FaceBaseColor length, so the copy targets always match raw.
	copy(lo.ColorModel.FaceBaseColor, raw.FaceBaseColor)
	if lo.SampleModel != lo.ColorModel {
		copy(lo.SampleModel.FaceBaseColor, raw.FaceBaseColor)
	}
	if opts.BaseColor != "" {
		applyBaseColorOverride(lo.ColorModel, opts.BaseColor)
		if lo.SampleModel != lo.ColorModel {
			applyBaseColorOverride(lo.SampleModel, opts.BaseColor)
		}
	}
	lo.InputMesh = buildInputMeshData(lo.ColorModel)
	lo.appliedBaseColor = opts.BaseColor
}

func runVoxelize(ctx context.Context, cache *StageCache, opts Options, lo *loadOutput, so *stickerOutput, tracker progress.Tracker) error {
	return runStageCached(cache, StageVoxelize, opts, tracker, func() error {
		layer0Size := opts.NozzleDiameter * squarevoxel.Layer0CellScale
		upperSize := opts.NozzleDiameter * squarevoxel.UpperCellScale
		layerH := opts.LayerHeight

		// Two cases for sample-vs-sticker mesh wiring:
		//
		//   alpha-wrap OFF (so.FromAlphaWrap == false): so.Model is a clone of
		//     lo.ColorModel, so it carries UVs/textures AND decals. Use it for
		//     both color and sticker sampling — single nearest-tri lookup per
		//     cell.
		//
		//   alpha-wrap ON: so.Model is a clone of the wrap (no UVs/textures);
		//     decals key into it. lo.SampleModel still carries the original
		//     mesh's UVs/textures. Use lo.SampleModel for color and so.Model
		//     for stickers — two lookups per cell.
		// Invariant: runSticker always populates the sticker stage, so so
		// is never nil here — runSticker runs first and stores at least an
		// empty &stickerOutput{} on the no-stickers path.
		sampleModel := lo.SampleModel
		var stickerModel *loader.LoadedModel
		var stickerSI *voxel.SpatialIndex
		if so.Model != nil {
			if so.FromAlphaWrap {
				stickerModel = so.Model
				stickerSI = so.ensureSI()
			} else {
				sampleModel = so.Model
			}
		}

		fmt.Println("Voxelizing...")
		result, err := squarevoxel.VoxelizeTwoGrids(ctx, lo.Model, sampleModel,
			stickerModel, stickerSI,
			layer0Size, upperSize, layerH, tracker, so.Decals)
		if err != nil {
			return fmt.Errorf("voxelize: %w", err)
		}
		cache.setVoxelize(opts, &voxelizeOutput{
			Cells:         result.Cells,
			CellAssignMap: result.CellAssignMap,
			MinV:          result.MinV,
			Layer0Size:    layer0Size,
			UpperSize:     upperSize,
			LayerH:        layerH,
		})
		return nil
	})
}

func runSticker(ctx context.Context, cache *StageCache, opts Options, lo *loadOutput, tracker progress.Tracker, onWarning func(string)) error {
	return runStageCached(cache, StageSticker, opts, tracker, func() error {
		if len(opts.Stickers) == 0 {
			// No work to do, but emit a single marker so the stage list
			// looks uniform from run to run.
			progress.BeginStage(tracker, stageNames[StageSticker], false, 0).Done()
			cache.setSticker(opts, &stickerOutput{})
			return nil
		}

		// Pick the sticker substrate. With alpha-wrap on, the wrap mesh
		// is the canonical sticker carrier — the original mesh's surface
		// is too dirty (slivers, near-degenerate tris, interior-facing
		// fragments) for LSCM to converge cleanly on real-world meshes.
		// With alpha-wrap off, stickers go on the original mesh as
		// before; the user accepts the quality tradeoff in exchange for
		// skipping the alpha-wrap cost.
		//
		// Either way we deep-clone so the BFS can subdivide in place
		// without mutating the cached lo.Model / lo.ColorModel (which
		// would compound across re-runs) and without aliasing
		// lo.SampleModel (shallow clones share face-indexed slices).
		var sourceModel *loader.LoadedModel
		if opts.AlphaWrap {
			sourceModel = lo.Model
		} else {
			sourceModel = lo.ColorModel
		}
		model := loader.DeepCloneForMutation(sourceModel)
		adj := voxel.BuildTriAdjacency(model)
		si := voxel.NewSpatialIndex(model, 2)

		// One aggregate stage across all stickers. Each sticker gets an
		// equal 1000-unit segment; the builder reports [0,1] of its
		// local progress which we map into that segment.
		const stickerUnits = 1000
		stage := progress.BeginStage(tracker, stageNames[StageSticker], true, len(opts.Stickers)*stickerUnits)
		defer stage.Done()

		var decals []*voxel.StickerDecal
		for i, s := range opts.Stickers {
			// Normalize empty Mode (legacy settings before the field
			// existed) to the current default. Internally Mode must
			// always be a known value.
			if s.Mode == "" {
				s.Mode = "projection"
			}
			base := i * stickerUnits
			onProgress := func(frac float64) {
				if frac < 0 {
					frac = 0
				}
				if frac > 1 {
					frac = 1
				}
				stage.Progress(base + int(frac*float64(stickerUnits)))
			}

			f, err := os.Open(s.ImagePath)
			if err != nil {
				return fmt.Errorf("sticker %s: %w", s.ImagePath, err)
			}
			img, _, err := image.Decode(f)
			f.Close()
			if err != nil {
				return fmt.Errorf("sticker %s: %w", s.ImagePath, err)
			}

			bounds := img.Bounds()
			if bounds.Dx() == 0 || bounds.Dy() == 0 {
				fmt.Printf("  Sticker %s: 0x0 image, skipping\n", s.ImagePath)
				stage.Progress(base + stickerUnits)
				continue
			}

			var decal *voxel.StickerDecal
			switch s.Mode {
			case "unfold":
				seedTri := voxel.FindSeedTriangle(s.Center, model, si)
				if seedTri < 0 {
					fmt.Printf("  Sticker %s: no triangle found near center, skipping\n", s.ImagePath)
					stage.Progress(base + stickerUnits)
					continue
				}
				decal, err = voxel.BuildStickerDecal(ctx, model, adj, img,
					seedTri, s.Center, s.Normal, s.Up, s.Scale, s.Rotation, s.MaxAngle,
					onProgress)
				if err != nil {
					return err
				}
			case "projection":
				decal, err = voxel.BuildStickerDecalProjection(ctx, model, img,
					s.Center, s.Normal, s.Up, s.Scale, s.Rotation, onProgress)
				if err != nil {
					return err
				}
				if len(decal.TriUVs) == 0 {
					fmt.Printf("  Sticker %s: no front-facing geometry within projection rect, skipping\n", s.ImagePath)
					stage.Progress(base + stickerUnits)
					continue
				}
			default:
				return fmt.Errorf("sticker %s: unknown mode %q", s.ImagePath, s.Mode)
			}
			fmt.Printf("  Sticker %s: %d triangles covered\n", s.ImagePath, len(decal.TriUVs))
			if decal.LSCMResidual > 1e-5 && onWarning != nil {
				onWarning(fmt.Sprintf(
					"Sticker %q didn't unfold cleanly (residual %.1e). The mesh in this region has very-poor-quality triangles; the sticker may look distorted. Try alpha-wrap or a different placement.",
					filepath.Base(s.ImagePath), decal.LSCMResidual))
			}
			decals = append(decals, decal)
			stage.Progress(base + stickerUnits)
		}

		so := &stickerOutput{
			Decals:        decals,
			Model:         model,
			FromAlphaWrap: opts.AlphaWrap,
		}
		// si is unexported (and non-gob); seed it from the BFS pass we
		// just ran so downstream stages on this run skip the rebuild.
		// On a disk cache hit, ensureSI rebuilds it.
		so.si = si
		cache.setSticker(opts, so)
		return nil
	})
}

func runColorAdjust(ctx context.Context, cache *StageCache, opts Options, vo *voxelizeOutput, tracker progress.Tracker) error {
	return runStageCached(cache, StageColorAdjust, opts, tracker, func() error {
		stage := progress.BeginStage(tracker, stageNames[StageColorAdjust], false, 0)
		defer stage.Done()

		adj := voxel.ColorAdjustment{
			Brightness: opts.Brightness,
			Contrast:   opts.Contrast,
			Saturation: opts.Saturation,
		}
		tAdj := time.Now()
		cells, err := voxel.AdjustCellColors(ctx, vo.Cells, adj)
		if err != nil {
			return err
		}
		if !adj.IsIdentity() {
			fmt.Printf("  Adjusted colors (B:%+.0f C:%+.0f S:%+.0f) in %.1fs\n",
				opts.Brightness, opts.Contrast, opts.Saturation, time.Since(tAdj).Seconds())
		}

		cache.setColorAdjust(opts, &colorAdjustOutput{Cells: cells})
		return nil
	})
}

func runColorWarp(ctx context.Context, cache *StageCache, opts Options, cao *colorAdjustOutput, tracker progress.Tracker) error {
	return runStageCached(cache, StageColorWarp, opts, tracker, func() error {
		stage := progress.BeginStage(tracker, stageNames[StageColorWarp], false, 0)
		defer stage.Done()

		if len(opts.WarpPins) == 0 {
			// Pass through — copy cells to avoid aliasing cached output.
			out := make([]voxel.ActiveCell, len(cao.Cells))
			copy(out, cao.Cells)
			cache.setColorWarp(opts, &colorWarpOutput{Cells: out})
			return nil
		}

		pins := make([]voxel.ColorWarpPin, len(opts.WarpPins))
		for i, p := range opts.WarpPins {
			src, err := palette.ParsePalette([]string{p.SourceHex})
			if err != nil {
				return fmt.Errorf("warp pin %d source: %w", i, err)
			}
			tgt, err := palette.ParsePalette([]string{p.TargetHex})
			if err != nil {
				return fmt.Errorf("warp pin %d target: %w", i, err)
			}
			pins[i] = voxel.ColorWarpPin{Source: src[0], Target: tgt[0], Sigma: p.Sigma}
		}

		tWarp := time.Now()
		cells, err := voxel.WarpCellColors(ctx, cao.Cells, pins)
		if err != nil {
			return err
		}
		fmt.Printf("  Warped colors (%d pins) in %.1fs\n", len(pins), time.Since(tWarp).Seconds())

		cache.setColorWarp(opts, &colorWarpOutput{Cells: cells})
		return nil
	})
}

func runDecimate(ctx context.Context, cache *StageCache, opts Options, lo *loadOutput, tracker progress.Tracker) error {
	return runStageCached(cache, StageDecimate, opts, tracker, func() error {
		// DecimateMesh emits its own progress events under the
		// "Decimating" name with a determinate bar fed by cell counts;
		// no outer BeginStage here would conflict with that.
		fmt.Println("Decimating...")
		cellSize := opts.NozzleDiameter * squarevoxel.UpperCellScale
		targetCells := squarevoxel.CountSurfaceCells(ctx, lo.Model, opts.NozzleDiameter, opts.LayerHeight)
		decimModel, err := squarevoxel.DecimateMesh(ctx, lo.Model, targetCells, cellSize, opts.NoSimplify, tracker)
		if err != nil {
			return fmt.Errorf("decimate: %w", err)
		}
		cache.setDecimate(opts, &decimateOutput{DecimModel: decimModel})
		return nil
	})
}

func runPalette(ctx context.Context, cache *StageCache, opts Options, cwo *colorWarpOutput, tracker progress.Tracker) error {
	return runStageCached(cache, StagePalette, opts, tracker, func() error {
		stage := progress.BeginStage(tracker, stageNames[StagePalette], false, 0)
		defer stage.Done()

		pcfg, err := buildPaletteConfig(opts)
		if err != nil {
			return err
		}

		if pcfg.NumColors > export3mf.MaxFilaments {
			return fmt.Errorf("palette has %d colors but max supported is %d", pcfg.NumColors, export3mf.MaxFilaments)
		}

		// Copy cells so SnapColors doesn't mutate the color-warp output.
		cells := make([]voxel.ActiveCell, len(cwo.Cells))
		copy(cells, cwo.Cells)

		ditherMode := opts.Dither
		pal, palLabels, palDisplay, err := voxel.ResolvePalette(ctx, cells, pcfg, ditherMode != "none", tracker)
		if err != nil {
			return err
		}
		if palDisplay != "" {
			fmt.Printf("%s\n", palDisplay)
		}
		if len(pal) == 0 {
			return fmt.Errorf("no palette colors")
		}

		if opts.ColorSnap > 0 {
			if err := voxel.SnapColors(ctx, cells, pal, opts.ColorSnap); err != nil {
				return err
			}
			fmt.Printf("  Snapped cell colors toward palette by delta E %.1f\n", opts.ColorSnap)
		}

		// Put the most-used color in slot 0 so 3mf-aware slicers use it for
		// infill (the first filament handles non-color regions). Only reorder
		// when slot 0 is auto-selected: locked colors occupy slots 0..N-1, so
		// slot 0 is unlocked iff there are no locked colors at all.
		if len(pcfg.Locked) == 0 && len(pal) > 1 {
			assigns, err := voxel.AssignColors(ctx, cells, pal)
			if err != nil {
				return err
			}
			counts := make([]int, len(pal))
			for _, a := range assigns {
				counts[a]++
			}
			best := 0
			for i := 1; i < len(counts); i++ {
				if counts[i] > counts[best] {
					best = i
				}
			}
			if best != 0 {
				pal[0], pal[best] = pal[best], pal[0]
				palLabels[0], palLabels[best] = palLabels[best], palLabels[0]
			}
		}

		cache.setPalette(opts, &paletteOutput{
			Palette:       pal,
			PaletteLabels: palLabels,
			Cells:         cells,
		})
		return nil
	})
}

func runDither(ctx context.Context, cache *StageCache, opts Options, po *paletteOutput, vo *voxelizeOutput, tracker progress.Tracker) error {
	return runStageCached(cache, StageDither, opts, tracker, func() error {
		// "Dithering" is the canonical stage marker. Flood fill is folded
		// in as part of the same stage — its progress is reported via
		// Progress events rather than a separate StageStart.
		stage := progress.BeginStage(tracker, stageNames[StageDither], true, 2*len(po.Cells))
		defer stage.Done()

		ditherMode := opts.Dither
		cells := po.Cells
		pal := po.Palette

		tDither := time.Now()
		var assignments []int32
		var err error
		switch ditherMode {
		case "dizzy":
			neighbors := vo.getNeighbors()
			assignments, err = voxel.DitherWithNeighbors(ctx, cells, pal, neighbors, tracker)
		default:
			assignments, err = voxel.AssignColors(ctx, cells, pal)
		}
		if err != nil {
			return err
		}
		fmt.Printf("  Dithered (%s) %d cells in %.1fs\n", ditherMode, len(cells), time.Since(tDither).Seconds())

		// Per-color usage counts.
		counts := make([]int, len(pal))
		for _, a := range assignments {
			counts[a]++
		}
		total := len(assignments)
		order := make([]int, len(pal))
		for i := range order {
			order[i] = i
		}
		sort.Slice(order, func(a, b int) bool { return counts[order[a]] > counts[order[b]] })
		for _, i := range order {
			c := pal[i]
			fmt.Printf("    #%02X%02X%02X: %d cells (%.1f%%)\n", c[0], c[1], c[2], counts[i], 100*float64(counts[i])/float64(total))
		}

		// Flood fill: same stage, additional progress.
		tFlood := time.Now()
		patchMap, numPatches, err := floodFillTwoGrids(ctx, cells, assignments, tracker)
		if err != nil {
			return err
		}
		fmt.Printf("  Flood fill: %d patches in %.1fs\n", numPatches, time.Since(tFlood).Seconds())

		// Build per-patch palette assignment.
		patchAssignment := make([]int32, numPatches)
		for i, c := range cells {
			k := voxel.CellKey{Grid: c.Grid, Col: c.Col, Row: c.Row, Layer: c.Layer}
			pid := patchMap[k]
			patchAssignment[pid] = assignments[i]
		}

		cache.setDither(opts, &ditherOutput{
			Assignments:     assignments,
			PatchMap:        patchMap,
			NumPatches:      numPatches,
			PatchAssignment: patchAssignment,
		})
		return nil
	})
}

// floodFillTwoGrids runs flood fill separately for each grid and merges results.
func floodFillTwoGrids(ctx context.Context, cells []voxel.ActiveCell, assignments []int32, tracker progress.Tracker) (map[voxel.CellKey]int, int, error) {
	// Partition cells by grid.
	var cells0, cells1 []voxel.ActiveCell
	var assign0, assign1 []int32
	idx0 := make([]int, 0, len(cells))
	idx1 := make([]int, 0, len(cells))
	for i, c := range cells {
		if c.Grid == 0 {
			cells0 = append(cells0, c)
			assign0 = append(assign0, assignments[i])
			idx0 = append(idx0, i)
		} else {
			cells1 = append(cells1, c)
			assign1 = append(assign1, assignments[i])
			idx1 = append(idx1, i)
		}
	}

	var counter atomic.Int64
	pm0, n0, err := voxel.FloodFillPatches(ctx, cells0, assign0, tracker, &counter)
	if err != nil {
		return nil, 0, err
	}
	pm1, n1, err := voxel.FloodFillPatches(ctx, cells1, assign1, tracker, &counter)
	if err != nil {
		return nil, 0, err
	}

	// Merge: offset grid-1 patch IDs by n0.
	merged := make(map[voxel.CellKey]int, len(cells))
	for k, v := range pm0 {
		merged[k] = v
	}
	for k, v := range pm1 {
		merged[k] = v + n0
	}
	return merged, n0 + n1, nil
}

func runClip(ctx context.Context, cache *StageCache, opts Options, do *ditherOutput, deco *decimateOutput, vo *voxelizeOutput, tracker progress.Tracker) error {
	return runStageCached(cache, StageClip, opts, tracker, func() error {
		// ClipMeshByPatchesTwoGrid emits its own determinate "Clipping"
		// progress fed by worker counters; no outer BeginStage needed.
		tClip := time.Now()
		cfg := voxel.TwoGridConfig{
			MinV:       vo.MinV,
			Layer0Size: vo.Layer0Size,
			UpperSize:  vo.UpperSize,
			LayerH:     vo.LayerH,
			SeamZ:      vo.MinV[2] + 0.5*vo.LayerH,
		}
		shellVerts, shellFaces, shellAssignments, err := voxel.ClipMeshByPatchesTwoGrid(
			ctx, deco.DecimModel, do.PatchMap, do.PatchAssignment, cfg, tracker)
		if err != nil {
			return fmt.Errorf("clip: %w", err)
		}
		fmt.Printf("  Clipped mesh: %d faces in %.1fs\n", len(shellFaces), time.Since(tClip).Seconds())
		fmt.Printf("  After clip: %s\n", voxel.CheckWatertight(shellFaces))

		cache.setClip(opts, &clipOutput{
			ShellVerts:       shellVerts,
			ShellFaces:       shellFaces,
			ShellAssignments: shellAssignments,
		})
		return nil
	})
}

func runMerge(ctx context.Context, cache *StageCache, opts Options, tracker progress.Tracker) error {
	return runStageCached(cache, StageMerge, opts, tracker, func() error {
		co := cache.getClip(opts)
		shellVerts := co.ShellVerts
		shellFaces := co.ShellFaces
		shellAssignments := co.ShellAssignments

		if !opts.NoMerge {
			// MergeCoplanarTriangles emits its own "Merging" progress.
			tMerge := time.Now()
			before := len(shellFaces)
			var err error
			shellFaces, shellAssignments, err = voxel.MergeCoplanarTriangles(ctx, shellVerts, shellFaces, shellAssignments, tracker)
			if err != nil {
				return fmt.Errorf("merge: %w", err)
			}
			fmt.Printf("  Merged shell: %d -> %d faces in %.1fs\n", before, len(shellFaces), time.Since(tMerge).Seconds())
		} else {
			// NoMerge case: emit a single Merging spinner so the UI shows
			// the step happened (matches what the cache-hit path emits).
			progress.BeginStage(tracker, stageNames[StageMerge], false, 0).Done()
		}
		fmt.Printf("  Output mesh: %s\n", voxel.CheckWatertight(shellFaces))

		cache.setMerge(opts, &mergeOutput{
			ShellVerts:       shellVerts,
			ShellFaces:       shellFaces,
			ShellAssignments: shellAssignments,
		})
		return nil
	})
}

func buildPaletteConfig(opts Options) (voxel.PaletteConfig, error) {
	var pcfg voxel.PaletteConfig
	pcfg.NumColors = opts.NumColors
	if pcfg.NumColors <= 0 {
		pcfg.NumColors = 4
	}

	// Parse locked colors. Labels are managed by the frontend and not
	// included in LockedColors, so locked entries have empty labels here.
	if len(opts.LockedColors) > 0 {
		colors, err := palette.ParsePalette(opts.LockedColors)
		if err != nil {
			return pcfg, err
		}
		pcfg.Locked = make([]palette.InventoryEntry, len(colors))
		for i, c := range colors {
			pcfg.Locked[i] = palette.InventoryEntry{Color: c}
		}
	}
	if len(pcfg.Locked) > pcfg.NumColors {
		return pcfg, fmt.Errorf("locked %d colors but only %d total requested", len(pcfg.Locked), pcfg.NumColors)
	}

	if opts.InventoryFile != "" {
		inv, err := palette.ParseInventoryFile(opts.InventoryFile)
		if err != nil {
			return pcfg, err
		}
		pcfg.Inventory = inv
	} else if len(opts.InventoryColors) > 0 {
		for i, c := range opts.InventoryColors {
			label := ""
			if i < len(opts.InventoryLabels) {
				label = opts.InventoryLabels[i]
			}
			pcfg.Inventory = append(pcfg.Inventory, palette.InventoryEntry{Color: c, Label: label})
		}
	} else {
		// Default: select from built-in color set.
		defaultColors := []string{"cyan", "magenta", "yellow", "black", "white", "red", "green", "blue"}
		for _, name := range defaultColors {
			rgb, _ := palette.ParsePalette([]string{name})
			pcfg.Inventory = append(pcfg.Inventory, palette.InventoryEntry{Color: rgb[0], Label: name})
		}
	}
	return pcfg, nil
}

func modelExtents(model *loader.LoadedModel) [3]float32 {
	minV, maxV := model.Vertices[0], model.Vertices[0]
	for _, v := range model.Vertices[1:] {
		for i := 0; i < 3; i++ {
			if v[i] < minV[i] {
				minV[i] = v[i]
			}
			if v[i] > maxV[i] {
				maxV[i] = v[i]
			}
		}
	}
	return [3]float32{maxV[0] - minV[0], maxV[1] - minV[1], maxV[2] - minV[2]}
}

func modelMaxExtent(model *loader.LoadedModel) float32 {
	ex := modelExtents(model)
	m := ex[0]
	if ex[1] > m {
		m = ex[1]
	}
	if ex[2] > m {
		m = ex[2]
	}
	return m
}

func normalizeZ(model *loader.LoadedModel) {
	if len(model.Vertices) == 0 {
		return
	}
	minZ := model.Vertices[0][2]
	for _, v := range model.Vertices[1:] {
		if v[2] < minZ {
			minZ = v[2]
		}
	}
	if minZ != 0 {
		for i := range model.Vertices {
			model.Vertices[i][2] -= minZ
		}
	}
}

func printStats(assignments []int32, paletteRGB [][3]uint8) {
	fmt.Println("  Face counts per material:")
	for i, p := range paletteRGB {
		hexColor := fmt.Sprintf("#%02X%02X%02X", p[0], p[1], p[2])
		count := 0
		for _, a := range assignments {
			if int(a) == i {
				count++
			}
		}
		fmt.Printf("    [%d] %s: %d faces\n", i, hexColor, count)
	}
}
