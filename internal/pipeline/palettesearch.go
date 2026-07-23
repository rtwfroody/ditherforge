package pipeline

// Ground-truth palette-search harness. For one model + settings it resolves
// the pipeline once up through Voxelize (cells held in memory), then for every
// candidate filament palette it dithers the cells, computes the TD-simulated
// per-cell appearance, renders directly from the cells (no Clip/Merge), and
// scores a multi-scale Gaussian-blur Lab ΔE against a palette-independent
// sampled-target render. The result is a ranked table plus a "regret"
// comparison against the production fast scorer's pick — the calibration
// ground truth for improving that scorer.
//
// This lives in package pipeline because the voxelizeOutput cell container is
// unexported. Rendering is injected (SplatRenderFunc) rather than called
// directly so the package need not import debugrender, which imports pipeline.

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/voxel"
	"github.com/rtwfroody/ditherforge/tests/percep"
)

// PaletteSearchViews is the default ordered set of view names the harness
// scores and renders. The caller's SplatRenderFunc maps each name to a camera.
var PaletteSearchViews = []string{"front", "side", "top", "persp"}

// SplatRenderFunc renders a splat MeshData for one named view to an sRGB
// image. Every candidate (and the target) is handed geometry that is
// bit-identical except for FaceColors, so an implementation that derives its
// framing from the mesh bounds automatically frames all renders identically.
// It must be safe to call from multiple goroutines. Injected by the caller so
// package pipeline need not import debugrender (an import cycle).
type SplatRenderFunc func(mesh *MeshData, viewName string, res int) *image.RGBA

// PaletteSearchConfig tunes one RunPaletteSearch invocation.
type PaletteSearchConfig struct {
	Res        int              // square render resolution (default 512)
	Sigmas     []float64        // blur σ (px) to score at (default 0,2,4,8,16)
	RankSigmas []float64        // σ subset averaged into the rank key (default 2,8)
	Workers    int              // candidate worker pool size (default GOMAXPROCS)
	Limit      int              // evaluate only the first N candidates (0 = all)
	Views      []string         // subset of PaletteSearchViews (nil = all)
	RenderTop  int              // write PNGs for the top-N candidates (default 3)
	OutDir     string           // output directory for CSV/JSON/PNG (default ".")
	Tracker    progress.Tracker // progress sink for the one-time voxelize (nil = null)

	// Inventory, when non-nil, overrides the inventory resolved from the
	// settings (the CLI's --inventory flag). Locked colors are still taken
	// from the settings.
	Inventory []palette.InventoryEntry
}

// PaletteCandidate is one evaluated palette's scores. The Labels/Hexes cover
// only the free (non-locked) slots, in the palette order they were appended.
type PaletteCandidate struct {
	Labels        []string  `json:"labels"`
	Hexes         []string  `json:"hexes"`
	ScoresBySigma []float64 `json:"scoresBySigma"` // parallel to result.Sigmas
	P99Sigma2     float64   `json:"p99Sigma2"`
	RankKey       float64   `json:"rankKey"`
	Rank          int       `json:"rank"` // 1-based, assigned after ranking
}

// PaletteSearchResult is the full ranked outcome.
type PaletteSearchResult struct {
	Sigmas              []float64          `json:"sigmas"`
	RankSigmas          []float64          `json:"rankSigmas"`
	LockedHexes         []string           `json:"lockedHexes"`
	Candidates          []PaletteCandidate `json:"candidates"` // ranked ascending by RankKey
	Production          *PaletteCandidate  `json:"production"` // the fast scorer's pick, Rank set
	ProductionInSweep   bool               `json:"productionInSweep"`
	NumCells            int                `json:"numCells"`
	NumCandidates       int                `json:"numCandidates"`
	SecondsPerCandidate float64            `json:"secondsPerCandidate"`
}

