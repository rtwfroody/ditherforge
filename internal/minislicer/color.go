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
// from the model.
//
// For ribbon sections with a known source triangle, the color is
// the AVERAGE of several samples taken across the section's
// footprint (arc × layerH) on that triangle's surface — three
// arc positions (start, mid, end) times three Z positions (zBot,
// mid, zTop) = nine samples. Single-point sampling at the midpoint
// produced visible per-section noise on high-frequency textures
// like earth.glb, where each section covers many texels.
//
// For ribbon sections without a source triangle (rare; lost
// provenance after a slicer merge), we fall back to
// SampleNearestColor at the midpoint. Cap-tile sections (Kind !=
// KindRibbon) also single-sample at the midpoint, since their
// footprint is the cap tile rather than an arc range.
//
// alpha[i] is true if the section's sample came back with
// alpha >= 128 (visible). Sections with alpha < 128 are considered
// transparent and are excluded from dithering by callers.
func SampleSectionColors(model *loader.LoadedModel, si *voxel.SpatialIndex, layers []Layer, sections []Section, cellSize, layerH float32) (colors [][3]uint8, alpha []bool) {
	colors = make([][3]uint8, len(sections))
	alpha = make([]bool, len(sections))
	if len(sections) == 0 {
		return
	}
	radius := 3 * cellSize
	buf := voxel.NewSearchBuf(len(model.Faces))

	// Cache cumulative arc lengths per (layer, loop) so multiple
	// sections sharing a loop don't recompute it.
	type loopKey struct{ layer, loop int }
	cumLens := make(map[loopKey][]float32)
	getLoop := func(layerIdx, loopIdx int) (*Loop, []float32) {
		if layerIdx < 0 || layerIdx >= len(layers) {
			return nil, nil
		}
		if loopIdx < 0 || loopIdx >= len(layers[layerIdx].Loops) {
			return nil, nil
		}
		lp := &layers[layerIdx].Loops[loopIdx]
		k := loopKey{layerIdx, loopIdx}
		cum, ok := cumLens[k]
		if !ok {
			cum = loopCumLen(lp.Points)
			cumLens[k] = cum
		}
		return lp, cum
	}

	for i, s := range sections {
		var rgba [4]uint8

		if s.Kind == KindRibbon && s.SrcTriIdx >= 0 {
			pts := ribbonFootprintPoints(layers, getLoop, &s, layerH)
			rgba = voxel.SampleByTrianglePoints(model, s.SrcTriIdx, pts)
		} else if s.SrcTriIdx >= 0 {
			rgba = voxel.SampleByTriangle([3]float32{s.Mid[0], s.Mid[1], s.Z}, model, s.SrcTriIdx)
		} else {
			rgba = voxel.SampleNearestColor([3]float32{s.Mid[0], s.Mid[1], s.Z}, model, si, radius, buf, nil, nil)
		}
		colors[i] = [3]uint8{rgba[0], rgba[1], rgba[2]}
		alpha[i] = rgba[3] >= 128
	}
	return colors, alpha
}

// ribbonFootprintPoints builds a small set of 3D sample positions
// inside a ribbon section's footprint (arc range × layer height)
// for color averaging. Returns at most 9 points: three arc
// positions (startArc, midArc, endArc) × three Z positions (zBot,
// mid, zTop). Falls back to a single midpoint when the loop /
// cumulative-arc lookup fails.
func ribbonFootprintPoints(layers []Layer, getLoop func(int, int) (*Loop, []float32), s *Section, layerH float32) [][3]float32 {
	if layers == nil || getLoop == nil {
		return [][3]float32{{s.Mid[0], s.Mid[1], s.Z}}
	}
	lp, cum := getLoop(s.LayerIdx, s.LoopIdx)
	if lp == nil || cum == nil || len(lp.Points) < 2 {
		return [][3]float32{{s.Mid[0], s.Mid[1], s.Z}}
	}
	midArc := 0.5 * (s.StartArc + s.EndArc)
	// 25/50/75 % positions along the section arc; 0/100 % sit on
	// section boundaries shared with neighbors and would pick up
	// edge-of-triangle samples that overshoot the source.
	a0 := s.StartArc + 0.25*s.Length
	a1 := midArc
	a2 := s.StartArc + 0.75*s.Length
	p0 := pointAtArc(lp.Points, cum, a0)
	p1 := pointAtArc(lp.Points, cum, a1)
	p2 := pointAtArc(lp.Points, cum, a2)
	half := 0.4 * layerH
	zs := []float32{s.Z - half, s.Z, s.Z + half}
	out := make([][3]float32, 0, 9)
	for _, z := range zs {
		out = append(out,
			[3]float32{p0[0], p0[1], z},
			[3]float32{p1[0], p1[1], z},
			[3]float32{p2[0], p2[1], z})
	}
	return out
}
