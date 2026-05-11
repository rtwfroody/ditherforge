package minislicer

import (
	"math"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// PopulateSectionNormalZ fills Section.SrcTriNormalZ for every
// section whose SrcTriIdx points to a valid model face. The
// normal is computed once per source triangle and cached so we
// don't recompute when many sections share a triangle.
func PopulateSectionNormalZ(model *loader.LoadedModel, sections []Section) {
	if model == nil || len(model.Faces) == 0 {
		return
	}
	cache := make(map[int32]float32)
	for i := range sections {
		s := &sections[i]
		if s.SrcTriIdx < 0 || int(s.SrcTriIdx) >= len(model.Faces) {
			continue
		}
		if nz, ok := cache[s.SrcTriIdx]; ok {
			s.SrcTriNormalZ = nz
			continue
		}
		f := model.Faces[s.SrcTriIdx]
		a := model.Vertices[f[0]]
		b := model.Vertices[f[1]]
		c := model.Vertices[f[2]]
		// Triangle normal = (b-a) × (c-a). We only need Z, then
		// normalize against the full normal magnitude so the value
		// lives in [-1, 1].
		ex, ey, ez := b[0]-a[0], b[1]-a[1], b[2]-a[2]
		fx, fy, fz := c[0]-a[0], c[1]-a[1], c[2]-a[2]
		nx := ey*fz - ez*fy
		ny := ez*fx - ex*fz
		nz := ex*fy - ey*fx
		mag := float32(math.Sqrt(float64(nx*nx + ny*ny + nz*nz)))
		var nzn float32
		if mag > 0 {
			nzn = nz / mag
		}
		cache[s.SrcTriIdx] = nzn
		s.SrcTriNormalZ = nzn
	}
}

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
		var rgba [4]uint8
		// Prefer source-triangle sampling: each ribbon section
		// records the model triangle whose intersection with the
		// slicing plane produced its midpoint. Sampling directly
		// on that triangle's surface avoids the nearest-tri
		// lookup picking up unrelated triangles from a nearby
		// object — the cause of color leakage between a fish and
		// the cutting board it rests on, or between adjacent
		// pieces of a sliced model.
		if s.SrcTriIdx >= 0 {
			rgba = voxel.SampleByTriangle(p, model, s.SrcTriIdx)
		} else {
			rgba = voxel.SampleNearestColor(p, model, si, radius, buf, nil, nil)
		}
		colors[i] = [3]uint8{rgba[0], rgba[1], rgba[2]}
		alpha[i] = rgba[3] >= 128
	}
	return colors, alpha
}
