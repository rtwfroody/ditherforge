// Command palettesearch is an offline ground-truth palette-search harness.
//
// For one model + settings (the same settings JSON ditherforge-cli consumes)
// it evaluates candidate filament palettes against ground truth: the pipeline
// runs once up through voxelize, then every candidate palette is dithered,
// TD-simulated, rendered directly from the cells, and scored by multi-scale
// Gaussian-blur Lab ΔE against a palette-independent sampled-target render. It
// prints a ranked table and a "regret" comparison against the production fast
// scorer's pick, and writes results.csv / results.json / PNGs to --out.
//
// This is calibration ground truth for improving the fast selection scorer; it
// is intentionally slow and exhaustive.
package main

import (
	"context"
	"fmt"
	"image"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/alexflint/go-arg"

	"github.com/rtwfroody/ditherforge/internal/collection"
	"github.com/rtwfroody/ditherforge/internal/debugrender"
	"github.com/rtwfroody/ditherforge/internal/diskcache"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/render"
	"github.com/rtwfroody/ditherforge/internal/settings"
)

type Args struct {
	Settings    string `arg:"positional,required" help:"settings JSON file (same format as ditherforge-cli)"`
	Inventory   string `arg:"--inventory" help:"inventory file to search (overrides the settings' collection)"`
	Res         int    `arg:"--res" default:"512" help:"square render resolution"`
	Sigmas      string `arg:"--sigmas" default:"0,2,4,8,16" help:"comma-separated blur sigmas (px) to score at"`
	RankSigmas  string `arg:"--rank-sigmas" default:"2,8" help:"comma-separated sigmas averaged into the rank key"`
	Workers     int    `arg:"--workers" help:"candidate worker pool size (default GOMAXPROCS)"`
	Out         string `arg:"--out" default:"." help:"output directory for results.csv/json and PNGs"`
	RenderTop   int    `arg:"--render-top" default:"3" help:"write PNGs for the top-N candidates"`
	Limit       int    `arg:"--limit" help:"evaluate only the first N candidates (0 = all)"`
	Views       string `arg:"--views" help:"comma subset of front,side,top,persp (default all)"`
	Quiet         bool   `arg:"--quiet" help:"suppress the one-time voxelize progress bar (per-candidate dither logs are always suppressed in a sweep)"`
	RegretTable   string `arg:"--regret-table" help:"skip the sweep: run the production scorer and report its rank in this existing results.csv"`
	RenderPalette string `arg:"--render-palette" help:"skip the sweep: dither and render one explicit palette (comma-separated free-slot hexes, e.g. \"#06924D,#55331A\") to --out"`
	RenderPrefix  string `arg:"--render-prefix" default:"render" help:"filename prefix for --render-palette PNGs"`
}

func main() {
	var args Args
	arg.MustParse(&args)

	s, legacyAbsoluteUnits, err := settings.Load(args.Settings)
	if err != nil {
		fatalf("Error: %v", err)
	}
	if s.InputFile == "" {
		fatalf("Error: settings file has no input model (inputFile).")
	}
	if _, statErr := os.Stat(s.InputFile); statErr != nil {
		fatalf("Error: input model %q not found: %v", s.InputFile, statErr)
	}

	// Resolve the inventory collection exactly as ditherforge-cli does, so the
	// same settings file yields the same locked/inventory context.
	var mgr *collection.Manager
	if s.InventoryCollection != "" {
		if m, mErr := collection.NewManager(); mErr != nil {
			fmt.Fprintf(os.Stderr, "warning: filament collections unavailable (%v)\n", mErr)
		} else {
			mgr = m
		}
	}
	opts, err := settings.ToOptions(s, mgr)
	if err != nil {
		fatalf("Error: %v", err)
	}
	opts.LegacyAbsoluteUnits = legacyAbsoluteUnits

	sigmas, err := parseFloatList(args.Sigmas)
	if err != nil {
		fatalf("Error: --sigmas: %v", err)
	}
	rankSigmas, err := parseFloatList(args.RankSigmas)
	if err != nil {
		fatalf("Error: --rank-sigmas: %v", err)
	}

	// --quiet silences the one-time voxelize progress bar; the per-candidate
	// dither flood is suppressed inside the sweep regardless.
	var tracker progress.Tracker = progress.NewCLITracker()
	if args.Quiet {
		tracker = progress.NullTracker{}
	}
	cfg := pipeline.PaletteSearchConfig{
		Res:        args.Res,
		Sigmas:     sigmas,
		RankSigmas: rankSigmas,
		Workers:    args.Workers,
		Limit:      args.Limit,
		RenderTop:  args.RenderTop,
		OutDir:     args.Out,
		Tracker:    tracker,
	}
	if args.Views != "" {
		for _, v := range strings.Split(args.Views, ",") {
			if v = strings.TrimSpace(v); v != "" {
				cfg.Views = append(cfg.Views, v)
			}
		}
	}
	if args.Inventory != "" {
		inv, err := palette.ParseInventoryFile(args.Inventory)
		if err != nil {
			fatalf("Error: --inventory: %v", err)
		}
		cfg.Inventory = inv
	}

	cache := pipeline.NewStageCache()
	if dir, err := diskcache.DefaultDir(); err == nil {
		if d, derr := diskcache.Open(dir); derr == nil {
			cache.SetDisk(d)
		}
	}

	// Regret-only mode: skip the sweep, run just voxelize + the production
	// scorer, and report the pick's rank in the given table. Seconds-to-minutes,
	// so `for MU in ...; do palettesearch --regret-table ...; done` tunes the
	// scorer against a fixed ground-truth table (the env overrides
	// DITHERFORGE_SELECT_WASH/MU/NU are honored in-process by SelectFromInventory).
	if args.RegretTable != "" {
		rep, err := pipeline.RunRegretLookup(context.Background(), cache, opts, cfg, args.RegretTable)
		if err != nil {
			fatalf("Error: %v", err)
		}
		printRegretReport(rep, args.RegretTable)
		return
	}

	// Render-only mode: skip the sweep, dither and render one explicit palette
	// to --out. Reuses the sweep's voxelize + dither + render path so the PNGs
	// are framed identically to the sweep's target/top renders.
	if args.RenderPalette != "" {
		hexes := parseHexList(args.RenderPalette)
		if len(hexes) == 0 {
			fatalf("Error: --render-palette: no hexes given")
		}
		if err := pipeline.RenderExplicitPalette(context.Background(), cache, opts, cfg, hexes, args.RenderPrefix, newRenderer()); err != nil {
			fatalf("Error: %v", err)
		}
		fmt.Printf("Rendered %s to %s (prefix %q)\n", strings.Join(hexes, " "), args.Out, args.RenderPrefix)
		return
	}

	res, err := pipeline.RunPaletteSearch(context.Background(), cache, opts, cfg, newRenderer())
	if err != nil {
		fatalf("Error: %v", err)
	}

	printReport(res)
}

