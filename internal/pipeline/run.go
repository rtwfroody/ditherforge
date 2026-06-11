package pipeline

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rtwfroody/ditherforge/internal/alphawrap"
	"github.com/rtwfroody/ditherforge/internal/cellslicer"
	"github.com/rtwfroody/ditherforge/internal/export3mf"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/plog"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/split"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// debugHoles is read once at init from DITHERFORGE_HOLE_REPORT —
// matches the cellslicer package's identically-named flag. Used to
// gate the reportHolesIfEnabled call below.
var debugHoles = os.Getenv("DITHERFORGE_HOLE_REPORT") != ""

// debugOverlap is read once at init from DITHERFORGE_OVERLAP_REPORT.
// When set, the per-slab phase checks each slab's cells for overlapping
// polygons (a partition bug) and logs any it finds. No-op when unset.
var debugOverlap = os.Getenv("DITHERFORGE_OVERLAP_REPORT") != ""

// reportOverlapsIfEnabled, gated on DITHERFORGE_OVERLAP_REPORT, scans
// every slab for cells whose Outer polygons overlap (cells are supposed
// to tile the footprint sharing only edges). It logs a per-slab summary
// plus the worst offending pairs, so a layer with overlapping ring/hex
// cells shows up by slab index. minAreaMM2 is scaled per-slab from that
// slab's cellSize so shared-edge rounding noise is ignored.
func reportOverlapsIfEnabled(slabs []cellslicer.Slab, cellSizeForSlab func(int) float32) {
	if !debugOverlap {
		return
	}
	totalPairs, slabsWithOverlap := 0, 0
	for i := range slabs {
		cs := cellSizeForSlab(i)
		// Tolerance: 5% of a nominal cell area. Real overlaps (a skinny
		// ring cell laid over a neighbour) are a large fraction of a
		// cell; integer-grid rounding slivers along a shared edge are
		// far below this.
		minArea := 0.05 * cs * cs
		ov := cellslicer.DetectCellOverlaps(slabs[i].Cells, minArea)
		if len(ov) == 0 {
			continue
		}
		slabsWithOverlap++
		totalPairs += len(ov)
		var maxArea float32
		for _, o := range ov {
			if o.AreaMM2 > maxArea {
				maxArea = o.AreaMM2
			}
		}
		plog.Printf("  [overlap-report] slab %d (Z %.3f–%.3f, %d cells): %d overlapping pairs, worst %.4f mm² (tol %.4f)",
			slabs[i].Index, slabs[i].ZBot, slabs[i].ZTop, len(slabs[i].Cells), len(ov), maxArea, minArea)
		// Show up to the first few pairs for detail.
		const showN = 5
		for k, o := range ov {
			if k >= showN {
				plog.Printf("      … and %d more pairs", len(ov)-showN)
				break
			}
			plog.Printf("      cells %d(%s) ∩ %d(%s) = %.4f mm²",
				o.I, kindName(o.KindI), o.J, kindName(o.KindJ), o.AreaMM2)
		}
	}
	if totalPairs == 0 {
		plog.Printf("  [overlap-report] no overlapping cells in any of %d slabs", len(slabs))
	} else {
		plog.Printf("  [overlap-report] TOTAL: %d overlapping pairs across %d/%d slabs",
			totalPairs, slabsWithOverlap, len(slabs))
	}
}

func kindName(k cellslicer.CellKind) string {
	switch k {
	case cellslicer.KindRing:
		return "ring"
	case cellslicer.KindHex:
		return "hex"
	default:
		return "?"
	}
}

// reportHolesIfEnabled, gated on DITHERFORGE_HOLE_REPORT=1, runs
// voxel.CheckWatertight on a stage-output mesh and logs its boundary /
// non-manifold counts. Used to bisect at which pipeline stage holes
// appear that aren't present in the alpha-wrap input. No-op when the
// env var is unset, so a normal run pays nothing.
//
// Vertex indices must be properly shared across faces — for meshes
// emitted with per-fragment duplicate vertices (e.g. inside the
// cellslicer before cross-piece dedup), use a position-keyed counter
// instead.
func reportHolesIfEnabled(stage string, faces [][3]uint32) {
	if !debugHoles {
		return
	}
	wr := voxel.CheckWatertight(faces)
	plog.Printf("  [hole-report] %s: %d faces, %s", stage, len(faces), wr)
}

// pipelineRun is a demand-driven driver for one pipeline invocation.
// The stage graph itself — names, dependencies, cache policy, bodies —
// is declared as data in the stageDefs table (stagedef.go); resolve
// walks it. Each typed accessor below (Parse, Load, …, Merge) is a
// thin facade over resolve(StageX), which:
//
//  1. Returns memoized output if this Run has already computed it.
//  2. Otherwise asks the cache. If the cache hits (disk),
//     runStageCached emits a UI marker and the body never runs.
//  3. On a cache miss, resolves the stage's declared Deps, then runs
//     its body.
//
// A "make"-like dependency graph: top-level callers ask for the
// outputs they need (typically Load/Sticker for previews, Merge/
// Palette for export). Intermediate stages (Voxelize, ColorAdjust,
// Dither, Clip, …) are loaded only when something downstream of them
// can't be served from cache.
type pipelineRun struct {
	ctx       context.Context
	cache     *StageCache
	opts      Options
	tracker   progress.Tracker
	onWarning func(kind, message string)

	// memo holds per-Run resolved stage outputs: once a stage has been
	// resolved, subsequent consumers within the same Run skip the
	// cache lookup. Lazily allocated by resolve.
	memo map[StageID]any
}

// Typed stage accessors — the public face of the stageDefs table.

func (r *pipelineRun) Parse() (*loader.LoadedModel, error) {
	return resolveTyped[loader.LoadedModel](r, StageParse)
}

func (r *pipelineRun) Load() (*loadOutput, error) {
	return resolveTyped[loadOutput](r, StageLoad)
}

func (r *pipelineRun) Split() (*splitOutput, error) {
	return resolveTyped[splitOutput](r, StageSplit)
}

func (r *pipelineRun) Sticker() (*stickerOutput, error) {
	return resolveTyped[stickerOutput](r, StageSticker)
}

func (r *pipelineRun) Voxelize() (*voxelizeOutput, error) {
	return resolveTyped[voxelizeOutput](r, StageVoxelize)
}

func (r *pipelineRun) ColorAdjust() (*colorAdjustOutput, error) {
	return resolveTyped[colorAdjustOutput](r, StageColorAdjust)
}

func (r *pipelineRun) ColorWarp() (*colorWarpOutput, error) {
	return resolveTyped[colorWarpOutput](r, StageColorWarp)
}

func (r *pipelineRun) Palette() (*paletteOutput, error) {
	return resolveTyped[paletteOutput](r, StagePalette)
}

func (r *pipelineRun) Dither() (*ditherOutput, error) {
	return resolveTyped[ditherOutput](r, StageDither)
}

func (r *pipelineRun) Clip() (*clipOutput, error) {
	return resolveTyped[clipOutput](r, StageClip)
}

func (r *pipelineRun) Merge() (*mergeOutput, error) {
	return resolveTyped[mergeOutput](r, StageMerge)
}

func (r *pipelineRun) checkCancel() error {
	if r.ctx.Err() != nil {
		return r.ctx.Err()
	}
	return nil
}

// ----- Stage methods -----

// decimateErrorBudget translates a voxel cell size into the QEM cost
// ceiling we hand to DecimateMesh: the squared half-cell. QEM cost
// tracks the squared distance the merged vertex moves from every
// tangent plane it represents (sums quadrics across collapses), so
// capping it at (cellSize/2)² keeps any single vertex from drifting
// more than ~½ a voxel from the original surface. Below voxelization's
// resolving power -- safe to compress everywhere in the pipeline that
// uses voxel cell sizing.
func decimateErrorBudget(cellSize float32) float64 {
	half := float64(cellSize) / 2
	return half * half
}

// runParse is StageParse's body (see stageDefs).
func (r *pipelineRun) runParse() (any, error) {
	stage := progress.BeginStage(r.tracker, stageNames[StageParse], false, 0)
	defer stage.Done()
	plog.Printf("Parsing %s...", r.opts.Input)
	t := time.Now()
	loaded, err := loadModel(r.opts.Input, r.opts.ObjectIndex)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filepath.Ext(r.opts.Input), err)
	}
	plog.Printf("  Parsed: %d vertices, %d faces in %.1fs",
		len(loaded.Vertices), len(loaded.Faces), time.Since(t).Seconds())
	return loaded, nil
}

// afterLoad is StageLoad's After hook: it reapplies the base-color
// override on top of the (possibly cached) load output on EVERY
// resolve. Cheap and idempotent. On a fresh disk hit
// (lo.appliedBaseColor=="") this skips the parse cache lookup.
func afterLoad(r *pipelineRun, out any) error {
	applyBaseColor(r.ctx, r.cache, out.(*loadOutput), r.opts, r.tracker)
	return nil
}

