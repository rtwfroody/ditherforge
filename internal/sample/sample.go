// Package sample samples texture colors at face centroids and optionally
// applies Floyd-Steinberg dithering in Morton-ordered face space.
package sample

import (
	"image"
	"math"
	"sort"

	"github.com/rtwfroody/text2filament/internal/loader"
)

// bilinearSample samples an image at normalized UV coordinates using bilinear
// interpolation. Returns [3]float32 RGB in 0-255 range.
func bilinearSample(img image.Image, u, v float32) [3]float32 {
	bounds := img.Bounds()
	W := float32(bounds.Max.X - bounds.Min.X)
	H := float32(bounds.Max.Y - bounds.Min.Y)

	px := u * (W - 1)
	py := (1.0 - v) * (H - 1)

	x0 := int(math.Floor(float64(px)))
	y0 := int(math.Floor(float64(py)))
	x1 := x0 + 1
	y1 := y0 + 1

	// Clamp.
	maxX := int(W) - 1
	maxY := int(H) - 1
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > maxX {
		x1 = maxX
	}
	if y1 > maxY {
		y1 = maxY
	}
	if x0 > maxX {
		x0 = maxX
	}
	if y0 > maxY {
		y0 = maxY
	}

	fx := px - float32(math.Floor(float64(px)))
	fy := py - float32(math.Floor(float64(py)))

	// Offset for bounds origin.
	ox := bounds.Min.X
	oy := bounds.Min.Y

	samplePixel := func(x, y int) [3]float32 {
		// Try type assertions for fast access.
		switch img := img.(type) {
		case *image.NRGBA:
			i := (y*img.Stride + x*4)
			return [3]float32{float32(img.Pix[i]), float32(img.Pix[i+1]), float32(img.Pix[i+2])}
		case *image.RGBA:
			i := (y*img.Stride + x*4)
			return [3]float32{float32(img.Pix[i]), float32(img.Pix[i+1]), float32(img.Pix[i+2])}
		default:
			r, g, b, _ := img.At(x+ox, y+oy).RGBA()
			return [3]float32{float32(r >> 8), float32(g >> 8), float32(b >> 8)}
		}
	}

	p00 := samplePixel(x0, y0)
	p10 := samplePixel(x1, y0)
	p01 := samplePixel(x0, y1)
	p11 := samplePixel(x1, y1)

	return [3]float32{
		(1-fx)*(1-fy)*p00[0] + fx*(1-fy)*p10[0] + (1-fx)*fy*p01[0] + fx*fy*p11[0],
		(1-fx)*(1-fy)*p00[1] + fx*(1-fy)*p10[1] + (1-fx)*fy*p01[1] + fx*fy*p11[1],
		(1-fx)*(1-fy)*p00[2] + fx*(1-fy)*p10[2] + (1-fx)*fy*p01[2] + fx*fy*p11[2],
	}
}

// SampleFaceColors samples the texture color at each face's UV centroid.
// Returns a slice of [3]uint8 RGB values per face.
func SampleFaceColors(model *loader.LoadedModel) [][3]uint8 {
	F := len(model.Faces)
	colors := make([][3]float32, F)

	// Compute centroid UVs per face.
	centroidUVs := make([][2]float32, F)
	for fi, face := range model.Faces {
		u0, v0 := model.UVs[face[0]][0], model.UVs[face[0]][1]
		u1, v1 := model.UVs[face[1]][0], model.UVs[face[1]][1]
		u2, v2 := model.UVs[face[2]][0], model.UVs[face[2]][1]
		centroidUVs[fi] = [2]float32{
			(u0 + u1 + u2) / 3.0,
			(v0 + v1 + v2) / 3.0,
		}
	}

	for texIdx, tex := range model.Textures {
		for fi, ti := range model.FaceTextureIdx {
			if int(ti) != texIdx {
				continue
			}
			u := centroidUVs[fi][0]
			v := centroidUVs[fi][1]
			// Clamp UVs.
			if u < 0 {
				u = 0
			}
			if u > 1 {
				u = 1
			}
			if v < 0 {
				v = 0
			}
			if v > 1 {
				v = 1
			}
			colors[fi] = bilinearSample(tex, u, v)
		}
	}

	result := make([][3]uint8, F)
	for i, c := range colors {
		r := math.Round(float64(c[0]))
		g := math.Round(float64(c[1]))
		b := math.Round(float64(c[2]))
		if r < 0 {
			r = 0
		}
		if r > 255 {
			r = 255
		}
		if g < 0 {
			g = 0
		}
		if g > 255 {
			g = 255
		}
		if b < 0 {
			b = 0
		}
		if b > 255 {
			b = 255
		}
		result[i] = [3]uint8{uint8(r), uint8(g), uint8(b)}
	}
	return result
}

// spreadBits spreads 16-bit integer bits for Morton code interleaving.
func spreadBits(v uint32) uint32 {
	v &= 0xFFFF
	v = (v | (v << 8)) & 0x00FF00FF
	v = (v | (v << 4)) & 0x0F0F0F0F
	v = (v | (v << 2)) & 0x33333333
	v = (v | (v << 1)) & 0x55555555
	return v
}

