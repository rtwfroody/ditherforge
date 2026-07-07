package export3mf

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// rawFieldFloat reads a numeric field from the embedded raw process JSON,
// resolving OrcaSlicer percentage forms ("105%") against the nozzle diameter.
// The test derives its expected shell thickness from the embedded profile
// itself rather than hardcoding blind guesses.
func rawFieldFloat(t *testing.T, raw map[string]any, key string, nozzleDia float64) float64 {
	t.Helper()
	s, ok := raw[key].(string)
	if !ok {
		t.Fatalf("field %q missing or not a string in raw profile", key)
	}
	s = strings.TrimSpace(s)
	if pct, isPct := strings.CutSuffix(s, "%"); isPct {
		v, err := strconv.ParseFloat(strings.TrimSpace(pct), 64)
		if err != nil {
			t.Fatalf("parse percentage %q for %q: %v", s, key, err)
		}
		return v / 100 * nozzleDia
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("parse %q for %q: %v", s, key, err)
	}
	return v
}

// expectedShell computes the reference shell thickness straight from the raw
// embedded profile, using the same outer + (loops-1)*inner formula.
func expectedShell(t *testing.T, printerID, diameter string, layerHeight float32) float32 {
	t.Helper()
	p := FindPrinter(printerID)
	if p == nil {
		t.Fatalf("printer %q not in registry", printerID)
	}
	n := p.FindNozzle(diameter)
	if n == nil {
		t.Fatalf("nozzle %q not found on %s", diameter, printerID)
	}
	proc := n.ClosestProcess(layerHeight)
	if proc == nil {
		t.Fatalf("no process for %s nozzle %s", printerID, diameter)
	}
	raw, err := loadProcessProfile(printerID, proc)
	if err != nil {
		t.Fatalf("load raw process: %v", err)
	}
	nozzleDia, err := strconv.ParseFloat(diameter, 64)
	if err != nil {
		t.Fatalf("parse nozzle diameter %q: %v", diameter, err)
	}
	loops := 1
	if s, ok := raw["wall_loops"].(string); ok {
		if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && v > 1 {
			loops = v
		}
	}
	outer := rawFieldFloat(t, raw, "outer_wall_line_width", nozzleDia)
	inner := rawFieldFloat(t, raw, "inner_wall_line_width", nozzleDia)
	return float32(outer + float64(loops-1)*inner)
}

func TestShellThicknessMM_Absolute(t *testing.T) {
	// snapmaker_u1 0.4mm nozzle at layer height 0.20: absolute mm widths.
	const printerID, diameter, layerHeight = "snapmaker_u1", "0.4", float32(0.20)
	p := FindPrinter(printerID)
	if p == nil {
		t.Fatalf("printer %q not in registry", printerID)
	}
	n := p.FindNozzle(diameter)
	if n == nil {
		t.Fatalf("nozzle %q not found", diameter)
	}
	proc := n.ClosestProcess(layerHeight)
	if proc == nil {
		t.Fatalf("no process for layer height %.2f", layerHeight)
	}

	got, ok := ShellThicknessMM(printerID, n, proc)
	if !ok {
		t.Fatal("ShellThicknessMM returned ok=false for a real profile")
	}
	want := expectedShell(t, printerID, diameter, layerHeight)
	if math.Abs(float64(got-want)) > 1e-4 {
		t.Fatalf("shell thickness = %.4f mm, want %.4f mm", got, want)
	}
	// Sanity: this profile is 2 walls of 0.42 (outer) + 0.45 (inner) = 0.87mm.
	if math.Abs(float64(got-0.87)) > 1e-4 {
		t.Fatalf("shell thickness = %.4f mm, expected ~0.87 mm for snapmaker_u1 0.4/0.20", got)
	}
}

func TestShellThicknessMM_Percentage(t *testing.T) {
	// snapmaker_u1 0.6mm nozzle at 0.20 uses percentage-of-nozzle widths
	// ("105%"/"112%"), exercising the percentage-resolution path.
	const printerID, diameter, layerHeight = "snapmaker_u1", "0.6", float32(0.20)
	p := FindPrinter(printerID)
	if p == nil {
		t.Fatalf("printer %q not in registry", printerID)
	}
	n := p.FindNozzle(diameter)
	if n == nil {
		t.Skipf("nozzle %q not found on %s; skipping percentage test", diameter, printerID)
	}
	proc := n.ClosestProcess(layerHeight)
	if proc == nil {
		t.Fatalf("no process for layer height %.2f", layerHeight)
	}

	got, ok := ShellThicknessMM(printerID, n, proc)
	if !ok {
		t.Fatal("ShellThicknessMM returned ok=false for a real profile")
	}
	want := expectedShell(t, printerID, diameter, layerHeight)
	if math.Abs(float64(got-want)) > 1e-4 {
		t.Fatalf("shell thickness = %.4f mm, want %.4f mm", got, want)
	}
	// The absolute-mm path would have produced a much smaller number; confirm
	// the percentages actually scaled against the 0.6mm nozzle.
	if got <= 0.87 {
		t.Fatalf("percentage widths did not scale against nozzle: got %.4f mm", got)
	}
}

func TestShellThicknessMM_NotFound(t *testing.T) {
	// nil nozzle/process → ok=false (the pipeline fallback path).
	if _, ok := ShellThicknessMM("snapmaker_u1", nil, nil); ok {
		t.Fatal("expected ok=false for nil nozzle/process")
	}
	// A bogus printer ID means loadProcessProfile can't read the file.
	p := FindPrinter("snapmaker_u1")
	n := p.FindNozzle("0.4")
	proc := n.ClosestProcess(0.20)
	if _, ok := ShellThicknessMM("no_such_printer", n, proc); ok {
		t.Fatal("expected ok=false for unknown printer ID")
	}
}