// runLoad is StageLoad's body (see stageDefs).
func (r *pipelineRun) runLoad() (any, error) {
	raw, err := r.Parse()
	if err != nil {
		return nil, err
	}
	label := stageNames[StageLoad]
	if r.opts.AlphaWrap {
		label += " (including alpha-wrap)"
	}
	stage := progress.BeginStage(r.tracker, label, false, 0)
	defer stage.Done()

	inputExt := strings.ToLower(filepath.Ext(r.opts.Input))
	unitScale := unitScaleForExt(inputExt)
	scale := unitScale * r.opts.Scale

	model := loader.CloneForEdit(raw)
	totalScale := scale
	if r.opts.Size != nil {
		ext := modelMaxExtent(model) * scale
		if ext != *r.opts.Size {
			totalScale = scale * (*r.opts.Size / ext)
		}
	}
	if totalScale != 1 {
		loader.ScaleModel(model, totalScale)
	}
	normalizeZ(model)

	ex := modelExtents(model)
	plog.Printf("  Extent: %.1f x %.1f x %.1f mm", ex[0], ex[1], ex[2])

	if err := r.checkCancel(); err != nil {
		return nil, err
	}
	nativeExtentMM := modelMaxExtent(model) * unitScale / totalScale

	// Load-time decimation: prune geometry to voxel resolution on every
	// load, alpha-wrap or not. errorBudget bounds geometric drift to ~½ a
	// voxel cell -- finer detail than that won't survive voxelization
	// downstream, so it's safe to discard here. Only the geometry `model`
	// is decimated; the pristine mesh stays intact for ColorModel below
	// (UVs, textures, and per-face colors feed color sampling at full
	// resolution). When alpha-wrap is enabled this decimated mesh is also
	// the wrap input, so the wrapper rebuilds from an already-pruned
	// surface.
	geomModel := model
	if !r.opts.NoSimplify {
		cellSize := voxelCellSizes(r.opts).UpperXY
		budget := decimateErrorBudget(cellSize)
		dec, derr := voxel.DecimateMesh(r.ctx, model, 1, cellSize, budget, false, progress.NullTracker{})
		if derr != nil {
			return nil, fmt.Errorf("load decimate: %w", derr)
		}
		if len(dec.Faces) < len(model.Faces) {
			plog.Printf("  Decimate: %d faces -> %d faces (cellSize=%.3f mm)",
				len(model.Faces), len(dec.Faces), cellSize)
			geomModel = dec
		}
		if err := r.checkCancel(); err != nil {
			return nil, err
		}
	}

	if r.opts.AlphaWrap {
		alpha := r.opts.AlphaWrapAlpha
		if alpha <= 0 {
			alpha = r.opts.NozzleDiameter
		}
		offset := r.opts.AlphaWrapOffset
		if offset <= 0 {
			offset = alpha / 30
		}

		plog.Printf("  Alpha-wrap: alpha=%.3f mm, offset=%.3f mm starting", alpha, offset)
		tWrap := time.Now()
		wrapped, werr := alphawrap.Wrap(geomModel, alpha, offset)
		if werr != nil {
			return nil, fmt.Errorf("alpha-wrap: %w", werr)
		}
		plog.Printf("  Alpha-wrap: %d vertices, %d faces in %.1fs",
			len(wrapped.Vertices), len(wrapped.Faces), time.Since(tWrap).Seconds())
		geomModel = wrapped
		reportHolesIfEnabled("alpha-wrap output", wrapped.Faces)

		// Post-wrap decimation: alpha-wrap output is dense (~one face
		// per α² of surface area), but downstream stages (Sticker,
		// Voxelize) only need detail at voxel cell resolution.
		// errorBudget caps drift at ½ a cell, so flat regions
		// collapse aggressively while curved silhouettes stop being
		// thinned once cumulative drift would exceed what
		// voxelization can resolve.
		if !r.opts.NoSimplify {
			cellSize := voxelCellSizes(r.opts).UpperXY
			budget := decimateErrorBudget(cellSize)
			postDec, derr := voxel.DecimateMesh(r.ctx, geomModel, 1, cellSize, budget, false, progress.NullTracker{})
			if derr != nil {
				return nil, fmt.Errorf("post-wrap decimate: %w", derr)
			}
			if len(postDec.Faces) < len(geomModel.Faces) {
				plog.Printf("  Post-wrap decimate: %d faces -> %d faces (cellSize=%.3f mm)",
					len(geomModel.Faces), len(postDec.Faces), cellSize)
				geomModel = postDec
			}
			reportHolesIfEnabled("post-wrap decimate output", geomModel.Faces)
			if err := r.checkCancel(); err != nil {
				return nil, err
			}
		}
	}

	return &loadOutput{
		Model:        geomModel,
		ColorModel:   model,
		InputMesh:    buildInputMeshData(model),
		PreviewScale: unitScale / totalScale,
		ExtentMM:     nativeExtentMM,
		// Freshly built: FaceBaseColor is pristine and the (empty)
		// appliedBaseColor* markers describe it accurately. A
		// disk-decoded loadOutput leaves this false; see the field doc.
		markersValid: true,
	}, nil
}

// runSplit is StageSplit's body (see stageDefs).
func (r *pipelineRun) runSplit() (any, error) {
	lo, err := r.Load()
	if err != nil {
		return nil, err
	}
	stage := progress.BeginStage(r.tracker, stageNames[StageSplit], false, 0)
	defer stage.Done()

	// Disabled-passthrough: emit the stage event so the UI shows
	// "Splitting" ticking by, then return a marker output that
	// downstream stages treat as "no split."
	if !r.opts.Split.Enabled {
		return &splitOutput{Enabled: false}, nil
	}

	// Split requires a watertight input; the design doc says the
	// frontend forces AlphaWrap=true when Split is enabled.
	// Surface the precondition violation here so the user sees a
	// clear error rather than a downstream "non-manifold cut
	// polygon" message from split.Cut.
	if !r.opts.AlphaWrap {
		return nil, fmt.Errorf("split: requires AlphaWrap=true (split.Cut needs a watertight input mesh; see docs/SPLIT.md)")
	}

	tSplit := time.Now()

	// Translate Options.Split into split.Cut + split.Layout calls.
	plane := split.AxisPlane(r.opts.Split.Axis, r.opts.Split.Offset)
	conn := split.ConnectorSettings{
		Style:       parseConnectorStyle(r.opts.Split.ConnectorStyle),
		Count:       r.opts.Split.ConnectorCount,
		DiamMM:      r.opts.Split.ConnectorDiamMM,
		DepthMM:     r.opts.Split.ConnectorDepthMM,
		ClearanceMM: r.opts.Split.ClearanceMM,
	}
	// Cut runs on lo.Model. The frontend forces AlphaWrap=true
	// when Split is enabled (see docs/SPLIT.md "Watertight
	// requirement"), so lo.Model is watertight under correct
	// frontend wiring. If a caller bypasses that guard,
	// split.Cut surfaces a clear error.
	res, err := split.Cut(lo.Model, plane, conn)
	if err != nil {
		return nil, fmt.Errorf("split.Cut: %w", err)
	}
	res.Orientation = [2]split.Orientation{
		parseOrientation(r.opts.Split.Orientation[0]),
		parseOrientation(r.opts.Split.Orientation[1]),
	}
	// Bed gap between the two laid-out halves. Hardcoded — users
	// who need a different layout rearrange in the slicer.
	const bedGapMM = 5.0
	xforms := split.Layout(res, bedGapMM)

	plog.Printf("  Split: cut and laid out two halves in %.1fs (half 0: %d verts, %d faces; half 1: %d verts, %d faces)",
		time.Since(tSplit).Seconds(),
		len(res.Halves[0].Vertices), len(res.Halves[0].Faces),
		len(res.Halves[1].Vertices), len(res.Halves[1].Faces))
	return &splitOutput{
		Enabled:   true,
		Halves:    res.Halves,
		Xform:     xforms,
		CutNormal: plane.Normal,
		CutPlaneD: plane.D,
	}, nil
}

// parseConnectorStyle converts the Options string into the typed
// split.ConnectorStyle. Unknown values fall back to NoConnectors;
// we trust the frontend to send valid strings.
func parseConnectorStyle(s string) split.ConnectorStyle {
	switch s {
	case "pegs":
		return split.Pegs
	case "dowels":
		return split.Dowels
	default:
		return split.NoConnectors
	}
}

// parseOrientation converts the Options string into the typed
// split.Orientation. Empty / unknown values fall back to OrientOriginal.
func parseOrientation(s string) split.Orientation {
	switch s {
	case "seam-up":
		return split.OrientSeamUp
	case "seam-down":
		return split.OrientSeamDown
	case "seam-left":
		return split.OrientSeamLeft
	case "seam-right":
		return split.OrientSeamRight
	default:
		return split.OrientOriginal
	}
}

// runSticker is StageSticker's body (see stageDefs).
func (r *pipelineRun) runSticker() (any, error) {
	lo, err := r.Load()
	if err != nil {
		return nil, err
	}
	return r.computeSticker(lo)
}