// RunPaletteSearch executes the ground-truth sweep. render is required.
func RunPaletteSearch(ctx context.Context, cache *StageCache, opts Options, cfg PaletteSearchConfig, render SplatRenderFunc) (*PaletteSearchResult, error) {
	if render == nil {
		return nil, fmt.Errorf("palettesearch: render function is required")
	}

	// Resolve the pipeline once, up to Voxelize only, mirroring RunCached's
	// prologue (fractional-option resolution) but stopping short of Palette.
	tracker := cfg.Tracker
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	mon := progress.NewMonitor(tracker)
	defer mon.Stop()
	r := &pipelineRun{ctx: ctx, cache: cache, opts: opts, tracker: mon}
	if err := r.resolveFractionalOptions(); err != nil {
		return nil, err
	}
	vo, err := r.Voxelize()
	if err != nil {
		return nil, err
	}
	so, err := r.Split()
	if err != nil {
		return nil, err
	}
	return runSearchOnCells(ctx, opts, cfg, vo, so, render)
}

// runSearchOnCells is the palette-search core, factored out of the pipeline
// prologue so it can be exercised hermetically against a synthetic
// voxelizeOutput fixture (see palettesearch_test.go). It validates the config,
// then enumerates, scores and ranks every candidate and writes the outputs.
func runSearchOnCells(ctx context.Context, opts Options, cfg PaletteSearchConfig, vo *voxelizeOutput, so *splitOutput, render SplatRenderFunc) (*PaletteSearchResult, error) {
	cfg = cfg.withDefaults()

	// Unsupported-for-v1 settings that would invalidate the direct-from-cells
	// scoring model. Fail loudly rather than silently mis-scoring.
	if !validDitherMode(opts.Dither) {
		return nil, fmt.Errorf("palettesearch: invalid --dither %q: must be one of %s", opts.Dither, strings.Join(ditherModes, ", "))
	}
	if opts.ColorSnap != 0 {
		return nil, fmt.Errorf("palettesearch: ColorSnap != 0 is unsupported in v1 (it mutates cell colors)")
	}
	if opts.HonorTD && opts.TDModel == "layered" {
		return nil, fmt.Errorf("palettesearch: tdModel=%q is unsupported in v1; use the default area TD model", opts.TDModel)
	}
	for _, v := range cfg.Views {
		if !containsStr(PaletteSearchViews, v) {
			return nil, fmt.Errorf("palettesearch: unknown view %q (valid: %s)", v, strings.Join(PaletteSearchViews, ", "))
		}
	}
	if len(vo.Cells) == 0 {
		return nil, fmt.Errorf("palettesearch: model produced no visible cells")
	}

	// Palette config: locked slots + TD context + inventory. cfg.Inventory
	// overrides the settings-derived inventory when set.
	pcfg, err := buildPaletteConfig(opts)
	if err != nil {
		return nil, err
	}
	if cfg.Inventory != nil {
		pcfg.Inventory = cfg.Inventory
	}
	freeSlots := pcfg.NumColors - len(pcfg.Locked)
	if freeSlots <= 0 {
		return nil, fmt.Errorf("palettesearch: %d colors requested but %d are locked; no free slots to search", pcfg.NumColors, len(pcfg.Locked))
	}

	// Locked-filtered inventory (mirror voxel.filterInventory) drives the
	// production fast scorer; a color-deduplicated, sorted copy drives the
	// exhaustive candidate enumeration.
	filtered := excludeLocked(pcfg.Inventory, pcfg.Locked)
	if len(filtered) == 0 {
		return nil, fmt.Errorf("palettesearch: inventory has no colors left after excluding locked colors")
	}
	eligible := dedupSortInventory(filtered)
	if len(eligible) < freeSlots {
		return nil, fmt.Errorf("palettesearch: %d eligible inventory colors but %d free slots", len(eligible), freeSlots)
	}

	// Neighbor-mix parameters, identical to production sampling (both the
	// dither model and the TD-simulated appearance use them).
	simEll, simK := simNeighborParams(opts.LayerHeight)

	// The production fast scorer's pick, on the exact inputs ResolvePalette
	// would construct — for the regret comparison.
	prodEntries, err := productionPick(ctx, vo.Cells, filtered, pcfg, freeSlots, opts, progress.NullTracker{})
	if err != nil {
		return nil, err
	}
	prodHexKey := hexSetKey(entryHexes(prodEntries))

	// Enumerate candidate combinations deterministically.
	combos := combinations(len(eligible), freeSlots)
	if cfg.Limit > 0 && cfg.Limit < len(combos) {
		combos = combos[:cfg.Limit]
	}
	// Guarantee the production pick is always evaluated so regret is
	// reportable even when it falls outside a --limit window.
	prodInSweep := false
	prodIdx := -1
	for i, combo := range combos {
		if hexSetKey(comboHexes(eligible, combo)) == prodHexKey {
			prodInSweep = true
			prodIdx = i
			break
		}
	}
	if !prodInSweep {
		// Append the production pick's combo (as explicit inventory indices)
		// so it is scored on the same footing as the rest.
		extra := comboForEntries(eligible, prodEntries)
		if extra != nil {
			prodIdx = len(combos)
			combos = append(combos, extra)
		}
	}

	// Shared, color-independent splat geometry. Built once; every candidate
	// only swaps FaceColors, which is why one bounds computation frames them
	// all identically.
	geom := buildSplatGeometry(vo, splitNormalXform(so))

	// Pre-render and pre-blur the palette-independent sampled target once per
	// (view, σ); reused for every candidate comparison.
	targetColors := make([][3]uint8, len(vo.VisibleToCell))
	for vi, gi := range vo.VisibleToCell {
		if gi >= 0 && gi < len(vo.CellSamples) {
			targetColors[vi] = vo.CellSamples[gi].Color
		} else {
			targetColors[vi] = [3]uint8{defaultGray, defaultGray, defaultGray}
		}
	}
	targetMesh := geom.colorByVisible(targetColors)
	targetBlur := make([][]*percep.Image, len(cfg.Views)) // [viewIdx][sigmaIdx]
	targetRGBA := make([]*image.RGBA, len(cfg.Views))
	for vi, viewName := range cfg.Views {
		img := render(targetMesh, viewName, cfg.Res)
		targetRGBA[vi] = img
		base := percep.FromRGBA(img)
		blurs := make([]*percep.Image, len(cfg.Sigmas))
		for si, sigma := range cfg.Sigmas {
			blurs[si] = base.Blur(sigma)
		}
		targetBlur[vi] = blurs
	}

	rankIdx := rankSigmaIndices(cfg.Sigmas, cfg.RankSigmas)
	sigma2Idx := indexOfSigma(cfg.Sigmas, 2)

	// Score every candidate in a worker pool. cells/neighbors/geom/targetBlur
	// are read-only and shared; each worker builds its own dither model and
	// buffers, so there is no shared mutable state.
	type job struct {
		idx   int
		combo []int
	}
	results := make([]PaletteCandidate, len(combos))
	jobs := make(chan job)
	var wg sync.WaitGroup
	var done int64
	start := time.Now()
	var firstErr error
	var errOnce sync.Once

	worker := func() {
		defer wg.Done()
		nullTr := progress.NullTracker{}
		for j := range jobs {
			if ctx.Err() != nil {
				return
			}
			pal, tds, labels, hexes := buildCandidatePalette(pcfg.Locked, eligible, j.combo)
			cellColors, cerr := candidateVisibleColors(ctx, opts, vo, pal, tds, simEll, simK, nullTr)
			if cerr != nil {
				errOnce.Do(func() { firstErr = cerr })
				return
			}
			mesh := geom.colorByVisible(cellColors)
			cand := scoreMeshAgainstTarget(mesh, cfg.Views, cfg.Res, cfg.Sigmas, targetBlur, sigma2Idx, rankIdx, render)
			cand.Labels = labels
			cand.Hexes = hexes
			results[j.idx] = cand

			n := atomic.AddInt64(&done, 1)
			if n%50 == 0 {
				reportProgress(int(n), len(combos), start)
			}
		}
	}
	wg.Add(cfg.Workers)
	for i := 0; i < cfg.Workers; i++ {
		go worker()
	}
	for i, combo := range combos {
		jobs <- job{idx: i, combo: combo}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	elapsed := time.Since(start)

	// Rank ascending by RankKey; ties broken by the sorted free-hex set for
	// full determinism.
	order := make([]int, len(results))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ra, rb := results[order[a]], results[order[b]]
		if ra.RankKey != rb.RankKey {
			return ra.RankKey < rb.RankKey
		}
		return hexSetKey(ra.Hexes) < hexSetKey(rb.Hexes)
	})
	ranked := make([]PaletteCandidate, len(order))
	prodRank := -1
	for newPos, oldIdx := range order {
		c := results[oldIdx]
		c.Rank = newPos + 1
		ranked[newPos] = c
		if oldIdx == prodIdx {
			prodRank = newPos + 1
		}
	}
	var production *PaletteCandidate
	if prodRank >= 1 {
		p := ranked[prodRank-1]
		production = &p
	}

	res := &PaletteSearchResult{
		Sigmas:              cfg.Sigmas,
		RankSigmas:          cfg.RankSigmas,
		LockedHexes:         lockedHexes(pcfg.Locked),
		Candidates:          ranked,
		Production:          production,
		ProductionInSweep:   prodInSweep,
		NumCells:            len(vo.Cells),
		NumCandidates:       len(combos),
		SecondsPerCandidate: elapsed.Seconds() / math.Max(1, float64(len(combos))),
	}

	if err := writeSearchOutputs(res, cfg, opts, vo, geom, pcfg, eligible, simEll, simK, targetRGBA, production, render); err != nil {
		return nil, err
	}
	return res, nil
}

