package split

import (
	"math"
	"testing"
)

const tiltEps = 1e-9

func vecClose(a, b [3]float64, eps float64) bool {
	return math.Abs(a[0]-b[0]) < eps && math.Abs(a[1]-b[1]) < eps && math.Abs(a[2]-b[2]) < eps
}

// TestTiltedFrame_ZeroIsAxisAligned — with both angles 0, TiltedFrame
// reproduces the axis-aligned normal and AxisBasis exactly, so a
// zero-tilt cut is bit-identical to the legacy AxisPlane path.
func TestTiltedFrame_ZeroIsAxisAligned(t *testing.T) {
	for axis := 0; axis < 3; axis++ {
		n, u, v := TiltedFrame(axis, 0, 0)
		var wantN [3]float64
		wantN[axis] = 1
		wu, wv := AxisBasis(axis)
		if !vecClose(n, wantN, tiltEps) || !vecClose(u, wu, tiltEps) || !vecClose(v, wv, tiltEps) {
			t.Errorf("axis=%d: got n=%v u=%v v=%v, want n=%v u=%v v=%v", axis, n, u, v, wantN, wu, wv)
		}
	}
}

// TestTiltedFrame_Orthonormal — for a spread of angles the returned
// frame stays orthonormal and right-handed (u × v == normal), which
// split.Cut requires (it rejects non-unit normals).
func TestTiltedFrame_Orthonormal(t *testing.T) {
	angles := []float64{-85, -30, -1, 12, 45, 60, 85}
	for axis := 0; axis < 3; axis++ {
		for _, a := range angles {
			for _, b := range angles {
				n, u, v := TiltedFrame(axis, a, b)
				if math.Abs(dot3(n, n)-1) > 1e-9 {
					t.Errorf("axis=%d a=%g b=%g: normal not unit (|n|²=%g)", axis, a, b, dot3(n, n))
				}
				if math.Abs(dot3(u, v)) > 1e-9 || math.Abs(dot3(u, n)) > 1e-9 || math.Abs(dot3(v, n)) > 1e-9 {
					t.Errorf("axis=%d a=%g b=%g: basis not orthogonal", axis, a, b)
				}
				if !vecClose(cross3(u, v), n, 1e-9) {
					t.Errorf("axis=%d a=%g b=%g: u×v=%v, want normal=%v", axis, a, b, cross3(u, v), n)
				}
			}
		}
	}
}

// TestTiltedFrame_KnownRotation — a 90° tilt about the in-plane U axis
// of the Z cut (U=+X) sends the +Z normal to −Y, pinning the rotation
// sense and axis convention.
func TestTiltedFrame_KnownRotation(t *testing.T) {
	n, _, _ := TiltedFrame(2, 90, 0)
	if !vecClose(n, [3]float64{0, -1, 0}, 1e-9) {
		t.Errorf("TiltedFrame(Z, 90, 0) normal = %v, want (0,-1,0)", n)
	}
}

// TestPlaneThrough — D is Normal·pivot, and for a zero tilt this
// reduces to AxisPlane's offset.
func TestPlaneThrough(t *testing.T) {
	n, _, _ := TiltedFrame(2, 0, 0)
	pivot := [3]float64{7, -3, 12.5} // arbitrary; only the Z component matters at zero tilt
	p := PlaneThrough(n, pivot)
	if math.Abs(p.D-12.5) > 1e-9 {
		t.Errorf("PlaneThrough zero-tilt D = %g, want 12.5 (pivot Z)", p.D)
	}
	if !isUnitNormal(p.Normal) {
		t.Errorf("PlaneThrough produced non-unit normal %v", p.Normal)
	}
	// Tilted: D must equal the explicit dot product.
	nt, _, _ := TiltedFrame(2, 30, 15)
	pt := PlaneThrough(nt, pivot)
	if math.Abs(pt.D-dot3(nt, pivot)) > 1e-12 {
		t.Errorf("PlaneThrough tilted D = %g, want %g", pt.D, dot3(nt, pivot))
	}
}
