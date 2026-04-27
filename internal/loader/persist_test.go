package loader

import (
	"bytes"
	"encoding/gob"
	"image"
	"image/color"
	"testing"
)

// TestLoadedModelGobRoundTripNoTextures: a texture-less STL-style model
// survives gob serialization with all numeric fields intact.
func TestLoadedModelGobRoundTripNoTextures(t *testing.T) {
	src := &LoadedModel{
		Vertices:       [][3]float32{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}},
		Faces:          [][3]uint32{{0, 1, 2}},
		FaceBaseColor:  [][4]uint8{{255, 0, 0, 255}},
		FaceTextureIdx: []int32{0},
		FaceMeshIdx:    []int32{0},
		NumMeshes:      1,
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(src); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var dst LoadedModel
	if err := gob.NewDecoder(&buf).Decode(&dst); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dst.Vertices) != 3 || dst.Vertices[1][0] != 1 {
		t.Errorf("vertices not preserved: %+v", dst.Vertices)
	}
	if len(dst.Faces) != 1 || dst.Faces[0][2] != 2 {
		t.Errorf("faces not preserved: %+v", dst.Faces)
	}
	if dst.NumMeshes != 1 {
		t.Errorf("NumMeshes = %d, want 1", dst.NumMeshes)
	}
	if dst.FaceBaseColor[0] != [4]uint8{255, 0, 0, 255} {
		t.Errorf("FaceBaseColor not preserved: %+v", dst.FaceBaseColor[0])
	}
}

// TestLoadedModelGobRoundTripWithTextures: textures survive raw NRGBA
// round-trip inside the gob payload.
func TestLoadedModelGobRoundTripWithTextures(t *testing.T) {
	tex := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	tex.Set(0, 0, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
	tex.Set(3, 3, color.NRGBA{R: 0, G: 255, B: 0, A: 128})
	src := &LoadedModel{
		Vertices: [][3]float32{{0, 0, 0}},
		Faces:    [][3]uint32{},
		Textures: []image.Image{tex},
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(src); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var dst LoadedModel
	if err := gob.NewDecoder(&buf).Decode(&dst); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dst.Textures) != 1 {
		t.Fatalf("Textures len = %d, want 1", len(dst.Textures))
	}
	got := dst.Textures[0]
	if got.Bounds() != tex.Bounds() {
		t.Errorf("texture bounds %v, want %v", got.Bounds(), tex.Bounds())
	}
	r, g, b, a := got.At(0, 0).RGBA()
	if r>>8 != 200 || g>>8 != 100 || b>>8 != 50 || a>>8 != 255 {
		t.Errorf("texel (0,0) lost in round trip: r=%d g=%d b=%d a=%d", r>>8, g>>8, b>>8, a>>8)
	}
}

// TestLoadedModelGobNilTextureSlot: a nil entry in Textures survives
// without panicking.
func TestLoadedModelGobNilTextureSlot(t *testing.T) {
	src := &LoadedModel{
		Vertices: [][3]float32{{0, 0, 0}},
		Textures: []image.Image{nil},
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(src); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var dst LoadedModel
	if err := gob.NewDecoder(&buf).Decode(&dst); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dst.Textures) != 1 {
		t.Fatalf("Textures len = %d, want 1", len(dst.Textures))
	}
	if dst.Textures[0] != nil {
		t.Error("nil texture slot was not preserved as nil")
	}
}
