package split

import (
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// TestRecoverCapPolygons_Cube — a cube cut at z=25 yields two halves.
// Half 0 (z<=25) has its cap on z=25 with outward normal +Z. The
// recovered cap polygon should be a single 4-vertex outer loop with
// area 50×50 = 2500.
func TestRecoverCapPolygons_Cube(t *testing.T) {
	verts := [][3]float32{
		{0, 0, 0}, {50, 0, 0}, {50, 50, 0}, {0, 50, 0},
		{0, 0, 50}, {50, 0, 50}, {50, 50, 50}, {0, 50, 50},
	}
	faces := [][3]uint32{
		{0, 2, 1}, {0, 3, 2}, {4, 5, 6}, {4, 6, 7},
		{0, 1, 5}, {0, 5, 4}, {2, 3, 7}, {2, 7, 6},
		{0, 4, 7}, {0, 7, 3}, {1, 2, 6}, {1, 6, 5},
	}
	cube := &loader.LoadedModel{Vertices: verts, Faces: faces}
	res, err := Cut(cube, AxisPlane(2, 25), ConnectorSettings{})
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}

	polys, err := recoverCapPolygons(res.Halves[0], [3]float64{0, 0, 1}, 25)
	if err != nil {
		t.Fatalf("recoverCapPolygons: %v", err)
	}
	if len(polys) != 1 {
		t.Fatalf("got %d cap polygons, want 1", len(polys))
	}
	if len(polys[0].outer) < 4 {
		t.Fatalf("outer loop has %d vertices, want >= 4 (cube cap is a square; CGAL may add midpoints)", len(polys[0].outer))
	}
	if len(polys[0].holes) != 0 {
		t.Errorf("got %d holes on cube cap, want 0", len(polys[0].holes))
	}
	area := math.Abs(signedArea2D(polys[0].outer))
	want := 50.0 * 50.0
	if math.Abs(area-want) > 0.5 {
		t.Errorf("cap area = %g, want %g", area, want)
	}
}