func (r *pipelineRun) computeSticker(lo *loadOutput) (*stickerOutput, error) {
	if len(r.opts.Stickers) == 0 {
		progress.BeginStage(r.tracker, stageNames[StageSticker], false, 0).Done()
		return &stickerOutput{}, nil
	}
	var sourceModel *loader.LoadedModel
	if r.opts.AlphaWrap {
		sourceModel = lo.Model
	} else {
		sourceModel = lo.ColorModel
	}
	model := loader.DeepCloneForMutation(sourceModel)
	adj := voxel.BuildTriAdjacency(model)
	si := voxel.NewSpatialIndex(model, 2)

	const stickerUnits = 1000
	stage := progress.BeginStage(r.tracker, stageNames[StageSticker], true, len(r.opts.Stickers)*stickerUnits)
	defer stage.Done()

	var decals []*voxel.StickerDecal
	for i, s := range r.opts.Stickers {
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
			return nil, fmt.Errorf("sticker %s: %w", s.ImagePath, err)
		}
		img, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("sticker %s: %w", s.ImagePath, err)
		}

		bounds := img.Bounds()
		if bounds.Dx() == 0 || bounds.Dy() == 0 {
			plog.Printf("  Sticker %s: 0x0 image, skipping", s.ImagePath)
			stage.Progress(base + stickerUnits)
			continue
		}

		var decal *voxel.StickerDecal
		switch s.Mode {
		case "unfold":
			seedTri := voxel.FindSeedTriangle(s.Center, model, si)
			if seedTri < 0 {
				plog.Printf("  Sticker %s: no triangle found near center, skipping", s.ImagePath)
				stage.Progress(base + stickerUnits)
				continue
			}
			decal, err = voxel.BuildStickerDecal(r.ctx, model, adj, img,
				seedTri, s.Center, s.Normal, s.Up, s.Scale, s.Rotation, s.MaxAngle,
				onProgress)
			if err != nil {
				return nil, err
			}
		case "projection":
			decal, err = voxel.BuildStickerDecalProjection(r.ctx, model, img,
				s.Center, s.Normal, s.Up, s.Scale, s.Rotation, onProgress)
			if err != nil {
				return nil, err
			}
			if len(decal.TriUVs) == 0 {
				plog.Printf("  Sticker %s: no front-facing geometry within projection rect, skipping", s.ImagePath)
				stage.Progress(base + stickerUnits)
				continue
			}
		default:
			return nil, fmt.Errorf("sticker %s: unknown mode %q", s.ImagePath, s.Mode)
		}
		plog.Printf("  Sticker %s: %d triangles covered", s.ImagePath, len(decal.TriUVs))
		if decal.LSCMResidual > 1e-5 && r.onWarning != nil {
			r.onWarning(progress.WarnKindGeneric, fmt.Sprintf(
				"Sticker %q didn't unfold cleanly (residual %.1e). The mesh in this region has very-poor-quality triangles; the sticker may look distorted. Try alpha-wrap or a different placement.",
				filepath.Base(s.ImagePath), decal.LSCMResidual))
		}
		decals = append(decals, decal)
		stage.Progress(base + stickerUnits)
	}

	so := &stickerOutput{
		Decals:        decals,
		Model:         model,
		FromAlphaWrap: r.opts.AlphaWrap,
	}
	so.si = si
	return so, nil
}

// Voxelize partitions the geometry mesh into cellslicer slabs and
// cells, samples a color per cell from the texture-bearing color
// mesh, and builds the cell-adjacency graph used by Dither. Output
// cells (visible only) feed ColorAdjust → Dither; the full per-slab
// cell polygons (vo.CellSlabs) feed Clip.
// runVoxelize is StageVoxelize's body (see stageDefs).
func (r *pipelineRun) runVoxelize() (any, error) {
	lo, err := r.Load()
	if err != nil {
		return nil, err
	}
	// Resolve the sticker stage and feed its decals into cell
	// sampling below. Base color always comes from ColorModel; each
	// decal is composited via a second nearest-tri lookup against the
	// sticker substrate (stickerOut.Model) — a clone of ColorModel
	// (projection/unfold) or the alpha-wrap mesh. Both configurations
	// use the same two-lookup path, so there is no alpha-wrap special
	// case here. stickerOut.Model lives in the same original-mesh
	// frame ColorModel does, so the per-half colorXform maps sample
	// points correctly for both lookups.
	stickerOut, err := r.Sticker()
	if err != nil {
		return nil, err
	}
	// Split, when enabled, drives the per-half cellslicer pass
	// below: each half's bed-space geometry is sliced and sampled
	// independently. See docs/split-cellslicer.md.
	so, err := r.Split()
	if err != nil {
		return nil, err
	}

	voxelSizes := voxelCellSizes(r.opts)
	cellSizeUpper := voxelSizes.UpperXY
	if cellSizeUpper <= 0 {
		cellSizeUpper = 0.4
	}
	cellSizeLayer0 := voxelSizes.Layer0XY
	if cellSizeLayer0 <= 0 {
		cellSizeLayer0 = cellSizeUpper
	}
	// Single policy point: layer 0 = the bottom slab. Used by
	// PartitionSlabAnalytic, SampleSlab, and AddWithinSlabAdjacency.
	cellSizeForSlab := func(i int) float32 {
		if i == 0 {
			return cellSizeLayer0
		}
		return cellSizeUpper
	}
	layerH := r.opts.LayerHeight
	if layerH <= 0 {
		layerH = 0.2
	}
	// The printer's first layer is typically taller than the rest
	// (e.g. Snapmaker U1 prints 0.2mm initial under 0.08mm layers).
	// Size the bottom slab to match so each mesh slab aligns 1:1 with
	// a print layer and the slicer cuts through slab interiors, not
	// the horizontal seams between them. See SlabBoundaryPlanesFirst.
	firstLayerH := voxelSizes.Layer0Z
	if firstLayerH <= 0 {
		firstLayerH = layerH
	}

	// The slab count (the natural work unit) is only known after
	// slicing, so the bar is normalized to ScaleTotal and each
	// unit/phase maps onto a weighted window of it — see Stage.Span.
	stage := progress.BeginStage(r.tracker, stageNames[StageVoxelize], true, progress.ScaleTotal)
	defer stage.Done()

	// Color sampling reads from ColorModel (original-mesh coords,
	// uncut and unmoved by Split). The spatial index is built once
	// and shared across both halves. When Split is enabled, each
	// half's geometry is sliced in its own bed-space frame and a
	// per-half inverse layout transform maps sample points back to
	// ColorModel coords. See docs/split-cellslicer.md.
	colorModel := lo.ColorModel
	spatial := voxel.NewSpatialIndex(colorModel, cellSizeUpper)

	// MaterialX base-color override for untextured faces, plumbed
	// into the per-cell sampler. Without it every cell on an
	// untextured face falls back to that face's single centroid-
	// baked FaceBaseColor (the preview approximation), so a
	// triplanar-textured face collapses to one flat color and the
	// dither turns it into noise. Memoized on StageCache, so this
	// shares Load's XML parse + image decode (nil when no MaterialX
	// is configured). Any parse error was already surfaced in Load.
	baseColorOverride, _ := r.cache.baseColorOverride(
		r.opts.BaseColorMaterialX,
		r.opts.BaseColorMaterialXTileMM,
		r.opts.BaseColorMaterialXTriplanarSharpness,
		r.tracker,
	)

	// Sticker substrate + its spatial index for the per-cell decal
	// lookup. All nil when no stickers were placed, in which case
	// SampleSlab falls straight through to the base-color-only path.
	var (
		stickerModel *loader.LoadedModel
		stickerSI    *voxel.SpatialIndex
		decals       []*voxel.StickerDecal
	)
	if len(stickerOut.Decals) > 0 {
		stickerModel = stickerOut.Model
		stickerSI = stickerOut.ensureSI()
		decals = stickerOut.Decals
	}

	// Work units: one per split half (geometry already laid out in
	// bed coords by split.Layout), or a single unit on the whole
	// model when Split is off. colorXform maps a unit's sample
	// points back into ColorModel coords; nil = identity.
	type voxUnit struct {
		geom       *loader.LoadedModel
		colorXform func([3]float32) [3]float32
		halfIdx    byte
	}
	var units []voxUnit
	if so.Enabled {
		for h := 0; h < 2; h++ {
			// Each half's geometry is in bed coords; ApplyInverse maps
			// a sample point back to the original-mesh coords where
			// ColorModel (and sticker decals) live.
			units = append(units, voxUnit{
				geom:       so.Halves[h],
				colorXform: so.Xform[h].ApplyInverse,
				halfIdx:    byte(h),
			})
		}
	} else {
		units = []voxUnit{{geom: lo.Model, colorXform: nil, halfIdx: 0}}
	}

	// Run the cellslicer chain (slice → footprint → partition →
	// sample → adjacency) once per unit, then concatenate. The
	// global cell index is the position in the flattened CellSlabs
	// (unit 0 first), which matches CellSamples and the neighbor
	// graph. Neighbor indices from unit N are shifted by the count
	// of cells already emitted; halves never share adjacency edges
	// (they are physically separate on the bed).
	var (
		slabs           []cellslicer.Slab
		samples         []cellslicer.CellSample
		globalNeighbors [][]voxel.Neighbor
		agg             cellslicer.PartitionStats
	)
	for ui, u := range units {
		// Each unit owns an equal window of the normalized bar
		// (split halves are roughly equal work; unsplit = the
		// whole bar).
		progLo := ui * progress.ScaleTotal / len(units)
		progHi := (ui + 1) * progress.ScaleTotal / len(units)
		hv, herr := r.sliceSampleHalf(u.geom, colorModel, spatial, stickerModel, stickerSI, decals, baseColorOverride, u.colorXform, u.halfIdx, cellSizeForSlab, firstLayerH, layerH, stage, progLo, progHi)
		if herr != nil {
			return nil, herr
		}
		cellOffset := len(samples)
		slabOffset := len(slabs)
		// Renumber this unit's slabs and samples from unit-local to
		// global indices. Slab.Index and CellSample.SlabIdx must
		// both address the flattened CellSlabs list, or anything
		// indexing slabs[SlabIdx] (debug cell dumps) and any
		// Layer-keyed adjacency (ActiveCell.Layer → BuildNeighbors,
		// FloydSteinberg's layer sort) would collide half 1's cells
		// onto half 0's slabs.
		for i := range hv.slabs {
			hv.slabs[i].Index = slabOffset + i
		}
		for i := range hv.samples {
			hv.samples[i].SlabIdx += slabOffset
		}
		slabs = append(slabs, hv.slabs...)
		samples = append(samples, hv.samples...)
		for _, nbrs := range hv.neighbors {
			if cellOffset == 0 {
				// First unit (and the whole unsplit graph): indices
				// are already global, so reuse the rows as-is.
				globalNeighbors = append(globalNeighbors, nbrs)
				continue
			}
			shifted := make([]voxel.Neighbor, len(nbrs))
			for k, n := range nbrs {
				shifted[k] = voxel.Neighbor{Idx: n.Idx + cellOffset, Weight: n.Weight}
			}
			globalNeighbors = append(globalNeighbors, shifted)
		}
		agg.RawRing += hv.stats.RawRing
		agg.RawHex += hv.stats.RawHex
		agg.Final += hv.stats.Final
	}
	nCells := len(samples)

	// Build ActiveCells: one per visible cell. Hidden
	// (Alpha == false) cells are dropped so palette selection
	// and dither operate only on visible color. cellToVisible
	// maps global cell index → visible index, used to reindex
	// the adjacency graph below.
	cells := make([]voxel.ActiveCell, 0, len(samples))
	visibleToCell := make([]int, 0, len(samples))
	cellToVisible := make([]int, len(samples))
	for i := range cellToVisible {
		cellToVisible[i] = -1
	}
	for gi, s := range samples {
		if !s.Alpha {
			continue
		}
		cellToVisible[gi] = len(cells)
		visibleToCell = append(visibleToCell, gi)
		cells = append(cells, voxel.ActiveCell{
			Grid:  0,
			Col:   s.CellIdx,
			Row:   0,
			Layer: s.SlabIdx,
			Cx:    s.Centroid[0],
			Cy:    s.Centroid[1],
			Cz:    s.Centroid[2],
			Color: s.Color,
			Area:  s.Area,
		})
	}
	visibleNeighbors := make([][]voxel.Neighbor, len(cells))
	nEdges := 0
	for gi, nbrs := range globalNeighbors {
		vi := cellToVisible[gi]
		if vi < 0 {
			continue
		}
		out := visibleNeighbors[vi]
		for _, n := range nbrs {
			vj := cellToVisible[n.Idx]
			if vj < 0 {
				continue
			}
			out = append(out, voxel.Neighbor{Idx: vj, Weight: n.Weight})
		}
		visibleNeighbors[vi] = out
		nEdges += len(out)
	}

	plog.Printf("  Cellslicer: %d units, %d slabs, %d cells (%d visible), %d adj-edges; cellSize=%.3f/%.3fmm (layer0/upper) layerH=%.3fmm",
		len(units), len(slabs), nCells, len(cells), nEdges/2,
		cellSizeLayer0, cellSizeUpper, layerH)

	// agg accumulates per-slab partition stats across all units.
	// RawRing+RawHex are the pre-clip generator output; Final is the
	// surviving cell count after each raw cell is Clipper-clipped to
	// its region (empty intersections are never emitted). The gap
	// between RawRing+RawHex and Final is cells that clipped to
	// nothing.
	plog.Printf("  Partition: ring=%d hex=%d final=%d",
		agg.RawRing, agg.RawHex, agg.Final)

	return &voxelizeOutput{
		Cells:         cells,
		CellSlabs:     slabs,
		CellSamples:   samples,
		Neighbors:     visibleNeighbors,
		VisibleToCell: visibleToCell,
		LayerH:        layerH,
		CellSize:      cellSizeUpper,
	}, nil
}

