package voxel

import (
	"context"
	"image"
	"image/color"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

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

// makeFlatQuadModel creates a flat quad (two triangles) on the XY plane at z=0.
// The quad spans from (0,0,0) to (size,size,0).
func makeFlatQuadModel(size float32) *loader.LoadedModel {
	return &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 0},
			{size, 0, 0},
			{size, size, 0},
			{0, size, 0},
		},
		Faces: [][3]uint32{
			{0, 1, 2},
			{0, 2, 3},
		},
		FaceBaseColor: [][4]uint8{
			{128, 128, 128, 255},
			{128, 128, 128, 255},
		},
	}
}

func TestFindSeedTriangle(t *testing.T) {
	model := makeFlatQuadModel(10)
	si := NewSpatialIndex(model, 2)

	// Click near center of the quad — should find a triangle.
	tri := FindSeedTriangle([3]float64{5, 5, 0}, model, si)
	if tri < 0 {
		t.Fatal("expected to find a seed triangle")
	}
	if tri != 0 && tri != 1 {
		t.Fatalf("expected tri 0 or 1, got %d", tri)
	}

	// Click far away — should still find something (expanding radius).
	tri = FindSeedTriangle([3]float64{100, 100, 0}, model, si)
	if tri < 0 {
		t.Fatal("expected to find a seed triangle even far from mesh")
	}
}

func TestBuildStickerDecalBasic(t *testing.T) {
	model := makeFlatQuadModel(10)
	adj := BuildTriAdjacency(model)
	img := makeTestImage(4, 4, color.NRGBA{255, 0, 0, 255})

	// Place sticker at center of quad, normal pointing up (+Z).
	center := [3]float64{5, 5, 0}
	normal := [3]float64{0, 0, 1}
	up := [3]float64{0, 1, 0}
	scale := 3.0

	si := NewSpatialIndex(model, 2)
	seedTri := FindSeedTriangle(center, model, si)
	if seedTri < 0 {
		t.Fatal("no seed triangle found")
	}

	decal, err := BuildStickerDecal(context.Background(), model, adj, img, seedTri, center, normal, up, scale, 0, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(decal.TriUVs) == 0 {
		t.Fatal("expected some triangles in decal, got 0")
	}
	t.Logf("Decal covers %d triangles", len(decal.TriUVs))
}

func TestBuildStickerDecalDoesNotWrapThrough(t *testing.T) {
	// Build a box-like mesh: 6 faces (12 triangles). Place a sticker on one face.
	// The decal should not wrap to the opposite face.
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			// Front face (z=1)
			{0, 0, 1}, {1, 0, 1}, {1, 1, 1}, {0, 1, 1},
			// Back face (z=0)
			{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0},
		},
		Faces: [][3]uint32{
			// Front
			{0, 1, 2}, {0, 2, 3},
			// Back
			{5, 4, 7}, {5, 7, 6},
			// Right
			{1, 5, 6}, {1, 6, 2},
			// Left
			{4, 0, 3}, {4, 3, 7},
			// Top
			{3, 2, 6}, {3, 6, 7},
			// Bottom
			{4, 5, 1}, {4, 1, 0},
		},
		FaceBaseColor: make([][4]uint8, 12),
	}
	for i := range model.FaceBaseColor {
		model.FaceBaseColor[i] = [4]uint8{128, 128, 128, 255}
	}

	adj := BuildTriAdjacency(model)
	img := makeTestImage(4, 4, color.NRGBA{255, 0, 0, 255})

	// Place sticker on front face, centered, scale covers just the front face.
	center := [3]float64{0.5, 0.5, 1}
	normal := [3]float64{0, 0, 1}
	up := [3]float64{0, 1, 0}
	scale := 0.8

	si := NewSpatialIndex(model, 0.5)
	seedTri := FindSeedTriangle(center, model, si)
	if seedTri < 0 {
		t.Fatal("no seed triangle found")
	}

	decal, err := BuildStickerDecal(context.Background(), model, adj, img, seedTri, center, normal, up, scale, 0, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Check that no back-face triangles (indices 2,3) are in the decal.
	for _, backTri := range []int32{2, 3} {
		if _, ok := decal.TriUVs[backTri]; ok {
			t.Errorf("back face triangle %d should not be in decal", backTri)
		}
	}
	t.Logf("Decal covers %d triangles (front face has 2)", len(decal.TriUVs))
}

func TestCompositeStickerColor(t *testing.T) {
	img := makeTestImage(4, 4, color.NRGBA{255, 0, 0, 255})

	decal := &StickerDecal{
		Image: img,
		TriUVs: map[int32][3][2]float32{
			0: {
				{0, 0},
				{1, 0},
				{0.5, 1},
			},
		},
	}

	base := [4]uint8{128, 128, 128, 255}
	// Barycentric at center of triangle.
	bary := [3]float32{1.0 / 3, 1.0 / 3, 1.0 / 3}

	result := CompositeStickerColor(base, 0, bary, []*StickerDecal{decal})

	// Should be red (sticker is fully opaque red).
	if result[0] < 200 {
		t.Errorf("expected red channel > 200, got %d", result[0])
	}
	if result[1] > 50 || result[2] > 50 {
		t.Errorf("expected low green/blue, got g=%d b=%d", result[1], result[2])
	}
}

func TestCompositeStickerColorTransparent(t *testing.T) {
	img := makeTestImage(4, 4, color.NRGBA{255, 0, 0, 0}) // fully transparent

	decal := &StickerDecal{
		Image: img,
		TriUVs: map[int32][3][2]float32{
			0: {{0, 0}, {1, 0}, {0.5, 1}},
		},
	}

	base := [4]uint8{128, 128, 128, 255}
	bary := [3]float32{1.0 / 3, 1.0 / 3, 1.0 / 3}

	result := CompositeStickerColor(base, 0, bary, []*StickerDecal{decal})

	// Transparent sticker should not change the base color.
	if result != base {
		t.Errorf("transparent sticker should not change base, got %v", result)
	}
}

func TestCompositeStickerColorNoDecal(t *testing.T) {
	// Triangle not in any decal — color should be unchanged.
	base := [4]uint8{128, 128, 128, 255}
	result := CompositeStickerColor(base, 99, [3]float32{0.33, 0.33, 0.34}, nil)
	if result != base {
		t.Errorf("no decal should leave base unchanged, got %v", result)
	}
}
