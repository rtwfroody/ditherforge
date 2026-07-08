// Command ditherrender judges dither modes by their RENDERED pipeline
// output, measured perceptually against a continuous-color reference.
//
// Unlike tests/ditherbench — which scores modes on a 2D grid of ideal
// square cells — ditherrender runs the real pipeline on a 3D model, so
// it exercises the actual cell geometry the dither lands on: irregular
// cell areas, merged cells, TD opacity weighting, and the projected
// silhouette. Those are things the 2D bench cannot see.
//
// The reference is produced by the same pipeline run with
// ShowSampledColors=true: every visible face is painted with the raw
// continuous color sampled from the model, i.e. what the surface would
// look like with no palette quantization at all. A good dither should
// perceptually match that continuous surface once the eye integrates
// adjacent tiles. Using the sampled render as the reference means there
// are no golden images to maintain — the reference is regenerated every
// run and never churns when a dither mode is intentionally changed.
//
// For each (mode, view) we render both the dithered mesh and the
// reference mesh into a shared framing rectangle, blur both in linear
// light with a mask-normalized Gaussian at each requested physical
// scale (mm), and report the mean / 99th-percentile Lab ΔE (see the
// tests/percep package doc for why linear-light blur + Lab is the
// correct perceptual comparison). Small ΔE = the dithered surface is
// perceptually indistinguishable from the continuous-color surface at
// that integration scale. Alongside the per-σ blur columns we report a
// single csf_ΔE: a CSF-weighted visible difference (percep.VisibleDE)
// that rolls every scale into one number for a viewer at -view-mm, and
// which is the sole ballot voter. The σ rows are stdout diagnostics.
//
// The "none" mode (nearest-color quantization, no error diffusion) is
// the sanity anchor: its visible quantization patches survive the blur,
// so it should score clearly worse than the error-diffusing modes at
// the large scale.
//
// With -suite the tool sweeps a built-in table of models (cube, earth,
// building, glyphid) at their known-good sizes and alpha-wrap settings,
// emitting one ballot per (model, view). A single model overfits its
// own palette and geometry — a mode that wins on earth's smooth
// gradients may lose on the cube's flat uniform faces or the glyphid's
// fine detail — so ranking across several models is what makes the 3D
// ballot pool representative rather than a verdict on one mesh.
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rtwfroody/ditherforge/internal/debugrender"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
	"github.com/rtwfroody/ditherforge/tests/ballots"
	"github.com/rtwfroody/ditherforge/tests/inventories"
	"github.com/rtwfroody/ditherforge/tests/percep"
)

// suiteModel is one entry in the built-in -suite table: a model path
// plus the pipeline settings it needs to run cleanly (native scale vs
// normalized size, alpha-wrap for open meshes). name is used as the
// ballot modelBase so voter names stay stable and readable.
type suiteModel struct {
	name      string
	path      string
	sizeMM    float64 // normalized max-extent in mm; ignored when scaleOnly
	scaleOnly bool    // run at native scale (Scale=1, no Size normalization)
	alphaWrap bool    // watertight open meshes before the cellslicer
}

// suiteModels mirrors the known-good configs in
// tests/sampled_match_input_test.go and tests/objects/*.json. Glyphid
// runs at 50mm rather than its 100mm fixture size to keep the suite's
// runtime bounded; alpha-wrap is required for it and the building
// (both open meshes).
var suiteModels = []suiteModel{
	{name: "cube", path: filepath.Join("tests", "objects", "cube.stl"), scaleOnly: true},                              // 20mm native
	{name: "earth", path: filepath.Join("tests", "objects", "earth.glb"), sizeMM: 50},                                 //
	{name: "building", path: filepath.Join("tests", "objects", "low_poly_building.glb"), sizeMM: 35, alphaWrap: true}, //
	{name: "glyphid", path: filepath.Join("tests", "objects", "glyphid_praetorian.glb"), sizeMM: 50, alphaWrap: true}, //
}

