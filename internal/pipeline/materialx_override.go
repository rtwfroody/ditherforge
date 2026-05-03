package pipeline

import (
	"fmt"
	"log"
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
func (m *materialxOverride) triplanar(pos [3]float64, normal [3]float32) [3]float64 {
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
		// Degenerate normal — fall back to one planar sample so we at
		// least produce a defined color rather than black.
		return m.sampler.SampleAt(materialx.SampleContext{
			Pos: pos,
			UV:  [2]float64{pos[0], pos[1]},
		})
	}
	wx /= sum
	wy /= sum
	wz /= sum

	var out [3]float64
	if wx > 1e-6 {
		c := m.sampler.SampleAt(materialx.SampleContext{
			Pos: pos,
			UV:  [2]float64{pos[1], pos[2]},
		})
		out[0] += c[0] * wx
		out[1] += c[1] * wx
		out[2] += c[2] * wx
	}
	if wy > 1e-6 {
		c := m.sampler.SampleAt(materialx.SampleContext{
			Pos: pos,
			UV:  [2]float64{pos[0], pos[2]},
		})
		out[0] += c[0] * wy
		out[1] += c[1] * wy
		out[2] += c[2] * wy
	}
	if wz > 1e-6 {
		c := m.sampler.SampleAt(materialx.SampleContext{
			Pos: pos,
			UV:  [2]float64{pos[0], pos[1]},
		})
		out[0] += c[0] * wz
		out[1] += c[1] * wz
		out[2] += c[2] * wz
	}
	return out
}

func rgbToBytes(rgb [3]float64) [3]uint8 {
	return [3]uint8{
		floatToByte(rgb[0]),
		floatToByte(rgb[1]),
		floatToByte(rgb[2]),
	}
}

func floatToByte(f float64) uint8 {
	// NaN propagates through arithmetic and fails both comparison
	// branches below; uint8(NaN) is implementation-defined in Go. Pin
	// it to 0 so a malformed graph evaluates to black instead of
	// random per-voxel garbage.
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

// buildBaseColorOverride parses a MaterialX package (path to .mtlx or
// .zip) and returns an override that samples its first material's
// base_color graph. tileMM scales world-space mm into the procedural's
// shading frame (values <= 0 are treated as 1 mm). triplanarSharpness
// only matters for image-backed graphs; <= 0 picks a sensible default.
// Returns (nil, nil) when path is empty so callers can pass the result
// straight through to the voxelizer.
func buildBaseColorOverride(path string, tileMM, triplanarSharpness float64) (voxel.BaseColorOverride, error) {
	if path == "" {
		return nil, nil
	}
	doc, err := materialx.ParsePackage(path)
	if err != nil {
		return nil, fmt.Errorf("MaterialX package %q: %w", path, err)
	}
	s, err := doc.DefaultBaseColorSampler()
	if err != nil {
		return nil, fmt.Errorf("MaterialX base color: %w", err)
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

// safeBaseColorOverride wraps buildBaseColorOverride in a logging
// helper for the run path: a malformed .mtlx is reported as a warning
// and the pipeline proceeds without the override (mirroring how
// applyBaseColor handles invalid hex colors). Returns nil on any
// error so the downstream voxel sampler falls back to per-face base
// colors.
func safeBaseColorOverride(path string, tileMM, triplanarSharpness float64) voxel.BaseColorOverride {
	o, err := buildBaseColorOverride(path, tileMM, triplanarSharpness)
	if err != nil {
		log.Printf("Warning: ignoring MaterialX base color: %v", err)
		return nil
	}
	return o
}
