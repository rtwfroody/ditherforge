package voxel

import (
	"context"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// insideSignOverride returns white for sample points strictly on the inside
// of the boundary plane axis==0 and black exactly on or outside it. It lets a
// test detect whether the along-normal sampler reads color just BENEATH the
// surface (white) or exactly on the surface plane (black) — a position-driven
// material whose color-boundary coincides with the wall is only well-defined
// just inside.
type insideSignOverride struct{ axis int }

func (o insideSignOverride) SampleBaseColor(ctx BaseColorContext) [3]uint8 {
	if ctx.Pos[o.axis] > 0 {
		return [3]uint8{255, 255, 255}
	}
	return [3]uint8{0, 0, 0}
}

// TestSampleAlongNormalSamplesInsideSurface pins the surfaceColorInsetMM fix.
// When a material color-boundary plane coincides with an axis-aligned wall
// (here the box walls sit exactly on the x=0 / y=0 planes of the override),
// the hit lands on the discontinuity and sampling color AT it is ambiguous —
// FP rounding scatters a flat wall into a gray average. The fix nudges the
// sample a fixed distance inward so it reads the well-defined inside material.
// Without the inset the hit is exactly on the plane and the override returns
// black; with it every wall sample is strictly inside and returns white.
func TestSampleAlongNormalSamplesInsideSurface(t *testing.T) {
	model := &loader.LoadedModel{}
	appendBox(model, [3]float32{0, 0, 0}, [3]float32{10, 10, 10})

	bvh, err := BuildRayBVH(context.Background(), model)
	if err != nil {
		t.Fatalf("BuildRayBVH: %v", err)
	}
	si := NewSpatialIndex(model, 2)
	buf := NewSearchBuf(len(model.Faces))

	cases := []struct {
		name   string
		p      [3]float32 // interior sample point near the wall
		normal [3]float32 // outward surface normal
		axis   int        // boundary-plane axis the wall coincides with
	}{
		{"y=0 wall", [3]float32{5, 0.5, 5}, [3]float32{0, -1, 0}, 1},
		{"x=0 wall", [3]float32{0.5, 5, 5}, [3]float32{-1, 0, 0}, 0},
	}
	for _, c := range cases {
		// Span the wall tangentially: every point must agree and read the
		// inside (white) color — the bug scattered these to black/gray.
		for _, off := range []float32{-2, -1, 0, 1, 2} {
			p := c.p
			p[2] = 5 + off
			rgba := SampleAlongNormal(p, c.normal, 1, 1, model, bvh, si, 2, buf,
				nil, nil, nil, nil, insideSignOverride{axis: c.axis})
			if rgba[0] != 255 {
				t.Errorf("%s off=%g: sampled on/outside surface (rgb=%d,%d,%d); want inside material (255)",
					c.name, off, rgba[0], rgba[1], rgba[2])
			}
		}
	}
}
