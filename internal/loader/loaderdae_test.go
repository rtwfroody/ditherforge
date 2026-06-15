package loader

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// A minimal COLLADA 1.4.1 document: Z-up, inch units (meter=0.0254), one node
// translated +10 in X. Geometry "geo1" has one textured triangle (separate
// POSITION/TEXCOORD index streams) and one solid-red untextured triangle.
// Exercises de-indexing, the matrix transform, unit→mm scaling, the texture vs
// color material split, the sampler→surface→image chain, and the UV flip.
const testDAE = `<?xml version="1.0" encoding="UTF-8"?>
<COLLADA xmlns="http://www.collada.org/2005/11/COLLADASchema" version="1.4.1">
  <asset>
    <unit meter="0.0254" name="inch"/>
    <up_axis>Z_UP</up_axis>
  </asset>
  <library_images>
    <image id="img1"><init_from>tex.png</init_from></image>
  </library_images>
  <library_effects>
    <effect id="eff_tex">
      <profile_COMMON>
        <newparam sid="surf1"><surface type="2D"><init_from>img1</init_from></surface></newparam>
        <newparam sid="samp1"><sampler2D><source>surf1</source></sampler2D></newparam>
        <technique sid="COMMON">
          <lambert><diffuse><texture texture="samp1" texcoord="UVSET0"/></diffuse></lambert>
        </technique>
      </profile_COMMON>
    </effect>
    <effect id="eff_red">
      <profile_COMMON>
        <technique sid="COMMON">
          <lambert><diffuse><color>1 0 0 1</color></diffuse></lambert>
        </technique>
      </profile_COMMON>
    </effect>
  </library_effects>
  <library_materials>
    <material id="mat_tex"><instance_effect url="#eff_tex"/></material>
    <material id="mat_red"><instance_effect url="#eff_red"/></material>
  </library_materials>
  <library_geometries>
    <geometry id="geo1">
      <mesh>
        <source id="pos">
          <float_array id="pa" count="12">0 0 0  1 0 0  1 0 1  0 0 1</float_array>
          <technique_common><accessor count="4" source="#pa" stride="3">
            <param name="X" type="float"/><param name="Y" type="float"/><param name="Z" type="float"/>
          </accessor></technique_common>
        </source>
        <source id="uv">
          <float_array id="ua" count="8">0 0  1 0  1 1  0 1</float_array>
          <technique_common><accessor count="4" source="#ua" stride="2">
            <param name="S" type="float"/><param name="T" type="float"/>
          </accessor></technique_common>
        </source>
        <vertices id="verts1"><input semantic="POSITION" source="#pos"/></vertices>
        <triangles count="1" material="symTex">
          <input offset="0" semantic="VERTEX" source="#verts1"/>
          <input offset="1" semantic="TEXCOORD" source="#uv"/>
          <p>0 0 1 1 2 2</p>
        </triangles>
        <triangles count="1" material="symRed">
          <input offset="0" semantic="VERTEX" source="#verts1"/>
          <p>0 1 3</p>
        </triangles>
      </mesh>
    </geometry>
  </library_geometries>
  <library_visual_scenes>
    <visual_scene id="scene1">
      <node id="n1" name="n1">
        <matrix>1 0 0 10  0 1 0 0  0 0 1 0  0 0 0 1</matrix>
        <instance_geometry url="#geo1">
          <bind_material><technique_common>
            <instance_material symbol="symTex" target="#mat_tex"/>
            <instance_material symbol="symRed" target="#mat_red"/>
          </technique_common></bind_material>
        </instance_geometry>
      </node>
    </visual_scene>
  </library_visual_scenes>
</COLLADA>`

func checkDAEModel(t *testing.T, m *LoadedModel) {
	t.Helper()
	if len(m.Faces) != 2 {
		t.Fatalf("faces = %d, want 2", len(m.Faces))
	}
	if len(m.Textures) != 1 {
		t.Fatalf("textures = %d, want 1", len(m.Textures))
	}
	if m.NoTextureMask == nil {
		t.Fatal("NoTextureMask is nil; expected the red triangle to be untextured")
	}
	// One textured face, one untextured.
	nTextured, nUntextured := 0, 0
	for i := range m.Faces {
		if m.NoTextureMask[i] {
			nUntextured++
		} else {
			nTextured++
		}
	}
	if nTextured != 1 || nUntextured != 1 {
		t.Fatalf("textured/untextured = %d/%d, want 1/1", nTextured, nUntextured)
	}
	// The untextured face must carry the red Kd base color.
	for i := range m.Faces {
		if m.NoTextureMask[i] {
			bc := m.FaceBaseColor[i]
			if bc[0] != 255 || bc[1] != 0 || bc[2] != 0 {
				t.Fatalf("red face base color = %v, want [255 0 0 _]", bc)
			}
		}
	}
	// Unit scaling: file is inches (meter=0.0254 → 25.4 mm/unit) and node n1
	// translates +10 in X. Vertex at file (0,0,0) lands at x=10*25.4=254 mm.
	const wantMaxX = 11 * 25.4 // file x in {0,1} + translate 10, scaled
	minX, maxX := m.Vertices[0][0], m.Vertices[0][0]
	for _, v := range m.Vertices {
		if v[0] < minX {
			minX = v[0]
		}
		if v[0] > maxX {
			maxX = v[0]
		}
	}
	if minX < 254-0.01 || maxX > wantMaxX+0.01 {
		t.Fatalf("x range = [%.2f %.2f] mm, want within [254 %.2f]", minX, maxX, wantMaxX)
	}
}

