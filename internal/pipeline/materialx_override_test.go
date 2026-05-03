package pipeline

import (
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/materialx"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// fakeSampler is a UV-aware materialx.Sampler stub that delegates each
// SampleAt call to a caller-supplied function so triplanar tests can
// observe which UV plane the adapter chose.
type fakeSampler struct {
	cb     func(ctx materialx.SampleContext) [3]float64
	usesUV bool
}

func (f *fakeSampler) Sample(p [3]float64) [3]float64 { return f.cb(materialx.SampleContext{Pos: p}) }
func (f *fakeSampler) SampleAt(ctx materialx.SampleContext) [3]float64 { return f.cb(ctx) }
func (f *fakeSampler) UsesUV() bool                                    { return f.usesUV }

// TestTriplanarPicksDominantPlane verifies that with a sharply-axial
// face normal, the triplanar adapter returns nearly the color
// produced by the corresponding plane: +Z normal → XY plane, +X → YZ,
// +Y → XZ. The fake sampler returns a distinct color per plane based
// on which UV component pair the adapter handed it.
//
// Positions are picked to be exactly representable in float32 so the
// fake can identify the plane by exact-equality check on UV.
func TestTriplanarPicksDominantPlane(t *testing.T) {
	pos32 := [3]float32{0.25, 0.5, 0.75}
	px, py, pz := float64(pos32[0]), float64(pos32[1]), float64(pos32[2])
	colors := [3][3]float64{
		{1, 0, 0}, // YZ plane → red
		{0, 1, 0}, // XZ plane → green
		{0, 0, 1}, // XY plane → blue
	}
	fake := &fakeSampler{
		usesUV: true,
		cb: func(ctx materialx.SampleContext) [3]float64 {
			switch {
			case ctx.UV[0] == py && ctx.UV[1] == pz:
				return colors[0] // YZ
			case ctx.UV[0] == px && ctx.UV[1] == pz:
				return colors[1] // XZ
			case ctx.UV[0] == px && ctx.UV[1] == py:
				return colors[2] // XY
			}
			t.Errorf("unexpected UV %v", ctx.UV)
			return [3]float64{}
		},
	}
	o := &materialxOverride{sampler: fake, invTileMM: 1, useUV: true, sharpness: 8}

	cases := []struct {
		normal [3]float32
		want   [3]float64
	}{
		{[3]float32{0, 0, 1}, colors[2]}, // XY dominates
		{[3]float32{1, 0, 0}, colors[0]}, // YZ dominates
		{[3]float32{0, 1, 0}, colors[1]}, // XZ dominates
	}
	for _, tc := range cases {
		got := o.SampleBaseColor(voxel.BaseColorContext{
			Pos:    pos32,
			Normal: tc.normal,
		})
		// Sharpness=8 with one component=1 and others=0 produces ~100%
		// weight for the dominant plane; the result should be the pure
		// dominant color.
		want := [3]uint8{
			uint8(math.Round(tc.want[0] * 255)),
			uint8(math.Round(tc.want[1] * 255)),
			uint8(math.Round(tc.want[2] * 255)),
		}
		if got != want {
			t.Errorf("normal=%v: got %v, want %v", tc.normal, got, want)
		}
	}
}

// TestTriplanarBlendsWhenNormalIsDiagonal — a 45° normal in the XY
// plane should blend YZ and XZ samples roughly equally; XY is
// suppressed because |z|=0.
func TestTriplanarBlendsWhenNormalIsDiagonal(t *testing.T) {
	pos32 := [3]float32{0.25, 0.5, 0.75}
	px, py, pz := float64(pos32[0]), float64(pos32[1]), float64(pos32[2])
	colors := map[[2]float64][3]float64{
		{py, pz}: {1, 0, 0}, // YZ → red
		{px, pz}: {0, 1, 0}, // XZ → green
		{px, py}: {0, 0, 1}, // XY → blue (should be 0-weighted with z=0)
	}
	fake := &fakeSampler{
		usesUV: true,
		cb: func(ctx materialx.SampleContext) [3]float64 {
			c, ok := colors[ctx.UV]
			if !ok {
				t.Errorf("unexpected UV %v", ctx.UV)
			}
			return c
		},
	}
	o := &materialxOverride{sampler: fake, invTileMM: 1, useUV: true, sharpness: 4}

	got := o.SampleBaseColor(voxel.BaseColorContext{
		Pos:    pos32,
		Normal: [3]float32{0.7071, 0.7071, 0}, // XY-plane diagonal
	})
	// Expect roughly (0.5, 0.5, 0) ± rounding. Blue (XY plane) should
	// not contribute since |normal.z|=0.
	if got[0] < 100 || got[0] > 155 || got[1] < 100 || got[1] > 155 || got[2] > 20 {
		t.Errorf("expected ~50/50 red+green blend, got %v", got)
	}
}

// TestTriplanarUVSignFlip verifies the per-plane sign flip: a face
// with +X normal samples YZ at u=+pos.y; a -X normal samples at
// u=-pos.y. Without the flip, mirror seams appear on opposite-facing
// parallel faces with directional textures.
func TestTriplanarUVSignFlip(t *testing.T) {
	pos32 := [3]float32{0.25, 0.5, 0.75}
	py, pz := float64(pos32[1]), float64(pos32[2])
	var seenUVs []float64
	fake := &fakeSampler{
		usesUV: true,
		cb: func(ctx materialx.SampleContext) [3]float64 {
			seenUVs = append(seenUVs, ctx.UV[0])
			return [3]float64{1, 1, 1}
		},
	}
	o := &materialxOverride{sampler: fake, invTileMM: 1, useUV: true, sharpness: 8}

	seenUVs = nil
	o.SampleBaseColor(voxel.BaseColorContext{Pos: pos32, Normal: [3]float32{1, 0, 0}})
	if len(seenUVs) == 0 || seenUVs[0] != py {
		t.Errorf("+X normal should sample YZ with u=+pos.y=%v; saw %v", py, seenUVs)
	}

	seenUVs = nil
	o.SampleBaseColor(voxel.BaseColorContext{Pos: pos32, Normal: [3]float32{-1, 0, 0}})
	if len(seenUVs) == 0 || seenUVs[0] != -py {
		t.Errorf("-X normal should sample YZ with u=-pos.y=%v; saw %v", -py, seenUVs)
	}
	_ = pz
}

// TestTriplanarDegenerateNormalAveragesAllPlanes verifies that a
// zero-length normal produces an equal three-way blend rather than
// silently picking one plane. A face with random distinct per-plane
// colors should resolve to their average.
func TestTriplanarDegenerateNormalAveragesAllPlanes(t *testing.T) {
	pos32 := [3]float32{0.25, 0.5, 0.75}
	px, py, pz := float64(pos32[0]), float64(pos32[1]), float64(pos32[2])
	colors := map[[2]float64][3]float64{
		{py, pz}: {0.6, 0.0, 0.0},
		{px, pz}: {0.0, 0.6, 0.0},
		{px, py}: {0.0, 0.0, 0.6},
	}
	fake := &fakeSampler{
		usesUV: true,
		cb: func(ctx materialx.SampleContext) [3]float64 {
			return colors[ctx.UV]
		},
	}
	o := &materialxOverride{sampler: fake, invTileMM: 1, useUV: true, sharpness: 4}
	got := o.SampleBaseColor(voxel.BaseColorContext{
		Pos:    pos32,
		Normal: [3]float32{0, 0, 0},
	})
	// Each channel: 0.6 / 3 = 0.2 → 0.2*255+0.5 = 51.5 → 51.
	want := [3]uint8{51, 51, 51}
	for i := range 3 {
		if got[i] < 50 || got[i] > 52 {
			t.Errorf("degenerate normal: channel %d got %d, want ~51 (3-way average)", i, got[i])
			break
		}
	}
	_ = want
}

// TestNonUVSamplerSkipsTriplanar verifies the fast path: a
// non-UV-using sampler is consulted exactly once per call regardless
// of normal, since no projection is needed.
func TestNonUVSamplerSkipsTriplanar(t *testing.T) {
	calls := 0
	fake := &fakeSampler{
		usesUV: false,
		cb: func(ctx materialx.SampleContext) [3]float64 {
			calls++
			return [3]float64{0.5, 0.5, 0.5}
		},
	}
	o := &materialxOverride{sampler: fake, invTileMM: 1, useUV: false, sharpness: 4}
	o.SampleBaseColor(voxel.BaseColorContext{
		Pos:    [3]float32{0, 0, 0},
		Normal: [3]float32{0.577, 0.577, 0.577}, // diagonal would trigger 3-way blend
	})
	if calls != 1 {
		t.Errorf("non-UV sampler should be called once per SampleBaseColor; got %d", calls)
	}
}
