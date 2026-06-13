package voxel

import (
	"context"
	"fmt"
	"image"
	"math"
	"math/rand"
	"sort"
	"strings"

	colorful "github.com/lucasb-eyer/go-colorful"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/progress"
)

// ResolvePalette determines the final palette from cells and config.
// dithering indicates whether dithering will be used, which affects
// inventory color selection strategy.
// Returns the palette RGB values, parallel labels, and a display string
// for logging. Labels come from inventory entries; locked entries carry
// whatever label was set in PaletteConfig.Locked.
func ResolvePalette(ctx context.Context, cells []ActiveCell, pcfg PaletteConfig, dithering bool, tracker progress.Tracker) ([][3]uint8, []string, string, error) {
	lockedColors := make([][3]uint8, len(pcfg.Locked))
	lockedLabels := make([]string, len(pcfg.Locked))
	for i, e := range pcfg.Locked {
		lockedColors[i] = e.Color
		lockedLabels[i] = e.Label
	}

	remaining := pcfg.NumColors - len(pcfg.Locked)
	if remaining <= 0 {
		return lockedColors, lockedLabels, "", nil
	}

	cellColors := make([][3]uint8, len(cells))
	for i, c := range cells {
		cellColors[i] = c.Color
	}
	// All-or-nothing: production voxelization populates Area on every
	// cell, so cellWeights gets the per-cell areas. Synthetic/test
	// cell slices that don't populate Area on any cell get nil
	// (legacy uniform weighting). Mixed input — some Area populated,
	// some not — falls back to nil; CellColorHistogram and the dither
	// kernels use the same all-or-nothing rule via effectiveAreas, so
	// the palette and dither stages stay consistent.
	var cellWeights []float32
	allHaveAreas := true
	for _, c := range cells {
		if c.Area <= 0 {
			allHaveAreas = false
			break
		}
	}
	if allHaveAreas {
		cellWeights = make([]float32, len(cells))
		for i, c := range cells {
			cellWeights[i] = c.Area
		}
	}

	if len(pcfg.Inventory) > 0 {
		filtered := filterInventory(pcfg.Inventory, pcfg.Locked)
		if len(filtered) == 0 {
			return nil, nil, "", fmt.Errorf("inventory has no colors left after excluding locked colors")
		}
		selected, err := palette.SelectFromInventory(ctx, cellColors, cellWeights, filtered, remaining, lockedColors, dithering, tracker)
		if err != nil {
			return nil, nil, "", err
		}
		pal := make([][3]uint8, len(lockedColors), pcfg.NumColors)
		copy(pal, lockedColors)
		labels := make([]string, len(lockedLabels), pcfg.NumColors)
		copy(labels, lockedLabels)
		strs := make([]string, 0, len(selected))
		for _, e := range selected {
			pal = append(pal, e.Color)
			labels = append(labels, e.Label)
			s := fmt.Sprintf("#%02X%02X%02X", e.Color[0], e.Color[1], e.Color[2])
			if e.Label != "" {
				s += " (" + e.Label + ")"
			}
			strs = append(strs, s)
		}
		display := " " + strings.Join(strs, ", ")
		return pal, labels, display, nil
	}

	return nil, nil, "", fmt.Errorf("palette config has %d remaining slots but no inventory is set", remaining)
}

