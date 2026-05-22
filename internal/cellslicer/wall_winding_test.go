package cellslicer

import "testing"

// TestVerticalRouting_FlushWall covers a regression in the routing
// threshold (isPolyXYDegenerate): when a slab-clipped polygon
// collapses to a constant-Y line (a triangle from a ±Y-axis-aligned
// cube face), an XY bbox-area-relative threshold becomes 0 because
// the bbox itself has zero area in one dimension. Float-precision
// noise in polygonXYSignedArea (~3e-5 for a 20mm polygon) then
// escapes a 1e-12 absolute fallback, sending the polygon into the
// Clipper-2D cap path where its near-zero n.z makes the plane-
// equation Z-lift unstable and wall fragments vanish.
//
// Symptom: white-stripe wedge near the (low-X, low-Z) corner of the
// cube's -Y face when the GUI renders with FrontSide culling. Caught
// by TestSampledMatchesInput/cube on the otherside view.
//
// Fix: use max(xRange, yRange)² as the relative-area scale so the
// threshold survives axis-aligned bbox collapse.
func TestVerticalRouting_FlushWall(t *testing.T) {
	// Cube -Y face's bottom-right triangle (T1 in cube.stl).
	a := [3]float32{20, -20, 20}
	b := [3]float32{0, -20, 0}
	c := [3]float32{20, -20, 0}

	// Slab 1: Z ∈ [0.2, 0.4]. Before the fix, the slab-clipped polygon
	// did NOT get classified as XY-degenerate, and the cap path then
	// silently dropped its fragments.
	poly := sliceTriangleToSlab(a, b, c, 0.2, 0.4)
	if poly == nil {
		t.Fatal("expected slabPoly for vertical triangle overlapping slab Z=[0.2,0.4], got nil")
	}
	if len(poly.Pts) < 3 {
		t.Errorf("slabPoly.Pts: want >=3 verts, got %d", len(poly.Pts))
	}
	if !isPolyXYDegenerate(poly.Pts) {
		t.Errorf("expected slabPoly to be classified as XY-degenerate (vertical-clip routing); got cap-path routing")
	}
	// Normal must point outward (-Y) to match the source.
	if poly.Normal[1] >= 0 {
		t.Errorf("slabPoly.Normal.y: want < 0 (outward of cube), got %v", poly.Normal)
	}
}