// A material declared only via a SketchUp-style <constant><transparent> color
// whose alpha is 0 (the A_ONE opacity encoding). The loader must borrow only
// the RGB and keep the face opaque — taking alpha=0 literally would mark the
// face transparent and let voxelize drop the solid geometry.
const testDAETransparent = `<?xml version="1.0" encoding="UTF-8"?>
<COLLADA xmlns="http://www.collada.org/2005/11/COLLADASchema" version="1.4.1">
  <asset><up_axis>Z_UP</up_axis></asset>
  <library_effects>
    <effect id="eff_c">
      <profile_COMMON><technique sid="COMMON">
        <constant>
          <transparent opaque="A_ONE"><color>0.2 0.4 0.6 0</color></transparent>
          <transparency><float>1</float></transparency>
        </constant>
      </technique></profile_COMMON>
    </effect>
  </library_effects>
  <library_materials>
    <material id="mat_c"><instance_effect url="#eff_c"/></material>
  </library_materials>
  <library_geometries>
    <geometry id="g">
      <mesh>
        <source id="pos">
          <float_array id="pa" count="9">0 0 0  1 0 0  0 1 0</float_array>
          <technique_common><accessor count="3" source="#pa" stride="3">
            <param name="X" type="float"/><param name="Y" type="float"/><param name="Z" type="float"/>
          </accessor></technique_common>
        </source>
        <vertices id="v"><input semantic="POSITION" source="#pos"/></vertices>
        <triangles count="1" material="sym"><input offset="0" semantic="VERTEX" source="#v"/><p>0 1 2</p></triangles>
      </mesh>
    </geometry>
  </library_geometries>
  <library_visual_scenes><visual_scene id="s"><node id="n">
    <instance_geometry url="#g"><bind_material><technique_common>
      <instance_material symbol="sym" target="#mat_c"/>
    </technique_common></bind_material></instance_geometry>
  </node></visual_scene></library_visual_scenes>
</COLLADA>`

func TestLoadDAE_TransparentColorStaysOpaque(t *testing.T) {
	dir := t.TempDir()
	daePath := filepath.Join(dir, "c.dae")
	if err := os.WriteFile(daePath, []byte(testDAETransparent), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadDAE(daePath, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Faces) != 1 {
		t.Fatalf("faces = %d, want 1 (geometry must not be dropped)", len(m.Faces))
	}
	// RGB borrowed from the transparent color; alpha forced opaque.
	bc := m.FaceBaseColor[0]
	if bc[3] != 255 {
		t.Fatalf("base color alpha = %d, want 255 (opaque)", bc[3])
	}
	if bc[0] == 0 && bc[1] == 0 && bc[2] == 0 {
		t.Fatalf("base color RGB = %v, want the transparent color's RGB (~51,102,153)", bc)
	}
	// FaceAlpha must not mark the face translucent (nil = all opaque).
	if m.FaceAlpha != nil && m.FaceAlpha[0] < 1 {
		t.Fatalf("FaceAlpha[0] = %v, want opaque", m.FaceAlpha[0])
	}
}

func TestLoadDAE_Direct(t *testing.T) {
	dir := t.TempDir()
	daePath := filepath.Join(dir, "model.dae")
	if err := os.WriteFile(daePath, []byte(testDAE), 0o644); err != nil {
		t.Fatal(err)
	}
	writePNG(t, filepath.Join(dir, "tex.png"))

	m, err := LoadDAE(daePath, -1)
	if err != nil {
		t.Fatal(err)
	}
	checkDAEModel(t, m)
}

func TestLoadDAE_Zip(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "tex.png")
	writePNG(t, pngPath)
	pngBytes, err := os.ReadFile(pngPath)
	if err != nil {
		t.Fatal(err)
	}

	zipPath := filepath.Join(dir, "model.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	// Place the .dae under a subdirectory and the texture beside it, mirroring
	// the SketchUp export layout where init_from is "tex.png" but the entry may
	// be nested.
	for name, data := range map[string][]byte{
		"model.dae":      []byte(testDAE),
		"images/tex.png": pngBytes,
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

	// LoadZip must auto-detect the COLLADA archive and route to LoadDAEZip.
	m, err := LoadZip(zipPath, -1)
	if err != nil {
		t.Fatal(err)
	}
	checkDAEModel(t, m)
}

// TestLoadDAE_RealFile loads the user's Nord Stage export end-to-end if present.
// Skipped in CI / on machines without the asset.
func TestLoadDAE_RealFile(t *testing.T) {
	path := os.ExpandEnv("$HOME/Documents/3d_print/objects/Nord+Stage+4+-+88+High+detailed+5.3.zip")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("real asset not present: %v", err)
	}
	m, err := LoadZip(path, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Vertices) == 0 || len(m.Faces) == 0 {
		t.Fatalf("empty model: verts=%d faces=%d", len(m.Vertices), len(m.Faces))
	}
	if len(m.Textures) == 0 {
		t.Fatal("expected at least one decoded texture")
	}
	lo, hi := m.Vertices[0], m.Vertices[0]
	for _, v := range m.Vertices {
		for k := 0; k < 3; k++ {
			if v[k] < lo[k] {
				lo[k] = v[k]
			}
			if v[k] > hi[k] {
				hi[k] = v[k]
			}
		}
	}
	t.Logf("Nord Stage: verts=%d faces=%d textures=%d meshes=%d  bbox mm=[%.0f %.0f %.0f]",
		len(m.Vertices), len(m.Faces), len(m.Textures), m.NumMeshes,
		hi[0]-lo[0], hi[1]-lo[1], hi[2]-lo[2])
}