// filterInventory returns inventory entries that don't match any locked color.
func filterInventory(inv []palette.InventoryEntry, locked []palette.InventoryEntry) []palette.InventoryEntry {
	if len(locked) == 0 {
		return inv
	}
	lockedSet := make(map[[3]uint8]bool, len(locked))
	for _, e := range locked {
		lockedSet[e.Color] = true
	}
	filtered := make([]palette.InventoryEntry, 0, len(inv))
	for _, e := range inv {
		if !lockedSet[e.Color] {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// BoxSample averages a (2*radiusU+1) × (2*radiusV+1) box of texels
// around the UV point. radiusU/V are integer pixel radii. UV wraps
// in both dimensions to match BilinearSample.
//
// Intended for sliced-mesh sampling where each output face covers
// multiple texels and a point-sample produces visible aliasing:
// the section's UV footprint (cellSize × layerH translated through
// the source-triangle UV gradient) is large enough that adjacent
// sections land on different texels of a high-detail texture
// (earth.glb's coastlines and bathymetry). Averaging over the
// section's footprint kills the per-section noise without losing
// the texture's coarse structure.
func BoxSample(img image.Image, u, v float32, radiusU, radiusV int) [4]uint8 {
	if radiusU <= 0 && radiusV <= 0 {
		return BilinearSample(img, u, v)
	}
	bounds := img.Bounds()
	W := bounds.Max.X - bounds.Min.X
	H := bounds.Max.Y - bounds.Min.Y
	if W <= 0 || H <= 0 {
		return [4]uint8{}
	}
	u = u - float32(math.Floor(float64(u)))
	v = v - float32(math.Floor(float64(v)))
	cx := int(u*float32(W-1)) + bounds.Min.X
	cy := int(v*float32(H-1)) + bounds.Min.Y

	switch src := img.(type) {
	case *image.NRGBA:
		return boxSampleNRGBA(src, cx, cy, radiusU, radiusV, W, H)
	case *image.RGBA:
		return boxSampleRGBA(src, cx, cy, radiusU, radiusV, W, H)
	default:
		return boxSampleGeneric(img, cx, cy, radiusU, radiusV, W, H, bounds.Min.X, bounds.Min.Y)
	}
}

func boxSampleNRGBA(src *image.NRGBA, cx, cy, rU, rV, W, H int) [4]uint8 {
	pix := src.Pix
	stride := src.Stride
	origX := src.Rect.Min.X
	origY := src.Rect.Min.Y
	var sumR, sumG, sumB, sumA uint32
	var n uint32
	for dy := -rV; dy <= rV; dy++ {
		y := cy + dy
		if y < origY {
			y = origY
		}
		if y >= origY+H {
			y = origY + H - 1
		}
		for dx := -rU; dx <= rU; dx++ {
			x := ((cx + dx - origX) % W + W) % W + origX
			i := (y-origY)*stride + (x-origX)*4
			r, g, b, a := nrgbaPremul(pix, i)
			sumR += uint32(r)
			sumG += uint32(g)
			sumB += uint32(b)
			sumA += uint32(a)
			n++
		}
	}
	if n == 0 {
		return [4]uint8{}
	}
	return [4]uint8{uint8(sumR / n), uint8(sumG / n), uint8(sumB / n), uint8(sumA / n)}
}

func boxSampleRGBA(src *image.RGBA, cx, cy, rU, rV, W, H int) [4]uint8 {
	pix := src.Pix
	stride := src.Stride
	origX := src.Rect.Min.X
	origY := src.Rect.Min.Y
	var sumR, sumG, sumB, sumA uint32
	var n uint32
	for dy := -rV; dy <= rV; dy++ {
		y := cy + dy
		if y < origY {
			y = origY
		}
		if y >= origY+H {
			y = origY + H - 1
		}
		for dx := -rU; dx <= rU; dx++ {
			x := ((cx + dx - origX) % W + W) % W + origX
			i := (y-origY)*stride + (x-origX)*4
			sumR += uint32(pix[i])
			sumG += uint32(pix[i+1])
			sumB += uint32(pix[i+2])
			sumA += uint32(pix[i+3])
			n++
		}
	}
	if n == 0 {
		return [4]uint8{}
	}
	return [4]uint8{uint8(sumR / n), uint8(sumG / n), uint8(sumB / n), uint8(sumA / n)}
}

func boxSampleGeneric(img image.Image, cx, cy, rU, rV, W, H, minX, minY int) [4]uint8 {
	var sumR, sumG, sumB, sumA uint32
	var n uint32
	for dy := -rV; dy <= rV; dy++ {
		y := cy + dy
		if y < minY {
			y = minY
		}
		if y >= minY+H {
			y = minY + H - 1
		}
		for dx := -rU; dx <= rU; dx++ {
			x := ((cx + dx - minX) % W + W) % W + minX
			r, g, b, a := img.At(x, y).RGBA()
			sumR += r >> 8
			sumG += g >> 8
			sumB += b >> 8
			sumA += a >> 8
			n++
		}
	}
	if n == 0 {
		return [4]uint8{}
	}
	return [4]uint8{uint8(sumR / n), uint8(sumG / n), uint8(sumB / n), uint8(sumA / n)}
}

// BilinearSample samples a texture at normalized UV coordinates.
// Returns RGBA; alpha is 255 for textures without transparency.
//
// Values are premultiplied to match color.Color.RGBA() semantics: for NRGBA
// sources, RGB channels are scaled by A/255.
func BilinearSample(img image.Image, u, v float32) [4]uint8 {
	bounds := img.Bounds()
	W := float32(bounds.Max.X - bounds.Min.X)
	H := float32(bounds.Max.Y - bounds.Min.Y)

	u = u - float32(math.Floor(float64(u)))
	v = v - float32(math.Floor(float64(v)))

	px := u * (W - 1)
	py := v * (H - 1)

	// Relative (0-based within bounds) coordinates for the four corners.
	x0 := int(px)
	y0 := int(py)
	x1 := x0 + 1
	y1 := y0 + 1
	if x1 >= int(W) {
		x1 = int(W) - 1
	}
	if y1 >= int(H) {
		y1 = int(H) - 1
	}

	fx := px - float32(x0)
	fy := py - float32(y0)

	// Fast paths avoid the per-sample virtual dispatch of img.At().RGBA(),
	// which dominates the "Coloring cells" stage on textured models.
	// image.Decode produces *image.NRGBA for most PNGs and *image.YCbCr for
	// JPEGs; the RGBA case covers atlases and pre-premultiplied sources.
	switch src := img.(type) {
	case *image.NRGBA:
		return bilinearSampleNRGBA(src, x0, y0, x1, y1, fx, fy)
	case *image.RGBA:
		return bilinearSampleRGBA(src, x0, y0, x1, y1, fx, fy)
	default:
		return bilinearSampleGeneric(img, x0+bounds.Min.X, y0+bounds.Min.Y,
			x1+bounds.Min.X, y1+bounds.Min.Y, fx, fy)
	}
}

func bilinearLerp(a, b, c, d, fx, fy float32) uint8 {
	v := a*(1-fx)*(1-fy) + b*fx*(1-fy) + c*(1-fx)*fy + d*fx*fy
	if v < 0 {
		v = 0
	}
	if v > 255 {
		v = 255
	}
	return uint8(v + 0.5)
}

// bilinearSampleNRGBA reads pixels directly from the Pix buffer and
// premultiplies by alpha to match the NRGBA.RGBA() conversion done in the
// generic path.
func bilinearSampleNRGBA(src *image.NRGBA, x0, y0, x1, y1 int, fx, fy float32) [4]uint8 {
	pix := src.Pix
	stride := src.Stride
	// Coordinates are 0-based within src.Rect; Pix is row-major at that origin.
	i00 := y0*stride + x0*4
	i10 := y0*stride + x1*4
	i01 := y1*stride + x0*4
	i11 := y1*stride + x1*4

	r00, g00, b00, a00 := nrgbaPremul(pix, i00)
	r10, g10, b10, a10 := nrgbaPremul(pix, i10)
	r01, g01, b01, a01 := nrgbaPremul(pix, i01)
	r11, g11, b11, a11 := nrgbaPremul(pix, i11)

	return [4]uint8{
		bilinearLerp(r00, r10, r01, r11, fx, fy),
		bilinearLerp(g00, g10, g01, g11, fx, fy),
		bilinearLerp(b00, b10, b01, b11, fx, fy),
		bilinearLerp(a00, a10, a01, a11, fx, fy),
	}
}

func nrgbaPremul(pix []uint8, i int) (float32, float32, float32, float32) {
	a := uint32(pix[i+3])
	// Mirror NRGBA.RGBA() exactly so rounding matches the generic path:
	// stdlib computes R*0x101*A/0xff as a 16-bit value; we then take the
	// high byte as the 8-bit sample value.
	r := (uint32(pix[i]) * 0x101 * a / 0xff) >> 8
	g := (uint32(pix[i+1]) * 0x101 * a / 0xff) >> 8
	b := (uint32(pix[i+2]) * 0x101 * a / 0xff) >> 8
	return float32(r), float32(g), float32(b), float32(a)
}

// bilinearSampleRGBA reads directly from the Pix buffer. RGBA is already
// premultiplied, so no conversion is needed.
func bilinearSampleRGBA(src *image.RGBA, x0, y0, x1, y1 int, fx, fy float32) [4]uint8 {
	pix := src.Pix
	stride := src.Stride
	i00 := y0*stride + x0*4
	i10 := y0*stride + x1*4
	i01 := y1*stride + x0*4
	i11 := y1*stride + x1*4

	return [4]uint8{
		bilinearLerp(float32(pix[i00]), float32(pix[i10]), float32(pix[i01]), float32(pix[i11]), fx, fy),
		bilinearLerp(float32(pix[i00+1]), float32(pix[i10+1]), float32(pix[i01+1]), float32(pix[i11+1]), fx, fy),
		bilinearLerp(float32(pix[i00+2]), float32(pix[i10+2]), float32(pix[i01+2]), float32(pix[i11+2]), fx, fy),
		bilinearLerp(float32(pix[i00+3]), float32(pix[i10+3]), float32(pix[i01+3]), float32(pix[i11+3]), fx, fy),
	}
}

// bilinearSampleGeneric is the original image.Image-based path. Used for
// sources like *image.YCbCr (JPEG), *image.Paletted, and *image.Gray.
func bilinearSampleGeneric(img image.Image, x0, y0, x1, y1 int, fx, fy float32) [4]uint8 {
	sample := func(x, y int) (float32, float32, float32, float32) {
		r, g, b, a := img.At(x, y).RGBA()
		return float32(r >> 8), float32(g >> 8), float32(b >> 8), float32(a >> 8)
	}
	r00, g00, b00, a00 := sample(x0, y0)
	r10, g10, b10, a10 := sample(x1, y0)
	r01, g01, b01, a01 := sample(x0, y1)
	r11, g11, b11, a11 := sample(x1, y1)

	return [4]uint8{
		bilinearLerp(r00, r10, r01, r11, fx, fy),
		bilinearLerp(g00, g10, g01, g11, fx, fy),
		bilinearLerp(b00, b10, b01, b11, fx, fy),
		bilinearLerp(a00, a10, a01, a11, fx, fy),
	}
}

// faceMaterial returns material-level alpha, base color, and texture index for a face.
func faceMaterial(faceIdx int, model *loader.LoadedModel) (matAlpha float32, bc [4]uint8, texIdx int32) {
	matAlpha = 1.0
	if model.FaceAlpha != nil {
		matAlpha = model.FaceAlpha[faceIdx]
	}
	bc = [4]uint8{255, 255, 255, 255}
	if model.FaceBaseColor != nil {
		bc = model.FaceBaseColor[faceIdx]
	}
	texIdx = -1
	if model.FaceTextureIdx != nil {
		texIdx = model.FaceTextureIdx[faceIdx]
	}
	return
}

// FaceAlpha returns the effective alpha for a face, sampling the texture at
// the centroid UV and combining with material alpha and base color alpha.
// Note: centroid sampling is an approximation; large triangles spanning
// both opaque and transparent texture regions may be misclassified.
func FaceAlpha(faceIdx int, model *loader.LoadedModel) uint8 {
	matAlpha, bc, texIdx := faceMaterial(faceIdx, model)

	f := model.Faces[faceIdx]

	// Vertex-colored faces: average vertex alpha at centroid.
	if (texIdx < 0 || int(texIdx) >= len(model.Textures)) && model.VertexColors != nil {
		c0 := model.VertexColors[f[0]]
		c1 := model.VertexColors[f[1]]
		c2 := model.VertexColors[f[2]]
		avgA := (float32(c0[3]) + float32(c1[3]) + float32(c2[3])) / 3
		a := avgA / 255 * float32(bc[3]) * matAlpha
		return uint8(ClampF(a+0.5, 0, 255))
	}

	if texIdx < 0 || int(texIdx) >= len(model.Textures) {
		return uint8(ClampF(matAlpha*float32(bc[3])+0.5, 0, 255))
	}
	uv0 := model.UVs[f[0]]
	uv1 := model.UVs[f[1]]
	uv2 := model.UVs[f[2]]
	u := (uv0[0] + uv1[0] + uv2[0]) / 3
	v := (uv0[1] + uv1[1] + uv2[1]) / 3

	rgba := BilinearSample(model.Textures[texIdx], u, v)
	texA := float32(rgba[3]) / 255
	a := texA * float32(bc[3]) * matAlpha
	return uint8(ClampF(a+0.5, 0, 255))
}

// BaseColorContext bundles the inputs an override may consult when
// sampling. Pos is the world-space sample point in the original
// (pre-split) mesh frame; Normal is the unit-length surface normal of
// the closest face (zero when the face is degenerate). Image-backed
// MaterialX graphs use Normal to drive triplanar projection; pure-
// procedural graphs ignore it.
type BaseColorContext struct {
	Pos    [3]float32
	Normal [3]float32
}

// BaseColorOverride supplies a procedural replacement for an
// untextured face's base color, evaluated at the sample's 3D position.
// Implementations must be safe to call from many goroutines
// concurrently. Returns RGB only; alpha continues to come from the
// model's per-face material.
type BaseColorOverride interface {
	SampleBaseColor(ctx BaseColorContext) [3]uint8
}

// FaceNormal returns the unit-length normal of the face at faceIdx
// computed from its three vertex positions (right-handed cross
// product, v0→v1 × v0→v2). Returns the zero vector when the face is
// degenerate (zero area within float32 precision).
func FaceNormal(faceIdx int, model *loader.LoadedModel) [3]float32 {
	f := model.Faces[faceIdx]
	v0 := model.Vertices[f[0]]
	v1 := model.Vertices[f[1]]
	v2 := model.Vertices[f[2]]
	e1 := [3]float32{v1[0] - v0[0], v1[1] - v0[1], v1[2] - v0[2]}
	e2 := [3]float32{v2[0] - v0[0], v2[1] - v0[1], v2[2] - v0[2]}
	n := [3]float32{
		e1[1]*e2[2] - e1[2]*e2[1],
		e1[2]*e2[0] - e1[0]*e2[2],
		e1[0]*e2[1] - e1[1]*e2[0],
	}
	l := float32(math.Sqrt(float64(n[0]*n[0] + n[1]*n[1] + n[2]*n[2])))
	if l < 1e-9 {
		return [3]float32{}
	}
	return [3]float32{n[0] / l, n[1] / l, n[2] / l}
}

// SampleNearestColor finds the closest surface point to p on `model`, then
// samples the texture color and alpha there. If decals are provided,
// sticker textures are composited over the base color. Returns RGBA.
//
// When stickers live on a *different* mesh than the base color sample
// model (alpha-wrap mode: original mesh carries texture/UV, wrap mesh
// carries decals), call SampleNearestColorWithSticker instead.
func SampleNearestColor(p [3]float32, model *loader.LoadedModel, si *SpatialIndex, radius float32, buf *SearchBuf, decals []*StickerDecal, override BaseColorOverride) [4]uint8 {
	return SampleNearestColorWithSticker(p, model, si, radius, buf, decals, nil, nil, nil, override)
}

// SampleNearestColorWithSticker is the two-mesh form of SampleNearestColor.
// When stickerModel is nil (or aliases model), behavior is identical to
// SampleNearestColor: a single nearest-tri lookup against `model` is used
// for both base color and sticker compositing. When stickerModel is a
// distinct mesh, a second nearest-tri lookup against stickerModel/stickerSI
// is performed and decals are composited based on that result. stickerBuf
// must be a separate SearchBuf sized for stickerModel; passing nil reuses
// `buf` (safe because the two lookups don't overlap in time).
//
// override (optional) replaces the per-face base color with a
// procedurally sampled RGB at p, but only for untextured faces — when
// the nearest face has a usable texture, the texture wins as usual.
// Pass nil for the legacy behavior (per-face FaceBaseColor only).
func SampleNearestColorWithSticker(
	p [3]float32,
	model *loader.LoadedModel, si *SpatialIndex, radius float32, buf *SearchBuf,
	decals []*StickerDecal,
	stickerModel *loader.LoadedModel, stickerSI *SpatialIndex, stickerBuf *SearchBuf,
	override BaseColorOverride,
) [4]uint8 {
	cands := si.CandidatesRadiusZ(p[0], p[1], radius, p[2], radius, buf)
	// Track the nearest exterior-visible face and the nearest hidden
	// face separately (si.FaceVisible nil = everything visible). A
	// visible face wins over any nearer hidden one: interior geometry
	// that hugs the skin — flood-fill pocket caps sit well under one
	// search radius beneath the painted surface — must not bleed its
	// color into cells on the visible surface. The hidden best is only
	// a fallback for sample points with no visible face in range at
	// all (e.g. a car interior behind window glass), which keeps fully
	// enclosed regions sampling their own colors.
	vis := si.FaceVisible
	bestDistSq := float32(math.MaxFloat32)
	bestTri := int32(-1)
	var bestS, bestT float32
	hidDistSq := float32(math.MaxFloat32)
	hidTri := int32(-1)
	var hidS, hidT float32
	for _, ti := range cands {
		f := model.Faces[ti]
		r := ClosestPointOnTriangle(p, model.Vertices[f[0]], model.Vertices[f[1]], model.Vertices[f[2]])
		if vis != nil && !vis[ti] {
			if r.DistSq < hidDistSq {
				hidDistSq = r.DistSq
				hidTri = ti
				hidS = r.S
				hidT = r.T
			}
			continue
		}
		if r.DistSq < bestDistSq {
			bestDistSq = r.DistSq
			bestTri = ti
			bestS = r.S
			bestT = r.T
		}
	}
	if bestTri < 0 {
		bestTri, bestS, bestT = hidTri, hidS, hidT
	}

	if bestTri < 0 {
		// No triangle within the search radius (in XY and the Z
		// window). This is "no surface found here," not a real grey
		// sample — return fully transparent so multi-point cell
		// samplers (SampleSlab) skip it instead of averaging an
		// opaque mid-grey into the cell. Painting misses grey
		// silently darkened cells whose nearest surface sat just
		// beyond the radius (e.g. inner-ring cells ~1 cell-width in
		// from a wall), and a grid-bucket boundary asymmetry made it
		// hit the max-coordinate walls but not the min ones — turning
		// a uniform cube into a two-tone one after quantization.
		return [4]uint8{0, 0, 0, 0}
	}

	matAlpha, bc, texIdx := faceMaterial(int(bestTri), model)
	if override != nil && (texIdx < 0 || int(texIdx) >= len(model.Textures)) {
		rgb := override.SampleBaseColor(BaseColorContext{
			Pos:    p,
			Normal: FaceNormal(int(bestTri), model),
		})
		bc[0], bc[1], bc[2] = rgb[0], rgb[1], rgb[2]
	}
	f := model.Faces[bestTri]
	bary := [3]float32{1 - bestS - bestT, bestS, bestT}

	var rgba [4]uint8
	if texIdx >= 0 && int(texIdx) < len(model.Textures) {
		// Texture sampling path.
		uv0 := model.UVs[f[0]]
		uv1 := model.UVs[f[1]]
		uv2 := model.UVs[f[2]]

		u := bary[0]*uv0[0] + bary[1]*uv1[0] + bary[2]*uv2[0]
		v := bary[0]*uv0[1] + bary[1]*uv1[1] + bary[2]*uv2[1]

		rgba = BilinearSample(model.Textures[texIdx], u, v)
		// Alpha-blend texture sample over material base color.
		texA := float32(rgba[3]) / 255
		rgba[0] = uint8(float32(rgba[0])*texA + float32(bc[0])*(1-texA))
		rgba[1] = uint8(float32(rgba[1])*texA + float32(bc[1])*(1-texA))
		rgba[2] = uint8(float32(rgba[2])*texA + float32(bc[2])*(1-texA))
		// Combine texture alpha, base color alpha, and material alpha.
		rgba[3] = uint8(ClampF(texA*float32(bc[3])*matAlpha+0.5, 0, 255))
	} else if model.VertexColors != nil {
		// Vertex color interpolation path.
		c0 := model.VertexColors[f[0]]
		c1 := model.VertexColors[f[1]]
		c2 := model.VertexColors[f[2]]
		r := bary[0]*float32(c0[0]) + bary[1]*float32(c1[0]) + bary[2]*float32(c2[0])
		g := bary[0]*float32(c0[1]) + bary[1]*float32(c1[1]) + bary[2]*float32(c2[1])
		b := bary[0]*float32(c0[2]) + bary[1]*float32(c1[2]) + bary[2]*float32(c2[2])
		a := bary[0]*float32(c0[3]) + bary[1]*float32(c1[3]) + bary[2]*float32(c2[3])
		// Modulate by material base color and alpha.
		rgba = [4]uint8{
			uint8(ClampF(r*float32(bc[0])/255+0.5, 0, 255)),
			uint8(ClampF(g*float32(bc[1])/255+0.5, 0, 255)),
			uint8(ClampF(b*float32(bc[2])/255+0.5, 0, 255)),
			uint8(ClampF(a*float32(bc[3])/255*matAlpha+0.5, 0, 255)),
		}
	} else {
		// Base color only path.
		a := uint8(ClampF(matAlpha*float32(bc[3])+0.5, 0, 255))
		rgba = [4]uint8{bc[0], bc[1], bc[2], a}
	}

	// Composite sticker decals over the base color. When stickers live on
	// a separate mesh (alpha-wrap mode), do an independent nearest-tri
	// lookup against that mesh; decal TriUVs index into it, not into the
	// color sample mesh.
	if len(decals) > 0 {
		if stickerModel == nil || stickerModel == model {
			rgba = CompositeStickerColor(rgba, bestTri, bary, decals)
		} else {
			sBuf := stickerBuf
			if sBuf == nil {
				sBuf = buf
			}
			// Note: this lookup deliberately ignores FaceVisible-style
			// visibility filtering (stickerSI carries none). The sticker
			// substrate is the projection clone / wrap — an outer surface
			// with no interior geometry to bleed through — and this
			// lookup only maps p into the substrate's UV space for decal
			// compositing; the base color above is already
			// visibility-filtered.
			sCands := stickerSI.CandidatesRadiusZ(p[0], p[1], radius, p[2], radius, sBuf)
			sBestDistSq := float32(math.MaxFloat32)
			sBestTri := int32(-1)
			var sBestS, sBestT float32
			for _, ti := range sCands {
				f := stickerModel.Faces[ti]
				r := ClosestPointOnTriangle(p,
					stickerModel.Vertices[f[0]], stickerModel.Vertices[f[1]], stickerModel.Vertices[f[2]])
				if r.DistSq < sBestDistSq {
					sBestDistSq = r.DistSq
					sBestTri = ti
					sBestS = r.S
					sBestT = r.T
				}
			}
			if sBestTri >= 0 {
				sBary := [3]float32{1 - sBestS - sBestT, sBestS, sBestT}
				rgba = CompositeStickerColor(rgba, sBestTri, sBary, decals)
			}
		}
	}

	return rgba
}

// Neighbor holds a precomputed neighbor reference with its diffusion weight.
type Neighbor struct {
	Idx    int
	Weight float32
}

// sampleBaryAt returns the barycentric coords of `p` on triangle
// (v0,v1,v2) via 3D closest-point. For points lying on the
// triangle (ribbon section midpoints) this returns the exact
// barycentric. For points off the surface (cap-tile XY centers
// at z = cap_z, where the dome surface curves above/below the
// tile) it returns the barycentric of the nearest surface point,
// which is what we want — sample the model's color at the
// closest piece of geometry.
func sampleBaryAt(p, v0, v1, v2 [3]float32) [3]float32 {
	r := ClosestPointOnTriangle(p, v0, v1, v2)
	return [3]float32{1 - r.S - r.T, r.S, r.T}
}

// SampleByTriangle samples the model's color at a point assumed to
// be at or near a specific triangle's surface. Computes barycentric
// of p on triangle triIdx and looks up texture / vertex color /
// base color from those coords. Unlike SampleNearestColor, this
// performs no spatial-index search — the caller asserts which
// triangle the point belongs to. Used when the slicer already
// knows the source triangle for a sample point and we want to
// avoid nearest-tri picking up unrelated triangles from a nearby
// object.
func SampleByTriangle(p [3]float32, model *loader.LoadedModel, triIdx int32) [4]uint8 {
	return SampleByTriangleFootprint(p, model, triIdx, 0, 0)
}

// SampleByTriangleFootprint is SampleByTriangle with an explicit
// texture-domain box filter: the texture is sampled as the average
// of a (2*radiusU+1) × (2*radiusV+1) texel box, instead of one
// bilinear texel. Use this when one section covers many texels of
// the source texture (high-detail textures like earth.glb): each
// section gets a single sample whose value represents the texture
// AVERAGE across the section's UV footprint, not one arbitrary
// texel inside it. With radiusU=radiusV=0 this collapses to the
// plain bilinear sample.
//
// Caller computes the section's UV footprint and converts to pixel
// radii using the texture dimensions; this function doesn't know
// the section.
func SampleByTriangleFootprint(p [3]float32, model *loader.LoadedModel, triIdx int32, radiusU, radiusV int) [4]uint8 {
	if triIdx < 0 || int(triIdx) >= len(model.Faces) {
		return [4]uint8{128, 128, 128, 255}
	}
	f := model.Faces[triIdx]
	v0 := model.Vertices[f[0]]
	v1 := model.Vertices[f[1]]
	v2 := model.Vertices[f[2]]
	bary := sampleBaryAt(p, v0, v1, v2)
	matAlpha, bc, texIdx := faceMaterial(int(triIdx), model)

	if texIdx >= 0 && int(texIdx) < len(model.Textures) {
		uv0 := model.UVs[f[0]]
		uv1 := model.UVs[f[1]]
		uv2 := model.UVs[f[2]]
		u := bary[0]*uv0[0] + bary[1]*uv1[0] + bary[2]*uv2[0]
		v := bary[0]*uv0[1] + bary[1]*uv1[1] + bary[2]*uv2[1]
		var rgba [4]uint8
		if radiusU > 0 || radiusV > 0 {
			rgba = BoxSample(model.Textures[texIdx], u, v, radiusU, radiusV)
		} else {
			rgba = BilinearSample(model.Textures[texIdx], u, v)
		}
		texA := float32(rgba[3]) / 255
		rgba[0] = uint8(float32(rgba[0])*texA + float32(bc[0])*(1-texA))
		rgba[1] = uint8(float32(rgba[1])*texA + float32(bc[1])*(1-texA))
		rgba[2] = uint8(float32(rgba[2])*texA + float32(bc[2])*(1-texA))
		rgba[3] = uint8(ClampF(texA*float32(bc[3])*matAlpha+0.5, 0, 255))
		return rgba
	}
	if model.VertexColors != nil {
		c0 := model.VertexColors[f[0]]
		c1 := model.VertexColors[f[1]]
		c2 := model.VertexColors[f[2]]
		rr := bary[0]*float32(c0[0]) + bary[1]*float32(c1[0]) + bary[2]*float32(c2[0])
		gg := bary[0]*float32(c0[1]) + bary[1]*float32(c1[1]) + bary[2]*float32(c2[1])
		bb := bary[0]*float32(c0[2]) + bary[1]*float32(c1[2]) + bary[2]*float32(c2[2])
		aa := bary[0]*float32(c0[3]) + bary[1]*float32(c1[3]) + bary[2]*float32(c2[3])
		return [4]uint8{
			uint8(ClampF(rr*float32(bc[0])/255+0.5, 0, 255)),
			uint8(ClampF(gg*float32(bc[1])/255+0.5, 0, 255)),
			uint8(ClampF(bb*float32(bc[2])/255+0.5, 0, 255)),
			uint8(ClampF(aa*float32(bc[3])/255*matAlpha+0.5, 0, 255)),
		}
	}
	a := uint8(ClampF(matAlpha*float32(bc[3])+0.5, 0, 255))
	return [4]uint8{bc[0], bc[1], bc[2], a}
}

// SampleByTrianglePoints is a legacy multi-point averaging path
// kept for callers that don't yet compute footprint radii. Each
// point is single-sampled and the results are averaged.
func SampleByTrianglePoints(model *loader.LoadedModel, triIdx int32, pts [][3]float32) [4]uint8 {
	if triIdx < 0 || int(triIdx) >= len(model.Faces) || len(pts) == 0 {
		return [4]uint8{128, 128, 128, 255}
	}
	f := model.Faces[triIdx]
	v0 := model.Vertices[f[0]]
	v1 := model.Vertices[f[1]]
	v2 := model.Vertices[f[2]]
	matAlpha, bc, texIdx := faceMaterial(int(triIdx), model)

	var sumR, sumG, sumB, sumA float32
	n := float32(len(pts))

	switch {
	case texIdx >= 0 && int(texIdx) < len(model.Textures):
		uv0 := model.UVs[f[0]]
		uv1 := model.UVs[f[1]]
		uv2 := model.UVs[f[2]]
		tex := model.Textures[texIdx]
		for _, p := range pts {
			bary := sampleBaryAt(p, v0, v1, v2)
			u := bary[0]*uv0[0] + bary[1]*uv1[0] + bary[2]*uv2[0]
			v := bary[0]*uv0[1] + bary[1]*uv1[1] + bary[2]*uv2[1]
			rgba := BilinearSample(tex, u, v)
			texA := float32(rgba[3]) / 255
			sumR += float32(rgba[0])*texA + float32(bc[0])*(1-texA)
			sumG += float32(rgba[1])*texA + float32(bc[1])*(1-texA)
			sumB += float32(rgba[2])*texA + float32(bc[2])*(1-texA)
			sumA += texA * float32(bc[3]) * matAlpha
		}
		return [4]uint8{
			uint8(ClampF(sumR/n+0.5, 0, 255)),
			uint8(ClampF(sumG/n+0.5, 0, 255)),
			uint8(ClampF(sumB/n+0.5, 0, 255)),
			uint8(ClampF(sumA/n+0.5, 0, 255)),
		}
	case model.VertexColors != nil:
		c0 := model.VertexColors[f[0]]
		c1 := model.VertexColors[f[1]]
		c2 := model.VertexColors[f[2]]
		for _, p := range pts {
			bary := sampleBaryAt(p, v0, v1, v2)
			rr := bary[0]*float32(c0[0]) + bary[1]*float32(c1[0]) + bary[2]*float32(c2[0])
			gg := bary[0]*float32(c0[1]) + bary[1]*float32(c1[1]) + bary[2]*float32(c2[1])
			bb := bary[0]*float32(c0[2]) + bary[1]*float32(c1[2]) + bary[2]*float32(c2[2])
			aa := bary[0]*float32(c0[3]) + bary[1]*float32(c1[3]) + bary[2]*float32(c2[3])
			sumR += rr * float32(bc[0]) / 255
			sumG += gg * float32(bc[1]) / 255
			sumB += bb * float32(bc[2]) / 255
			sumA += aa * float32(bc[3]) / 255 * matAlpha
		}
		return [4]uint8{
			uint8(ClampF(sumR/n+0.5, 0, 255)),
			uint8(ClampF(sumG/n+0.5, 0, 255)),
			uint8(ClampF(sumB/n+0.5, 0, 255)),
			uint8(ClampF(sumA/n+0.5, 0, 255)),
		}
	default:
		a := uint8(ClampF(matAlpha*float32(bc[3])+0.5, 0, 255))
		return [4]uint8{bc[0], bc[1], bc[2], a}
	}
}

// effectiveAreas returns a per-cell area slice for use by area-weighted
// dithering. All-or-nothing: if every cell has Area > 0 (production
// path), returns those areas verbatim. If any cell has Area <= 0
// (synthetic / test slices), returns a uniform-1 slice so the dither
// kernel reduces to the legacy unweighted form. Mixed inputs would
// silently overweight the zero-area cells, so we fall back rather than
// guess. Always returns a slice of len(cells); never nil.
func effectiveAreas(cells []ActiveCell) []float32 {
	areas := make([]float32, len(cells))
	for i, c := range cells {
		if c.Area <= 0 {
			for j := range areas {
				areas[j] = 1
			}
			return areas
		}
		areas[i] = c.Area
	}
	return areas
}

// BuildNeighbors computes the neighbor list for each cell using within-grid
// CellKey adjacency (26-connected). Face-adjacent weight 1.0, edge 0.1,
// corner 0.01.
func BuildNeighbors(cells []ActiveCell) [][]Neighbor {
	n := len(cells)
	cellMap := make(map[CellKey]int, n)
	for i, c := range cells {
		cellMap[CellKey{Grid: c.Grid, Col: c.Col, Row: c.Row, Layer: c.Layer}] = i
	}

	neighbors := make([][]Neighbor, n)
	for i, c := range cells {
		var nbrs []Neighbor
		for dc := -1; dc <= 1; dc++ {
			for dr := -1; dr <= 1; dr++ {
				for dl := -1; dl <= 1; dl++ {
					if dc == 0 && dr == 0 && dl == 0 {
						continue
					}
					if j, ok := cellMap[CellKey{Grid: c.Grid, Col: c.Col + dc, Row: c.Row + dr, Layer: c.Layer + dl}]; ok {
						axes := 0
						if dc != 0 {
							axes++
						}
						if dr != 0 {
							axes++
						}
						if dl != 0 {
							axes++
						}
						var w float32
						switch axes {
						case 1:
							w = 1.0
						case 2:
							w = 0.1
						case 3:
							w = 0.01
						}
						nbrs = append(nbrs, Neighbor{Idx: j, Weight: w})
					}
				}
			}
		}
		neighbors[i] = nbrs
	}
	return neighbors
}

// BuildNeighbors2Hop is BuildNeighbors with the offset range extended
// from {-1,0,+1} to {-2,-1,0,+1,+2}. It exists to give random-order
// error-diffusion (dizzy) somewhere to dump residual error when all
// 1-hop neighbors are already processed (the "stranded tail" that
// causes dizzy's chroma drift).
//
// 1-hop weights (chebyshev=1) match BuildNeighbors exactly:
//   axes=1 → 1.0, axes=2 → 0.1, axes=3 → 0.01.
// 2-hop weights (chebyshev=2) are 100× smaller for the same axes
// count, continuing the same 10×-per-step falloff pattern:
//   axes=1 → 0.01, axes=2 → 0.001, axes=3 → 0.0001.
//
// The 100× gap means 1-hop neighbors dominate when any are
// unprocessed; 2-hop only matters as a fallback. Surface cells
// typically have 4-8 1-hop neighbors and another 8-16 2-hop
// neighbors, so the per-cell list grows ~3× — memory cost is real
// (~160-240 MB for a million-cell mesh) but manageable for an
// experimental mode.
func BuildNeighbors2Hop(cells []ActiveCell) [][]Neighbor {
	n := len(cells)
	cellMap := make(map[CellKey]int, n)
	for i, c := range cells {
		cellMap[CellKey{Grid: c.Grid, Col: c.Col, Row: c.Row, Layer: c.Layer}] = i
	}

	baseWeight := [4]float32{0, 1.0, 0.1, 0.01} // by axes count

	neighbors := make([][]Neighbor, n)
	for i, c := range cells {
		var nbrs []Neighbor
		for dc := -2; dc <= 2; dc++ {
			for dr := -2; dr <= 2; dr++ {
				for dl := -2; dl <= 2; dl++ {
					if dc == 0 && dr == 0 && dl == 0 {
						continue
					}
					j, ok := cellMap[CellKey{Grid: c.Grid, Col: c.Col + dc, Row: c.Row + dr, Layer: c.Layer + dl}]
					if !ok {
						continue
					}
					axes := 0
					if dc != 0 {
						axes++
					}
					if dr != 0 {
						axes++
					}
					if dl != 0 {
						axes++
					}
					adc, adr, adl := dc, dr, dl
					if adc < 0 {
						adc = -adc
					}
					if adr < 0 {
						adr = -adr
					}
					if adl < 0 {
						adl = -adl
					}
					cheb := adc
					if adr > cheb {
						cheb = adr
					}
					if adl > cheb {
						cheb = adl
					}
					w := baseWeight[axes]
					if cheb == 2 {
						w /= 100
					}
					nbrs = append(nbrs, Neighbor{Idx: j, Weight: w})
				}
			}
		}
		neighbors[i] = nbrs
	}
	return neighbors
}

// --- Perceptual dithering color space ---
//
// Error-diffusion dithering decides the nearest filament in CIELAB
// (perceptual) but accumulates and diffuses the quantization residual
// in LINEAR light. The eye spatially integrates adjacent filament
// tiles as photons (a linear sum), so error conservation is only
// physically correct in linear space; the nearest-tile *choice* is
// best judged perceptually. Plain gamma-sRGB (the historical
// behavior) is wrong for both jobs: it under-mixes saturated/midtone
// targets because the eye's linear integration of the tiles lands
// brighter/different than the sRGB-space arithmetic the old diffusion
// conserved.

// srgbToLinearLUT maps an 8-bit sRGB channel to linear light in [0,1].
var srgbToLinearLUT = func() [256]float32 {
	var lut [256]float32
	for i := range lut {
		c := float64(i) / 255.0
		if c <= 0.04045 {
			lut[i] = float32(c / 12.92)
		} else {
			lut[i] = float32(math.Pow((c+0.055)/1.055, 2.4))
		}
	}
	return lut
}()

// labF is the CIELAB nonlinearity. math.Cbrt handles the negative
// arguments that arise when accumulated diffusion error pushes a
// linear target below 0, so out-of-gamut targets never produce NaN.
func labF(t float64) float64 {
	const d = 6.0 / 29.0
	if t > d*d*d {
		return math.Cbrt(t)
	}
	return t/(3*d*d) + 4.0/29.0
}

// linearToLab converts linear-light RGB (D65), which may fall outside
// [0,1] while carrying accumulated diffusion error, to CIELAB at
// standard scale (L in [0,100]). Only relative distances matter for
// the nearest-palette search, so the absolute scale is unimportant as
// long as palette and target use this same function.
func linearToLab(r, g, b float32) (float32, float32, float32) {
	R, G, B := float64(r), float64(g), float64(b)
	x := R*0.4124564 + G*0.3575761 + B*0.1804375
	y := R*0.2126729 + G*0.7151522 + B*0.0721750
	z := R*0.0193339 + G*0.1191920 + B*0.9503041
	const xn, yn, zn = 0.95047, 1.0, 1.08883
	fx, fy, fz := labF(x/xn), labF(y/yn), labF(z/zn)
	return float32(116*fy - 16), float32(500 * (fx - fy)), float32(200 * (fy - fz))
}

// linearToSrgbByte encodes a linear-light channel [0,1] back to an
// 8-bit sRGB value, the inverse of srgbToLinearLUT. Used where a
// linear-domain computation must hand a color back to the uint8
// ActiveCell.Color representation (DitherCorrected's input shift).
func linearToSrgbByte(l float32) uint8 {
	L := float64(l)
	if L <= 0 {
		return 0
	}
	if L >= 1 {
		return 255
	}
	var s float64
	if L <= 0.0031308 {
		s = L * 12.92
	} else {
		s = 1.055*math.Pow(L, 1.0/2.4) - 0.055
	}
	v := s*255 + 0.5
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// paletteLinearLab precomputes each palette color in both linear-light
// RGB (for residual computation/diffusion) and CIELAB (for the
// nearest-color decision).
func paletteLinearLab(pal [][3]uint8) (lin [][3]float32, lab [][3]float32) {
	lin = make([][3]float32, len(pal))
	lab = make([][3]float32, len(pal))
	for i, p := range pal {
		lin[i] = [3]float32{srgbToLinearLUT[p[0]], srgbToLinearLUT[p[1]], srgbToLinearLUT[p[2]]}
		l, a, b := linearToLab(lin[i][0], lin[i][1], lin[i][2])
		lab[i] = [3]float32{l, a, b}
	}
	return lin, lab
}

// nearestPaletteLab returns the index of the palette entry closest to
// (L,A,B) by squared CIELAB distance.
func nearestPaletteLab(L, A, B float32, palLab [][3]float32) int {
	best := 0
	bestDist := float32(math.MaxFloat32)
	for pi := range palLab {
		dL := L - palLab[pi][0]
		dA := A - palLab[pi][1]
		dB := B - palLab[pi][2]
		d := dL*dL + dA*dA + dB*dB
		if d < bestDist {
			bestDist = d
			best = pi
		}
	}
	return best
}

// DitherCellsDizzy applies dizzy dithering: random traversal order with
// error diffusion to actual spatial neighbors. Produces blue-noise-like
// results without directional bias.
func DitherCellsDizzy(ctx context.Context, cells []ActiveCell, pal [][3]uint8) ([]int32, error) {
	return DitherWithNeighbors(ctx, cells, pal, BuildNeighbors(cells), nil)
}
// DitherWithNeighbors runs dizzy dithering using a precomputed neighbor table.
// If tracker is non-nil, emits StageProgress("Dithering", current) every 1000
// cells. Caller owns StageStart/StageDone.
//
// Colors are handled in linear light with a perceptual (CIELAB)
// nearest-filament decision; see "Perceptual dithering color space".
// The (cell + accumulated error) target is fed to the nearest-palette
// search WITHOUT clamping: clamping silently discards the residual
// past the gamut boundary and biases the output toward the clamped
// direction. linearToLab tolerates out-of-[0,1] (even negative)
// targets, so the per-cell error term carries the full unclamped
// discrepancy out to neighbors. This matches the structure of the
// reference dizzy implementation (Liam Appelbe, 2020).
func DitherWithNeighbors(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)

	rng := rand.New(rand.NewSource(42))
	order := rng.Perm(n)

	assignments := make([]int32, n)
	errBuf := make([][3]float32, n) // accumulated residual in linear light
	processed := make([]bool, n)
	areas := effectiveAreas(cells)
	palLin, palLab := paletteLinearLab(pal)

	for oi, idx := range order {
		if oi%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", oi)
		}
		// Target = linearized cell color + accumulated linear residual.
		// Decide the nearest filament perceptually (CIELAB); diffuse
		// the residual in linear light. See "Perceptual dithering
		// color space" above.
		r := srgbToLinearLUT[cells[idx].Color[0]] + errBuf[idx][0]
		g := srgbToLinearLUT[cells[idx].Color[1]] + errBuf[idx][1]
		b := srgbToLinearLUT[cells[idx].Color[2]] + errBuf[idx][2]

		tL, tA, tB := linearToLab(r, g, b)
		bestIdx := nearestPaletteLab(tL, tA, tB, palLab)
		assignments[idx] = int32(bestIdx)
		processed[idx] = true

		eR := r - palLin[bestIdx][0]
		eG := g - palLin[bestIdx][1]
		eB := b - palLin[bestIdx][2]

		// Area-weighted error diffusion: outgoing mass is eR*aSender;
		// each neighbor's color-domain shift = mass * adjacency_fraction
		// / aReceiver. Mass-preserving across the diffusion step (modulo
		// the documented stranded-cell drop). With uniform areas the
		// aSender/aReceiver factor reduces to 1.
		aSender := areas[idx]
		var totalWeight float32
		for _, nb := range neighbors[idx] {
			if !processed[nb.Idx] {
				totalWeight += nb.Weight
			}
		}
		if totalWeight > 0 {
			scale := 1.0 / totalWeight
			for _, nb := range neighbors[idx] {
				if !processed[nb.Idx] {
					w := nb.Weight * scale * (aSender / areas[nb.Idx])
					errBuf[nb.Idx][0] += eR * w
					errBuf[nb.Idx][1] += eG * w
					errBuf[nb.Idx][2] += eB * w
				}
			}
		}
		// When totalWeight == 0, all neighbors are already processed
		// and the residual error is dropped. ~9% of cells in the
		// random-order tail hit this on a typical 3D voxel surface,
		// producing a directionally-biased global chroma drift.
		// DitherCorrected addresses this by iterating dizzy with
		// pre-corrected inputs, converging on the correct global
		// average without changing the no-neighbor branch.
	}

	return assignments, nil
}

// regionObjective evaluates the local-solve objective for a candidate
// palette assignment over a region: Σ|r_i|² + λ · |Σ r_i|². The
// first term is per-cell quality (each cell wants its palette close
// to its target), the second is regional residual (the region's net
// rendered error wants to be small). λ = recoverQualityWeight.
// regionObjective works in LINEAR light: both targets and palLin are
// linear-light RGB. The regional-residual term |Σr_i|² is a diffusion
// mass-conservation quantity, which is only meaningful in linear space
// (the eye sums tiles linearly). λ is scale-invariant — both terms
// scale as |r|² — so recoverQualityWeight is unchanged from the sRGB
// era. See "Perceptual dithering color space".
func regionObjective(region []int, combo []int32, targets [][3]float32, palLin [][3]float32) float32 {
	var sumQ float32
	var sumR, sumG, sumB float32
	for i, ci := range region {
		T := targets[ci]
		p := palLin[combo[i]]
		rR := T[0] - p[0]
		rG := T[1] - p[1]
		rB := T[2] - p[2]
		sumQ += rR*rR + rG*rG + rB*rB
		sumR += rR
		sumG += rG
		sumB += rB
	}
	residSq := sumR*sumR + sumG*sumG + sumB*sumB
	return sumQ + recoverQualityWeight*residSq
}

// recoverQualityWeight balances per-cell quality against local
// regional residual in the local-solve objective:
//
//   combined = (2·L·s + |s|²) + λ · (2·r_N·s + |s|²)
//
// where:
//   - s = palette[old_p_N] - palette[p_N'] (the swap shift)
//   - L = e_C + Σ r_N: net rendered error of the local region
//     {stranded cell, all its neighbors}
//   - r_N = T_N - palette[old_p_N]: N's individual quality residual
//
// λ = 0: pure regional balance. Any quality cost at N is accepted
//        to reduce local residual.
// λ → ∞: pure per-cell quality. Swaps never hurt N's individual
//        fidelity even if it would help the regional balance.
// λ = 1: equal weight; swaps that hurt N's quality by the same
//        amount they help regional balance are rejected.
const recoverQualityWeight = 0.1

// DitherWithRecover is DitherWithNeighbors with a local-solve
// recovery pass when a cell is stranded (all 1-hop neighbors already
// processed). Instead of dropping the residual e_C, search across
// (neighbor N, candidate palette p_N') swaps for one that reduces
// the *local region's* net residual — purely local, no global state.
//
// Tracks per-cell target T_i = input + accumulated diffused error at
// processing time, so the swap evaluator knows each neighbor's
// individual quality residual r_N = T_N - palette[old_p_N]. The
// regional residual L = e_C + Σ r_N (over neighbors) is what the
// swap aims to reduce; r_N alone is the per-neighbor quality cost.
//
// Approximation caveats:
//
//   - We change assignments[N] but the residual N pushed to its own
//     neighbors at processing time was based on its OLD palette.
//     Changing N retroactively introduces a small ripple equal to
//     |s| × diffusion_weights, distributed across N's processed
//     neighbors. Bounded, second-order.
//
//   - Single-swap per stranded cell. A multi-cell joint optimization
//     would do better but scales as |palette|^k for k-cell regions;
//     single-swap is the cheap heuristic.
func DitherWithRecover(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)

	rng := rand.New(rand.NewSource(42))
	order := rng.Perm(n)

	assignments := make([]int32, n)
	errBuf := make([][3]float32, n)  // accumulated residual in linear light
	processed := make([]bool, n)
	targets := make([][3]float32, n) // per-cell target in linear light
	areas := effectiveAreas(cells)
	palLin, palLab := paletteLinearLab(pal)

	for oi, idx := range order {
		if oi%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", oi)
		}
		// Perceptual decision (CIELAB), linear-light residual — see
		// "Perceptual dithering color space" and DitherWithNeighbors.
		r := srgbToLinearLUT[cells[idx].Color[0]] + errBuf[idx][0]
		g := srgbToLinearLUT[cells[idx].Color[1]] + errBuf[idx][1]
		b := srgbToLinearLUT[cells[idx].Color[2]] + errBuf[idx][2]
		targets[idx] = [3]float32{r, g, b}

		tL, tA, tB := linearToLab(r, g, b)
		bestIdx := nearestPaletteLab(tL, tA, tB, palLab)
		assignments[idx] = int32(bestIdx)
		processed[idx] = true

		eR := r - palLin[bestIdx][0]
		eG := g - palLin[bestIdx][1]
		eB := b - palLin[bestIdx][2]

		// Area-weighted diffusion (see DitherWithNeighbors).
		aSender := areas[idx]
		var totalWeight float32
		for _, nb := range neighbors[idx] {
			if !processed[nb.Idx] {
				totalWeight += nb.Weight
			}
		}
		if totalWeight > 0 {
			scale := 1.0 / totalWeight
			for _, nb := range neighbors[idx] {
				if !processed[nb.Idx] {
					w := nb.Weight * scale * (aSender / areas[nb.Idx])
					errBuf[nb.Idx][0] += eR * w
					errBuf[nb.Idx][1] += eG * w
					errBuf[nb.Idx][2] += eB * w
				}
			}
			continue
		}
		// Stranded. Joint optimization over the region {C, k
		// nearest neighbors}: enumerate ALL palette combinations
		// for the region and pick the one minimizing
		//
		//   Σ|r_i|² + λ · |Σ r_i|²
		//
		// where r_i = T_i - palette[p_i] (per-cell residual). This
		// is the actual local solve — finds the joint minimum, not
		// a greedy descent that gets stuck. Crucially, joint search
		// can use combinations of small swaps in different
		// directions to absorb a residual that no single swap can
		// (each swap shift |s| is one palette gap, so single-swap
		// floors at |s|/2; joint can do better by combining shifts).
		//
		// Region size capped at recoverRegionSize to keep
		// enumeration tractable: |palette|^region per stranded
		// cell. With palette=4 and region=5 that's 1024 evals;
		// negligible total cost.
		const recoverRegionSize = 5
		region := make([]int, 0, recoverRegionSize)
		region = append(region, idx)
		for _, nb := range neighbors[idx] {
			if len(region) >= recoverRegionSize {
				break
			}
			region = append(region, nb.Idx)
		}
		regionLen := len(region)
		bestCombo := make([]int32, regionLen)
		for i, ci := range region {
			bestCombo[i] = assignments[ci]
		}
		bestObj := regionObjective(region, bestCombo, targets, palLin)
		combo := make([]int32, regionLen)
		var search func(depth int)
		search = func(depth int) {
			if depth == regionLen {
				obj := regionObjective(region, combo, targets, palLin)
				if obj < bestObj {
					bestObj = obj
					copy(bestCombo, combo)
				}
				return
			}
			for p := range pal {
				combo[depth] = int32(p)
				search(depth + 1)
			}
		}
		search(0)
		for i, ci := range region {
			assignments[ci] = bestCombo[i]
		}
	}

	return assignments, nil
}

