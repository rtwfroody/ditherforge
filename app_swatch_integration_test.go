package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/diskcache"
	"github.com/rtwfroody/ditherforge/internal/export3mf"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
	"github.com/rtwfroody/ditherforge/internal/settings"
	"github.com/rtwfroody/ditherforge/internal/swatch"
)

// TestSwatchNoForeignColors runs the full swatch pipeline with the exact palette
// and printer settings that produced the reported third-color contamination
// (ColdWhite+Orange and Black+Orange plates showing Beige at 0.08mm on the
// Snapmaker U1), and asserts every plate/section has ZERO foreign coverage — a
// color that is neither of the plate's two filaments — in the RAW pipeline
// output. It also checks endpoints are exactly 0/1, interiors track nominal, and
// the front and back faces agree (they share one pattern texture).
//
// This runs at the 0.08mm upper layer (the finest, where the bug was reported
// and where the chamfered geometry keeps the raw output clean). NOTE: at coarser
// layers (e.g. 0.20mm) the RAW pipeline output carries a pre-existing residual
// third-color contamination in the interior mixing sections that is independent
// of this geometry (it reproduces identically on the pre-chamfer box); the
// export path scrubs it via swatch.SnapToPairs before writing the 3MF, so the
// shipped plates are clean. Guarding raw==0 here is therefore scoped to 0.08mm.
//
// Uses CGO clip stages, so it is skipped under -short.
func TestSwatchNoForeignColors(t *testing.T) {
	if testing.Short() {
		t.Skip("full swatch pipeline (CGO clip) is slow; skipped under -short")
	}

	// Palette with Beige present alongside the White/Orange pair so a blend can
	// land on the third color — the real repro.
	filaments := []swatch.Filament{
		{Hex: "#080A0D", TD: 0.1, Label: "Black"},
		{Hex: "#C2AB72", TD: 0.5, Label: "Beige"},
		{Hex: "#D9DFE5", TD: 0.3, Label: "ColdWhite"},
		{Hex: "#F67405", TD: 3.3, Label: "Orange"},
	}

	s := settings.Default()
	s.Printer = "snapmaker_u1"
	s.NozzleDiameter = "0.4"
	s.LayerHeight = "0.08" // finest upper layer — where contamination appeared
	s.SizeMode = "scale"
	s.ScaleValue = "1.0"
	s.Dither = "none"
	s.MeshRepair = "none"
	s.SplitEnabled = false
	s.Stickers = nil
	s.Brightness, s.Contrast, s.Saturation = 0, 0, 0
	s.WarpPins = nil
	s.ColorSnap = 0
	s.HonorTD = false
	s.ColorAwareCells = false
	s.BaseColor = nil
	s.ColorSlots = make([]*settings.ColorSlotSetting, len(filaments))
	for i, f := range filaments {
		s.ColorSlots[i] = &settings.ColorSlotSetting{Hex: f.Hex, TD: f.TD, Label: f.Label}
	}
	s.InfillFilament = filaments[0].Hex

	opts, err := settings.ToOptions(s, nil)
	if err != nil {
		t.Fatalf("ToOptions: %v", err)
	}
	blockWidthMM := float64(pipeline.UpperCellSizeMM(opts))
	layer0Z, upperZ := pipeline.SlabHeightsMM(opts)
	plan := swatch.BuildPlan(filaments, blockWidthMM, float64(layer0Z), float64(upperZ))

	dir := t.TempDir()
	objPath, err := swatch.WriteOBJ(plan, dir)
	if err != nil {
		t.Fatalf("WriteOBJ: %v", err)
	}
	s.InputFile = objPath
	opts, err = settings.ToOptions(s, nil)
	if err != nil {
		t.Fatalf("ToOptions: %v", err)
	}

	cache := pipeline.NewStageCache()
	cacheDir := filepath.Join(dir, "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	disk, err := diskcache.Open(cacheDir)
	if err != nil {
		t.Fatalf("diskcache: %v", err)
	}
	cache.SetDisk(disk)

	if _, err := pipeline.RunCached(context.Background(), cache, opts, nil); err != nil {
		t.Fatalf("RunCached: %v", err)
	}

	// Export so the 3MF path (ExportFileWithAssignments) is exercised too.
	verts, faces, rawAssign, _, err := pipeline.OutputFaceAssignments(cache, opts)
	if err != nil {
		t.Fatalf("OutputFaceAssignments: %v", err)
	}
	snapped := swatch.SnapToPairs(plan, verts, faces, rawAssign)
	out := filepath.Join(dir, "swatches.3mf")
	if _, err := pipeline.ExportFileWithAssignments(cache, opts, out, export3mf.Options{
		PrinterID: opts.Printer, NozzleDiameter: opts.NozzleDiameter,
		LayerHeight: opts.LayerHeight, AppVersion: pipeline.VersionSemver,
	}, snapped); err != nil {
		t.Fatalf("export: %v", err)
	}

	// The root-cause fix (row-aligned texture) must eliminate foreign colors in
	// the RAW pipeline output — snapping is only a safety net, so verify raw.
	front, back := swatch.MeasureCoverage(plan, verts, faces, rawAssign)
	for p, plate := range plan.Plates {
		pair := plan.Palette[plate.A].Label + "+" + plan.Palette[plate.B].Label
		for sec := 0; sec < swatch.Sections; sec++ {
			if f := front[p][sec].Foreign; f > 0.005 {
				t.Errorf("plate %d (%s) section %d: front foreign coverage %.3f, want 0", p, pair, sec, f)
			}
			if f := back[p][sec].Foreign; f > 0.005 {
				t.Errorf("plate %d (%s) section %d: back foreign coverage %.3f, want 0", p, pair, sec, f)
			}
			nom := swatch.Coverage(sec)
			if nom == 0 || nom == 1 { // endpoints must be exact
				if got := front[p][sec].B; got != nom {
					t.Errorf("plate %d section %d: endpoint B=%.3f, want %.3f", p, sec, got, nom)
				}
			} else if got := front[p][sec].B; got < nom-0.06 || got > nom+0.06 {
				t.Errorf("plate %d section %d: B=%.3f, want ~%.3f", p, sec, got, nom)
			}
			// Front and back share one pattern texture, so their realized B
			// coverage must agree closely.
			if d := front[p][sec].B - back[p][sec].B; d < -0.06 || d > 0.06 {
				t.Errorf("plate %d section %d: front B=%.3f vs back B=%.3f differ", p, sec, front[p][sec].B, back[p][sec].B)
			}
		}
	}
}
