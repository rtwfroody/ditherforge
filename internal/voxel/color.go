package voxel

import (
	"fmt"
	"image"
	"math"
	"math/rand"
	"strings"

	colorful "github.com/lucasb-eyer/go-colorful"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/palette"
)

// ResolvePalette determines the final palette from cells and config.
// dithering indicates whether dithering will be used, which affects
// inventory color selection strategy.
// Returns the palette RGB values and a display string for logging.
func ResolvePalette(cells []ActiveCell, pcfg PaletteConfig, dithering bool) ([][3]uint8, string) {
	if pcfg.Palette != nil {
		return pcfg.Palette, ""
	}

	cellColors := make([][3]uint8, len(cells))
	for i, c := range cells {
		cellColors[i] = c.Color
	}

	if len(pcfg.Inventory) > 0 {
		fmt.Printf("  Selecting %d colors from %d-color inventory...", pcfg.InventoryN, len(pcfg.Inventory))
		selected := palette.SelectFromInventory(cellColors, pcfg.Inventory, pcfg.InventoryN, dithering)
		pal := make([][3]uint8, len(selected))
		strs := make([]string, len(selected))
		for i, e := range selected {
			pal[i] = e.Color
			s := fmt.Sprintf("#%02X%02X%02X", e.Color[0], e.Color[1], e.Color[2])
			if e.Label != "" {
				s += " (" + e.Label + ")"
			}
			strs[i] = s
		}
		display := " " + strings.Join(strs, ", ")
		return pal, display
	}

	if pcfg.AutoPaletteN > 0 {
		fmt.Printf("  Computing %d-color palette from cell colors...", pcfg.AutoPaletteN)
		pal := palette.ComputePalette(cellColors, pcfg.AutoPaletteN)
		strs := make([]string, len(pal))
		for i, p := range pal {
			strs[i] = fmt.Sprintf("#%02X%02X%02X", p[0], p[1], p[2])
		}
		display := " " + strings.Join(strs, ", ")
		return pal, display
	}

	return nil, ""
}

// BilinearSample samples a texture at normalized UV coordinates.
// Returns RGBA; alpha is 255 for textures without transparency.
func BilinearSample(img image.Image, u, v float32) [4]uint8 {
	bounds := img.Bounds()
	W := float32(bounds.Max.X - bounds.Min.X)
	H := float32(bounds.Max.Y - bounds.Min.Y)

	u = u - float32(math.Floor(float64(u)))
	v = v - float32(math.Floor(float64(v)))

	px := u * (W - 1)
	py := v * (H - 1)

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

	x0 += bounds.Min.X
	y0 += bounds.Min.Y
	x1 += bounds.Min.X
	y1 += bounds.Min.Y

	sample := func(x, y int) (float32, float32, float32, float32) {
		r, g, b, a := img.At(x, y).RGBA()
		return float32(r >> 8), float32(g >> 8), float32(b >> 8), float32(a >> 8)
	}

	r00, g00, b00, a00 := sample(x0, y0)
	r10, g10, b10, a10 := sample(x1, y0)
	r01, g01, b01, a01 := sample(x0, y1)
	r11, g11, b11, a11 := sample(x1, y1)

	lerp := func(a, b, c, d, fx, fy float32) uint8 {
		v := a*(1-fx)*(1-fy) + b*fx*(1-fy) + c*(1-fx)*fy + d*fx*fy
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		return uint8(v + 0.5)
	}

	return [4]uint8{
		lerp(r00, r10, r01, r11, fx, fy),
		lerp(g00, g10, g01, g11, fx, fy),
		lerp(b00, b10, b01, b11, fx, fy),
		lerp(a00, a10, a01, a11, fx, fy),
	}
}

