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
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rtwfroody/ditherforge/internal/export3mf"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/plog"
	"github.com/rtwfroody/ditherforge/internal/progress"
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
	// BaseColorMaterialX is the path to a .mtlx file or a .zip archive
	// containing one (with adjacent textures) applied to untextured
	// faces as a procedural or image-backed base color. When non-empty
	// it takes precedence over BaseColor. Cache invalidation tracks
	// the file's mtime + size; in-place edits without mtime change
	// won't be picked up.
	BaseColorMaterialX string
	// BaseColorMaterialXTileMM scales positions before sampling the
	// MaterialX graph: a value of 10 means one shading-unit cycle of
	// the procedural maps to 10 mm of object space. Zero or negative
	// is treated as 1 mm (i.e. raw position).
	BaseColorMaterialXTileMM float64
	// BaseColorMaterialXTriplanarSharpness controls the blend
	// weighting for image-backed graphs that get triplanar-projected
	// onto untextured faces. Higher values produce sharper transitions
	// between the three axis-aligned projections (closer to a hard box
	// map); lower values blend more softly. Zero or negative falls
	// back to a sensible default (4). Ignored by purely position-
	// driven graphs (marble, brick).
	BaseColorMaterialXTriplanarSharpness float64
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
	Split           SplitSettings `json:"Split,omitempty"`
}

