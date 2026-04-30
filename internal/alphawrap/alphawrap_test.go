package alphawrap

import (
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// TestWrapTetrahedron wraps a simple tetrahedron via CGAL's
// alpha_wrap_3.
func TestWrapTetrahedron(t *testing.T) {
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 0}, {1, 0, 0}, {0, 1, 0}, {0, 0, 1},
		},
		Faces: [][3]uint32{
			{0, 1, 2}, {0, 2, 3}, {0, 3, 1}, {1, 3, 2},
		},
	}

	out, err := Wrap(model, 0.1, 0.01)
	if err != nil {
		t.Fatalf("Wrap failed: %v", err)
	}
	if len(out.Vertices) == 0 || len(out.Faces) == 0 {
		t.Fatalf("Wrap produced empty mesh: %d verts, %d faces", len(out.Vertices), len(out.Faces))
	}
	t.Logf("wrapped tetrahedron: %d verts, %d faces", len(out.Vertices), len(out.Faces))
}
