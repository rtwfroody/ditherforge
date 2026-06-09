package main

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// TestPathForSaving exercises the rule "store relative if the asset
// is in the same directory, a subdirectory, or one directory up;
// otherwise store absolute".
func TestPathForSaving(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path layout assumptions are POSIX-shaped")
	}
	cases := []struct {
		name     string
		jsonPath string
		input    string
		want     string
	}{
		{
			name:     "same directory",
			jsonPath: "/home/u/proj/settings.json",
			input:    "/home/u/proj/model.glb",
			want:     "model.glb",
		},
		{
			name:     "subdirectory",
			jsonPath: "/home/u/proj/settings.json",
			input:    "/home/u/proj/assets/sticker.png",
			want:     "assets/sticker.png",
		},
		{
			name:     "one up",
			jsonPath: "/home/u/proj/sub/settings.json",
			input:    "/home/u/proj/model.glb",
			want:     "../model.glb",
		},
		{
			name:     "one up plus descent",
			jsonPath: "/home/u/proj/sub/settings.json",
			input:    "/home/u/proj/other/model.glb",
			want:     "../other/model.glb",
		},
		{
			name:     "two up — falls back to absolute",
			jsonPath: "/home/u/proj/sub/settings.json",
			input:    "/home/u/model.glb",
			want:     "/home/u/model.glb",
		},
		{
			name:     "different tree — falls back to absolute",
			jsonPath: "/home/u/proj/settings.json",
			input:    "/tmp/x.glb",
			want:     "/tmp/x.glb",
		},
		{
			name:     "empty",
			jsonPath: "/home/u/proj/settings.json",
			input:    "",
			want:     "",
		},
		{
			name:     "already-relative passes through",
			jsonPath: "/home/u/proj/settings.json",
			input:    "model.glb",
			want:     "model.glb",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pathForSaving(tc.jsonPath, tc.input)
			if got != tc.want {
				t.Errorf("pathForSaving(%q, %q) = %q, want %q", tc.jsonPath, tc.input, got, tc.want)
			}
		})
	}
}

// TestPathForLoading is the inverse: relative paths resolve against
// the JSON's directory, absolute paths pass through.
func TestPathForLoading(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path layout assumptions are POSIX-shaped")
	}
	cases := []struct {
		name     string
		jsonPath string
		stored   string
		want     string
	}{
		{
			name:     "same directory",
			jsonPath: "/home/u/proj/settings.json",
			stored:   "model.glb",
			want:     "/home/u/proj/model.glb",
		},
		{
			name:     "one up",
			jsonPath: "/home/u/proj/sub/settings.json",
			stored:   "../model.glb",
			want:     "/home/u/proj/model.glb",
		},
		{
			name:     "absolute passes through",
			jsonPath: "/home/u/proj/settings.json",
			stored:   "/tmp/x.glb",
			want:     "/tmp/x.glb",
		},
		{
			name:     "empty",
			jsonPath: "/home/u/proj/settings.json",
			stored:   "",
			want:     "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pathForLoading(tc.jsonPath, tc.stored)
			if got != tc.want {
				t.Errorf("pathForLoading(%q, %q) = %q, want %q", tc.jsonPath, tc.stored, got, tc.want)
			}
		})
	}
}