// withDefaults fills unset config fields with their documented defaults.
func (cfg PaletteSearchConfig) withDefaults() PaletteSearchConfig {
	if cfg.Res <= 0 {
		cfg.Res = 512
	}
	if cfg.Sigmas == nil {
		cfg.Sigmas = []float64{0, 2, 4, 8, 16}
	}
	if cfg.RankSigmas == nil {
		cfg.RankSigmas = []float64{2, 8}
	}
	if cfg.Workers <= 0 {
		cfg.Workers = runtime.GOMAXPROCS(0)
	}
	if cfg.RenderTop < 0 {
		cfg.RenderTop = 0
	} else if cfg.RenderTop == 0 {
		cfg.RenderTop = 3
	}
	if len(cfg.Views) == 0 {
		cfg.Views = append([]string(nil), PaletteSearchViews...)
	}
	if cfg.OutDir == "" {
		cfg.OutDir = "."
	}
	return cfg
}

// ditherModes lists the production dither modes; validDitherMode gates the
// harness the same way RunCached gates a real run.
var ditherModes = []string{"dlc-d30-p7", "floyd-steinberg", "riemersma", "bn-adapt-5", "none"}

func validDitherMode(mode string) bool { return containsStr(ditherModes, mode) }

// candidateVisibleColors dithers the cells against pal and returns the
// TD-simulated per-visible-cell color (voxel.EffectiveCellColors), mirroring
// buildSimFaceColors' cell/assignment indexing and honorTD gating. The dither
// mode dispatch is the shared ditherAssignments used by the production stage.
func candidateVisibleColors(ctx context.Context, opts Options, vo *voxelizeOutput, pal [][3]uint8, tds []float32, simEll float32, simK float64, tracker progress.Tracker) ([][3]uint8, error) {
	model := voxel.NewDitherModel(pal, tds, float64(simEll), simK, opts.HonorTD)
	var assignments []int32
	var err error
	if len(vo.Neighbors) == 0 {
		// Phase-2 transition: no adjacency graph → nearest-color assignment,
		// exactly as runDither short-circuits.
		assignments, err = voxel.AssignColors(ctx, vo.Cells, pal)
	} else {
		assignments, err = ditherAssignments(ctx, opts, vo.Cells, pal, model, vo.Neighbors, tracker)
	}
	if err != nil {
		return nil, err
	}
	return voxel.EffectiveCellColors(vo.Cells, assignments, pal, tds, vo.Neighbors, simEll, 2, simK), nil
}

