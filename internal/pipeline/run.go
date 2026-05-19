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

// pipelineRun is a demand-driven driver for one pipeline invocation.
// Each stage is a method that:
//
//  1. Returns memoized output if this Run has already computed it.
//  2. Otherwise asks the cache. If the cache hits (memory or disk),
//     runStageCached emits a UI marker and the body never runs.
//  3. On a cache miss, the body lazily resolves upstream stages by
//     calling r.Upstream(), then computes its own output.
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

	// Per-Run memos: once a stage has been resolved, subsequent
	// consumers within the same Run skip the cache lookup.
	parse       *loader.LoadedModel
	load        *loadOutput
	split       *splitOutput
	decimate    *decimateOutput
	sticker     *stickerOutput
	voxelize    *voxelizeOutput
	colorAdjust *colorAdjustOutput
	colorWarp   *colorWarpOutput
	palette     *paletteOutput
	dither      *ditherOutput
	clip        *clipOutput
	merge       *mergeOutput
}

func (r *pipelineRun) checkCancel() error {
	if r.ctx.Err() != nil {
		return r.ctx.Err()
	}
	return nil
}

// runStage is the shared scaffold for every per-run stage method. The
// per-method boilerplate (memoization slot, body invocation, cache
// set, cache-hit fallback) is identical across stages and varies only
// in the output type, the slot pointer, the StageID, and the body —
// which this helper takes as parameters.
//
// Behavior:
//
//  1. If the slot already holds a value (this Run already produced or
//     decoded it), return immediately.
//  2. Run the cache-aware wrapper. On a cache hit it returns the
//     decoded value, which we stash directly into the slot. On a miss
//     the body produces the value, stores it in the slot, and
//     async-writes the encoded blob to the disk cache.
//
// The slot-then-cache-set ordering is load-bearing: a downstream call
// to the typed getter (e.g. cache.getX) cannot return a value the
// disk-write goroutine hasn't yet flushed. Memoizing into the slot
// before kicking the async write ensures the same Run's downstream
// consumers see the live pointer immediately.
func runStage[T any](
	r *pipelineRun,
	stage StageID,
	slot **T,
	body func() (*T, error),
) (*T, error) {
	if *slot != nil {
		return *slot, nil
	}
	cached, err := runStageCached(r.cache, stage, r.opts, r.tracker, func() error {
		out, err := body()
		if err != nil {
			return err
		}
		// Order is load-bearing: write the slot before kicking
		// the async cache.set. Within-run consumers read the
		// slot via pipelineRun memoization and would race the
		// disk-write goroutine if we set the cache first.
		*slot = out
		r.cache.set(stage, r.opts, out)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if cached != nil {
		// Cache-hit path: stash the wrapper's already-decoded value
		// instead of doing a second cache.get. A second call would
		// race the background disk-cache sweep (kicked at the end of
		// every pipeline run) and could observe the file as deleted,
		// leaving the slot nil and the caller dereferencing it.
		*slot = cached.(*T)
	}
	if *slot == nil {
		// Defensive: succeeded with neither a cache hit nor a body
		// that populated the slot. Should be unreachable; surface
		// loudly rather than return a nil pointer that downstream
		// consumers will dereference.
		return nil, fmt.Errorf("pipeline: stage %s succeeded with no result (cache file vanished?)", stageNames[stage])
	}
	return *slot, nil
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

func (r *pipelineRun) Parse() (*loader.LoadedModel, error) {
	return runStage(r, StageParse, &r.parse, func() (*loader.LoadedModel, error) {
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
	})
}

func (r *pipelineRun) Load() (*loadOutput, error) {
	lo, err := runStage(r, StageLoad, &r.load, func() (*loadOutput, error) {
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

		geomModel := model
		sampleModel := model
		if r.opts.AlphaWrap {
			alpha := r.opts.AlphaWrapAlpha
			if alpha <= 0 {
				alpha = r.opts.NozzleDiameter
			}
			offset := r.opts.AlphaWrapOffset
			if offset <= 0 {
				offset = alpha / 30
			}

			// Pre-wrap decimation: alpha-wrap rebuilds the surface anyway,
			// so feed it a mesh already pruned to voxel resolution.
			// errorBudget bounds geometric drift to ~½ a voxel cell --
			// finer detail than that won't survive voxelization
			// downstream, so it's safe to discard before alpha-wrap.
			// NullTracker avoids colliding with the dedicated
			// StageDecimate event later. Only the alpha-wrap input is
			// decimated -- `model` stays intact for the inflate calc and
			// for ColorModel / SampleModel below.
			wrapInput := model
			if !r.opts.NoSimplify {
				cellSize := voxelCellSizes(r.opts).UpperXY
				budget := decimateErrorBudget(cellSize)
				preDec, derr := voxel.DecimateMesh(r.ctx, model, 1, cellSize, budget, false, progress.NullTracker{})
				if derr != nil {
					return nil, fmt.Errorf("pre-wrap decimate: %w", derr)
				}
				if len(preDec.Faces) < len(model.Faces) {
					plog.Printf("  Pre-wrap decimate: %d faces -> %d faces (cellSize=%.3f mm)",
						len(model.Faces), len(preDec.Faces), cellSize)
					wrapInput = preDec
				}
				if err := r.checkCancel(); err != nil {
					return nil, err
				}
			}

			plog.Printf("  Alpha-wrap: alpha=%.3f mm, offset=%.3f mm starting", alpha, offset)
			tWrap := time.Now()
			wrapped, werr := alphawrap.Wrap(wrapInput, alpha, offset)
			if werr != nil {
				return nil, fmt.Errorf("alpha-wrap: %w", werr)
			}
			plog.Printf("  Alpha-wrap: %d vertices, %d faces in %.1fs",
				len(wrapped.Vertices), len(wrapped.Faces), time.Since(tWrap).Seconds())
			geomModel = wrapped

			// Compute the inflate offset from the wrap envelope before
			// post-decimation runs: post-decimate can nudge the bbox
			// slightly and the inflate amount must reflect what
			// alpha-wrap actually expanded, not the decimated
			// approximation of it. Kept inside the AlphaWrap block so
			// the dependency on `wrapped` is explicit and can't be
			// silently broken by a future refactor.
			origExt := modelMaxExtent(model)
			inflateOffset := (modelMaxExtent(geomModel) - origExt) / 2
			if inflateOffset > 1e-4 {
				plog.Printf("  Inflating color-sample mesh by %.3f mm", inflateOffset)
				sampleModel = loader.InflateAlongNormals(model, inflateOffset)
			}

			// Post-wrap decimation: alpha-wrap output is dense (~one face
			// per α² of surface area), but downstream stages (Sticker,
			// Voxelize, StageDecimate) only need detail at voxel cell
			// resolution. errorBudget caps drift at ½ a cell, so flat
			// regions collapse aggressively while curved silhouettes
			// stop being thinned once cumulative drift would exceed
			// what voxelization can resolve. NullTracker avoids
			// colliding with the dedicated StageDecimate event later.
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
				if err := r.checkCancel(); err != nil {
					return nil, err
				}
			}
		}

		return &loadOutput{
			Model:        geomModel,
			ColorModel:   model,
			SampleModel:  sampleModel,
			InputMesh:    buildInputMeshData(model),
			PreviewScale: unitScale / totalScale,
			ExtentMM:     nativeExtentMM,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	// Apply base-color override on top of the (possibly cached)
	// load output. Cheap and idempotent. On a fresh disk hit
	// (lo.appliedBaseColor=="") this skips the parse cache lookup.
	applyBaseColor(r.cache, lo, r.opts, r.tracker)
	return lo, nil
}

func (r *pipelineRun) Split() (*splitOutput, error) {
	return runStage(r, StageSplit, &r.split, func() (*splitOutput, error) {
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
	})
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

func (r *pipelineRun) Decimate() (*decimateOutput, error) {
	return runStage(r, StageDecimate, &r.decimate, func() (*decimateOutput, error) {
		lo, err := r.Load()
		if err != nil {
			return nil, err
		}
		so, err := r.Split()
		if err != nil {
			return nil, err
		}
		cellSize := voxelCellSizes(r.opts).UpperXY
		budget := decimateErrorBudget(cellSize)

		if so.Enabled {
			// Targets are vestigial under the cost-budget regime --
			// pass 1 (DecimateHalves clamps to a per-half floor of 1)
			// and let `budget` be the actual stopping criterion.
			halves, derr := voxel.DecimateHalves(r.ctx, so.Halves, 1, cellSize, budget, r.opts.NoSimplify, r.tracker)
			if derr != nil {
				return nil, fmt.Errorf("decimate (split): %w", derr)
			}
			return &decimateOutput{Halves: halves}, nil
		}

		decimModel, derr := voxel.DecimateMesh(r.ctx, lo.Model, 1, cellSize, budget, r.opts.NoSimplify, r.tracker)
		if derr != nil {
			return nil, fmt.Errorf("decimate: %w", derr)
		}
		return &decimateOutput{DecimModel: decimModel}, nil
	})
}

func (r *pipelineRun) Sticker() (*stickerOutput, error) {
	return runStage(r, StageSticker, &r.sticker, func() (*stickerOutput, error) {
		lo, err := r.Load()
		if err != nil {
			return nil, err
		}
		return r.computeSticker(lo)
	})
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
func (r *pipelineRun) Voxelize() (*voxelizeOutput, error) {
	return runStage(r, StageVoxelize, &r.voxelize, func() (*voxelizeOutput, error) {
		lo, err := r.Load()
		if err != nil {
			return nil, err
		}
		// Sticker / Split are stubbed during the cellslicer
		// transition; resolve them so their stubs cache, but
		// ignore the (empty) outputs.
		if _, err := r.Sticker(); err != nil {
			return nil, err
		}
		if _, err := r.Split(); err != nil {
			return nil, err
		}

		cellSize := r.opts.NozzleDiameter
		if cellSize <= 0 {
			cellSize = 0.4
		}
		layerH := r.opts.LayerHeight
		if layerH <= 0 {
			layerH = 0.2
		}

		stage := progress.BeginStage(r.tracker, stageNames[StageVoxelize], false, 0)
		defer stage.Done()

		// Slicing operates on the geometry mesh (alpha-wrapped if
		// requested). Color sampling reads from ColorModel. They
		// alias when alpha-wrap is off.
		geomModel := lo.Model
		colorModel := lo.ColorModel

		// Slice once and build the spatial index once — both feed
		// every per-slab worker below. Each slab is then independent,
		// so we fan out NumCPU workers over the slab list and have
		// each one own its own SearchBuf (the only piece of voxel
		// state that isn't safe to share).
		tSlice := time.Now()
		zMin, zMax := modelZRange(geomModel)
		if zMax <= zMin {
			return nil, fmt.Errorf("cellslicer: degenerate Z range")
		}
		planes := cellslicer.SlabBoundaryPlanes(zMin, zMax, layerH)
		layers := cellslicer.SliceMesh(geomModel, planes)
		nSlabs := len(layers) - 1
		if nSlabs < 1 {
			return nil, fmt.Errorf("cellslicer: no slabs produced")
		}
		spatial := voxel.NewSpatialIndex(colorModel, cellSize)
		sliceElapsed := time.Since(tSlice).Seconds()

		nWorkers := runtime.NumCPU()
		if nWorkers < 1 {
			nWorkers = 1
		}
		if nWorkers > nSlabs {
			nWorkers = nSlabs
		}

		// Footprint phase: compute the planar footprint for every
		// slab up front. Used twice each — once for the slab itself
		// and once when its neighbours look at it to decide where
		// caps lie. ComputeFootprint is a small Clipper Union per
		// slab; doing it once cleanly is cheaper than the 3× per-
		// slab recompute pattern that grouping it inline would force.
		tFp := time.Now()
		footprints := make([]*cellslicer.Footprint, nSlabs)
		runParallel(nWorkers, nSlabs, nil, func(i int, _ any) {
			footprints[i] = cellslicer.ComputeFootprint(layers[i].Loops, layers[i+1].Loops)
		})
		fpElapsed := time.Since(tFp).Seconds()

		// Per-slab phase: partition + sample. Each worker writes only
		// its own slabs[i] and perSlabSamples[i] slots, so no locks
		// are needed. We need the per-slab cell counts to compute
		// global offsets before adjacency, so this completes first.
		// partitionNs / sampleNs are atomically-accumulated per-worker
		// busy time, summed to a CPU-time total — handy for spotting
		// which substep dominates the parallel wall time.
		tSlab := time.Now()
		var partitionNs, sampleNs atomic.Int64
		slabs := make([]cellslicer.Slab, nSlabs)
		perSlabSamples := make([][]cellslicer.CellSample, nSlabs)
		runParallel(nWorkers, nSlabs, func(workerID int) any {
			return voxel.NewSearchBuf(len(colorModel.Faces))
		}, func(i int, state any) {
			buf := state.(*voxel.SearchBuf)
			t0 := time.Now()
			// PartitionSlabRaster takes the slab's own footprint plus
			// its neighbours' (or nil at the model's top/bottom). It
			// emits ring cells along the lateral band, hex cells only
			// where the slab's footprint differs from its neighbours
			// (cap surfaces). Wall slabs — by far the common case —
			// produce only ring cells. The dense raster is dropped
			// on return; downstream consumers see polygons.
			var fpBelow, fpAbove *cellslicer.Footprint
			if i > 0 {
				fpBelow = footprints[i-1]
			}
			if i+1 < nSlabs {
				fpAbove = footprints[i+1]
			}
			cells, _ := cellslicer.PartitionSlabRaster(footprints[i], fpBelow, fpAbove, cellSize, 0)
			slabs[i] = cellslicer.Slab{
				Index:     i,
				ZBot:      planes[i],
				ZTop:      planes[i+1],
				BotLayer:  &layers[i],
				TopLayer:  &layers[i+1],
				Footprint: footprints[i],
				Cells:     cells,
			}
			t1 := time.Now()
			partitionNs.Add(int64(t1.Sub(t0)))
			perSlabSamples[i] = cellslicer.SampleSlab(&slabs[i], i, colorModel, spatial, cellSize, 0, nil, nil, buf)
			sampleNs.Add(int64(time.Since(t1)))
		})
		slabElapsed := time.Since(tSlab).Seconds()
		partitionCPU := time.Duration(partitionNs.Load()).Seconds()
		sampleCPU := time.Duration(sampleNs.Load()).Seconds()

		nCells := 0
		for i := range slabs {
			nCells += len(slabs[i].Cells)
		}
		samples := make([]cellslicer.CellSample, 0, nCells)
		for i := range perSlabSamples {
			samples = append(samples, perSlabSamples[i]...)
		}

		// Adjacency phase. Within-slab is fully independent per slab.
		// Cross-slab pair (i,i+1) writes to both slab i and slab i+1's
		// neighbor rows, so neighboring pairs collide on the shared
		// slab; we split pairs into even and odd parities to keep the
		// two phases lock-free.
		tAdj := time.Now()
		globalOffsets := cellslicer.SlabGlobalOffsets(slabs)
		globalNeighbors := make([][]voxel.Neighbor, globalOffsets[nSlabs])
		tWithin := time.Now()
		runParallel(nWorkers, nSlabs, nil, func(i int, _ any) {
			cellslicer.AddWithinSlabAdjacency(&slabs[i], globalOffsets[i], cellSize, 0, globalNeighbors)
		})
		withinElapsed := time.Since(tWithin).Seconds()
		tCross := time.Now()
		for parity := 0; parity < 2; parity++ {
			pairs := make([]int, 0, nSlabs/2+1)
			for i := parity; i < nSlabs-1; i += 2 {
				pairs = append(pairs, i)
			}
			runParallel(nWorkers, len(pairs), nil, func(k int, _ any) {
				i := pairs[k]
				cellslicer.AddCrossSlabAdjacency(&slabs[i], globalOffsets[i], &slabs[i+1], globalOffsets[i+1], globalNeighbors)
			})
		}
		crossElapsed := time.Since(tCross).Seconds()
		adjElapsed := time.Since(tAdj).Seconds()

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

		plog.Printf("  Cellslicer: %d slabs, %d cells (%d visible), %d adj-edges; cellSize=%.3fmm layerH=%.3fmm slice=%.2fs fp=%.2fs slab=%.2fs [partCPU=%.2fs sampCPU=%.2fs] adj=%.2fs [within=%.2fs cross=%.2fs] (workers=%d)",
			len(slabs), nCells, len(cells), nEdges/2,
			cellSize, layerH, sliceElapsed, fpElapsed, slabElapsed, partitionCPU, sampleCPU, adjElapsed, withinElapsed, crossElapsed, nWorkers)

		return &voxelizeOutput{
			Cells:         cells,
			CellSlabs:     slabs,
			CellSamples:   samples,
			Neighbors:     visibleNeighbors,
			VisibleToCell: visibleToCell,
			LayerH:        layerH,
			CellSize:      cellSize,
		}, nil
	})
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

func (r *pipelineRun) ColorAdjust() (*colorAdjustOutput, error) {
	return runStage(r, StageColorAdjust, &r.colorAdjust, func() (*colorAdjustOutput, error) {
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
	})
}

func (r *pipelineRun) ColorWarp() (*colorWarpOutput, error) {
	return runStage(r, StageColorWarp, &r.colorWarp, func() (*colorWarpOutput, error) {
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
	})
}

func (r *pipelineRun) Palette() (*paletteOutput, error) {
	return runStage(r, StagePalette, &r.palette, func() (*paletteOutput, error) {
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
	})
}

func (r *pipelineRun) Dither() (*ditherOutput, error) {
	return runStage(r, StageDither, &r.dither, func() (*ditherOutput, error) {
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
	})
}

// Clip cuts the geometry mesh into per-cell fragments via
// cellslicer.ClipMeshToCells2D (pure Go, per-slab parallel; see
// clip2d.go). Each output face is tagged with the dithered palette
// index of its source cell; faces from cells with no dither
// assignment fall back to the mesh's most-common palette index.
// The geometry mesh must be closed and orientable — the alpha-wrap
// path produces this directly; for raw meshes the pipeline relies
// on opts.AlphaWrap.
func (r *pipelineRun) Clip() (*clipOutput, error) {
	return runStage(r, StageClip, &r.clip, func() (*clipOutput, error) {
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
		// Decimate + Split are stubbed during the cellslicer
		// transition; keep the calls so their stubs cache.
		if _, err := r.Decimate(); err != nil {
			return nil, err
		}
		if _, err := r.Split(); err != nil {
			return nil, err
		}

		stage := progress.BeginStage(r.tracker, stageNames[StageClip], false, 0)
		defer stage.Done()
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

		triIdx := cellslicer.NewTriXYZIndex(lo.Model, vo.CellSize*2)
		clipped, cerr := cellslicer.ClipMeshToCells2D(lo.Model, vo.CellSlabs, triIdx)
		if cerr != nil {
			return nil, fmt.Errorf("cellslicer clip: %w", cerr)
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

		return &clipOutput{
			ShellVerts:       clipped.Verts,
			ShellFaces:       clipped.Faces,
			ShellAssignments: faceAssign,
			ShellSectionIdx:  clipped.FaceCellIdx,
		}, nil
	})
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

func (r *pipelineRun) Merge() (*mergeOutput, error) {
	return runStage(r, StageMerge, &r.merge, func() (*mergeOutput, error) {
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
				// Per-half merge: halves don't share vertices (clipSplit
				// offsets each half's vertex indices), so
				// MergeCoplanarTriangles run on the full mesh would not
				// merge across halves anyway, but the per-face HalfIdx
				// parallel array needs to track the merged face count.
				// Simplest: extract per-half slices, merge each, then
				// concatenate. Faces in clipSplit's output are already
				// grouped by half (h=0 then h=1), so the slice ranges
				// are contiguous.
				shellFaces, shellAssignments, shellHalfIdx, merr =
					mergeSplitFaces(r.ctx, shellVerts, shellFaces, shellAssignments, shellHalfIdx, r.tracker)
			} else {
				shellFaces, shellAssignments, merr = voxel.MergeCoplanarTriangles(r.ctx, shellVerts, shellFaces, shellAssignments, r.tracker)
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
	})
}

// mergeSplitFaces runs MergeCoplanarTriangles independently on each
// half's contiguous face slice (clipSplit groups faces by half), then
// concatenates results and rebuilds the per-face HalfIdx array.
// Vertices are shared across halves by index space (clipSplit emits a
// unified vertex table with offsets), but faces never reference
// across halves, so per-half merge is correct.
func mergeSplitFaces(
	ctx context.Context,
	verts [][3]float32,
	faces [][3]uint32,
	assignments []int32,
	halfIdx []byte,
	tracker progress.Tracker,
) ([][3]uint32, []int32, []byte, error) {
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

	mergedH0Faces, mergedH0Assign, err := voxel.MergeCoplanarTriangles(ctx, verts, h0Faces, h0Assign, tracker)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("merge half 0: %w", err)
	}
	mergedH1Faces, mergedH1Assign, err := voxel.MergeCoplanarTriangles(ctx, verts, h1Faces, h1Assign, tracker)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("merge half 1: %w", err)
	}

	combinedFaces := append(mergedH0Faces, mergedH1Faces...)
	combinedAssign := append(mergedH0Assign, mergedH1Assign...)
	combinedHalfIdx := make([]byte, 0, len(combinedFaces))
	for range mergedH0Faces {
		combinedHalfIdx = append(combinedHalfIdx, 0)
	}
	for range mergedH1Faces {
		combinedHalfIdx = append(combinedHalfIdx, 1)
	}
	return combinedFaces, combinedAssign, combinedHalfIdx, nil
}

