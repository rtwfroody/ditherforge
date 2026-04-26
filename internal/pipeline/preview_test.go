package pipeline

import (
	"image"
	"image/color"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// TestBuildStickerOverlayMesh verifies the alpha-wrap overlay mesh has
// the structural invariants the frontend depends on: one face per sticker
// triangle, 6 sticker UVs per face, mask=1 everywhere, atlas non-empty.
func TestBuildStickerOverlayMesh(t *testing.T) {
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0},
		},
		Faces: [][3]uint32{
			{0, 1, 2},
			{0, 2, 3},
		},
		FaceBaseColor: [][4]uint8{{200, 200, 200, 255}, {200, 200, 200, 255}},
	}
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.SetNRGBA(x, y, color.NRGBA{255, 0, 0, 255})
		}
	}
	decal := &voxel.StickerDecal{
		Image: img,
		TriUVs: map[int32][3][2]float32{
			0: {{0, 0}, {1, 0}, {1, 1}},
			// face 1 omitted on purpose — should NOT appear in overlay.
		},
	}
	mesh := buildStickerOverlayMesh(model, []*voxel.StickerDecal{decal})
	if mesh == nil {
		t.Fatal("buildStickerOverlayMesh returned nil")
	}
	wantFaces := 1
	if got := len(mesh.Faces) / 3; got != wantFaces {
		t.Errorf("face count: got %d, want %d", got, wantFaces)
	}
	if got := len(mesh.StickerUVs); got != wantFaces*6 {
		t.Errorf("sticker UV float count: got %d, want %d", got, wantFaces*6)
	}
	if got := len(mesh.StickerFaceMask); got != wantFaces {
		t.Errorf("sticker mask byte count: got %d, want %d", got, wantFaces)
	}
	for i, m := range mesh.StickerFaceMask {
		if m != 1 {
			t.Errorf("mask[%d] = %d, want 1", i, m)
		}
	}
	if got := len(mesh.StickerBounds); got != wantFaces*4 {
		t.Errorf("sticker bounds count: got %d, want %d", got, wantFaces*4)
	}
	if mesh.StickerAtlas == "" {
		t.Error("StickerAtlas is empty")
	}
	// Vertices: 3 verts per face × 3 floats.
	if got := len(mesh.Vertices); got != wantFaces*9 {
		t.Errorf("vertex float count: got %d, want %d", got, wantFaces*9)
	}
	// FaceColors: 3 entries per face.
	if got := len(mesh.FaceColors); got != wantFaces*3 {
		t.Errorf("face color count: got %d, want %d", got, wantFaces*3)
	}
}
