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
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	Input        string
	NumColors    int
	LockedColors []string
	Scale        float32
	Output       string
	BaseColor    string // hex color for untextured faces (e.g. "#FF0000"); empty = use model default
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
	NozzleDiameter                       float32
	LayerHeight                          float32
	// Printer is the printer profile ID (e.g. "snapmaker_u1") used when
	// writing the 3MF project settings. Empty = export3mf.DefaultPrinterID.
	// NozzleDiameter selects the matching nozzle variant within that printer.
	Printer         string
	InventoryFile   string
	InventoryColors [][3]uint8 `json:"InventoryColors,omitempty"`
	InventoryLabels []string   `json:"InventoryLabels,omitempty"` // parallel to InventoryColors
	Brightness      float32
	Contrast        float32
	Saturation      float32
	Dither          string
	// RiemersmaInputBias is the per-cell input-bias maximum used by
	// the Riemersma dither (only consulted when Dither == "riemersma").
	// 0..1; higher = stronger pull toward nearest-input palette,
	// suppressing chroma-balancing oscillation in flat near-palette
	// regions at the cost of more posterization in textured regions.
	// 0 = pure Riemersma (no bias). Pipeline default is
	// voxel.RiemersmaInputBiasDefault.
	RiemersmaInputBias float64
	// BlueNoiseTolerance is the per-cell projection-error tolerance
	// (in 8-bit RGB Euclidean units) for the blue-noise / simplex-
	// adaptive dither (only consulted when Dither == "blue-noise").
	// Smaller = higher K (less wander shows up as visible far-palette
	// picks but more drift); larger = lower K (closer-to-input picks
	// but per-cell drift accumulates). Pipeline default is
	// voxel.BlueNoiseAdaptiveTolDefault.
	BlueNoiseTolerance float64
	NoMerge            bool
	NoSimplify         bool
	Size               *float32
	Force              bool
	ReloadSeq          int64 // bumped to force re-read of the same input file
	Stats              bool
	ColorSnap          float64
	WarpPins           []WarpPin `json:"WarpPins,omitempty"`
	Stickers           []Sticker `json:"Stickers,omitempty"`
	ObjectIndex        int       `json:"ObjectIndex"` // -1 = all objects, >=0 = specific object
	AlphaWrap          bool      // enable CGAL Alpha_wrap_3 post-load mesh cleanup
	AlphaWrapAlpha     float32   // mm; 0 = auto (5 × NozzleDiameter)
	AlphaWrapOffset    float32   // mm; 0 = auto (alpha / 30)
	// Layer0AdhesionXYScale multiplies the layer-0 minimum feature
	// size — the printer-profile InitialLayerLineWidth when a profile
	// is resolved, else the bare nozzle diameter. >1 enlarges first-
	// layer cells for bed adhesion (heavily dithered first layers
	// print as larger plastic blobs that stick). 0 / negative treats
	// as 1.
	Layer0AdhesionXYScale float32
	// UpperLayerXYScale multiplies the upper-layer minimum feature
	// size — the printer-profile LineWidth when a profile is
	// resolved, else the bare nozzle diameter. >1 gives coarser
	// color detail with fewer primitives. 0 / negative treats as 1.
	UpperLayerXYScale float32
	Split             SplitSettings `json:"Split,omitempty"`
	// NoInteriorFaceFootprint disables the interior-horizontal-face
	// footprint augmentation in Voxelize (see
	// cellslicer.InteriorHorizontalFootprints). Default false = feature
	// ON: thin horizontal sheets that fall between two slab planes (e.g.
	// an alpha-wrapped single-surface roof) are projected into the slab
	// footprint so cap detection sees them. Set true to fall back to the
	// pure bounding-plane footprint — an advanced/diagnostic knob, mainly
	// for A/B timing of the augmentation's cost.
	NoInteriorFaceFootprint bool `json:"NoInteriorFaceFootprint,omitempty"`

	// NoCellMerge disables same-color cell merging in the Clip stage (see
	// cellslicer.ClipMeshToMergedCellsManifold). Default false = merging is
	// ON: within each slab, adjacent same-kind cells of the same dithered
	// color are paired (at most two per group) and clipped against one
	// merged prism in a single Manifold intersection instead of one per
	// cell, cutting boolean count and removing internal seams between
	// same-color cells. Because colors come from Dither, it never affects
	// the dithered output — it only changes clip time and triangle count
	// (faster, fewer triangles). Set true to clip every cell individually.
	// Merging is also forced off under ShowSampledColors, which needs
	// per-cell face provenance.
	NoCellMerge bool `json:"NoCellMerge,omitempty"`

	// ShowSampledColors is a diagnostic mode: when true, the output
	// mesh's per-face colors come from each face's originating
	// section's RAW SAMPLED RGB instead of its dithered palette
	// index. Bypasses the palette/dither stage for visualization
	// only. Use to isolate sampling bugs from dither/palette bugs:
	// if an artifact is visible in the sampled-color view, it's in
	// SampleSectionColors (or upstream slicer geometry); if only in
	// the normal view, it's in dither/palette/mesh-emission.
	ShowSampledColors bool `json:"ShowSampledColors,omitempty"`
}

