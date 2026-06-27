package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// TestPathForSaving exercises the rule "store relative if the asset is in
// the same directory, a subdirectory, or one directory up; otherwise
// store absolute".
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
		{"same directory", "/home/u/proj/settings.json", "/home/u/proj/model.glb", "model.glb"},
		{"subdirectory", "/home/u/proj/settings.json", "/home/u/proj/assets/sticker.png", "assets/sticker.png"},
		{"one up", "/home/u/proj/sub/settings.json", "/home/u/proj/model.glb", "../model.glb"},
		{"one up plus descent", "/home/u/proj/sub/settings.json", "/home/u/proj/other/model.glb", "../other/model.glb"},
		{"two up — falls back to absolute", "/home/u/proj/sub/settings.json", "/home/u/model.glb", "/home/u/model.glb"},
		{"different tree — falls back to absolute", "/home/u/proj/settings.json", "/tmp/x.glb", "/tmp/x.glb"},
		{"empty", "/home/u/proj/settings.json", "", ""},
		{"already-relative passes through", "/home/u/proj/settings.json", "model.glb", "model.glb"},
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

// TestPathForLoading is the inverse: relative paths resolve against the
// JSON's directory, absolute paths pass through.
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
		{"same directory", "/home/u/proj/settings.json", "model.glb", "/home/u/proj/model.glb"},
		{"one up", "/home/u/proj/sub/settings.json", "../model.glb", "/home/u/proj/model.glb"},
		{"absolute passes through", "/home/u/proj/settings.json", "/tmp/x.glb", "/tmp/x.glb"},
		{"empty", "/home/u/proj/settings.json", "", ""},
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

// TestSaveLoadRoundTrip writes a settings file and re-reads it, verifying
// that absolute paths come back absolute regardless of whether they were
// stored as relative or absolute on disk.
func TestSaveLoadRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	jsonPath := filepath.Join(tmp, "settings.json")

	near := filepath.Join(tmp, "model.glb")
	far := filepath.Join(string(filepath.Separator), "definitely-far-"+t.Name(), "far.glb")
	if got := pathForSaving(jsonPath, far); got != far {
		t.Fatalf("expected pathForSaving to keep %q absolute, got %q", far, got)
	}

	original := Settings{
		InputFile:         near,
		BaseMaterialXPath: far,
		Stickers: []StickerSetting{
			{ImagePath: near, Mode: "unfold"},
		},
	}

	if err := Save(jsonPath, original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, _, err := Load(jsonPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.InputFile != near {
		t.Errorf("InputFile round-trip: got %q, want %q", loaded.InputFile, near)
	}
	if loaded.BaseMaterialXPath != far {
		t.Errorf("BaseMaterialXPath round-trip: got %q, want %q", loaded.BaseMaterialXPath, far)
	}
	if len(loaded.Stickers) != 1 || loaded.Stickers[0].ImagePath != near {
		t.Errorf("Sticker ImagePath round-trip: got %+v, want one entry with %q", loaded.Stickers, near)
	}
	// Also verify Save did NOT mutate the caller's struct.
	if original.InputFile != near || original.BaseMaterialXPath != far {
		t.Errorf("Save mutated caller's Settings: got %+v", original)
	}
	if len(original.Stickers) != 1 || original.Stickers[0].ImagePath != near {
		t.Errorf("Save mutated caller's Stickers: got %+v", original.Stickers)
	}
}

// nonDefaultSettings returns a Settings populated with non-zero,
// distinctive values for every field. Used by the round-trip preservation
// test below to detect Go-side data drops.
//
// MAINTENANCE CONTRACT: when adding a field to Settings, extend
// nonDefaultSettings to set it to a non-zero value.
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
		BaseColor:                       &ColorSlotSetting{Hex: "#abcdef", Label: "blu", Collection: "C", TD: 2.1},
		BaseMaterialXPath:               "/mat.mtlx",
		BaseMaterialXTileMM:             7.5,
		BaseMaterialXTriplanarSharpness: 9,
		BaseColorMode:                   "texture",
		ColorSlots: []*ColorSlotSetting{
			{Hex: "#111111", Label: "a", Collection: "X", TD: 1.2},
			nil,
			{Hex: "#222222", Label: "b", Collection: "Y", TD: 3.4},
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
		HonorTD:               true,
		ColorAwareCells:       true,
		ColorRegionContrast:   33,
		RegionDither:          true,
		RegionDitherDeltaE:    27,
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
		SplitTiltA:            12.5,
		SplitTiltB:            -7.5,
		SplitConnectorStyle:   "dovetail",
		SplitConnectorCount:   4,
		SplitConnectorDiamMM:  5,
		SplitConnectorDepthMM: 4,
		SplitClearanceMM:      0.25,
		SplitOrientationA:     "rotated",
		SplitOrientationB:     "mirrored",
	}
}