// halfVoxels is one work unit's cellslicer output, before the global
// visible-cell / adjacency-reindex step Voxelize does across units.
// neighbors is indexed by the unit-local global cell index (parallel
// to samples and to the flattened slabs[*].Cells).
type halfVoxels struct {
	slabs     []cellslicer.Slab
	samples   []cellslicer.CellSample
	neighbors [][]voxel.Neighbor
	stats     cellslicer.PartitionStats
}

// sampleBufs holds the per-worker scratch buffers for one cellslicer
// sampling goroutine: color indexes ColorModel for base color, sticker
// indexes the sticker substrate for the decal lookup (nil when no
// stickers were placed). Kept per worker so the SampleSlab inner loop
// never allocates.
type sampleBufs struct {
	color   *voxel.SearchBuf
	sticker *voxel.SearchBuf
}

// sliceSampleHalf runs slice → footprint → partition → sample →
// adjacency on one geometry mesh. geom is sliced in its own coordinate
// frame; colorModel + spatial are the shared (original-coords) color
// source, and colorXform maps each sample point from geom's frame back
// into colorModel's frame (nil = identity). halfIdx is stamped onto
// every emitted slab and sample. See docs/split-cellslicer.md.
//
// stage + [progLo, progHi] is this unit's window of the normalized
// Voxelize progress bar (see progress.ScaleTotal); sub-phases tick
// inside it weighted by rough wall-clock share.
func (r *pipelineRun) sliceSampleHalf(
	geom, colorModel *loader.LoadedModel,
	spatial *voxel.SpatialIndex,
	stickerModel *loader.LoadedModel,
	stickerSI *voxel.SpatialIndex,
	decals []*voxel.StickerDecal,
	override voxel.BaseColorOverride,
	colorXform func([3]float32) [3]float32,
	halfIdx byte,
	cellSizeForSlab func(int) float32,
	firstLayerH, layerH float32,
	stage *progress.Stage,
	progLo, progHi int,
) (halfVoxels, error) {
	// Sub-phase weights (percent of this unit's window). Rough
	// wall-clock shares — good enough for a smooth bar, not a timing
	// claim; the plog line below reports the real per-phase seconds.
	pct := func(p int) int { return progLo + (progHi-progLo)*p/100 }
	progSlice := stage.Span(pct(0), pct(20))
	progFp := stage.Span(pct(20), pct(35))
	progSlab := stage.Span(pct(35), pct(90))
	progAdj := stage.Span(pct(90), pct(100))

	tSlice := time.Now()
	zMin, zMax := modelZRange(geom)
	if zMax <= zMin {
		return halfVoxels{}, fmt.Errorf("cellslicer: degenerate Z range")
	}
	planes := cellslicer.SlabBoundaryPlanesFirst(zMin, zMax, firstLayerH, layerH)
	layers := cellslicer.SliceMeshProgress(r.ctx, geom, planes, progSlice)
	if err := r.checkCancel(); err != nil {
		return halfVoxels{}, err
	}
	nSlabs := len(layers) - 1
	if nSlabs < 1 {
		return halfVoxels{}, fmt.Errorf("cellslicer: no slabs produced")
	}
	sliceElapsed := time.Since(tSlice).Seconds()

	nWorkers := runtime.NumCPU()
	if nWorkers < 1 {
		nWorkers = 1
	}
	if nWorkers > nSlabs {
		nWorkers = nSlabs
	}

	// Footprint phase: compute the planar footprint for every slab up
	// front. Used twice each — once for the slab itself and once when
	// its neighbours look at it to decide where caps lie.
	tFp := time.Now()
	// Interior-face footprints recover thin horizontal sheets (e.g. an
	// alpha-wrapped single-surface roof, ~0.03 mm) that lie wholly
	// between two slab planes and so contribute nothing to the bounding-
	// plane slices ComputeFootprint uses. Unioned into the slab footprint
	// below, they give cap detection the sheet's surface. Gated by an
	// advanced opt-out for A/B timing.
	var interiorFps []*cellslicer.Footprint
	if !r.opts.NoInteriorFaceFootprint {
		interiorFps = cellslicer.InteriorHorizontalFootprints(r.ctx, geom, planes)
	}
	surfaceFps, surfDrop := cellslicer.SlabSurfaceFootprints(r.ctx, geom, planes)
	if err := r.checkCancel(); err != nil {
		return halfVoxels{}, err
	}
	if surfDrop.Dropped > 0 {
		// The surface-projection stage discards near-vertical wall slices
		// whose XY projection is a degenerate sliver (see triBandXYPath).
		// Logged so the drop is never silent. minPx is the smallest dither
		// pixel across the model; a vertical wall's discarded slices are
		// collinear (~0 area), so a discard whose area rivals a pixel is the
		// breadcrumb that the filter may have eaten real coverage — flagged
		// loudly here so a future surface hole points straight at this stage
		// instead of costing a blind bisect.
		minCS := cellSizeForSlab(0)
		for i := 1; i < nSlabs; i++ {
			if cs := cellSizeForSlab(i); cs < minCS {
				minCS = cs
			}
		}
		minPx := (minCS / 4) * (minCS / 4)
		plog.Printf("  Surface footprint: dropped %d/%d near-vertical slivers (Σarea %.4g mm², max single %.4g mm², pixel %.4g mm²)",
			surfDrop.Dropped, surfDrop.Considered, surfDrop.AreaSum, surfDrop.AreaMax, minPx)
		if surfDrop.AreaMax > minPx {
			plog.Printf("  WARNING: a dropped surface sliver (%.4g mm²) exceeded one dither pixel (%.4g mm²); if the output shows surface holes, suspect the triBandXYPath degeneracy filter",
				surfDrop.AreaMax, minPx)
		}
	}
	footprints := make([]*cellslicer.Footprint, nSlabs)
	capFps := make([]*cellslicer.Footprint, nSlabs)
	var fpDone atomic.Int64
	fpErr := runParallel(r.ctx, nWorkers, nSlabs, nil, func(i int, _ any) {
		defer func() { progFp(int(fpDone.Add(1)), nSlabs) }()
		// capFp = bounding-plane footprint (zBot/zTop contours + interior
		// horizontal sheets) — feeds the cap/buried-wall neighbour test.
		capFp := cellslicer.ComputeFootprint(layers[i].Loops, layers[i+1].Loops)
		// interiorFps, when present, is sized to nSlabs (== len(planes)-1
		// == len(layers)-1); the length guard makes that invariant
		// explicit rather than load-bearing.
		if interiorFps != nil && i < len(interiorFps) && interiorFps[i] != nil {
			capFp = cellslicer.FootprintUnion(capFp, interiorFps[i])
		}
		capFps[i] = capFp
		// covFp = capFp ∪ in-band surface projection. Stored as the slab
		// Footprint and used for all coverage/tiling (band, seeds, clip).
		footprints[i] = capFp
		if surfaceFps != nil && i < len(surfaceFps) && surfaceFps[i] != nil {
			footprints[i] = cellslicer.FootprintUnion(capFp, surfaceFps[i])
		}
	})
	if fpErr != nil {
		return halfVoxels{}, fpErr
	}
	fpElapsed := time.Since(tFp).Seconds()

	// Per-slab phase: partition + sample. Each worker writes only its
	// own slabs[i] / perSlabSamples[i] slots, so no locks are needed.
	tSlab := time.Now()
	var partitionNs, sampleNs, slabDone atomic.Int64
	slabs := make([]cellslicer.Slab, nSlabs)
	perSlabSamples := make([][]cellslicer.CellSample, nSlabs)
	perSlabStats := make([]cellslicer.PartitionStats, nSlabs)
	slabErr := runParallel(r.ctx, nWorkers, nSlabs, func(workerID int) any {
		b := &sampleBufs{color: voxel.NewSearchBuf(len(colorModel.Faces))}
		if stickerModel != nil {
			// The decal lookup indexes stickerModel's faces, so its
			// SearchBuf must be sized to stickerModel — it can differ
			// (subdivided clone / wrap) from colorModel.
			b.sticker = voxel.NewSearchBuf(len(stickerModel.Faces))
		}
		return b
	}, func(i int, state any) {
		bufs := state.(*sampleBufs)
		buf := bufs.color
		t0 := time.Now()
		// PartitionSlabAnalytic takes this slab's COVERAGE footprint
		// (footprints[i], the in-band silhouette) for band/ring/clip, and
		// the neighbours' bounding-plane CAP footprints (capFps[i±1], or
		// nil at the top/bottom) for the buried-wall test. It emits ring
		// cells along the lateral band, hex cells only where the neighbour
		// cap cross-sections leave interior surface exposed (caps).
		var fpBelow, fpAbove *cellslicer.Footprint
		if i > 0 {
			fpBelow = capFps[i-1]
		}
		if i+1 < nSlabs {
			fpAbove = capFps[i+1]
		}
		cs := cellSizeForSlab(i)
		cells, coverTarget, stats := cellslicer.PartitionSlabAnalytic(footprints[i], fpBelow, fpAbove, cs)
		perSlabStats[i] = stats
		slabs[i] = cellslicer.Slab{
			Index:       i,
			HalfIdx:     halfIdx,
			ZBot:        planes[i],
			ZTop:        planes[i+1],
			BotLayer:    &layers[i],
			TopLayer:    &layers[i+1],
			Footprint:   footprints[i],
			CoverTarget: coverTarget,
			Cells:       cells,
		}
		t1 := time.Now()
		partitionNs.Add(int64(t1.Sub(t0)))
		perSlabSamples[i] = cellslicer.SampleSlab(&slabs[i], i, colorModel, spatial, cs, 0, decals, stickerModel, stickerSI, override, colorXform, buf, bufs.sticker)
		sampleNs.Add(int64(time.Since(t1)))
		progSlab(int(slabDone.Add(1)), nSlabs)
	})
	if slabErr != nil {
		return halfVoxels{}, slabErr
	}
	slabElapsed := time.Since(tSlab).Seconds()

	reportOverlapsIfEnabled(slabs, cellSizeForSlab)

	nCells := 0
	for i := range slabs {
		nCells += len(slabs[i].Cells)
	}
	samples := make([]cellslicer.CellSample, 0, nCells)
	for i := range perSlabSamples {
		samples = append(samples, perSlabSamples[i]...)
	}

	// Adjacency phase, within this unit only. Within-slab is fully
	// independent per slab. Cross-slab pair (i,i+1) writes to both
	// slabs' neighbor rows, so we split pairs into even/odd parities to
	// keep the two phases lock-free.
	tAdj := time.Now()
	globalOffsets := cellslicer.SlabGlobalOffsets(slabs)
	neighbors := make([][]voxel.Neighbor, globalOffsets[nSlabs])
	// Progress ticks cover the within-slab pass only; the cross-slab
	// parity passes below are a small tail of the phase.
	var adjDone atomic.Int64
	if err := runParallel(r.ctx, nWorkers, nSlabs, nil, func(i int, _ any) {
		cellslicer.AddWithinSlabAdjacency(&slabs[i], globalOffsets[i], cellSizeForSlab(i), 0, neighbors)
		progAdj(int(adjDone.Add(1)), nSlabs)
	}); err != nil {
		return halfVoxels{}, err
	}
	for parity := 0; parity < 2; parity++ {
		pairs := make([]int, 0, nSlabs/2+1)
		for i := parity; i < nSlabs-1; i += 2 {
			pairs = append(pairs, i)
		}
		if err := runParallel(r.ctx, nWorkers, len(pairs), nil, func(k int, _ any) {
			i := pairs[k]
			cellslicer.AddCrossSlabAdjacency(&slabs[i], globalOffsets[i], &slabs[i+1], globalOffsets[i+1], neighbors)
		}); err != nil {
			return halfVoxels{}, err
		}
	}
	adjElapsed := time.Since(tAdj).Seconds()

	var agg cellslicer.PartitionStats
	for _, s := range perSlabStats {
		agg.RawRing += s.RawRing
		agg.RawHex += s.RawHex
		agg.Final += s.Final
	}
	partitionCPU := time.Duration(partitionNs.Load()).Seconds()
	sampleCPU := time.Duration(sampleNs.Load()).Seconds()
	plog.Printf("  Cellslicer half %d: %d slabs, %d cells; slice=%.2fs fp=%.2fs slab=%.2fs [partCPU=%.2fs sampCPU=%.2fs] adj=%.2fs (workers=%d)",
		halfIdx, nSlabs, nCells, sliceElapsed, fpElapsed, slabElapsed, partitionCPU, sampleCPU, adjElapsed, nWorkers)

	return halfVoxels{slabs: slabs, samples: samples, neighbors: neighbors, stats: agg}, nil
}