// SplitSettings controls the optional Split stage that cuts a model
// into two halves with peg/pocket connectors and lays them out
// side-by-side on the bed. The zero value disables the stage; the
// pipeline runs bit-identically to the pre-Split path. See
// docs/SPLIT.md for the architecture.
type SplitSettings struct {
	Enabled          bool
	Axis             int     // 0=X, 1=Y, 2=Z
	Offset           float64 // model-space, along Axis
	ConnectorStyle   string  // "none", "pegs", "dowels"
	ConnectorCount   int     // 0 = auto, 1..3 explicit
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
	Center    [3]float64 `json:"Center"`   // world-space placement point
	Normal    [3]float64 `json:"Normal"`   // surface normal at placement
	Up        [3]float64 `json:"Up"`       // camera up vector at placement time
	Scale     float64    `json:"Scale"`    // world-unit width of sticker
	Rotation  float64    `json:"Rotation"` // degrees, around surface normal
	MaxAngle  float64    `json:"MaxAngle"` // max inter-triangle angle (degrees) for flood-fill; 0 = no limit
	Mode      string     `json:"Mode"`     // "projection" (default) or "unfold"
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
	// OnAlphaWrappedMesh fires after the load stage when alpha-wrap
	// is enabled, carrying the wrapped geometry mesh (flat-shaded,
	// no UVs or textures) so the frontend can offer a "show wrapped"
	// toggle in the Input Model panel. Called with mesh=nil when
	// alpha-wrap is off so the frontend can drop any stale wrapped
	// mesh and force the toggle back to the input view.
	OnAlphaWrappedMesh func(*MeshData, float32)
	OnPalette          func([][3]uint8, []string)
	// OnWarning is called for non-fatal user-facing notices (e.g. an
	// LSCM solve that didn't converge cleanly). kind is a stable
	// identifier (see progress package constants) that lets the
	// frontend route specific warnings to inline banners adjacent to
	// the offending input; "" routes to the generic status line.
	OnWarning func(kind, message string)
	Progress  progress.Tracker
}

// stageNames maps StageID to a human-readable name for progress reporting.
var stageNames = map[StageID]string{
	StageParse:       "Parsing",
	StageLoad:        "Loading",
	StageSplit:       "Splitting",
	StageVoxelize:    "Voxelizing",
	StageSticker:     "Applying stickers",
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
	StickerAtlas    string    `json:"StickerAtlas,omitempty"`    // base64 encoded atlas image, empty if no stickers

	// FaceAlpha (one byte per face, 0..255) carries per-face alpha for
	// the input preview so it can render translucent faces faithfully
	// instead of collapsing them to a binary visibility decision.
	// Combines material alpha, base-color alpha, and (for vertex-
	// colored faces) the face's average vertex alpha. Texture alpha is
	// NOT folded in — the texture path handles that per-pixel via
	// alpha-blended materials. Nil when every face is fully opaque so
	// opaque models carry no extra payload.
	FaceAlpha []uint8 `json:"FaceAlpha,omitempty"`

	// FaceTranslucent (one byte per face, 1 = needs alpha-blend
	// pipeline, 0 = render opaque) tells the renderer which faces
	// have any non-opaque contribution at all (per-face alpha OR a
	// translucent texture sample). Computed from voxel.FaceAlpha so
	// it matches the output model's translucency criterion. The
	// renderer needs this to put opaque and translucent faces in
	// separate draw calls — keeping the opaque ones writing depth
	// avoids the back-of-mesh depth-sort artifacts that show up when
	// everything is drawn through the alpha-blend pipeline. Nil when
	// every face is fully opaque.
	FaceTranslucent []uint8 `json:"FaceTranslucent,omitempty"`
	// BaseColorAtlas carries a packed image of per-triangle MaterialX
	// patches plus per-face-vertex UVs into the atlas. When non-nil
	// the frontend renders the input mesh with this atlas mapped via
	// the per-face-vertex UVs, replacing the flat per-face colors.
	// Nil when no MaterialX override is active.
	BaseColorAtlas *BaseColorAtlas `json:"BaseColorAtlas,omitempty"`
}