// TestSaveLoadRoundTripPreservesAllFields walks every exported field of
// Settings via reflection and asserts that nonDefaultSettings sets it to a
// non-zero value, then round-trips through Save/Load and asserts every
// field returns identical via DeepEqual. This is the structural guarantee
// that no Settings field can be silently dropped.
//
// nonDefaultSettings's path-typed values are rooted at "/" so they are
// always more than one directory above the test's temp JSON path, which
// keeps pathForSaving in the absolute-pass-through branch.
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
	if err := Save(path, original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(original, loaded) {
		t.Errorf("round-trip lost data:\n  original: %+v\n  loaded:   %+v", original, loaded)
	}
}

// TestLoadFillsMissingKeysWithDefaults verifies the pre-populate-then-
// unmarshal contract: a settings file containing only the DitherForge
// metadata (no settings keys at all) must load as Default() rather than
// the Go zero value.
func TestLoadFillsMissingKeysWithDefaults(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "minimal.json")
	minimal := []byte(`{"_ditherforge":{"url":"https://github.com/rtwfroody/ditherforge","version":"old"},"settings":{}}`)
	if err := os.WriteFile(path, minimal, 0644); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Default()
	if !reflect.DeepEqual(loaded, want) {
		t.Errorf("missing-keys file should load as defaults:\n  got:  %+v\n  want: %+v", loaded, want)
	}
}

