package main

import (
	"testing"

	"github.com/rtwfroody/ditherforge/internal/swatch"
)

func TestSwatchFilename(t *testing.T) {
	cases := []struct {
		name      string
		filaments []swatch.Filament
		layerMM   float32
		want      string
	}{
		{
			name: "layer height then alphabetical labels regardless of palette order",
			filaments: []swatch.Filament{
				{Label: "Orange"}, {Label: "Black"}, {Label: "Beige"}, {Label: "ColdWhite"},
			},
			layerMM: 0.2,
			want:    "swatches-0.2mm-Beige-Black-ColdWhite-Orange.3mf",
		},
		{
			name: "whitespace removed, invalid chars stripped",
			filaments: []swatch.Filament{
				{Label: "Cold White"}, {Label: "Fire/Red"}, {Label: "  Sky  Blue "},
			},
			layerMM: 0.08,
			want:    "swatches-0.08mm-ColdWhite-FireRed-SkyBlue.3mf",
		},
		{
			name:      "case-insensitive sort",
			filaments: []swatch.Filament{{Label: "zinc"}, {Label: "Amber"}},
			layerMM:   0.2,
			want:      "swatches-0.2mm-Amber-zinc.3mf",
		},
		{
			name:      "non-positive layer height omits the segment",
			filaments: []swatch.Filament{{Label: "Beige"}},
			layerMM:   0,
			want:      "swatches-Beige.3mf",
		},
		{
			name:      "no labels keeps layer height",
			filaments: []swatch.Filament{{Label: ""}, {Label: "   "}},
			layerMM:   0.2,
			want:      "swatches-0.2mm.3mf",
		},
		{
			name:      "empty palette and no layer height falls back to plain name",
			filaments: nil,
			layerMM:   0,
			want:      "swatches.3mf",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := swatchFilename(c.filaments, c.layerMM); got != c.want {
				t.Errorf("swatchFilename() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSanitizeFilenamePart(t *testing.T) {
	cases := map[string]string{
		"Beige":       "Beige",
		"Cold White":  "ColdWhite",
		`a<b>c:"d/e`:  "abcde",
		`f\g|h?i*j`:   "fghij",
		"  trim me  ": "trimme",
		"":            "",
	}
	for in, want := range cases {
		if got := sanitizeFilenamePart(in); got != want {
			t.Errorf("sanitizeFilenamePart(%q) = %q, want %q", in, got, want)
		}
	}
}
