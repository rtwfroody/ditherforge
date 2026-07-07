// Command ditherbench measures dither algorithm quality across the
// existing PNG fixtures plus a few synthetic uniform-color fixtures
// designed to surface banding artifacts.
//
// Four categories of metric per (fixture, mode):
//
//   - pcp2_ΔE / pcp8_ΔE: the primary "what the eye sees" numbers.
//     Both the input-color field and the assigned-palette field are
//     rasterized at cell resolution (1 px = 1 cell), blurred in
//     LINEAR light with a mask-normalized Gaussian at σ=2 px and
//     σ=8 px, then compared as mean Lab ΔE (see the tests/percep
//     package doc). Small = the dithered field is perceptually
//     indistinguishable from the input when the eye integrates at
//     that scale. This subsumes drift (a constant offset survives any
//     blur) and banding (structure at or above σ survives the blur),
//     and it is measured in a space with no byte-averaging Jensen gap
//     — unlike a naive mean of sRGB bytes. σ=2 is close inspection,
//     σ=8 is across-the-room integration.
//
//   - drift_lin_ΔE: avg(output_color) - avg(input_color): both means
//     taken in LINEAR light (unweighted over cells), then converted to
//     Lab for the ΔE. Linear light is the space the error-diffusion
//     modes conserve since the linear-light refactor (the eye
//     integrates adjacent tiles as photons; see "Perceptual dithering
//     color space" in internal/voxel/color.go), so a mode that
//     conserves correctly scores near 0. Small = good.
//
//     A byte-space variant (averaging raw sRGB bytes) used to be
//     reported alongside; it was removed as stale. Mean of a nonlinear
//     map ≠ map of the mean, and because output palette colors span a
//     much wider range than input colors, exact linear conservation
//     still leaves a large byte-space Jensen offset — that column
//     penalized exactly the modes that conserve correctly.
//
//   - wander_ΔE: mean Lab ΔE between the chosen palette entry and
//     the nearest-input palette entry. Captures how often the
//     algorithm reaches past the closest palette color to a far
//     one (e.g. white/black to average to grey when a near-grey
//     entry exists). Pure nearest-color picks score 0; algorithms
//     that aggressively distribute residual into large-magnitude
//     swings score high.
//
//   - pcell_p50/p99: per-cell ΔE between cell color and its
//     assigned palette color (Lab). The local quantization error
//     each cell contributes; bounded by ~half the palette
//     quantization step regardless of mode.
//
//   - blockvar(S=...): variance of error vector at multiple block
//     scales, normalized by per-pixel variance. For ideal white
//     noise this ratio decays as 1/S². For blue noise it decays
//     faster than 1/S² (high-frequency error cancels in larger
//     blocks). For banding at scale B the ratio stays close to 1
//     at S=B. The shape of the curve across scales tells you
//     where dither structure lives.
//
// Synthetic fixtures (all-one-color images) are the cleanest test
// of banding: there's no input texture to hide structure behind,
// so any pattern in the output is dither artifact.
//
// Output is informational. No pass/fail thresholds; intended for
// use during algorithm development (e.g., evaluating different
// scrambling permutation subsets when implementing scrambled
// Z-order traversal).
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	colorful "github.com/lucasb-eyer/go-colorful"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/voxel"
	"github.com/rtwfroody/ditherforge/tests/inventories"
	"github.com/rtwfroody/ditherforge/tests/percep"
)

// fixture is a 2D set of cells laid out at integer (Col, Row), with
// the original image dimensions retained so we can reconstruct an
// output image after dithering and so block-variance can iterate
// over a known canvas.
type fixture struct {
	name   string
	cells  []voxel.ActiveCell
	width  int
	height int
}

// dmode wraps one dither algorithm in a uniform signature.
type dmode struct {
	name string
	run  func(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error)
}

