package voxel

import (
	"image"
	"image/color"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// alphaModeModel builds a one-triangle textured model whose single 2×2
// texture is a uniform color+alpha, with a deliberately non-white base
// color so the spec's "ignore texture alpha" vs "composite over base"
// behaviors produce visibly different RGB.
func alphaModeModel(texR, texG, texB, texA uint8, mode uint8, cutoff float32) *loader.LoadedModel {
	tex := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			tex.Set(x, y, color.NRGBA{R: texR, G: texG, B: texB, A: texA})
		}
	}
	return &loader.LoadedModel{
		Vertices:        [][3]float32{{0, 0, 0}, {2, 0, 0}, {0, 2, 0}},
		Faces:           [][3]uint32{{0, 1, 2}},
		UVs:             [][2]float32{{0, 0}, {1, 0}, {0, 1}},
		Textures:        []image.Image{tex},
		FaceTextureIdx:  []int32{0},
		FaceBaseColor:   [][4]uint8{{200, 0, 0, 255}}, // red base, distinct from texture
		FaceAlphaMode:   []uint8{mode},
		FaceAlphaCutoff: []float32{cutoff},
	}
}

// TestAlphaModeSampling pins the glTF alphaMode rules at the sampling layer:
// OPAQUE ignores the texture alpha (full texture RGB, opaque), MASK steps at
// the cutoff, BLEND composites over the base color by the texel alpha.
func TestAlphaModeSampling(t *testing.T) {
	p := [3]float32{0.5, 0.5, 0} // inside the triangle
	const green0, green1, green2 = 0, 255, 0

	cases := []struct {
		name       string
		texA       uint8
		mode       uint8
		cutoff     float32
		wantRGB    [3]uint8
		wantOpaque bool // alpha >= 128
	}{
		// OPAQUE: texture alpha ignored entirely → full texture green, opaque,
		// even where the texel is fully transparent.
		{"opaque/transparent-texel", 0, loader.AlphaModeOpaque, 0.5, [3]uint8{green0, green1, green2}, true},
		{"opaque/opaque-texel", 255, loader.AlphaModeOpaque, 0.5, [3]uint8{green0, green1, green2}, true},
		// MASK: alpha-test at the cutoff. Below → discarded (alpha 0); at/above
		// → opaque. RGB is the full texture color either way.
		{"mask/below-cutoff", 64, loader.AlphaModeMask, 0.5, [3]uint8{green0, green1, green2}, false},
		{"mask/above-cutoff", 200, loader.AlphaModeMask, 0.5, [3]uint8{green0, green1, green2}, true},
		// BLEND: composite the texture over the base color by the texel alpha.
		// Fully transparent → base red, alpha 0; fully opaque → texture green.
		{"blend/transparent-texel", 0, loader.AlphaModeBlend, 0.5, [3]uint8{200, 0, 0}, false},
		{"blend/opaque-texel", 255, loader.AlphaModeBlend, 0.5, [3]uint8{green0, green1, green2}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := alphaModeModel(0, 255, 0, c.texA, c.mode, c.cutoff)
			got := SampleByTriangle(p, m, 0)
			if [3]uint8{got[0], got[1], got[2]} != c.wantRGB {
				t.Errorf("RGB = %v, want %v", got[:3], c.wantRGB)
			}
			if (got[3] >= 128) != c.wantOpaque {
				t.Errorf("alpha = %d, want opaque=%v", got[3], c.wantOpaque)
			}
		})
	}
}
