// Package render provides orthographic z-buffered depth rendering for meshes.
package render

import (
	"image"
	"image/color"
	"math"
)

// Bounds holds viewport and depth extents in projected space.
type Bounds struct {
	XMin, XMax float64
	YMin, YMax float64
	DMin, DMax float64 // depth (into screen)
}

// UnionBounds returns the union of two Bounds.
func UnionBounds(a, b Bounds) Bounds {
	return Bounds{
		XMin: math.Min(a.XMin, b.XMin),
		XMax: math.Max(a.XMax, b.XMax),
		YMin: math.Min(a.YMin, b.YMin),
		YMax: math.Max(a.YMax, b.YMax),
		DMin: math.Min(a.DMin, b.DMin),
		DMax: math.Max(a.DMax, b.DMax),
	}
}

// DepthImage holds a rendered depth buffer.
type DepthImage struct {
	Width, Height int
	Depth         []float64 // row-major, NaN = no geometry
}

// Mask returns true for pixels that have geometry.
func (d *DepthImage) Mask() []bool {
	m := make([]bool, len(d.Depth))
	for i, v := range d.Depth {
		m[i] = !math.IsNaN(v)
	}
	return m
}

// ToRGBA converts the depth buffer to an RGBA image with depth-encoded gray
// and transparent background. Uses the provided bounds for depth mapping.
func (d *DepthImage) ToRGBA(bounds Bounds) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, d.Width, d.Height))
	dRange := bounds.DMax - bounds.DMin
	if dRange < 1e-12 {
		dRange = 1
	}
	for i, v := range d.Depth {
		if math.IsNaN(v) {
			continue
		}
		gray := (v - bounds.DMin) / dRange * 255
		if gray < 0 {
			gray = 0
		} else if gray > 255 {
			gray = 255
		}
		g := uint8(gray)
		x := i % d.Width
		y := i / d.Width
		img.SetRGBA(x, y, color.RGBA{g, g, g, 255})
	}
	return img
}

// GrayAt returns the gray value (0-255) at pixel (x,y) given bounds, or -1 if
// no geometry.
func (d *DepthImage) GrayAt(x, y int, bounds Bounds) int {
	v := d.Depth[y*d.Width+x]
	if math.IsNaN(v) {
		return -1
	}
	dRange := bounds.DMax - bounds.DMin
	if dRange < 1e-12 {
		dRange = 1
	}
	gray := (v - bounds.DMin) / dRange * 255
	if gray < 0 {
		gray = 0
	} else if gray > 255 {
		gray = 255
	}
	return int(gray)
}

// rotationMatrix builds a 3x3 rotation: azimuth around Z, then elevation tilt.
func rotationMatrix(azimuthDeg, elevationDeg float64) [3][3]float64 {
	az := (azimuthDeg + 90) * math.Pi / 180
	el := elevationDeg * math.Pi / 180
	cosAz, sinAz := math.Cos(az), math.Sin(az)
	cosEl, sinEl := math.Cos(el), math.Sin(el)
	rz := [3][3]float64{
		{cosAz, -sinAz, 0},
		{sinAz, cosAz, 0},
		{0, 0, 1},
	}
	rx := [3][3]float64{
		{1, 0, 0},
		{0, cosEl, -sinEl},
		{0, sinEl, cosEl},
	}
	// result = rx @ rz
	var m [3][3]float64
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			for k := 0; k < 3; k++ {
				m[i][j] += rx[i][k] * rz[k][j]
			}
		}
	}
	return m
}

// transform applies a 3x3 rotation to a vertex.
func transform(rot [3][3]float64, v [3]float32) [3]float64 {
	return [3]float64{
		rot[0][0]*float64(v[0]) + rot[0][1]*float64(v[1]) + rot[0][2]*float64(v[2]),
		rot[1][0]*float64(v[0]) + rot[1][1]*float64(v[1]) + rot[1][2]*float64(v[2]),
		rot[2][0]*float64(v[0]) + rot[2][1]*float64(v[1]) + rot[2][2]*float64(v[2]),
	}
}

// ProjectedBounds computes viewport and depth bounds for a mesh from a given view.
func ProjectedBounds(vertices [][3]float32, azimuth, elevation float64) Bounds {
	rot := rotationMatrix(azimuth, elevation)
	b := Bounds{
		XMin: math.Inf(1), XMax: math.Inf(-1),
		YMin: math.Inf(1), YMax: math.Inf(-1),
		DMin: math.Inf(1), DMax: math.Inf(-1),
	}
	for _, v := range vertices {
		t := transform(rot, v)
		px, py, d := t[0], t[2], t[1] // X=horizontal, Z=vertical, Y=depth
		if px < b.XMin {
			b.XMin = px
		}
		if px > b.XMax {
			b.XMax = px
		}
		if py < b.YMin {
			b.YMin = py
		}
		if py > b.YMax {
			b.YMax = py
		}
		if d < b.DMin {
			b.DMin = d
		}
		if d > b.DMax {
			b.DMax = d
		}
	}
	return b
}