// modelZRange returns the min and max Z over a model's vertices.
func modelZRange(m *loader.LoadedModel) (zMin, zMax float32) {
	if len(m.Vertices) == 0 {
		return
	}
	zMin = m.Vertices[0][2]
	zMax = m.Vertices[0][2]
	for _, v := range m.Vertices {
		if v[2] < zMin {
			zMin = v[2]
		}
		if v[2] > zMax {
			zMax = v[2]
		}
	}
	return
}

// runColorAdjust is StageColorAdjust's body (see stageDefs).
func (r *pipelineRun) runColorAdjust() (any, error) {
	vo, err := r.Voxelize()
	if err != nil {
		return nil, err
	}
	stage := progress.BeginStage(r.tracker, stageNames[StageColorAdjust], false, 0)
	defer stage.Done()
	adj := voxel.ColorAdjustment{
		Brightness: r.opts.Brightness,
		Contrast:   r.opts.Contrast,
		Saturation: r.opts.Saturation,
	}
	tAdj := time.Now()
	cells, cerr := voxel.AdjustCellColors(r.ctx, vo.Cells, adj)
	if cerr != nil {
		return nil, cerr
	}
	if !adj.IsIdentity() {
		plog.Printf("  Adjusted colors (B:%+.0f C:%+.0f S:%+.0f) in %.1fs",
			r.opts.Brightness, r.opts.Contrast, r.opts.Saturation, time.Since(tAdj).Seconds())
	}
	return &colorAdjustOutput{Cells: cells}, nil
}

// runColorWarp is StageColorWarp's body (see stageDefs).
func (r *pipelineRun) runColorWarp() (any, error) {
	cao, err := r.ColorAdjust()
	if err != nil {
		return nil, err
	}
	stage := progress.BeginStage(r.tracker, stageNames[StageColorWarp], false, 0)
	defer stage.Done()
	if len(r.opts.WarpPins) == 0 {
		cells := make([]voxel.ActiveCell, len(cao.Cells))
		copy(cells, cao.Cells)
		return &colorWarpOutput{Cells: cells}, nil
	}
	pins := make([]voxel.ColorWarpPin, len(r.opts.WarpPins))
	for i, p := range r.opts.WarpPins {
		src, perr := palette.ParsePalette([]string{p.SourceHex})
		if perr != nil {
			return nil, fmt.Errorf("warp pin %d source: %w", i, perr)
		}
		tgt, perr := palette.ParsePalette([]string{p.TargetHex})
		if perr != nil {
			return nil, fmt.Errorf("warp pin %d target: %w", i, perr)
		}
		pins[i] = voxel.ColorWarpPin{Source: src[0], Target: tgt[0], Sigma: p.Sigma}
	}
	tWarp := time.Now()
	cells, werr := voxel.WarpCellColors(r.ctx, cao.Cells, pins)
	if werr != nil {
		return nil, werr
	}
	plog.Printf("  Warped colors (%d pins) in %.1fs", len(pins), time.Since(tWarp).Seconds())
	return &colorWarpOutput{Cells: cells}, nil
}

