package loader

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// writePNG writes a 2x2 PNG to dir/name and returns nothing.
func writePNG(t *testing.T, path string) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{255, 0, 0, 255})
	img.Set(1, 1, color.NRGBA{0, 0, 255, 255})
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// A tiny OBJ: one textured quad (material "tex") and one untextured triangle
// (material "red", Kd 1 0 0). Exercises quad triangulation, the textured/
// untextured split, base color from Kd, UV flip, and the Y-up→Z-up transform.
const testMTL = `newmtl tex
Kd 1 1 1
map_Kd tex.png
newmtl red
Kd 1 0 0
`

const testOBJ = `mtllib model.mtl
v 0 0 0
v 1 0 0
v 1 0 1
v 0 0 1
v 2 0 0
v 3 0 0
v 2 1 0
vt 0 0
vt 1 0
vt 1 1
vt 0 1
vt 0.25 0.75
usemtl tex
f 1/1 2/2 3/3 4/4
usemtl red
f 5/1 6/2 7/3
`

func checkTestModel(t *testing.T, m *LoadedModel) {
	t.Helper()
	// Quad → 2 triangles, plus 1 triangle = 3 faces.
	if len(m.Faces) != 3 {
		t.Fatalf("faces = %d, want 3", len(m.Faces))
	}
	if len(m.Textures) != 1 {
		t.Fatalf("textures = %d, want 1", len(m.Textures))
	}
	if m.NoTextureMask == nil {
		t.Fatal("NoTextureMask is nil; expected the red triangle to be untextured")
	}

	textured, untextured := 0, 0
	var redFace = -1
	for i := range m.Faces {
		if m.NoTextureMask[i] {
			untextured++
			redFace = i
		} else {
			textured++
		}
	}
	if textured != 2 || untextured != 1 {
		t.Fatalf("textured=%d untextured=%d, want 2 and 1", textured, untextured)
	}

	// The untextured face uses material "red" → Kd 1 0 0.
	if got := m.FaceBaseColor[redFace]; got != [4]uint8{255, 0, 0, 255} {
		t.Errorf("red face base color = %v, want [255 0 0 255]", got)
	}
	// Its texture index must be the out-of-range sentinel.
	if int(m.FaceTextureIdx[redFace]) < len(m.Textures) {
		t.Errorf("red face texIdx=%d, want sentinel >= %d", m.FaceTextureIdx[redFace], len(m.Textures))
	}

	// Y-up→Z-up: OBJ (x,y,z) → (x,-z,y). The OBJ has a vertex at (1,0,1),
	// which must appear as (1,-1,0). And no vertex should have a negative Y
	// other than from a positive OBJ-Z (sanity that we transformed at all).
	wantPos := [3]float32{1, -1, 0}
	found := false
	for _, v := range m.Vertices {
		if v == wantPos {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a vertex at %v after Y-up→Z-up; got verts=%v", wantPos, m.Vertices)
	}

	// UV flip: OBJ vt (1,1) → stored (1,0); vt (0,0) → (0,1).
	wantUV := [2]float32{1, 0}
	foundUV := false
	for _, uv := range m.UVs {
		if uv == wantUV {
			foundUV = true
			break
		}
	}
	if !foundUV {
		t.Errorf("expected a flipped UV %v; got uvs=%v", wantUV, m.UVs)
	}
}

func TestLoadOBJ_Directory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model.obj"), []byte(testOBJ), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "model.mtl"), []byte(testMTL), 0o644); err != nil {
		t.Fatal(err)
	}
	writePNG(t, filepath.Join(dir, "tex.png"))

	m, err := LoadOBJ(filepath.Join(dir, "model.obj"), -1)
	if err != nil {
		t.Fatalf("LoadOBJ: %v", err)
	}
	checkTestModel(t, m)
}

func TestLoadOBJZip(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "model.zip")

	var pngBuf bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.NRGBA{255, 0, 0, 255})
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}

	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	for name, data := range map[string][]byte{
		"model.obj": []byte(testOBJ),
		"model.mtl": []byte(testMTL),
		"tex.png":   pngBuf.Bytes(),
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zf.Close()

	m, err := LoadOBJZip(zipPath, -1)
	if err != nil {
		t.Fatalf("LoadOBJZip: %v", err)
	}
	checkTestModel(t, m)
}

func TestLoadOBJ_MixedUVRejected(t *testing.T) {
	// gwob produces a ragged interleaved buffer when some faces have texture
	// coordinates and others don't. The loader must reject this rather than
	// emit corrupt geometry or panic.
	dir := t.TempDir()
	const obj = `v 0 0 0
v 1 0 0
v 1 1 0
v 2 0 0
v 3 0 0
v 3 1 0
vt 0 0
vt 1 0
vt 1 1
f 1/1 2/2 3/3
f 4 5 6
`
	if err := os.WriteFile(filepath.Join(dir, "m.obj"), []byte(obj), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOBJ(filepath.Join(dir, "m.obj"), -1); err == nil {
		t.Fatal("expected an error for a mixed-UV OBJ, got nil")
	}
}

func TestLoadOBJ_NoMTL(t *testing.T) {
	// An OBJ with no mtllib and no materials should still load as a neutral
	// untextured mesh rather than erroring.
	dir := t.TempDir()
	const obj = "v 0 0 0\nv 1 0 0\nv 0 1 0\nf 1 2 3\n"
	if err := os.WriteFile(filepath.Join(dir, "m.obj"), []byte(obj), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadOBJ(filepath.Join(dir, "m.obj"), -1)
	if err != nil {
		t.Fatalf("LoadOBJ: %v", err)
	}
	if len(m.Faces) != 1 {
		t.Fatalf("faces = %d, want 1", len(m.Faces))
	}
	if m.NoTextureMask == nil || !m.NoTextureMask[0] {
		t.Error("expected the lone face to be untextured")
	}
}
