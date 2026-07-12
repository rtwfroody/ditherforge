package pipeline

import "testing"

// TestDesignateInfill exercises the infill-filament designation rule (see
// designateInfill): the pure function that decides which resolved palette entry
// becomes palette index 0 (the infill filament, by invariant).
func TestDesignateInfill(t *testing.T) {
	// A 3-color palette used across cases; colors are arbitrary but distinct.
	red := [3]uint8{0xFF, 0x00, 0x00}
	green := [3]uint8{0x00, 0xFF, 0x00}
	blue := [3]uint8{0x00, 0x00, 0xFF}
	colors := [][3]uint8{red, green, blue}

	cases := []struct {
		name        string
		tds         []float32
		counts      []int
		overrideHex string
		honorTD     bool
		hasLocked   bool
		want        int
	}{
		{
			// Uniform TDs, no locks: legacy most-used designation. green (idx 1)
			// is used most, so it becomes the infill.
			name:    "uniform-td-no-locks-most-used",
			tds:     []float32{0, 0, 0},
			counts:  []int{10, 50, 20},
			honorTD: true,
			want:    1,
		},
		{
			// Uniform TDs, with locks: legacy behavior keeps index 0 (no swap),
			// regardless of usage.
			name:      "uniform-td-with-locks-index-0",
			tds:       []float32{0, 0, 0},
			counts:    []int{10, 50, 20},
			honorTD:   true,
			hasLocked: true,
			want:      0,
		},
		{
			// Differing TDs + HonorTD: designate the lowest-TD (most opaque)
			// entry — blue (idx 2) at TD 1 vs 5/8.
			name:    "differing-td-lowest-wins",
			tds:     []float32{8, 5, 1},
			counts:  []int{10, 50, 20},
			honorTD: true,
			want:    2,
		},
		{
			// TD tie at the minimum (idx 0 and 2 both TD 2): break by most-used.
			// idx 2 has the higher count.
			name:    "td-tie-breaks-on-most-used",
			tds:     []float32{2, 9, 2},
			counts:  []int{10, 50, 40},
			honorTD: true,
			want:    2,
		},
		{
			// Override names a palette entry: it wins over auto rules.
			name:        "override-in-palette-wins",
			tds:         []float32{8, 5, 1},
			counts:      []int{10, 50, 20},
			overrideHex: "#00FF00", // green, idx 1
			honorTD:     true,
			want:        1,
		},
		{
			// Override (lowercase, no '#') still matches case-insensitively.
			name:        "override-case-insensitive",
			tds:         []float32{8, 5, 1},
			counts:      []int{10, 50, 20},
			overrideHex: "ff0000", // red, idx 0
			honorTD:     true,
			want:        0,
		},
		{
			// Override not in palette: fall through to auto (lowest TD = idx 2).
			name:        "override-not-in-palette-falls-through",
			tds:         []float32{8, 5, 1},
			counts:      []int{10, 50, 20},
			overrideHex: "#123456",
			honorTD:     true,
			want:        2,
		},
		{
			// Differing TDs but HonorTD off: TD is ignored, so legacy most-used
			// applies (no locks) — idx 1.
			name:    "differing-td-honortd-off-most-used",
			tds:     []float32{8, 5, 1},
			counts:  []int{10, 50, 20},
			honorTD: false,
			want:    1,
		},
		{
			// Missing/non-positive/NaN/Inf TDs normalize to 0 (opaque). Here
			// idx 0 has a real translucent TD and the rest normalize to opaque;
			// among the opaque tie (idx 1,2) most-used wins (idx 2).
			name:    "non-positive-td-normalizes-opaque",
			tds:     []float32{5, -1, 0},
			counts:  []int{10, 20, 50},
			honorTD: true,
			want:    2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := designateInfill(colors, tc.tds, tc.counts, tc.overrideHex, tc.honorTD, tc.hasLocked)
			if got != tc.want {
				t.Errorf("designateInfill = %d, want %d", got, tc.want)
			}
		})
	}
}
