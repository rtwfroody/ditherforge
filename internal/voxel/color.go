package voxel

import (
	"context"
	"fmt"
	"image"
	"math"
	"math/rand"
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

	if len(pcfg.Inventory) > 0 {
		filtered := filterInventory(pcfg.Inventory, pcfg.Locked)
		if len(filtered) == 0 {
			return nil, nil, "", fmt.Errorf("inventory has no colors left after excluding locked colors")
		}
		selected, err := palette.SelectFromInventory(ctx, cellColors, filtered, remaining, lockedColors, dithering, tracker)
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

// SampleNearestColor finds the closest surface point to p on `model`, then
// samples the texture color and alpha there. If decals are provided,
// sticker textures are composited over the base color. Returns RGBA.
//
// When stickers live on a *different* mesh than the base color sample
// model (alpha-wrap mode: original mesh carries texture/UV, wrap mesh
// carries decals), call SampleNearestColorWithSticker instead.
func SampleNearestColor(p [3]float32, model *loader.LoadedModel, si *SpatialIndex, radius float32, buf *SearchBuf, decals []*StickerDecal) [4]uint8 {
	return SampleNearestColorWithSticker(p, model, si, radius, buf, decals, nil, nil, nil)
}

// SampleNearestColorWithSticker is the two-mesh form of SampleNearestColor.
// When stickerModel is nil (or aliases model), behavior is identical to
// SampleNearestColor: a single nearest-tri lookup against `model` is used
// for both base color and sticker compositing. When stickerModel is a
// distinct mesh, a second nearest-tri lookup against stickerModel/stickerSI
// is performed and decals are composited based on that result. stickerBuf
// must be a separate SearchBuf sized for stickerModel; passing nil reuses
// `buf` (safe because the two lookups don't overlap in time).
func SampleNearestColorWithSticker(
	p [3]float32,
	model *loader.LoadedModel, si *SpatialIndex, radius float32, buf *SearchBuf,
	decals []*StickerDecal,
	stickerModel *loader.LoadedModel, stickerSI *SpatialIndex, stickerBuf *SearchBuf,
) [4]uint8 {
	cands := si.CandidatesRadiusZ(p[0], p[1], radius, p[2], radius, buf)
	bestDistSq := float32(math.MaxFloat32)
	bestTri := int32(-1)
	var bestS, bestT float32
	for _, ti := range cands {
		f := model.Faces[ti]
		r := ClosestPointOnTriangle(p, model.Vertices[f[0]], model.Vertices[f[1]], model.Vertices[f[2]])
		if r.DistSq < bestDistSq {
			bestDistSq = r.DistSq
			bestTri = ti
			bestS = r.S
			bestT = r.T
		}
	}

	if bestTri < 0 {
		return [4]uint8{128, 128, 128, 255}
	}

	matAlpha, bc, texIdx := faceMaterial(int(bestTri), model)
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

// DitherCellsDizzy applies dizzy dithering: random traversal order with
// error diffusion to actual spatial neighbors. Produces blue-noise-like
// results without directional bias.
func DitherCellsDizzy(ctx context.Context, cells []ActiveCell, pal [][3]uint8) ([]int32, error) {
	return DitherWithNeighbors(ctx, cells, pal, BuildNeighbors(cells), nil)
}

// DitherWithNeighbors runs dizzy dithering using a precomputed neighbor table.
// If tracker is non-nil, emits StageProgress("Dithering", current) every 1000
// cells. Caller owns StageStart/StageDone.
func DitherWithNeighbors(ctx context.Context, cells []ActiveCell, pal [][3]uint8, neighbors [][]Neighbor, tracker progress.Tracker) ([]int32, error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}
	n := len(cells)

	rng := rand.New(rand.NewSource(42))
	order := rng.Perm(n)

	assignments := make([]int32, n)
	errBuf := make([][3]float32, n)
	processed := make([]bool, n)

	for oi, idx := range order {
		if oi%1000 == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			tracker.StageProgress("Dithering", oi)
		}
		r := ClampF(float32(cells[idx].Color[0])+errBuf[idx][0], 0, 255)
		g := ClampF(float32(cells[idx].Color[1])+errBuf[idx][1], 0, 255)
		b := ClampF(float32(cells[idx].Color[2])+errBuf[idx][2], 0, 255)

		bestIdx := 0
		bestDist := float32(math.MaxFloat32)
		for pi, p := range pal {
			dr := r - float32(p[0])
			dg := g - float32(p[1])
			db := b - float32(p[2])
			d := dr*dr + dg*dg + db*db
			if d < bestDist {
				bestDist = d
				bestIdx = pi
			}
		}
		assignments[idx] = int32(bestIdx)
		processed[idx] = true

		chosen := pal[bestIdx]
		eR := r - float32(chosen[0])
		eG := g - float32(chosen[1])
		eB := b - float32(chosen[2])

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
					w := nb.Weight * scale
					errBuf[nb.Idx][0] += eR * w
					errBuf[nb.Idx][1] += eG * w
					errBuf[nb.Idx][2] += eB * w
				}
			}
		}
	}

	return assignments, nil
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