func main() {
	model := flag.String("model", filepath.Join("tests", "objects", "earth.glb"), "input model path")
	modesArg := flag.String("modes", "none,floyd-steinberg,riemersma,dlc-d30-p7,bn-adapt-5", "comma-separated dither modes")
	res := flag.Int("res", 512, "render resolution (pixels per side)")
	sizeArg := flag.Float64("size", 50, "normalized max extent in mm")
	numColors := flag.Int("num-colors", 6, "number of palette colors")
	outDir := flag.String("out", "", "directory to dump rendered + blurred PNGs (default: none)")
	sigmasArg := flag.String("sigmas-mm", "0.8,3.2", "comma-separated physical blur scales in mm")
	viewMM := flag.Float64("view-mm", 4000, "viewing distance in mm for the CSF visible-difference voter")
	ballotsPath := flag.String("ballots", "", "write perceptual ranked ballots (one CSF visible-difference score per model/view, group \"3d\") to this JSON path for tests/ditherrank. Only modes that ran without error are included.")
	alphaWrap := flag.Bool("alpha-wrap", false, "watertight the input mesh with alpha-wrap before the cellslicer (needed for open meshes)")
	scaleOnly := flag.Bool("scale-only", false, "run the model at native scale (Scale=1, skip Size normalization); -size is ignored")
	suite := flag.Bool("suite", false, "sweep the built-in model table (cube/earth/building/glyphid); -model/-size/-alpha-wrap/-scale-only are ignored")
	flag.Parse()

	modes := splitTrim(*modesArg)
	if len(modes) == 0 {
		fail("no modes given")
	}
	sigmasMM, err := parseFloats(*sigmasArg)
	if err != nil {
		fail("parsing -sigmas-mm: %v", err)
	}
	if len(sigmasMM) == 0 {
		fail("no sigmas given")
	}
	if *outDir != "" {
		if err := os.MkdirAll(*outDir, 0o755); err != nil {
			fail("mkdir %s: %v", *outDir, err)
		}
	}

	// Build the inventory in the shape pipeline.Options wants (parallel
	// color / label slices) from the Panchroma set.
	inv := inventories.Panchroma()
	invColors := make([][3]uint8, len(inv))
	invLabels := make([]string, len(inv))
	for i, e := range inv {
		invColors[i] = e.Color
		invLabels[i] = e.Label
	}

	// The set of models to run: the built-in suite, or a single entry
	// from the -model/-size/-alpha-wrap/-scale-only flags.
	var models []suiteModel
	if *suite {
		models = suiteModels
	} else {
		models = []suiteModel{{
			name:      strings.TrimSuffix(filepath.Base(*model), filepath.Ext(*model)),
			path:      *model,
			sizeMM:    *sizeArg,
			scaleOnly: *scaleOnly,
			alphaWrap: *alphaWrap,
		}}
	}

	ctx := context.Background()
	cfg := renderConfig{
		modes:       modes,
		sigmasMM:    sigmasMM,
		res:         *res,
		numColors:   *numColors,
		outDir:      *outDir,
		invColors:   invColors,
		invLabels:   invLabels,
		viewMM:      *viewMM,
		wantBallots: *ballotsPath != "",
	}

	printHeader(sigmasMM)

	// Ballots accumulate ACROSS models into one slice (voter names carry
	// the modelBase, so they stay distinct) and are written once at the
	// end.
	var allBallots []ballots.Ballot
	for _, m := range models {
		fmt.Printf("=== %s ===\n", m.name)
		bs, err := runModel(ctx, cfg, m)
		if err != nil {
			// A model's reference-run failure shouldn't abort the whole
			// suite: report it and move on to the remaining models.
			fmt.Fprintf(os.Stderr, "model %q: %v\n", m.name, err)
			fmt.Printf("  %s: %v\n", m.name, err)
			continue
		}
		allBallots = append(allBallots, bs...)
	}

	if *ballotsPath != "" {
		if err := ballots.WriteFile(*ballotsPath, allBallots); err != nil {
			fail("writing ballots: %v", err)
		}
		fmt.Fprintf(os.Stderr, "wrote %d ballots to %s\n", len(allBallots), *ballotsPath)
	}
}

// renderConfig holds the per-invocation settings shared by every model
// in a run (they come from flags, not the per-model table).
type renderConfig struct {
	modes       []string
	sigmasMM    []float64
	res         int
	numColors   int
	outDir      string
	invColors   [][3]uint8
	invLabels   []string
	viewMM      float64
	wantBallots bool
}