func main() {
	outDir := flag.String("out", "", "directory to write per-(fixture,mode) output PNGs (default: none)")
	onlyFixture := flag.String("fixture", "", "run only this fixture (substring match); default: all")
	onlyMode := flag.String("mode", "", "run only this mode (substring match); default: all")
	flag.Parse()

	if *outDir != "" {
		if err := os.MkdirAll(*outDir, 0o755); err != nil {
			fail("mkdir %s: %v", *outDir, err)
		}
	}

	fixtures := loadAllFixtures()
	if *onlyFixture != "" {
		fixtures = filterFixtures(fixtures, *onlyFixture)
	}
	if len(fixtures) == 0 {
		fail("no fixtures matched")
	}

	modes := []dmode{
		{"none", wrapAssign},
		// dizzy is kept here as a baseline reference: dizzy-corrected
		// is built from three iterated dizzy passes, and the bench
		// is the place to verify the corrected variant continues to
		// strictly improve on its building block.
		{"dizzy", wrapDizzy},
		{"dizzy-corrected", wrapDizzyCorrected},
		{"dizzy-2hop", wrapDizzy2Hop},
		{"dizzy-recover", wrapDizzyRecover},
		{"dizzy-local-corrected", wrapDizzyLocalCorrected},
		// Damped-relaxation sweep of DitherLocalCorrected: the default
		// above is γ=1.0 (plain replace) / 3 passes. These under-relax
		// (γ<1) with more passes to test whether that tames the earth
		// pass-2 overshoot without regressing the well-behaved fixtures.
		{"dlc-d70-p5", wrapDLCd70p5},
		{"dlc-d50-p5", wrapDLCd50p5},
		{"dlc-d30-p7", wrapDLCd30p7},
		{"floyd-steinberg", wrapFS},
		{"riemersma", wrapRiemersma},
		{"r-knearest-3", wrapRKNearest3},
		{"r-clip-60", wrapRClip60},
		{"r-adaptK-1.5", wrapRAdaptK15},
		{"r-adaptK-2.0", wrapRAdaptK20},
		{"r-adaptK-2.5", wrapRAdaptK25},
		{"r-bounded-60", wrapRBounded60},
		{"r-mk3-r2.0", wrapRMK3R20},
		{"r-leak-0.05", wrapRLeak005},
		{"r-leak-0.1", wrapRLeak01},
		{"r-leak-0.2", wrapRLeak02},
		// riemersma-pair: shipped sliding 2-cell Riemersma at the
		// production cancellation default. r-pair-disj: research
		// disjoint variant at the same λ, kept for A/B comparison
		// against the sliding choice.
		{"riemersma-pair", wrapRiemersmaPair},
		{"r-pair-disj", wrapRPairDisjointDefault},
		{"bn-pair", wrapBlueNoise},
		{"bn-tri", wrapBlueNoiseTri},
		{"bn-simplex", wrapBlueNoiseSimplex},
		{"bn-adapt-2", wrapBNAdapt2},
		{"bn-adapt-5", wrapBNAdapt5},
		{"bn-adapt-10", wrapBNAdapt10},
		{"bn-adapt-20", wrapBNAdapt20},
		{"bn-pair-d", wrapBNPairDiffused},
		{"bn-tri-d", wrapBNTriDiffused},
		{"dbs-3", wrapDBS3},
		{"dbs-8", wrapDBS8},
		{"dbs-2hop-8", wrapDBS2Hop8},
		{"dbs-bn20-8", wrapDBSFromBN20},
	}
	if *onlyMode != "" {
		// Comma-separated list of substring patterns; a mode is kept
		// if any of the patterns is a substring of its name.
		patterns := strings.Split(*onlyMode, ",")
		var keep []dmode
		for _, m := range modes {
			for _, pat := range patterns {
				pat = strings.TrimSpace(pat)
				if pat != "" && strings.Contains(m.name, pat) {
					keep = append(keep, m)
					break
				}
			}
		}
		modes = keep
		if len(modes) == 0 {
			fail("no modes matched")
		}
	}

	blockScales := []int{2, 4, 8, 16, 32}

	inv := inventories.Panchroma()

	for _, fx := range fixtures {
		fmt.Printf("=== %s (%dx%d, %d opaque cells) ===\n", fx.name, fx.width, fx.height, len(fx.cells))
		// Use the production scorer with chroma weighting for parity
		// with the live pipeline. dithering=true matches what real
		// users get when any dither mode is selected.
		pcfg := voxel.PaletteConfig{NumColors: 4, Inventory: inv}
		pal, _, _, _, err := voxel.ResolvePalette(context.Background(), fx.cells, pcfg, true, progress.NullTracker{})
		if err != nil {
			fmt.Printf("  ResolvePalette failed: %v\n\n", err)
			continue
		}
		fmt.Print("  palette:")
		for _, p := range pal {
			fmt.Printf(" #%02X%02X%02X", p[0], p[1], p[2])
		}
		fmt.Println()

		nbrs := buildNeighbors2D(fx.cells)
		// Cluster cells by their nearest palette entry in INPUT-color
		// space. Stable across passes (depends only on input), so
		// suitable for diagnosing whether per-cluster drifts differ
		// in direction (which would suggest segmented correction
		// could outperform global correction).
		cellCluster := make([]int, len(fx.cells))
		for i, c := range fx.cells {
			cellCluster[i] = nearestPaletteIdx(c.Color, pal)
		}
		printHeader(blockScales)
		// Run all modes in parallel; collect results then print/save
		// in the originally-declared mode order so output is
		// reproducible. Each mode is pure on (cells, pal, nbrs) so
		// there's no synchronization needed beyond the final join.
		type modeResult struct {
			assigns []int32
			err     error
			met     metrics
		}
		results := make([]modeResult, len(modes))
		var wg sync.WaitGroup
		sem := make(chan struct{}, runtime.NumCPU())
		for i, m := range modes {
			wg.Add(1)
			// Acquire the semaphore on the parent goroutine so the
			// loop blocks rather than fan-out launching N goroutines
			// up front; each worker holds its slot for its lifetime
			// and releases via defer.
			sem <- struct{}{}
			go func(i int, m dmode) {
				defer wg.Done()
				defer func() { <-sem }()
				assigns, err := m.run(context.Background(), fx.cells, pal, nbrs)
				if err != nil {
					results[i] = modeResult{err: err}
					return
				}
				met := computeMetrics(fx, pal, assigns, blockScales)
				results[i] = modeResult{assigns: assigns, met: met}
			}(i, m)
		}
		wg.Wait()
		modeAssigns := make(map[string][]int32, len(modes))
		for i, m := range modes {
			r := results[i]
			if r.err != nil {
				fmt.Printf("  %-16s ERROR: %v\n", m.name, r.err)
				continue
			}
			printRow(m.name, r.met, blockScales)
			modeAssigns[m.name] = r.assigns
			if *outDir != "" {
				path := filepath.Join(*outDir, fmt.Sprintf("%s.%s.png", fx.name, m.name))
				if werr := writeOutputPNG(path, fx, pal, r.assigns); werr != nil {
					fmt.Printf("    write %s: %v\n", path, werr)
				}
			}
		}

		// Palette-assignment distribution: shows what proportion of
		// cells each algorithm assigned to each palette entry, side
		// by side with the OPTIMAL mix — the proportions that, when
		// averaged, would exactly hit the input mean color. Drift
		// is determined entirely by the gap between actual mix and
		// optimal mix; this table makes the gap explicit per palette
		// entry.
		printAssignmentDistribution(fx, pal, modes, modeAssigns)

		// Per-cluster drift diagnostic for the most-recently-run mode
		// (skipped if no dither work — none mode has identical input
		// and output structure, drift = quantization). Show drift per
		// cluster against dizzy's output, which is the un-corrected
		// baseline most likely to surface the multimodal-bias effect.
		if dizzyAssigns, ok := modeAssigns["dizzy"]; ok {
			fmt.Println("  per-cluster drift, linear-light means (cluster center = nearest palette in input space, dizzy output):")
			printClusterDrifts(fx, pal, dizzyAssigns, cellCluster)
		}
		// Also dizzy-corrected, since that's what auto-mode picks on
		// borderline scenes — useful to see whether residual drift
		// after correction is concentrated in one cluster.
		if dcAssigns, ok := modeAssigns["dizzy-corrected"]; ok {
			fmt.Println("  per-cluster drift (dizzy-corrected output):")
			printClusterDrifts(fx, pal, dcAssigns, cellCluster)
		}
		// And dizzy-local-corrected, the experimental localized corrector
		// under sweep — its per-cluster drift shows whether the localized
		// correction shifts specific clusters (e.g. pushing a uniform grey
		// field toward chromatic palette entries).
		if dlcAssigns, ok := modeAssigns["dizzy-local-corrected"]; ok {
			fmt.Println("  per-cluster drift (dizzy-local-corrected output):")
			printClusterDrifts(fx, pal, dlcAssigns, cellCluster)
		}
		fmt.Println()
	}
}

// ----- fixture loading -----

func loadAllFixtures() []fixture {
	var out []fixture
	matches, _ := filepath.Glob("tests/testdata/color/*.png")
	sort.Strings(matches)
	for _, p := range matches {
		fx, err := loadPNGFixture(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", p, err)
			continue
		}
		out = append(out, fx)
	}
	// Synthetic uniform-color fixtures designed to surface banding
	// without any input texture to hide it. Sized to give multiple
	// blocks at every scale we measure (32 is the largest, so 512
	// gives 16x16 = 256 blocks -- enough for a stable variance).
	out = append(out,
		// Terracotta sits between palette entries — maximally
		// diagnostic for ordinary dither structure.
		makeUniformFixture("uniform_terracotta", 512, 512, [3]uint8{0xB3, 0x7D, 0x67}),
		// Neutral grey at moderate luminance — same idea, neutral hue.
		makeUniformFixture("uniform_neutral_grey", 512, 512, [3]uint8{0x80, 0x80, 0x80}),
		// Saturated magenta sits well outside the chroma any
		// Panchroma-derived 4-color palette can reach. Tests the
		// "out-of-gamut input" failure mode: an algorithm that
		// silently breaks energy conservation will look "blue-noise"
		// here while drift_lin_ΔE blows up.
		makeUniformFixture("uniform_saturated_magenta", 512, 512, [3]uint8{0xFF, 0x00, 0xFF}),
		// Checkerboard of two colors that aren't in the Panchroma
		// palette: warm orange (#E08840) and cool teal (#40A0E0).
		// Each check is 16x16 cells; whole fixture is 512x512 with
		// 32x32 checks. Tests whether dither algorithms blur the
		// sharp color boundaries -- error diffusion crosses pixel
		// boundaries via spatial neighbor propagation, so we expect
		// some "bleeding" of one check's color into the next. The
		// extent of bleed varies by algorithm; visually inspect the
		// --out PNGs to compare.
		makeCheckerboardFixture("checkerboard_orange_teal", 512, 512, 16,
			[3]uint8{0xE0, 0x88, 0x40}, [3]uint8{0x40, 0xA0, 0xE0}),
		// Smooth horizontal sRGB gradient between two near-palette
		// colors. Tests how cleanly an algorithm can render slowly-
		// varying input — the regime where Riemersma's residual
		// accumulator can wander far from the local input.
		makeGradientFixture("gradient_warm", 512, 256,
			[3]uint8{0x33, 0x22, 0x18}, [3]uint8{0xE8, 0xB8, 0x90}),
		// Mid-luminance grey with a faint diagonal stripe (~5 RGB
		// units amplitude). Captures the "near-flat with subtle
		// texture" regime: an algorithm that snaps too hard kills
		// the texture; one that dithers too aggressively wanders.
		makeFaintTextureFixture("faint_texture_grey", 512, 256,
			[3]uint8{0x80, 0x80, 0x80}, 5),
	)
	return out
}

