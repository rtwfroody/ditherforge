package cellslicer

import (
	"math"
	"testing"
)

// rectFootprint returns a Footprint consisting of a single CCW
// rectangle from (x0, y0) to (x1, y1). Used in raster smoke tests
// to avoid pulling in the full Clipper-based slab pipeline.
func rectFootprint(x0, y0, x1, y1 float32) *Footprint {
	pts := []Point2{
		{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1},
	}
	lp := FootprintLoop{Points: pts}
	lp.computeBbox()
	return &Footprint{Loops: []FootprintLoop{lp}}
}

func TestRasterizeFootprintRectangle(t *testing.T) {
	// 10×6 mm rectangle at 0.1 mm pxSize → 100×60 in-footprint area
	// inside a slightly larger raster.
	fp := rectFootprint(0, 0, 10, 6)
	pxSize := float32(0.1)
	margin := float32(0.5)
	originX, originY := -margin, -margin
	w := int(math.Ceil(float64(10+2*margin) / float64(pxSize)))
	h := int(math.Ceil(float64(6+2*margin) / float64(pxSize)))
	bits := make([]uint64, BitsForPixels(w, h))
	RasterizeFootprint(fp, originX, originY, pxSize, w, h, bits)

	r := &SlabRaster{OriginX: originX, OriginY: originY, PxSize: pxSize, W: w, H: h, InFootprint: bits}

	// Pixels well inside should be set.
	for _, p := range [][2]int{
		{int(5 / pxSize), int(3 / pxSize)},
		{int(1 / pxSize), int(1 / pxSize)},
		{int(9 / pxSize), int(5 / pxSize)},
	} {
		if !r.PixelInFootprint(p[0]+int(margin/pxSize), p[1]+int(margin/pxSize)) {
			t.Errorf("interior pixel (%d,%d) should be in-footprint", p[0], p[1])
		}
	}

	// Pixels well outside should be clear.
	for _, p := range [][2]int{
		{0, 0}, {w - 1, 0}, {0, h - 1}, {w - 1, h - 1},
	} {
		if r.PixelInFootprint(p[0], p[1]) {
			t.Errorf("exterior pixel (%d,%d) should be outside footprint", p[0], p[1])
		}
	}

	// Counted area should match nominal area within ±1 pixel per
	// boundary edge (sub-pixel quantisation slop).
	want := 10.0 * 6.0
	got := float64(FootprintPixelCount(r)) * float64(pxSize) * float64(pxSize)
	tol := 4 * (10 + 6) * float64(pxSize)
	if math.Abs(got-want) > tol {
		t.Errorf("rasterised area %.3f mm² differs from %.3f mm² by more than %.3f", got, want, tol)
	}
}

