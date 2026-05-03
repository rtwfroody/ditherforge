package pipeline

import (
	"fmt"
	"math"

	"github.com/rtwfroody/ditherforge/internal/materialx"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// materialxOverride adapts a materialx.Sampler to voxel.BaseColorOverride.
//
// For purely position-driven graphs (procedural marble, brick, etc.)
// the adapter forwards a single SampleAt call with the world-space mm
// position scaled by 1/tileMM. For graphs that consume UVs (image-
// backed PBR packs) it triplanar-projects: three SampleAt calls along
// the YZ, XZ, and XY planes, blended by |normal|^sharpness. This gives
// untextured meshes a continuous, seam-light texture without UV
// authoring.
//
// All fields are immutable after construction. Reentrant by virtue of
// the underlying Sampler being reentrant.
type materialxOverride struct {
	sampler   materialx.Sampler
	invTileMM float64
	useUV     bool
	sharpness float64
}

// SampleBaseColor implements voxel.BaseColorOverride.
func (m *materialxOverride) SampleBaseColor(ctx voxel.BaseColorContext) [3]uint8 {
	pos := [3]float64{
		float64(ctx.Pos[0]) * m.invTileMM,
		float64(ctx.Pos[1]) * m.invTileMM,
		float64(ctx.Pos[2]) * m.invTileMM,
	}
	if !m.useUV {
		// Procedural graphs ignore UV — one call is enough.
		return rgbToBytes(m.sampler.SampleAt(materialx.SampleContext{Pos: pos}))
	}
	return rgbToBytes(m.triplanar(pos, ctx.Normal))
}

// triplanar runs the underlying sampler three times against the three
// axis-aligned planes (YZ for X-facing surfaces, XZ for Y, XY for Z)
// and blends by the normal-derived weights. Sharpness controls how
// abruptly the projection switches across axis transitions: 1 is a
// soft cosine-weighted blend, higher values approach a hard box map.
//
// Each plane's U coordinate is multiplied by sign(normal.axis) so a
// face's UV traverses the same direction whether its normal points
// along +axis or -axis. Without this, a directional texture (text,
// arrows) would render mirrored across opposite-facing parallel
// faces. For direction-free textures (cobblestone, marble) the flip
// is invisible.
func (m *materialxOverride) triplanar(pos [3]float64, normal [3]float32) [3]float64 {
	signX := signOrPos(float64(normal[0]))
	signY := signOrPos(float64(normal[1]))
	signZ := signOrPos(float64(normal[2]))
	nx := math.Abs(float64(normal[0]))
	ny := math.Abs(float64(normal[1]))
	nz := math.Abs(float64(normal[2]))

	sharp := m.sharpness
	if sharp <= 0 {
		sharp = 4
	}
	wx := math.Pow(nx, sharp)
	wy := math.Pow(ny, sharp)
	wz := math.Pow(nz, sharp)
	sum := wx + wy + wz
	if sum < 1e-12 {
		// Degenerate normal — average the three planar samples
		// equally so a degenerate face renders with the same
		// "everywhere" pattern instead of popping to one of the three
		// projections.
		wx, wy, wz = 1.0/3, 1.0/3, 1.0/3
	} else {
		wx /= sum
		wy /= sum
		wz /= sum
	}

	var out [3]float64
	if wx > 1e-6 {
		c := m.sampler.SampleAt(materialx.SampleContext{
			Pos: pos,
			UV:  [2]float64{pos[1] * signX, pos[2]},
		})
		out[0] += c[0] * wx
		out[1] += c[1] * wx
		out[2] += c[2] * wx
	}
	if wy > 1e-6 {
		c := m.sampler.SampleAt(materialx.SampleContext{
			Pos: pos,
			UV:  [2]float64{pos[0] * signY, pos[2]},
		})
		out[0] += c[0] * wy
		out[1] += c[1] * wy
		out[2] += c[2] * wy
	}
	if wz > 1e-6 {
		c := m.sampler.SampleAt(materialx.SampleContext{
			Pos: pos,
			UV:  [2]float64{pos[0] * signZ, pos[1]},
		})
		out[0] += c[0] * wz
		out[1] += c[1] * wz
		out[2] += c[2] * wz
	}
	return out
}

// signOrPos returns -1 when v < 0, +1 otherwise (including 0).
// Triplanar UV flipping needs a deterministic sign for the zero-normal
// case, where any choice is fine because the corresponding weight is
// zero (or 1/3 in the degenerate-normal fallback, where the flip
// doesn't visually matter either).
func signOrPos(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

func rgbToBytes(rgb [3]float64) [3]uint8 {
	return [3]uint8{
		floatToByte(rgb[0]),
		floatToByte(rgb[1]),
		floatToByte(rgb[2]),
	}
}

// floatToByte quantizes a [0, 1] float to an 8-bit channel value.
//
// On the triplanar path, identical 8-bit sub-samples can come back at
// ±1 from the input byte: each sub-sample is rgb_float = byte/255,
// then weighted-sum across three planes accumulates a few ULPs of FP
// error before the round step here. Practically invisible against the
// dithering that runs downstream, but worth knowing if a future
// debugger asks "why isn't this pixel exactly equal to the texel".
//
// NaN propagates through arithmetic and fails both comparison
// branches below; uint8(NaN) is implementation-defined in Go. Pin it
// to 0 so a malformed graph evaluates to black instead of random
// per-voxel garbage.
func floatToByte(f float64) uint8 {
	if f != f {
		return 0
	}
	v := f*255 + 0.5
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v)
}

// baseColorOverride wraps the cached materialx.Sampler for the package
// at path with a per-run tile/triplanar config. tileMM scales
// world-space mm into the procedural's shading frame (values <= 0 are
// treated as 1 mm). triplanarSharpness only matters for image-backed
// graphs; <= 0 picks a sensible default. Returns (nil, nil) when
// path is empty so callers can pass the result straight through to
// the voxelizer. The expensive parts (XML parse + image decode) are
// memoized on StageCache, so applyBaseColor and the voxelize stage
// share one parse per pipeline run.
func (c *StageCache) baseColorOverride(path string, tileMM, triplanarSharpness float64) (voxel.BaseColorOverride, error) {
	if path == "" {
		return nil, nil
	}
	s, err := c.materialXSampler(path)
	if err != nil {
		return nil, fmt.Errorf("MaterialX %q: %w", path, err)
	}
	if s == nil {
		return nil, nil
	}
	if tileMM <= 0 {
		tileMM = 1
	}
	return &materialxOverride{
		sampler:   s,
		invTileMM: 1 / tileMM,
		useUV:     s.UsesUV(),
		sharpness: triplanarSharpness,
	}, nil
}