// TestSaveLoadRoundTrip writes a settings file and re-reads it,
// verifying that absolute paths come back absolute regardless of
// whether they were stored as relative or absolute on disk. Uses the
// real (*App).SaveSettings and (*App).LoadSettingsFile so the wiring
// in those functions is exercised end-to-end.
func TestSaveLoadRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	jsonPath := filepath.Join(tmp, "settings.json")

	// Two assets: one nearby (should serialize relative), one
	// definitely far away (should serialize absolute). The "far"
	// path is constructed off the filesystem root with a unique
	// segment, so it's guaranteed to be more than one directory up
	// from `tmp` regardless of how deep the temp dir happens to be.
	near := filepath.Join(tmp, "model.glb")
	far := filepath.Join(string(filepath.Separator), "definitely-far-"+t.Name(), "far.glb")
	// Pre-flight: confirm pathForSaving picks the absolute branch for
	// "far" before exercising the full round-trip. This makes the
	// test's intent self-evident if someone later changes the rule.
	if got := pathForSaving(jsonPath, far); got != far {
		t.Fatalf("expected pathForSaving to keep %q absolute, got %q", far, got)
	}

	app := &App{}
	original := Settings{
		InputFile:         near,
		BaseMaterialXPath: far,
		Stickers: []StickerSetting{
			{ImagePath: near, Mode: "unfold"},
		},
	}

	if err := app.SaveSettings(jsonPath, original); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	res, err := app.LoadSettingsFile(jsonPath)
	if err != nil {
		t.Fatalf("LoadSettingsFile: %v", err)
	}
	if res.Settings.InputFile != near {
		t.Errorf("InputFile round-trip: got %q, want %q", res.Settings.InputFile, near)
	}
	if res.Settings.BaseMaterialXPath != far {
		t.Errorf("BaseMaterialXPath round-trip: got %q, want %q", res.Settings.BaseMaterialXPath, far)
	}
	if len(res.Settings.Stickers) != 1 || res.Settings.Stickers[0].ImagePath != near {
		t.Errorf("Sticker ImagePath round-trip: got %+v, want one entry with %q", res.Settings.Stickers, near)
	}
	// Also verify SaveSettings did NOT mutate the caller's struct.
	if original.InputFile != near || original.BaseMaterialXPath != far {
		t.Errorf("SaveSettings mutated caller's Settings: got %+v", original)
	}
	if len(original.Stickers) != 1 || original.Stickers[0].ImagePath != near {
		t.Errorf("SaveSettings mutated caller's Stickers: got %+v", original.Stickers)
	}
}

// nonDefaultSettings returns a Settings populated with non-zero,
// distinctive values for every field. Used by the round-trip
// preservation test below to detect Go-side data drops.
//
// MAINTENANCE CONTRACT: when adding a field to Settings, extend
// nonDefaultSettings to set it to a non-zero value. (It does NOT
// need to differ from defaultSettings() — the round-trip test
// unmarshals into a zero-valued SettingsFile, so a marshal-side
// drop always surfaces as zero on the load side regardless of the
// chosen value.) The reflection guard at the head of
// TestSaveLoadRoundTripPreservesAllFields refuses to run unless
// every exported top-level field is populated.
func nonDefaultSettings() Settings {
	idx := 7
	return Settings{
		InputFile:                       "/in.glb",
		ObjectIndex:                     &idx,
		SizeMode:                        "scale",
		SizeValue:                       "123",
		ScaleValue:                      "2.5",
		Printer:                         "prusa_xl",
		NozzleDiameter:                  "0.6",
		LayerHeight:                     "0.30",
		BaseColor:                       &ColorSlotSetting{Hex: "#abcdef", Label: "blu", Collection: "C"},
		BaseMaterialXPath:               "/mat.mtlx",
		BaseMaterialXTileMM:             7.5,
		BaseMaterialXTriplanarSharpness: 9,
		BaseColorMode:                   "texture",
		ColorSlots: []*ColorSlotSetting{
			{Hex: "#111111", Label: "a", Collection: "X"},
			nil,
			{Hex: "#222222", Label: "b", Collection: "Y"},
		},
		InventoryCollection: "Custom",
		Brightness:          11,
		Contrast:            -22,
		Saturation:          33,
		WarpPins: []WarpPinSetting{
			{SourceHex: "#aabbcc", TargetHex: "#ddeeff", TargetLabel: "lbl", Sigma: 1.5},
		},
		Stickers: []StickerSetting{
			{
				ImagePath: "/img.png",
				Center:    [3]float64{1, 2, 3}, Normal: [3]float64{0, 0, 1}, Up: [3]float64{0, 1, 0},
				Scale: 2.0, Rotation: 0.5, MaxAngle: 45, Mode: "projection",
			},
		},
		Dither:                "floyd-steinberg",
		RiemersmaBias:         0.42,
		BlueNoiseTol:          15,
		ColorSnap:             3,
		NoMerge:               true,
		NoSimplify:            true,
		NoCellMerge:           true,
		Stats:                 true,
		ShowSampledColors:     true,
		AlphaWrap:             true,
		AlphaWrapAlpha:        "0.8",
		AlphaWrapOffset:       "0.04",
		Layer0AdhesionXYScale: 3.5,
		UpperLayerXYScale:     0.7,
		SplitEnabled:          true,
		SplitAxis:             1,
		SplitOffset:           1.5,
		SplitConnectorStyle:   "dovetail",
		SplitConnectorCount:   4,
		SplitConnectorDiamMM:  5,
		SplitConnectorDepthMM: 4,
		SplitClearanceMM:      0.25,
		SplitOrientationA:     "rotated",
		SplitOrientationB:     "mirrored",
	}
}