func filterFixtures(fxs []fixture, substr string) []fixture {
	var out []fixture
	for _, fx := range fxs {
		if strings.Contains(fx.name, substr) {
			out = append(out, fx)
		}
	}
	return out
}

func loadPNGFixture(path string) (fixture, error) {
	f, err := os.Open(path)
	if err != nil {
		return fixture{}, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return fixture{}, err
	}
	b := img.Bounds()
	nrgba := image.NewNRGBA(b)
	draw.Draw(nrgba, b, img, b.Min, draw.Src)
	var cells []voxel.ActiveCell
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := nrgba.NRGBAAt(x, y)
			if c.A < 128 {
				continue
			}
			cells = append(cells, voxel.ActiveCell{
				Col:   x,
				Row:   y,
				Cx:    float32(x),
				Cy:    float32(y),
				Color: [3]uint8{c.R, c.G, c.B},
			})
		}
	}
	name := strings.TrimSuffix(filepath.Base(path), ".png")
	return fixture{name: name, cells: cells, width: b.Dx(), height: b.Dy()}, nil
}

// makeCheckerboardFixture builds a w×h grid where each checkSize×
// checkSize block alternates between c1 (even sum of check
// coordinates) and c2 (odd). Tests dither behavior at sharp color
// boundaries: error diffusion algorithms will "bleed" some of one
// check's color into the adjacent check via spatial neighbor
// propagation; the extent and visibility of the bleed varies by
// algorithm.
func makeCheckerboardFixture(name string, w, h, checkSize int, c1, c2 [3]uint8) fixture {
	cells := make([]voxel.ActiveCell, 0, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			checkX := x / checkSize
			checkY := y / checkSize
			c := c1
			if (checkX+checkY)&1 == 1 {
				c = c2
			}
			cells = append(cells, voxel.ActiveCell{
				Col:   x,
				Row:   y,
				Color: c,
			})
		}
	}
	return fixture{name: name, cells: cells, width: w, height: h}
}

func makeUniformFixture(name string, w, h int, c [3]uint8) fixture {
	cells := make([]voxel.ActiveCell, 0, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			cells = append(cells, voxel.ActiveCell{
				Col:   x,
				Row:   y,
				Cx:    float32(x),
				Cy:    float32(y),
				Color: c,
			})
		}
	}
	return fixture{name: name, cells: cells, width: w, height: h}
}

// makeGradientFixture builds a w×h fixture with a smooth horizontal
// sRGB-space gradient from c0 (left) to c1 (right). Tests slowly-
// varying input — regime where Riemersma's window accumulator can
// produce wander.
func makeGradientFixture(name string, w, h int, c0, c1 [3]uint8) fixture {
	cells := make([]voxel.ActiveCell, 0, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			t := float64(x) / float64(w-1)
			c := [3]uint8{
				uint8(math.Round(float64(c0[0])*(1-t) + float64(c1[0])*t)),
				uint8(math.Round(float64(c0[1])*(1-t) + float64(c1[1])*t)),
				uint8(math.Round(float64(c0[2])*(1-t) + float64(c1[2])*t)),
			}
			cells = append(cells, voxel.ActiveCell{
				Col:   x,
				Row:   y,
				Cx:    float32(x),
				Cy:    float32(y),
				Color: c,
			})
		}
	}
	return fixture{name: name, cells: cells, width: w, height: h}
}

// makeFaintTextureFixture builds a near-flat fixture: a base grey
// with a low-amplitude diagonal stripe pattern. amplitude is the
// per-channel ± swing in 8-bit RGB units. Captures the regime where
// an algorithm must reproduce subtle texture without over-dithering.
func makeFaintTextureFixture(name string, w, h int, base [3]uint8, amplitude int) fixture {
	cells := make([]voxel.ActiveCell, 0, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Diagonal sinusoid, period 32 cells.
			phase := math.Sin(2 * math.Pi * float64(x+y) / 32.0)
			delta := math.Round(float64(amplitude) * phase)
			c := [3]uint8{
				clampU8(int(base[0]) + int(delta)),
				clampU8(int(base[1]) + int(delta)),
				clampU8(int(base[2]) + int(delta)),
			}
			cells = append(cells, voxel.ActiveCell{
				Col:   x,
				Row:   y,
				Cx:    float32(x),
				Cy:    float32(y),
				Color: c,
			})
		}
	}
	return fixture{name: name, cells: cells, width: w, height: h}
}

func clampU8(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// buildNeighbors2D gives each cell its 8-connected 2D neighbors via
// a (Col, Row) lookup. Weights mirror the production
// voxel.BuildNeighbors policy: face-adjacent = 1.0, diagonal = 0.1.
// Keep these in sync with voxel.BuildNeighbors -- if the production
// weight scheme is tuned, the bench will silently disagree until
// this function is updated to match.
func buildNeighbors2D(cells []voxel.ActiveCell) [][]voxel.Neighbor {
	pos := make(map[[2]int]int, len(cells))
	for i, c := range cells {
		pos[[2]int{c.Col, c.Row}] = i
	}
	out := make([][]voxel.Neighbor, len(cells))
	for i, c := range cells {
		var nbs []voxel.Neighbor
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				j, ok := pos[[2]int{c.Col + dx, c.Row + dy}]
				if !ok {
					continue
				}
				w := float32(1.0)
				if dx != 0 && dy != 0 {
					w = 0.1
				}
				nbs = append(nbs, voxel.Neighbor{Idx: j, Weight: w})
			}
		}
		out[i] = nbs
	}
	return out
}

// ----- mode wrappers -----