// Render produces a depth buffer from an orthographic view of a triangle mesh.
func Render(vertices [][3]float32, faces [][3]uint32, azimuth, elevation float64, resolution int, bounds Bounds) *DepthImage {
	rot := rotationMatrix(azimuth, elevation)
	margin := 0.05

	// Transform all vertices.
	type projected struct {
		px, py, depth float64
	}
	proj := make([]projected, len(vertices))
	for i, v := range vertices {
		t := transform(rot, v)
		proj[i] = projected{px: t[0], py: t[2], depth: t[1]}
	}

	// Compute pixel mapping from bounds.
	xRange := bounds.XMax - bounds.XMin
	yRange := bounds.YMax - bounds.YMin
	maxRange := math.Max(xRange, yRange)
	if maxRange < 1e-12 {
		maxRange = 1
	}
	res := float64(resolution)
	scale := res * (1 - 2*margin) / maxRange
	cx := res/2 - (bounds.XMin+bounds.XMax)/2*scale
	cy := res/2 + (bounds.YMin+bounds.YMax)/2*scale

	// Map to pixel coords.
	type pixVert struct {
		x, y, d float64
	}
	pv := make([]pixVert, len(proj))
	for i, p := range proj {
		pv[i] = pixVert{
			x: p.px*scale + cx,
			y: -p.py*scale + cy,
			d: p.depth,
		}
	}

	// Z-buffer.
	img := &DepthImage{
		Width:  resolution,
		Height: resolution,
		Depth:  make([]float64, resolution*resolution),
	}
	for i := range img.Depth {
		img.Depth[i] = math.NaN()
	}
	zbuf := make([]float64, resolution*resolution)
	for i := range zbuf {
		zbuf[i] = math.Inf(1)
	}

	for _, f := range faces {
		v0, v1, v2 := pv[f[0]], pv[f[1]], pv[f[2]]

		// Bounding box clipped to image.
		minX := math.Floor(math.Min(v0.x, math.Min(v1.x, v2.x)))
		maxX := math.Ceil(math.Max(v0.x, math.Max(v1.x, v2.x)))
		minY := math.Floor(math.Min(v0.y, math.Min(v1.y, v2.y)))
		maxY := math.Ceil(math.Max(v0.y, math.Max(v1.y, v2.y)))

		bx0 := int(minX)
		bx1 := int(maxX)
		by0 := int(minY)
		by1 := int(maxY)
		if bx0 < 0 {
			bx0 = 0
		}
		if by0 < 0 {
			by0 = 0
		}
		if bx1 >= resolution {
			bx1 = resolution - 1
		}
		if by1 >= resolution {
			by1 = resolution - 1
		}
		if bx0 > bx1 || by0 > by1 {
			continue
		}

		// Edge vectors for barycentric coords.
		e0x, e0y := v1.x-v0.x, v1.y-v0.y
		e1x, e1y := v2.x-v0.x, v2.y-v0.y
		dot00 := e0x*e0x + e0y*e0y
		dot01 := e0x*e1x + e0y*e1y
		dot11 := e1x*e1x + e1y*e1y
		denom := dot00*dot11 - dot01*dot01
		if math.Abs(denom) < 1e-10 {
			continue
		}
		invDenom := 1.0 / denom

		for py := by0; py <= by1; py++ {
			for px := bx0; px <= bx1; px++ {
				// Pixel center.
				qx := float64(px) + 0.5 - v0.x
				qy := float64(py) + 0.5 - v0.y

				dot02 := e0x*qx + e0y*qy
				dot12 := e1x*qx + e1y*qy

				u := (dot11*dot02 - dot01*dot12) * invDenom
				v := (dot00*dot12 - dot01*dot02) * invDenom

				if u < 0 || v < 0 || u+v > 1 {
					continue
				}

				d := v0.d + u*(v1.d-v0.d) + v*(v2.d-v0.d)
				idx := py*resolution + px
				if d < zbuf[idx] {
					zbuf[idx] = d
					img.Depth[idx] = d
				}
			}
		}
	}

	return img
}

// ColorImage holds a rendered color buffer.
type ColorImage struct {
	Width, Height int
	R, G, B       []uint8
	HasPixel      []bool
}

// ToRGBA converts the color buffer to an RGBA image with transparent background.
func (c *ColorImage) ToRGBA() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, c.Width, c.Height))
	for i := 0; i < c.Width*c.Height; i++ {
		if !c.HasPixel[i] {
			continue
		}
		x := i % c.Width
		y := i / c.Width
		img.SetRGBA(x, y, color.RGBA{c.R[i], c.G[i], c.B[i], 255})
	}
	return img
}