// FloydSteinberg runs error-diffusion dithering with a deterministic
// (Grid, Layer, Row, Col) traversal order, distributing error only to
// "forward" neighbors (cells later in that order). Adapted from the
// classic 2D Floyd-Steinberg to the 3D voxel surface: instead of the
// fixed 4-cell kernel, error spreads to whatever forward neighbors
// each cell happens to have, weighted face/edge/corner = 1.0/0.1/0.01
// (same scheme as DitherWithNeighbors).
//
// Compared to dizzy: chroma fidelity is much higher because only
// genuinely-stranded cells (those whose active neighbors all sort
// earlier) lose error -- on a typical 3D scene that's a tiny fraction
// vs. dizzy's ~9% random-tail. The trade-off is directional structure
// in the output: error propagates "forward" along the scanline within
// each Z layer (Row/Col axes; Layer is slowest-varying), so the
// dither pattern shows the underlying traversal order rather than
// blue-noise texture.
//
// Like DitherWithNeighbors, the (cell + accumulated error) target is
// fed unclamped to the nearest-palette search.
func FloydSteinberg(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)

	// Deterministic traversal order: (Grid, Layer, Row, Col). Layer is
	// the slowest-varying axis so error flows "across the layer"
	// (along Row/Col within a single Z slice) rather than between
	// layers. The other reasonable choice would be a serpentine
	// scanline that reverses Col every Row to break up directional
	// banding, but the simple sort makes the FS/dizzy code-path
	// difference obvious.
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		ca, cb := cells[order[a]], cells[order[b]]
		if ca.Grid != cb.Grid {
			return ca.Grid < cb.Grid
		}
		if ca.Layer != cb.Layer {
			return ca.Layer < cb.Layer
		}
		if ca.Row != cb.Row {
			return ca.Row < cb.Row
		}
		return ca.Col < cb.Col
	})

	assignments := make([]int32, n)
	errBuf := make([][3]float32, n) // accumulated residual in linear light
	processed := make([]bool, n)
	areas := effectiveAreas(cells)
	palLin, palLab := paletteLinearLab(pal)

	for oi, idx := range order {
		if oi%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", oi)
		}
		// Perceptual decision (CIELAB), linear-light residual — see
		// "Perceptual dithering color space" and DitherWithNeighbors.
		r := srgbToLinearLUT[cells[idx].Color[0]] + errBuf[idx][0]
		g := srgbToLinearLUT[cells[idx].Color[1]] + errBuf[idx][1]
		b := srgbToLinearLUT[cells[idx].Color[2]] + errBuf[idx][2]

		tL, tA, tB := linearToLab(r, g, b)
		bestIdx := nearestPaletteLab(tL, tA, tB, palLab)
		assignments[idx] = int32(bestIdx)
		processed[idx] = true

		eR := r - palLin[bestIdx][0]
		eG := g - palLin[bestIdx][1]
		eB := b - palLin[bestIdx][2]

		// Forward neighbors: same predicate as dizzy. The only
		// algorithmic difference between the two functions is the
		// traversal order — random vs. (Grid, Layer, Row, Col).
		// Area weighting is identical to DitherWithNeighbors — see
		// that function for the mass-conservation rationale.
		aSender := areas[idx]
		var totalWeight float32
		for _, nb := range neighbors[idx] {
			if !processed[nb.Idx] {
				totalWeight += nb.Weight
			}
		}
		if totalWeight > 0 {
			scale := 1.0 / totalWeight
			for _, nb := range neighbors[idx] {
				if !processed[nb.Idx] {
					w := nb.Weight * scale * (aSender / areas[nb.Idx])
					errBuf[nb.Idx][0] += eR * w
					errBuf[nb.Idx][1] += eG * w
					errBuf[nb.Idx][2] += eB * w
				}
			}
		}
	}

	return assignments, nil
}