func wrapAssign(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, _ [][]voxel.Neighbor) ([]int32, error) {
	return voxel.AssignColors(ctx, cells, pal)
}
func wrapDizzy(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.DitherWithNeighbors(ctx, cells, pal, nil, nbrs, progress.NullTracker{})
}
func wrapDizzyCorrected(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.DitherCorrected(ctx, cells, pal, nil, nbrs, progress.NullTracker{})
}
func wrapDizzy2Hop(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, _ [][]voxel.Neighbor) ([]int32, error) {
	return voxel.DitherWithNeighbors(ctx, cells, pal, nil, voxel.BuildNeighbors2Hop(cells), progress.NullTracker{})
}
func wrapDizzyRecover(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.DitherWithRecover(ctx, cells, pal, nil, nbrs, progress.NullTracker{})
}
func wrapDizzyLocalCorrected(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.DitherLocalCorrected(ctx, cells, pal, nil, nbrs, progress.NullTracker{})
}
func wrapDLCd70p5(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.DitherLocalCorrectedTuned(ctx, cells, pal, nil, nbrs, progress.NullTracker{}, 0.7, 5)
}
func wrapDLCd50p5(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.DitherLocalCorrectedTuned(ctx, cells, pal, nil, nbrs, progress.NullTracker{}, 0.5, 5)
}
func wrapDLCd30p7(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.DitherLocalCorrectedTuned(ctx, cells, pal, nil, nbrs, progress.NullTracker{}, 0.3, 7)
}
func wrapFS(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.FloydSteinberg(ctx, cells, pal, nil, nbrs, progress.NullTracker{})
}
func wrapRiemersma(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.Riemersma(ctx, cells, pal, nil, nbrs, voxel.RiemersmaInputBiasDefault, progress.NullTracker{})
}
func wrapRKNearest3(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.RiemersmaKNearest(ctx, cells, pal, nbrs, 3, progress.NullTracker{})
}
func wrapRClip60(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.RiemersmaResidualClipped(ctx, cells, pal, nbrs, 60, progress.NullTracker{})
}
func wrapRAdaptK15(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.RiemersmaAdaptiveK(ctx, cells, pal, nbrs, 1.5, progress.NullTracker{})
}
func wrapRAdaptK20(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.RiemersmaAdaptiveK(ctx, cells, pal, nbrs, 2.0, progress.NullTracker{})
}
func wrapRAdaptK25(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.RiemersmaAdaptiveK(ctx, cells, pal, nbrs, 2.5, progress.NullTracker{})
}
func wrapRBounded60(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.RiemersmaBoundedWander(ctx, cells, pal, nbrs, 60, progress.NullTracker{})
}
func wrapRMK3R20(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.RiemersmaMinKAdaptive(ctx, cells, pal, nbrs, 3, 2.0, voxel.RiemersmaInputBiasDefault, progress.NullTracker{})
}
func wrapRLeak005(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.RiemersmaLeaky(ctx, cells, pal, nbrs, 0.05, voxel.RiemersmaInputBiasDefault, progress.NullTracker{})
}
func wrapRLeak01(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.RiemersmaLeaky(ctx, cells, pal, nbrs, 0.1, voxel.RiemersmaInputBiasDefault, progress.NullTracker{})
}
func wrapRLeak02(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.RiemersmaLeaky(ctx, cells, pal, nbrs, 0.2, voxel.RiemersmaInputBiasDefault, progress.NullTracker{})
}
func wrapRiemersmaPair(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.RiemersmaPair(ctx, cells, pal, nil, nbrs, voxel.RiemersmaPairCancellationDefault, voxel.RiemersmaInputBiasDefault, progress.NullTracker{})
}
func wrapRPairDisjointDefault(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.RiemersmaPairDisjoint(ctx, cells, pal, nbrs, voxel.RiemersmaPairCancellationDefault, voxel.RiemersmaInputBiasDefault, progress.NullTracker{})
}
func wrapBlueNoise(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.BlueNoiseThresholdSimplex(ctx, cells, pal, nbrs, progress.NullTracker{})
}
func wrapBlueNoiseSimplex(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.BlueNoiseSimplexFull(ctx, cells, pal, nbrs, progress.NullTracker{})
}
func wrapBlueNoiseTri(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.BlueNoiseTriangle(ctx, cells, pal, nbrs, progress.NullTracker{})
}
func wrapBNPairDiffused(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.BlueNoisePairDiffused(ctx, cells, pal, nbrs, progress.NullTracker{})
}
func wrapBNTriDiffused(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.BlueNoiseTriangleDiffused(ctx, cells, pal, nbrs, progress.NullTracker{})
}
func wrapBNAdapt2(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.BlueNoiseAdaptive(ctx, cells, pal, nil, nbrs, 2, progress.NullTracker{})
}
func wrapBNAdapt5(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.BlueNoiseAdaptive(ctx, cells, pal, nil, nbrs, 5, progress.NullTracker{})
}
func wrapBNAdapt10(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.BlueNoiseAdaptive(ctx, cells, pal, nil, nbrs, 10, progress.NullTracker{})
}
func wrapBNAdapt20(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.BlueNoiseAdaptive(ctx, cells, pal, nil, nbrs, 20, progress.NullTracker{})
}
func wrapDBS3(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.DBS(ctx, cells, pal, nbrs, 3, voxel.RiemersmaInputBiasDefault, progress.NullTracker{})
}
func wrapDBS8(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.DBS(ctx, cells, pal, nbrs, 8, voxel.RiemersmaInputBiasDefault, progress.NullTracker{})
}
func wrapDBS2Hop8(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.DBS2Hop(ctx, cells, pal, nbrs, 8, voxel.RiemersmaInputBiasDefault, progress.NullTracker{})
}
func wrapDBSFromBN20(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.DBSFromBN(ctx, cells, pal, nbrs, 20, 8, progress.NullTracker{})
}
func wrapRLab(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	// In Lab, RiemersmaInputBiasRange=30 RGB equates roughly to Lab range ~15-20.
	return voxel.RiemersmaLab(ctx, cells, pal, nbrs, voxel.RiemersmaInputBiasDefault, 15, progress.NullTracker{})
}
func wrapRLab100_30(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.RiemersmaLab(ctx, cells, pal, nbrs, 1.0, 30, progress.NullTracker{})
}
func wrapRLab100_15(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.RiemersmaLab(ctx, cells, pal, nbrs, 1.0, 15, progress.NullTracker{})
}

// ----- metrics -----

type metrics struct {
	driftLinDE  float64         // Lab ΔE of the LINEAR-light means
	percep2     float64         // mean Lab ΔE after mask-normalized linear-light blur, σ=2px
	percep8     float64         // mean Lab ΔE after mask-normalized linear-light blur, σ=8px
	pcell       []float64       // sorted per-cell ΔE
	wanderDE    float64         // mean ΔE(chosen palette, nearest-input palette)
	wanderClump float64         // 8x8 block-mean variance of per-cell wander, normalized — captures clumping
	blockVar    map[int]float64 // scale -> normalized block-mean variance
	maxDirCorr  map[int]float64 // distance -> max |autocorrelation| over 8 directions
}

