package split

import (
	"math"
	"testing"
)

// capNormalAfterLayout runs a tilted XY cut with the given per-half
// orientations and returns each half's outward cut-face normal in bed
// coordinates after Layout.
func capNormalAfterLayout(t *testing.T, tiltA, tiltB float64, oA, oB Orientation) [2][3]float64 {
	t.Helper()
	cube := makeUnitCube()
	normal, _, _ := TiltedFrame(2, tiltA, tiltB)
	res, err := Cut(cube, PlaneThrough(normal, [3]float64{0.5, 0.5, 0.5}), ConnectorSettings{})
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	res.Orientation = [2]Orientation{oA, oB}
	res.Axis = 2
	res.CapAlign = FrameAlignRotation(2, tiltA, tiltB)
	xf := Layout(res, 5.0)

	capModel := [2][3]float64{normal, {-normal[0], -normal[1], -normal[2]}}
	var out [2][3]float64
	for h := 0; h < 2; h++ {
		cb := applyRotation(xf[h].Rotation, [3]float32{
			float32(capModel[h][0]), float32(capModel[h][1]), float32(capModel[h][2]),
		})
		out[h] = [3]float64{float64(cb[0]), float64(cb[1]), float64(cb[2])}
	}
	return out
}

// TestLayout_CutFaceDownSeatsFlatUnderTilt — the headline fix. With a
// 4°-tilted XY cut and a half oriented "cut face down" (z-down), the
// cut face must end up pointing straight down (flat on the bed), not at
// the tilt angle. The other half (x-up, a model-axis orientation) must
// be unaffected: it rests on a model side, so its cut face points
// sideways regardless of the tilt.
func TestLayout_CutFaceDownSeatsFlatUnderTilt(t *testing.T) {
	caps := capNormalAfterLayout(t, -4, 0, OrientZDown, OrientXUp)

	// Half 0 (z-down = cut face down): cap normal → (0,0,-1).
	angDown := math.Acos(clamp01(-caps[0][2])) * 180 / math.Pi
	if angDown > 0.01 {
		t.Errorf("z-down half: cut face is %.3f° off the bed, want flat (cap=%v)", angDown, caps[0])
	}
	// Half 1 (x-up = model axis): cap points sideways, unaffected by tilt.
	if math.Abs(caps[1][2]) > 0.01 {
		t.Errorf("x-up half: cap normal has vertical component %.3f, want ~0 (rests on model face, cap=%v)", caps[1][2], caps[1])
	}
}

// TestLayout_CutFaceSeatsFlatBothHalves — with both halves on a
// cut-axis orientation (z-up), each cap must end up exactly flat
// (vertical normal) regardless of tilt. The two halves carry opposite
// cap normals (+N and −N), so the same z-up choice seats half 0's cap
// up and half 1's cap down — both flat. The point of the fix is the
// flatness, not the direction.
func TestLayout_CutFaceSeatsFlatBothHalves(t *testing.T) {
	caps := capNormalAfterLayout(t, 6, -3, OrientZUp, OrientZUp)
	// Half 0 cap (+N) → straight up; half 1 cap (−N) → straight down.
	wantZ := [2]float64{+1, -1}
	for h := 0; h < 2; h++ {
		if math.Abs(caps[h][2]-wantZ[h]) > 1e-4 {
			t.Errorf("half %d (z-up): cap normal z=%.5f, want %.0f (flat on bed); cap=%v", h, caps[h][2], wantZ[h], caps[h])
		}
	}
}

// TestLayout_ZeroTiltUnchanged — with no tilt, CapAlign is the identity
// so the cut-face orientations behave exactly as the legacy layout: a
// z-down cap points straight down already.
func TestLayout_ZeroTiltUnchanged(t *testing.T) {
	caps := capNormalAfterLayout(t, 0, 0, OrientZDown, OrientXUp)
	if ang := math.Acos(clamp01(-caps[0][2])) * 180 / math.Pi; ang > 0.01 {
		t.Errorf("zero-tilt z-down: cap %.3f° off down, want 0 (cap=%v)", ang, caps[0])
	}
}

func clamp01(x float64) float64 {
	if x < -1 {
		return -1
	}
	if x > 1 {
		return 1
	}
	return x
}
