package pipeline

import (
	"math"
	"sync"
	"testing"
)

// unitCubeVerts returns the 8 corners of a 1×1×1 cube at origin.
func unitCubeVerts() [][3]float32 {
	return [][3]float32{
		{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0},
		{0, 0, 1}, {1, 0, 1}, {1, 1, 1}, {0, 1, 1},
	}
}

// TestComputeSplitPreview_NoCachedLoad — without a cached load
// output, ComputeSplitPreview returns a clear error.
func TestComputeSplitPreview_NoCachedLoad(t *testing.T) {
	c := NewStageCache()
	_, err := ComputeSplitPreview(c, Options{}, SplitSettings{})
	if err == nil {
		t.Fatal("expected error when no load output is cached")
	}
}

// TestSplitPreview_EmptyVertices — degenerate input is handled with
// a clear error rather than a divide-by-zero or nil panic.
func TestSplitPreview_EmptyVertices(t *testing.T) {
	_, err := computeSplitPreviewFromVertices(nil, SplitSettings{Axis: 2})
	if err == nil {
		t.Fatal("expected error on empty vertices")
	}
}

// TestSplitPreview_PlaneEquation — Normal·Origin == Offset for all
// three axes and a range of offsets. This is the load-bearing
// invariant that lets the frontend render the cut plane correctly.
func TestSplitPreview_PlaneEquation(t *testing.T) {
	verts := unitCubeVerts()
	for axis := 0; axis < 3; axis++ {
		for _, offset := range []float64{-5, 0, 0.5, 3.7, 100} {
			res, err := computeSplitPreviewFromVertices(verts, SplitSettings{Axis: axis, Offset: offset})
			if err != nil {
				t.Fatalf("axis=%d offset=%g: %v", axis, offset, err)
			}
			dot := float64(res.Origin[0])*float64(res.Normal[0]) +
				float64(res.Origin[1])*float64(res.Normal[1]) +
				float64(res.Origin[2])*float64(res.Normal[2])
			if math.Abs(dot-offset) > 1e-5 {
				t.Errorf("axis=%d offset=%g: Normal·Origin = %g, want %g", axis, offset, dot, offset)
			}
		}
	}
}

// TestSplitPreview_BasisOrthonormal — for each axis, U × V == Normal.
// Right-handed orientation lets the frontend render the quad with
// consistent face culling.
func TestSplitPreview_BasisOrthonormal(t *testing.T) {
	verts := unitCubeVerts()
	for axis := 0; axis < 3; axis++ {
		res, err := computeSplitPreviewFromVertices(verts, SplitSettings{Axis: axis})
		if err != nil {
			t.Fatalf("axis=%d: %v", axis, err)
		}
		// U·Normal == 0 and V·Normal == 0 (basis vectors are in-plane).
		if dot := dot3(res.U, res.Normal); math.Abs(float64(dot)) > 1e-5 {
			t.Errorf("axis=%d: U·Normal = %g, want 0", axis, dot)
		}
		if dot := dot3(res.V, res.Normal); math.Abs(float64(dot)) > 1e-5 {
			t.Errorf("axis=%d: V·Normal = %g, want 0", axis, dot)
		}
		// U × V == Normal.
		cx := res.U[1]*res.V[2] - res.U[2]*res.V[1]
		cy := res.U[2]*res.V[0] - res.U[0]*res.V[2]
		cz := res.U[0]*res.V[1] - res.U[1]*res.V[0]
		if math.Abs(float64(cx-res.Normal[0])) > 1e-5 ||
			math.Abs(float64(cy-res.Normal[1])) > 1e-5 ||
			math.Abs(float64(cz-res.Normal[2])) > 1e-5 {
			t.Errorf("axis=%d: U × V = (%g, %g, %g), want %v", axis, cx, cy, cz, res.Normal)
		}
	}
}

func dot3(a, b [3]float32) float32 {
	return a[0]*b[0] + a[1]*b[1] + a[2]*b[2]
}