// scoreMeshAgainstTarget renders a colored splat mesh across every view and σ
// and scores it against the pre-blurred target: for each σ the per-view mean
// Lab ΔE is combined pixel-count-weighted across views. targetBlur is indexed
// [viewIdx][sigmaIdx]. sigma2Idx is the σ index whose weighted p99 is recorded
// (-1 to skip); rankIdx lists the σ indices averaged into the rank key. The
// returned candidate carries only scores — Labels/Hexes are filled by the
// caller. Factored out of the worker loop so the metric plumbing is directly
// testable.
func scoreMeshAgainstTarget(mesh *MeshData, views []string, res int, sigmas []float64, targetBlur [][]*percep.Image, sigma2Idx int, rankIdx []int, render SplatRenderFunc) PaletteCandidate {
	cand := PaletteCandidate{ScoresBySigma: make([]float64, len(sigmas))}
	// Render once per view, then blur that render at each σ (rendering is far
	// costlier than a blur, so hoist it out of the σ loop).
	viewImgs := make([]*percep.Image, len(views))
	for vi := range views {
		viewImgs[vi] = percep.FromRGBA(render(mesh, views[vi], res))
	}
	means := make([]float64, len(views))
	p99s := make([]float64, len(views))
	weights := make([]float64, len(views))
	for si := range sigmas {
		for vi := range views {
			cb := viewImgs[vi].Blur(sigmas[si])
			mean, p99, n := percep.MeanLabDE(cb, targetBlur[vi][si])
			means[vi], p99s[vi], weights[vi] = mean, p99, float64(n)
		}
		cand.ScoresBySigma[si] = weightedMean(means, weights)
		if si == sigma2Idx {
			cand.P99Sigma2 = weightedMean(p99s, weights)
		}
	}
	var rk float64
	for _, si := range rankIdx {
		rk += cand.ScoresBySigma[si]
	}
	if len(rankIdx) > 0 {
		rk /= float64(len(rankIdx))
	}
	cand.RankKey = rk
	return cand
}

