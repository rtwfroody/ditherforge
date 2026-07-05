package pipeline

import "testing"

// TestComputeColorUsage checks the read-only per-color triangle histogram:
// each palette color gets its face count and "#RRGGBB" hex, out-of-range and
// negative (hidden) assignments are ignored, and the ordering follows the
// palette index.
func TestComputeColorUsage(t *testing.T) {
	palette := [][3]uint8{
		{0x10, 0x10, 0x10},
		{0xF0, 0xF0, 0xF0},
		{0xDC, 0x50, 0x3C},
	}
	// 6 faces: color0 x3, color2 x2, one hidden (-1). color1 goes unused.
	assignments := []int32{0, 0, 2, -1, 2, 0}

	usage := computeColorUsage(assignments, palette)
	if len(usage) != len(palette) {
		t.Fatalf("got %d usage entries, want %d", len(usage), len(palette))
	}

	wantTris := []int{3, 0, 2}
	wantHex := []string{"#101010", "#F0F0F0", "#DC503C"}
	for i := range palette {
		if usage[i].PaletteIndex != i {
			t.Errorf("entry %d: PaletteIndex = %d, want %d", i, usage[i].PaletteIndex, i)
		}
		if usage[i].Triangles != wantTris[i] {
			t.Errorf("color %d: Triangles = %d, want %d", i, usage[i].Triangles, wantTris[i])
		}
		if usage[i].Hex != wantHex[i] {
			t.Errorf("color %d: Hex = %q, want %q", i, usage[i].Hex, wantHex[i])
		}
	}
}

// TestComputeColorUsageEmpty tolerates an empty palette / no faces.
func TestComputeColorUsageEmpty(t *testing.T) {
	if got := computeColorUsage(nil, nil); len(got) != 0 {
		t.Fatalf("empty palette should yield no usage, got %d", len(got))
	}
	usage := computeColorUsage(nil, [][3]uint8{{1, 2, 3}})
	if len(usage) != 1 || usage[0].Triangles != 0 {
		t.Fatalf("no faces should yield zero-count entry, got %+v", usage)
	}
}
