package tests

import (
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/voxel"
	"github.com/rtwfroody/ditherforge/tests/inventories"
)

// Inventory used across color tests. Matches the user's physical filament stock.
var testInventory = []palette.InventoryEntry{
	{Color: [3]uint8{0x08, 0xab, 0xfb}, Label: "Polymaker translucent cyan"},
	{Color: [3]uint8{0xD9, 0x3B, 0x90}, Label: "Polymaker translucent magenta"},
	{Color: [3]uint8{0xF9, 0xED, 0x3D}, Label: "Polymaker translucent yellow"},
	{Color: [3]uint8{0xEB, 0xF7, 0xFF}, Label: "Polymaker Panchroma PLA White"},
	{Color: [3]uint8{0xE6, 0xDD, 0xDB}, Label: "Polymaker Panchroma Matte Cotton White"},
	{Color: [3]uint8{0xD8, 0x4B, 0x2E}, Label: "Polymaker Panchroma Matte PLA Filament, Muted Red"},
	{Color: [3]uint8{0x00, 0x00, 0x00}, Label: "Creality PLA Black"},
	{Color: [3]uint8{0x7A, 0x7C, 0x7D}, Label: "Creality PLA Grey"},
	{Color: [3]uint8{0x00, 0x9D, 0xFF}, Label: "Creality PLA Blue"},
	{Color: [3]uint8{0xE7, 0x2F, 0x1D}, Label: "Snapmaker Speed Red"},
	{Color: [3]uint8{0xF4, 0xC0, 0x32}, Label: "Snapmaker Speed Yellow"},
	{Color: [3]uint8{0x08, 0x0A, 0x0D}, Label: "Snapmaker Speed Black"},
}

// panchromaInventory is the Panchroma Basic filament set (28 colors).
// Lives in tests/inventories so the bench tool can share it.
var panchromaInventory = inventories.Panchroma()

// colorTestCase defines a test for inventory palette selection. Each
// case names a fixture PNG under testdata/color/<name>.png that the
// fixturegen tool produces from a real model. The PNG's opaque pixels
// are fed to ResolvePalette as cell colors; this keeps the test
// hermetic (no GLB/STL/texture files in the repo) while still
// exercising the scorer on real-world color distributions.
type colorTestCase struct {
	name      string
	nColors   int
	inventory []palette.InventoryEntry // nil = use testInventory
	required  [][3]uint8               // colors that must appear in the selected palette
	anyOf     [][][3]uint8             // for each group, at least one color must appear
}

var colorTests = []colorTestCase{
	{
		name:    "delorean",
		nColors: 4,
		required: [][3]uint8{
			{0x7A, 0x7C, 0x7D}, // gray — needed for the car body
		},
	},
	{
		name:    "golden_pheasant",
		nColors: 4,
		anyOf: [][][3]uint8{
			// At least one red — visually dominant on the pheasant
			{{0xD8, 0x4B, 0x2E}, {0xE7, 0x2F, 0x1D}},
		},
	},
	{
		name:      "earth",
		nColors:   4,
		inventory: panchromaInventory,
		anyOf: [][][3]uint8{
			// At least one green/brown for land
			{
				{0x4E, 0x74, 0x2D}, // Jungle Green
				{0x94, 0x89, 0x02}, // Olive Green
				{0x06, 0x92, 0x4D}, // Green
				{0x55, 0x33, 0x1A}, // Brown
				{0xC2, 0xAB, 0x72}, // Beige
				{0xA7, 0x9E, 0x82}, // Tan
			},
			// At least one blue for ocean
			{
				{0x00, 0x37, 0x76}, // Blue
				{0x00, 0x66, 0xD9}, // Azure Blue
				{0x48, 0x7B, 0xA2}, // Stone Blue
				{0x5E, 0xBD, 0xDB}, // Aqua Blue
			},
		},
	},
	{
		name:      "bricks_benchy",
		nColors:   4,
		inventory: panchromaInventory,
		anyOf: [][][3]uint8{
			// At least one warm chromatic color so the brick texture
			// reads as terracotta rather than tan-with-cool-flecks.
			// Without one of these the scorer falls back to a hull-
			// covering set (Tan + Orange + Blue + White) that
			// reproduces the cell-color average correctly but reads
			// perceptually as the wrong color.
			{
				{0xE7, 0x2F, 0x1D}, // Red
				{0xD6, 0x02, 0x12}, // Wine Red
				{0x55, 0x33, 0x1A}, // Brown
			},
		},
	},
}

