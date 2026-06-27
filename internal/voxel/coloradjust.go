package voxel

// ColorAdjustment holds brightness/contrast/saturation parameters.
// All values are in the range -100 to +100, with 0 meaning no change.
type ColorAdjustment struct {
	Brightness float32
	Contrast   float32
	Saturation float32
}

// IsIdentity returns true if no adjustments are needed.
func (ca ColorAdjustment) IsIdentity() bool {
	return ca.Brightness == 0 && ca.Contrast == 0 && ca.Saturation == 0
}

// AdjustColor applies brightness, contrast, and saturation adjustments
// to a single RGB color. The math here must match the GLSL shader exactly.
func AdjustColor(r, g, b uint8, adj ColorAdjustment) (uint8, uint8, uint8) {
	// Map slider values to internal parameters.
	brightness := adj.Brightness / 100.0           // -1.0 to +1.0
	contrast := (100.0 + adj.Contrast) / 100.0     // 0.0 to 2.0
	saturation := (100.0 + adj.Saturation) / 100.0 // 0.0 to 2.0

	rf := float32(r) / 255.0
	gf := float32(g) / 255.0
	bf := float32(b) / 255.0

	// Brightness: add offset.
	rf += brightness
	gf += brightness
	bf += brightness

	// Contrast: scale around mid-gray.
	rf = (rf-0.5)*contrast + 0.5
	gf = (gf-0.5)*contrast + 0.5
	bf = (bf-0.5)*contrast + 0.5

	// Saturation: lerp between luminance and color.
	lum := 0.2126*rf + 0.7152*gf + 0.0722*bf
	rf = lum + saturation*(rf-lum)
	gf = lum + saturation*(gf-lum)
	bf = lum + saturation*(bf-lum)

	return clamp8(rf), clamp8(gf), clamp8(bf)
}

func clamp8(v float32) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v*255 + 0.5)
}

// AdjustColor is applied per-color through ColorTransform (see
// colortransform.go); the former AdjustCellColors batch helper was removed
// when color correction moved into the sampler.
