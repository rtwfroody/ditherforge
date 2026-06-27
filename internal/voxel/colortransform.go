package voxel

import "fmt"

// ColorTransform is the per-color correction applied to every color the
// pipeline samples from the source model. It combines, in order:
//
//  1. brightness / contrast / saturation adjustment (AdjustColor), then
//  2. Gaussian-RBF warp pins (the color "pins" that map a sampled source
//     color toward a chosen filament color).
//
// It exists so the correction can be applied AT SAMPLING TIME — inside the
// Voxelize stage, before color-aware cell segmentation and before the
// per-cell color is recorded — rather than as a separate post-voxelize
// stage. That way every consumer (region segmentation, dither, and the
// "Show sampled colors" debug view) sees the corrected colors; there is no
// point in the pipeline that operates on the raw, uncorrected colors.
//
// The math here is identical to the old StageColorAdjust + StageColorWarp
// pair, so a build with the correction folded into sampling produces the
// same final colors the separate stages did (and the same GLSL preview
// mirror in frontend/src/lib/components/ModelViewer.svelte still matches).
type ColorTransform struct {
	adj ColorAdjustment
	rbf *rbfSystem // nil when there are no warp pins
}

// NewColorTransform builds a ColorTransform from a brightness/contrast/
// saturation adjustment and a set of warp pins (either may be empty).
// Returns an error only if the warp-pin RBF system is singular.
func NewColorTransform(adj ColorAdjustment, pins []ColorWarpPin) (*ColorTransform, error) {
	ct := &ColorTransform{adj: adj}
	if len(pins) > 0 {
		sys, err := newRBFSystem(pins)
		if err != nil {
			return nil, fmt.Errorf("color transform: %w", err)
		}
		ct.rbf = sys
	}
	return ct, nil
}

// IsIdentity reports whether the transform leaves every color unchanged
// (no adjustment and no warp pins). A nil receiver is identity. Callers use
// this to skip threading the transform through the sampler entirely.
func (ct *ColorTransform) IsIdentity() bool {
	return ct == nil || (ct.adj.IsIdentity() && ct.rbf == nil)
}

// Apply maps a single raw sampled RGB to its corrected RGB. Safe on a nil
// receiver (returns rgb unchanged).
func (ct *ColorTransform) Apply(rgb [3]uint8) [3]uint8 {
	if ct == nil {
		return rgb
	}
	out := rgb
	if !ct.adj.IsIdentity() {
		r, g, b := AdjustColor(out[0], out[1], out[2], ct.adj)
		out = [3]uint8{r, g, b}
	}
	if ct.rbf != nil {
		L, a, b := rgbToLab(out)
		wL, wa, wb := ct.rbf.eval(L, a, b)
		out = labToRGB(wL, wa, wb)
	}
	return out
}
