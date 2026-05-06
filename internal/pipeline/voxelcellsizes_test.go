package pipeline

import (
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/squarevoxel"
)

// TestVoxelCellSizesFromProfile checks that for a real (printer,
// nozzle, layer-height) tuple in the embedded registry, the voxel
// sizes come from the OrcaSlicer process settings rather than the
// nozzle×constant approximation.
//
// The pinned values come from the flattened process JSONs
// internal/export3mf/profiles/<printer>/process_<nozzle>_<lh>.json —
// if upstream OrcaSlicer ships different defaults on a profile bump
// and you regenerate the manifest, expect this test to fail and the
// failure mode to be self-explanatory: the new value lives in the
// process file the test name references.
func TestVoxelCellSizesFromProfile(t *testing.T) {
	cases := []struct {
		name       string
		opts       Options
		wantLayer0XY float32
		wantUpperXY  float32
		wantLayer0Z  float32
		wantUpperZ   float32
	}{
		{
			// snapmaker_u1/process_0.4_0.20.json:
			// initial_layer_line_width=0.5, line_width=0.42,
			// initial_layer_print_height=0.25, layer_height=0.20.
			// First-layer Z (0.25) DIVERGES from upper Z (0.20) —
			// this is the marquee non-uniform-Z case in the registry.
			name: "snapmaker_u1 0.4 0.20",
			opts: Options{
				Printer:        "snapmaker_u1",
				NozzleDiameter: 0.4,
				LayerHeight:    0.20,
			},
			wantLayer0XY: 0.5,
			wantUpperXY:  0.42,
			wantLayer0Z:  0.25,
			wantUpperZ:   0.20,
		},
		{
			// Empty Printer must resolve to export3mf.DefaultPrinterID
			// (snapmaker_u1) so the voxel grid agrees with what the
			// exported 3MF claims to be — rather than silently
			// diverging via the nozzle×constant fallback.
			name: "empty printer falls back to snapmaker_u1 default",
			opts: Options{
				Printer:        "",
				NozzleDiameter: 0.4,
				LayerHeight:    0.20,
			},
			wantLayer0XY: 0.5,
			wantUpperXY:  0.42,
			wantLayer0Z:  0.25,
			wantUpperZ:   0.20,
		},
		{
			// prusa_xl/process_0.4_0.20.json:
			// initial_layer_line_width=0.5, line_width=0.45,
			// initial_layer_print_height=0.20, layer_height=0.20.
			// Line width DIVERGES from nozzle×scale (0.42 ≠ 0.45)
			// but Z heights are uniform.
			name: "prusa_xl 0.4 0.20",
			opts: Options{
				Printer:        "prusa_xl",
				NozzleDiameter: 0.4,
				LayerHeight:    0.20,
			},
			wantLayer0XY: 0.5,
			wantUpperXY:  0.45,
			wantLayer0Z:  0.20,
			wantUpperZ:   0.20,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := voxelCellSizes(tc.opts)
			if math.Abs(float64(got.Layer0XY-tc.wantLayer0XY)) > 1e-4 {
				t.Errorf("Layer0XY: got %v, want %v", got.Layer0XY, tc.wantLayer0XY)
			}
			if math.Abs(float64(got.UpperXY-tc.wantUpperXY)) > 1e-4 {
				t.Errorf("UpperXY: got %v, want %v", got.UpperXY, tc.wantUpperXY)
			}
			if math.Abs(float64(got.Layer0Z-tc.wantLayer0Z)) > 1e-4 {
				t.Errorf("Layer0Z: got %v, want %v", got.Layer0Z, tc.wantLayer0Z)
			}
			if math.Abs(float64(got.UpperZ-tc.wantUpperZ)) > 1e-4 {
				t.Errorf("UpperZ: got %v, want %v", got.UpperZ, tc.wantUpperZ)
			}
		})
	}
}

// TestVoxelCellSizesFallback covers the three "no profile match"
// branches — unknown printer, a known printer with an unknown
// nozzle, and a known (printer, nozzle) with an off-grid layer
// height that ClosestProcess can't match exactly. All three must
// return the legacy nozzle×constant approximations so users picking
// a layer height outside the registry's process slots get
// deterministic behaviour rather than a stale slot's settings.
//
// Empty Printer is NOT a fallback case — it resolves to
// export3mf.DefaultPrinterID (snapmaker_u1), tested above in
// TestVoxelCellSizesFromProfile.
func TestVoxelCellSizesFallback(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{"unknown printer", Options{Printer: "no_such_printer", NozzleDiameter: 0.4, LayerHeight: 0.20}},
		{"unknown nozzle", Options{Printer: "snapmaker_u1", NozzleDiameter: 0.99, LayerHeight: 0.20}},
		// snapmaker_u1's 0.4mm nozzle has 0.08/0.12/0.16/0.20/0.24/0.28
		// process slots; 0.15 is between 0.12 and 0.16 with neither
		// closer than 0.001mm. ClosestProcess would return 0.16 but
		// the helper must reject it as off-grid.
		{"off-grid layer height", Options{Printer: "snapmaker_u1", NozzleDiameter: 0.4, LayerHeight: 0.15}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantLayer0XY := tc.opts.NozzleDiameter * squarevoxel.Layer0CellScale
			wantUpperXY := tc.opts.NozzleDiameter * squarevoxel.UpperCellScale
			wantZ := tc.opts.LayerHeight // both Layer0Z and UpperZ collapse to LayerHeight in fallback
			got := voxelCellSizes(tc.opts)
			if got.Layer0XY != wantLayer0XY {
				t.Errorf("Layer0XY: got %v, want approximation %v", got.Layer0XY, wantLayer0XY)
			}
			if got.UpperXY != wantUpperXY {
				t.Errorf("UpperXY: got %v, want approximation %v", got.UpperXY, wantUpperXY)
			}
			if got.Layer0Z != wantZ {
				t.Errorf("Layer0Z: got %v, want %v (fallback uses LayerHeight for both Z)", got.Layer0Z, wantZ)
			}
			if got.UpperZ != wantZ {
				t.Errorf("UpperZ: got %v, want %v", got.UpperZ, wantZ)
			}
		})
	}
}