// SampleNearestColor finds the closest surface point to p, then samples the
// texture color and alpha there. Returns RGBA.
func SampleNearestColor(p [3]float32, model *loader.LoadedModel, si *SpatialIndex, radius float32, buf *SearchBuf) [4]uint8 {
	cands := si.CandidatesRadiusZ(p[0], p[1], radius, p[2], radius, buf)
	bestDistSq := float32(math.MaxFloat32)
	bestTri := int32(-1)
	var bestS, bestT float32
	for _, ti := range cands {
		f := model.Faces[ti]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]

		e0 := [3]float32{v1[0] - v0[0], v1[1] - v0[1], v1[2] - v0[2]}
		e1 := [3]float32{v2[0] - v0[0], v2[1] - v0[1], v2[2] - v0[2]}
		d := [3]float32{v0[0] - p[0], v0[1] - p[1], v0[2] - p[2]}

		a := Dot3(e0, e0)
		b := Dot3(e0, e1)
		c := Dot3(e1, e1)
		dd := Dot3(e0, d)
		e := Dot3(e1, d)

		det := a*c - b*b
		s := b*e - c*dd
		t := b*dd - a*e

		if s+t <= det {
			if s < 0 {
				if t < 0 {
					if dd < 0 {
						t = 0
						s = ClampF(-dd/a, 0, 1)
					} else {
						s = 0
						t = ClampF(-e/c, 0, 1)
					}
				} else {
					s = 0
					t = ClampF(-e/c, 0, 1)
				}
			} else if t < 0 {
				t = 0
				s = ClampF(-dd/a, 0, 1)
			} else {
				invDet := 1.0 / det
				s *= invDet
				t *= invDet
			}
		} else {
			if s < 0 {
				tmp0 := b + dd
				tmp1 := c + e
				if tmp1 > tmp0 {
					numer := tmp1 - tmp0
					denom := a - 2*b + c
					s = ClampF(numer/denom, 0, 1)
					t = 1 - s
				} else {
					s = 0
					t = ClampF(-e/c, 0, 1)
				}
			} else if t < 0 {
				tmp0 := b + e
				tmp1 := a + dd
				if tmp1 > tmp0 {
					numer := tmp1 - tmp0
					denom := a - 2*b + c
					t = ClampF(numer/denom, 0, 1)
					s = 1 - t
				} else {
					t = 0
					s = ClampF(-dd/a, 0, 1)
				}
			} else {
				numer := (c + e) - (b + dd)
				if numer <= 0 {
					s = 0
				} else {
					denom := a - 2*b + c
					s = ClampF(numer/denom, 0, 1)
				}
				t = 1 - s
			}
		}

		dx := d[0] + s*e0[0] + t*e1[0]
		dy := d[1] + s*e0[1] + t*e1[1]
		dz := d[2] + s*e0[2] + t*e1[2]
		distSq := dx*dx + dy*dy + dz*dz
		if distSq < bestDistSq {
			bestDistSq = distSq
			bestTri = ti
			bestS = s
			bestT = t
		}
	}

	if bestTri < 0 {
		return [4]uint8{128, 128, 128, 255}
	}

	// Material-level alpha (from AlphaMode + BaseColorFactor).
	matAlpha := float32(1.0)
	if model.FaceAlpha != nil {
		matAlpha = model.FaceAlpha[bestTri]
	}

	// Get material base color.
	bc := [4]uint8{255, 255, 255, 255}
	if model.FaceBaseColor != nil {
		bc = model.FaceBaseColor[bestTri]
	}

	texIdx := model.FaceTextureIdx[bestTri]
	if texIdx < 0 || int(texIdx) >= len(model.Textures) {
		a := uint8(ClampF(matAlpha*float32(bc[3])+0.5, 0, 255))
		return [4]uint8{bc[0], bc[1], bc[2], a}
	}

	bary := [3]float32{1 - bestS - bestT, bestS, bestT}
	f := model.Faces[bestTri]
	uv0 := model.UVs[f[0]]
	uv1 := model.UVs[f[1]]
	uv2 := model.UVs[f[2]]

	u := bary[0]*uv0[0] + bary[1]*uv1[0] + bary[2]*uv2[0]
	v := bary[0]*uv0[1] + bary[1]*uv1[1] + bary[2]*uv2[1]

	rgba := BilinearSample(model.Textures[texIdx], u, v)
	// Alpha-blend texture sample over material base color.
	texA := float32(rgba[3]) / 255
	rgba[0] = uint8(float32(rgba[0])*texA + float32(bc[0])*(1-texA))
	rgba[1] = uint8(float32(rgba[1])*texA + float32(bc[1])*(1-texA))
	rgba[2] = uint8(float32(rgba[2])*texA + float32(bc[2])*(1-texA))
	rgba[3] = bc[3] // use base color alpha (texture alpha already composited)
	// Multiply with material alpha.
	if matAlpha < 1.0 {
		combined := float32(rgba[3]) * matAlpha
		rgba[3] = uint8(ClampF(combined+0.5, 0, 255))
	}
	return rgba
}

