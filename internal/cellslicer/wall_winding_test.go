package cellslicer

import "testing"

// TestVerticalRouting_FlushWall covers a regression in
// sliceTriangleToSlab's vertical-routing threshold: when a slab-
// clipped polygon collapses to a constant-Y line (a triangle from a
// ±Y-axis-aligned cube face), polygonXYBboxArea = 0 and the
// relative threshold (1e-6*bbox) becomes 0. Float-precision noise
// in polygonXYSignedArea (~3e-5 for a 20mm polygon) then escapes
// the 1e-12 absolute fallback, sending the polygon into the
// barycentric slabTri path where every sub-triangle is filtered
// for zero area and the wall fragment vanishes.
//
// Symptom: white-stripe wedge near the (low-X, low-Z) corner of
// the cube's -Y face when the GUI renders with FrontSide culling.
// Caught by TestSampledMatchesInput/cube on the otherside view.
//
// Fix: use max(xRange, yRange)² as the relative-area scale so the
// threshold survives axis-aligned bbox collapse.
func TestVerticalRouting_FlushWall(t *testing.T) {
	// Cube -Y face's bottom-right triangle (T1 in the cube.stl).
	a := [3]float32{20, -20, 20}
	b := [3]float32{0, -20, 0}
	c := [3]float32{20, -20, 0}

	// Slab 1: Z ∈ [0.2, 0.4]. Before the fix this returned (nil, nil).
	pieces, vpoly := sliceTriangleToSlab(a, b, c, 0.2, 0.4)
	if vpoly == nil {
		t.Errorf("expected vpoly for axis-aligned vertical triangle in slab Z=[0.2,0.4], got nil")
	}
	if len(pieces) != 0 {
		t.Errorf("expected zero slabTris (the triangle is vertical), got %d", len(pieces))
	}
	if vpoly != nil {
		if len(vpoly.Pts) < 3 {
			t.Errorf("vpoly.Pts: want >=3 verts, got %d", len(vpoly.Pts))
		}
		// Normal must point outward (-Y) to match the source.
		if vpoly.Normal[1] >= 0 {
			t.Errorf("vpoly.Normal.y: want < 0 (outward of cube), got %v", vpoly.Normal)
		}
	}
}