// RiemersmaInputBiasDefault is the default value for the per-cell
// adaptive input-bias maximum used in the Riemersma palette pick:
//
//   score = (1 - α) · dist²(target, palette) + α · dist²(input, palette)
//
// α is computed per-cell from the input's distance to the nearest
// palette color (see RiemersmaInputBiasRange). The fundamental
// trade-off the bias resolves:
//
//   - α = 0: pure Riemersma, zero average drift, chroma-balanced
//     by swinging palette to cancel accumulated error. Looks bad
//     on flat near-palette regions (grey hood: black/white
//     oscillation around grey instead of just picking grey).
//
//   - α = 1: pure snap, each cell goes to nearest-input palette.
//     Looks bad on textured surfaces (all bricks snap to the same
//     palette → posterized patches).
//
// Adaptive: high α when input is near a palette (snap dominates,
// kills oscillation), low α when input is between palettes (dither
// dominates, smooths gradients). One pass through the palette
// finds the cell's nearest-input distance; α is a linear ramp
// down from biasMax at distance 0 to 0 at distance
// RiemersmaInputBiasRange.
//
// 0.85 default is empirically the strongest snap-tendency that
// doesn't visibly posterize. The actual value used is configurable
// per Riemersma call (--riemersma-bias / Settings → Dither slider).
const RiemersmaInputBiasDefault = 0.85

