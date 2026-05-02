package split

import (
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// makeCube builds a 50mm cube triangle mesh aligned at the origin.
func makeCube() *loader.LoadedModel {
	verts := [][3]float32{
		{0, 0, 0}, {50, 0, 0}, {50, 50, 0}, {0, 50, 0},
		{0, 0, 50}, {50, 0, 50}, {50, 50, 50}, {0, 50, 50},
	}
	faces := [][3]uint32{
		{0, 2, 1}, {0, 3, 2}, {4, 5, 6}, {4, 6, 7},
		{0, 1, 5}, {0, 5, 4}, {2, 3, 7}, {2, 7, 6},
		{0, 4, 7}, {0, 7, 3}, {1, 2, 6}, {1, 6, 5},
	}
	return &loader.LoadedModel{Vertices: verts, Faces: faces}
}

func volume(m *loader.LoadedModel) float64 {
	v := 0.0
	for _, f := range m.Faces {
		a := m.Vertices[f[0]]
		b := m.Vertices[f[1]]
		c := m.Vertices[f[2]]
		v += float64(a[0])*(float64(b[1])*float64(c[2])-float64(c[1])*float64(b[2])) -
			float64(a[1])*(float64(b[0])*float64(c[2])-float64(c[0])*float64(b[2])) +
			float64(a[2])*(float64(b[0])*float64(c[1])-float64(c[0])*float64(b[1]))
	}
	return math.Abs(v / 6)
}

// TestCut_PegsAddsAndSubtractsVolume — cut a cube at z=25 with a peg
// connector and verify half 0's volume increased by the peg cylinder
// volume and half 1's volume decreased by the female pocket cylinder
// volume (within tolerance).
func TestCut_PegsAddsAndSubtractsVolume(t *testing.T) {
	cube := makeCube()
	flatRes, err := Cut(cube, AxisPlane(2, 25), ConnectorSettings{})
	if err != nil {
		t.Fatalf("flat Cut: %v", err)
	}
	flatV0 := volume(flatRes.Halves[0])
	flatV1 := volume(flatRes.Halves[1])

	settings := ConnectorSettings{
		Style:       Pegs,
		Count:       1,
		DiamMM:      4,
		DepthMM:     5,
		ClearanceMM: 0.15,
	}
	res, err := Cut(cube, AxisPlane(2, 25), settings)
	if err != nil {
		t.Fatalf("Pegs Cut: %v", err)
	}
	v0 := volume(res.Halves[0])
	v1 := volume(res.Halves[1])

	// Male cylinder is centered on z=25 with halfHeight=DepthMM=5, so it
	// straddles [20, 30]. Half 0 (z<=25) gains the lower half (z=25 down
	// to z=20), volume π·r²·DepthMM = π·4·5 ≈ 62.83.
	// Female cylinder is centered on z=25 with halfHeight=DepthMM+Clearance=5.15,
	// straddling [19.85, 30.15]. Half 1 (z>=25) loses the upper half with
	// female radius 2.15 and height 5.15, volume π·2.15²·5.15 ≈ 74.78.
	expectedAdded := math.Pi * 2 * 2 * 5
	expectedRemoved := math.Pi * 2.15 * 2.15 * 5.15
	delta0 := v0 - flatV0
	delta1 := flatV1 - v1
	tol := 5.0 // tolerance for cylinder discretization (32 segments)
	if math.Abs(delta0-expectedAdded) > tol {
		t.Errorf("half 0 volume added = %g, want ≈ %g (peg cylinder lower half)", delta0, expectedAdded)
	}
	if math.Abs(delta1-expectedRemoved) > tol {
		t.Errorf("half 1 volume removed = %g, want ≈ %g (pocket cylinder upper half)", delta1, expectedRemoved)
	}
}

// TestCut_DowelsRemovesFromBoth — Dowels punches matching pockets in
// both halves; both halves' volumes should decrease.
func TestCut_DowelsRemovesFromBoth(t *testing.T) {
	cube := makeCube()
	flatRes, err := Cut(cube, AxisPlane(2, 25), ConnectorSettings{})
	if err != nil {
		t.Fatalf("flat Cut: %v", err)
	}
	flatV0 := volume(flatRes.Halves[0])
	flatV1 := volume(flatRes.Halves[1])

	settings := ConnectorSettings{
		Style:       Dowels,
		Count:       1,
		DiamMM:      4,
		DepthMM:     5,
		ClearanceMM: 0.15,
	}
	res, err := Cut(cube, AxisPlane(2, 25), settings)
	if err != nil {
		t.Fatalf("Dowels Cut: %v", err)
	}
	v0 := volume(res.Halves[0])
	v1 := volume(res.Halves[1])
	if v0 >= flatV0 {
		t.Errorf("Dowels: half 0 volume = %g, want < flat %g (pocket should remove material)", v0, flatV0)
	}
	if v1 >= flatV1 {
		t.Errorf("Dowels: half 1 volume = %g, want < flat %g (pocket should remove material)", v1, flatV1)
	}
}
