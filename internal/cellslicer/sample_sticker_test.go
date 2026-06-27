package cellslicer

import (
	"image"
	"image/color"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// solidImage returns a 4×4 image filled with c — an opaque or transparent
// sticker stand-in.
func solidImage(c color.RGBA) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

// TestSampleSlabCompositesStickerFromSeparateMesh verifies the cellslicer
// color sampler composites a decal sourced from a DIFFERENT mesh than the
// base-color model — the alpha-wrap / subdivided-clone configuration the
// unified sticker path targets. Base color comes from colorModel; the decal
// triangle is found by an independent nearest-tri lookup against stickerModel.
//
// The two meshes are intentionally indexed differently: the cell's base color
// is on colorModel face 0, but the decal is on stickerModel face 1 (face 0 is
// parked far away). If the sampler wrongly reused the base model's triangle
// index (0) to look up the decal, it would miss the decal entirely and this
// test would see blue instead of red.
func TestSampleSlabCompositesStickerFromSeparateMesh(t *testing.T) {
	// Base-color mesh: one large triangle over the lower-left half of
	// [0,20]² at z=0, painted solid blue.
	colorModel := &loader.LoadedModel{
		Vertices:      [][3]float32{{0, 0, 0}, {20, 0, 0}, {0, 20, 0}},
		Faces:         [][3]uint32{{0, 1, 2}},
		FaceBaseColor: [][4]uint8{{0, 0, 255, 255}},
	}
	colorSI := voxel.NewSpatialIndex(colorModel, 4)

	// Sticker substrate: a different mesh with two faces. Face 0 is far away;
	// face 1 covers the cell and carries the decal.
	stickerModel := &loader.LoadedModel{
		Vertices: [][3]float32{
			{100, 100, 0}, {101, 100, 0}, {100, 101, 0}, // face 0, off in a corner
			{0, 0, 0}, {20, 0, 0}, {0, 20, 0}, // face 1, over the cell
		},
		Faces: [][3]uint32{{0, 1, 2}, {3, 4, 5}},
	}
	stickerSI := voxel.NewSpatialIndex(stickerModel, 4)

	// Decal on face 1: UVs u=x/20, v=y/20, so a cell around (2..6, 2..6)
	// samples well inside [0,1]².
	decalUVs := map[int32][3][2]float32{1: {{0, 0}, {1, 0}, {0, 1}}}

	// A 4×4 CCW cell square fully inside face 1 (x+y < 20).
	newSlab := func() Slab {
		return Slab{ZBot: -1, ZTop: 1, Cells: []Cell{
			{Outer: []Point2{{2, 2}, {6, 2}, {6, 6}, {2, 6}}},
		}}
	}

	const cellSize = 4

	sample := func(decals []*voxel.StickerDecal, sm *loader.LoadedModel, sSI *voxel.SpatialIndex) [3]uint8 {
		slab := newSlab()
		buf := voxel.NewSearchBuf(len(colorModel.Faces))
		var sbuf *voxel.SearchBuf
		if sm != nil {
			sbuf = voxel.NewSearchBuf(len(sm.Faces))
		}
		out := SampleSlab(&slab, 0, colorModel, colorSI, cellSize, 0,
			decals, sm, sSI, nil, nil, buf, sbuf, nil, nil, nil, nil, nil)
		if len(out) != 1 {
			t.Fatalf("want 1 cell sample, got %d", len(out))
		}
		if !out[0].Alpha {
			t.Fatalf("cell sample should be visible (Alpha=true)")
		}
		return out[0].Color
	}

	t.Run("opaque sticker overrides base", func(t *testing.T) {
		decal := &voxel.StickerDecal{Image: solidImage(color.RGBA{255, 0, 0, 255}), TriUVs: decalUVs}
		if got := sample([]*voxel.StickerDecal{decal}, stickerModel, stickerSI); got != [3]uint8{255, 0, 0} {
			t.Fatalf("opaque red sticker over blue base: want {255,0,0}, got %v", got)
		}
	})

	t.Run("transparent sticker keeps base", func(t *testing.T) {
		decal := &voxel.StickerDecal{Image: solidImage(color.RGBA{0, 0, 0, 0}), TriUVs: decalUVs}
		if got := sample([]*voxel.StickerDecal{decal}, stickerModel, stickerSI); got != [3]uint8{0, 0, 255} {
			t.Fatalf("transparent sticker: want base blue {0,0,255}, got %v", got)
		}
	})

	t.Run("no decals keeps base", func(t *testing.T) {
		if got := sample(nil, nil, nil); got != [3]uint8{0, 0, 255} {
			t.Fatalf("no decals: want base blue {0,0,255}, got %v", got)
		}
	})
}