// RiemersmaInputBiasRange is the input-distance (CIELAB ΔE) at which
// α drops to 0. Inputs farther than this from every palette are
// dithered with no input bias (pure Riemersma). Inputs at distance
// d ∈ [0, range] get α = RiemersmaInputBiasMax · (1 - d/range).
//
// 20 is the perceptual-metric recalibration of the historical 30-in-
// 8-bit-sRGB value: the dither now scores in CIELAB (see "Perceptual
// dithering color space"), where ~20 ΔE is roughly the old 30-RGB
// snap radius near mid-tones while keeping the snap region tighter
// than a 4-palette Voronoi half-radius. The exact value is a
// candidate for bench tuning; the user-facing --riemersma-bias slider
// scales the overall bias (biasMax) independently.
const RiemersmaInputBiasRange = 20.0

// RiemersmaWindowSize is the sliding-window length used by
// Riemersma — the number of past errors each cell sees, weighted by
// age. 16 is the value Riemersma's original 1998 description used
// and the typical setting in compuphase's reference implementation.
// Larger windows smear error over a wider region (less local
// fidelity); smaller windows behave closer to a deterministic FS-
// alike. 16 hits the usual blue-noise/detail balance.
const RiemersmaWindowSize = 16

// RiemersmaDecayRatio is the geometric decay between newest and
// oldest in-window errors: weight[k] = ratio^(k/(L-1)), with k=0
// being newest and k=L-1 being oldest. The 1/16 default matches
// Riemersma's recommendation: oldest entry contributes 1/16 of the
// newest, so the contribution from each cell's error fades out over
// the window without abrupt edges. Smaller ratios localize the
// effect more (closer to "no diffusion past a small neighborhood");
// larger ratios spread error wider (closer to a blue-noise mask).
const RiemersmaDecayRatio = 1.0 / 16.0

