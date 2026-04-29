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

// ----- Stage methods -----

func (r *pipelineRun) Parse() (*loader.LoadedModel, error) {
	if r.parse != nil {
		return r.parse, nil
	}
	err := runStageCached(r.cache, StageParse, r.opts, r.tracker, func() error {
		stage := progress.BeginStage(r.tracker, stageNames[StageParse], false, 0)
		defer stage.Done()
		fmt.Printf("Parsing %s...", r.opts.Input)
		t := time.Now()
		loaded, err := loadModel(r.opts.Input, r.opts.ObjectIndex)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", filepath.Ext(r.opts.Input), err)
		}
		fmt.Printf(" %d vertices, %d faces in %.1fs\n",
			len(loaded.Vertices), len(loaded.Faces), time.Since(t).Seconds())
		r.cache.setParse(r.opts, loaded)
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.parse = r.cache.getParse(r.opts)
	return r.parse, nil
}

func (r *pipelineRun) Load() (*loadOutput, error) {
	if r.load != nil {
		return r.load, nil
	}
	err := runStageCached(r.cache, StageLoad, r.opts, r.tracker, func() error {
		label := stageNames[StageLoad]
		if r.opts.AlphaWrap {
			label += " (including alpha-wrap)"
		}
		stage := progress.BeginStage(r.tracker, label, false, 0)
		defer stage.Done()

		raw, err := r.Parse()
		if err != nil {
			return err
		}
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
			return err
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
				return fmt.Errorf("alpha-wrap: %w", werr)
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

		r.cache.setLoad(r.opts, &loadOutput{
			Model:        geomModel,
			ColorModel:   model,
			SampleModel:  sampleModel,
			InputMesh:    buildInputMeshData(model),
			PreviewScale: unitScale / totalScale,
			ExtentMM:     nativeExtentMM,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.load = r.cache.getLoad(r.opts)
	// Apply base-color override on top of the (possibly cached)
	// load output. Cheap and idempotent. On a fresh disk hit
	// (lo.appliedBaseColor=="") this skips the parse cache lookup.
	applyBaseColor(r.cache, r.load, r.opts)
	return r.load, nil
}

func (r *pipelineRun) Decimate() (*decimateOutput, error) {
	if r.decimate != nil {
		return r.decimate, nil
	}
	err := runStageCached(r.cache, StageDecimate, r.opts, r.tracker, func() error {
		lo, err := r.Load()
		if err != nil {
			return err
		}
		fmt.Println("Decimating...")
		cellSize := r.opts.NozzleDiameter * squarevoxel.UpperCellScale
		targetCells := squarevoxel.CountSurfaceCells(r.ctx, lo.Model, r.opts.NozzleDiameter, r.opts.LayerHeight)
		decimModel, derr := squarevoxel.DecimateMesh(r.ctx, lo.Model, targetCells, cellSize, r.opts.NoSimplify, r.tracker)
		if derr != nil {
			return fmt.Errorf("decimate: %w", derr)
		}
		r.cache.setDecimate(r.opts, &decimateOutput{DecimModel: decimModel})
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.decimate = r.cache.getDecimate(r.opts)
	return r.decimate, nil
}

func (r *pipelineRun) Sticker() (*stickerOutput, error) {
	if r.sticker != nil {
		return r.sticker, nil
	}
	err := runStageCached(r.cache, StageSticker, r.opts, r.tracker, func() error {
		lo, err := r.Load()
		if err != nil {
			return err
		}
		return r.computeSticker(lo)
	})
	if err != nil {
		return nil, err
	}
	r.sticker = r.cache.getSticker(r.opts)
	return r.sticker, nil
}

func (r *pipelineRun) computeSticker(lo *loadOutput) error {
	if len(r.opts.Stickers) == 0 {
		progress.BeginStage(r.tracker, stageNames[StageSticker], false, 0).Done()
		r.cache.setSticker(r.opts, &stickerOutput{})
		return nil
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
			decal, err = voxel.BuildStickerDecal(r.ctx, model, adj, img,
				seedTri, s.Center, s.Normal, s.Up, s.Scale, s.Rotation, s.MaxAngle,
				onProgress)
			if err != nil {
				return err
			}
		case "projection":
			decal, err = voxel.BuildStickerDecalProjection(r.ctx, model, img,
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
	r.cache.setSticker(r.opts, so)
	return nil
}

func (r *pipelineRun) Voxelize() (*voxelizeOutput, error) {
	if r.voxelize != nil {
		return r.voxelize, nil
	}
	err := runStageCached(r.cache, StageVoxelize, r.opts, r.tracker, func() error {
		lo, err := r.Load()
		if err != nil {
			return err
		}
		so, err := r.Sticker()
		if err != nil {
			return err
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
			layer0Size, upperSize, layerH, r.tracker, so.Decals)
		if verr != nil {
			return fmt.Errorf("voxelize: %w", verr)
		}
		r.cache.setVoxelize(r.opts, &voxelizeOutput{
			Cells:         result.Cells,
			CellAssignMap: result.CellAssignMap,
			MinV:          result.MinV,
			Layer0Size:    layer0Size,
			UpperSize:     upperSize,
			LayerH:        layerH,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.voxelize = r.cache.getVoxelize(r.opts)
	return r.voxelize, nil
}

func (r *pipelineRun) ColorAdjust() (*colorAdjustOutput, error) {
	if r.colorAdjust != nil {
		return r.colorAdjust, nil
	}
	err := runStageCached(r.cache, StageColorAdjust, r.opts, r.tracker, func() error {
		stage := progress.BeginStage(r.tracker, stageNames[StageColorAdjust], false, 0)
		defer stage.Done()
		vo, err := r.Voxelize()
		if err != nil {
			return err
		}
		adj := voxel.ColorAdjustment{
			Brightness: r.opts.Brightness,
			Contrast:   r.opts.Contrast,
			Saturation: r.opts.Saturation,
		}
		tAdj := time.Now()
		cells, cerr := voxel.AdjustCellColors(r.ctx, vo.Cells, adj)
		if cerr != nil {
			return cerr
		}
		if !adj.IsIdentity() {
			fmt.Printf("  Adjusted colors (B:%+.0f C:%+.0f S:%+.0f) in %.1fs\n",
				r.opts.Brightness, r.opts.Contrast, r.opts.Saturation, time.Since(tAdj).Seconds())
		}
		r.cache.setColorAdjust(r.opts, &colorAdjustOutput{Cells: cells})
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.colorAdjust = r.cache.getColorAdjust(r.opts)
	return r.colorAdjust, nil
}

func (r *pipelineRun) ColorWarp() (*colorWarpOutput, error) {
	if r.colorWarp != nil {
		return r.colorWarp, nil
	}
	err := runStageCached(r.cache, StageColorWarp, r.opts, r.tracker, func() error {
		stage := progress.BeginStage(r.tracker, stageNames[StageColorWarp], false, 0)
		defer stage.Done()
		cao, err := r.ColorAdjust()
		if err != nil {
			return err
		}
		if len(r.opts.WarpPins) == 0 {
			out := make([]voxel.ActiveCell, len(cao.Cells))
			copy(out, cao.Cells)
			r.cache.setColorWarp(r.opts, &colorWarpOutput{Cells: out})
			return nil
		}
		pins := make([]voxel.ColorWarpPin, len(r.opts.WarpPins))
		for i, p := range r.opts.WarpPins {
			src, perr := palette.ParsePalette([]string{p.SourceHex})
			if perr != nil {
				return fmt.Errorf("warp pin %d source: %w", i, perr)
			}
			tgt, perr := palette.ParsePalette([]string{p.TargetHex})
			if perr != nil {
				return fmt.Errorf("warp pin %d target: %w", i, perr)
			}
			pins[i] = voxel.ColorWarpPin{Source: src[0], Target: tgt[0], Sigma: p.Sigma}
		}
		tWarp := time.Now()
		cells, werr := voxel.WarpCellColors(r.ctx, cao.Cells, pins)
		if werr != nil {
			return werr
		}
		fmt.Printf("  Warped colors (%d pins) in %.1fs\n", len(pins), time.Since(tWarp).Seconds())
		r.cache.setColorWarp(r.opts, &colorWarpOutput{Cells: cells})
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.colorWarp = r.cache.getColorWarp(r.opts)
	return r.colorWarp, nil
}

func (r *pipelineRun) Palette() (*paletteOutput, error) {
	if r.palette != nil {
		return r.palette, nil
	}
	err := runStageCached(r.cache, StagePalette, r.opts, r.tracker, func() error {
		stage := progress.BeginStage(r.tracker, stageNames[StagePalette], false, 0)
		defer stage.Done()

		cwo, err := r.ColorWarp()
		if err != nil {
			return err
		}
		pcfg, perr := buildPaletteConfig(r.opts)
		if perr != nil {
			return perr
		}
		if pcfg.NumColors > export3mf.MaxFilaments {
			return fmt.Errorf("palette has %d colors but max supported is %d", pcfg.NumColors, export3mf.MaxFilaments)
		}
		cells := make([]voxel.ActiveCell, len(cwo.Cells))
		copy(cells, cwo.Cells)
		ditherMode := r.opts.Dither
		pal, palLabels, palDisplay, perr := voxel.ResolvePalette(r.ctx, cells, pcfg, ditherMode != "none", r.tracker)
		if perr != nil {
			return perr
		}
		if palDisplay != "" {
			fmt.Printf("%s\n", palDisplay)
		}
		if len(pal) == 0 {
			return fmt.Errorf("no palette colors")
		}
		if r.opts.ColorSnap > 0 {
			if serr := voxel.SnapColors(r.ctx, cells, pal, r.opts.ColorSnap); serr != nil {
				return serr
			}
			fmt.Printf("  Snapped cell colors toward palette by delta E %.1f\n", r.opts.ColorSnap)
		}
		if len(pcfg.Locked) == 0 && len(pal) > 1 {
			assigns, aerr := voxel.AssignColors(r.ctx, cells, pal)
			if aerr != nil {
				return aerr
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
		r.cache.setPalette(r.opts, &paletteOutput{
			Palette:       pal,
			PaletteLabels: palLabels,
			Cells:         cells,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.palette = r.cache.getPalette(r.opts)
	return r.palette, nil
}

func (r *pipelineRun) Dither() (*ditherOutput, error) {
	if r.dither != nil {
		return r.dither, nil
	}
	err := runStageCached(r.cache, StageDither, r.opts, r.tracker, func() error {
		po, err := r.Palette()
		if err != nil {
			return err
		}
		vo, err := r.Voxelize()
		if err != nil {
			return err
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
			return derr
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
			return ferr
		}
		fmt.Printf("  Flood fill: %d patches in %.1fs\n", numPatches, time.Since(tFlood).Seconds())
		patchAssignment := make([]int32, numPatches)
		for i, c := range cells {
			k := voxel.CellKey{Grid: c.Grid, Col: c.Col, Row: c.Row, Layer: c.Layer}
			pid := patchMap[k]
			patchAssignment[pid] = assignments[i]
		}
		r.cache.setDither(r.opts, &ditherOutput{
			Assignments:     assignments,
			PatchMap:        patchMap,
			NumPatches:      numPatches,
			PatchAssignment: patchAssignment,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.dither = r.cache.getDither(r.opts)
	return r.dither, nil
}

func (r *pipelineRun) Clip() (*clipOutput, error) {
	if r.clip != nil {
		return r.clip, nil
	}
	err := runStageCached(r.cache, StageClip, r.opts, r.tracker, func() error {
		do, err := r.Dither()
		if err != nil {
			return err
		}
		deco, err := r.Decimate()
		if err != nil {
			return err
		}
		vo, err := r.Voxelize()
		if err != nil {
			return err
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
			return fmt.Errorf("clip: %w", cerr)
		}
		fmt.Printf("  Clipped mesh: %d faces in %.1fs\n", len(shellFaces), time.Since(tClip).Seconds())
		fmt.Printf("  After clip: %s\n", voxel.CheckWatertight(shellFaces))
		r.cache.setClip(r.opts, &clipOutput{
			ShellVerts:       shellVerts,
			ShellFaces:       shellFaces,
			ShellAssignments: shellAssignments,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.clip = r.cache.getClip(r.opts)
	return r.clip, nil
}

func (r *pipelineRun) Merge() (*mergeOutput, error) {
	if r.merge != nil {
		return r.merge, nil
	}
	err := runStageCached(r.cache, StageMerge, r.opts, r.tracker, func() error {
		co, err := r.Clip()
		if err != nil {
			return err
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
				return fmt.Errorf("merge: %w", merr)
			}
			fmt.Printf("  Merged shell: %d -> %d faces in %.1fs\n", before, len(shellFaces), time.Since(tMerge).Seconds())
		} else {
			progress.BeginStage(r.tracker, stageNames[StageMerge], false, 0).Done()
		}
		fmt.Printf("  Output mesh: %s\n", voxel.CheckWatertight(shellFaces))
		r.cache.setMerge(r.opts, &mergeOutput{
			ShellVerts:       shellVerts,
			ShellFaces:       shellFaces,
			ShellAssignments: shellAssignments,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.merge = r.cache.getMerge(r.opts)
	return r.merge, nil
}

