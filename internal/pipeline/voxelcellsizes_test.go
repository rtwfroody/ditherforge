package pipeline

import (
	"math"
	"testing"
)

func approxEq(a, b float32) bool {
	return math.Abs(float64(a-b)) <= 1e-4
}

// TestVoxelCellSizesFromProfile checks that for a real (printer,
// nozzle, layer-height) tuple in the embedded registry, the voxel
// sizes come from the OrcaSlicer process settings rather than the
// bare-nozzle fallback. Scales are left at their zero value so they
// resolve to 1× and the expectations are the raw profile line widths.
//
// The pinned values live in the flattened process JSONs under
// internal/export3mf/profiles/<printer>/process_<nozzle>_<lh>.json.
// If upstream OrcaSlicer ships different defaults on a profile bump
// and the manifest is regenerated, expect this test to fail with a
// self-explanatory diff — the new value lives in the process file the
// test name references.
func TestVoxelCellSizesFromProfile(t *testing.T) {
	cases := []struct {
		name                                            string
		opts                                            Options
		wantLayer0XY, wantUpperXY, wantLayer0Z, wantUpZ float32
	}{
		{
			// snapmaker_u1/process_0.4_0.20.json: initial line width
			// 0.5, line width 0.42, initial Z 0.25, layer Z 0.20.
			// First-layer Z (0.25) DIVERGES from upper Z (0.20) — the
			// marquee non-uniform-Z case in the registry.
			name:         "snapmaker_u1 0.4 0.20",
			opts:         Options{Printer: "snapmaker_u1", NozzleDiameter: 0.4, LayerHeight: 0.20},
			wantLayer0XY: 0.5, wantUpperXY: 0.42, wantLayer0Z: 0.25, wantUpZ: 0.20,
		},
		{
			// Empty Printer must resolve to export3mf.DefaultPrinterID
			// (snapmaker_u1) so the voxel grid agrees with the exported
			// 3MF rather than diverging via the nozzle fallback.
			name:         "empty printer falls back to default printer",
			opts:         Options{Printer: "", NozzleDiameter: 0.4, LayerHeight: 0.20},
			wantLayer0XY: 0.5, wantUpperXY: 0.42, wantLayer0Z: 0.25, wantUpZ: 0.20,
		},
		{
			// prusa_xl/process_0.4_0.20.json: line width 0.45 DIVERGES
			// from the nozzle 0.4, but Z heights are uniform.
			name:         "prusa_xl 0.4 0.20",
			opts:         Options{Printer: "prusa_xl", NozzleDiameter: 0.4, LayerHeight: 0.20},
			wantLayer0XY: 0.5, wantUpperXY: 0.45, wantLayer0Z: 0.20, wantUpZ: 0.20,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := voxelCellSizes(tc.opts)
			if !approxEq(got.Layer0XY, tc.wantLayer0XY) {
				t.Errorf("Layer0XY: got %v, want %v", got.Layer0XY, tc.wantLayer0XY)
			}
			if !approxEq(got.UpperXY, tc.wantUpperXY) {
				t.Errorf("UpperXY: got %v, want %v", got.UpperXY, tc.wantUpperXY)
			}
			if !approxEq(got.Layer0Z, tc.wantLayer0Z) {
				t.Errorf("Layer0Z: got %v, want %v", got.Layer0Z, tc.wantLayer0Z)
			}
			if !approxEq(got.UpperZ, tc.wantUpZ) {
				t.Errorf("UpperZ: got %v, want %v", got.UpperZ, tc.wantUpZ)
			}
		})
	}
}

// TestVoxelCellSizesScales verifies the XY scale knobs multiply the
// resolved base widths (Layer0AdhesionXYScale onto layer 0 for bed
// adhesion, UpperLayerXYScale onto upper layers), while Z is left
// untouched.
func TestVoxelCellSizesScales(t *testing.T) {
	opts := Options{
		Printer:               "snapmaker_u1",
		NozzleDiameter:        0.4,
		LayerHeight:           0.20,
		Layer0AdhesionXYScale: 2,
		UpperLayerXYScale:     1.25,
	}
	got := voxelCellSizes(opts)
	if want := float32(0.5 * 2); !approxEq(got.Layer0XY, want) {
		t.Errorf("Layer0XY: got %v, want %v (0.5 × 2)", got.Layer0XY, want)
	}
	if want := float32(0.42 * 1.25); !approxEq(got.UpperXY, want) {
		t.Errorf("UpperXY: got %v, want %v (0.42 × 1.25)", got.UpperXY, want)
	}
	// Z is unaffected by the XY scales.
	if !approxEq(got.Layer0Z, 0.25) {
		t.Errorf("Layer0Z: got %v, want 0.25 (Z not scaled)", got.Layer0Z)
	}
	if !approxEq(got.UpperZ, 0.20) {
		t.Errorf("UpperZ: got %v, want 0.20 (Z not scaled)", got.UpperZ)
	}
}

// TestVoxelCellSizesFallback covers the "no profile match" branches —
// unknown printer, a known printer with an unknown nozzle, and a known
// (printer, nozzle) with an off-grid layer height that fails the
// exactness check. All must fall back to the bare nozzle diameter for
// both XY widths and LayerHeight for both Z heights, giving a uniform
// grid rather than a stale slot's settings. Empty Printer is NOT a
// fallback case (it resolves to the default printer, covered above).
func TestVoxelCellSizesFallback(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{"unknown printer", Options{Printer: "no_such_printer", NozzleDiameter: 0.4, LayerHeight: 0.20}},
		{"unknown nozzle", Options{Printer: "snapmaker_u1", NozzleDiameter: 0.99, LayerHeight: 0.20}},
		// 0.15mm sits between this printer's process slots with neither
		// closer than the 0.001mm exactness tolerance, so ClosestProcess'
		// nearest match must be rejected as off-grid.
		{"off-grid layer height", Options{Printer: "snapmaker_u1", NozzleDiameter: 0.4, LayerHeight: 0.15}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := voxelCellSizes(tc.opts)
			// Scales unset → 1×, so the fallback XY widths are the bare
			// nozzle diameter and both Z heights collapse to LayerHeight.
			if !approxEq(got.Layer0XY, tc.opts.NozzleDiameter) {
				t.Errorf("Layer0XY: got %v, want nozzle %v", got.Layer0XY, tc.opts.NozzleDiameter)
			}
			if !approxEq(got.UpperXY, tc.opts.NozzleDiameter) {
				t.Errorf("UpperXY: got %v, want nozzle %v", got.UpperXY, tc.opts.NozzleDiameter)
			}
			if !approxEq(got.Layer0Z, tc.opts.LayerHeight) {
				t.Errorf("Layer0Z: got %v, want LayerHeight %v", got.Layer0Z, tc.opts.LayerHeight)
			}
			if !approxEq(got.UpperZ, tc.opts.LayerHeight) {
				t.Errorf("UpperZ: got %v, want LayerHeight %v", got.UpperZ, tc.opts.LayerHeight)
			}
		})
	}
}
