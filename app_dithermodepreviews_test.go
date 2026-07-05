package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/ditherpreview"
)

// makeSrcPNG builds a tiny opaque test PNG and returns it as a base64 string
// (bare, no data-URI prefix).
func makeSrcPNG(t *testing.T) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 12, 8))
	for y := range 8 {
		for x := range 12 {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 20), G: 128, B: uint8(y * 30), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode src png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// TestDitherModePreviews exercises the full endpoint glue: base64 decode,
// palette parse, dithering, and PNG re-encode, for both a bare base64 payload
// and a data-URI payload.
func TestDitherModePreviews(t *testing.T) {
	a := &App{}
	pal := []string{"#101010", "#f0f0f0", "#dc503c", "#3c78d2"}

	for _, src := range []string{
		makeSrcPNG(t),
		"data:image/png;base64," + makeSrcPNG(t),
	} {
		out, err := a.DitherModePreviews(src, pal, 0.85, 20)
		if err != nil {
			t.Fatalf("DitherModePreviews: %v", err)
		}
		if len(out) != len(ditherpreview.Modes) {
			t.Fatalf("got %d previews, want %d", len(out), len(ditherpreview.Modes))
		}
		for _, mode := range ditherpreview.Modes {
			uri, ok := out[mode]
			if !ok {
				t.Fatalf("missing preview for mode %q", mode)
			}
			if !strings.HasPrefix(uri, "data:image/png;base64,") {
				t.Fatalf("mode %q: preview is not a PNG data URI: %.32q", mode, uri)
			}
			raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/png;base64,"))
			if err != nil {
				t.Fatalf("mode %q: decode preview: %v", mode, err)
			}
			if _, err := png.Decode(bytes.NewReader(raw)); err != nil {
				t.Fatalf("mode %q: preview is not a valid PNG: %v", mode, err)
			}
		}
	}
}

// TestDitherModePreviewsTooFewColors rejects palettes with fewer than 2 usable
// colors (malformed/empty entries are skipped, not errored).
func TestDitherModePreviewsTooFewColors(t *testing.T) {
	a := &App{}
	src := makeSrcPNG(t)
	if _, err := a.DitherModePreviews(src, []string{"#123456", "", "not-a-color"}, 0.85, 20); err == nil {
		t.Fatal("expected error for <2 usable colors, got nil")
	}
}

// TestParseHexPalette skips malformed entries and parses valid ones.
func TestParseHexPalette(t *testing.T) {
	got := parseHexPalette([]string{"#FF8040", "808080", "", "#xyz", "#12345", "abcdef"})
	want := [][3]uint8{{0xFF, 0x80, 0x40}, {0x80, 0x80, 0x80}, {0xAB, 0xCD, 0xEF}}
	if len(got) != len(want) {
		t.Fatalf("got %d colors, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("color %d = %v, want %v", i, got[i], want[i])
		}
	}
}
