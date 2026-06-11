package cellslicer

import (
	"context"

	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

func TestSlabIndexForZ(t *testing.T) {
	planes := []float32{0, 1, 2, 3} // 3 slabs: [0,1) [1,2) [2,3)
	cases := []struct {
		z    float32
		want int
	}{
		{-0.1, -1}, // below
		{0, 0},     // on bottom plane → slab 0
		{0.5, 0},
		{1, 1}, // on interior plane → upper slab (half-open [1,2))
		{1.5, 1},
		{2.999, 2},
		{3, -1},   // on top plane → out (half-open)
		{3.5, -1}, // above
	}
	for _, c := range cases {
		if got := slabIndexForZ(planes, c.z); got != c.want {
			t.Errorf("slabIndexForZ(%.3f) = %d, want %d", c.z, got, c.want)
		}
	}
}

func TestNearHorizontal(t *testing.T) {
	cases := []struct {
		name    string
		a, b, c [3]float32
		want    bool
	}{
		{"flat", [3]float32{0, 0, 5}, [3]float32{1, 0, 5}, [3]float32{0, 1, 5}, true},
		{"vertical", [3]float32{0, 0, 0}, [3]float32{1, 0, 0}, [3]float32{0, 0, 1}, false},
		// rise 1 over run 1 → 45°, |nz| ≈ 0.707 < 0.9
		{"tilted45", [3]float32{0, 0, 0}, [3]float32{1, 0, 1}, [3]float32{0, 1, 0}, false},
		// rise 0.05 over run 1 → ~3°, near-horizontal
		{"gentle", [3]float32{0, 0, 0}, [3]float32{1, 0, 0.05}, [3]float32{0, 1, 0}, true},
		{"degenerate", [3]float32{0, 0, 0}, [3]float32{0, 0, 0}, [3]float32{0, 0, 0}, false},
	}
	for _, c := range cases {
		if got := nearHorizontal(c.a, c.b, c.c); got != c.want {
			t.Errorf("nearHorizontal(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTriPathCCW(t *testing.T) {
	// A clockwise XY winding must come back out CCW (positive signed area).
	a := [3]float32{0, 0, 0}
	b := [3]float32{0, 1, 0} // a→b→c is CW
	c := [3]float32{1, 0, 0}
	path := triPathCCW(a, b, c)
	pts := clipperPathToPoints(path)
	if area := signedArea(pts); area <= 0 {
		t.Errorf("triPathCCW produced area %.3f, want > 0 (CCW)", area)
	}
	// Already-CCW input stays CCW.
	path2 := triPathCCW(a, c, b)
	if area := signedArea(clipperPathToPoints(path2)); area <= 0 {
		t.Errorf("triPathCCW(CCW input) produced area %.3f, want > 0", area)
	}
}

// quad returns the two triangles of an axis-aligned XY square [x0,x1]×[y0,y1]
// at heights z00,z10,z11,z01 (CCW corners), as faces appended to verts.
func appendQuad(verts *[][3]float32, faces *[][3]uint32, x0, y0, x1, y1, z float32) {
	base := uint32(len(*verts))
	*verts = append(*verts,
		[3]float32{x0, y0, z}, [3]float32{x1, y0, z},
		[3]float32{x1, y1, z}, [3]float32{x0, y1, z})
	*faces = append(*faces, [3]uint32{base, base + 1, base + 2}, [3]uint32{base, base + 2, base + 3})
}

func TestInteriorHorizontalFootprints(t *testing.T) {
	planes := []float32{0, 1, 2, 3} // 3 slabs
	var verts [][3]float32
	var faces [][3]uint32

	// (1) Thin flat plate at Z=1.5, wholly inside slab 1, XY [0,10]².
	appendQuad(&verts, &faces, 0, 0, 10, 10, 1.5)
	// (2) Vertical wall fragment wholly inside slab 1 at X=20 (must be
	//     filtered out as not near-horizontal).
	base := uint32(len(verts))
	verts = append(verts,
		[3]float32{20, 0, 1.2}, [3]float32{20, 5, 1.2}, [3]float32{20, 0, 1.8})
	faces = append(faces, [3]uint32{base, base + 1, base + 2})

	model := &loader.LoadedModel{Vertices: verts, Faces: faces}
	out, ifpErr := InteriorHorizontalFootprints(context.Background(), model, planes)
	if ifpErr != nil {
		t.Fatalf("InteriorHorizontalFootprints: %v", ifpErr)
	}

	if len(out) != 3 {
		t.Fatalf("got %d slabs, want 3", len(out))
	}
	if out[0] != nil {
		t.Errorf("slab 0 should have no interior faces, got %d loops", len(out[0].Loops))
	}
	if out[1] == nil {
		t.Fatal("slab 1 should contain the plate projection, got nil")
	}
	if !out[1].Contains(5, 5) {
		t.Errorf("slab 1 footprint should contain the plate center (5,5)")
	}
	if out[1].Contains(20, 2) {
		t.Errorf("slab 1 footprint should NOT contain the vertical wall fragment at (20,2)")
	}
	if out[2] != nil {
		t.Errorf("slab 2 should have no interior faces, got %d loops", len(out[2].Loops))
	}
}

func TestInteriorHorizontalFootprints_CrossingPlaneExcluded(t *testing.T) {
	planes := []float32{0, 1, 2, 3}
	var verts [][3]float32
	var faces [][3]uint32
	// A near-horizontal plate that straddles plane Z=2.0 (Z 1.95..2.05):
	// the bounding-plane slice already owns it, so it must NOT be projected.
	base := uint32(len(verts))
	verts = append(verts,
		[3]float32{0, 0, 1.95}, [3]float32{10, 0, 1.95},
		[3]float32{10, 10, 2.05}, [3]float32{0, 10, 2.05})
	faces = append(faces,
		[3]uint32{base, base + 1, base + 2}, [3]uint32{base, base + 2, base + 3})

	model := &loader.LoadedModel{Vertices: verts, Faces: faces}
	out, ifpErr := InteriorHorizontalFootprints(context.Background(), model, planes)
	if ifpErr != nil {
		t.Fatalf("InteriorHorizontalFootprints: %v", ifpErr)
	}
	for i, fp := range out {
		if fp != nil && fp.Contains(5, 5) {
			t.Errorf("slab %d should not contain a plane-crossing plate at (5,5)", i)
		}
	}
}
