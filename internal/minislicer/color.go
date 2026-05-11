package minislicer

import (
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// SampleSectionColors returns one [3]uint8 RGB per section, sampled
// from the model at each section's 3D midpoint via
// voxel.SampleNearestColor.
//
// alpha[i] is true if the section's nearest-triangle sample came
// back with alpha >= 128 (visible). Sections with alpha < 128 are
// considered transparent and are excluded from dithering by callers.
func SampleSectionColors(model *loader.LoadedModel, si *voxel.SpatialIndex, sections []Section, cellSize float32) (colors [][3]uint8, alpha []bool) {
	colors = make([][3]uint8, len(sections))
	alpha = make([]bool, len(sections))
	if len(sections) == 0 {
		return
	}
	// Search radius is generous; the section midpoint sits exactly
	// on the surface so the nearest-tri is at zero distance, but a
	// few cellSize allows the spatial index to find candidates
	// across cell boundaries.
	radius := 3 * cellSize
	buf := voxel.NewSearchBuf(len(model.Faces))
	for i, s := range sections {
		p := [3]float32{s.Mid[0], s.Mid[1], s.Z}
		rgba := voxel.SampleNearestColor(p, model, si, radius, buf, nil, nil)
		colors[i] = [3]uint8{rgba[0], rgba[1], rgba[2]}
		alpha[i] = rgba[3] >= 128
	}
	return colors, alpha
}