// RenderColor produces a color image from an orthographic view of a triangle mesh.
// colorFn is called for each visible pixel with the face index and barycentric
// coordinates (u, v) where the point = (1-u-v)*v0 + u*v1 + v*v2.
func RenderColor(
	vertices [][3]float32,
	faces [][3]uint32,
	azimuth, elevation float64,
	resolution int,
	bounds Bounds,
	colorFn func(faceIdx int, baryU, baryV float64) [3]uint8,
) *ColorImage {
	rot := rotationMatrix(azimuth, elevation)
	margin := 0.05

	type projected struct {
		px, py, depth float64
	}
	proj := make([]projected, len(vertices))
	for i, v := range vertices {
		t := transform(rot, v)
		proj[i] = projected{px: t[0], py: t[2], depth: t[1]}
	}

	xRange := bounds.XMax - bounds.XMin
	yRange := bounds.YMax - bounds.YMin
	maxRange := math.Max(xRange, yRange)
	if maxRange < 1e-12 {
		maxRange = 1
	}
	res := float64(resolution)
	scale := res * (1 - 2*margin) / maxRange
	cx := res/2 - (bounds.XMin+bounds.XMax)/2*scale
	cy := res/2 + (bounds.YMin+bounds.YMax)/2*scale

	type pixVert struct {
		x, y, d float64
	}
	pv := make([]pixVert, len(proj))
	for i, p := range proj {
		pv[i] = pixVert{
			x: p.px*scale + cx,
			y: -p.py*scale + cy,
			d: p.depth,
		}
	}

	n := resolution * resolution
	img := &ColorImage{
		Width:    resolution,
		Height:   resolution,
		R:        make([]uint8, n),
		G:        make([]uint8, n),
		B:        make([]uint8, n),
		HasPixel: make([]bool, n),
	}
	zbuf := make([]float64, n)
	for i := range zbuf {
		zbuf[i] = math.Inf(1)
	}

	for fi, f := range faces {
		v0, v1, v2 := pv[f[0]], pv[f[1]], pv[f[2]]

		minX := math.Floor(math.Min(v0.x, math.Min(v1.x, v2.x)))
		maxX := math.Ceil(math.Max(v0.x, math.Max(v1.x, v2.x)))
		minY := math.Floor(math.Min(v0.y, math.Min(v1.y, v2.y)))
		maxY := math.Ceil(math.Max(v0.y, math.Max(v1.y, v2.y)))

		bx0 := int(minX)
		bx1 := int(maxX)
		by0 := int(minY)
		by1 := int(maxY)
		if bx0 < 0 {
			bx0 = 0
		}
		if by0 < 0 {
			by0 = 0
		}
		if bx1 >= resolution {
			bx1 = resolution - 1
		}
		if by1 >= resolution {
			by1 = resolution - 1
		}
		if bx0 > bx1 || by0 > by1 {
			continue
		}

		e0x, e0y := v1.x-v0.x, v1.y-v0.y
		e1x, e1y := v2.x-v0.x, v2.y-v0.y
		dot00 := e0x*e0x + e0y*e0y
		dot01 := e0x*e1x + e0y*e1y
		dot11 := e1x*e1x + e1y*e1y
		denom := dot00*dot11 - dot01*dot01
		if math.Abs(denom) < 1e-10 {
			continue
		}
		invDenom := 1.0 / denom

		for py := by0; py <= by1; py++ {
			for px := bx0; px <= bx1; px++ {
				qx := float64(px) + 0.5 - v0.x
				qy := float64(py) + 0.5 - v0.y

				dot02 := e0x*qx + e0y*qy
				dot12 := e1x*qx + e1y*qy

				u := (dot11*dot02 - dot01*dot12) * invDenom
				v := (dot00*dot12 - dot01*dot02) * invDenom

				if u < 0 || v < 0 || u+v > 1 {
					continue
				}

				d := v0.d + u*(v1.d-v0.d) + v*(v2.d-v0.d)
				idx := py*resolution + px
				if d < zbuf[idx] {
					zbuf[idx] = d
					c := colorFn(fi, u, v)
					img.R[idx] = c[0]
					img.G[idx] = c[1]
					img.B[idx] = c[2]
					img.HasPixel[idx] = true
				}
			}
		}
	}

	return img
}

// DilateMask dilates a boolean mask by radius pixels using a box filter.
func DilateMask(mask []bool, width, height, radius int) []bool {
	out := make([]bool, len(mask))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if !mask[y*width+x] {
				continue
			}
			// Spread this true pixel to neighbors within radius.
			y0 := y - radius
			y1 := y + radius
			x0 := x - radius
			x1 := x + radius
			if y0 < 0 {
				y0 = 0
			}
			if y1 >= height {
				y1 = height - 1
			}
			if x0 < 0 {
				x0 = 0
			}
			if x1 >= width {
				x1 = width - 1
			}
			for dy := y0; dy <= y1; dy++ {
				for dx := x0; dx <= x1; dx++ {
					out[dy*width+dx] = true
				}
			}
		}
	}
	return out
}