func TestRasterizeFootprintWithHole(t *testing.T) {
	// Square with a square hole: 10×10 outer, 4×4 hole centred.
	outer := []Point2{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	hole := []Point2{{3, 3}, {3, 7}, {7, 7}, {7, 3}} // CW hole (orientation ignored by even-odd)
	outerLoop := FootprintLoop{Points: outer}
	outerLoop.computeBbox()
	holeLoop := FootprintLoop{Points: hole, IsHole: true}
	holeLoop.computeBbox()
	fp := &Footprint{Loops: []FootprintLoop{outerLoop, holeLoop}}

	pxSize := float32(0.1)
	margin := float32(0.5)
	originX, originY := -margin, -margin
	w := int(math.Ceil(float64(10+2*margin) / float64(pxSize)))
	h := int(math.Ceil(float64(10+2*margin) / float64(pxSize)))
	bits := make([]uint64, BitsForPixels(w, h))
	RasterizeFootprint(fp, originX, originY, pxSize, w, h, bits)
	r := &SlabRaster{OriginX: originX, OriginY: originY, PxSize: pxSize, W: w, H: h, InFootprint: bits}

	wantArea := 100.0 - 16.0
	gotArea := float64(FootprintPixelCount(r)) * float64(pxSize) * float64(pxSize)
	tol := 4 * (10 + 10 + 4 + 4) * float64(pxSize)
	if math.Abs(gotArea-wantArea) > tol {
		t.Errorf("area with hole: got %.3f mm², want ≈ %.3f mm² (tol %.3f)", gotArea, wantArea, tol)
	}

	// Centre pixel should be in the hole (not in-footprint).
	cx := int((5-originX)/pxSize - 0.5)
	cy := int((5-originY)/pxSize - 0.5)
	if r.PixelInFootprint(cx, cy) {
		t.Errorf("hole centre pixel (%d,%d) should NOT be in-footprint", cx, cy)
	}

	// Corner-adjacent pixel inside outer but outside hole.
	if !r.PixelInFootprint(int((1-originX)/pxSize), int((1-originY)/pxSize)) {
		t.Errorf("outer-only pixel should be in-footprint")
	}
}

func TestCellOutlineFromRasterRectangle(t *testing.T) {
	// 5×4 mm rectangle as a single cell. Raster at 0.1 mm pxSize:
	// the recovered outline should trace the rectangle's grid-
	// snapped edges, collapsing to 4 corners after simplify.
	cells := []Cell{
		{Outer: []Point2{{0, 0}, {5, 0}, {5, 4}, {0, 4}}, Kind: KindHex},
	}
	fp := rectFootprint(0, 0, 5, 4)
	slab := &Slab{Footprint: fp, Cells: cells}
	r := BuildSlabRaster(slab, 1.0, 0.1)
	if r == nil {
		t.Fatal("BuildSlabRaster returned nil")
	}
	out := CellOutlineFromRaster(r, 0, 0, 0, r.W-1, r.H-1)
	if len(out) < 4 {
		t.Fatalf("outline has %d vertices, want ≥ 4", len(out))
	}
	// Outline should be axis-aligned with vertices on pxSize-grid.
	// Bbox must be approximately [0..5, 0..4] within sub-pixel slop.
	var minX, minY, maxX, maxY float32 = 1e9, 1e9, -1e9, -1e9
	for _, p := range out {
		if p[0] < minX {
			minX = p[0]
		}
		if p[1] < minY {
			minY = p[1]
		}
		if p[0] > maxX {
			maxX = p[0]
		}
		if p[1] > maxY {
			maxY = p[1]
		}
	}
	if math.Abs(float64(minX)) > 0.2 || math.Abs(float64(minY)) > 0.2 ||
		math.Abs(float64(maxX-5)) > 0.2 || math.Abs(float64(maxY-4)) > 0.2 {
		t.Errorf("outline bbox = (%.2f,%.2f)-(%.2f,%.2f), want ≈ (0,0)-(5,4)",
			minX, minY, maxX, maxY)
	}
	// Should collapse to 4 vertices for a clean rectangle.
	if len(out) != 4 {
		t.Errorf("rectangle outline simplified to %d verts, want 4: %v", len(out), out)
	}
}

func TestPartitionSlabRasterSquare(t *testing.T) {
	// Identical bot+top loops → a square footprint, ~10×10 mm.
	square := []Loop{{Points: []Point2{
		{0, 0}, {10, 0}, {10, 10}, {0, 10},
	}}}
	cells, fp, r := PartitionSlabRaster(square, square, 0.4, 0.1)
	if fp == nil || len(fp.Loops) == 0 {
		t.Fatal("PartitionSlabRaster returned nil footprint")
	}
	if r == nil {
		t.Fatal("PartitionSlabRaster returned nil raster")
	}
	if len(cells) == 0 {
		t.Fatal("PartitionSlabRaster produced no cells")
	}
	// All cells should have valid outlines (≥ 3 verts).
	for i, c := range cells {
		if len(c.Outer) < 3 {
			t.Errorf("cell %d has %d-vertex outline", i, len(c.Outer))
		}
	}
	// Summed pixel-derived cell area should ≈ footprint area (100 mm²).
	areas := CellAreasFromRaster(r, len(cells))
	total := float32(0)
	for _, a := range areas {
		total += a
	}
	// Boundary slop: pxSize × perimeter ≈ 0.1 × 40 = 4 mm² tolerance.
	if math.Abs(float64(total-100)) > 4 {
		t.Errorf("total cell area = %.2f mm², want ≈ 100 (±4)", total)
	}
	t.Logf("PartitionSlabRaster: %d cells, total area %.2f mm²", len(cells), total)
}

func TestStampCellsFromOuterMatchesPolygonArea(t *testing.T) {
	// Two cells side by side that tile a 10×4 strip.
	cells := []Cell{
		{Outer: []Point2{{0, 0}, {5, 0}, {5, 4}, {0, 4}}, Kind: KindHex},
		{Outer: []Point2{{5, 0}, {10, 0}, {10, 4}, {5, 4}}, Kind: KindHex},
	}
	fp := rectFootprint(0, 0, 10, 4)
	slab := &Slab{
		Footprint: fp,
		Cells:     cells,
	}
	r := BuildSlabRaster(slab, 1.0, 0.1) // pxSize=0.1
	if r == nil {
		t.Fatal("BuildSlabRaster returned nil")
	}

	// Each cell is a 5×4 = 20 mm² rectangle. Allow boundary-pixel
	// slop for the half-open scan-conversion + the shared edge at
	// x=5 (one cell claims, the other gets NoCellID for those pixels).
	areas := CellAreasFromRaster(r, len(cells))
	for i, a := range areas {
		if math.Abs(float64(a)-20.0) > 1.0 {
			t.Errorf("cell %d area = %.3f, want ≈ 20 (tol 1)", i, a)
		}
	}

	// Centroids should be near the geometric centres.
	cents := CellCentroidsFromRaster(r, len(cells))
	want := [][2]float32{{2.5, 2.0}, {7.5, 2.0}}
	for i, c := range cents {
		if math.Abs(float64(c[0]-want[i][0])) > 0.05 || math.Abs(float64(c[1]-want[i][1])) > 0.05 {
			t.Errorf("cell %d centroid = %v, want ≈ %v", i, c, want[i])
		}
	}

	// Summed cell pixel count should match the footprint pixel count
	// to within the shared-edge slop.
	counts := CellPixelCounts(r, len(cells))
	sumCells := int(counts[0]) + int(counts[1])
	fpCount := FootprintPixelCount(r)
	// The shared edge pixels can drop in either direction depending
	// on which cell's bbox sweeps them first; tolerance is 1 pixel
	// per edge length on average.
	tol := r.H + r.W
	if diff := fpCount - sumCells; diff < 0 || diff > tol {
		t.Errorf("footprint pixels (%d) vs summed cell pixels (%d) differ by more than %d", fpCount, sumCells, tol)
	}
}
