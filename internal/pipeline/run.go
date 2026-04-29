package pipeline

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rtwfroody/ditherforge/internal/alphawrap"
	"github.com/rtwfroody/ditherforge/internal/export3mf"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/squarevoxel"
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
	onWarning func(string)

	// Per-Run memos: once a stage has been resolved, subsequent
	// consumers within the same Run skip the cache lookup.
	parse       *loader.LoadedModel
	load        *loadOutput
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
//  2. Run the cache-aware wrapper. On a cache hit the body is skipped
//     and the slot stays nil; on a miss, the body produces the value,
//     stores it in the slot, and async-writes the encoded blob to the
//     disk cache.
//  3. If the slot is still nil after the wrapper, the cache-hit path
//     ran — decode from the cache to populate the slot.
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
	err := runStageCached(r.cache, stage, r.opts, r.tracker, func() error {
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
	if *slot == nil {
		if v := r.cache.get(stage, r.opts); v != nil {
			*slot = v.(*T)
		}
	}
	return *slot, nil
}

// ----- Stage methods -----

func (r *pipelineRun) Parse() (*loader.LoadedModel, error) {
	return runStage(r, StageParse, &r.parse, func() (*loader.LoadedModel, error) {
		stage := progress.BeginStage(r.tracker, stageNames[StageParse], false, 0)
		defer stage.Done()
		fmt.Printf("Parsing %s...", r.opts.Input)
		t := time.Now()
		loaded, err := loadModel(r.opts.Input, r.opts.ObjectIndex)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", filepath.Ext(r.opts.Input), err)
		}
		fmt.Printf(" %d vertices, %d faces in %.1fs\n",
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
		fmt.Printf("  Extent: %.1f x %.1f x %.1f mm\n", ex[0], ex[1], ex[2])

		if err := r.checkCancel(); err != nil {
			return nil, err
		}
		nativeExtentMM := modelMaxExtent(model) * unitScale / totalScale

		geomModel := model
		if r.opts.AlphaWrap {
			alpha := r.opts.AlphaWrapAlpha
			if alpha <= 0 {
				alpha = r.opts.NozzleDiameter
			}
			offset := r.opts.AlphaWrapOffset
			if offset <= 0 {
				offset = alpha / 30
			}
			fmt.Printf("  Alpha-wrap: alpha=%.3f mm, offset=%.3f mm...", alpha, offset)
			tWrap := time.Now()
			wrapped, werr := alphawrap.Wrap(model, alpha, offset)
			if werr != nil {
				return nil, fmt.Errorf("alpha-wrap: %w", werr)
			}
			fmt.Printf(" %d vertices, %d faces in %.1fs\n",
				len(wrapped.Vertices), len(wrapped.Faces), time.Since(tWrap).Seconds())
			geomModel = wrapped
		}

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
	applyBaseColor(r.cache, lo, r.opts)
	return lo, nil
}

func (r *pipelineRun) Decimate() (*decimateOutput, error) {
	return runStage(r, StageDecimate, &r.decimate, func() (*decimateOutput, error) {
		lo, err := r.Load()
		if err != nil {
			return nil, err
		}
		fmt.Println("Decimating...")
		cellSize := r.opts.NozzleDiameter * squarevoxel.UpperCellScale
		targetCells := squarevoxel.CountSurfaceCells(r.ctx, lo.Model, r.opts.NozzleDiameter, r.opts.LayerHeight)
		decimModel, derr := squarevoxel.DecimateMesh(r.ctx, lo.Model, targetCells, cellSize, r.opts.NoSimplify, r.tracker)
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
				fmt.Printf("  Sticker %s: no front-facing geometry within projection rect, skipping\n", s.ImagePath)
				stage.Progress(base + stickerUnits)
				continue
			}
		default:
			return nil, fmt.Errorf("sticker %s: unknown mode %q", s.ImagePath, s.Mode)
		}
		fmt.Printf("  Sticker %s: %d triangles covered\n", s.ImagePath, len(decal.TriUVs))
		if decal.LSCMResidual > 1e-5 && r.onWarning != nil {
			r.onWarning(fmt.Sprintf(
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

func (r *pipelineRun) Voxelize() (*voxelizeOutput, error) {
	return runStage(r, StageVoxelize, &r.voxelize, func() (*voxelizeOutput, error) {
		lo, err := r.Load()
		if err != nil {
			return nil, err
		}
		so, err := r.Sticker()
		if err != nil {
			return nil, err
		}
		layer0Size := r.opts.NozzleDiameter * squarevoxel.Layer0CellScale
		upperSize := r.opts.NozzleDiameter * squarevoxel.UpperCellScale
		layerH := r.opts.LayerHeight

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
		result, verr := squarevoxel.VoxelizeTwoGrids(r.ctx, lo.Model, sampleModel,
			stickerModel, stickerSI,
			layer0Size, upperSize, layerH, r.tracker, so.Decals, nil)
		if verr != nil {
			return nil, fmt.Errorf("voxelize: %w", verr)
		}
		return &voxelizeOutput{
			Cells:         result.Cells,
			CellAssignMap: result.CellAssignMap,
			MinV:          result.MinV,
			Layer0Size:    layer0Size,
			UpperSize:     upperSize,
			LayerH:        layerH,
		}, nil
	})
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
			fmt.Printf("  Adjusted colors (B:%+.0f C:%+.0f S:%+.0f) in %.1fs\n",
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
		fmt.Printf("  Warped colors (%d pins) in %.1fs\n", len(pins), time.Since(tWarp).Seconds())
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
			fmt.Printf("%s\n", palDisplay)
		}
		if len(pal) == 0 {
			return nil, fmt.Errorf("no palette colors")
		}
		if r.opts.ColorSnap > 0 {
			if serr := voxel.SnapColors(r.ctx, cells, pal, r.opts.ColorSnap); serr != nil {
				return nil, serr
			}
			fmt.Printf("  Snapped cell colors toward palette by delta E %.1f\n", r.opts.ColorSnap)
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
		stage := progress.BeginStage(r.tracker, stageNames[StageDither], true, 2*len(po.Cells))
		defer stage.Done()
		ditherMode := r.opts.Dither
		cells := po.Cells
		pal := po.Palette
		tDither := time.Now()
		var assignments []int32
		var derr error
		switch ditherMode {
		case "dizzy":
			neighbors := vo.getNeighbors()
			assignments, derr = voxel.DitherWithNeighbors(r.ctx, cells, pal, neighbors, r.tracker)
		default:
			assignments, derr = voxel.AssignColors(r.ctx, cells, pal)
		}
		if derr != nil {
			return nil, derr
		}
		fmt.Printf("  Dithered (%s) %d cells in %.1fs\n", ditherMode, len(cells), time.Since(tDither).Seconds())
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
		tFlood := time.Now()
		patchMap, numPatches, ferr := floodFillTwoGrids(r.ctx, cells, assignments, r.tracker)
		if ferr != nil {
			return nil, ferr
		}
		fmt.Printf("  Flood fill: %d patches in %.1fs\n", numPatches, time.Since(tFlood).Seconds())
		patchAssignment := make([]int32, numPatches)
		for i, c := range cells {
			k := voxel.CellKey{Grid: c.Grid, Col: c.Col, Row: c.Row, Layer: c.Layer}
			pid := patchMap[k]
			patchAssignment[pid] = assignments[i]
		}
		return &ditherOutput{
			Assignments:     assignments,
			PatchMap:        patchMap,
			NumPatches:      numPatches,
			PatchAssignment: patchAssignment,
		}, nil
	})
}

func (r *pipelineRun) Clip() (*clipOutput, error) {
	return runStage(r, StageClip, &r.clip, func() (*clipOutput, error) {
		do, err := r.Dither()
		if err != nil {
			return nil, err
		}
		deco, err := r.Decimate()
		if err != nil {
			return nil, err
		}
		vo, err := r.Voxelize()
		if err != nil {
			return nil, err
		}
		tClip := time.Now()
		cfg := voxel.TwoGridConfig{
			MinV:       vo.MinV,
			Layer0Size: vo.Layer0Size,
			UpperSize:  vo.UpperSize,
			LayerH:     vo.LayerH,
			SeamZ:      vo.MinV[2] + 0.5*vo.LayerH,
		}
		shellVerts, shellFaces, shellAssignments, cerr := voxel.ClipMeshByPatchesTwoGrid(
			r.ctx, deco.DecimModel, do.PatchMap, do.PatchAssignment, cfg, r.tracker)
		if cerr != nil {
			return nil, fmt.Errorf("clip: %w", cerr)
		}
		fmt.Printf("  Clipped mesh: %d faces in %.1fs\n", len(shellFaces), time.Since(tClip).Seconds())
		fmt.Printf("  After clip: %s\n", voxel.CheckWatertight(shellFaces))
		return &clipOutput{
			ShellVerts:       shellVerts,
			ShellFaces:       shellFaces,
			ShellAssignments: shellAssignments,
		}, nil
	})
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
		if !r.opts.NoMerge {
			tMerge := time.Now()
			before := len(shellFaces)
			var merr error
			shellFaces, shellAssignments, merr = voxel.MergeCoplanarTriangles(r.ctx, shellVerts, shellFaces, shellAssignments, r.tracker)
			if merr != nil {
				return nil, fmt.Errorf("merge: %w", merr)
			}
			fmt.Printf("  Merged shell: %d -> %d faces in %.1fs\n", before, len(shellFaces), time.Since(tMerge).Seconds())
		} else {
			progress.BeginStage(r.tracker, stageNames[StageMerge], false, 0).Done()
		}
		fmt.Printf("  Output mesh: %s\n", voxel.CheckWatertight(shellFaces))
		return &mergeOutput{
			ShellVerts:       shellVerts,
			ShellFaces:       shellFaces,
			ShellAssignments: shellAssignments,
		}, nil
	})
}