// loadCellsFromPNG reads a fixture PNG and returns one ActiveCell per
// opaque pixel, with that pixel's RGB as the cell color. Background
// (alpha < 128) pixels are skipped so transparent regions of the
// strip don't pollute the histogram. The Lab math downstream cares
// only about Color, so other ActiveCell fields can stay zero.
//
// The fixturegen tool writes via png.Encode of an *image.RGBA, which
// produces a non-paletted, gamma-encoded sRGB PNG; png.Decode returns
// either *image.NRGBA or *image.RGBA depending on whether the file
// has an alpha channel, both with sRGB-encoded straight channels for
// non-paletted data. Either way the per-pixel color matches what the
// live pipeline gives the dither stage (8-bit sRGB straight RGB), so
// no gamma conversion is needed; we just read the channels via
// image.At which normalizes both representations to the same model.
func loadCellsFromPNG(t *testing.T, path string) []voxel.ActiveCell {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	b := img.Bounds()
	cells := make([]voxel.ActiveCell, 0, b.Dx()*b.Dy())
	switch im := img.(type) {
	case *image.NRGBA:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := im.NRGBAAt(x, y)
				if c.A < 128 {
					continue
				}
				cells = append(cells, voxel.ActiveCell{Color: [3]uint8{c.R, c.G, c.B}})
			}
		}
	default:
		// Paletted, RGBA, or any other concrete type: copy through
		// NRGBA so we never read premultiplied channels.
		nrgba := image.NewNRGBA(b)
		draw.Draw(nrgba, b, img, b.Min, draw.Src)
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := nrgba.NRGBAAt(x, y)
				if c.A < 128 {
					continue
				}
				cells = append(cells, voxel.ActiveCell{Color: [3]uint8{c.R, c.G, c.B}})
			}
		}
	}
	return cells
}