// runPalette is StagePalette's body (see stageDefs).
func (r *pipelineRun) runPalette() (any, error) {
	cwo, err := r.ColorWarp()
	if err != nil {
		return nil, err
	}
	stage := progress.BeginStage(r.tracker, stageNames[StagePalette], false, 0)
	defer stage.Done()

	pcfg, perr := buildPaletteConfig(r.opts)
	if perr != nil {
		return nil, perr
	}
	if pcfg.NumColors > export3mf.MaxFilaments {
		return nil, fmt.Errorf("palette has %d colors but max supported is %d", pcfg.NumColors, export3mf.MaxFilaments)
	}
	cells := make([]voxel.ActiveCell, len(cwo.Cells))
	copy(cells, cwo.Cells)
	ditherMode := r.opts.Dither
	pal, palLabels, palDisplay, perr := voxel.ResolvePalette(r.ctx, cells, pcfg, ditherMode != "none", r.tracker)
	if perr != nil {
		return nil, perr
	}
	if palDisplay != "" {
		plog.Printf("%s", palDisplay)
	}
	if len(pal) == 0 {
		return nil, fmt.Errorf("no palette colors")
	}
	if r.opts.ColorSnap > 0 {
		if serr := voxel.SnapColors(r.ctx, cells, pal, r.opts.ColorSnap); serr != nil {
			return nil, serr
		}
		plog.Printf("  Snapped cell colors toward palette by delta E %.1f", r.opts.ColorSnap)
	}
	if len(pcfg.Locked) == 0 && len(pal) > 1 {
		assigns, aerr := voxel.AssignColors(r.ctx, cells, pal)
		if aerr != nil {
			return nil, aerr
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
	return &paletteOutput{
		Palette:       pal,
		PaletteLabels: palLabels,
		Cells:         cells,
	}, nil
}

// runDither is StageDither's body (see stageDefs).
func (r *pipelineRun) runDither() (any, error) {
	po, err := r.Palette()
	if err != nil {
		return nil, err
	}
	vo, err := r.Voxelize()
	if err != nil {
		return nil, err
	}
	// Budget: dither work units + flood-fill work units. Most modes
	// do one dither pass over n cells, so dither = n. dizzy-
	// corrected runs voxel.DizzyCorrectionPasses passes back-to-
	// back, so its dither budget scales accordingly. The internal
	// passes use a tracker wrapper that offsets per-pass progress
	// onto a single continuous bar -- see ditherPassTracker.
	ditherMode := r.opts.Dither
	ditherUnits := len(po.Cells)
	if ditherMode == "dizzy-corrected" {
		ditherUnits = voxel.DizzyCorrectionPasses * len(po.Cells)
	}
	stage := progress.BeginStage(r.tracker, stageNames[StageDither], true, ditherUnits+len(po.Cells))
	defer stage.Done()
	cells := po.Cells
	pal := po.Palette
	tDither := time.Now()
	var assignments []int32
	var derr error
	// Phase 2 transition: cellslicer Voxelize doesn't yet
	// populate the adjacency graph (Phase 3 will). Error-
	// diffusion dithers degenerate to nearest-palette without
	// neighbors, so short-circuit to AssignColors when the
	// graph is empty, regardless of requested mode.
	if len(vo.Neighbors) == 0 {
		assignments, derr = voxel.AssignColors(r.ctx, cells, pal)
		if derr != nil {
			return nil, derr
		}
		plog.Printf("  Dithered (none; cell-adjacency graph empty, Phase 3 TODO) %d cells in %.1fs",
			len(cells), time.Since(tDither).Seconds())
		return &ditherOutput{Assignments: assignments}, nil
	}
	switch ditherMode {
	case "dizzy-corrected":
		neighbors := vo.Neighbors
		assignments, derr = voxel.DitherCorrected(r.ctx, cells, pal, neighbors, r.tracker)
	case "dizzy-2hop":
		// Single-pass dizzy with an expanded 2-hop neighbor
		// stencil so stranded cells (no unprocessed 1-hop
		// neighbors) can still distribute error to 2-hop
		// neighbors instead of dropping it.
		neighbors := voxel.BuildNeighbors2Hop(cells)
		assignments, derr = voxel.DitherWithNeighbors(r.ctx, cells, pal, neighbors, r.tracker)
	case "dizzy-recover":
		// Single-pass dizzy with a local-solve recovery on
		// stranded cells: instead of dropping the residual,
		// search neighbor palette swaps for one that absorbs
		// it in the global-drift sense.
		neighbors := vo.Neighbors
		assignments, derr = voxel.DitherWithRecover(r.ctx, cells, pal, neighbors, r.tracker)
	case "floyd-steinberg":
		neighbors := vo.Neighbors
		assignments, derr = voxel.FloydSteinberg(r.ctx, cells, pal, neighbors, r.tracker)
	case "riemersma":
		neighbors := vo.Neighbors
		assignments, derr = voxel.Riemersma(r.ctx, cells, pal, neighbors, r.opts.RiemersmaInputBias, r.tracker)
	case "riemersma-pair":
		// Sliding 2-cell Riemersma with residual-cancellation
		// coupling. Same drift as base Riemersma; lower wander on
		// flat/textured fixtures at ≈2× the per-cell cost.
		neighbors := vo.Neighbors
		assignments, derr = voxel.RiemersmaPair(r.ctx, cells, pal, neighbors, voxel.RiemersmaPairCancellationDefault, r.opts.RiemersmaInputBias, r.tracker)
	case "blue-noise":
		// Adaptive simplex blue-noise threshold dither: per-cell
		// best-K simplex (1..palette_size) selected by per-cell
		// projection-error tolerance, with LDS-driven choice
		// among simplex vertices. Trades a small drift for big
		// reductions in wander on uniform/near-flat regions
		// (where Riemersma's window accumulator forces visible
		// far-palette picks).
		neighbors := vo.Neighbors
		tol := r.opts.BlueNoiseTolerance
		if tol <= 0 {
			tol = voxel.BlueNoiseAdaptiveTolDefault
		}
		assignments, derr = voxel.BlueNoiseAdaptive(r.ctx, cells, pal, neighbors, tol, r.tracker)
	default:
		assignments, derr = voxel.AssignColors(r.ctx, cells, pal)
	}
	if derr != nil {
		return nil, derr
	}
	plog.Printf("  Dithered (%s) %d cells in %.1fs", ditherMode, len(cells), time.Since(tDither).Seconds())
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
		plog.Printf("    #%02X%02X%02X: %d cells (%.1f%%)", c[0], c[1], c[2], counts[i], 100*float64(counts[i])/float64(total))
	}
	// The minislicer pipeline doesn't need flood-fill patches:
	// each section is its own colored region in the prism wall,
	// and Mesh3D extrudes per-section walls directly from
	// `assignments`. Leaving PatchMap/NumPatches/PatchAssignment
	// nil keeps the cached struct shape stable.
	return &ditherOutput{
		Assignments: assignments,
	}, nil
}

// Clip cuts the geometry mesh into per-cell fragments via
// cellslicer.ClipMeshToCellsManifold (Manifold per-slab boolean
// intersect; see clip_manifold.go). Each output face is tagged with
// the dithered palette index of its source cell; faces from cells
// with no dither assignment fall back to the mesh's most-common
// palette index. The geometry mesh must be closed and orientable —
// the alpha-wrap path produces this directly; for raw meshes the
// pipeline relies on opts.AlphaWrap.
// runClip is StageClip's body (see stageDefs).
func (r *pipelineRun) runClip() (any, error) {
	do, err := r.Dither()
	if err != nil {
		return nil, err
	}
	vo, err := r.Voxelize()
	if err != nil {
		return nil, err
	}
	lo, err := r.Load()
	if err != nil {
		return nil, err
	}
	// Split, when enabled, makes the clip run once per half against
	// that half's bed-space geometry. See docs/split-cellslicer.md.
	so, err := r.Split()
	if err != nil {
		return nil, err
	}

	// The clip job count (cells, or merged groups) is only known
	// mid-stage, so the bar is normalized to ScaleTotal. Each
	// half/window gives its first ~15% to the sequential per-slab
	// source pre-split and the rest to the clip jobs.
	stage := progress.BeginStage(r.tracker, stageNames[StageClip], true, progress.ScaleTotal)
	defer stage.Done()
	clipProgFor := func(lo, hi int) *cellslicer.ClipProgress {
		mid := lo + (hi-lo)*15/100
		return &cellslicer.ClipProgress{
			SlabSplit: stage.Span(lo, mid),
			Jobs:      stage.Span(mid, hi),
		}
	}
	tClip := time.Now()

	// Build a global-cell-index → palette-assignment lookup.
	// Visible cells have a valid Dither output; hidden cells
	// (currently none, since SampleCells marks every textured
	// surface alpha=true) get -1.
	nGlobal := len(vo.CellSamples)
	cellAssign := make([]int32, nGlobal)
	for i := range cellAssign {
		cellAssign[i] = -1
	}
	for vi, gi := range vo.VisibleToCell {
		cellAssign[gi] = do.Assignments[vi]
	}

	// Cell merging groups same-kind, same-color cells within each
	// slab and clips the model against each group's merged prism in
	// one Manifold intersection (instead of one per cell), cutting
	// boolean count and removing internal seams between same-color
	// cells. Colors come from Dither, so this is purely a clip-time
	// /geometry optimisation with no effect on the dithered output.
	//
	// It is forced off under ShowSampledColors: that diagnostic colours
	// each output face by its source cell's SAMPLED input colour
	// (overrideFaceColorsFromSamples via ShellSectionIdx), which
	// needs per-cell face provenance. Merging same-palette cells
	// intentionally coarsens that provenance — fine for the real
	// palette-coloured output (a merge group shares one palette
	// index), but it would smear the per-cell sampled view. So the
	// diagnostic runs the per-cell clip to keep its provenance exact.
	// Merging is ON by default (clip-time / triangle-count win); NoCellMerge
	// opts out per-cell. Shared with the Clip cache key via
	// effectiveMergeCells so the two can never diverge.
	mergeCells := effectiveMergeCells(r.opts)
	if mergeCells {
		plog.Printf("  Clip: Manifold merged-cell intersect (same-color cells per slab, open-edge bloat=%.3gmm)",
			cellslicer.OpenEdgeBloatMM)
	} else {
		plog.Printf("  Clip: Manifold per-cell intersect (open-edge bloat=%.3gmm)",
			cellslicer.OpenEdgeBloatMM)
	}
	var (
		clipped      cellslicer.ClipResult
		shellHalfIdx []byte
		cerr         error
	)
	if so.Enabled {
		// One clip per half, against that half's bed-space geometry.
		// clipPerHalf concatenates the two results (half 0 first,
		// unified vertex table) and tags each face with its half;
		// FaceCellIdx is remapped to the global flattened-CellSlabs
		// index space so the per-cell bookkeeping below is unchanged.
		// Each half's progress window is proportional to its share
		// of the cells.
		mkHalfProg := func(cellOffset, nSub, total int) *cellslicer.ClipProgress {
			if total <= 0 {
				return nil
			}
			return clipProgFor(progress.ScaleTotal*cellOffset/total,
				progress.ScaleTotal*(cellOffset+nSub)/total)
		}
		if mergeCells {
			clipped, shellHalfIdx, cerr = clipPerHalfMerged(r.ctx, so, vo.CellSlabs, cellAssign, mkHalfProg)
		} else {
			clipped, shellHalfIdx, cerr = clipPerHalf(r.ctx, so, vo.CellSlabs, mkHalfProg)
		}
	} else {
		prog := clipProgFor(0, progress.ScaleTotal)
		if mergeCells {
			clipped, cerr = cellslicer.ClipMeshToMergedCellsManifoldProgress(r.ctx, lo.Model, vo.CellSlabs, cellAssign, prog)
		} else {
			clipped, cerr = cellslicer.ClipMeshToCellsManifoldProgress(r.ctx, lo.Model, vo.CellSlabs, prog)
		}
	}
	if cerr != nil {
		return nil, fmt.Errorf("cellslicer clip: %w", cerr)
	}
	if clipped.CellRep != nil {
		plog.Printf("  Clip merged %d cells → %d groups", len(clipped.CellRep), distinctReps(clipped.CellRep))
	}
	// Map per-face cell index → palette assignment. Faces from
	// cells with no assignment (-1) get -1, downstream
	// SafeAssignments will substitute the fallback.
	faceAssign := make([]int32, len(clipped.Faces))
	for i, gi := range clipped.FaceCellIdx {
		if gi >= 0 && int(gi) < len(cellAssign) {
			faceAssign[i] = cellAssign[gi]
		} else {
			faceAssign[i] = -1
		}
	}
	fallback := mostCommonNonNeg(faceAssign)
	for i, a := range faceAssign {
		if a < 0 {
			faceAssign[i] = fallback
		}
	}

	plog.Printf("  Clip: %d verts, %d faces in %.1fs",
		len(clipped.Verts), len(clipped.Faces), time.Since(tClip).Seconds())
	reportHolesIfEnabled("clip output", clipped.Faces)

	// Per-cell face-count cross-tab against partition pixel
	// bucket. Identifies the "missing geometry" suspects:
	// small cells whose outline is too thin for any source-tri
	// fragment to land inside, producing 0 faces in the clip
	// output. A high zero-face fraction in the 1-px / 2-4 px
	// buckets means surface area visible in the input mesh is
	// silently dropped at clip time.
	facesPerCell := make([]int, nGlobal)
	for _, gi := range clipped.FaceCellIdx {
		if gi >= 0 && int(gi) < nGlobal {
			facesPerCell[gi]++
		}
	}
	// Under cell merging, faces are tagged with their group's
	// representative cell, so a non-representative member's own
	// slot is 0 even though its surface was clipped (as part of the
	// group). repOf maps each cell to the representative whose face
	// count reflects the whole group, keeping "zero-face" honest.
	repOf := func(gi int) int {
		if clipped.CellRep != nil && gi < len(clipped.CellRep) {
			return int(clipped.CellRep[gi])
		}
		return gi
	}
	bucketOf := func(px int) int {
		switch {
		case px <= 1:
			return 0
		case px <= 4:
			return 1
		case px <= 16:
			return 2
		case px <= 64:
			return 3
		default:
			return 4
		}
	}
	// [kind][bucket]: [0]=ring [1]=hex
	var totalByBucket, zeroByBucket [2][5]int
	// Per-slab counters: ring/hex × total/zero-face.
	type slabStat struct {
		ringTotal, ringZero int
		hexTotal, hexZero   int
	}
	perSlab := make([]slabStat, len(vo.CellSlabs))
	gi := 0
	for si := range vo.CellSlabs {
		for ci := range vo.CellSlabs[si].Cells {
			c := &vo.CellSlabs[si].Cells[ci]
			k := 0
			if c.Kind == cellslicer.KindHex {
				k = 1
			}
			b := bucketOf(c.Pixels)
			totalByBucket[k][b]++
			zero := facesPerCell[repOf(gi)] == 0
			if zero {
				zeroByBucket[k][b]++
			}
			if k == 0 {
				perSlab[si].ringTotal++
				if zero {
					perSlab[si].ringZero++
				}
			} else {
				perSlab[si].hexTotal++
				if zero {
					perSlab[si].hexZero++
				}
			}
			gi++
		}
	}
	plog.Printf("  Clip cell→face ring: 1px=%d/%d 2-4=%d/%d 5-16=%d/%d 17-64=%d/%d 65+=%d/%d (zero-face/total)",
		zeroByBucket[0][0], totalByBucket[0][0],
		zeroByBucket[0][1], totalByBucket[0][1],
		zeroByBucket[0][2], totalByBucket[0][2],
		zeroByBucket[0][3], totalByBucket[0][3],
		zeroByBucket[0][4], totalByBucket[0][4])
	plog.Printf("  Clip cell→face hex:  1px=%d/%d 2-4=%d/%d 5-16=%d/%d 17-64=%d/%d 65+=%d/%d (zero-face/total)",
		zeroByBucket[1][0], totalByBucket[1][0],
		zeroByBucket[1][1], totalByBucket[1][1],
		zeroByBucket[1][2], totalByBucket[1][2],
		zeroByBucket[1][3], totalByBucket[1][3],
		zeroByBucket[1][4], totalByBucket[1][4])
	// Top 10 slabs by zero-face cell count. Includes Z range so
	// we can correlate with what's at that height in the input
	// (caps vs walls, horizontal trim features, etc.).
	type slabIdxStat struct {
		si    int
		total int
		zero  int
		ring  int // zero-face ring count
		hex   int // zero-face hex count
	}
	ranked := make([]slabIdxStat, 0, len(perSlab))
	for si, s := range perSlab {
		z := s.ringZero + s.hexZero
		if z == 0 {
			continue
		}
		ranked = append(ranked, slabIdxStat{si: si, total: s.ringTotal + s.hexTotal, zero: z, ring: s.ringZero, hex: s.hexZero})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].zero > ranked[j].zero })
	topN := len(ranked)
	if topN > 10 {
		topN = 10
	}
	for k := 0; k < topN; k++ {
		entry := ranked[k]
		s := &vo.CellSlabs[entry.si]
		plog.Printf("    zero-face slab %d Z=[%.2f..%.2f]mm: %d/%d cells (ring=%d hex=%d)",
			entry.si, s.ZBot, s.ZTop, entry.zero, entry.total, entry.ring, entry.hex)
	}

	return &clipOutput{
		ShellVerts:       clipped.Verts,
		ShellFaces:       clipped.Faces,
		ShellAssignments: faceAssign,
		ShellSectionIdx:  clipped.FaceCellIdx,
		ShellHalfIdx:     shellHalfIdx,
	}, nil
}