// TestSaveLoadRoundTripPreservesAllFields walks every exported field
// of Settings via reflection and asserts that nonDefaultSettings sets
// it to a non-zero value, then round-trips through (*App).SaveSettings
// and (*App).LoadSettingsFile and asserts every field returns
// identical via DeepEqual.
//
// This is the structural guarantee that no Settings field can be
// silently dropped: if a field doesn't appear in JSON output
// (missing/typo'd json tag, unexported, …) or is missed by the
// path-transform stage, the unmarshal restores it as the Go zero
// value (or a transformed-and-not-restored path), which differs
// from the distinctive non-default value, and the test fails.
//
// nonDefaultSettings's path-typed values are rooted at "/" so they
// are always more than one directory above the test's temp JSON
// path, which keeps pathForSaving in the absolute-pass-through
// branch and lets DeepEqual round-trip them verbatim. The relative-
// path branches are exercised separately by TestSaveLoadRoundTrip.
func TestSaveLoadRoundTripPreservesAllFields(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("nonDefaultSettings uses POSIX-rooted paths")
	}
	original := nonDefaultSettings()
	rv := reflect.ValueOf(original)
	for i := 0; i < rv.NumField(); i++ {
		field := rv.Type().Field(i)
		if !field.IsExported() {
			continue
		}
		if rv.Field(i).IsZero() {
			t.Fatalf("nonDefaultSettings.%s is the Go zero value — extend nonDefaultSettings to give it a non-zero value so the round-trip test can detect drops", field.Name)
		}
	}

	tmp := t.TempDir()
	path := filepath.Join(tmp, "settings.json")
	app := &App{}
	if err := app.SaveSettings(path, original); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	res, err := app.LoadSettingsFile(path)
	if err != nil {
		t.Fatalf("LoadSettingsFile: %v", err)
	}
	if !reflect.DeepEqual(original, res.Settings) {
		t.Errorf("round-trip lost data:\n  original: %+v\n  loaded:   %+v", original, res.Settings)
	}
}

// TestLoadSettingsFillsMissingKeysWithDefaults verifies the
// pre-populate-then-unmarshal contract: a settings file containing
// only the DitherForge metadata (no settings keys at all) must load
// as defaultSettings() rather than the Go zero value. This is the
// structural guarantee that legacy files predating any field never
// silently observe Go zeros.
func TestLoadSettingsFillsMissingKeysWithDefaults(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "minimal.json")
	minimal := []byte(`{"_ditherforge":{"url":"https://github.com/rtwfroody/ditherforge","version":"old"},"settings":{}}`)
	if err := os.WriteFile(path, minimal, 0644); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	res, err := app.LoadSettingsFile(path)
	if err != nil {
		t.Fatalf("LoadSettingsFile: %v", err)
	}
	want := defaultSettings()
	if !reflect.DeepEqual(res.Settings, want) {
		t.Errorf("missing-keys file should load as defaults:\n  got:  %+v\n  want: %+v", res.Settings, want)
	}
}