// weightedMean returns Σ(vals·weights)/Σweights, or 0 when the total weight is
// zero. Used to combine per-view ΔE by pixel count so a view that fills more of
// the frame counts proportionally more.
func weightedMean(vals, weights []float64) float64 {
	var s, w float64
	for i := range vals {
		s += vals[i] * weights[i]
		w += weights[i]
	}
	if w == 0 {
		return 0
	}
	return s / w
}

// buildCandidatePalette assembles a full candidate palette (locked entries
// followed by the combination, order-stable) and the free-slot labels/hexes.
// The area TD model does not use an infill designation, so no slot-0 swap is
// performed — the realized appearance is invariant to it anyway.
func buildCandidatePalette(locked []palette.InventoryEntry, eligible []palette.InventoryEntry, combo []int) (pal [][3]uint8, tds []float32, labels, hexes []string) {
	n := len(locked) + len(combo)
	pal = make([][3]uint8, 0, n)
	tds = make([]float32, 0, n)
	for _, e := range locked {
		pal = append(pal, e.Color)
		tds = append(tds, e.TD)
	}
	labels = make([]string, 0, len(combo))
	hexes = make([]string, 0, len(combo))
	for _, ci := range combo {
		e := eligible[ci]
		pal = append(pal, e.Color)
		tds = append(tds, e.TD)
		labels = append(labels, e.Label)
		hexes = append(hexes, hexOf(e.Color))
	}
	return pal, tds, labels, hexes
}

// productionPick reproduces ResolvePalette's fast-scorer selection on the same
// cellColors / cellWeights it would construct, returning the chosen free-slot
// entries (locked entries excluded).
func productionPick(ctx context.Context, cells []voxel.ActiveCell, filtered []palette.InventoryEntry, pcfg voxel.PaletteConfig, freeSlots int, opts Options, tracker progress.Tracker) ([]palette.InventoryEntry, error) {
	cellColors := make([][3]uint8, len(cells))
	for i, c := range cells {
		cellColors[i] = c.Color
	}
	// All-or-nothing area weighting, identical to ResolvePalette.
	var cellWeights []float32
	allHaveAreas := true
	for _, c := range cells {
		if c.Area <= 0 {
			allHaveAreas = false
			break
		}
	}
	if allHaveAreas {
		cellWeights = make([]float32, len(cells))
		for i, c := range cells {
			cellWeights[i] = c.Area
		}
	}
	return palette.SelectFromInventory(ctx, cellColors, cellWeights, filtered, freeSlots, pcfg.Locked, opts.Dither != "none", pcfg.TD, tracker)
}

// splitNormalXform mirrors runClip's normalXform: for a split run it rotates a
// cell's color-model-frame normal into bed coords via the half's forward rigid
// transform; nil for an unsplit run (centroids and normals already share the
// output frame).
func splitNormalXform(so *splitOutput) func(halfIdx byte, normal [3]float32) [3]float32 {
	if so == nil || !so.Enabled {
		return nil
	}
	spl := so
	return func(halfIdx byte, n [3]float32) [3]float32 {
		if int(halfIdx) >= len(spl.Xform) {
			return n
		}
		t := spl.Xform[int(halfIdx)]
		o := t.Apply([3]float32{0, 0, 0})
		p := t.Apply(n)
		return [3]float32{p[0] - o[0], p[1] - o[1], p[2] - o[2]}
	}
}

