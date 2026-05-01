package split

import (
	"math"
	"testing"
)

// TestBuildCylinder_Watertight — every triangle edge must have
// exactly two incident faces (a closed mesh has no boundary).
func TestBuildCylinder_Watertight(t *testing.T) {
	cyl, err := buildCylinder([3]float64{0, 0, 1}, 1.0, 2.0, 16)
	if err != nil {
		t.Fatalf("buildCylinder: %v", err)
	}

	// 16 segments → 16 side quads (32 tris) + 14 cap tris × 2 = 60 tris.
	if got := len(cyl.Faces); got != 60 {
		t.Errorf("face count = %d, want 60 (32 side + 28 caps)", got)
	}
	if got := len(cyl.Vertices); got != 32 {
		t.Errorf("vertex count = %d, want 32 (16 per ring × 2 rings)", got)
	}

	type ek struct{ a, b uint32 }
	mk := func(a, b uint32) ek {
		if a < b {
			return ek{a, b}
		}
		return ek{b, a}
	}
	count := make(map[ek]int)
	for _, f := range cyl.Faces {
		count[mk(f[0], f[1])]++
		count[mk(f[1], f[2])]++
		count[mk(f[2], f[0])]++
	}
	for e, n := range count {
		if n != 2 {
			t.Errorf("edge %v has %d incident faces, want 2", e, n)
			break
		}
	}
}

// TestBuildCylinder_Bbox — extents along axis are ±halfHeight, and
// extents perpendicular are ≈ ±radius.
func TestBuildCylinder_Bbox(t *testing.T) {
	cyl, err := buildCylinder([3]float64{0, 0, 1}, 2.5, 4.0, 32)
	if err != nil {
		t.Fatalf("buildCylinder: %v", err)
	}
	mn := [3]float32{math.MaxFloat32, math.MaxFloat32, math.MaxFloat32}
	mx := [3]float32{-math.MaxFloat32, -math.MaxFloat32, -math.MaxFloat32}
	for _, v := range cyl.Vertices {
		for i := 0; i < 3; i++ {
			if v[i] < mn[i] {
				mn[i] = v[i]
			}
			if v[i] > mx[i] {
				mx[i] = v[i]
			}
		}
	}
	// Z extents: ±4.0
	if math.Abs(float64(mn[2])+4) > 1e-5 || math.Abs(float64(mx[2])-4) > 1e-5 {
		t.Errorf("z extent = [%g, %g], want [-4, 4]", mn[2], mx[2])
	}
	// XY extents: should be inscribed in radius circle, exactly at 2.5
	// only at vertices that lie on the +X axis (segment 0).
	for _, v := range cyl.Vertices {
		r := math.Sqrt(float64(v[0]*v[0] + v[1]*v[1]))
		if math.Abs(r-2.5) > 1e-5 {
			t.Errorf("vertex radius = %g, want 2.5", r)
		}
	}
}

// TestBuildCylinder_RejectsBadInputs guards against silent zero-size.
func TestBuildCylinder_RejectsBadInputs(t *testing.T) {
	if _, err := buildCylinder([3]float64{0, 0, 1}, 1, 1, 2); err == nil {
		t.Error("segments=2 should error")
	}
	if _, err := buildCylinder([3]float64{0, 0, 1}, 0, 1, 8); err == nil {
		t.Error("radius=0 should error")
	}
	if _, err := buildCylinder([3]float64{0, 0, 1}, 1, 0, 8); err == nil {
		t.Error("halfHeight=0 should error")
	}
}
