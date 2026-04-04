package loader

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// writeTestZip creates a minimal 3MF ZIP in a temp file and returns its path.
func writeTestZip(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	zw.Close()
	path := filepath.Join(t.TempDir(), "test.3mf")
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad3MF_BaseMaterials(t *testing.T) {
	modelXML := `<?xml version="1.0" encoding="UTF-8"?>
<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" unit="millimeter">
 <resources>
  <basematerials id="1">
   <base name="Red" displaycolor="#FF0000"/>
   <base name="Blue" displaycolor="#0000FF"/>
  </basematerials>
  <object id="2" type="model">
   <mesh>
    <vertices>
     <vertex x="0" y="0" z="0"/>
     <vertex x="10" y="0" z="0"/>
     <vertex x="0" y="10" z="0"/>
     <vertex x="0" y="0" z="10"/>
    </vertices>
    <triangles>
     <triangle v1="0" v2="1" v3="2" pid="1" p1="0"/>
     <triangle v1="0" v2="1" v3="3" pid="1" p1="1"/>
    </triangles>
   </mesh>
  </object>
 </resources>
</model>`

	path := writeTestZip(t, map[string]string{
		"3D/3dmodel.model": modelXML,
	})

	model, err := Load3MF(path, 1.0)
	if err != nil {
		t.Fatalf("Load3MF: %v", err)
	}

	if len(model.Vertices) != 4 {
		t.Errorf("got %d vertices, want 4", len(model.Vertices))
	}
	if len(model.Faces) != 2 {
		t.Errorf("got %d faces, want 2", len(model.Faces))
	}

	// Check face colors.
	wantColors := [][4]uint8{
		{255, 0, 0, 255}, // Red
		{0, 0, 255, 255}, // Blue
	}
	for i, want := range wantColors {
		got := model.FaceBaseColor[i]
		if got != want {
			t.Errorf("face %d color = %v, want %v", i, got, want)
		}
	}

	// All faces should be marked as no-texture.
	for i, mask := range model.NoTextureMask {
		if !mask {
			t.Errorf("NoTextureMask[%d] = false, want true", i)
		}
	}
}

func TestLoad3MF_PaintColor(t *testing.T) {
	modelXML := `<?xml version="1.0" encoding="UTF-8"?>
<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" unit="millimeter">
 <resources>
  <object id="1" type="model">
   <mesh>
    <vertices>
     <vertex x="0" y="0" z="0"/>
     <vertex x="10" y="0" z="0"/>
     <vertex x="0" y="10" z="0"/>
     <vertex x="0" y="0" z="10"/>
    </vertices>
    <triangles>
     <triangle v1="0" v2="1" v3="2" paint_color="4"/>
     <triangle v1="0" v2="1" v3="3" paint_color="8"/>
    </triangles>
   </mesh>
  </object>
 </resources>
</model>`

	projectSettings := `{
  "filament_colour": ["#FF0000", "#00FF00", "#0000FF"]
}`

	path := writeTestZip(t, map[string]string{
		"3D/Objects/object_1.model":         modelXML,
		"Metadata/project_settings.config": projectSettings,
	})

	model, err := Load3MF(path, 1.0)
	if err != nil {
		t.Fatalf("Load3MF: %v", err)
	}

	if len(model.Faces) != 2 {
		t.Fatalf("got %d faces, want 2", len(model.Faces))
	}

	// paint_color "4" → filament 1 → index 0 → #FF0000
	// paint_color "8" → filament 2 → index 1 → #00FF00
	wantColors := [][4]uint8{
		{255, 0, 0, 255},
		{0, 255, 0, 255},
	}
	for i, want := range wantColors {
		got := model.FaceBaseColor[i]
		if got != want {
			t.Errorf("face %d color = %v, want %v", i, got, want)
		}
	}
}

func TestLoad3MF_Scale(t *testing.T) {
	modelXML := `<?xml version="1.0" encoding="UTF-8"?>
<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" unit="millimeter">
 <resources>
  <object id="1" type="model">
   <mesh>
    <vertices>
     <vertex x="5" y="10" z="15"/>
     <vertex x="20" y="0" z="0"/>
     <vertex x="0" y="20" z="0"/>
    </vertices>
    <triangles>
     <triangle v1="0" v2="1" v3="2"/>
    </triangles>
   </mesh>
  </object>
 </resources>
</model>`

	path := writeTestZip(t, map[string]string{
		"3D/3dmodel.model": modelXML,
	})

	model, err := Load3MF(path, 2.0)
	if err != nil {
		t.Fatalf("Load3MF: %v", err)
	}

	// Vertex 0 should be (10, 20, 30) after 2× scale.
	v := model.Vertices[0]
	if v[0] != 10 || v[1] != 20 || v[2] != 30 {
		t.Errorf("vertex 0 = %v, want [10 20 30]", v)
	}
}

func TestLoad3MF_NoColor(t *testing.T) {
	modelXML := `<?xml version="1.0" encoding="UTF-8"?>
<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" unit="millimeter">
 <resources>
  <object id="1" type="model">
   <mesh>
    <vertices>
     <vertex x="0" y="0" z="0"/>
     <vertex x="10" y="0" z="0"/>
     <vertex x="0" y="10" z="0"/>
    </vertices>
    <triangles>
     <triangle v1="0" v2="1" v3="2"/>
    </triangles>
   </mesh>
  </object>
 </resources>
</model>`

	path := writeTestZip(t, map[string]string{
		"3D/3dmodel.model": modelXML,
	})

	model, err := Load3MF(path, 1.0)
	if err != nil {
		t.Fatalf("Load3MF: %v", err)
	}

	// Default color should be white.
	got := model.FaceBaseColor[0]
	want := [4]uint8{255, 255, 255, 255}
	if got != want {
		t.Errorf("face 0 color = %v, want %v", got, want)
	}
}

func TestParseMixedFilaments(t *testing.T) {
	physical := [][4]uint8{
		{255, 0, 0, 255},   // filament 1: red
		{0, 0, 255, 255},   // filament 2: blue
		{255, 255, 0, 255}, // filament 3: yellow
	}

	// Two rows: one enabled (50% blend of red+blue), one deleted (should be skipped).
	defs := "1,2,1,0,50,0,g,w,m2,d0,o1,u1;1,3,1,0,50,0,g,w,m2,d1,o1,u2"
	result := parseMixedFilaments(defs, physical)

	if len(result) != 1 {
		t.Fatalf("got %d mixed colors, want 1", len(result))
	}
	// 50% red + 50% blue = (127, 0, 127)
	got := result[0]
	want := [4]uint8{127, 0, 127, 255}
	if got != want {
		t.Errorf("mixed color = %v, want %v", got, want)
	}
}

func TestParseMixedFilaments_Weights(t *testing.T) {
	physical := [][4]uint8{
		{200, 0, 0, 255}, // filament 1
		{0, 100, 0, 255}, // filament 2
	}

	// mix_b_percent=25 → 75% A + 25% B
	defs := "1,2,1,0,25,0,g,w,m2,d0,o1,u1"
	result := parseMixedFilaments(defs, physical)

	if len(result) != 1 {
		t.Fatalf("got %d mixed colors, want 1", len(result))
	}
	got := result[0]
	// 75% of 200 + 25% of 0 = 150, 75% of 0 + 25% of 100 = 25
	want := [4]uint8{150, 25, 0, 255}
	if got != want {
		t.Errorf("mixed color = %v, want %v", got, want)
	}
}

func TestLoad3MF_MixedFilaments(t *testing.T) {
	modelXML := `<?xml version="1.0" encoding="UTF-8"?>
<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" unit="millimeter">
 <resources>
  <object id="1" type="model">
   <mesh>
    <vertices>
     <vertex x="0" y="0" z="0"/>
     <vertex x="10" y="0" z="0"/>
     <vertex x="0" y="10" z="0"/>
    </vertices>
    <triangles>
     <triangle v1="0" v2="1" v3="2" paint_color="2C"/>
    </triangles>
   </mesh>
  </object>
 </resources>
</model>`

	// 4 physical filaments + 1 active mixed = 5 total.
	// paint_color "2C" → filament 5 → index 4 → mixed(red+blue, 50%).
	projectSettings := `{
  "filament_colour": ["#FF0000", "#0000FF", "#00FF00", "#FFFFFF"],
  "mixed_filament_definitions": "1,2,1,0,50,0,g,w,m2,d0,o1,u1"
}`

	path := writeTestZip(t, map[string]string{
		"3D/3dmodel.model":                 modelXML,
		"Metadata/project_settings.config": projectSettings,
	})

	model, err := Load3MF(path, 1.0)
	if err != nil {
		t.Fatalf("Load3MF: %v", err)
	}

	// paint_color "2C" → filament 5 → index 4 → mixed(red+blue, 50%) = (127,0,127)
	got := model.FaceBaseColor[0]
	want := [4]uint8{127, 0, 127, 255}
	if got != want {
		t.Errorf("face 0 color = %v, want %v", got, want)
	}
}

func TestParseHexColor(t *testing.T) {
	tests := []struct {
		input string
		want  [4]uint8
	}{
		{"#FF0000", [4]uint8{255, 0, 0, 255}},
		{"#00FF00FF", [4]uint8{0, 255, 0, 255}},
		{"#0000FF80", [4]uint8{0, 0, 255, 128}},
		{"AABBCC", [4]uint8{170, 187, 204, 255}},
	}
	for _, tc := range tests {
		got, err := parseHexColor(tc.input)
		if err != nil {
			t.Errorf("parseHexColor(%q): %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseHexColor(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}