// TestSplitPreview_HalfExtents — for a unit cube, half-extent on
// each in-plane axis = 0.5.
func TestSplitPreview_HalfExtents(t *testing.T) {
	verts := unitCubeVerts()
	for axis := 0; axis < 3; axis++ {
		res, err := computeSplitPreviewFromVertices(verts, SplitSettings{Axis: axis, Offset: 0.5})
		if err != nil {
			t.Fatalf("axis=%d: %v", axis, err)
		}
		if math.Abs(float64(res.HalfExtentU)-0.5) > 1e-5 || math.Abs(float64(res.HalfExtentV)-0.5) > 1e-5 {
			t.Errorf("axis=%d: half-extents = (%g, %g), want (0.5, 0.5)", axis, res.HalfExtentU, res.HalfExtentV)
		}
	}
}

// TestSplitPreview_AsymmetricBbox — when the model is asymmetric
// across the in-plane axes, the returned Origin shifts off the
// world-axis-projected point but still satisfies Normal·Origin =
// Offset (the centering only translates within the plane).
func TestSplitPreview_AsymmetricBbox(t *testing.T) {
	// Model offset to (10..12, 20..23, 0..1) — asymmetric in X and Y.
	verts := [][3]float32{
		{10, 20, 0}, {12, 20, 0}, {12, 23, 0}, {10, 23, 0},
		{10, 20, 1}, {12, 20, 1}, {12, 23, 1}, {10, 23, 1},
	}
	res, err := computeSplitPreviewFromVertices(verts, SplitSettings{Axis: 2, Offset: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	// Cut at z=0.5; basis is (U=+X, V=+Y). Centre of projected bbox:
	// X=(10+12)/2=11, Y=(20+23)/2=21.5. Origin should be (11, 21.5, 0.5).
	if math.Abs(float64(res.Origin[0])-11) > 1e-5 || math.Abs(float64(res.Origin[1])-21.5) > 1e-5 {
		t.Errorf("Origin XY = (%g, %g), want (11, 21.5)", res.Origin[0], res.Origin[1])
	}
	// Plane equation still holds: Normal·Origin = Offset.
	if math.Abs(float64(res.Origin[2])-0.5) > 1e-5 {
		t.Errorf("Origin Z = %g, want 0.5 (= offset)", res.Origin[2])
	}
	if math.Abs(float64(res.HalfExtentU)-1) > 1e-5 || math.Abs(float64(res.HalfExtentV)-1.5) > 1e-5 {
		t.Errorf("half-extents = (%g, %g), want (1, 1.5)", res.HalfExtentU, res.HalfExtentV)
	}
}

// TestSplitPreview_InvalidAxisFallsBackToZ — out-of-range axis
// values (-1, 3, 99) silently fall back to Z. This matches the
// AxisPlane convention in internal/split.
func TestSplitPreview_InvalidAxisFallsBackToZ(t *testing.T) {
	verts := unitCubeVerts()
	wantZ := [3]float32{0, 0, 1}
	for _, axis := range []int{-1, 3, 99} {
		res, err := computeSplitPreviewFromVertices(verts, SplitSettings{Axis: axis})
		if err != nil {
			t.Fatalf("axis=%d: %v", axis, err)
		}
		if res.Normal != wantZ {
			t.Errorf("axis=%d: normal=%v, want Z fallback %v", axis, res.Normal, wantZ)
		}
	}
}

// TestSplitPreview_ConcurrentSafety — fires many goroutines at the
// pure helper to make sure there's no shared-state hazard. The
// helper is stateless by construction; this test exists so a
// future change can't introduce hidden state without breaking it.
func TestSplitPreview_ConcurrentSafety(t *testing.T) {
	verts := unitCubeVerts()
	const N = 64
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(axis int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, err := computeSplitPreviewFromVertices(verts, SplitSettings{Axis: axis % 3, Offset: float64(j)})
				if err != nil {
					t.Errorf("axis=%d j=%d: %v", axis, j, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

// TestProjectAxis_DotProduct — sanity check the helper.
func TestProjectAxis_DotProduct(t *testing.T) {
	p := [3]float32{3, 4, 5}
	if got := projectAxis(p, [3]float32{1, 0, 0}); got != 3 {
		t.Errorf("projectAxis on +X: got %g, want 3", got)
	}
	if got := projectAxis(p, [3]float32{0, 1, 0}); got != 4 {
		t.Errorf("projectAxis on +Y: got %g, want 4", got)
	}
	if got := projectAxis(p, [3]float32{0, 0, 1}); got != 5 {
		t.Errorf("projectAxis on +Z: got %g, want 5", got)
	}
}