func TestColorSelection(t *testing.T) {
	for _, tc := range colorTests {
		t.Run(tc.name, func(t *testing.T) {
			fixturePath := filepath.Join("testdata", "color", tc.name+".png")
			if _, err := os.Stat(fixturePath); os.IsNotExist(err) {
				t.Skipf("fixture not found: %s (regenerate with: go run ./fixturegen --only %s)", fixturePath, tc.name)
			}
			cells := loadCellsFromPNG(t, fixturePath)
			if len(cells) == 0 {
				t.Fatalf("fixture %s contained no opaque pixels", fixturePath)
			}

			inv := testInventory
			if tc.inventory != nil {
				inv = tc.inventory
			}
			pcfg := voxel.PaletteConfig{
				NumColors: tc.nColors,
				Inventory: inv,
			}
			paletteRGB, _, _, err := voxel.ResolvePalette(context.Background(), cells, pcfg, true, progress.NullTracker{})
			if err != nil {
				t.Fatalf("ResolvePalette: %v", err)
			}

			t.Logf("Selected palette:")
			for i, p := range paletteRGB {
				t.Logf("  [%d] #%02X%02X%02X", i, p[0], p[1], p[2])
			}

			for _, req := range tc.required {
				found := false
				for _, p := range paletteRGB {
					if p == req {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("required color #%02X%02X%02X not in selected palette", req[0], req[1], req[2])
				}
			}

			for _, group := range tc.anyOf {
				found := false
				for _, candidate := range group {
					for _, p := range paletteRGB {
						if p == candidate {
							found = true
							break
						}
					}
					if found {
						break
					}
				}
				if !found {
					names := make([]string, len(group))
					for i, c := range group {
						names[i] = fmt.Sprintf("#%02X%02X%02X", c[0], c[1], c[2])
					}
					t.Errorf("none of %v found in selected palette", names)
				}
			}
		})
	}
}

func TestResolvePaletteWithLockedColors(t *testing.T) {
	// Use a small inventory to keep the test fast.
	inv := []palette.InventoryEntry{
		{Color: [3]uint8{0, 0, 0}, Label: "black"},
		{Color: [3]uint8{255, 255, 255}, Label: "white"},
		{Color: [3]uint8{255, 0, 0}, Label: "red"},
		{Color: [3]uint8{0, 0, 255}, Label: "blue"},
		{Color: [3]uint8{0, 255, 0}, Label: "green"},
	}

	// Fake cells with varied colors.
	cells := []voxel.ActiveCell{
		{Color: [3]uint8{200, 50, 50}},
		{Color: [3]uint8{50, 50, 200}},
		{Color: [3]uint8{10, 10, 10}},
		{Color: [3]uint8{240, 240, 240}},
	}

	t.Run("locked colors appear in palette", func(t *testing.T) {
		locked := []palette.InventoryEntry{{Color: [3]uint8{255, 0, 0}}} // red
		pcfg := voxel.PaletteConfig{
			NumColors: 3,
			Locked:    locked,
			Inventory: inv,
		}
		pal, _, _, err := voxel.ResolvePalette(context.Background(), cells, pcfg, true, progress.NullTracker{})
		if err != nil {
			t.Fatalf("ResolvePalette: %v", err)
		}
		if len(pal) != 3 {
			t.Fatalf("expected 3 colors, got %d", len(pal))
		}
		// First color must be the locked red.
		if pal[0] != locked[0].Color {
			t.Errorf("first color should be locked red, got #%02X%02X%02X", pal[0][0], pal[0][1], pal[0][2])
		}
		// Red should not appear again in the remaining colors (filtered from inventory).
		for _, p := range pal[1:] {
			if p == locked[0].Color {
				t.Errorf("locked color red appeared again in remaining slots")
			}
		}
	})

	t.Run("all colors locked", func(t *testing.T) {
		locked := []palette.InventoryEntry{{Color: [3]uint8{255, 0, 0}}, {Color: [3]uint8{0, 0, 255}}}
		pcfg := voxel.PaletteConfig{
			NumColors: 2,
			Locked:    locked,
			Inventory: inv,
		}
		pal, _, _, err := voxel.ResolvePalette(context.Background(), cells, pcfg, true, progress.NullTracker{})
		if err != nil {
			t.Fatalf("ResolvePalette: %v", err)
		}
		if len(pal) != 2 {
			t.Fatalf("expected 2 colors, got %d", len(pal))
		}
		if pal[0] != locked[0].Color || pal[1] != locked[1].Color {
			t.Errorf("palette should be exactly the locked colors")
		}
	})

	t.Run("empty inventory after filtering errors", func(t *testing.T) {
		// Inventory has only red; lock red. No colors left.
		smallInv := []palette.InventoryEntry{
			{Color: [3]uint8{255, 0, 0}, Label: "red"},
		}
		pcfg := voxel.PaletteConfig{
			NumColors: 2,
			Locked:    []palette.InventoryEntry{{Color: [3]uint8{255, 0, 0}}},
			Inventory: smallInv,
		}
		_, _, _, err := voxel.ResolvePalette(context.Background(), cells, pcfg, true, progress.NullTracker{})
		if err == nil {
			t.Fatalf("expected error when inventory is exhausted after filtering")
		}
	})

	t.Run("no source errors", func(t *testing.T) {
		pcfg := voxel.PaletteConfig{
			NumColors: 3,
		}
		_, _, _, err := voxel.ResolvePalette(context.Background(), cells, pcfg, true, progress.NullTracker{})
		if err == nil {
			t.Fatalf("expected error when no color source is set")
		}
	})
}