// --- pure helpers (unit-tested) ---------------------------------------------

// combinations returns every k-subset of {0..n-1} as ascending index slices,
// in lexicographic order. Deterministic; the enumeration order of the sweep.
func combinations(n, k int) [][]int {
	if k < 0 || k > n {
		return nil
	}
	if k == 0 {
		return [][]int{{}}
	}
	var out [][]int
	idx := make([]int, k)
	for i := range idx {
		idx[i] = i
	}
	for {
		c := make([]int, k)
		copy(c, idx)
		out = append(out, c)
		// Advance to the next combination (rightmost position that can move).
		i := k - 1
		for i >= 0 && idx[i] == n-k+i {
			i--
		}
		if i < 0 {
			break
		}
		idx[i]++
		for j := i + 1; j < k; j++ {
			idx[j] = idx[j-1] + 1
		}
	}
	return out
}

// rankSigmaIndices maps the rank-key σ values onto their indices in the scored
// σ list. σ values not present in sigmas are skipped.
func rankSigmaIndices(sigmas, rankSigmas []float64) []int {
	var out []int
	for _, rs := range rankSigmas {
		if i := indexOfSigma(sigmas, rs); i >= 0 {
			out = append(out, i)
		}
	}
	return out
}

func indexOfSigma(sigmas []float64, want float64) int {
	for i, s := range sigmas {
		if s == want {
			return i
		}
	}
	return -1
}

// excludeLocked returns inv entries whose color matches no locked color
// (mirror of voxel.filterInventory, which is unexported).
func excludeLocked(inv, locked []palette.InventoryEntry) []palette.InventoryEntry {
	if len(locked) == 0 {
		return inv
	}
	lockedSet := make(map[[3]uint8]bool, len(locked))
	for _, e := range locked {
		lockedSet[e.Color] = true
	}
	out := make([]palette.InventoryEntry, 0, len(inv))
	for _, e := range inv {
		if !lockedSet[e.Color] {
			out = append(out, e)
		}
	}
	return out
}

// dedupSortInventory drops duplicate colors (first occurrence wins) and sorts
// the survivors by hex, giving the candidate enumeration a stable order.
func dedupSortInventory(inv []palette.InventoryEntry) []palette.InventoryEntry {
	seen := make(map[[3]uint8]bool, len(inv))
	out := make([]palette.InventoryEntry, 0, len(inv))
	for _, e := range inv {
		if seen[e.Color] {
			continue
		}
		seen[e.Color] = true
		out = append(out, e)
	}
	sort.SliceStable(out, func(a, b int) bool { return hexOf(out[a].Color) < hexOf(out[b].Color) })
	return out
}

func hexOf(c [3]uint8) string { return fmt.Sprintf("#%02X%02X%02X", c[0], c[1], c[2]) }

func entryHexes(entries []palette.InventoryEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = hexOf(e.Color)
	}
	return out
}

func comboHexes(eligible []palette.InventoryEntry, combo []int) []string {
	out := make([]string, len(combo))
	for i, ci := range combo {
		out[i] = hexOf(eligible[ci].Color)
	}
	return out
}

func lockedHexes(locked []palette.InventoryEntry) []string {
	out := make([]string, len(locked))
	for i, e := range locked {
		out[i] = hexOf(e.Color)
	}
	return out
}

// hexSetKey is an order-insensitive identity for a set of hex colors.
func hexSetKey(hexes []string) string {
	c := append([]string(nil), hexes...)
	sort.Strings(c)
	return strings.Join(c, ",")
}