// neighbor holds a precomputed neighbor reference with its diffusion weight.
type neighbor struct {
	idx    int
	weight float32
}

// DitherCellsDizzy applies dizzy dithering: random traversal order with
// error diffusion to actual spatial neighbors. Produces blue-noise-like
// results without directional bias.
func DitherCellsDizzy(cells []ActiveCell, pal [][3]uint8) []int32 {
	n := len(cells)

	// Build cell lookup map.
	cellMap := make(map[CellKey]int, n)
	for i, c := range cells {
		cellMap[CellKey{c.Col, c.Row, c.Layer}] = i
	}

	// Precompute neighbor lists with weights.
	// Face-adjacent (1 axis differs): weight 1.0
	// Edge-adjacent (2 axes differ): weight 0.1
	// Corner-adjacent (3 axes differ): weight 0.01
	neighbors := make([][]neighbor, n)
	for i, c := range cells {
		var nbrs []neighbor
		for dc := -1; dc <= 1; dc++ {
			for dr := -1; dr <= 1; dr++ {
				for dl := -1; dl <= 1; dl++ {
					if dc == 0 && dr == 0 && dl == 0 {
						continue
					}
					if j, ok := cellMap[CellKey{c.Col + dc, c.Row + dr, c.Layer + dl}]; ok {
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
						nbrs = append(nbrs, neighbor{idx: j, weight: w})
					}
				}
			}
		}
		neighbors[i] = nbrs
	}

	// Random permutation with deterministic seed.
	rng := rand.New(rand.NewSource(42))
	order := rng.Perm(n)

	assignments := make([]int32, n)
	errBuf := make([][3]float32, n)
	processed := make([]bool, n)

	for _, idx := range order {
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

		// Distribute error to unprocessed neighbors.
		var totalWeight float32
		for _, nb := range neighbors[idx] {
			if !processed[nb.idx] {
				totalWeight += nb.weight
			}
		}
		if totalWeight > 0 {
			scale := 1.0 / totalWeight
			for _, nb := range neighbors[idx] {
				if !processed[nb.idx] {
					w := nb.weight * scale
					errBuf[nb.idx][0] += eR * w
					errBuf[nb.idx][1] += eG * w
					errBuf[nb.idx][2] += eB * w
				}
			}
		}
	}

	return assignments
}

// SnapColors moves each cell's color toward its nearest palette color by up
// to deltaE units (standard CIE76 ΔE) in CIELAB space. If the cell is
// already closer than deltaE, it snaps to the palette color exactly.
func SnapColors(cells []ActiveCell, pal [][3]uint8, deltaE float64) {
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
}

// AssignColors assigns palette indices without dithering.
func AssignColors(cells []ActiveCell, pal [][3]uint8) []int32 {
	colors := make([][3]uint8, len(cells))
	for i, c := range cells {
		colors[i] = c.Color
	}
	return palette.AssignPalette(colors, pal)
}