// Riemersma dithers cells by walking them along a locally-coherent
// tour through the neighbor graph and maintaining a sliding window
// of recent errors that diffuse into the current cell with
// exponentially decaying weights. Unlike Floyd-Steinberg's scanline
// schedule it has no axis-aligned scan direction, so flat areas
// don't band; unlike dizzy it has no stranded random tail, so global
// chroma is preserved by construction (every cell's error is
// integrated into subsequent cells, with steady-state DC gain 1).
//
// The tour is a greedy nearest-neighbor walk through the cell
// neighbor graph: at each step, move to the unvisited neighbor with
// smallest 3D distance from the current cell. On a dead end (no
// unvisited neighbors), jump to the globally nearest unvisited
// cell. For surface meshes, dead-end jumps are rare; the few that
// occur introduce a brief high-error transient that the window
// absorbs over the next L cells.
//
// Cost: O(N · L) for the dither, plus O(N · avg_degree) for the
// tour with O(N) extra work per dead end.
func Riemersma(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, biasMax float64, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}

	// Weights indexed by age (0 = newest, L-1 = oldest). Normalized
	// so a same-area window holds DC gain 1 (a constant residual e
	// replicated through the window returns e back through the
	// weighted average). With non-uniform per-cell areas the
	// consumption uses a mass-weighted average (Σ w·r·a / Σ w·a),
	// which preserves DC gain regardless of the area distribution
	// across slots.
	L := RiemersmaWindowSize
	weights := make([]float32, L)
	var total float32
	for k := 0; k < L; k++ {
		weights[k] = float32(math.Pow(RiemersmaDecayRatio, float64(k)/float64(L-1)))
		total += weights[k]
	}
	for k := range weights {
		weights[k] /= total
	}

	tour := buildRiemersmaTour(cells, neighbors)

	// Circular buffer storing per-slot (residual, sender_area).
	// head points at the slot that will be overwritten next (i.e.,
	// currently the oldest). Empty slots at start-up have area 0
	// and contribute nothing to either numerator or denominator of
	// the mass-weighted average.
	type slot struct {
		residual [3]float32
		area     float32
	}
	window := make([]slot, L)
	head := 0

	assigns := make([]int32, n)
	dI := make([]float32, len(pal))
	areas := effectiveAreas(cells)
	palLin, palLab := paletteLinearLab(pal)
	for ti, idx := range tour {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}

		// Mass-weighted average of in-window residuals (linear light):
		// each slot contributes (weight × area) of vote toward that
		// slot's residual color. Slot indexed by age: age 0 (newest)
		// lives at (head - 1 + L) % L, age k at (head - 1 - k + L) % L.
		var numR, numG, numB, den float32
		for k := 0; k < L; k++ {
			s := &window[(head+L-1-k)%L]
			wa := weights[k] * s.area
			numR += wa * s.residual[0]
			numG += wa * s.residual[1]
			numB += wa * s.residual[2]
			den += wa
		}
		var eR, eG, eB float32
		if den > 0 {
			eR = numR / den
			eG = numG / den
			eB = numB / den
		}

		// Input and error-shifted target in linear light; nearest-
		// palette decisions in CIELAB. See "Perceptual dithering
		// color space".
		iR := srgbToLinearLUT[cells[idx].Color[0]]
		iG := srgbToLinearLUT[cells[idx].Color[1]]
		iB := srgbToLinearLUT[cells[idx].Color[2]]
		iL, iA, iBb := linearToLab(iR, iG, iB)
		r := iR + eR
		g := iG + eG
		b := iB + eB
		tL, tA, tBb := linearToLab(r, g, b)

		// First pass: ΔE²(input, p) for each palette, plus min.
		// Second pass scores with α derived from the min-distance.
		// α is high when input is near a palette (snap suppresses
		// runaway oscillation in flat regions) and low when input
		// is between palettes (dither smooths textured gradients).
		var minDI float32 = math.MaxFloat32
		for pi := range palLab {
			dl := iL - palLab[pi][0]
			da := iA - palLab[pi][1]
			db := iBb - palLab[pi][2]
			d := dl*dl + da*da + db*db
			dI[pi] = d
			if d < minDI {
				minDI = d
			}
		}
		nearDist := float32(math.Sqrt(float64(minDI)))
		alpha := float32(biasMax) * (1 - nearDist/RiemersmaInputBiasRange)
		if alpha < 0 {
			alpha = 0
		}
		wt := 1 - alpha
		wi := alpha
		bestIdx := 0
		bestDist := float32(math.MaxFloat32)
		for pi := range palLab {
			dl := tL - palLab[pi][0]
			da := tA - palLab[pi][1]
			db := tBb - palLab[pi][2]
			dT := dl*dl + da*da + db*db
			d := wt*dT + wi*dI[pi]
			if d < bestDist {
				bestDist = d
				bestIdx = pi
			}
		}
		assigns[idx] = int32(bestIdx)

		window[head].residual[0] = r - palLin[bestIdx][0]
		window[head].residual[1] = g - palLin[bestIdx][1]
		window[head].residual[2] = b - palLin[bestIdx][2]
		window[head].area = areas[idx]
		head = (head + 1) % L
	}
	return assigns, nil
}

// RiemersmaPairCancellationDefault is the residual-cancellation
// coupling used by RiemersmaPair. λ ≈ 0.1 sits at the inflection of
// the wander-vs-fidelity curve in the bench: large enough to break
// up the same-direction-residual pattern that single-cell Riemersma
// produces on flat regions (see RiemersmaPair docs), small enough to
// barely move wander on near-palette images. Higher values (0.2–1.0)
// give larger wander wins on textured fixtures but visibly degrade
// fidelity on detailed near-palette images like the delorean fixture.
const RiemersmaPairCancellationDefault = 0.1

// RiemersmaPair is the *sliding* 2-cell variant of Riemersma — the
// production pick after benching disjoint vs sliding (sliding wins on
// drift on flat fixtures: 0.00 vs ≈0.16 dE at the same λ). The user-
// facing dither name "riemersma-pair" intentionally hides this detail.
// The disjoint variant lives in color_research.go as
// RiemersmaPairDisjoint, kept only as a bench-time A/B comparison.
//
// At every position along the tour the joint pair (cell i, cell i+1)
// is scored together, but only cell i's choice is committed; cell i+1
// is re-decided on the next iteration jointly with cell i+2. The joint
// score adds a residual-cancellation coupling
//
//	λ · ||(r0 - pal[a]) + (r1 - pal[b])||²
//
// to the per-cell biased-distance score, which penalises pairs whose
// two cells push residual in the same direction. With λ > 0 the solver
// prefers (black, white) over (black, black) for a flat-gray input,
// breaking the long-tour kick a same-direction pick would otherwise
// inject into the window.
//
// Versus single-cell Riemersma:
//   - Drift stays at single-cell levels on every tested fixture
//     (cancellation only redistributes residual within pairs; it
//     doesn't bias DC).
//   - Wander improves on textured/flat fixtures (≈2.7 dE drop on
//     bricks-style images at λ=0.1).
//   - On detailed near-palette images (e.g. delorean) wander
//     marginally regresses; that's the trade-off the λ knob picks.
//
// Cost: ≈2× single-cell Riemersma per cell (each cell is scored twice,
// once as left-of-pair and once as right-of-pair) plus a |pal|² inner
// loop for the joint search. With |pal| ≤ 16 the joint search adds
// constant overhead.
//
// Pass biasMax = RiemersmaInputBiasDefault to inherit the same near-
// palette input bias Riemersma uses.
func RiemersmaPair(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, lambda float32, biasMax float64, tracker progress.Tracker) ([]int32, error) {
	return riemersmaPairImpl(ctx, cells, pal, neighbors, biasMax, lambda, true, tracker)
}