// TestLegacyUnitsDetection verifies the presence-based marker that
// distinguishes legacy (absolute-mm) files from the fraction-of-extent
// format: files Save writes carry _ditherforge.sizeRelativeUnits=true and
// load as non-legacy; files lacking the marker (any version, including the
// unreliable high version strings some old fixtures carry) load as legacy.
func TestLegacyUnitsDetection(t *testing.T) {
	tmp := t.TempDir()

	// A file Save wrote is the current fraction format → not legacy.
	current := filepath.Join(tmp, "current.json")
	if err := Save(current, Default()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, legacy, err := Load(current); err != nil {
		t.Fatalf("Load current: %v", err)
	} else if legacy {
		t.Error("file written by Save should not be detected as legacy")
	}

	// Hand-authored files without the marker are legacy regardless of the
	// version string — even an implausibly high one.
	for _, ver := range []string{"ditherforge 0.9.5", "ditherforge 0.9.27", "ditherforge 9.9.9", ""} {
		p := filepath.Join(tmp, "legacy.json")
		body := `{"_ditherforge":{"url":"https://github.com/rtwfroody/ditherforge","version":"` + ver + `"},"settings":{}}`
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		if _, legacy, err := Load(p); err != nil {
			t.Fatalf("Load legacy (%q): %v", ver, err)
		} else if !legacy {
			t.Errorf("file without sizeRelativeUnits marker (version %q) should be legacy", ver)
		}
	}
}

// TestToOptionsMapping checks the Settings→Options conversion for the
// fields that involve real logic (not a straight copy): size/scale modes,
// string-to-float parsing with fallbacks, base-color vs MaterialX mode,
// locked palette slots with TD defaulting, the nil ObjectIndex sentinel,
// warp-pin hex filtering, and split assembly. Inventory resolution (which
// needs a collection.Manager) is left to integration coverage; nil mgr
// skips it here.
func TestToOptionsMapping(t *testing.T) {
	td2 := float32(2.5)
	s := Default()
	s.InputFile = "/x.glb"
	s.SizeMode = "size"
	s.SizeValue = "150"
	s.NozzleDiameter = "0.6"
	s.LayerHeight = "" // exercises fallback
	s.BaseColorMode = "texture"
	s.BaseMaterialXPath = "/m.mtlx"
	s.BaseColor = &ColorSlotSetting{Hex: "#abcabc"}
	s.ColorSlots = []*ColorSlotSetting{
		{Hex: "#ff0000", TD: td2},
		nil,
		{Hex: "#00ff00"}, // TD 0 → defaults
		nil,
	}
	s.InventoryCollection = "" // skip inventory
	s.WarpPins = []WarpPinSetting{
		{SourceHex: "#112233", TargetHex: "#445566", Sigma: 1},
		{SourceHex: "not-a-hex", TargetHex: "#445566", Sigma: 1}, // dropped
	}
	s.SplitEnabled = true
	s.SplitAxis = 1
	s.SplitOrientationA = "z-up"
	s.SplitOrientationB = "z-down"

	opts, err := ToOptions(s, nil)
	if err != nil {
		t.Fatalf("ToOptions: %v", err)
	}

	if opts.Input != "/x.glb" {
		t.Errorf("Input = %q", opts.Input)
	}
	if opts.NumColors != 4 {
		t.Errorf("NumColors = %d, want 4 (len ColorSlots)", opts.NumColors)
	}
	if opts.Size == nil || *opts.Size != 150 {
		t.Errorf("Size = %v, want 150", opts.Size)
	}
	if opts.Scale != 1.0 {
		t.Errorf("Scale = %v, want 1.0 in size mode", opts.Scale)
	}
	if opts.NozzleDiameter != 0.6 {
		t.Errorf("NozzleDiameter = %v", opts.NozzleDiameter)
	}
	if opts.LayerHeight != 0.2 {
		t.Errorf("LayerHeight = %v, want 0.2 fallback", opts.LayerHeight)
	}
	if opts.BaseColorMaterialX != "/m.mtlx" {
		t.Errorf("BaseColorMaterialX = %q, want texture path", opts.BaseColorMaterialX)
	}
	if opts.BaseColor != "#abcabc" {
		t.Errorf("BaseColor = %q (sent even in texture mode)", opts.BaseColor)
	}
	wantLocked := []string{"#ff0000", "#00ff00"}
	if !reflect.DeepEqual(opts.LockedColors, wantLocked) {
		t.Errorf("LockedColors = %v, want %v", opts.LockedColors, wantLocked)
	}
	if len(opts.LockedTDs) != 2 || opts.LockedTDs[0] != td2 || opts.LockedTDs[1] != 1.0 {
		t.Errorf("LockedTDs = %v, want [2.5 1.0]", opts.LockedTDs)
	}
	if len(opts.WarpPins) != 1 || opts.WarpPins[0].SourceHex != "#112233" {
		t.Errorf("WarpPins = %+v, want one valid pin", opts.WarpPins)
	}
	if !opts.Split.Enabled || opts.Split.Axis != 1 ||
		opts.Split.Orientation != [2]string{"z-up", "z-down"} {
		t.Errorf("Split = %+v", opts.Split)
	}

	// nil ObjectIndex → -1 sentinel.
	s.ObjectIndex = nil
	opts, _ = ToOptions(s, nil)
	if opts.ObjectIndex != -1 {
		t.Errorf("nil ObjectIndex → %d, want -1", opts.ObjectIndex)
	}

	// scale mode.
	s.SizeMode = "scale"
	s.ScaleValue = "3"
	opts, _ = ToOptions(s, nil)
	if opts.Scale != 3 || opts.Size != nil {
		t.Errorf("scale mode: Scale=%v Size=%v, want 3 / nil", opts.Scale, opts.Size)
	}
}

// TestToOptionsInventoryDegrades documents that a named inventory collection
// that can't be resolved (here: nil manager) does NOT abort the conversion —
// it degrades to an empty inventory and the run proceeds on the locked
// palette slots, matching the old GUI buildOpts which never failed here.
func TestToOptionsInventoryDegrades(t *testing.T) {
	s := Default()
	s.InventoryCollection = "DefinitelyNotARealCollection"
	opts, err := ToOptions(s, nil)
	if err != nil {
		t.Fatalf("ToOptions must not error on an unresolvable inventory: %v", err)
	}
	if len(opts.InventoryColors) != 0 {
		t.Errorf("expected empty inventory, got %d colors", len(opts.InventoryColors))
	}
}

// TestParseF32 covers the JS `parseFloat(x) || def` emulation: blank and
// unparseable strings fall back to def, an explicit "0" falls back to def
// (0 is falsy in JS), surrounding whitespace is tolerated, and a real value
// passes through.
func TestParseF32(t *testing.T) {
	cases := []struct {
		in   string
		def  float32
		want float32
	}{
		{"0.6", 0.4, 0.6},
		{"", 0.4, 0.4},
		{"junk", 0.4, 0.4},
		{"0", 0.4, 0.4},     // falsy-zero → default
		{" 0.4 ", 0.2, 0.4}, // trimmed
		{"0", 0, 0},         // default itself 0 (alpha-wrap auto)
	}
	for _, c := range cases {
		if got := parseF32(c.in, c.def); got != c.want {
			t.Errorf("parseF32(%q, %v) = %v, want %v", c.in, c.def, got, c.want)
		}
	}
}