// comboForEntries maps a set of chosen entries back to eligible-index form so
// it can be scored like any enumerated combination. Returns nil if any entry's
// color is absent from eligible.
func comboForEntries(eligible []palette.InventoryEntry, entries []palette.InventoryEntry) []int {
	idxByHex := make(map[string]int, len(eligible))
	for i, e := range eligible {
		idxByHex[hexOf(e.Color)] = i
	}
	out := make([]int, 0, len(entries))
	for _, e := range entries {
		i, ok := idxByHex[hexOf(e.Color)]
		if !ok {
			return nil
		}
		out = append(out, i)
	}
	sort.Ints(out)
	return out
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func reportProgress(done, total int, start time.Time) {
	elapsed := time.Since(start).Seconds()
	rate := float64(done) / math.Max(elapsed, 1e-9)
	var eta float64
	if rate > 0 {
		eta = float64(total-done) / rate
	}
	fmt.Fprintf(os.Stderr, "palettesearch: %d/%d candidates (%.2fs/cand, ETA %.0fs)\n",
		done, total, elapsed/math.Max(1, float64(done)), eta)
}

// --- output writers ---------------------------------------------------------

func writeSearchOutputs(res *PaletteSearchResult, cfg PaletteSearchConfig, opts Options, vo *voxelizeOutput, geom *splatGeometry, pcfg voxel.PaletteConfig, eligible []palette.InventoryEntry, simEll float32, simK float64, targetRGBA []*image.RGBA, production *PaletteCandidate, render SplatRenderFunc) error {
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return err
	}
	if err := writeCSV(filepath.Join(cfg.OutDir, "results.csv"), res); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(cfg.OutDir, "results.json"), res); err != nil {
		return err
	}

	// PNGs: the sampled target, the top-N candidates, and the production pick.
	for vi, viewName := range cfg.Views {
		p := filepath.Join(cfg.OutDir, fmt.Sprintf("target_%s.png", viewName))
		if err := writeSearchPNG(p, targetRGBA[vi]); err != nil {
			return err
		}
	}
	renderCand := func(prefix string, c PaletteCandidate) error {
		combo := comboForEntries(eligible, entriesForHexes(eligible, c.Hexes))
		if combo == nil {
			return nil
		}
		pal, tds, _, _ := buildCandidatePalette(pcfg.Locked, eligible, combo)
		colors, err := candidateVisibleColors(context.Background(), opts, vo, pal, tds, simEll, simK, progress.NullTracker{})
		if err != nil {
			return err
		}
		mesh := geom.colorByVisible(colors)
		for _, viewName := range cfg.Views {
			p := filepath.Join(cfg.OutDir, fmt.Sprintf("%s_%s.png", prefix, viewName))
			if err := writeSearchPNG(p, render(mesh, viewName, cfg.Res)); err != nil {
				return err
			}
		}
		return nil
	}
	for i := 0; i < cfg.RenderTop && i < len(res.Candidates); i++ {
		if err := renderCand(fmt.Sprintf("top%d", i+1), res.Candidates[i]); err != nil {
			return err
		}
	}
	if production != nil {
		if err := renderCand("prodpick", *production); err != nil {
			return err
		}
	}
	return nil
}

// entriesForHexes rebuilds inventory entries (with TD/label) from a hex list by
// looking each up in eligible.
func entriesForHexes(eligible []palette.InventoryEntry, hexes []string) []palette.InventoryEntry {
	byHex := make(map[string]palette.InventoryEntry, len(eligible))
	for _, e := range eligible {
		byHex[hexOf(e.Color)] = e
	}
	out := make([]palette.InventoryEntry, 0, len(hexes))
	for _, h := range hexes {
		if e, ok := byHex[strings.ToUpper(h)]; ok {
			out = append(out, e)
		}
	}
	return out
}

func writeCSV(path string, res *PaletteSearchResult) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	header := []string{"rank", "labels", "hexes"}
	for _, s := range res.Sigmas {
		header = append(header, "sigma"+strconv.FormatFloat(s, 'g', -1, 64))
	}
	header = append(header, "p99_sigma2", "rank_key")
	if err := w.Write(header); err != nil {
		return err
	}
	for _, c := range res.Candidates {
		row := []string{strconv.Itoa(c.Rank), strings.Join(c.Labels, "|"), strings.Join(c.Hexes, "|")}
		for _, s := range c.ScoresBySigma {
			row = append(row, strconv.FormatFloat(s, 'f', 4, 64))
		}
		row = append(row, strconv.FormatFloat(c.P99Sigma2, 'f', 4, 64), strconv.FormatFloat(c.RankKey, 'f', 4, 64))
		if err := w.Write(row); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func writeJSON(path string, res *PaletteSearchResult) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

func writeSearchPNG(path string, img *image.RGBA) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