func computeMetrics(fx fixture, pal [][3]uint8, assigns []int32, blockScales []int) metrics {
	// Global drift in Lab ΔE (see the drift_lin_ΔE doc at the top of
	// this file): average input and output in linear light, then map
	// the two linear means back to sRGB float bytes so we can reuse
	// toLab (which expects 0-255 sRGB) for the ΔE. Linear light is the
	// space the inner error-diffusion conserves, so a correctly-
	// conserving mode lands near 0.
	var iLinR, iLinG, iLinB, oLinR, oLinG, oLinB float64
	for i, c := range fx.cells {
		iLinR += srgb8ToLinear(c.Color[0])
		iLinG += srgb8ToLinear(c.Color[1])
		iLinB += srgb8ToLinear(c.Color[2])
		a := assigns[i]
		oLinR += srgb8ToLinear(pal[a][0])
		oLinG += srgb8ToLinear(pal[a][1])
		oLinB += srgb8ToLinear(pal[a][2])
	}
	n := float64(len(fx.cells))
	ilL, ilA, ilBl := toLab(linearToSrgb255(iLinR/n), linearToSrgb255(iLinG/n), linearToSrgb255(iLinB/n))
	olL, olA, olBl := toLab(linearToSrgb255(oLinR/n), linearToSrgb255(oLinG/n), linearToSrgb255(oLinB/n))
	driftLinDE := math.Sqrt((ilL-olL)*(ilL-olL) + (ilA-olA)*(ilA-olA) + (ilBl-olBl)*(ilBl-olBl))

	// Perceptual blur-ΔE: rasterize the input-color field and the
	// assigned-palette field at cell resolution (1 px = 1 cell), blur
	// both in linear light with a mask-normalized Gaussian, and take
	// the mean Lab ΔE at two integration scales. See the pcp*_ΔE doc
	// at the top of this file and the tests/percep package doc.
	inImg := percep.Image{W: fx.width, H: fx.height, Pix: make([][3]float64, fx.width*fx.height), Mask: make([]bool, fx.width*fx.height)}
	outImg := percep.Image{W: fx.width, H: fx.height, Pix: make([][3]float64, fx.width*fx.height), Mask: make([]bool, fx.width*fx.height)}
	for i, c := range fx.cells {
		idx := c.Row*fx.width + c.Col
		if idx < 0 || idx >= len(inImg.Mask) {
			continue
		}
		p := pal[assigns[i]]
		inImg.Mask[idx] = true
		outImg.Mask[idx] = true
		inImg.Pix[idx] = [3]float64{srgb8ToLinear(c.Color[0]), srgb8ToLinear(c.Color[1]), srgb8ToLinear(c.Color[2])}
		outImg.Pix[idx] = [3]float64{srgb8ToLinear(p[0]), srgb8ToLinear(p[1]), srgb8ToLinear(p[2])}
	}
	percep2, _, _ := percep.MeanLabDE(inImg.Blur(2), outImg.Blur(2))
	percep8, _, _ := percep.MeanLabDE(inImg.Blur(8), outImg.Blur(8))

	// Per-cell ΔE distribution and wander_ΔE.
	//
	// wander_ΔE = mean ΔE(chosen palette entry, nearest-to-input palette
	// entry). Captures how often the algorithm reaches past the closest
	// palette entry to a far one — e.g. picking white/black to average
	// to grey when a near-grey entry exists. Pure nearest-color picks
	// score 0; algorithms that aggressively distribute residual into
	// large-magnitude palette swings score high.
	palLab := make([][3]float64, len(pal))
	for k, p := range pal {
		l, a, b := toLab(float64(p[0]), float64(p[1]), float64(p[2]))
		palLab[k] = [3]float64{l, a, b}
	}
	pcell := make([]float64, len(fx.cells))
	wanderPerCell := make([]float64, len(fx.cells))
	var wanderSum float64
	for i, c := range fx.cells {
		cL, cA, cB := toLab(float64(c.Color[0]), float64(c.Color[1]), float64(c.Color[2]))
		// Nearest-input palette entry, in Lab.
		nearest := 0
		var nearestD2 float64 = math.Inf(1)
		for k, lab := range palLab {
			dL := cL - lab[0]
			dA := cA - lab[1]
			dB := cB - lab[2]
			d2 := dL*dL + dA*dA + dB*dB
			if d2 < nearestD2 {
				nearestD2 = d2
				nearest = k
			}
		}
		chosen := int(assigns[i])
		pL, pA, pB := palLab[chosen][0], palLab[chosen][1], palLab[chosen][2]
		pcell[i] = math.Sqrt((cL-pL)*(cL-pL) + (cA-pA)*(cA-pA) + (cB-pB)*(cB-pB))
		nL, nA, nB := palLab[nearest][0], palLab[nearest][1], palLab[nearest][2]
		w := math.Sqrt((pL-nL)*(pL-nL) + (pA-nA)*(pA-nA) + (pB-nB)*(pB-nB))
		wanderPerCell[i] = w
		wanderSum += w
	}
	wanderDE := wanderSum / float64(len(fx.cells))
	wanderClump := computeWanderClump(fx, wanderPerCell, 8)
	sort.Float64s(pcell)

	// Build the per-pixel error grid, then subtract the global mean
	// error vector before measuring spatial structure. The drift
	// metric above already reports the mean separately; leaving it
	// in here would inflate both blockvar and maxdircorr by a
	// constant proportional to the squared mean error, making cross-mode
	// comparison unfair (a high-drift mode would look "bandy" even
	// with perfectly white residuals because |μ|² leaks into both
	// the autocorrelation numerator and the per-pixel-variance
	// denominator).
	grid := buildErrorGrid(fx, pal, assigns)
	centerErrorGrid(grid)
	bv := computeBlockVariance(grid, fx.width, fx.height, blockScales)
	mdc := computeMaxDirCorr(grid, fx.width, fx.height, []int{1, 4})

	return metrics{
		driftLinDE:  driftLinDE,
		percep2:     percep2,
		percep8:     percep8,
		pcell:       pcell,
		wanderDE:    wanderDE,
		wanderClump: wanderClump,
		blockVar:    bv,
		maxDirCorr:  mdc,
	}
}

// computeWanderClump returns the ratio of (variance of B×B block-mean
// wander) / (per-cell variance of wander). For white noise this is
// 1/B². Higher = clumpy (some regions consistently high-wander, others
// low). Lower = uniformly distributed wander.
//
// Subtle uniform-region clumps that mean wanderDE doesn't surface
// show up here as a high ratio.
func computeWanderClump(fx fixture, wander []float64, blockSize int) float64 {
	var total, mean float64
	for _, w := range wander {
		total += w
	}
	n := float64(len(wander))
	if n == 0 {
		return 0
	}
	mean = total / n
	var perCellVar float64
	for _, w := range wander {
		d := w - mean
		perCellVar += d * d
	}
	perCellVar /= n
	if perCellVar < 1e-12 {
		return 0
	}
	// Build a present-mask grid of wander values.
	type wcell struct {
		present bool
		w       float64
	}
	grid := make([]wcell, fx.width*fx.height)
	for i, c := range fx.cells {
		grid[c.Row*fx.width+c.Col] = wcell{present: true, w: wander[i]}
	}
	bw := (fx.width + blockSize - 1) / blockSize
	bh := (fx.height + blockSize - 1) / blockSize
	var blockMeans []float64
	for by := 0; by < bh; by++ {
		for bx := 0; bx < bw; bx++ {
			var sum float64
			var count int
			for dy := 0; dy < blockSize; dy++ {
				y := by*blockSize + dy
				if y >= fx.height {
					break
				}
				for dx := 0; dx < blockSize; dx++ {
					x := bx*blockSize + dx
					if x >= fx.width {
						break
					}
					if g := grid[y*fx.width+x]; g.present {
						sum += g.w
						count++
					}
				}
			}
			if count > 0 {
				blockMeans = append(blockMeans, sum/float64(count))
			}
		}
	}
	if len(blockMeans) == 0 {
		return 0
	}
	var bSum float64
	for _, m := range blockMeans {
		bSum += m
	}
	bMean := bSum / float64(len(blockMeans))
	var bVar float64
	for _, m := range blockMeans {
		d := m - bMean
		bVar += d * d
	}
	bVar /= float64(len(blockMeans))
	return bVar / perCellVar
}

