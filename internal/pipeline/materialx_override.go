package pipeline

import (
	"fmt"
	"log"

	"github.com/rtwfroody/ditherforge/internal/materialx"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// materialxOverride adapts a materialx.Sampler to voxel.BaseColorOverride,
// scaling input mm-positions into shading-unit positions before sampling
// and clamping the resulting RGB into 8-bit range. Reentrant — Sampler
// guarantees concurrent-safe Sample calls.
type materialxOverride struct {
	sampler   materialx.Sampler
	invTileMM float64
}

// SampleBaseColor implements voxel.BaseColorOverride.
func (m *materialxOverride) SampleBaseColor(p [3]float32) [3]uint8 {
	pos := [3]float64{
		float64(p[0]) * m.invTileMM,
		float64(p[1]) * m.invTileMM,
		float64(p[2]) * m.invTileMM,
	}
	rgb := m.sampler.Sample(pos)
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

// buildBaseColorOverride parses a MaterialX document and returns an
// override that samples its first material's base_color graph. tileMM
// converts world-space mm positions into the procedural's shading
// frame; values <= 0 are treated as 1 mm (no scaling). Returns
// (nil, nil) when content is empty so callers can pass the result
// straight through to the voxelizer.
func buildBaseColorOverride(content string, tileMM float64) (voxel.BaseColorOverride, error) {
	if content == "" {
		return nil, nil
	}
	doc, err := materialx.ParseBytes([]byte(content))
	if err != nil {
		return nil, fmt.Errorf("parse MaterialX: %w", err)
	}
	s, err := doc.DefaultBaseColorSampler()
	if err != nil {
		return nil, fmt.Errorf("MaterialX base color: %w", err)
	}
	if tileMM <= 0 {
		tileMM = 1
	}
	return &materialxOverride{sampler: s, invTileMM: 1 / tileMM}, nil
}

// safeBaseColorOverride wraps buildBaseColorOverride in a logging helper
// for the run path: a malformed .mtlx is reported as a warning and the
// pipeline proceeds without the override (mirroring how applyBaseColor
// handles invalid hex colors). Returns nil on any error so the
// downstream voxel sampler falls back to per-face base colors.
func safeBaseColorOverride(content string, tileMM float64) voxel.BaseColorOverride {
	o, err := buildBaseColorOverride(content, tileMM)
	if err != nil {
		log.Printf("Warning: ignoring MaterialX base color: %v", err)
		return nil
	}
	return o
}