// riemersmaPairImpl is the shared body for RiemersmaPair (production,
// slide=true) and RiemersmaPairDisjoint (research, slide=false).
//
//   - lambda: residual-cancellation coupling. 0 = pure separable score
//     (joint search degenerates to per-cell nearest); higher values
//     favour pairs whose residuals cancel.
//   - slide=false: tour advances by 2 per step (disjoint pairs); both
//     cells in the pair are committed together.
//   - slide=true: tour advances by 1 per step (sliding pair); only the
//     left cell is committed each step, so each cell is scored as both
//     the right of one pair and the left of the next.
func riemersmaPairImpl(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, biasMax float64, lambda float32, slide bool, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)
	if n == 0 {
		return nil, nil
	}

	L := RiemersmaWindowSize
	weights := make([]float32, L)
	var total float32
	for k := 0; k < L; k++ {
		weights[k] = float32(math.Pow(RiemersmaDecayRatio, float64(k)/float64(L-1)))
		total += weights[k]
	}
	for k := range weights {
		weights[k] /= total
	}

	tour := buildRiemersmaTour(cells, neighbors)
	type slot struct {
		residual [3]float32
		area     float32
	}
	window := make([]slot, L)
	head := 0

	assigns := make([]int32, n)
	dI0 := make([]float32, len(pal))
	dI1 := make([]float32, len(pal))
	s0 := make([]float32, len(pal))
	s1 := make([]float32, len(pal))
	res0 := make([][3]float32, len(pal))
	res1 := make([][3]float32, len(pal))
	areas := effectiveAreas(cells)
	palLin, palLab := paletteLinearLab(pal)

	step := 2
	if slide {
		step = 1
	}

	for ti := 0; ti < len(tour); ti += step {
		if ti%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", ti)
		}

		// Mass-weighted average of past residuals — the same
		// correction is applied to both cells of the joint pair (the
		// diffused error reflects past work, not the current cells'
		// areas). See Riemersma for the DC-gain rationale.
		var numR, numG, numB, den float32
		for k := 0; k < L; k++ {
			s := &window[(head+L-1-k)%L]
			wa := weights[k] * s.area
			numR += wa * s.residual[0]
			numG += wa * s.residual[1]
			numB += wa * s.residual[2]
			den += wa
		}
		var eR, eG, eB float32
		if den > 0 {
			eR = numR / den
			eG = numG / den
			eB = numB / den
		}

		idx0 := tour[ti]
		hasPartner := ti+1 < len(tour)
		var idx1 int
		if hasPartner {
			idx1 = tour[ti+1]
		}

		// Linear-light targets, CIELAB decision scores; the residual
		// vectors res0/res1 (and the window pushes + cancellation
		// coupling) stay in linear light. See "Perceptual dithering
		// color space".
		a0 := areas[idx0]
		i0R := srgbToLinearLUT[cells[idx0].Color[0]]
		i0G := srgbToLinearLUT[cells[idx0].Color[1]]
		i0B := srgbToLinearLUT[cells[idx0].Color[2]]
		i0L, i0A, i0Bb := linearToLab(i0R, i0G, i0B)
		r0R := i0R + eR
		r0G := i0G + eG
		r0B := i0B + eB
		t0L, t0A, t0Bb := linearToLab(r0R, r0G, r0B)

		var minDI0 float32 = math.MaxFloat32
		for pi := range palLab {
			dl := i0L - palLab[pi][0]
			da := i0A - palLab[pi][1]
			db := i0Bb - palLab[pi][2]
			d := dl*dl + da*da + db*db
			dI0[pi] = d
			if d < minDI0 {
				minDI0 = d
			}
		}
		nearDist0 := float32(math.Sqrt(float64(minDI0)))
		alpha0 := float32(biasMax) * (1 - nearDist0/RiemersmaInputBiasRange)
		if alpha0 < 0 {
			alpha0 = 0
		}
		wt0 := 1 - alpha0
		wi0 := alpha0

		// res0 holds the CIELAB residual (target − chosen) so the
		// joint cancellation term λ·||res0+res1||² is in the same ΔE²
		// units as s0/s1; the linear-light residual for the window
		// push is recomputed at commit time. (If res were linear it
		// would be ~10³× smaller than the Lab scores and λ would have
		// no effect.)
		for a := range palLab {
			dl := t0L - palLab[a][0]
			da := t0A - palLab[a][1]
			db := t0Bb - palLab[a][2]
			dT := dl*dl + da*da + db*db
			s0[a] = wt0*dT + wi0*dI0[a]
			res0[a][0] = dl
			res0[a][1] = da
			res0[a][2] = db
		}

		if !hasPartner {
			// Single-cell tail: pick cell 0 alone, push (residual, area).
			bestA := 0
			bestScore := s0[0]
			for a := 1; a < len(pal); a++ {
				if s0[a] < bestScore {
					bestScore = s0[a]
					bestA = a
				}
			}
			assigns[idx0] = int32(bestA)
			window[head].residual[0] = r0R - palLin[bestA][0]
			window[head].residual[1] = r0G - palLin[bestA][1]
			window[head].residual[2] = r0B - palLin[bestA][2]
			window[head].area = a0
			head = (head + 1) % L
			break
		}

		a1 := areas[idx1]
		i1R := srgbToLinearLUT[cells[idx1].Color[0]]
		i1G := srgbToLinearLUT[cells[idx1].Color[1]]
		i1B := srgbToLinearLUT[cells[idx1].Color[2]]
		i1L, i1A, i1Bb := linearToLab(i1R, i1G, i1B)
		r1R := i1R + eR
		r1G := i1G + eG
		r1B := i1B + eB
		t1L, t1A, t1Bb := linearToLab(r1R, r1G, r1B)

		var minDI1 float32 = math.MaxFloat32
		for pi := range palLab {
			dl := i1L - palLab[pi][0]
			da := i1A - palLab[pi][1]
			db := i1Bb - palLab[pi][2]
			d := dl*dl + da*da + db*db
			dI1[pi] = d
			if d < minDI1 {
				minDI1 = d
			}
		}
		nearDist1 := float32(math.Sqrt(float64(minDI1)))
		alpha1 := float32(biasMax) * (1 - nearDist1/RiemersmaInputBiasRange)
		if alpha1 < 0 {
			alpha1 = 0
		}
		wt1 := 1 - alpha1
		wi1 := alpha1

		for b := range palLab {
			dl := t1L - palLab[b][0]
			da := t1A - palLab[b][1]
			db := t1Bb - palLab[b][2]
			dT := dl*dl + da*da + db*db
			s1[b] = wt1*dT + wi1*dI1[b]
			res1[b][0] = dl
			res1[b][1] = da
			res1[b][2] = db
		}

		// Joint search: minimize s0[a] + s1[b] [+ λ·||r0_resid + r1_resid||²].
		// |pal| ≤ 16 in practice so |pal|² ≤ 256 — cheap.
		bestA, bestB := 0, 0
		bestScore := float32(math.MaxFloat32)
		for a := range pal {
			for b := range pal {
				ss := s0[a] + s1[b]
				if lambda > 0 {
					sr := res0[a][0] + res1[b][0]
					sg := res0[a][1] + res1[b][1]
					sb := res0[a][2] + res1[b][2]
					ss += lambda * (sr*sr + sg*sg + sb*sb)
				}
				if ss < bestScore {
					bestScore = ss
					bestA = a
					bestB = b
				}
			}
		}

		assigns[idx0] = int32(bestA)
		window[head].residual[0] = r0R - palLin[bestA][0]
		window[head].residual[1] = r0G - palLin[bestA][1]
		window[head].residual[2] = r0B - palLin[bestA][2]
		window[head].area = a0
		head = (head + 1) % L

		if !slide {
			// Disjoint-pair mode: also commit cell 1 and push its
			// residual. Sliding mode skips this — cell 1 will be
			// re-evaluated jointly with cell 2 on the next iteration.
			assigns[idx1] = int32(bestB)
			window[head].residual[0] = r1R - palLin[bestB][0]
			window[head].residual[1] = r1G - palLin[bestB][1]
			window[head].residual[2] = r1B - palLin[bestB][2]
			window[head].area = a1
			head = (head + 1) % L
		}
	}
	return assigns, nil
}

// buildRiemersmaTour produces a Hamiltonian-path-ish ordering of
// cells suitable for Riemersma. Starts at cell 0; at each step
// picks an unvisited neighbor uniformly at random (reservoir
// sampling, fixed seed). On a dead end (no unvisited neighbors),
// jumps to the nearest unvisited cell via the bucket-grid spatial
// index.
//
// Earlier revisions added a visited-count bias here (each
// unvisited neighbor's weight = 1 + (count of its own already-
// visited neighbors)) to fix a uniform-input cube test case where
// the 4 mutually-connected side faces showed a visible spatial-
// temporal correlation pattern. The bias kept the walk filling
// pockets densely. On real textured 3D models that produced a
// worse artifact: each densely-filled pocket got ~L=16 cells of
// window-correlated palette choices in a small spatial blob —
// visible clumps. Layer height interacted because it changes
// pocket sizes through cell connectivity.
//
// Reverting to uniform-random walk: the cube edge case may show
// minor structure on uniform input, but real-world textured
// inputs hide that and don't exhibit the dense-pocket clumping.
//
// The bucket-grid dead-end fallback stays nearest-by-distance —
// when jumping between disconnected regions we want the next
// region to be physically close, since the window will spend the
// next L cells averaging in errors from the old region.
func buildRiemersmaTour(cells []ActiveCell, neighbors [][]Neighbor) []int {
	n := len(cells)
	visited := make([]bool, n)
	tour := make([]int, 0, n)
	rng := rand.New(rand.NewSource(42))

	grid := newCellBucketGrid(cells)

	cur := 0
	visited[cur] = true
	tour = append(tour, cur)
	grid.markVisited(cur)
	for len(tour) < n {
		// Reservoir-sample one unvisited neighbor uniformly at
		// random in a single pass: each candidate replaces the
		// current pick with probability 1/k where k is the count
		// of candidates seen so far.
		pick := -1
		count := 0
		for _, nb := range neighbors[cur] {
			if visited[nb.Idx] {
				continue
			}
			count++
			if rng.Intn(count) == 0 {
				pick = nb.Idx
			}
		}
		if pick >= 0 {
			visited[pick] = true
			tour = append(tour, pick)
			grid.markVisited(pick)
			cur = pick
			continue
		}
		next := grid.nearestUnvisited(cur, cells, visited)
		visited[next] = true
		tour = append(tour, next)
		grid.markVisited(next)
		cur = next
	}
	return tour
}

// cellBucketGrid is a sparse 3D bucket grid over cell positions.
// Each bucket holds the indices of unvisited cells that fall within
// it; markVisited removes a cell index from its bucket. The grid is
// sized so the average bucket holds ~32 cells (cuberoot scaling),
// which keeps both per-step expansion shells small and bookkeeping
// overhead bounded.
type cellBucketGrid struct {
	minX, minY, minZ float32
	stepX, stepY, stepZ float32
	nx, ny, nz int
	buckets map[int][]int
	cellBucket []int // per-cell linear bucket index
}

func newCellBucketGrid(cells []ActiveCell) *cellBucketGrid {
	n := len(cells)
	if n == 0 {
		return &cellBucketGrid{buckets: map[int][]int{}}
	}
	minX, minY, minZ := cells[0].Cx, cells[0].Cy, cells[0].Cz
	maxX, maxY, maxZ := minX, minY, minZ
	for i := 1; i < n; i++ {
		c := cells[i]
		if c.Cx < minX {
			minX = c.Cx
		} else if c.Cx > maxX {
			maxX = c.Cx
		}
		if c.Cy < minY {
			minY = c.Cy
		} else if c.Cy > maxY {
			maxY = c.Cy
		}
		if c.Cz < minZ {
			minZ = c.Cz
		} else if c.Cz > maxZ {
			maxZ = c.Cz
		}
	}
	rangeX := maxX - minX
	rangeY := maxY - minY
	rangeZ := maxZ - minZ
	// Target ~32 cells per bucket.
	side := int(math.Round(math.Cbrt(float64(n) / 32.0)))
	if side < 1 {
		side = 1
	}
	nx, ny, nz := side, side, side
	stepX := rangeX / float32(nx)
	stepY := rangeY / float32(ny)
	stepZ := rangeZ / float32(nz)
	if stepX <= 0 {
		stepX = 1
	}
	if stepY <= 0 {
		stepY = 1
	}
	if stepZ <= 0 {
		stepZ = 1
	}
	g := &cellBucketGrid{
		minX: minX, minY: minY, minZ: minZ,
		stepX: stepX, stepY: stepY, stepZ: stepZ,
		nx: nx, ny: ny, nz: nz,
		buckets:    make(map[int][]int, nx*ny*nz),
		cellBucket: make([]int, n),
	}
	for i, c := range cells {
		bx, by, bz := g.bucketIdx(c.Cx, c.Cy, c.Cz)
		key := g.linearKey(bx, by, bz)
		g.buckets[key] = append(g.buckets[key], i)
		g.cellBucket[i] = key
	}
	return g
}

func (g *cellBucketGrid) bucketIdx(x, y, z float32) (int, int, int) {
	bx := int((x - g.minX) / g.stepX)
	by := int((y - g.minY) / g.stepY)
	bz := int((z - g.minZ) / g.stepZ)
	if bx < 0 {
		bx = 0
	} else if bx >= g.nx {
		bx = g.nx - 1
	}
	if by < 0 {
		by = 0
	} else if by >= g.ny {
		by = g.ny - 1
	}
	if bz < 0 {
		bz = 0
	} else if bz >= g.nz {
		bz = g.nz - 1
	}
	return bx, by, bz
}

func (g *cellBucketGrid) linearKey(bx, by, bz int) int {
	return (bz*g.ny+by)*g.nx + bx
}

func (g *cellBucketGrid) markVisited(cellIdx int) {
	key := g.cellBucket[cellIdx]
	bucket := g.buckets[key]
	for i, idx := range bucket {
		if idx == cellIdx {
			bucket[i] = bucket[len(bucket)-1]
			bucket = bucket[:len(bucket)-1]
			if len(bucket) == 0 {
				delete(g.buckets, key)
			} else {
				g.buckets[key] = bucket
			}
			return
		}
	}
}

