package loader

import (
	"path/filepath"
	"runtime"
	"testing"
)

// stlFixture returns the absolute path to a committed STL fixture under
// tests/objects, located relative to this source file so the test is
// independent of the working directory.
func stlFixture(name string) string {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	return filepath.Join(repoRoot, "tests", "objects", name)
}

// assertCube checks that the loaded model is the 20mm fixture cube: 12
// triangles, 8 deduplicated vertices, and a 20×20×20 bounding box. Both the
// ASCII and binary fixtures encode the same cube, so they share this check.
func assertCube(t *testing.T, model *LoadedModel) {
	t.Helper()
	if len(model.Faces) != 12 {
		t.Fatalf("expected 12 faces for a cube, got %d", len(model.Faces))
	}
	// A cube has 8 corners; the loader must weld the per-facet duplicate
	// vertices back down to those 8.
	if len(model.Vertices) != 8 {
		t.Fatalf("expected 8 deduplicated vertices for a cube, got %d", len(model.Vertices))
	}
	lo, hi := model.Vertices[0], model.Vertices[0]
	for _, v := range model.Vertices {
		for k := 0; k < 3; k++ {
			if v[k] < lo[k] {
				lo[k] = v[k]
			}
			if v[k] > hi[k] {
				hi[k] = v[k]
			}
		}
	}
	for k := 0; k < 3; k++ {
		if d := hi[k] - lo[k]; d < 19.99 || d > 20.01 {
			t.Errorf("axis %d extent = %.3f, want 20", k, d)
		}
	}
}

func TestLoadSTL_ASCII(t *testing.T) {
	model, err := LoadSTL(stlFixture("cube.stl"), -1)
	if err != nil {
		t.Fatalf("LoadSTL: %v", err)
	}
	assertCube(t, model)
}

func TestLoadSTL_Binary(t *testing.T) {
	model, err := LoadSTL(stlFixture("cube_bin.stl"), -1)
	if err != nil {
		t.Fatalf("LoadSTL: %v", err)
	}
	assertCube(t, model)
}
