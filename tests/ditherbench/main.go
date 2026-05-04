// Command ditherbench measures dither algorithm quality across the
// existing PNG fixtures plus a few synthetic uniform-color fixtures
// designed to surface banding artifacts.
//
// Three categories of metric per (fixture, mode):
//
//   - drift_ΔE: avg(output_color) - avg(input_color), measured in
//     Lab ΔE. Small = good; FS-class scores ~0.3, dizzy ~7-8,
//     no-dither up to ~15. The cost dither pays to fix the
//     unavoidable quantization bias.
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
	"sort"
	"strings"

	colorful "github.com/lucasb-eyer/go-colorful"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/voxel"
	"github.com/rtwfroody/ditherforge/tests/inventories"
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
		{"floyd-steinberg", wrapFS},
		{"auto", wrapAuto},
	}
	if *onlyMode != "" {
		var keep []dmode
		for _, m := range modes {
			if strings.Contains(m.name, *onlyMode) {
				keep = append(keep, m)
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
		pal, _, _, err := voxel.ResolvePalette(context.Background(), fx.cells, pcfg, true, progress.NullTracker{})
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
		// Retain assignments per mode so we can do cross-mode
		// diagnostics (palette-assignment distribution, per-cluster
		// drift) without re-running each algorithm.
		modeAssigns := make(map[string][]int32, len(modes))
		for _, m := range modes {
			assigns, err := m.run(context.Background(), fx.cells, pal, nbrs)
			if err != nil {
				fmt.Printf("  %-16s ERROR: %v\n", m.name, err)
				continue
			}
			met := computeMetrics(fx, pal, assigns, blockScales)
			printRow(m.name, met, blockScales)
			modeAssigns[m.name] = assigns

			if *outDir != "" {
				path := filepath.Join(*outDir, fmt.Sprintf("%s.%s.png", fx.name, m.name))
				if werr := writeOutputPNG(path, fx, pal, assigns); werr != nil {
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
			fmt.Println("  per-cluster drift (cluster center = nearest palette in input space, dizzy output):")
			printClusterDrifts(fx, pal, dizzyAssigns, cellCluster)
		}
		// Also dizzy-corrected, since that's what auto-mode picks on
		// borderline scenes — useful to see whether residual drift
		// after correction is concentrated in one cluster.
		if dcAssigns, ok := modeAssigns["dizzy-corrected"]; ok {
			fmt.Println("  per-cluster drift (dizzy-corrected output):")
			printClusterDrifts(fx, pal, dcAssigns, cellCluster)
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
		// here while drift_ΔE blows up.
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
				Color: c,
			})
		}
	}
	return fixture{name: name, cells: cells, width: w, height: h}
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
	return voxel.DitherWithNeighbors(ctx, cells, pal, nbrs, progress.NullTracker{})
}
func wrapDizzyCorrected(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.DitherCorrected(ctx, cells, pal, nbrs, progress.NullTracker{})
}
func wrapFS(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.FloydSteinberg(ctx, cells, pal, nbrs, progress.NullTracker{})
}
func wrapAuto(ctx context.Context, cells []voxel.ActiveCell, pal [][3]uint8, nbrs [][]voxel.Neighbor) ([]int32, error) {
	return voxel.DitherAuto(ctx, cells, pal, nbrs, progress.NullTracker{})
}

// ----- metrics -----

type metrics struct {
	driftDE    float64
	pcell      []float64       // sorted per-cell ΔE
	blockVar   map[int]float64 // scale -> normalized block-mean variance
	maxDirCorr map[int]float64 // distance -> max |autocorrelation| over 8 directions
}

func computeMetrics(fx fixture, pal [][3]uint8, assigns []int32, blockScales []int) metrics {
	// Global drift in Lab ΔE.
	var iR, iG, iB, oR, oG, oB float64
	for i, c := range fx.cells {
		iR += float64(c.Color[0])
		iG += float64(c.Color[1])
		iB += float64(c.Color[2])
		a := assigns[i]
		oR += float64(pal[a][0])
		oG += float64(pal[a][1])
		oB += float64(pal[a][2])
	}
	n := float64(len(fx.cells))
	iL, iA, iBl := toLab(iR/n, iG/n, iB/n)
	oL, oA, oBl := toLab(oR/n, oG/n, oB/n)
	driftDE := math.Sqrt((iL-oL)*(iL-oL) + (iA-oA)*(iA-oA) + (iBl-oBl)*(iBl-oBl))

	// Per-cell ΔE distribution.
	pcell := make([]float64, len(fx.cells))
	for i, c := range fx.cells {
		cL, cA, cB := toLab(float64(c.Color[0]), float64(c.Color[1]), float64(c.Color[2]))
		p := pal[assigns[i]]
		pL, pA, pB := toLab(float64(p[0]), float64(p[1]), float64(p[2]))
		pcell[i] = math.Sqrt((cL-pL)*(cL-pL) + (cA-pA)*(cA-pA) + (cB-pB)*(cB-pB))
	}
	sort.Float64s(pcell)

	// Build the per-pixel error grid, then subtract the global mean
	// error vector before measuring spatial structure. The drift
	// metrics above already report the mean separately; leaving it
	// in here would inflate both blockvar and maxdircorr by a
	// constant proportional to drift_ΔE², making cross-mode
	// comparison unfair (a high-drift mode would look "bandy" even
	// with perfectly white residuals because |μ|² leaks into both
	// the autocorrelation numerator and the per-pixel-variance
	// denominator).
	grid := buildErrorGrid(fx, pal, assigns)
	centerErrorGrid(grid)
	bv := computeBlockVariance(grid, fx.width, fx.height, blockScales)
	mdc := computeMaxDirCorr(grid, fx.width, fx.height, []int{1, 4})

	return metrics{
		driftDE:    driftDE,
		pcell:      pcell,
		blockVar:   bv,
		maxDirCorr: mdc,
	}
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
	fmt.Printf("  %-16s %8s %8s %8s   blockvar(S=%s)   maxdircorr(d=1,4)\n",
		"mode", "drift_ΔE", "p50_ΔE", "p99_ΔE", strings.Join(parts, ","))
}

func printRow(name string, m metrics, scales []int) {
	pcellP50 := percentile(m.pcell, 0.50)
	pcellP99 := percentile(m.pcell, 0.99)
	bvParts := make([]string, len(scales))
	for i, s := range scales {
		bvParts[i] = fmt.Sprintf("%5.3f", m.blockVar[s])
	}
	fmt.Printf("  %-16s %8.2f %8.1f %8.1f   %s        %5.3f %5.3f\n",
		name, m.driftDE, pcellP50, pcellP99, strings.Join(bvParts, " "),
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
// would make output average exactly equal input average.
//
// Output drift is determined entirely by the difference between
// actual assignment proportions and the optimal proportions. So
// gaps in this table directly explain each algorithm's drift_ΔE
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
	// Compute optimal mix from input avg.
	var iR, iG, iB float64
	for _, c := range fx.cells {
		iR += float64(c.Color[0])
		iG += float64(c.Color[1])
		iB += float64(c.Color[2])
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
	// Build the 4×4 system:
	//   [R0 R1 R2 R3] [p0]   [iR]
	//   [G0 G1 G2 G3] [p1] = [iG]
	//   [B0 B1 B2 B3] [p2]   [iB]
	//   [ 1  1  1  1] [p3]   [ 1]
	A := [4][5]float64{}
	for k := 0; k < 4; k++ {
		A[0][k] = float64(pal[k][0])
		A[1][k] = float64(pal[k][1])
		A[2][k] = float64(pal[k][2])
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
// drift (avg output - avg input) restricted to those cells, in
// per-channel ΔRGB plus Lab ΔE. If clusters drift in the same
// direction with similar magnitude, global correction is as good
// as per-cluster could be. If they drift in different directions
// or differ greatly in magnitude, segmented correction has room
// to improve.
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
		b.iR += float64(c.Color[0])
		b.iG += float64(c.Color[1])
		b.iB += float64(c.Color[2])
		a := assigns[i]
		b.oR += float64(pal[a][0])
		b.oG += float64(pal[a][1])
		b.oB += float64(pal[a][2])
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
		avgIn := [3]uint8{
			uint8(b.iR / n),
			uint8(b.iG / n),
			uint8(b.iB / n),
		}
		dR := (b.oR - b.iR) / n
		dG := (b.oG - b.iG) / n
		dB := (b.oB - b.iB) / n
		iL, iA, iBl := toLab(b.iR/n, b.iG/n, b.iB/n)
		oL, oA, oBl := toLab(b.oR/n, b.oG/n, b.oB/n)
		dE := math.Sqrt((iL-oL)*(iL-oL) + (iA-oA)*(iA-oA) + (iBl-oBl)*(iBl-oBl))
		// Cell count alone is misleading — palette-Voronoi clustering
		// in RGB Euclidean space biases mid-luminance pixels toward
		// whichever palette entry is mid-luminance, regardless of
		// hue. Reporting avg input color exposes what each cluster
		// actually contains: a 97% Grey-cluster doesn't mean "97%
		// visually grey," it means "97% have palette Grey as their
		// nearest RGB neighbor" — which can include warm-brown
		// pixels that the eye reads as brick.
		fmt.Printf("    cluster #%02X%02X%02X (%5.1f%% cells, avg in #%02X%02X%02X): ΔRGB=(%+6.2f,%+6.2f,%+6.2f)  ΔE=%5.2f\n",
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

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