// printRegretReport prints the regret-only scorecard for --regret-table.
func printRegretReport(rep *pipeline.RegretReport, tablePath string) {
	if len(rep.LockedHexes) > 0 {
		fmt.Printf("Locked: %s\n", strings.Join(rep.LockedHexes, " "))
	}
	fmt.Printf("Table:  %s (%d candidates)\n\n", tablePath, rep.Total)
	fmt.Printf("REGRET: production fast scorer picked %s\n", describeHexes(rep.PickHexes, rep.PickLabels))
	fmt.Printf("        its rank %d/%d (rankKey %.3f); winner %s rankKey %.3f; delta %.3f\n",
		rep.Rank, rep.Total, rep.RankKey,
		describeHexes(rep.WinnerHexes, rep.WinnerLabels), rep.WinnerRankKey, rep.Delta)
}

// describeHexes formats a "hex(label)" list, tolerating a missing label list.
func describeHexes(hexes, labels []string) string {
	parts := make([]string, len(hexes))
	for i := range hexes {
		if i < len(labels) && labels[i] != "" {
			parts[i] = fmt.Sprintf("%s(%s)", hexes[i], labels[i])
		} else {
			parts[i] = hexes[i]
		}
	}
	return strings.Join(parts, " ")
}

// newRenderer builds a SplatRenderFunc over debugrender. Framing is shared:
// the first mesh seen for a view fixes that view's bounds, and every later
// render (all with bit-identical geometry) reuses it, so the target and every
// candidate are framed identically.
func newRenderer() pipeline.SplatRenderFunc {
	viewByName := make(map[string]debugrender.View, len(debugrender.DefaultViews))
	for _, v := range debugrender.DefaultViews {
		viewByName[v.Name] = v
	}
	var mu sync.Mutex
	boundsByView := make(map[string]render.Bounds)

	return func(mesh *pipeline.MeshData, viewName string, res int) *image.RGBA {
		v := viewByName[viewName]
		mu.Lock()
		b, ok := boundsByView[viewName]
		if !ok {
			b = debugrender.MeshDataProjectedBounds(mesh, v)
			boundsByView[viewName] = b
		}
		mu.Unlock()
		return debugrender.RenderPipelineMeshCulledWithBounds(mesh, v, res, b).ToRGBA()
	}
}

func printReport(res *pipeline.PaletteSearchResult) {
	fmt.Printf("\nGround-truth palette search: %d candidates, %d cells, %.3f s/candidate\n",
		res.NumCandidates, res.NumCells, res.SecondsPerCandidate)
	if len(res.LockedHexes) > 0 {
		fmt.Printf("Locked: %s\n", strings.Join(res.LockedHexes, " "))
	}
	fmt.Printf("Rank sigmas: %v\n\n", res.RankSigmas)

	fmt.Printf("%-5s %-9s %-9s  %s\n", "rank", "rankKey", "p99(σ2)", "palette (free slots)")
	n := 10
	if len(res.Candidates) < n {
		n = len(res.Candidates)
	}
	for i := 0; i < n; i++ {
		c := res.Candidates[i]
		fmt.Printf("%-5d %-9.3f %-9.3f  %s\n", c.Rank, c.RankKey, c.P99Sigma2, describe(c))
	}

	fmt.Println()
	if res.Production != nil {
		p := res.Production
		winner := res.Candidates[0]
		fmt.Printf("REGRET: production fast scorer picked %s\n", describe(*p))
		fmt.Printf("        its rank %d/%d (rankKey %.3f); winner rankKey %.3f; delta %.3f%s\n",
			p.Rank, res.NumCandidates, p.RankKey, winner.RankKey, p.RankKey-winner.RankKey,
			inSweepNote(res.ProductionInSweep))
	} else {
		fmt.Println("REGRET: production pick unavailable")
	}
}

func describe(c pipeline.PaletteCandidate) string {
	return describeHexes(c.Hexes, c.Labels)
}

func inSweepNote(inSweep bool) string {
	if inSweep {
		return ""
	}
	return " [evaluated as an extra candidate, outside the enumerated sweep]"
}

func parseFloatList(s string) ([]float64, error) {
	var out []float64
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		v, err := strconv.ParseFloat(tok, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", tok)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no values")
	}
	return out, nil
}

// parseHexList splits a comma-separated hex list, trimming whitespace and
// dropping empty entries. Order is preserved; casing/format are left to the
// caller's inventory lookup (which upper-cases).
func parseHexList(s string) []string {
	var out []string
	for _, tok := range strings.Split(s, ",") {
		if tok = strings.TrimSpace(tok); tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