func (g *cellBucketGrid) nearestUnvisited(curIdx int, cells []ActiveCell, visited []bool) int {
	cur := cells[curIdx]
	cbx, cby, cbz := g.bucketIdx(cur.Cx, cur.Cy, cur.Cz)
	best := -1
	bestD := float32(math.MaxFloat32)
	// Expand in shells until at least one candidate is found AND
	// the next shell can't possibly contain a closer cell. The
	// minimum-possible distance to a cell in a shell of radius r
	// (in bucket units) is (r-1) * minStep, so we stop expanding
	// once bestD < ((r-1) * minStep)^2.
	minStep := g.stepX
	if g.stepY < minStep {
		minStep = g.stepY
	}
	if g.stepZ < minStep {
		minStep = g.stepZ
	}
	maxRadius := g.nx
	if g.ny > maxRadius {
		maxRadius = g.ny
	}
	if g.nz > maxRadius {
		maxRadius = g.nz
	}
	for r := 0; r <= maxRadius; r++ {
		// Iterate the surface of the shell at radius r, skipping
		// the interior we already covered at smaller radii.
		x0, x1 := cbx-r, cbx+r
		y0, y1 := cby-r, cby+r
		z0, z1 := cbz-r, cbz+r
		if x0 < 0 {
			x0 = 0
		}
		if y0 < 0 {
			y0 = 0
		}
		if z0 < 0 {
			z0 = 0
		}
		if x1 >= g.nx {
			x1 = g.nx - 1
		}
		if y1 >= g.ny {
			y1 = g.ny - 1
		}
		if z1 >= g.nz {
			z1 = g.nz - 1
		}
		for bz := z0; bz <= z1; bz++ {
			for by := y0; by <= y1; by++ {
				for bx := x0; bx <= x1; bx++ {
					if r > 0 && bx > cbx-r && bx < cbx+r && by > cby-r && by < cby+r && bz > cbz-r && bz < cbz+r {
						continue
					}
					bucket := g.buckets[g.linearKey(bx, by, bz)]
					for _, idx := range bucket {
						if visited[idx] {
							continue
						}
						dx := cur.Cx - cells[idx].Cx
						dy := cur.Cy - cells[idx].Cy
						dz := cur.Cz - cells[idx].Cz
						d := dx*dx + dy*dy + dz*dz
						if d < bestD {
							bestD = d
							best = idx
						}
					}
				}
			}
		}
		if best >= 0 {
			// We have a candidate. Continue expanding only if the
			// next shell could plausibly contain a closer cell.
			minPossibleDist := float32(r) * minStep
			if minPossibleDist*minPossibleDist >= bestD {
				return best
			}
		}
	}
	return best
}

// DizzyCorrectionPasses is the number of dizzy iterations
// DitherCorrected runs. Pass 1 measures the algorithm's natural
// drift; each subsequent pass shifts the cell inputs by the
// accumulated -drift correction and measures the residual. With
// 3 passes empirically: bricks_benchy drift 8.81 -> 7.94 -> 7.5
// -> 7.4 (diminishing returns; dizzy's bias on bricks isn't
// translation-invariant enough for the correction to fully
// converge). On earth/pheasant 3 passes gets within 1 ΔE of zero.
//
// Exported so the pipeline can size its progress bar to cover all
// the passes (one pass's worth of work × this many).
const DizzyCorrectionPasses = 3

// DitherCorrected iteratively runs dizzy with input pre-correction
// to converge on near-zero global drift. Pass 1 is standard dizzy
// to measure the algorithm's drift; each subsequent pass shifts
// every cell's input by the accumulated -drift and runs dizzy
// again on the shifted inputs.
//
// Hypothesis: dizzy's drift on a given (input distribution ×
// palette geometry) is approximately translation-invariant — so
// pre-correcting the input by the measured drift makes the next
// pass's output average closer to the original input average,
// even though dizzy's stranded-tail loss per pass is unchanged.
//
// Math sketch: avg_input = X. Pass 1 output averages X + D1. Pass
// 2 input is X - D1, output averages (X - D1) + D2 where D2 is
// dizzy's bias on the shifted input. If D2 ≈ D1 (translation-
// invariant), residual = -D1 + D2 ≈ 0. In practice D2 ≠ D1 on
// some scenes (bricks_benchy: D2 ≈ 0.9·D1) so the correction is
// partial; iterating compounds the correction.
//
// Cost: DizzyCorrectionPasses × a single dizzy pass.
//
// The shifted cell colors are clamped to [0, 255] (uint8
// limitation). For input averages well away from the boundaries
// (typical for real models) saturation loss is negligible. If
// shifts grew large enough to push many cells through saturation,
// the correction would degrade — none of our fixtures hit that.
func DitherCorrected(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, tracker progress.Tracker) ([]int32, error) {
	// Empty input: no work, no division by zero downstream.
	if len(cells) == 0 {
		return nil, nil
	}

	// All averaging/correction here is in LINEAR light, the space the
	// inner DitherWithNeighbors now conserves (see "Perceptual
	// dithering color space"). palLin maps each chosen index to its
	// linear-light color for the output mean.
	palLin, _ := paletteLinearLab(pal)

	// Compute the static input average once; it doesn't change
	// across passes (the SHIFTED inputs do, but the drift we measure
	// each pass is relative to the ORIGINAL input).
	var iR, iG, iB float64
	for i := range cells {
		iR += float64(srgbToLinearLUT[cells[i].Color[0]])
		iG += float64(srgbToLinearLUT[cells[i].Color[1]])
		iB += float64(srgbToLinearLUT[cells[i].Color[2]])
	}
	n := float64(len(cells))
	iR /= n
	iG /= n
	iB /= n

	// Working copy of cells whose colors get shifted between passes.
	// Pass 1 uses the originals (zero correction).
	shifted := make([]ActiveCell, len(cells))
	copy(shifted, cells)

	// cumulative correction applied to inputs across passes.
	var cR, cG, cB float64

	if tracker == nil {
		tracker = progress.NullTracker{}
	}

	// Best-so-far tracking: each pass's drift is checked against the
	// best previous drift, and if a new pass made things WORSE we
	// return the previous best instead. The iterative correction
	// usually converges, but on some inputs (e.g., bench's
	// checkerboard fixture) the second pass's correction overshoots
	// and increases drift; without monotone non-regression we'd
	// ship the worse result. Keep a copy of the best assignments
	// because subsequent passes overwrite the live `assigns` slice.
	var assigns []int32
	bestAssigns := make([]int32, len(cells))
	bestDriftDE := math.Inf(1)
	for pass := 0; pass < DizzyCorrectionPasses; pass++ {
		// Wrap the tracker so this pass's per-cell progress (which
		// reports oi in [0, n)) lands on the outer bar at the right
		// offset for a single continuous K*n unit of work.
		passTracker := ditherPassTracker{real: tracker, offset: pass * len(cells)}
		var err error
		assigns, err = DitherWithNeighbors(ctx, shifted, pal, neighbors, passTracker)
		if err != nil {
			return nil, err
		}

		// Measure drift relative to ORIGINAL input (linear light).
		var oR, oG, oB float64
		for _, a := range assigns {
			oR += float64(palLin[a][0])
			oG += float64(palLin[a][1])
			oB += float64(palLin[a][2])
		}
		oR /= n
		oG /= n
		oB /= n
		driftDE := computeDriftDEFromAvg(iR, iG, iB, oR, oG, oB)

		if driftDE < bestDriftDE {
			bestDriftDE = driftDE
			copy(bestAssigns, assigns)
		} else {
			// This pass regressed. Stop and return the previous best.
			return bestAssigns, nil
		}

		if pass == DizzyCorrectionPasses-1 {
			break
		}

		// Roll the per-channel drift into the cumulative correction;
		// shift the next pass's inputs by the new total. The shift is
		// applied in linear light, then re-encoded to the uint8 sRGB
		// ActiveCell.Color the inner pass re-linearizes.
		cR += oR - iR
		cG += oG - iG
		cB += oB - iB
		for i := range shifted {
			shifted[i].Color[0] = linearToSrgbByte(float32(float64(srgbToLinearLUT[cells[i].Color[0]]) - cR))
			shifted[i].Color[1] = linearToSrgbByte(float32(float64(srgbToLinearLUT[cells[i].Color[1]]) - cG))
			shifted[i].Color[2] = linearToSrgbByte(float32(float64(srgbToLinearLUT[cells[i].Color[2]]) - cB))
		}
	}
	return bestAssigns, nil
}

// computeDriftDEFromAvg returns the Lab ΔE between two pre-computed
// average colors given as LINEAR-light scalars in [0, 1]. The inner
// dither now conserves the linear-light spatial average (see
// "Perceptual dithering color space"), so drift must be measured in
// that same space — measuring the sRGB-byte mean instead would report
// a gamma-curvature offset (mean of a nonlinear map ≠ map of the mean)
// as phantom drift and make the correction chase it.
func computeDriftDEFromAvg(iR, iG, iB, oR, oG, oB float64) float64 {
	iL, iA, iBl := linearToLab(float32(iR), float32(iG), float32(iB))
	oL, oA, oBl := linearToLab(float32(oR), float32(oG), float32(oB))
	dL, dA, dB := float64(iL-oL), float64(iA-oA), float64(iBl-oBl)
	return math.Sqrt(dL*dL + dA*dA + dB*dB)
}

// ditherPassTracker shifts a child dither pass's incremental
// progress reports by a fixed offset so a sequence of passes
// appears to the underlying tracker as one continuous run. The
// outer pipeline opens / closes the stage; this wrapper only
// translates the per-pass StageProgress numbers and forwards
// warnings.
//
// DitherWithNeighbors emits StageProgress("Dithering", oi) where
// oi is the current cell index in [0, n). DitherCorrected drives
// it K times; without the wrapper the bar would rewind to 0 at
// the start of each pass, then jump back near the end. With the
// wrapper, pass k's progress maps to [k*n, (k+1)*n) on the bar.
type ditherPassTracker struct {
	real   progress.Tracker
	offset int
}

// StageStart and StageDone are no-ops because the outer caller
// (the pipeline's Dither stage) owns the stage lifecycle: it opens
// the stage once, runs all K passes through this wrapper, and
// closes the stage. Forwarding StageStart/StageDone from a sub-pass
// would re-open or close the outer bar mid-run.
func (ditherPassTracker) StageStart(string, bool, int) {}
func (ditherPassTracker) StageDone(string)             {}
func (t ditherPassTracker) Warn(kind, s string)        { t.real.Warn(kind, s) }
func (t ditherPassTracker) StageProgress(stage string, current int) {
	t.real.StageProgress(stage, t.offset+current)
}

// SnapColors moves each cell's color toward its nearest palette color by up
// to deltaE units (standard CIE76 ΔE) in CIELAB space. If the cell is
// already closer than deltaE, it snaps to the palette color exactly.
func SnapColors(ctx context.Context, cells []ActiveCell, pal [][3]uint8, deltaE float64) error {
	// go-colorful uses Lab values scaled by 1/100 relative to standard CIELAB,
	// so distances are also 1/100 of standard ΔE.
	scaledDE := deltaE / 100.0

	// Convert palette to Lab.
	palLab := make([][3]float64, len(pal))
	for i, p := range pal {
		c := colorful.Color{
			R: float64(p[0]) / 255.0,
			G: float64(p[1]) / 255.0,
			B: float64(p[2]) / 255.0,
		}
		palLab[i][0], palLab[i][1], palLab[i][2] = c.Lab()
	}

	for i := range cells {
		if i%1000 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		cc := cells[i].Color
		c := colorful.Color{
			R: float64(cc[0]) / 255.0,
			G: float64(cc[1]) / 255.0,
			B: float64(cc[2]) / 255.0,
		}
		cL, cA, cB := c.Lab()

		// Find nearest palette color in Lab.
		bestIdx := 0
		bestDist := math.MaxFloat64
		for pi, pl := range palLab {
			dL := cL - pl[0]
			dA := cA - pl[1]
			dB := cB - pl[2]
			d := math.Sqrt(dL*dL + dA*dA + dB*dB)
			if d < bestDist {
				bestDist = d
				bestIdx = pi
			}
		}

		if bestDist <= scaledDE {
			cells[i].Color = pal[bestIdx]
		} else {
			t := scaledDE / bestDist
			nL := cL + t*(palLab[bestIdx][0]-cL)
			nA := cA + t*(palLab[bestIdx][1]-cA)
			nB := cB + t*(palLab[bestIdx][2]-cB)
			nc := colorful.Lab(nL, nA, nB).Clamped()
			cells[i].Color = [3]uint8{
				uint8(math.Round(nc.R * 255)),
				uint8(math.Round(nc.G * 255)),
				uint8(math.Round(nc.B * 255)),
			}
		}
	}
	return nil
}

// AssignColors assigns palette indices without dithering.
func AssignColors(ctx context.Context, cells []ActiveCell, pal [][3]uint8) ([]int32, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	colors := make([][3]uint8, len(cells))
	for i, c := range cells {
		colors[i] = c.Color
	}
	return palette.AssignPalette(colors, pal), nil
}
