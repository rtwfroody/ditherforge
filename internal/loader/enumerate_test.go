package loader

import (
	"os"
	"testing"
)

func TestEnumerate3MFObjects(t *testing.T) {
	path := os.ExpandEnv("$HOME/Documents/3d_print/NWWA.3mf")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("test file not found: %s", path)
	}
	objs, err := Enumerate3MFObjects(path)
	if err != nil {
		t.Fatalf("Enumerate3MFObjects: %v", err)
	}
	if len(objs) < 2 {
		t.Fatalf("expected multiple objects, got %d", len(objs))
	}
	for _, o := range objs {
		t.Logf("[%d] %s: %d triangles, thumbnail=%d bytes", o.Index, o.Name, o.TriCount, len(o.Thumbnail))
		if o.Thumbnail == "" {
			t.Errorf("object %d has no thumbnail", o.Index)
		}
	}
}

func TestEnumerate3MFObjects_MultiObject(t *testing.T) {
	// Build a minimal 3MF with 2 non-empty objects and 1 empty object.
	path := writeTestZip(t, map[string]string{
		"3D/3dmodel.model": `<?xml version="1.0" encoding="UTF-8"?>
<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02">
  <resources>
    <object id="1" name="Cube" type="model">
      <mesh>
        <vertices>
          <vertex x="0" y="0" z="0"/>
          <vertex x="1" y="0" z="0"/>
          <vertex x="0" y="1" z="0"/>
        </vertices>
        <triangles>
          <triangle v1="0" v2="1" v3="2"/>
        </triangles>
      </mesh>
    </object>
    <object id="2" name="Empty" type="model">
      <mesh>
        <vertices/>
        <triangles/>
      </mesh>
    </object>
    <object id="3" name="Pyramid" type="model">
      <mesh>
        <vertices>
          <vertex x="0" y="0" z="0"/>
          <vertex x="2" y="0" z="0"/>
          <vertex x="1" y="2" z="0"/>
          <vertex x="1" y="1" z="2"/>
        </vertices>
        <triangles>
          <triangle v1="0" v2="1" v3="2"/>
          <triangle v1="0" v2="1" v3="3"/>
          <triangle v1="1" v2="2" v3="3"/>
          <triangle v1="0" v2="2" v3="3"/>
        </triangles>
      </mesh>
    </object>
  </resources>
</model>`,
	})

	objs, err := Enumerate3MFObjects(path)
	if err != nil {
		t.Fatalf("Enumerate3MFObjects: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("expected 2 non-empty objects, got %d", len(objs))
	}
	if objs[0].Name != "Cube" {
		t.Errorf("expected first object named Cube, got %q", objs[0].Name)
	}
	if objs[0].TriCount != 1 {
		t.Errorf("expected 1 triangle for Cube, got %d", objs[0].TriCount)
	}
	if objs[1].Name != "Pyramid" {
		t.Errorf("expected second object named Pyramid, got %q", objs[1].Name)
	}
	if objs[1].TriCount != 4 {
		t.Errorf("expected 4 triangles for Pyramid, got %d", objs[1].TriCount)
	}
	// Verify indices are sequential.
	if objs[0].Index != 0 || objs[1].Index != 1 {
		t.Errorf("expected sequential indices 0,1 but got %d,%d", objs[0].Index, objs[1].Index)
	}
}

func TestEnumerate3MFObjects_SingleObject(t *testing.T) {
	// A file with only one non-empty object should return nil (no picker needed).
	path := writeTestZip(t, map[string]string{
		"3D/3dmodel.model": `<?xml version="1.0" encoding="UTF-8"?>
<model xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02">
  <resources>
    <object id="1" name="Solo" type="model">
      <mesh>
        <vertices>
          <vertex x="0" y="0" z="0"/>
          <vertex x="1" y="0" z="0"/>
          <vertex x="0" y="1" z="0"/>
        </vertices>
        <triangles>
          <triangle v1="0" v2="1" v3="2"/>
        </triangles>
      </mesh>
    </object>
  </resources>
</model>`,
	})

	objs, err := Enumerate3MFObjects(path)
	if err != nil {
		t.Fatalf("Enumerate3MFObjects: %v", err)
	}
	if objs != nil {
		t.Fatalf("expected nil for single-object file, got %d objects", len(objs))
	}
}