// runModel runs the reference + per-mode pipeline for a single model,
// prints its table (including the per-mode AVG rows), and returns the
// ballots collected for it (empty when ballots weren't requested). It
// returns an error only when the reference run itself fails; a per-mode
// failure just skips that mode.
func runModel(ctx context.Context, cfg renderConfig, m suiteModel) ([]ballots.Ballot, error) {
	// A fresh cache per model: everything upstream of the dither stage
	// (load, voxelize, palette selection, clip) is identical across the
	// modes of ONE model, so a shared cache makes the per-mode cost just
	// dither + merge + render — but it must not leak between models.
	cache := pipeline.NewStageCache()

	baseOpts := pipeline.Options{
		Input:           m.path,
		ObjectIndex:     -1,
		NumColors:       cfg.numColors,
		InventoryColors: cfg.invColors,
		InventoryLabels: cfg.invLabels,
		NozzleDiameter:  0.4,
		LayerHeight:     0.2,
		ColorSnap:       5,
		Force:           true,
		Scale:           1,
	}
	if m.alphaWrap {
		baseOpts.MeshRepair = pipeline.RepairAlphaWrap
	}
	// scaleOnly leaves Size nil and runs at native scale (Scale=1),
	// exactly like the scaleOnly cases in sampled_match_input_test.go.
	if !m.scaleOnly {
		size := float32(m.sizeMM)
		baseOpts.Size = &size
	}

	// Reference: sampled continuous colors. The dither value is
	// irrelevant here (ShowSampledColors bypasses dither) but must be a
	// valid mode string.
	fmt.Fprintf(os.Stderr, "[%s] running reference (ShowSampledColors)...\n", m.name)
	refOpts := baseOpts
	refOpts.ShowSampledColors = true
	refOpts.Dither = "riemersma"
	refPR, err := pipeline.RunCached(ctx, cache, refOpts, nil)
	if err != nil {
		return nil, fmt.Errorf("reference run: %w", err)
	}
	if refPR.OutputMesh == nil {
		return nil, fmt.Errorf("reference run produced no output mesh")
	}
	refMesh := refPR.OutputMesh

	views := debugrender.DefaultViews

	// summary[mode][sigmaIdx] accumulates mean/p99 over views.
	type acc struct {
		meanSum, p99Sum float64
		n               int
	}
	summary := make(map[string][]acc, len(cfg.modes))
	// csfSummary[mode] accumulates the CSF visible-difference over views.
	type csfAcc struct {
		sum float64
		n   int
	}
	csfSummary := make(map[string]csfAcc, len(cfg.modes))

	// csfScores[viewName][mode] = CSF visible-difference mean Lab ΔE,
	// collected only when ballots are requested. Only modes that ran
	// without error contribute, so ballots are partial by construction.
	csfScores := map[string]map[string]float64{}

	// The CSF voter's band frequencies depend on mmPerPx, which is only
	// known once a view is framed; print the diagnostic once per model on
	// the first computed pitch.
	csfDiagPrinted := false

	for _, mode := range cfg.modes {
		fmt.Fprintf(os.Stderr, "[%s] running mode %q...\n", m.name, mode)
		opts := baseOpts
		opts.ShowSampledColors = false
		opts.Dither = mode
		pr, err := pipeline.RunCached(ctx, cache, opts, nil)
		if err != nil {
			fmt.Printf("  %-22s ERROR: %v\n", mode, err)
			continue
		}
		if pr.OutputMesh == nil {
			fmt.Printf("  %-22s ERROR: no output mesh\n", mode)
			continue
		}
		ditheredMesh := pr.OutputMesh
		summary[mode] = make([]acc, len(cfg.sigmasMM))

		for _, v := range views {
			sharedBounds := debugrender.UnionBounds(
				debugrender.MeshDataProjectedBounds(refMesh, v),
				debugrender.MeshDataProjectedBounds(ditheredMesh, v),
			)
			// The renderer maps max(xRange, yRange) across res*(1-2*0.05)
			// pixels (margin 0.05 each side); see render.ProjectToPixels.
			// So one pixel spans this many mm:
			mmPerPx := math.Max(sharedBounds.XMax-sharedBounds.XMin, sharedBounds.YMax-sharedBounds.YMin) / (float64(cfg.res) * 0.9)

			if !csfDiagPrinted {
				freqs, weights := percep.CSFBandWeights(mmPerPx, cfg.viewMM)
				fmt.Printf("  CSF voter geometry: mmPerPx=%.4f view-mm=%.0f\n", mmPerPx, cfg.viewMM)
				fmt.Printf("    band center freq (cpd):")
				for _, f := range freqs {
					fmt.Printf(" %7.2f", f)
				}
				fmt.Println()
				fmt.Printf("    band CSF weight       :")
				for _, w := range weights {
					fmt.Printf(" %7.4f", w)
				}
				fmt.Println()
				csfDiagPrinted = true
			}

			refImg := debugrender.RenderPipelineMeshCulledWithBounds(refMesh, v, cfg.res, sharedBounds).ToRGBA()
			ditImg := debugrender.RenderPipelineMeshCulledWithBounds(ditheredMesh, v, cfg.res, sharedBounds).ToRGBA()
			refP := percep.FromRGBA(refImg)
			ditP := percep.FromRGBA(ditImg)

			if cfg.outDir != "" {
				dumpPNG(filepath.Join(cfg.outDir, fmt.Sprintf("%s_%s_%s_ref.png", m.name, mode, v.Name)), refImg)
				dumpPNG(filepath.Join(cfg.outDir, fmt.Sprintf("%s_%s_%s_dithered.png", m.name, mode, v.Name)), ditImg)
			}

			cols := make([]sigmaResult, len(cfg.sigmasMM))
			for si, smm := range cfg.sigmasMM {
				sigPx := 0.0
				if mmPerPx > 0 {
					sigPx = smm / mmPerPx
				}
				refBlur := refP.Blur(sigPx)
				ditBlur := ditP.Blur(sigPx)
				mean, p99, _ := percep.MeanLabDE(refBlur, ditBlur)
				cols[si] = sigmaResult{sigPx: sigPx, mean: mean, p99: p99}
				a := &summary[mode][si]
				a.meanSum += mean
				a.p99Sum += p99
				a.n++
				if cfg.outDir != "" {
					dumpPNG(filepath.Join(cfg.outDir, fmt.Sprintf("%s_%s_%s_ref_blur%gmm.png", m.name, mode, v.Name, smm)), refBlur.ToRGBA())
					dumpPNG(filepath.Join(cfg.outDir, fmt.Sprintf("%s_%s_%s_dithered_blur%gmm.png", m.name, mode, v.Name, smm)), ditBlur.ToRGBA())
				}
			}

			// CSF visible difference on the UNBLURRED pair: rolls the whole
			// σ sweep into one CSF-weighted number and is the sole ballot
			// voter. The σ blur rows above stay as stdout diagnostics only.
			csfMean, _, _ := percep.VisibleDE(ditP, refP, mmPerPx, cfg.viewMM)
			ca := csfSummary[mode]
			ca.sum += csfMean
			ca.n++
			csfSummary[mode] = ca
			if cfg.wantBallots {
				if csfScores[v.Name] == nil {
					csfScores[v.Name] = map[string]float64{}
				}
				csfScores[v.Name][mode] = csfMean
			}

			printRow(mode, v.Name, mmPerPx, cols, csfMean)
		}

		// Per-mode summary row: mean/p99 averaged over views. σpx blank
		// (it varies per view with the framing).
		sumCols := make([]sigmaResult, len(cfg.sigmasMM))
		for si := range cfg.sigmasMM {
			a := summary[mode][si]
			if a.n > 0 {
				sumCols[si] = sigmaResult{sigPx: math.NaN(), mean: a.meanSum / float64(a.n), p99: a.p99Sum / float64(a.n)}
			}
		}
		csfAvg := math.NaN()
		if ca := csfSummary[mode]; ca.n > 0 {
			csfAvg = ca.sum / float64(ca.n)
		}
		printRow(mode, "AVG", math.NaN(), sumCols, csfAvg)
	}

	if !cfg.wantBallots {
		return nil, nil
	}
	var bs []ballots.Ballot
	for _, v := range views {
		scores, ok := csfScores[v.Name]
		if !ok || len(scores) == 0 {
			continue
		}
		bs = append(bs, ballots.Ballot{
			Voter:  fmt.Sprintf("render/%s/%s/csf", m.name, v.Name),
			Group:  "3d",
			Scores: scores,
		})
	}
	return bs, nil
}

