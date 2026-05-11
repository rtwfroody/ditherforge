package minislicer

import (
	"math"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// SampleSectionColors returns one averaged [3]uint8 RGB per section.
//
// Each section is sampled at multiple points covering its footprint,
// then averaged (alpha-weighted). This is the natural antialiasing
// for the dither: with one sample per section, a sub-cellSize
// feature (a thin texture stripe, a fish iris) is captured fully
// or missed entirely depending on where the midpoint falls,
// producing sharp horizontal bands and lost detail. Multi-sampling
// averages the section's actual surface region; the dither then
// works against the *mean* color of each section and the high-
// frequency variation feeds back through error diffusion.
//
//   - Ribbon section: ribbonSamples points evenly spaced along the
//     arc [StartArc, EndArc] of the section's parent loop.
//   - Cap tile: capSamples points (center + 4 inner-corner offsets)
//     within the tile rectangle.
//
// `layers` is the parent layer list, indexed by Section.LayerIdx
// and Section.LoopIdx so the ribbon arc parametrization is
// recoverable.
//
// alpha[i] is true if any sample for that section returned alpha
// >= 128. Sections with alpha < 128 (all samples transparent) are
// excluded from dithering by callers.
func SampleSectionColors(model *loader.LoadedModel, si *voxel.SpatialIndex, sections []Section, layers []Layer, cellSize float32) (colors [][3]uint8, alpha []bool) {
	colors = make([][3]uint8, len(sections))
	alpha = make([]bool, len(sections))
	if len(sections) == 0 {
		return
	}
	radius := 3 * cellSize
	buf := voxel.NewSearchBuf(len(model.Faces))

	const ribbonSamples = 4
	// Cap samples: center + four inset corners. Inset by 1/4 of the
	// tile size so corner samples cover the tile interior without
	// straddling neighboring tiles.
	const capInset = 0.25

	// Bias the sample point outward from the polygon boundary
	// before doing the nearest-tri lookup. Without this, the DP
	// simplification's allowed drift (cellSize/4 ≈ 0.1mm) can put a
	// section midpoint just inside the contour — and on a thin-shell
	// model (e.g. cut_fish, blue skin against a salmon cut surface
	// less than a mm apart) the nearest-tri lookup picks the wrong
	// side of the shell, flipping skin and cut colors. Biasing by
	// half a cell guarantees the sample sits in air on the
	// outward-facing side of the polygon.
	biasDist := cellSize * 0.5

	// Cache loop cumulative arc length so it's only computed once
	// per (layer, loop), not per section.
	type loopKey struct{ layer, loop int }
	cumLenCache := make(map[loopKey][]float32)
	loopFor := func(layerIdx, loopIdx int) (*Loop, []float32) {
		if layerIdx < 0 || layerIdx >= len(layers) {
			return nil, nil
		}
		layer := &layers[layerIdx]
		if loopIdx < 0 || loopIdx >= len(layer.Loops) {
			return nil, nil
		}
		loop := &layer.Loops[loopIdx]
		k := loopKey{layerIdx, loopIdx}
		cum, ok := cumLenCache[k]
		if !ok {
			cum = loopCumLen(loop.Points)
			cumLenCache[k] = cum
		}
		return loop, cum
	}

	for i, s := range sections {
		var rSum, gSum, bSum, aSum float32
		var nSum float32
		switch s.Kind {
		case KindRibbon:
			loop, cum := loopFor(s.LayerIdx, s.LoopIdx)
			if loop == nil {
				// Fall back to midpoint-only sample.
				p := [3]float32{s.Mid[0], s.Mid[1], s.Z}
				rgba := voxel.SampleNearestColor(p, model, si, radius, buf, nil, nil)
				colors[i] = [3]uint8{rgba[0], rgba[1], rgba[2]}
				alpha[i] = rgba[3] >= 128
				continue
			}
			// Outward-bias direction at the section midpoint.
			// Perpendicular to the local tangent, rotated 90° in the
			// direction that points away from the polygon's interior
			// (right of tangent for CCW, left for CW). For holes
			// (IsHole=true) we flip the sign so the bias goes into
			// the cavity instead of into the surrounding fish
			// material — sampling the cavity-facing wall surface.
			midArc := s.StartArc + 0.5*s.Length
			ds := s.Length * 0.1
			pA := pointAtArc(loop.Points, cum, midArc-ds)
			pB := pointAtArc(loop.Points, cum, midArc+ds)
			tx := pB[0] - pA[0]
			ty := pB[1] - pA[1]
			tn := float32(math.Sqrt(float64(tx*tx + ty*ty)))
			var outX, outY float32
			if tn > 1e-6 {
				tx /= tn
				ty /= tn
				sign := float32(1)
				if loop.SignedArea < 0 {
					sign = -1
				}
				if loop.IsHole {
					sign = -sign
				}
				outX = sign * ty
				outY = sign * -tx
			}
			for k := 0; k < ribbonSamples; k++ {
				t := (float32(k) + 0.5) / float32(ribbonSamples)
				arc := s.StartArc + t*s.Length
				xy := pointAtArc(loop.Points, cum, arc)
				p := [3]float32{
					xy[0] + outX*biasDist,
					xy[1] + outY*biasDist,
					s.Z,
				}
				rgba := voxel.SampleNearestColor(p, model, si, radius, buf, nil, nil)
				rSum += float32(rgba[0])
				gSum += float32(rgba[1])
				bSum += float32(rgba[2])
				aSum += float32(rgba[3])
				nSum++
			}
		case KindCapTop, KindCapBottom:
			x0, y0, x1, y1 := s.CapBoundsXY[0], s.CapBoundsXY[1], s.CapBoundsXY[2], s.CapBoundsXY[3]
			cx, cy := (x0+x1)*0.5, (y0+y1)*0.5
			dx := (x1 - x0) * 0.5 * capInset
			dy := (y1 - y0) * 0.5 * capInset
			samplePts := [5][2]float32{
				{cx, cy},
				{cx - dx, cy - dy},
				{cx + dx, cy - dy},
				{cx + dx, cy + dy},
				{cx - dx, cy + dy},
			}
			// Cap is on a horizontal surface — bias along ±Z based
			// on whether the cap faces up or down.
			zBias := biasDist
			if s.Kind == KindCapBottom {
				zBias = -biasDist
			}
			for _, sp := range samplePts {
				p := [3]float32{sp[0], sp[1], s.Z + zBias}
				rgba := voxel.SampleNearestColor(p, model, si, radius, buf, nil, nil)
				rSum += float32(rgba[0])
				gSum += float32(rgba[1])
				bSum += float32(rgba[2])
				aSum += float32(rgba[3])
				nSum++
			}
		}
		if nSum > 0 {
			colors[i] = [3]uint8{
				uint8(rSum / nSum),
				uint8(gSum / nSum),
				uint8(bSum / nSum),
			}
			alpha[i] = (aSum / nSum) >= 128
		}
	}
	return colors, alpha
}