// BaseColorAtlas is a packed atlas of per-triangle MaterialX bake
// patches. Each triangle gets a rectangular patch sized to its bbox
// in a frame aligned with its longest edge: width tier from the
// bbox's extent along that edge, height tier from the third
// vertex's perpendicular distance from it. Long thin triangles thus
// get wide-but-short patches; equilateral ones get square patches.
// Patch sizes come from a small set of tiers (powers of 2);
// (Wt, Ht) buckets are grid-packed and stacked vertically into one
// atlas image.
//
// Per-face-vertex UVs (FaceVertexUVs) put each vertex at the texel
// center it was baked at, so GPU sampling at any in-triangle UV
// reads the correct pre-baked value. Vertex order follows the
// original face's vertices (the bake's internal A/B/C reorder
// chosen by longest-edge selection is undone before writing).
type BaseColorAtlas struct {
	// Image is a base64-encoded PNG of the atlas (with a "png:" or
	// "jpeg:" prefix matching the existing convention used by
	// MeshData.Textures and StickerAtlas).
	Image string `json:"Image"`
	// Width / Height are the atlas dimensions in pixels.
	Width  int32 `json:"Width"`
	Height int32 `json:"Height"`
	// FaceVertexUVs holds nFaces*6 floats — [u, v] per face-vertex
	// (3 vertices × 2 coords). Non-indexed: each face has its own 3
	// UVs even when faces share vertices in the geometry, since the
	// atlas patch is per-triangle. Values are normalized to [0, 1].
	FaceVertexUVs []float32 `json:"FaceVertexUVs"`
}