// mortonCode computes the 2D Morton (Z-order) code for integer u, v.
func mortonCode(u, v uint32) uint32 {
	return spreadBits(u) | (spreadBits(v) << 1)
}

// faceArea computes the area of a triangle given its three 3D vertex positions.
func faceArea(v0, v1, v2 [3]float32) float32 {
	ax := v1[0] - v0[0]
	ay := v1[1] - v0[1]
	az := v1[2] - v0[2]
	bx := v2[0] - v0[0]
	by := v2[1] - v0[1]
	bz := v2[2] - v0[2]
	cx := ay*bz - az*by
	cy := az*bx - ax*bz
	cz := ax*by - ay*bx
	return 0.5 * float32(math.Sqrt(float64(cx*cx+cy*cy+cz*cz)))
}

// SampleFaceIndices performs face-level Floyd-Steinberg error diffusion with
// Morton-code spatial ordering to assign each face to a palette color.
// Error propagation is area-weighted: large-face errors are attenuated before
// being applied to smaller faces, preventing color bleeding at scale transitions.
func SampleFaceIndices(model *loader.LoadedModel, palette [][3]uint8) []int32 {
	F := len(model.Faces)
	faceColors := SampleFaceColors(model)

	// Precompute face areas for area-weighted error propagation.
	areas := make([]float32, F)
	for fi, face := range model.Faces {
		areas[fi] = faceArea(model.Vertices[face[0]], model.Vertices[face[1]], model.Vertices[face[2]])
	}

	// Compute centroid UVs for Morton ordering.
	centroidUVs := make([][2]float32, F)
	for fi, face := range model.Faces {
		u0, v0 := model.UVs[face[0]][0], model.UVs[face[0]][1]
		u1, v1 := model.UVs[face[1]][0], model.UVs[face[1]][1]
		u2, v2 := model.UVs[face[2]][0], model.UVs[face[2]][1]
		cu := (u0 + u1 + u2) / 3.0
		cv := (v0 + v1 + v2) / 3.0
		if cu < 0 {
			cu = 0
		}
		if cu > 1 {
			cu = 1
		}
		if cv < 0 {
			cv = 0
		}
		if cv > 1 {
			cv = 1
		}
		centroidUVs[fi] = [2]float32{cu, cv}
	}

	// Encode UV as uint16 and compute Morton codes.
	const N = float32(0xFFFF)
	mortonCodes := make([]uint32, F)
	for fi, uv := range centroidUVs {
		uInt := uint32(uv[0] * N)
		vInt := uint32(uv[1] * N)
		mortonCodes[fi] = mortonCode(uInt, vInt)
	}

	// Sort face indices by Morton code.
	order := make([]int, F)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return mortonCodes[order[i]] < mortonCodes[order[j]]
	})

	// Build inverse order mapping.
	invOrder := make([]int, F)
	for i, fi := range order {
		invOrder[fi] = i
	}

	// Floyd-Steinberg error diffusion in Morton order.
	// Use RGB Euclidean for speed during FS (close enough for error diffusion).
	paletteF := make([][3]float32, len(palette))
	for i, p := range palette {
		paletteF[i] = [3]float32{float32(p[0]), float32(p[1]), float32(p[2])}
	}

	assignmentsSorted := make([]int32, F)
	var errR, errG, errB float32

	for i, origIdx := range order {
		fc := faceColors[origIdx]
		cr := clampF(float32(fc[0])+errR, 0, 255)
		cg := clampF(float32(fc[1])+errG, 0, 255)
		cb := clampF(float32(fc[2])+errB, 0, 255)

		// Find nearest palette color by RGB Euclidean distance.
		bestIdx := 0
		bestDist := float32(math.MaxFloat32)
		for pi, p := range paletteF {
			dr := cr - p[0]
			dg := cg - p[1]
			db := cb - p[2]
			d := dr*dr + dg*dg + db*db
			if d < bestDist {
				bestDist = d
				bestIdx = pi
			}
		}

		// Store the sorted-position assignment by mapping sorted index back.
		sortedPos := invOrder[origIdx]
		assignmentsSorted[sortedPos] = int32(bestIdx)

		// Compute quantization error.
		eR := cr - paletteF[bestIdx][0]
		eG := cg - paletteF[bestIdx][1]
		eB := cb - paletteF[bestIdx][2]

		// Area-weighted error propagation: attenuate error when transitioning
		// from a large face to a smaller one so that one big face's rounding
		// error doesn't overwhelm many tiny faces.
		curArea := areas[origIdx]
		if i+1 < len(order) {
			nextArea := areas[order[i+1]]
			if nextArea > 0 && curArea > 0 {
				scale := curArea / nextArea
				if scale > 1 {
					scale = 1
				}
				eR *= scale
				eG *= scale
				eB *= scale
			}
		}
		errR = eR
		errG = eG
		errB = eB
	}

	// Unmap back to original face order.
	assignments := make([]int32, F)
	for fi := range assignments {
		assignments[fi] = assignmentsSorted[invOrder[fi]]
	}

	return assignments
}

func clampF(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