// clipPerHalf clips each split half's bed-space geometry against its own
// slabs and concatenates (the per-cell clip path). See clipPerHalfWith
// for the shared stitching; this just supplies the per-half clip call.
// mkProg (may be nil) builds a half's progress reporter from its global
// cell range — see the Clip stage caller.
func clipPerHalf(ctx context.Context, so *splitOutput, slabs []cellslicer.Slab, mkProg func(cellOffset, nSub, total int) *cellslicer.ClipProgress) (cellslicer.ClipResult, []byte, error) {
	return clipPerHalfWith(slabs, mkProg, func(h byte, sub []cellslicer.Slab, cellOffset, nSub int, prog *cellslicer.ClipProgress) (cellslicer.ClipResult, error) {
		return cellslicer.ClipMeshToCellsManifoldProgress(ctx, so.Halves[h], sub, prog)
	})
}

// clipPerHalfMerged is the merged-cell counterpart of clipPerHalf,
// clipping connected same-color cells together per slab. cellColor is
// the global per-cell color array (cellAssign), sliced per half by cell
// offset; the resulting CellRep is offset to the global space by
// clipPerHalfWith.
func clipPerHalfMerged(ctx context.Context, so *splitOutput, slabs []cellslicer.Slab, cellColor []int32, mkProg func(cellOffset, nSub, total int) *cellslicer.ClipProgress) (cellslicer.ClipResult, []byte, error) {
	return clipPerHalfWith(slabs, mkProg, func(h byte, sub []cellslicer.Slab, cellOffset, nSub int, prog *cellslicer.ClipProgress) (cellslicer.ClipResult, error) {
		return cellslicer.ClipMeshToMergedCellsManifoldProgress(ctx, so.Halves[h], sub, cellColor[cellOffset:cellOffset+nSub], prog)
	})
}