// ProcessResult summarizes a completed pipeline run (stages 0–6, no file export).
type ProcessResult struct {
	NeedsForce    bool
	ModelExtentMM float32
	OutputMesh    *MeshData `json:"-"` // sent async via events, not in JSON response
	// WrappedMesh is the alpha-wrap output (post-decimate) in the same
	// coord system as OutputMesh. Populated only when opts.AlphaWrap is
	// true; nil otherwise. Used by tests that want to compare the
	// sampled mesh against the *actual* input to the cellslicer (the
	// wrap) rather than the original model — wrap-induced divergence
	// (e.g. sealing open windows with surfaces at unexpected depths)
	// would otherwise pollute cellslicer-correctness metrics.
	WrappedMesh *MeshData `json:"-"`
	Duration    time.Duration
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
	case "none", "dizzy-corrected", "dizzy-2hop", "dizzy-recover", "floyd-steinberg", "riemersma", "riemersma-pair", "blue-noise":
	default:
		return nil, fmt.Errorf("invalid --dither %q: must be none, dizzy-corrected, dizzy-2hop, dizzy-recover, floyd-steinberg, riemersma, riemersma-pair, or blue-noise", opts.Dither)
	}

	// Extract callbacks, using safe defaults for nil.
	var onInputMesh func(*MeshData, float32, float32, [3]float32, [3]float32)
	var onStickerOverlay func(*MeshData, float32)
	var onAlphaWrappedMesh func(*MeshData, float32)
	var onPalette func([][3]uint8, []string)
	var onWarning func(kind, message string)
	var tracker progress.Tracker = progress.NullTracker{}
	if cb != nil {
		onInputMesh = cb.OnInputMesh
		onStickerOverlay = cb.OnStickerOverlay
		onAlphaWrappedMesh = cb.OnAlphaWrappedMesh
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
		mesh.BaseColorAtlas = lo.BaseColorAtlas
		if len(bakedDecals) > 0 {
			mesh = attachStickerOverlay(mesh, bakedDecals)
		}
		mesh = ScalePreviewMesh(mesh, lo.PreviewScale)
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

		if onAlphaWrappedMesh != nil {
			var wrapped *MeshData
			if opts.AlphaWrap && lo.Model != nil && lo.Model != lo.ColorModel {
				wrapped = buildWrappedMeshData(lo.Model)
				wrapped = ScalePreviewMesh(wrapped, lo.PreviewScale)
			}
			onAlphaWrappedMesh(wrapped, lo.PreviewScale)
		}

		if onStickerOverlay != nil {
			var overlay *MeshData
			if so != nil && so.FromAlphaWrap && len(so.Decals) > 0 {
				overlay = buildStickerOverlayMesh(so.Model, so.Decals)
				if overlay != nil {
					overlay = ScalePreviewMesh(overlay, lo.PreviewScale)
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
	//
	// ShowSampledColors debug bypass: skip Merge (which would erase
	// per-face section provenance via coplanar coalescing) and
	// synthesize the mergeOutput from the unmerged Clip output. The
	// post-processing below uses mo.ShellSectionIdx + the
	// voxelizeOutput's per-section sampled colors to recolor faces.
	var mo *mergeOutput
	if opts.ShowSampledColors {
		co, err := r.Clip()
		if err != nil {
			return nil, err
		}
		mo = &mergeOutput{
			ShellVerts:       co.ShellVerts,
			ShellFaces:       co.ShellFaces,
			ShellAssignments: co.ShellAssignments,
			ShellSectionIdx:  co.ShellSectionIdx,
			ShellHalfIdx:     co.ShellHalfIdx,
		}
	} else {
		mo, err = r.Merge()
		if err != nil {
			return nil, err
		}
	}

	// Build output preview mesh from merge result + palette.
	outModel := buildOutputModel(lo.ColorModel, mo)
	outputMesh := buildMeshData(outModel, mo.ShellAssignments, po.Palette)
	if opts.ShowSampledColors && mo.ShellSectionIdx != nil {
		vo, err := r.Voxelize()
		if err != nil {
			return nil, err
		}
		cellColors := make([][3]uint8, len(vo.CellSamples))
		for i, s := range vo.CellSamples {
			cellColors[i] = s.Color
		}
		overrideFaceColorsFromSamples(outputMesh, mo.ShellSectionIdx, cellColors)
	}
	if opts.Stats {
		printStats(mo.ShellAssignments, po.Palette)
	}

	var wrappedMesh *MeshData
	if opts.AlphaWrap && lo.Model != nil && lo.Model != lo.ColorModel {
		wrappedMesh = buildWrappedMeshData(lo.Model)
	}

	return &ProcessResult{
		OutputMesh:  outputMesh,
		WrappedMesh: wrappedMesh,
		Duration:    time.Since(start),
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
// NumMeshes is set to 2. export3mf.splitModelByMesh reads these to
// emit one `<object>` entry per half (each with its own vertex
// table), so the two laid-out halves export as sibling parts the
// slicer treats as independent printable objects.
// overrideFaceColorsFromSamples rewrites outputMesh.FaceColors so
// each face's RGB is its originating section's raw sampled color
// (looked up by faceSection[fi] in sectionColors). Faces with
// faceSection[fi] < 0 (interior fill triangles that don't trace back
// to a sampled cell) are left at their dithered-palette color so the
// unsampled fallback geometry still has *some* color. Used only when
// opts.ShowSampledColors is set.
func overrideFaceColorsFromSamples(mesh *MeshData, faceSection []int32, sectionColors [][3]uint8) {
	if mesh == nil || len(mesh.FaceColors) == 0 {
		return
	}
	nFaces := len(mesh.FaceColors) / 3
	if len(faceSection) != nFaces {
		return
	}
	for fi := 0; fi < nFaces; fi++ {
		sid := faceSection[fi]
		if sid < 0 || int(sid) >= len(sectionColors) {
			continue
		}
		c := sectionColors[sid]
		mesh.FaceColors[3*fi+0] = uint16(c[0])
		mesh.FaceColors[3*fi+1] = uint16(c[1])
		mesh.FaceColors[3*fi+2] = uint16(c[2])
	}
}

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

// voxelCells captures the XY widths and Z heights that drive the
// voxel grid and downstream decimation budgets. Returned by
// voxelCellSizes. Each grid has its own XY width (Layer0XY /
// UpperXY) and Z height (Layer0Z / UpperZ); for printers without a
// separate first-layer height the two Z values are equal.
type voxelCells struct {
	Layer0XY float32
	UpperXY  float32
	Layer0Z  float32
	UpperZ   float32
}

// offGridWarnKey dedups the "no exact process for this layer height"
// warning across the many cache-key derivations stageFnv runs.
type offGridWarnKey struct {
	printerID   string
	nozzle      float32
	layerHeight float32
}

var offGridWarned sync.Map

// voxelCellSizes resolves the voxel grid dimensions for a pipeline
// run. Reads them from the matched OrcaSlicer process profile
// (printer × nozzle × layer-height) so the voxel grid lines up with
// what the slicer will actually extrude:
//
//   - Layer0XY = process.InitialLayerLineWidth (e.g. 0.5mm for a
//     0.4 nozzle on Snapmaker / Prusa / Bambu profiles).
//   - UpperXY  = process.LineWidth             (e.g. 0.42mm on most;
//     0.45mm on Prusa XL).
//   - Layer0Z  = process.InitialLayerPrintHeight (e.g. 0.25mm on
//     Snapmaker U1 with a 0.20mm nominal layer; 0.20mm everywhere
//     else).
//   - UpperZ   = opts.LayerHeight (also matches process.LayerHeight
//     when the user picks one of the dropdown values).
//
// Falls back to the legacy nozzle×constant approximations from
// squarevoxel (Layer0CellScale / UpperCellScale) when the printer ID
// is empty, the registry doesn't recognize it, the nozzle isn't
// listed, the process entry is missing the field, or the requested
// LayerHeight isn't one of the registry's process slots (within
// 0.001mm). That last guard matters: ClosestProcess always returns
// *something* for a non-empty list, so without an exactness check
// requesting LayerHeight=0.15 on a printer with only 0.20/0.30 slots
// would silently pick 0.20's line_width. Falling back to
// nozzle×constant in that case is more honest than a stale slot's
// settings, and a plog warning makes the divergence diagnosable.
// In fallback mode Layer0Z = UpperZ = opts.LayerHeight so the grid
// stays uniform — there's no slicer setting available to derive a
// taller first-layer height from.
func voxelCellSizes(opts Options) voxelCells {
	// Resolve the unscaled minimum-feature-size base from the printer
	// profile (or nozzle fallback), then multiply by the user-facing
	// XY scale knobs. Two-step split so future early returns in the
	// base resolver can't accidentally bypass the scales.
	cells := voxelCellSizesBase(opts)
	layer0Scale := opts.Layer0AdhesionXYScale
	if layer0Scale <= 0 {
		layer0Scale = 1
	}
	upperScale := opts.UpperLayerXYScale
	if upperScale <= 0 {
		upperScale = 1
	}
	cells.Layer0XY *= layer0Scale
	cells.UpperXY *= upperScale
	return cells
}

// voxelCellSizesBase resolves the unscaled minimum-feature sizes:
// printer-profile InitialLayerLineWidth / LineWidth when a profile
// matches, else the bare nozzle diameter. Caller (voxelCellSizes)
// applies the XY scale multipliers on top.
func voxelCellSizesBase(opts Options) (cells voxelCells) {
	cells = voxelCells{
		Layer0XY: opts.NozzleDiameter,
		UpperXY:  opts.NozzleDiameter,
		Layer0Z:  opts.LayerHeight,
		UpperZ:   opts.LayerHeight,
	}
	// Match the export side's printer-default behavior (export3mf.go
	// substitutes DefaultPrinterID for an empty PrinterID), so the
	// voxel grid agrees with what the emitted 3MF claims to be.
	// Without this, a CLI run without --printer voxelizes against
	// nozzle×constant but exports as snapmaker_u1.
	printerID := opts.Printer
	if printerID == "" {
		printerID = export3mf.DefaultPrinterID
	}
	p := export3mf.FindPrinter(printerID)
	if p == nil {
		return cells
	}
	n := p.FindNozzleByDiameter(opts.NozzleDiameter)
	if n == nil {
		return cells
	}
	proc := n.ClosestProcess(opts.LayerHeight)
	if proc == nil {
		return cells
	}
	const layerHeightEpsilon = 0.001
	if math.Abs(float64(proc.LayerHeight-opts.LayerHeight)) > layerHeightEpsilon {
		// stageFnv calls voxelCellSizes once per stage cache-key
		// derivation, which can fire many times per pipeline run.
		// Dedup the warning per (printer, nozzle, layer height) tuple
		// so a single off-grid layer height doesn't spam the log.
		warnKey := offGridWarnKey{
			printerID:   printerID,
			nozzle:      opts.NozzleDiameter,
			layerHeight: opts.LayerHeight,
		}
		if _, alreadyWarned := offGridWarned.LoadOrStore(warnKey, struct{}{}); !alreadyWarned {
			plog.Printf("voxelCellSizes: %s nozzle %.2f has no exact process for layer height %.3f mm "+
				"(closest is %.3f mm); falling back to nozzle×scale voxel sizes",
				printerID, opts.NozzleDiameter, opts.LayerHeight, proc.LayerHeight)
		}
		return cells
	}
	if proc.InitialLayerLineWidth > 0 {
		cells.Layer0XY = proc.InitialLayerLineWidth
	}
	if proc.LineWidth > 0 {
		cells.UpperXY = proc.LineWidth
	}
	if proc.InitialLayerPrintHeight > 0 {
		cells.Layer0Z = proc.InitialLayerPrintHeight
	}
	// UpperZ stays at opts.LayerHeight — that's the user-facing
	// "layer height" knob, identical to proc.LayerHeight after the
	// 0.001mm exactness check above.
	return cells
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

// applyBaseColor resets lo.ColorModel.FaceBaseColor from the pristine parse
// output and reapplies the active base-color override, then rebuilds
// lo.InputMesh so the preview reflects the new colors. Idempotent — a no-op
// when the applied state already matches opts.
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
// contract for loadOutput: ColorModel.FaceBaseColor is mutated in place every
// run. Safe today because (a) the pipeline runs single-threaded under
// app.pipelineWorker, so no other reader is active when this runs; (b) the
// base-color settings are excluded from loadSettings, so multiple cached
// loadOutput entries don't exist for the same load key with different colors.
//
// Invariant: whenever lo is present, parse output is reachable via
// cache.getParse — but only when an override was previously applied do we
// actually fetch it (the pristine case skips the parse cache lookup).
func applyBaseColor(cache *StageCache, lo *loadOutput, opts Options, tracker progress.Tracker) {
	tupleMatches := lo.appliedBaseColor == opts.BaseColor &&
		lo.appliedBaseColorMaterialX == opts.BaseColorMaterialX &&
		lo.appliedBaseColorMaterialXTileMM == opts.BaseColorMaterialXTileMM &&
		lo.appliedBaseColorMaterialXTriplanarSharpness == opts.BaseColorMaterialXTriplanarSharpness
	// Also invalidate when the atlas bake is missing but should be
	// present — e.g. a disk-cached loadOutput from a prior version
	// that pre-dates this field. Without this, the tuple matches and
	// we'd silently keep serving a nil atlas.
	bakeMissing := opts.BaseColorMaterialX != "" && lo.BaseColorAtlas == nil
	if tupleMatches && !bakeMissing {
		return
	}
	pristine := lo.appliedBaseColor == "" && lo.appliedBaseColorMaterialX == ""
	if !pristine {
		raw := cache.getParse(opts)
		if raw == nil {
			panic("applyBaseColor: parse output missing but load cache mutated")
		}
		copy(lo.ColorModel.FaceBaseColor, raw.FaceBaseColor)
	}
	lo.BaseColorAtlas = nil
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
			// Per-triangle atlas bake for the input preview. Each
			// face's patch dims (W × H) come from a 2D tier
			// classification (longest-edge bbox extent × third-
			// vertex perpendicular distance), so the total sample
			// count varies with mesh shape. Pre-classify once for
			// an accurate progress total — the work is negligible
			// (< 50 ms even for 226k faces) compared to the bake.
			totalSamples := 0
			for fi := range lo.ColorModel.Faces {
				lay := computeFaceLayout(lo.ColorModel, fi)
				totalSamples += baseColorAtlasTierSizes[lay.WT] * baseColorAtlasTierSizes[lay.HT]
			}
			stage := progress.BeginStage(tracker, "Baking MaterialX preview", true, totalSamples)
			atlas, atlasErr := bakeMaterialXAtlas(lo.ColorModel, override, stage.Progress)
			stage.Done()
			if atlasErr != nil {
				tracker.Warn(progress.WarnKindMaterialXBaseColor, fmt.Sprintf("MaterialX preview atlas: %v", atlasErr))
			} else {
				lo.BaseColorAtlas = atlas
			}
		}
		_ = err // baseColorOverride already routed the warning through tracker.
	case opts.BaseColor != "":
		applyBaseColorOverride(lo.ColorModel, opts.BaseColor)
	}
	lo.InputMesh = buildInputMeshData(lo.ColorModel)
	lo.InputMesh.BaseColorAtlas = lo.BaseColorAtlas
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
