package loader

import (
	"os"
	"testing"
)

func TestLoadSTL_ASCII(t *testing.T) {
	path := os.ExpandEnv("$HOME/Documents/3d_print/20mm_cube.stl")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("test file not found: %s", path)
	}

	model, err := LoadSTL(path, 1.0, -1)
	if err != nil {
		t.Fatalf("LoadSTL: %v", err)
	}
	if len(model.Faces) == 0 {
		t.Fatal("expected faces")
	}
	// A 20mm cube has 12 triangles and 8 unique vertices after dedup.
	if len(model.Vertices) != 8 {
		t.Fatalf("expected 8 deduplicated vertices for a cube, got %d", len(model.Vertices))
	}
	t.Logf("Loaded %d vertices, %d faces", len(model.Vertices), len(model.Faces))
}

func TestLoadSTL_Binary(t *testing.T) {
	path := os.ExpandEnv("$HOME/Documents/3d_print/bday-alex.stl")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("test file not found: %s", path)
	}

	model, err := LoadSTL(path, 1.0, -1)
	if err != nil {
		t.Fatalf("LoadSTL: %v", err)
	}
	if len(model.Faces) == 0 {
		t.Fatal("expected faces")
	}
	t.Logf("Loaded %d vertices, %d faces", len(model.Vertices), len(model.Faces))
}
