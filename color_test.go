package main

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/squarevoxel"
	"github.com/rtwfroody/ditherforge/internal/voxel"
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
var panchromaInventory = []palette.InventoryEntry{
	{Color: [3]uint8{0x08, 0x0A, 0x0D}, Label: "Black"},
	{Color: [3]uint8{0x55, 0x33, 0x1A}, Label: "Brown"},
	{Color: [3]uint8{0xE7, 0x2F, 0x1D}, Label: "Red"},
	{Color: [3]uint8{0xD6, 0x02, 0x12}, Label: "Wine Red"},
	{Color: [3]uint8{0xF2, 0x45, 0x74}, Label: "Magenta"},
	{Color: [3]uint8{0xF1, 0xA1, 0xAF}, Label: "Pink"},
	{Color: [3]uint8{0xF6, 0x74, 0x05}, Label: "Orange"},
	{Color: [3]uint8{0xFF, 0xE8, 0x00}, Label: "Yellow"},
	{Color: [3]uint8{0xEE, 0xD2, 0x30}, Label: "Lemon Yellow"},
	{Color: [3]uint8{0xEE, 0xD1, 0xA8}, Label: "Cream"},
	{Color: [3]uint8{0xC2, 0xAB, 0x72}, Label: "Beige"},
	{Color: [3]uint8{0xA7, 0x9E, 0x82}, Label: "Tan"},
	{Color: [3]uint8{0x06, 0x92, 0x4D}, Label: "Green"},
	{Color: [3]uint8{0xD5, 0xD7, 0x01}, Label: "Lime Green"},
	{Color: [3]uint8{0x4E, 0x74, 0x2D}, Label: "Jungle Green"},
	{Color: [3]uint8{0x94, 0x89, 0x02}, Label: "Olive Green"},
	{Color: [3]uint8{0x57, 0x5B, 0x54}, Label: "Dark Olive Drab"},
	{Color: [3]uint8{0x00, 0x37, 0x76}, Label: "Blue"},
	{Color: [3]uint8{0x00, 0x66, 0xD9}, Label: "Azure Blue"},
	{Color: [3]uint8{0x48, 0x7B, 0xA2}, Label: "Stone Blue"},
	{Color: [3]uint8{0x5E, 0xBD, 0xDB}, Label: "Aqua Blue"},
	{Color: [3]uint8{0x4C, 0xC0, 0xC7}, Label: "Polymaker Teal"},
	{Color: [3]uint8{0x6C, 0x47, 0xB2}, Label: "Purple"},
	{Color: [3]uint8{0x48, 0x52, 0x59}, Label: "Dark Grey"},
	{Color: [3]uint8{0x61, 0x64, 0x69}, Label: "Steel Grey"},
	{Color: [3]uint8{0x8C, 0x90, 0x99}, Label: "Grey"},
	{Color: [3]uint8{0xD9, 0xDF, 0xE5}, Label: "Cold White"},
	{Color: [3]uint8{0xEB, 0xF7, 0xFF}, Label: "White"},
}

// colorTestCase defines a test for inventory palette selection.
type colorTestCase struct {
	name      string
	glbPath   string
	nColors   int
	inventory []palette.InventoryEntry // nil = use testInventory
	required  [][3]uint8              // colors that must appear in the selected palette
	anyOf     [][][3]uint8            // for each group, at least one color must appear
}

var colorTests = []colorTestCase{
	{
		name:    "delorean",
		glbPath: "~/Documents/3d_print/objects/1985_delorean_dmc-12_time_machine_bttf.glb",
		nColors: 4,
		required: [][3]uint8{
			{0x7A, 0x7C, 0x7D}, // gray — needed for the car body
		},
	},
	{
		name:    "golden_pheasant",
		glbPath: "~/Documents/3d_print/objects/golden_pheasant.glb",
		nColors: 4,
		anyOf: [][][3]uint8{
			// At least one red — visually dominant on the pheasant
			{{0xD8, 0x4B, 0x2E}, {0xE7, 0x2F, 0x1D}},
		},
	},
	{
		name:      "earth",
		glbPath:   "objects/earth.glb",
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
}

func expandHome(path string) string {
	if len(path) > 1 && path[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return home + path[1:]
	}
	return path
}

func TestColorSelection(t *testing.T) {
	for _, tc := range colorTests {
		t.Run(tc.name, func(t *testing.T) {
			glbPath := expandHome(tc.glbPath)
			if _, err := os.Stat(glbPath); os.IsNotExist(err) {
				t.Skipf("model not found: %s", glbPath)
			}

			const unitScale = float32(1000)
			model, err := loader.LoadGLB(glbPath, unitScale)
			if err != nil {
				t.Fatalf("LoadGLB: %v", err)
			}

			// Scale to 100mm extent.
			ext := modelExtent(model)
			if ext != 100 {
				scale := float32(100) / ext
				model, err = loader.LoadGLB(glbPath, unitScale*scale)
				if err != nil {
					t.Fatalf("LoadGLB (rescaled): %v", err)
				}
			}

			// Voxelize to get cell colors.
			cellSize := float32(0.4 * 1.275)
			layerH := float32(0.2)
			cells, _, _, err := squarevoxel.Voxelize(context.Background(), model, cellSize, layerH)
			if err != nil {
				t.Fatalf("Voxelize: %v", err)
			}

			inv := testInventory
			if tc.inventory != nil {
				inv = tc.inventory
			}
			pcfg := voxel.PaletteConfig{
				NumColors: tc.nColors,
				Inventory: inv,
			}
			paletteRGB, _, err := voxel.ResolvePalette(cells, pcfg, true)
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
		locked := [][3]uint8{{255, 0, 0}} // red
		pcfg := voxel.PaletteConfig{
			NumColors: 3,
			Locked:    locked,
			Inventory: inv,
		}
		pal, _, err := voxel.ResolvePalette(cells, pcfg, true)
		if err != nil {
			t.Fatalf("ResolvePalette: %v", err)
		}
		if len(pal) != 3 {
			t.Fatalf("expected 3 colors, got %d", len(pal))
		}
		// First color must be the locked red.
		if pal[0] != locked[0] {
			t.Errorf("first color should be locked red, got #%02X%02X%02X", pal[0][0], pal[0][1], pal[0][2])
		}
		// Red should not appear again in the remaining colors (filtered from inventory).
		for _, p := range pal[1:] {
			if p == locked[0] {
				t.Errorf("locked color red appeared again in remaining slots")
			}
		}
	})

	t.Run("all colors locked", func(t *testing.T) {
		locked := [][3]uint8{{255, 0, 0}, {0, 0, 255}}
		pcfg := voxel.PaletteConfig{
			NumColors: 2,
			Locked:    locked,
			Inventory: inv,
		}
		pal, _, err := voxel.ResolvePalette(cells, pcfg, true)
		if err != nil {
			t.Fatalf("ResolvePalette: %v", err)
		}
		if len(pal) != 2 {
			t.Fatalf("expected 2 colors, got %d", len(pal))
		}
		if pal[0] != locked[0] || pal[1] != locked[1] {
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
			Locked:    [][3]uint8{{255, 0, 0}},
			Inventory: smallInv,
		}
		_, _, err := voxel.ResolvePalette(cells, pcfg, true)
		if err == nil {
			t.Fatalf("expected error when inventory is exhausted after filtering")
		}
	})

	t.Run("no source errors", func(t *testing.T) {
		pcfg := voxel.PaletteConfig{
			NumColors: 3,
		}
		_, _, err := voxel.ResolvePalette(cells, pcfg, true)
		if err == nil {
			t.Fatalf("expected error when no color source is set")
		}
	})
}
