package export3mf

import (
	"bytes"
	"image/png"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/render"
)

// renderThumbnail produces a square PNG showing the model with per-face palette
// colors baked in. Orthographic view from azimuth/elevation chosen to match
// Bambu's default preview angle.
func renderThumbnail(model *loader.LoadedModel, assignments []int32, paletteRGB [][3]uint8, size int) ([]byte, error) {
	const azimuth = 135.0
	const elevation = 30.0

	colorFn := func(faceIdx int, _, _ float64) [3]uint8 {
		idx := int(assignments[faceIdx])
		if idx < 0 || idx >= len(paletteRGB) {
			return [3]uint8{200, 200, 200}
		}
		return paletteRGB[idx]
	}

	bounds := render.ProjectedBounds(model.Vertices, azimuth, elevation)
	img := render.RenderColor(model.Vertices, model.Faces, azimuth, elevation, size, bounds, colorFn)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img.ToRGBA()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