// sigmaResult is one blur scale's measurement for a (mode, view) cell.
type sigmaResult struct {
	sigPx, mean, p99 float64
}

func printHeader(sigmasMM []float64) {
	fmt.Printf("%-22s %-6s %8s", "mode", "view", "mmPerPx")
	for _, smm := range sigmasMM {
		fmt.Printf("   | σ=%.2gmm  %6s %6s %6s", smm, "σpx", "mean", "p99")
	}
	fmt.Printf("   | %8s", "csf_ΔE")
	fmt.Println()
}

func printRow(mode, view string, mmPerPx float64, cols []sigmaResult, csf float64) {
	fmt.Printf("%-22s %-6s %8s", mode, view, fmtOrBlank(mmPerPx, "%8.4f"))
	for _, c := range cols {
		fmt.Printf("   |          %6s %6.2f %6.2f", fmtOrBlank(c.sigPx, "%6.2f"), c.mean, c.p99)
	}
	fmt.Printf("   | %8s", fmtOrBlank(csf, "%8.2f"))
	fmt.Println()
}

// fmtOrBlank formats v unless it's NaN, in which case it returns a
// right-aligned dash of the same nominal width.
func fmtOrBlank(v float64, format string) string {
	if math.IsNaN(v) {
		return "     -"
	}
	return fmt.Sprintf(format, v)
}

func splitTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseFloats(s string) ([]float64, error) {
	parts := splitTrim(s)
	out := make([]float64, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", p, err)
		}
		out = append(out, v)
	}
	return out, nil
}

func dumpPNG(path string, img *image.RGBA) {
	if err := debugrender.WritePNG(path, img); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", path, err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
