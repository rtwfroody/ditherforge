package settings

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/palette"
)

// These tests exercise the on-disk JSON-document path end to end:
// Load(file) → Settings → ToOptions → pipeline.Options. The other settings
// tests build a Go Settings value directly, so they don't prove that the
// actual user-facing JSON (string-typed numbers, the _ditherforge wrapper,
// the exact json tags) decodes correctly. A renamed json tag or a type
// mismatch slips past a struct round-trip but fails here.

// writeJSON drops body at a temp path and returns it.
func writeJSON(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadJSONKitchenSink parses a document with nearly every option set to a
// distinctive non-default value and asserts the resulting pipeline.Options.
// The sizeRelativeUnits marker is present, so the file is the current
// (non-legacy) format. Inventory resolution is skipped (nil manager).
func TestLoadJSONKitchenSink(t *testing.T) {
	const body = `{
  "_ditherforge": {
    "url": "https://github.com/rtwfroody/ditherforge",
    "version": "ditherforge 0.9.8",
    "sizeRelativeUnits": true
  },
  "settings": {
    "inputFile": "/models/dragon.glb",
    "objectIndex": 2,
    "sizeMode": "size",
    "sizeValue": "120.5",
    "scaleValue": "2.5",
    "printer": "prusa_xl",
    "nozzleDiameter": "0.6",
    "layerHeight": "0.30",
    "baseColorMode": "texture",
    "baseMaterialXPath": "/mat/marble.mtlx",
    "baseMaterialXTileMM": 0.25,
    "baseMaterialXTriplanarSharpness": 6,
    "baseColor": {"hex": "#123456", "label": "navy"},
    "colorSlots": [
      {"hex": "#ff0000", "label": "red", "td": 1.5},
      null,
      {"hex": "#00ff00", "label": "green"},
      {"hex": "#0000ff", "label": "blue", "td": 3.0}
    ],
    "inventoryCollection": "Panchroma Basic",
    "brightness": 12,
    "contrast": -8,
    "saturation": 20,
    "warpPins": [
      {"sourceHex": "#aabbcc", "targetHex": "#ddeeff", "sigma": 1.25},
      {"sourceHex": "bogus", "targetHex": "#ddeeff", "sigma": 1.0}
    ],
    "stickers": [
      {"imagePath": "/img/logo.png", "center": [1,2,3], "normal": [0,0,1],
       "up": [0,1,0], "scale": 2.5, "rotation": 0.5, "maxAngle": 60, "mode": "projection"}
    ],
    "dither": "riemersma",
    "riemersmaBias": 0.7,
    "blueNoiseTol": 12,
    "colorSnap": 4,
    "noMerge": true,
    "noCellMerge": true,
    "noSimplify": true,
    "honorTD": true,
    "tdModel": "layered",
    "infillColor": "#010203",
    "colorAwareCells": true,
    "colorRegionContrast": 25,
    "stats": true,
    "showSampledColors": false,
    "alphaWrap": true,
    "alphaWrapAlpha": "0.5",
    "alphaWrapOffset": "0.03",
    "layer0AdhesionXYScale": 3,
    "upperLayerXYScale": 1.5,
    "splitEnabled": true,
    "splitAxis": 1,
    "splitOffset": 0.42,
    "splitConnectorStyle": "dovetail",
    "splitConnectorCount": 5,
    "splitConnectorDiamMM": 4,
    "splitConnectorDepthMM": 3,
    "splitClearanceMM": 0.2,
    "splitOrientationA": "rotated",
    "splitOrientationB": "mirrored"
  }
}`

	s, legacy, err := Load(writeJSON(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if legacy {
		t.Error("file with sizeRelativeUnits marker should not be legacy")
	}

	opts, err := ToOptions(s, nil)
	if err != nil {
		t.Fatalf("ToOptions: %v", err)
	}

	// Scalars and string-parsed numbers.
	if opts.Input != "/models/dragon.glb" {
		t.Errorf("Input = %q", opts.Input)
	}
	if opts.ObjectIndex != 2 {
		t.Errorf("ObjectIndex = %d, want 2", opts.ObjectIndex)
	}
	if opts.Size == nil || *opts.Size != 120.5 {
		t.Errorf("Size = %v, want 120.5", opts.Size)
	}
	// Size mode must ignore scaleValue ("2.5" here) and leave Scale at the
	// 1.0 identity — if a regression read scaleValue in size mode this trips.
	if opts.Scale != 1.0 {
		t.Errorf("Scale = %v, want 1.0 (scaleValue ignored in size mode)", opts.Scale)
	}
	if opts.Printer != "prusa_xl" {
		t.Errorf("Printer = %q", opts.Printer)
	}
	if opts.NozzleDiameter != 0.6 {
		t.Errorf("NozzleDiameter = %v, want 0.6", opts.NozzleDiameter)
	}
	if opts.LayerHeight != 0.3 {
		t.Errorf("LayerHeight = %v, want 0.3", opts.LayerHeight)
	}

	// MaterialX texture mode: the path is sent and the solid hex still rides
	// along (the pipeline ignores it when MaterialX is set).
	if opts.BaseColorMaterialX != "/mat/marble.mtlx" {
		t.Errorf("BaseColorMaterialX = %q", opts.BaseColorMaterialX)
	}
	if opts.BaseColor != "#123456" {
		t.Errorf("BaseColor = %q", opts.BaseColor)
	}
	if opts.BaseColorMaterialXTileMM != 0.25 {
		t.Errorf("BaseColorMaterialXTileMM = %v", opts.BaseColorMaterialXTileMM)
	}
	if opts.BaseColorMaterialXTriplanarSharpness != 6 {
		t.Errorf("BaseColorMaterialXTriplanarSharpness = %v", opts.BaseColorMaterialXTriplanarSharpness)
	}

	if opts.Brightness != 12 || opts.Contrast != -8 || opts.Saturation != 20 {
		t.Errorf("BCS = %v/%v/%v, want 12/-8/20", opts.Brightness, opts.Contrast, opts.Saturation)
	}

	// Locked palette: 3 non-nil slots, TDs default the slot with no td.
	wantLocked := []string{"#ff0000", "#00ff00", "#0000ff"}
	if len(opts.LockedColors) != 3 ||
		opts.LockedColors[0] != wantLocked[0] ||
		opts.LockedColors[1] != wantLocked[1] ||
		opts.LockedColors[2] != wantLocked[2] {
		t.Errorf("LockedColors = %v, want %v", opts.LockedColors, wantLocked)
	}
	if len(opts.LockedTDs) != 3 ||
		opts.LockedTDs[0] != 1.5 ||
		opts.LockedTDs[1] != palette.DefaultTD ||
		opts.LockedTDs[2] != 3.0 {
		t.Errorf("LockedTDs = %v, want [1.5 %v 3.0]", opts.LockedTDs, float32(palette.DefaultTD))
	}
	if opts.NumColors != 4 {
		t.Errorf("NumColors = %d, want 4 (len colorSlots)", opts.NumColors)
	}

	// Dither knobs.
	if opts.Dither != "riemersma" || opts.RiemersmaInputBias != 0.7 ||
		opts.BlueNoiseTolerance != 12 || opts.ColorSnap != 4 {
		t.Errorf("dither knobs = %q/%v/%v/%v", opts.Dither, opts.RiemersmaInputBias,
			opts.BlueNoiseTolerance, opts.ColorSnap)
	}

	// Booleans.
	if !opts.NoMerge || !opts.NoCellMerge || !opts.NoSimplify || !opts.HonorTD ||
		!opts.ColorAwareCells || !opts.Stats {
		t.Errorf("bool flags wrong: %+v", opts)
	}
	if opts.ColorRegionContrast != 25 {
		t.Errorf("ColorRegionContrast = %v, want 25", opts.ColorRegionContrast)
	}

	// Layered TD model fields.
	if opts.TDModel != "layered" {
		t.Errorf("TDModel = %q, want layered", opts.TDModel)
	}
	// ShellThicknessMM is derived from the printer profile (not from settings),
	// so ToOptions leaves it zero here; the "shellThickness" JSON key above is
	// an unknown key that Load must ignore harmlessly.
	if opts.InfillColor != [3]uint8{1, 2, 3} {
		t.Errorf("InfillColor = %v, want [1 2 3]", opts.InfillColor)
	}

	// Alpha-wrap.
	if !opts.AlphaWrap || opts.AlphaWrapAlpha != 0.5 || opts.AlphaWrapOffset != 0.03 {
		t.Errorf("alpha-wrap = %v/%v/%v", opts.AlphaWrap, opts.AlphaWrapAlpha, opts.AlphaWrapOffset)
	}
	if opts.Layer0AdhesionXYScale != 3 || opts.UpperLayerXYScale != 1.5 {
		t.Errorf("XY scales = %v/%v", opts.Layer0AdhesionXYScale, opts.UpperLayerXYScale)
	}

	// Warp pins: the malformed-hex pin is dropped.
	if len(opts.WarpPins) != 1 || opts.WarpPins[0].SourceHex != "#aabbcc" ||
		opts.WarpPins[0].TargetHex != "#ddeeff" || opts.WarpPins[0].Sigma != 1.25 {
		t.Errorf("WarpPins = %+v, want one valid pin", opts.WarpPins)
	}

	// Sticker round-trips through to Options.
	if len(opts.Stickers) != 1 {
		t.Fatalf("Stickers = %d, want 1", len(opts.Stickers))
	}
	st := opts.Stickers[0]
	if st.ImagePath != "/img/logo.png" || st.Mode != "projection" ||
		st.Scale != 2.5 || st.MaxAngle != 60 || st.Center != [3]float64{1, 2, 3} {
		t.Errorf("Sticker = %+v", st)
	}

	// Split assembly.
	sp := opts.Split
	if !sp.Enabled || sp.Axis != 1 || sp.Offset != 0.42 || sp.ConnectorStyle != "dovetail" ||
		sp.ConnectorCount != 5 || sp.ConnectorDiamMM != 4 || sp.ConnectorDepthMM != 3 ||
		sp.ClearanceMM != 0.2 || sp.Orientation != [2]string{"rotated", "mirrored"} {
		t.Errorf("Split = %+v", sp)
	}
}

// TestLoadJSONScaleModeAndFallbacks parses a sparser, legacy-format document
// (no sizeRelativeUnits marker) that uses scale sizing, solid base color, and
// the JS-style `parseFloat(x) || default` fallbacks for blank/"0" numeric
// strings. Keys omitted from the file must inherit Default() values, not Go
// zeros.
func TestLoadJSONScaleModeAndFallbacks(t *testing.T) {
	const body = `{
  "_ditherforge": {
    "url": "https://github.com/rtwfroody/ditherforge",
    "version": "ditherforge 0.9.2"
  },
  "settings": {
    "inputFile": "/m/x.glb",
    "sizeMode": "scale",
    "sizeValue": "100",
    "scaleValue": "2.5",
    "nozzleDiameter": "0.4",
    "layerHeight": "0",
    "baseColorMode": "solid",
    "baseColor": {"hex": "#abcdef"},
    "baseMaterialXPath": "/should/be/ignored.mtlx",
    "colorSlots": [null, null, null, null, null, null],
    "dither": "floyd-steinberg",
    "alphaWrap": true,
    "alphaWrapAlpha": "",
    "alphaWrapOffset": "0.05",
    "splitEnabled": false
  }
}`

	s, legacy, err := Load(writeJSON(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !legacy {
		t.Error("file without sizeRelativeUnits marker should be detected as legacy")
	}

	opts, err := ToOptions(s, nil)
	if err != nil {
		t.Fatalf("ToOptions: %v", err)
	}

	// Scale mode: Scale set from scaleValue, Size left nil.
	if opts.Scale != 2.5 {
		t.Errorf("Scale = %v, want 2.5", opts.Scale)
	}
	if opts.Size != nil {
		t.Errorf("Size = %v, want nil in scale mode", opts.Size)
	}

	// "0" / "" numeric strings fall back to the parse defaults (JS falsy-zero).
	if opts.LayerHeight != 0.2 {
		t.Errorf("LayerHeight = %v, want 0.2 fallback from \"0\"", opts.LayerHeight)
	}
	if opts.NozzleDiameter != 0.4 {
		t.Errorf("NozzleDiameter = %v, want 0.4", opts.NozzleDiameter)
	}
	if opts.AlphaWrapAlpha != 0 {
		t.Errorf("AlphaWrapAlpha = %v, want 0 from blank", opts.AlphaWrapAlpha)
	}
	if opts.AlphaWrapOffset != 0.05 {
		t.Errorf("AlphaWrapOffset = %v, want 0.05", opts.AlphaWrapOffset)
	}

	// Solid mode: BaseColor sent, MaterialX path NOT (mode is solid).
	if opts.BaseColor != "#abcdef" {
		t.Errorf("BaseColor = %q", opts.BaseColor)
	}
	if opts.BaseColorMaterialX != "" {
		t.Errorf("BaseColorMaterialX = %q, want empty in solid mode", opts.BaseColorMaterialX)
	}

	// Six nil slots: NumColors counts the slots; no locked colors.
	if opts.NumColors != 6 {
		t.Errorf("NumColors = %d, want 6", opts.NumColors)
	}
	if len(opts.LockedColors) != 0 {
		t.Errorf("LockedColors = %v, want none", opts.LockedColors)
	}

	// Omitted keys inherit Default(), not Go zero: honorTD defaults true,
	// riemersmaBias 0.85, colorSnap 5.
	if !opts.HonorTD {
		t.Error("HonorTD should default true for an omitted key")
	}
	if opts.RiemersmaInputBias != 0.85 {
		t.Errorf("RiemersmaInputBias = %v, want 0.85 default", opts.RiemersmaInputBias)
	}
	if opts.ColorSnap != 5 {
		t.Errorf("ColorSnap = %v, want 5 default", opts.ColorSnap)
	}
}

// TestLoadMigratesRemovedDitherModes checks that dither modes removed from the
// product (2026-07) silently migrate to their nearest surviving mode on load,
// so old settings files never fail or fall back to the default.
func TestLoadMigratesRemovedDitherModes(t *testing.T) {
	cases := []struct{ from, want string }{
		{"riemersma-pair", "riemersma"},
		{"dizzy-2hop", "dlc-d30-p7"},
		{"dizzy-recover", "dlc-d30-p7"},
		{"dizzy-corrected", "dlc-d30-p7"},
		{"dizzy-local-corrected", "dlc-d30-p7"},
		{"blue-noise", "bn-adapt-5"},
	}
	for _, tc := range cases {
		body := `{
  "_ditherforge": {
    "url": "https://github.com/rtwfroody/ditherforge",
    "version": "ditherforge 0.9.8",
    "sizeRelativeUnits": true
  },
  "settings": {
    "inputFile": "/m/x.glb",
    "dither": "` + tc.from + `"
  }
}`
		s, _, err := Load(writeJSON(t, body))
		if err != nil {
			t.Fatalf("Load(%q): %v", tc.from, err)
		}
		if s.Dither != tc.want {
			t.Errorf("dither %q migrated to %q, want %q", tc.from, s.Dither, tc.want)
		}
	}
}

// TestParseCommittedSettingsFixtures guards the real settings JSON files
// checked into tests/objects: each must Load and convert to Options without
// error and resolve to a usable size. This catches a format drift that would
// break loading a user's saved project, and keeps the committed fixtures
// honest as the schema evolves.
func TestParseCommittedSettingsFixtures(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	objects := filepath.Join(repoRoot, "tests", "objects")

	for _, name := range []string{
		"glyphid_praetorian.json",
		"earth.json",
		"low_poly_building.json",
	} {
		t.Run(name, func(t *testing.T) {
			s, _, err := Load(filepath.Join(objects, name))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			opts, err := ToOptions(s, nil)
			if err != nil {
				t.Fatalf("ToOptions: %v", err)
			}
			if opts.Input == "" {
				t.Error("Input resolved to empty")
			}
			if opts.NumColors < 1 {
				t.Errorf("NumColors = %d, want >= 1", opts.NumColors)
			}
			// The active sizing mode must resolve correctly: size mode sets a
			// concrete Size (leaving Scale at the 1.0 identity), scale mode
			// leaves Size nil and carries the multiplier in Scale (which is a
			// legitimate 1.0 for an identity scale, so a "Scale != 1.0" probe
			// would wrongly reject it).
			switch s.SizeMode {
			case "scale":
				if opts.Size != nil {
					t.Errorf("scale mode: Size = %v, want nil", opts.Size)
				}
			default: // "size"
				if opts.Size == nil {
					t.Error("size mode: Size is nil, want a resolved extent")
				}
			}
		})
	}
}
