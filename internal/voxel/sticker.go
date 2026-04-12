package voxel

import (
	"context"
	"image"
	"math"
)

// maxStickerDepth controls how far from the sticker's tangent plane a surface
// voxel can be and still receive the sticker. Expressed as a multiple of the
// cell size. Needs to be generous enough to catch all surface voxels on gently
// curved surfaces, but not so large that it bleeds through thin walls.
const maxStickerDepthCells = 3.0

// isSurfaceVoxel returns true if the cell has at least one empty face-neighbor
// (i.e. it's on the boundary of the solid, not buried in the interior).
func isSurfaceVoxel(key CellKey, cellMap map[CellKey]int) bool {
	for _, nk := range [6]CellKey{
		{Grid: key.Grid, Col: key.Col + 1, Row: key.Row, Layer: key.Layer},
		{Grid: key.Grid, Col: key.Col - 1, Row: key.Row, Layer: key.Layer},
		{Grid: key.Grid, Col: key.Col, Row: key.Row + 1, Layer: key.Layer},
		{Grid: key.Grid, Col: key.Col, Row: key.Row - 1, Layer: key.Layer},
		{Grid: key.Grid, Col: key.Col, Row: key.Row, Layer: key.Layer + 1},
		{Grid: key.Grid, Col: key.Col, Row: key.Row, Layer: key.Layer - 1},
	} {
		if _, ok := cellMap[nk]; !ok {
			return true
		}
	}
	return false
}

// ApplySticker applies a PNG image onto the voxel grid using planar decal
// projection. For each surface voxel, it projects the voxel's position onto
// the sticker's tangent plane to compute UV coordinates. Voxels within the
// image bounds, close to the tangent plane, and facing roughly the same
// direction as the sticker normal get their color overwritten.
//
// This approach is robust on any geometry — no surface traversal needed.
// The trade-off is slight stretching on highly curved surfaces, which is
// negligible at voxel resolution.
func ApplySticker(
	ctx context.Context,
	cells []ActiveCell,
	cellMap map[CellKey]int,
	img image.Image,
	center [3]float64,
	normal [3]float64,
	up [3]float64,
	scale float64,
	rotationDeg float64,
	cellSize float32,
) error {
	// Build orthonormal tangent frame from normal and camera up.
	n := normalize3(normal)
	u := normalize3(up)

	// If up is nearly parallel to normal, the cross product degenerates.
	// Fall back to a world axis that isn't parallel to normal.
	cross := cross3(u, n)
	crossLen := math.Sqrt(cross[0]*cross[0] + cross[1]*cross[1] + cross[2]*cross[2])
	if crossLen < 0.1 {
		if math.Abs(n[0]) < 0.9 {
			u = [3]float64{1, 0, 0}
		} else {
			u = [3]float64{0, 1, 0}
		}
	}

	// T (tangent/right) = normalize(cross(up, normal))
	t := normalize3(cross3(u, n))
	// B (bitangent/up on surface) = normalize(cross(normal, T))
	b := normalize3(cross3(n, t))

	// Apply rotation around the normal.
	if rotationDeg != 0 {
		rad := rotationDeg * math.Pi / 180
		cosR := math.Cos(rad)
		sinR := math.Sin(rad)
		newT := [3]float64{
			cosR*t[0] + sinR*b[0],
			cosR*t[1] + sinR*b[1],
			cosR*t[2] + sinR*b[2],
		}
		newB := [3]float64{
			-sinR*t[0] + cosR*b[0],
			-sinR*t[1] + cosR*b[1],
			-sinR*t[2] + cosR*b[2],
		}
		t = newT
		b = newB
	}

	imgBounds := img.Bounds()
	imgW := imgBounds.Dx()
	imgH := imgBounds.Dy()

	// Aspect ratio: scale is the width in world units.
	aspect := float64(imgH) / float64(imgW)
	halfW := scale / 2
	halfH := (scale * aspect) / 2

	// Depth threshold: how far from the tangent plane a voxel can be.
	maxDepth := maxStickerDepthCells * float64(cellSize)

	for i := range cells {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		c := cells[i]
		k := CellKey{Grid: c.Grid, Col: c.Col, Row: c.Row, Layer: c.Layer}

		// Only apply to surface voxels.
		if !isSurfaceVoxel(k, cellMap) {
			continue
		}

		// Vector from sticker center to this voxel.
		dx := float64(c.Cx) - center[0]
		dy := float64(c.Cy) - center[1]
		dz := float64(c.Cz) - center[2]

		// Depth: distance along the sticker normal from the tangent plane.
		depth := dx*n[0] + dy*n[1] + dz*n[2]
		if depth < -maxDepth || depth > maxDepth {
			continue
		}

		// Project onto tangent frame for UV coordinates.
		projT := dx*t[0] + dy*t[1] + dz*t[2]
		projB := dx*b[0] + dy*b[1] + dz*b[2]

		// Check if within sticker bounds (world units).
		if projT < -halfW || projT > halfW || projB < -halfH || projB > halfH {
			continue
		}

		// Convert to UV in [0, 1].
		uCoord := (projT/halfW + 1) / 2 // -halfW..halfW → 0..1
		vCoord := (projB/halfH + 1) / 2 // -halfH..halfH → 0..1

		applyStickerPixel(cells, i, img, imgW, imgH, uCoord, vCoord)
	}

	return nil
}

// applyStickerPixel samples the sticker image at (u,v) in [0,1] and overwrites
// the cell's color if the pixel alpha exceeds 128.
func applyStickerPixel(cells []ActiveCell, idx int, img image.Image, imgW, imgH int, u, v float64) {
	// Clamp UV to [0,1].
	if u < 0 {
		u = 0
	} else if u > 1 {
		u = 1
	}
	if v < 0 {
		v = 0
	} else if v > 1 {
		v = 1
	}

	// Convert to pixel coordinates. V is flipped (image y=0 is top).
	px := int(u * float64(imgW-1))
	py := int((1 - v) * float64(imgH-1))

	bounds := img.Bounds()
	px += bounds.Min.X
	py += bounds.Min.Y

	r, g, b, a := img.At(px, py).RGBA()
	if a < 0x8000 { // alpha < 128 (RGBA returns 16-bit values)
		return
	}

	// RGBA returns pre-multiplied 16-bit values; un-premultiply and
	// convert to 8-bit sRGB so semi-transparent pixels aren't darkened.
	cells[idx].Color = [3]uint8{
		uint8(r * 0xFF / a),
		uint8(g * 0xFF / a),
		uint8(b * 0xFF / a),
	}
}

// Vector math helpers.

func cross3(a, b [3]float64) [3]float64 {
	return [3]float64{
		a[1]*b[2] - a[2]*b[1],
		a[2]*b[0] - a[0]*b[2],
		a[0]*b[1] - a[1]*b[0],
	}
}

func normalize3(v [3]float64) [3]float64 {
	l := math.Sqrt(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])
	if l < 1e-12 {
		return [3]float64{0, 0, 1}
	}
	return [3]float64{v[0] / l, v[1] / l, v[2] / l}
}