// centerErrorGrid subtracts the mean error vector from every opaque
// cell, in place. Required before blockvar / maxdircorr — see the
// caller comment for why.
func centerErrorGrid(grid []errPixel) {
	var mR, mG, mB float64
	var n int
	for _, e := range grid {
		if !e.present {
			continue
		}
		mR += e.eR
		mG += e.eG
		mB += e.eB
		n++
	}
	if n == 0 {
		return
	}
	mR /= float64(n)
	mG /= float64(n)
	mB /= float64(n)
	for i, e := range grid {
		if !e.present {
			continue
		}
		grid[i].eR = e.eR - mR
		grid[i].eG = e.eG - mG
		grid[i].eB = e.eB - mB
	}
}

// errPixel holds the error vector for one cell (output - input). The
// `present` flag distinguishes unfilled pixels (transparent regions
// in the multi-view strips) from real cells that happen to have zero
// error.
type errPixel struct {
	present    bool
	eR, eG, eB float64
}

// buildErrorGrid materializes the per-pixel error vector field on a
// w×h canvas for spatial-structure analysis. Transparent cells leave
// errPixel{present: false}.
func buildErrorGrid(fx fixture, pal [][3]uint8, assigns []int32) []errPixel {
	grid := make([]errPixel, fx.width*fx.height)
	for i, c := range fx.cells {
		p := pal[assigns[i]]
		grid[c.Row*fx.width+c.Col] = errPixel{
			present: true,
			eR:      float64(p[0]) - float64(c.Color[0]),
			eG:      float64(p[1]) - float64(c.Color[1]),
			eB:      float64(p[2]) - float64(c.Color[2]),
		}
	}
	return grid
}

// perPixelVar returns the average squared error vector magnitude
// (uncentered variance) and the opaque-cell count. Returns 0,0 when
// the grid is empty.
func perPixelVar(grid []errPixel) (float64, int) {
	var v float64
	var n int
	for _, e := range grid {
		if !e.present {
			continue
		}
		v += e.eR*e.eR + e.eG*e.eG + e.eB*e.eB
		n++
	}
	if n == 0 {
		return 0, 0
	}
	return v / float64(n), n
}

// computeBlockVariance tabulates the multi-scale block-mean variance
// of the error vector field, normalized by the per-pixel error
// variance. Returns one ratio per requested block scale.
//
// At scale S, the canvas is partitioned into S×S blocks; for each
// block, the mean error vector is computed over the block's opaque
// pixels (transparent cells skipped). The variance of those block-
// mean vectors (across all blocks with at least one opaque pixel) is
// divided by the per-pixel error variance.
//
// White noise gives ratio ≈ 1/S² (variance averages out as 1/N for
// N independent samples per block). Blue noise gives ratio < 1/S²
// (high-frequency error cancels). Banding at scale B keeps the ratio
// close to 1 at S=B, and produces a visible bump in the curve.
//
// Caveat: square-block averaging is direction-blind. Diagonal stripe
// patterns (FS scanline output) average to near-zero per square block
// at all scales but are obvious to the eye — see computeMaxDirCorr
// for the directional companion metric.
func computeBlockVariance(grid []errPixel, w, h int, scales []int) map[int]float64 {
	perPixVar, _ := perPixelVar(grid)
	if perPixVar < 1e-9 {
		out := make(map[int]float64, len(scales))
		for _, s := range scales {
			out[s] = 0
		}
		return out
	}
	out := make(map[int]float64, len(scales))
	for _, s := range scales {
		out[s] = blockMeanVar(grid, w, h, s) / perPixVar
	}
	return out
}

// computeMaxDirCorr returns, for each requested distance d, the max
// absolute autocorrelation of the error vector field over 6
// directions sampling 30°-spaced angles in the upper half-plane:
// (d,0), (0,d), (d,d), (d,-d), (d,2d), (2d,d). Each direction's
// autocorrelation is the dot product of error vectors at offset
// (dx, dy), normalized by per-pixel variance — so 0 = uncorrelated
// (good blue-noise signature), 1 = perfectly correlated (banding at
// that distance and direction), -1 = perfectly anti-correlated.
//
// 6 directions instead of 4 because the (1,2)/(2,1) lattice angles
// catch stripe patterns at ~26.5° / ~63.5° that pure axis+diagonal
// sampling misses. Z-order scrambling artifacts in particular tend
// to land at lattice angles other than the cardinal four.
//
// This catches the failure mode block-variance misses: directional
// stripe patterns (e.g., the diagonal stripes FS produces along the
// scanline). A scrambled-Z-order candidate that's truly blue-noise
// should drive both this metric and block-variance to near zero.
func computeMaxDirCorr(grid []errPixel, w, h int, distances []int) map[int]float64 {
	perPixVar, _ := perPixelVar(grid)
	if perPixVar < 1e-9 {
		out := make(map[int]float64, len(distances))
		for _, d := range distances {
			out[d] = 0
		}
		return out
	}
	out := make(map[int]float64, len(distances))
	for _, d := range distances {
		dirs := [6][2]int{{d, 0}, {0, d}, {d, d}, {d, -d}, {d, 2 * d}, {2 * d, d}}
		var maxAbs float64
		for _, dir := range dirs {
			c := autocorr(grid, w, h, dir[0], dir[1]) / perPixVar
			if math.Abs(c) > maxAbs {
				maxAbs = math.Abs(c)
			}
		}
		out[d] = maxAbs
	}
	return out
}