// clipPerHalfWith clips each split half's bed-space geometry and
// concatenates into one ClipResult — half 0 faces first, a unified
// vertex table (each half's vertex indices offset) — plus a per-face
// HalfIdx array parallel to the faces. It walks slabs in CellSlabs
// order, grouping each contiguous run of one HalfIdx, and calls clipHalf
// for that run with its global cell offset and cell count.
//
// Each half's returned FaceCellIdx is half-local; clipPerHalfWith remaps
// it to the global flattened-CellSlabs index space (the same space
// cellAssign / facesPerCell use) by adding the running cell offset. If a
// half returns a CellRep (the merged clip does), it is merged into a
// global-length CellRep, offsetting both the cell index and the
// representative value it stores. See docs/split-cellslicer.md.
//
// mkProg (may be nil) builds each half's progress reporter from its
// (cellOffset, nSub, totalCells) range; the result is handed to
// clipHalf, which may receive nil.
func clipPerHalfWith(slabs []cellslicer.Slab, mkProg func(cellOffset, nSub, total int) *cellslicer.ClipProgress, clipHalf func(h byte, sub []cellslicer.Slab, cellOffset, nSub int, prog *cellslicer.ClipProgress) (cellslicer.ClipResult, error)) (cellslicer.ClipResult, []byte, error) {
	totalCells := 0
	for i := range slabs {
		totalCells += len(slabs[i].Cells)
	}
	var (
		out     cellslicer.ClipResult
		halfIdx []byte
	)
	cellOffset := 0
	start := 0
	for start < len(slabs) {
		h := slabs[start].HalfIdx
		end := start
		for end < len(slabs) && slabs[end].HalfIdx == h {
			end++
		}
		sub := slabs[start:end]
		nSub := 0
		for i := range sub {
			nSub += len(sub[i].Cells)
		}
		var prog *cellslicer.ClipProgress
		if mkProg != nil {
			prog = mkProg(cellOffset, nSub, totalCells)
		}
		cr, err := clipHalf(h, sub, cellOffset, nSub, prog)
		if err != nil {
			return cellslicer.ClipResult{}, nil, fmt.Errorf("half %d: %w", h, err)
		}
		base := uint32(len(out.Verts))
		out.Verts = append(out.Verts, cr.Verts...)
		for _, f := range cr.Faces {
			out.Faces = append(out.Faces, [3]uint32{f[0] + base, f[1] + base, f[2] + base})
		}
		for _, gi := range cr.FaceCellIdx {
			out.FaceCellIdx = append(out.FaceCellIdx, gi+int32(cellOffset))
		}
		for range cr.Faces {
			halfIdx = append(halfIdx, h)
		}
		if cr.CellRep != nil {
			if out.CellRep == nil {
				out.CellRep = make([]int32, totalCells)
			}
			for i, rep := range cr.CellRep {
				out.CellRep[cellOffset+i] = rep + int32(cellOffset)
			}
		}
		cellOffset += nSub
		start = end
	}
	return out, halfIdx, nil
}

// distinctReps counts the distinct representative indices in a CellRep
// array — i.e. the number of merge groups the cells collapsed into.
func distinctReps(cellRep []int32) int {
	seen := make(map[int32]struct{}, len(cellRep))
	for _, r := range cellRep {
		seen[r] = struct{}{}
	}
	return len(seen)
}

// mostCommonNonNeg returns the most frequent non-negative palette
// index in a, or 0 if the slice has no non-negative entries.
func mostCommonNonNeg(a []int32) int32 {
	counts := map[int32]int{}
	for _, v := range a {
		if v >= 0 {
			counts[v]++
		}
	}
	var best int32
	bestN := -1
	for k, n := range counts {
		if n > bestN {
			best = k
			bestN = n
		}
	}
	return best
}

// runMerge is StageMerge's body (see stageDefs).
func (r *pipelineRun) runMerge() (any, error) {
	co, err := r.Clip()
	if err != nil {
		return nil, err
	}
	shellVerts := co.ShellVerts
	shellFaces := co.ShellFaces
	shellAssignments := co.ShellAssignments
	shellHalfIdx := co.ShellHalfIdx
	shellSectionIdx := co.ShellSectionIdx
	if !r.opts.NoMerge {
		tMerge := time.Now()
		before := len(shellFaces)
		var merr error
		if shellHalfIdx != nil {
			// Per-half merge: halves don't share vertices (clipPerHalf
			// offsets each half's vertex indices), so
			// MergeCoplanarTriangles run on the full mesh would not
			// merge across halves anyway, but the per-face HalfIdx
			// parallel array needs to track the merged face count.
			// Simplest: extract per-half slices, merge each, then
			// concatenate. Faces in clipPerHalf's output are already
			// grouped by half (h=0 then h=1), so the slice ranges
			// are contiguous.
			shellVerts, shellFaces, shellAssignments, shellHalfIdx, merr =
				mergeSplitFaces(r.ctx, shellVerts, shellFaces, shellAssignments, shellHalfIdx, r.tracker)
		} else {
			shellVerts, shellFaces, shellAssignments, merr = voxel.MergeCoplanarTriangles(r.ctx, shellVerts, shellFaces, shellAssignments, r.tracker)
		}
		if merr != nil {
			return nil, fmt.Errorf("merge: %w", merr)
		}
		plog.Printf("  Merged shell: %d -> %d faces in %.1fs", before, len(shellFaces), time.Since(tMerge).Seconds())
		// Merge groups faces by color and re-triangulates;
		// section provenance is no longer per-face.
		shellSectionIdx = nil
	} else {
		progress.BeginStage(r.tracker, stageNames[StageMerge], false, 0).Done()
	}
	plog.Printf("  Output mesh: %s", voxel.CheckWatertight(shellFaces))
	return &mergeOutput{
		ShellVerts:       shellVerts,
		ShellFaces:       shellFaces,
		ShellAssignments: shellAssignments,
		ShellSectionIdx:  shellSectionIdx,
		ShellHalfIdx:     shellHalfIdx,
	}, nil
}

// mergeSplitFaces runs MergeCoplanarTriangles independently on each
// half's contiguous face slice (clipPerHalf groups faces by half), then
// concatenates results and rebuilds the per-face HalfIdx array. Faces
// never reference across halves, so per-half merge is correct.
//
// MergeCoplanarTriangles welds each half to its own compact vertex set
// (representatives chosen among that half's own faces, so the two halves
// never share an index even where positions coincide). We concatenate the
// two welded vertex tables and offset half 1's face indices past half 0.
func mergeSplitFaces(
	ctx context.Context,
	verts [][3]float32,
	faces [][3]uint32,
	assignments []int32,
	halfIdx []byte,
	tracker progress.Tracker,
) ([][3]float32, [][3]uint32, []int32, []byte, error) {
	// Find the boundary between half 0 and half 1.
	boundary := len(faces)
	for i, h := range halfIdx {
		if h == 1 {
			boundary = i
			break
		}
	}
	h0Faces := faces[:boundary]
	h1Faces := faces[boundary:]
	h0Assign := assignments[:boundary]
	h1Assign := assignments[boundary:]

	mergedH0Verts, mergedH0Faces, mergedH0Assign, err := voxel.MergeCoplanarTriangles(ctx, verts, h0Faces, h0Assign, tracker)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("merge half 0: %w", err)
	}
	mergedH1Verts, mergedH1Faces, mergedH1Assign, err := voxel.MergeCoplanarTriangles(ctx, verts, h1Faces, h1Assign, tracker)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("merge half 1: %w", err)
	}

	combinedVerts := make([][3]float32, 0, len(mergedH0Verts)+len(mergedH1Verts))
	combinedVerts = append(combinedVerts, mergedH0Verts...)
	combinedVerts = append(combinedVerts, mergedH1Verts...)
	off := uint32(len(mergedH0Verts))
	combinedFaces := make([][3]uint32, 0, len(mergedH0Faces)+len(mergedH1Faces))
	combinedFaces = append(combinedFaces, mergedH0Faces...)
	for _, f := range mergedH1Faces {
		combinedFaces = append(combinedFaces, [3]uint32{f[0] + off, f[1] + off, f[2] + off})
	}
	combinedAssign := make([]int32, 0, len(mergedH0Assign)+len(mergedH1Assign))
	combinedAssign = append(combinedAssign, mergedH0Assign...)
	combinedAssign = append(combinedAssign, mergedH1Assign...)
	combinedHalfIdx := make([]byte, 0, len(combinedFaces))
	for range mergedH0Faces {
		combinedHalfIdx = append(combinedHalfIdx, 0)
	}
	for range mergedH1Faces {
		combinedHalfIdx = append(combinedHalfIdx, 1)
	}
	return combinedVerts, combinedFaces, combinedAssign, combinedHalfIdx, nil
}
