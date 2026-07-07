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
// that integration scale.
//
// The "none" mode (nearest-color quantization, no error diffusion) is
// the sanity anchor: its visible quantization patches survive the blur,
// so it should score clearly worse than the error-diffusing modes at
// the large scale.
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
	"github.com/rtwfroody/ditherforge/tests/inventories"
	"github.com/rtwfroody/ditherforge/tests/percep"
)

func main() {
	model := flag.String("model", filepath.Join("tests", "objects", "earth.glb"), "input model path")
	modesArg := flag.String("modes", "none,floyd-steinberg,riemersma,dizzy-corrected,dizzy-local-corrected,blue-noise", "comma-separated dither modes")
	res := flag.Int("res", 512, "render resolution (pixels per side)")
	sizeArg := flag.Float64("size", 50, "normalized max extent in mm")
	numColors := flag.Int("num-colors", 6, "number of palette colors")
	outDir := flag.String("out", "", "directory to dump rendered + blurred PNGs (default: none)")
	sigmasArg := flag.String("sigmas-mm", "0.8,3.2", "comma-separated physical blur scales in mm")
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

	size := float32(*sizeArg)
	baseOpts := pipeline.Options{
		Input:           *model,
		ObjectIndex:     -1,
		NumColors:       *numColors,
		InventoryColors: invColors,
		InventoryLabels: invLabels,
		NozzleDiameter:  0.4,
		LayerHeight:     0.2,
		ColorSnap:       5,
		Force:           true,
		Scale:           1,
		Size:            &size,
	}

	ctx := context.Background()
	// One cache shared across all runs: everything upstream of the
	// dither stage (load, voxelize, palette selection, clip) is
	// identical for every mode, so the shared cache makes the per-mode
	// cost just the dither + merge + render.
	cache := pipeline.NewStageCache()

	// Reference: sampled continuous colors. The dither value is
	// irrelevant here (ShowSampledColors bypasses dither) but must be a
	// valid mode string.
	fmt.Fprintln(os.Stderr, "running reference (ShowSampledColors)...")
	refOpts := baseOpts
	refOpts.ShowSampledColors = true
	refOpts.Dither = "riemersma"
	refPR, err := pipeline.RunCached(ctx, cache, refOpts, nil)
	if err != nil {
		fail("reference run: %v", err)
	}
	if refPR.OutputMesh == nil {
		fail("reference run produced no output mesh")
	}
	refMesh := refPR.OutputMesh

	views := debugrender.DefaultViews

	// summary[mode][sigmaIdx] accumulates mean/p99 over views.
	type acc struct {
		meanSum, p99Sum float64
		n               int
	}
	summary := make(map[string][]acc, len(modes))

	printHeader(sigmasMM)

	for _, mode := range modes {
		fmt.Fprintf(os.Stderr, "running mode %q...\n", mode)
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
		summary[mode] = make([]acc, len(sigmasMM))

		for _, v := range views {
			sharedBounds := debugrender.UnionBounds(
				debugrender.MeshDataProjectedBounds(refMesh, v),
				debugrender.MeshDataProjectedBounds(ditheredMesh, v),
			)
			// The renderer maps max(xRange, yRange) across res*(1-2*0.05)
			// pixels (margin 0.05 each side); see render.ProjectToPixels.
			// So one pixel spans this many mm:
			mmPerPx := math.Max(sharedBounds.XMax-sharedBounds.XMin, sharedBounds.YMax-sharedBounds.YMin) / (float64(*res) * 0.9)

			refImg := debugrender.RenderPipelineMeshCulledWithBounds(refMesh, v, *res, sharedBounds).ToRGBA()
			ditImg := debugrender.RenderPipelineMeshCulledWithBounds(ditheredMesh, v, *res, sharedBounds).ToRGBA()
			refP := percep.FromRGBA(refImg)
			ditP := percep.FromRGBA(ditImg)

			if *outDir != "" {
				dumpPNG(filepath.Join(*outDir, fmt.Sprintf("%s_%s_ref.png", mode, v.Name)), refImg)
				dumpPNG(filepath.Join(*outDir, fmt.Sprintf("%s_%s_dithered.png", mode, v.Name)), ditImg)
			}

			cols := make([]sigmaResult, len(sigmasMM))
			for si, smm := range sigmasMM {
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
				if *outDir != "" {
					dumpPNG(filepath.Join(*outDir, fmt.Sprintf("%s_%s_ref_blur%gmm.png", mode, v.Name, smm)), refBlur.ToRGBA())
					dumpPNG(filepath.Join(*outDir, fmt.Sprintf("%s_%s_dithered_blur%gmm.png", mode, v.Name, smm)), ditBlur.ToRGBA())
				}
			}
			printRow(mode, v.Name, mmPerPx, cols)
		}

		// Per-mode summary row: mean/p99 averaged over views. σpx blank
		// (it varies per view with the framing).
		sumCols := make([]sigmaResult, len(sigmasMM))
		for si := range sigmasMM {
			a := summary[mode][si]
			if a.n > 0 {
				sumCols[si] = sigmaResult{sigPx: math.NaN(), mean: a.meanSum / float64(a.n), p99: a.p99Sum / float64(a.n)}
			}
		}
		printRow(mode, "AVG", math.NaN(), sumCols)
	}
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
	fmt.Println()
}

func printRow(mode, view string, mmPerPx float64, cols []sigmaResult) {
	fmt.Printf("%-22s %-6s %8s", mode, view, fmtOrBlank(mmPerPx, "%8.4f"))
	for _, c := range cols {
		fmt.Printf("   |          %6s %6.2f %6.2f", fmtOrBlank(c.sigPx, "%6.2f"), c.mean, c.p99)
	}
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