// SplitSettings controls the optional Split stage that cuts a model
// into two halves with peg/pocket connectors and lays them out
// side-by-side on the bed. The zero value disables the stage; the
// pipeline runs bit-identically to the pre-Split path. See
// docs/SPLIT.md for the architecture.
type SplitSettings struct {
	Enabled         bool
	Axis            int     // 0=X, 1=Y, 2=Z
	Offset          float64 // model-space, along Axis
	ConnectorStyle  string  // "none", "pegs", "dowels"
	ConnectorCount  int     // 0 = auto, 1..3 explicit
	ConnectorDiamMM  float64
	ConnectorDepthMM float64
	ClearanceMM      float64
	// Orientation per half: "original", "seam-up", "seam-down",
	// "seam-left", "seam-right". Empty string is treated as
	// "original".
	Orientation [2]string
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
	// OnInputMesh receives:
	//   mesh         — the preview-format mesh data
	//   previewScale — multiply by this to convert pipeline coords back to preview coords
	//   nativeExtentMM — native max bounding-box extent in mm
	//   bboxMin, bboxMax — original-mesh-coord bbox (in mm, post-scale, post-normalizeZ).
	//                       Used by the Split Settings panel to size the
	//                       offset slider per axis.
	OnInputMesh func(mesh *MeshData, previewScale, nativeExtentMM float32, bboxMin, bboxMax [3]float32)
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
	StageSplit:       "Splitting",
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

// RunCached executes the pipeline using a demand-driven, make-style
// driver: each pipeline output we actually need is requested by name,
// and only its transitive dependencies are loaded if any of them missed
// the cache. On a fully-warm cache, only Load + Sticker (for previews),
// Palette (for color mapping), and Merge (for output mesh) are read
// from disk; everything between Sticker and Merge can stay on disk
// untouched.
func RunCached(ctx context.Context, cache *StageCache, opts Options, cb *Callbacks) (*ProcessResult, error) {
	// Validate inputs before any expensive work.
	switch opts.Dither {
	case "none", "dizzy":
	default:
		return nil, fmt.Errorf("invalid --dither %q: must be none or dizzy", opts.Dither)
	}

	// Extract callbacks, using safe defaults for nil.
	var onInputMesh func(*MeshData, float32, float32, [3]float32, [3]float32)
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

	r := &pipelineRun{
		ctx:       ctx,
		cache:     cache,
		opts:      opts,
		tracker:   tracker,
		onWarning: onWarning,
	}

	// Load — needed for the input preview, extent check, and the
	// downstream output texture source. applyBaseColor runs inside.
	lo, err := r.Load()
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Force check (between load and voxelize). Use the original
	// (unwrapped) mesh — the wrap inflates extents by `offset` on
	// every side.
	if !opts.Force {
		ext := modelMaxExtent(lo.ColorModel)
		if ext > 300 {
			return &ProcessResult{
				NeedsForce:    true,
				ModelExtentMM: ext,
			}, nil
		}
	}

	// Sticker — needed for the input preview overlay.
	so, err := r.Sticker()
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Send input mesh (with sticker overlay) to the preview.
	if onInputMesh != nil && lo.ColorModel != nil {
		previewModel := lo.ColorModel
		var bakedDecals []*voxel.StickerDecal
		if so != nil && so.Model != nil && !so.FromAlphaWrap {
			previewModel = so.Model
			bakedDecals = so.Decals
		}
		mesh := buildInputMeshData(previewModel)
		if len(bakedDecals) > 0 {
			mesh = attachStickerOverlay(mesh, bakedDecals)
		}
		mesh = scalePreviewMesh(mesh, lo.PreviewScale)
		// Compute the original-mesh-coord bbox (in mm, post-scale,
		// post-normalizeZ). Used by the Split UI to size the offset
		// slider per axis.
		var bboxMin, bboxMax [3]float32
		if len(lo.ColorModel.Vertices) > 0 {
			bboxMin = lo.ColorModel.Vertices[0]
			bboxMax = lo.ColorModel.Vertices[0]
			for _, v := range lo.ColorModel.Vertices[1:] {
				for i := 0; i < 3; i++ {
					if v[i] < bboxMin[i] {
						bboxMin[i] = v[i]
					}
					if v[i] > bboxMax[i] {
						bboxMax[i] = v[i]
					}
				}
			}
		}
		onInputMesh(mesh, lo.PreviewScale, lo.ExtentMM, bboxMin, bboxMax)

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

	// Palette — needed for output color mapping; also fires
	// onPalette so the GUI can show the resolved palette while
	// the rest of the pipeline finishes.
	po, err := r.Palette()
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if onPalette != nil {
		onPalette(po.Palette, po.PaletteLabels)
	}

	// Merge — the final output mesh. On a warm cache this is the
	// only intermediate stage we touch; Voxelize/ColorAdjust/Warp/
	// Dither/Clip stay on disk and are never read.
	mo, err := r.Merge()
	if err != nil {
		return nil, err
	}

	// Build output preview mesh from merge result + palette.
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
	// Stage outputs are written to disk asynchronously by runStage, and
	// ExportFile reads them back from disk. After a fresh RunCached the
	// writes may still be in flight (a 1M-face merge encode takes
	// seconds). Block on them so the lookups below see the just-written
	// blobs instead of reporting "pipeline has not been run yet".
	cache.WaitForDiskWrites()

	lo := cache.getLoad(opts)
	po := cache.getPalette(opts)
	mo := cache.getMerge(opts)
	if lo == nil || po == nil || mo == nil {
		return 0, fmt.Errorf("pipeline has not been run yet")
	}

	outModel := buildOutputModel(lo.ColorModel, mo)

	plog.Printf("Exporting %s...", outputPath)
	tExport := time.Now()
	if err := export3mf.Export(outModel, mo.ShellAssignments, outputPath, po.Palette, exportOpts); err != nil {
		return 0, fmt.Errorf("exporting 3MF: %w", err)
	}
	plog.Printf("Exported in %.1fs", time.Since(tExport).Seconds())

	return len(outModel.Faces), nil
}

// buildOutputModel constructs a LoadedModel from merge output, suitable for
// export or preview mesh building.
//
// When the merge output carries a per-face HalfIdx (Split was
// enabled), the result's FaceMeshIdx is populated from it and
// NumMeshes is set to 2. NO CURRENT CONSUMER READS THESE FIELDS —
// the wiring is preparatory for the Phase 7 follow-up in
// internal/export3mf, which will iterate per FaceMeshIdx group to
// emit two `<object>` entries. Until that lands, the export path
// emits a single `<object>` containing both halves with the
// bed-layout gap between them.
func buildOutputModel(srcModel *loader.LoadedModel, mo *mergeOutput) *loader.LoadedModel {
	placeholder := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	placeholder.SetNRGBA(0, 0, color.NRGBA{128, 128, 128, 255})
	var textures []image.Image
	if len(srcModel.Textures) > 0 {
		textures = srcModel.Textures[:1]
	} else {
		textures = []image.Image{placeholder}
	}

	out := &loader.LoadedModel{
		Vertices:       mo.ShellVerts,
		Faces:          mo.ShellFaces,
		UVs:            make([][2]float32, len(mo.ShellVerts)),
		Textures:       textures,
		FaceTextureIdx: make([]int32, len(mo.ShellFaces)),
	}
	if mo.ShellHalfIdx != nil {
		faceMeshIdx := make([]int32, len(mo.ShellHalfIdx))
		for i, h := range mo.ShellHalfIdx {
			faceMeshIdx[i] = int32(h)
		}
		out.FaceMeshIdx = faceMeshIdx
		out.NumMeshes = 2
	}
	return out
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
// pristine parse output and reapplies the active base-color override, then
// rebuilds lo.InputMesh so the preview reflects the new colors. Idempotent —
// a no-op when the applied state already matches opts.
//
// Two override sources are supported, mutually exclusive at apply time:
//   - opts.BaseColorMaterialX (with TileMM): per-face centroid sample of the
//     procedural graph, baked into FaceBaseColor for the preview. The
//     voxelizer also samples per-voxel via the same graph for higher
//     fidelity (see cache.baseColorOverride). MaterialX takes precedence
//     when both are set.
//   - opts.BaseColor: legacy uniform hex override.
//
// This intentionally violates the cache's "outputs are immutable after set"
// contract for loadOutput: ColorModel.FaceBaseColor and SampleModel.FaceBaseColor
// are mutated in place every run. Safe today because (a) the pipeline runs
// single-threaded under app.pipelineWorker, so no other reader is active when
// this runs; (b) the base-color settings are excluded from loadSettings, so
// multiple cached loadOutput entries don't exist for the same load key with
// different colors.
//
// Invariant: whenever lo is present, parse output is reachable via
// cache.getParse — but only when an override was previously applied do we
// actually fetch it (the pristine case skips the parse cache lookup).
func applyBaseColor(cache *StageCache, lo *loadOutput, opts Options, tracker progress.Tracker) {
	if lo.appliedBaseColor == opts.BaseColor &&
		lo.appliedBaseColorMaterialX == opts.BaseColorMaterialX &&
		lo.appliedBaseColorMaterialXTileMM == opts.BaseColorMaterialXTileMM &&
		lo.appliedBaseColorMaterialXTriplanarSharpness == opts.BaseColorMaterialXTriplanarSharpness {
		return
	}
	pristine := lo.appliedBaseColor == "" && lo.appliedBaseColorMaterialX == ""
	if !pristine {
		raw := cache.getParse(opts)
		if raw == nil {
			panic("applyBaseColor: parse output missing but load cache mutated")
		}
		copy(lo.ColorModel.FaceBaseColor, raw.FaceBaseColor)
		if lo.SampleModel != lo.ColorModel {
			copy(lo.SampleModel.FaceBaseColor, raw.FaceBaseColor)
		}
	}
	switch {
	case opts.BaseColorMaterialX != "":
		// Build the override once and reuse it across both models.
		// The expensive parts (XML parse + image decode) are memoized
		// on StageCache so the Voxelize stage's per-voxel sampler
		// reuses the same compiled graph; the warning on parse error
		// is also deduped inside baseColorOverride.
		override, err := cache.baseColorOverride(
			opts.BaseColorMaterialX,
			opts.BaseColorMaterialXTileMM,
			opts.BaseColorMaterialXTriplanarSharpness,
			tracker,
		)
		if err == nil && override != nil {
			bakeMaterialXBaseColor(lo.ColorModel, override)
			if lo.SampleModel != lo.ColorModel {
				bakeMaterialXBaseColor(lo.SampleModel, override)
			}
		}
		_ = err // baseColorOverride already routed the warning through tracker.
	case opts.BaseColor != "":
		applyBaseColorOverride(lo.ColorModel, opts.BaseColor)
		if lo.SampleModel != lo.ColorModel {
			applyBaseColorOverride(lo.SampleModel, opts.BaseColor)
		}
	}
	lo.InputMesh = buildInputMeshData(lo.ColorModel)
	lo.appliedBaseColor = opts.BaseColor
	lo.appliedBaseColorMaterialX = opts.BaseColorMaterialX
	lo.appliedBaseColorMaterialXTileMM = opts.BaseColorMaterialXTileMM
	lo.appliedBaseColorMaterialXTriplanarSharpness = opts.BaseColorMaterialXTriplanarSharpness
}

// bakeMaterialXBaseColor evaluates the procedural at every untextured
// face's centroid (in original-mesh coords) and writes the result into
// model.FaceBaseColor. Mirrors applyBaseColorOverride's NoTextureMask
// gating. Centroid sampling is a preview-fidelity approximation; the
// voxelizer separately samples per-voxel for the actual print colors.
func bakeMaterialXBaseColor(model *loader.LoadedModel, override voxel.BaseColorOverride) {
	for i := range model.FaceBaseColor {
		if model.NoTextureMask != nil && !model.NoTextureMask[i] {
			continue
		}
		f := model.Faces[i]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]
		centroid := [3]float32{
			(v0[0] + v1[0] + v2[0]) / 3,
			(v0[1] + v1[1] + v2[1]) / 3,
			(v0[2] + v1[2] + v2[2]) / 3,
		}
		rgb := override.SampleBaseColor(voxel.BaseColorContext{
			Pos:    centroid,
			Normal: voxel.FaceNormal(i, model),
		})
		model.FaceBaseColor[i] = [4]uint8{rgb[0], rgb[1], rgb[2], 255}
	}
}

// floodFillTwoGrids runs flood fill separately for each (Grid,
// HalfIdx) partition and merges results. Partitioning by HalfIdx is
// load-bearing for the Split path: FloodFillPatches operates on
// CellKey index-arithmetic adjacency, not spatial adjacency, so two
// halves whose CellKey columns happen to be adjacent in index space
// (which can happen when the bed-layout gap is small relative to
// cellSize) would otherwise have patches bridging across the
// bed-layout gap. With this partition,
// patches are guaranteed to live in exactly one (Grid, HalfIdx) pair.
func floodFillTwoGrids(ctx context.Context, cells []voxel.ActiveCell, assignments []int32, tracker progress.Tracker) (map[voxel.CellKey]int, int, error) {
	// Up to 4 partitions: (Grid 0/1) × (HalfIdx 0/1). Empty groups are
	// skipped; the unsplit path produces only HalfIdx=0 entries.
	type partKey struct {
		grid    uint8
		halfIdx uint8
	}
	parts := make(map[partKey]*struct {
		cells   []voxel.ActiveCell
		assigns []int32
	})
	for i, c := range cells {
		k := partKey{grid: c.Grid, halfIdx: c.HalfIdx}
		p, ok := parts[k]
		if !ok {
			p = &struct {
				cells   []voxel.ActiveCell
				assigns []int32
			}{}
			parts[k] = p
		}
		p.cells = append(p.cells, c)
		p.assigns = append(p.assigns, assignments[i])
	}

	var counter atomic.Int64
	merged := make(map[voxel.CellKey]int, len(cells))
	totalPatches := 0
	// Iterate parts in a deterministic order so patch IDs are stable
	// across runs (matters for cache stability on downstream stages).
	order := []partKey{
		{grid: 0, halfIdx: 0},
		{grid: 0, halfIdx: 1},
		{grid: 1, halfIdx: 0},
		{grid: 1, halfIdx: 1},
	}
	for _, k := range order {
		p, ok := parts[k]
		if !ok {
			continue
		}
		pm, n, err := voxel.FloodFillPatches(ctx, p.cells, p.assigns, tracker, &counter)
		if err != nil {
			return nil, 0, err
		}
		for ck, v := range pm {
			merged[ck] = v + totalPatches
		}
		totalPatches += n
	}
	return merged, totalPatches, nil
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
	plog.Println("  Face counts per material:")
	for i, p := range paletteRGB {
		hexColor := fmt.Sprintf("#%02X%02X%02X", p[0], p[1], p[2])
		count := 0
		for _, a := range assignments {
			if int(a) == i {
				count++
			}
		}
		plog.Printf("    [%d] %s: %d faces", i, hexColor, count)
	}
}
