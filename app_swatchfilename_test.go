package main

import (
	"testing"

	"github.com/rtwfroody/ditherforge/internal/swatch"
)

func TestSwatchFilename(t *testing.T) {
	cases := []struct {
		name      string
		filaments []swatch.Filament
		want      string
	}{
		{
			name: "alphabetical order regardless of palette order",
			filaments: []swatch.Filament{
				{Label: "Orange"}, {Label: "Black"}, {Label: "Beige"}, {Label: "ColdWhite"},
			},
			want: "swatches-Beige-Black-ColdWhite-Orange.3mf",
		},
		{
			name: "whitespace removed, invalid chars stripped",
			filaments: []swatch.Filament{
				{Label: "Cold White"}, {Label: "Fire/Red"}, {Label: "  Sky  Blue "},
			},
			want: "swatches-ColdWhite-FireRed-SkyBlue.3mf",
		},
		{
			name:      "case-insensitive sort",
			filaments: []swatch.Filament{{Label: "zinc"}, {Label: "Amber"}},
			want:      "swatches-Amber-zinc.3mf",
		},
		{
			name:      "no labels falls back to plain name",
			filaments: []swatch.Filament{{Label: ""}, {Label: "   "}},
			want:      "swatches.3mf",
		},
		{
			name:      "empty palette falls back to plain name",
			filaments: nil,
			want:      "swatches.3mf",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := swatchFilename(c.filaments); got != c.want {
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