// autocorr is the average dot product of the error vector at (x, y)
// with the error vector at (x+dx, y+dy), over all pairs where both
// are opaque and in bounds.
func autocorr(grid []errPixel, w, h, dx, dy int) float64 {
	var sum float64
	var n int
	for y := 0; y < h; y++ {
		ny := y + dy
		if ny < 0 || ny >= h {
			continue
		}
		for x := 0; x < w; x++ {
			nx := x + dx
			if nx < 0 || nx >= w {
				continue
			}
			a := grid[y*w+x]
			b := grid[ny*w+nx]
			if !a.present || !b.present {
				continue
			}
			sum += a.eR*b.eR + a.eG*b.eG + a.eB*b.eB
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// blockMeanVar returns the average squared magnitude of per-block
// mean error vectors. Blocks with no opaque pixels are skipped.
//
// Partial blocks at the right/bottom edge (when w or h isn't a
// multiple of s) are included with their actual cell count rather
// than truncated, so we don't silently drop up to s-1 rows/cols on
// fixtures whose dimensions aren't clean multiples of every scale
// in the table (the multi-view PNG fixtures are not).
func blockMeanVar(grid []errPixel, w, h, s int) float64 {
	var sum float64
	var blockCount int
	for by := 0; by < h; by += s {
		yEnd := by + s
		if yEnd > h {
			yEnd = h
		}
		for bx := 0; bx < w; bx += s {
			xEnd := bx + s
			if xEnd > w {
				xEnd = w
			}
			var mR, mG, mB float64
			var n int
			for y := by; y < yEnd; y++ {
				for x := bx; x < xEnd; x++ {
					e := grid[y*w+x]
					if !e.present {
						continue
					}
					mR += e.eR
					mG += e.eG
					mB += e.eB
					n++
				}
			}
			if n == 0 {
				continue
			}
			fn := float64(n)
			mR /= fn
			mG /= fn
			mB /= fn
			sum += mR*mR + mG*mG + mB*mB
			blockCount++
		}
	}
	if blockCount == 0 {
		return 0
	}
	return sum / float64(blockCount)
}

// ----- output -----

func printHeader(scales []int) {
	parts := make([]string, len(scales))
	for i, s := range scales {
		parts[i] = fmt.Sprintf("%5d", s)
	}
	fmt.Printf("  %-16s %10s %8s %8s %8s %8s %8s %8s   blockvar(S=%s)   maxdircorr(d=1,4)\n",
		"mode", "drift_lin", "pcp2_ΔE", "pcp8_ΔE", "wander_ΔE", "wclump", "p50_ΔE", "p99_ΔE", strings.Join(parts, ","))
}

func printRow(name string, m metrics, scales []int) {
	pcellP50 := percentile(m.pcell, 0.50)
	pcellP99 := percentile(m.pcell, 0.99)
	bvParts := make([]string, len(scales))
	for i, s := range scales {
		bvParts[i] = fmt.Sprintf("%5.3f", m.blockVar[s])
	}
	fmt.Printf("  %-16s %10.2f %8.2f %8.2f %8.2f %8.3f %8.1f %8.1f   %s        %5.3f %5.3f\n",
		name, m.driftLinDE, m.percep2, m.percep8, m.wanderDE, m.wanderClump, pcellP50, pcellP99, strings.Join(bvParts, " "),
		m.maxDirCorr[1], m.maxDirCorr[4])
}

func writeOutputPNG(path string, fx fixture, pal [][3]uint8, assigns []int32) error {
	img := image.NewRGBA(image.Rect(0, 0, fx.width, fx.height))
	// Fill with opaque white so transparent regions (multi-view
	// fixture gaps) render readably in any image viewer instead of
	// leaving zero-alpha pixels that some viewers display as black.
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)
	for i, c := range fx.cells {
		p := pal[assigns[i]]
		img.SetRGBA(c.Col, c.Row, color.RGBA{R: p[0], G: p[1], B: p[2], A: 255})
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// ----- helpers -----

// printAssignmentDistribution emits a side-by-side comparison of the
// fraction of cells each algorithm assigned to each palette entry,
// alongside the OPTIMAL mix — the proportions that, if achievable,
// would make output average exactly equal input average in linear
// light (the space drift_lin_ΔE measures).
//
// Output drift is determined entirely by the difference between
// actual assignment proportions and the optimal proportions. So
// gaps in this table directly explain each algorithm's drift_lin_ΔE
// reported above. If FS's mix is close to optimal and dizzy's is
// off, the gap pinpoints which palette entries dizzy over- or
// under-uses to land at its drift number.
func printAssignmentDistribution(fx fixture, pal [][3]uint8, modes []dmode, modeAssigns map[string][]int32) {
	fmt.Println("  palette assignment distribution (% of cells assigned to each palette entry):")
	fmt.Print("    palette entry    ")
	for _, m := range modes {
		if _, ok := modeAssigns[m.name]; !ok {
			continue
		}
		fmt.Printf("%-9s ", abbreviateModeName(m.name))
	}
	fmt.Println("optimal")
	// Compute optimal mix from the input average in LINEAR light — the
	// space the error-diffusion modes conserve and drift_lin_ΔE
	// measures. A byte-space solve would produce proportions that no
	// correctly-conserving mode should match.
	var iR, iG, iB float64
	for _, c := range fx.cells {
		iR += srgb8ToLinear(c.Color[0])
		iG += srgb8ToLinear(c.Color[1])
		iB += srgb8ToLinear(c.Color[2])
	}
	n := float64(len(fx.cells))
	iR /= n
	iG /= n
	iB /= n
	optimal, optimalOK := optimalPaletteMix(pal, [3]float64{iR, iG, iB})
	for k, p := range pal {
		fmt.Printf("    [%d] #%02X%02X%02X    ", k, p[0], p[1], p[2])
		for _, m := range modes {
			assigns, ok := modeAssigns[m.name]
			if !ok {
				continue
			}
			count := 0
			for _, a := range assigns {
				if int(a) == k {
					count++
				}
			}
			fmt.Printf("%8.2f%% ", 100*float64(count)/n)
		}
		if optimalOK {
			fmt.Printf("%7.2f%%", 100*optimal[k])
		} else {
			fmt.Print("    n/a")
		}
		fmt.Println()
	}
}

// abbreviateModeName trims long mode names to fit the assignment-
// distribution table columns. Keeps the most distinguishing
// substring; full names are printed elsewhere.
func abbreviateModeName(name string) string {
	switch name {
	case "floyd-steinberg":
		return "fs"
	case "dizzy-corrected":
		return "dc"
	}
	return name
}

// optimalPaletteMix solves for non-negative proportions
// (p_0, ..., p_{K-1}) summing to 1 that minimize
// ||Σ p_k * pal_k - target||² in RGB space. For K=4 the system
// (3 RGB equations + sum-to-1) is exactly determined and we solve
// it directly via Gaussian elimination. Returns ok=false for
// other K, or when the solver hits a degenerate matrix.
//
// Negative proportions in the result indicate the target lies
// outside the convex hull of the palette — no valid mix can hit
// it exactly. Caller should treat that as a signal that the
// "optimal" mix is unreachable; we return the unconstrained
// solution anyway because the negative-proportion magnitudes are
// still informative (showing which palette entry's contribution
// would have to be "subtracted" to match the target).
func optimalPaletteMix(pal [][3]uint8, target [3]float64) ([]float64, bool) {
	K := len(pal)
	if K != 4 {
		return nil, false
	}
	// Build the 4×4 system in LINEAR light (target must also be linear):
	//   [R0 R1 R2 R3] [p0]   [iR]
	//   [G0 G1 G2 G3] [p1] = [iG]
	//   [B0 B1 B2 B3] [p2]   [iB]
	//   [ 1  1  1  1] [p3]   [ 1]
	A := [4][5]float64{}
	for k := 0; k < 4; k++ {
		A[0][k] = srgb8ToLinear(pal[k][0])
		A[1][k] = srgb8ToLinear(pal[k][1])
		A[2][k] = srgb8ToLinear(pal[k][2])
		A[3][k] = 1.0
	}
	A[0][4] = target[0]
	A[1][4] = target[1]
	A[2][4] = target[2]
	A[3][4] = 1.0
	// Gaussian elimination with partial pivoting.
	for i := 0; i < 4; i++ {
		// Pivot
		maxRow := i
		for r := i + 1; r < 4; r++ {
			if math.Abs(A[r][i]) > math.Abs(A[maxRow][i]) {
				maxRow = r
			}
		}
		A[i], A[maxRow] = A[maxRow], A[i]
		if math.Abs(A[i][i]) < 1e-9 {
			return nil, false
		}
		// Eliminate below
		for r := i + 1; r < 4; r++ {
			f := A[r][i] / A[i][i]
			for c := i; c <= 4; c++ {
				A[r][c] -= f * A[i][c]
			}
		}
	}
	// Back-substitute
	out := make([]float64, 4)
	for i := 3; i >= 0; i-- {
		s := A[i][4]
		for c := i + 1; c < 4; c++ {
			s -= A[i][c] * out[c]
		}
		out[i] = s / A[i][i]
	}
	return out, true
}

// nearestPaletteIdx returns the index of the palette entry closest
// to color in unweighted RGB Euclidean distance. Used to build the
// per-cell cluster assignment for the per-cluster drift diagnostic;
// the choice of distance metric (RGB vs Lab) doesn't matter much
// for the diagnostic since clusters are coarse and stable.
func nearestPaletteIdx(color [3]uint8, pal [][3]uint8) int {
	bestIdx := 0
	bestD := 1 << 30
	for i, p := range pal {
		dr := int(color[0]) - int(p[0])
		dg := int(color[1]) - int(p[1])
		db := int(color[2]) - int(p[2])
		d := dr*dr + dg*dg + db*db
		if d < bestD {
			bestD = d
			bestIdx = i
		}
	}
	return bestIdx
}

// printClusterDrifts emits one row per cluster: the palette color
// that defines the cluster, the cell count assigned to it, and the
// drift (avg output - avg input) restricted to those cells. If
// clusters drift in the same direction with similar magnitude, global
// correction is as good as per-cluster could be. If they drift in
// different directions or differ greatly in magnitude, segmented
// correction has room to improve.
//
// All averaging is in LINEAR light (the space the error-diffusion
// modes conserve; see drift_lin_ΔE at the top of this file). For
// display, the two linear means are re-encoded to sRGB: ΔsRGB is the
// per-channel difference of the encoded means (0-255 scale) and ΔE is
// their Lab distance — the drift the eye would see in that cluster.
// The cluster partition itself (nearestPaletteIdx over input bytes) is
// just a labeling and is unchanged.
func printClusterDrifts(fx fixture, pal [][3]uint8, assigns []int32, cellCluster []int) {
	type bucket struct {
		count            int
		iR, iG, iB       float64
		oR, oG, oB       float64
	}
	buckets := make([]bucket, len(pal))
	for i, c := range fx.cells {
		k := cellCluster[i]
		b := &buckets[k]
		b.count++
		b.iR += srgb8ToLinear(c.Color[0])
		b.iG += srgb8ToLinear(c.Color[1])
		b.iB += srgb8ToLinear(c.Color[2])
		a := assigns[i]
		b.oR += srgb8ToLinear(pal[a][0])
		b.oG += srgb8ToLinear(pal[a][1])
		b.oB += srgb8ToLinear(pal[a][2])
	}
	totalCells := 0
	for _, b := range buckets {
		totalCells += b.count
	}
	for k, b := range buckets {
		if b.count == 0 {
			fmt.Printf("    cluster #%02X%02X%02X: 0 cells (empty)\n",
				pal[k][0], pal[k][1], pal[k][2])
			continue
		}
		n := float64(b.count)
		// Re-encode the linear means to sRGB (0-255 floats) for display
		// and for the Lab ΔE.
		inR := linearToSrgb255(b.iR / n)
		inG := linearToSrgb255(b.iG / n)
		inB := linearToSrgb255(b.iB / n)
		outR := linearToSrgb255(b.oR / n)
		outG := linearToSrgb255(b.oG / n)
		outB := linearToSrgb255(b.oB / n)
		avgIn := [3]uint8{uint8(inR), uint8(inG), uint8(inB)}
		dR := outR - inR
		dG := outG - inG
		dB := outB - inB
		iL, iA, iBl := toLab(inR, inG, inB)
		oL, oA, oBl := toLab(outR, outG, outB)
		dE := math.Sqrt((iL-oL)*(iL-oL) + (iA-oA)*(iA-oA) + (iBl-oBl)*(iBl-oBl))
		// Cell count alone is misleading — palette-Voronoi clustering
		// in RGB Euclidean space biases mid-luminance pixels toward
		// whichever palette entry is mid-luminance, regardless of
		// hue. Reporting avg input color exposes what each cluster
		// actually contains: a 97% Grey-cluster doesn't mean "97%
		// visually grey," it means "97% have palette Grey as their
		// nearest RGB neighbor" — which can include warm-brown
		// pixels that the eye reads as brick.
		fmt.Printf("    cluster #%02X%02X%02X (%5.1f%% cells, avg in #%02X%02X%02X): ΔsRGB=(%+6.2f,%+6.2f,%+6.2f)  ΔE=%5.2f\n",
			pal[k][0], pal[k][1], pal[k][2],
			100*n/float64(totalCells),
			avgIn[0], avgIn[1], avgIn[2],
			dR, dG, dB, dE)
	}
}

// percentile uses lower-floor nearest-rank interpolation: pct(0.5)
// of a 2-element slice returns the first element, not the average.
// Adequate for diagnostic display; don't rely on this matching any
// specific statistical convention.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[int(p*float64(len(sorted)-1))]
}

func toLab(r, g, b float64) (float64, float64, float64) {
	c := colorful.Color{R: r / 255, G: g / 255, B: b / 255}
	L, A, B := c.Lab()
	return L * 100, A * 100, B * 100
}

// srgb8ToLinear converts an sRGB byte (0-255) to linear light in [0,1]
// using the standard IEC 61966-2-1 transfer function. Reimplemented
// locally because the voxel package's LUT (srgbToLinearLUT) is
// unexported; this matches it exactly.
func srgb8ToLinear(c uint8) float64 {
	x := float64(c) / 255
	if x <= 0.04045 {
		return x / 12.92
	}
	return math.Pow((x+0.055)/1.055, 2.4)
}

// linearToSrgb255 is the inverse of srgb8ToLinear, returning an sRGB
// value on the 0-255 float scale (not rounded to a byte) so the result
// can be fed straight into toLab.
func linearToSrgb255(x float64) float64 {
	var s float64
	if x <= 0.0031308 {
		s = 12.92 * x
	} else {
		s = 1.055*math.Pow(x, 1/2.4) - 0.055
	}
	return s * 255
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
