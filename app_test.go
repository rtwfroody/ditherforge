package main

import (
	"path/filepath"
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
