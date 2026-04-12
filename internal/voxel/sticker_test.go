package voxel

import (
	"context"
	"image"
	"image/color"
	"testing"
)

// makeFlatGrid creates a flat 2D grid of voxels at layer 0.
func makeFlatGrid(cols, rows int, cellSize float32) ([]ActiveCell, map[CellKey]int) {
	cells := make([]ActiveCell, 0, cols*rows)
	cellMap := make(map[CellKey]int)
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			idx := len(cells)
			cell := ActiveCell{
				Grid:  0,
				Col:   c,
				Row:   r,
				Layer: 0,
				Cx:    float32(c) * cellSize,
				Cy:    float32(r) * cellSize,
				Cz:    0,
				Color: [3]uint8{128, 128, 128}, // gray
			}
			cells = append(cells, cell)
			cellMap[CellKey{Grid: 0, Col: c, Row: r, Layer: 0}] = idx
		}
	}
	return cells, cellMap
}

// makeTestImage creates a solid colored image with full alpha.
func makeTestImage(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func TestApplyStickerBasic(t *testing.T) {
	// 10x10 grid, 1mm cell size.
	cells, cellMap := makeFlatGrid(10, 10, 1.0)

	// Red sticker image, 4x4 pixels.
	img := makeTestImage(4, 4, color.NRGBA{255, 0, 0, 255})

	// Place sticker at center of grid (5, 5, 0), normal pointing up (+Z),
	// camera up is +Y. Scale = 3mm width.
	center := [3]float64{5, 5, 0}
	normal := [3]float64{0, 0, 1}
	up := [3]float64{0, 1, 0}
	scale := 3.0
	rotation := 0.0

	err := ApplySticker(context.Background(), cells, cellMap,
		img, center, normal, up, scale, rotation, 1.0)
	if err != nil {
		t.Fatal(err)
	}

	// Count how many cells turned red.
	redCount := 0
	for _, c := range cells {
		if c.Color[0] == 255 && c.Color[1] == 0 && c.Color[2] == 0 {
			redCount++
		}
	}

	// With a 3mm sticker on a 1mm grid, we expect roughly 3x3 = 9 cells affected.
	if redCount == 0 {
		t.Errorf("expected some cells to be recolored, got 0")
	}
	if redCount > 25 {
		t.Errorf("too many cells recolored: %d (sticker should cover ~9 cells)", redCount)
	}
	t.Logf("Recolored %d cells (expected ~9 for 3mm sticker on 1mm grid)", redCount)
}

func TestApplyStickerTransparent(t *testing.T) {
	cells, cellMap := makeFlatGrid(10, 10, 1.0)

	// Fully transparent image — no cells should change.
	img := makeTestImage(4, 4, color.NRGBA{255, 0, 0, 0})

	err := ApplySticker(context.Background(), cells, cellMap,
		img, [3]float64{5, 5, 0}, [3]float64{0, 0, 1}, [3]float64{0, 1, 0},
		3.0, 0.0, 1.0)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range cells {
		if c.Color != [3]uint8{128, 128, 128} {
			t.Errorf("transparent sticker should not change any colors, but found %v", c.Color)
			break
		}
	}
}

func TestApplyStickerRotation(t *testing.T) {
	// 20x20 grid to give room for a rotated sticker.
	cells, cellMap := makeFlatGrid(20, 20, 1.0)

	// Non-square image: 8 wide x 2 tall, red, to see rotation effect.
	img := makeTestImage(8, 2, color.NRGBA{255, 0, 0, 255})

	center := [3]float64{10, 10, 0}
	normal := [3]float64{0, 0, 1}
	up := [3]float64{0, 1, 0}

	// Apply with 0° rotation.
	cellsCopy0 := make([]ActiveCell, len(cells))
	copy(cellsCopy0, cells)
	err := ApplySticker(context.Background(), cellsCopy0, cellMap,
		img, center, normal, up, 8.0, 0.0, 1.0)
	if err != nil {
		t.Fatal(err)
	}

	// Apply with 90° rotation.
	cellsCopy90 := make([]ActiveCell, len(cells))
	copy(cellsCopy90, cells)
	err = ApplySticker(context.Background(), cellsCopy90, cellMap,
		img, center, normal, up, 8.0, 90.0, 1.0)
	if err != nil {
		t.Fatal(err)
	}

	// Collect recolored positions for each.
	type pos struct{ col, row int }
	red0 := map[pos]bool{}
	red90 := map[pos]bool{}
	for _, c := range cellsCopy0 {
		if c.Color[0] == 255 && c.Color[1] == 0 {
			red0[pos{c.Col, c.Row}] = true
		}
	}
	for _, c := range cellsCopy90 {
		if c.Color[0] == 255 && c.Color[1] == 0 {
			red90[pos{c.Col, c.Row}] = true
		}
	}

	// The patterns should be different (one is horizontal, the other vertical).
	if len(red0) == 0 || len(red90) == 0 {
		t.Fatalf("both rotations should color some cells: 0°=%d, 90°=%d", len(red0), len(red90))
	}

	// Check that the sets are not identical.
	same := true
	for k := range red0 {
		if !red90[k] {
			same = false
			break
		}
	}
	if same && len(red0) == len(red90) {
		t.Error("rotation should change the pattern of colored cells")
	}
	t.Logf("0° rotation: %d cells, 90° rotation: %d cells", len(red0), len(red90))
}

func TestApplyStickerNoVoxelAtCenter(t *testing.T) {
	// Empty grid — ApplySticker should return nil gracefully.
	cells := []ActiveCell{}
	cellMap := map[CellKey]int{}

	img := makeTestImage(4, 4, color.NRGBA{255, 0, 0, 255})

	err := ApplySticker(context.Background(), cells, cellMap,
		img, [3]float64{5, 5, 0}, [3]float64{0, 0, 1}, [3]float64{0, 1, 0},
		3.0, 0.0, 1.0)
	if err != nil {
		t.Fatalf("expected nil error for empty grid, got %v", err)
	}
}
