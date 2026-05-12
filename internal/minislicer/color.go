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
// from the model. Each section gets a SINGLE sample at its 3D
// midpoint (s.Mid, s.Z), but the texture lookup is box-filtered
// over the section's UV footprint — so the sample represents the
// AVERAGE texture color across the section, not one arbitrary
// texel inside it.
//
// The box-filter radius is computed per source triangle: each
// triangle has a constant texels-per-mm density (its UV-area /
// 3D-area ratio), so a section of size ~max(cellSize, layerH) mm
// covers density × size texels. Without this filter, adjacent
// sections within one source triangle land on different texels of
// a high-detail texture (earth.glb's coastlines, ocean
// bathymetry) and produce visible per-section noise.
//
// alpha[i] is true if the section's sample came back with
// alpha >= 128 (visible). Sections with alpha < 128 are
// considered transparent and are excluded from dithering by
// callers.
func SampleSectionColors(model *loader.LoadedModel, si *voxel.SpatialIndex, layers []Layer, sections []Section, cellSize, layerH float32) (colors [][3]uint8, alpha []bool) {
	_ = layers
	colors = make([][3]uint8, len(sections))
	alpha = make([]bool, len(sections))
	if len(sections) == 0 {
		return
	}
	radius := 3 * cellSize
	buf := voxel.NewSearchBuf(len(model.Faces))

	// Per-source-triangle pixel-radius cache for the box-filter.
	// Computed lazily on first use of each triangle.
	radiusCache := make(map[int32][2]int)
	getRadii := func(triIdx int32) (int, int) {
		if r, ok := radiusCache[triIdx]; ok {
			return r[0], r[1]
		}
		rU, rV := triFootprintRadii(model, triIdx, cellSize, layerH)
		radiusCache[triIdx] = [2]int{rU, rV}
		return rU, rV
	}

	for i, s := range sections {
		p := [3]float32{s.Mid[0], s.Mid[1], s.Z}
		var rgba [4]uint8
		if s.SrcTriIdx >= 0 {
			rU, rV := getRadii(s.SrcTriIdx)
			rgba = voxel.SampleByTriangleFootprint(p, model, s.SrcTriIdx, rU, rV)
		} else {
			rgba = voxel.SampleNearestColor(p, model, si, radius, buf, nil, nil)
		}
		colors[i] = [3]uint8{rgba[0], rgba[1], rgba[2]}
		alpha[i] = rgba[3] >= 128
	}
	return colors, alpha
}

// triFootprintRadii returns (radiusU, radiusV) in texel units for
// box-filtering the source texture at a section's UV footprint on
// triangle triIdx. The radius matches half the section's 3D size
// projected through the triangle's UV gradient and scaled by
// texture dimensions.
//
// Isotropic approximation: we use the triangle's overall UV
// density (sqrt of UV-area / 3D-area) instead of computing
// separate U/V gradients along the section's arc and Z directions.
// Good enough for textures whose UV mapping is roughly
// orientation-uniform (equirectangular earth, UV-unwrapped
// painted assets); anisotropic textures (e.g. heavy U-stretching)
// would benefit from a per-axis computation but produce visibly
// fine results with the isotropic form too.
func triFootprintRadii(model *loader.LoadedModel, triIdx int32, cellSize, layerH float32) (int, int) {
	if triIdx < 0 || int(triIdx) >= len(model.Faces) {
		return 0, 0
	}
	if model.UVs == nil || model.FaceTextureIdx == nil ||
		int(triIdx) >= len(model.FaceTextureIdx) {
		return 0, 0
	}
	texIdx := model.FaceTextureIdx[triIdx]
	if texIdx < 0 || int(texIdx) >= len(model.Textures) {
		return 0, 0
	}
	img := model.Textures[texIdx]
	tw := float32(img.Bounds().Dx())
	th := float32(img.Bounds().Dy())
	if tw < 1 || th < 1 {
		return 0, 0
	}

	f := model.Faces[triIdx]
	v0 := model.Vertices[f[0]]
	v1 := model.Vertices[f[1]]
	v2 := model.Vertices[f[2]]
	uv0 := model.UVs[f[0]]
	uv1 := model.UVs[f[1]]
	uv2 := model.UVs[f[2]]

	e1x, e1y, e1z := v1[0]-v0[0], v1[1]-v0[1], v1[2]-v0[2]
	e2x, e2y, e2z := v2[0]-v0[0], v2[1]-v0[1], v2[2]-v0[2]
	nx := e1y*e2z - e1z*e2y
	ny := e1z*e2x - e1x*e2z
	nz := e1x*e2y - e1y*e2x
	area3D := 0.5 * float32(math.Sqrt(float64(nx*nx+ny*ny+nz*nz)))
	if area3D <= 0 {
		return 0, 0
	}
	// |UV cross| / 2 in normalized UV units; convert to texel² area.
	du1, dv1 := uv1[0]-uv0[0], uv1[1]-uv0[1]
	du2, dv2 := uv2[0]-uv0[0], uv2[1]-uv0[1]
	uvCross := du1*dv2 - du2*dv1
	if uvCross < 0 {
		uvCross = -uvCross
	}
	areaTex := 0.5 * uvCross * tw * th
	if areaTex <= 0 {
		return 0, 0
	}
	// Texels per mm along the triangle plane (isotropic).
	densityPerMM := float32(math.Sqrt(float64(areaTex / area3D)))
	rU := int(0.5*cellSize*densityPerMM + 0.5)
	rV := int(0.5*layerH*densityPerMM + 0.5)
	// Clamp to at least 1 if we'd otherwise produce 0 (means
	// section covers < 1 texel; a 3x3 box still smooths bilinear
	// noise) and to a sane upper bound so we don't read a huge
	// box for a degenerate UV layout.
	if rU < 1 {
		rU = 1
	}
	if rV < 1 {
		rV = 1
	}
	if rU > 32 {
		rU = 32
	}
	if rV > 32 {
		rV = 32
	}
	return rU, rV
}
