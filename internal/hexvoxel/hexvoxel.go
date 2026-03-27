// Package hexvoxel generates a hexagonal voxel shell of a textured mesh.
// Each hexagonal prism in the output corresponds to one column (1.5× nozzle width)
// at one layer height, matching what a slicer actually deposits.
// Isosurface extraction uses marching prisms (wedge decomposition) to produce
// a smooth surface that follows the original model shape.
package hexvoxel

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"runtime"
	"sort"
	"sync"

	"github.com/rtwfroody/text2filament/internal/loader"
	"github.com/rtwfroody/text2filament/internal/palette"
)

// Config holds parameters for hexagonal voxel remeshing.
type Config struct {
	NozzleDiameter float32 // flat-to-flat hex width in mm
	LayerHeight    float32 // Z extrusion per layer in mm
	NoMerge        bool    // skip coplanar triangle merging
}

// surfaceHit records where a hex column center intersects the original mesh.
type surfaceHit struct {
	z       float32
	meshIdx int32
}

// activeHex represents one hex prism to generate.
type activeHex struct {
	col, row, layer int
	cx, cy, cz      float32
	color           [3]uint8
}

type hexKey struct{ col, row, layer int }

// spatialIndex is a 2D uniform grid for fast triangle lookup by XY position.
type spatialIndex struct {
	cells    [][]int32    // cell index → list of triangle indices
	triZMin  []float32    // per-triangle Z min
	triZMax  []float32    // per-triangle Z max
	minX     float32
	minY     float32
	cellSize float32
	cols     int
	rows     int
}

func newSpatialIndex(model *loader.LoadedModel, cellSize float32) *spatialIndex {
	if len(model.Vertices) == 0 {
		return &spatialIndex{cellSize: cellSize}
	}

	minX, minY := float32(math.Inf(1)), float32(math.Inf(1))
	maxX, maxY := float32(math.Inf(-1)), float32(math.Inf(-1))
	for _, v := range model.Vertices {
		if v[0] < minX {
			minX = v[0]
		}
		if v[0] > maxX {
			maxX = v[0]
		}
		if v[1] < minY {
			minY = v[1]
		}
		if v[1] > maxY {
			maxY = v[1]
		}
	}

	// Pad by one cell.
	minX -= cellSize
	minY -= cellSize
	maxX += cellSize
	maxY += cellSize

	cols := int(math.Ceil(float64(maxX-minX)/float64(cellSize))) + 1
	rows := int(math.Ceil(float64(maxY-minY)/float64(cellSize))) + 1

	nTris := len(model.Faces)
	si := &spatialIndex{
		cells:    make([][]int32, cols*rows),
		triZMin:  make([]float32, nTris),
		triZMax:  make([]float32, nTris),
		minX:     minX,
		minY:     minY,
		cellSize: cellSize,
		cols:     cols,
		rows:     rows,
	}

	// Insert each triangle into overlapping cells and record Z ranges.
	for fi, f := range model.Faces {
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]

		txMin := minf(v0[0], minf(v1[0], v2[0]))
		txMax := maxf(v0[0], maxf(v1[0], v2[0]))
		tyMin := minf(v0[1], minf(v1[1], v2[1]))
		tyMax := maxf(v0[1], maxf(v1[1], v2[1]))
		si.triZMin[fi] = minf(v0[2], minf(v1[2], v2[2]))
		si.triZMax[fi] = maxf(v0[2], maxf(v1[2], v2[2]))

		c0 := int((txMin - minX) / cellSize)
		c1 := int((txMax - minX) / cellSize)
		r0 := int((tyMin - minY) / cellSize)
		r1 := int((tyMax - minY) / cellSize)

		if c0 < 0 {
			c0 = 0
		}
		if r0 < 0 {
			r0 = 0
		}
		if c1 >= cols {
			c1 = cols - 1
		}
		if r1 >= rows {
			r1 = rows - 1
		}

		for c := c0; c <= c1; c++ {
			for r := r0; r <= r1; r++ {
				idx := r*cols + c
				si.cells[idx] = append(si.cells[idx], int32(fi))
			}
		}
	}

	return si
}

// candidates returns triangle indices that might overlap the given XY point.
func (si *spatialIndex) candidates(x, y float32) []int32 {
	c := int((x - si.minX) / si.cellSize)
	r := int((y - si.minY) / si.cellSize)
	if c < 0 || c >= si.cols || r < 0 || r >= si.rows {
		return nil
	}
	return si.cells[r*si.cols+c]
}

// candidatesRadius returns triangle indices from all cells within radius of (x,y).
func (si *spatialIndex) candidatesRadius(x, y, radius float32) []int32 {
	c0 := int((x - radius - si.minX) / si.cellSize)
	c1 := int((x + radius - si.minX) / si.cellSize)
	r0 := int((y - radius - si.minY) / si.cellSize)
	r1 := int((y + radius - si.minY) / si.cellSize)

	if c0 < 0 {
		c0 = 0
	}
	if r0 < 0 {
		r0 = 0
	}
	if c1 >= si.cols {
		c1 = si.cols - 1
	}
	if r1 >= si.rows {
		r1 = si.rows - 1
	}

	seen := make(map[int32]struct{})
	var result []int32
	for c := c0; c <= c1; c++ {
		for r := r0; r <= r1; r++ {
			for _, ti := range si.cells[r*si.cols+c] {
				if _, ok := seen[ti]; !ok {
					seen[ti] = struct{}{}
					result = append(result, ti)
				}
			}
		}
	}
	return result
}

// searchBuf is a reusable buffer for spatial index queries, avoiding per-call
// map allocations. Each goroutine should have its own searchBuf.
type searchBuf struct {
	seen []uint32 // per-triangle generation counter
	gen  uint32   // current generation; bump per query
	result []int32
}

func newSearchBuf(nTris int) *searchBuf {
	return &searchBuf{seen: make([]uint32, nTris)}
}

// candidatesRadiusZ returns triangle indices from cells within XY radius whose
// Z range is within zRadius of the query Z. This filters out triangles that are
// far away in Z, dramatically reducing candidate counts for tall models.
func (si *spatialIndex) candidatesRadiusZ(x, y, xyRadius, z, zRadius float32, buf *searchBuf) []int32 {
	c0 := int((x - xyRadius - si.minX) / si.cellSize)
	c1 := int((x + xyRadius - si.minX) / si.cellSize)
	r0 := int((y - xyRadius - si.minY) / si.cellSize)
	r1 := int((y + xyRadius - si.minY) / si.cellSize)

	if c0 < 0 {
		c0 = 0
	}
	if r0 < 0 {
		r0 = 0
	}
	if c1 >= si.cols {
		c1 = si.cols - 1
	}
	if r1 >= si.rows {
		r1 = si.rows - 1
	}

	buf.gen++
	if buf.gen == 0 {
		// Overflow: reset all entries.
		for i := range buf.seen {
			buf.seen[i] = 0
		}
		buf.gen = 1
	}
	buf.result = buf.result[:0]

	zLo := z - zRadius
	zHi := z + zRadius
	gen := buf.gen
	for c := c0; c <= c1; c++ {
		for r := r0; r <= r1; r++ {
			for _, ti := range si.cells[r*si.cols+c] {
				if buf.seen[ti] == gen {
					continue
				}
				buf.seen[ti] = gen
				if si.triZMax[ti] < zLo || si.triZMin[ti] > zHi {
					continue
				}
				buf.result = append(buf.result, ti)
			}
		}
	}
	return buf.result
}

// pointInTriangleXY tests if (px, py) is inside the XY projection of triangle
// (v0, v1, v2) and returns barycentric coordinates.
func pointInTriangleXY(px, py float32, v0, v1, v2 [3]float32) (bool, [3]float32) {
	d00x := v1[0] - v0[0]
	d00y := v1[1] - v0[1]
	d01x := v2[0] - v0[0]
	d01y := v2[1] - v0[1]
	d02x := px - v0[0]
	d02y := py - v0[1]

	dot00 := d00x*d00x + d00y*d00y
	dot01 := d00x*d01x + d00y*d01y
	dot02 := d00x*d02x + d00y*d02y
	dot11 := d01x*d01x + d01y*d01y
	dot12 := d01x*d02x + d01y*d02y

	denom := dot00*dot11 - dot01*dot01
	if denom == 0 {
		return false, [3]float32{}
	}

	invDenom := 1.0 / denom
	u := (dot11*dot02 - dot01*dot12) * invDenom
	v := (dot00*dot12 - dot01*dot02) * invDenom

	if u >= 0 && v >= 0 && u+v <= 1 {
		return true, [3]float32{1 - u - v, u, v}
	}
	return false, [3]float32{}
}

// bilinearSample samples a texture at normalized UV coordinates.
func bilinearSample(img image.Image, u, v float32) [3]uint8 {
	bounds := img.Bounds()
	W := float32(bounds.Max.X - bounds.Min.X)
	H := float32(bounds.Max.Y - bounds.Min.Y)

	// Wrap UV to [0, 1).
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

	sample := func(x, y int) (float32, float32, float32) {
		r, g, b, _ := img.At(x, y).RGBA()
		return float32(r >> 8), float32(g >> 8), float32(b >> 8)
	}

	r00, g00, b00 := sample(x0, y0)
	r10, g10, b10 := sample(x1, y0)
	r01, g01, b01 := sample(x0, y1)
	r11, g11, b11 := sample(x1, y1)

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

	return [3]uint8{
		lerp(r00, r10, r01, r11, fx, fy),
		lerp(g00, g10, g01, g11, fx, fy),
		lerp(b00, b10, b01, b11, fx, fy),
	}
}

// closestPointOnTriangle3D returns the closest point on triangle (v0,v1,v2)
// to point p in 3D, and the squared distance.
func closestPointOnTriangle3D(p, v0, v1, v2 [3]float32) ([3]float32, float32) {
	// Edge vectors
	e0 := [3]float32{v1[0] - v0[0], v1[1] - v0[1], v1[2] - v0[2]}
	e1 := [3]float32{v2[0] - v0[0], v2[1] - v0[1], v2[2] - v0[2]}
	d := [3]float32{v0[0] - p[0], v0[1] - p[1], v0[2] - p[2]}

	a := dot3(e0, e0)
	b := dot3(e0, e1)
	c := dot3(e1, e1)
	dd := dot3(e0, d)
	e := dot3(e1, d)

	det := a*c - b*b
	s := b*e - c*dd
	t := b*dd - a*e

	if s+t <= det {
		if s < 0 {
			if t < 0 {
				// Region 4
				if dd < 0 {
					t = 0
					s = clampF(-dd/a, 0, 1)
				} else {
					s = 0
					t = clampF(-e/c, 0, 1)
				}
			} else {
				// Region 3
				s = 0
				t = clampF(-e/c, 0, 1)
			}
		} else if t < 0 {
			// Region 5
			t = 0
			s = clampF(-dd/a, 0, 1)
		} else {
			// Region 0 (inside triangle)
			invDet := 1.0 / det
			s *= invDet
			t *= invDet
		}
	} else {
		if s < 0 {
			// Region 2
			tmp0 := b + dd
			tmp1 := c + e
			if tmp1 > tmp0 {
				numer := tmp1 - tmp0
				denom := a - 2*b + c
				s = clampF(numer/denom, 0, 1)
				t = 1 - s
			} else {
				s = 0
				t = clampF(-e/c, 0, 1)
			}
		} else if t < 0 {
			// Region 6
			tmp0 := b + e
			tmp1 := a + dd
			if tmp1 > tmp0 {
				numer := tmp1 - tmp0
				denom := a - 2*b + c
				t = clampF(numer/denom, 0, 1)
				s = 1 - t
			} else {
				t = 0
				s = clampF(-dd/a, 0, 1)
			}
		} else {
			// Region 1
			numer := (c + e) - (b + dd)
			if numer <= 0 {
				s = 0
			} else {
				denom := a - 2*b + c
				s = clampF(numer/denom, 0, 1)
			}
			t = 1 - s
		}
	}

	closest := [3]float32{
		v0[0] + s*e0[0] + t*e1[0],
		v0[1] + s*e0[1] + t*e1[1],
		v0[2] + s*e0[2] + t*e1[2],
	}
	dx := p[0] - closest[0]
	dy := p[1] - closest[1]
	dz := p[2] - closest[2]
	return closest, dx*dx + dy*dy + dz*dz
}

func dot3(a, b [3]float32) float32 {
	return a[0]*b[0] + a[1]*b[1] + a[2]*b[2]
}

// computeSDF computes the signed distance field value at point p.
// Uses closest-surface-normal for sign determination, which is robust
// for non-watertight meshes where Z-ray parity fails.
// Points within shellThickness of the surface are always classified as
// inside, ensuring thin features are captured even with broken meshes.
func computeSDF(p [3]float32, model *loader.LoadedModel, si *spatialIndex, searchRadius float32, shellThickness float32, buf *searchBuf) float32 {
	cands := si.candidatesRadiusZ(p[0], p[1], searchRadius, p[2], searchRadius, buf)
	bestDistSq := float32(math.MaxFloat32)
	var bestClosest [3]float32
	bestTri := int32(-1)
	for _, ti := range cands {
		f := model.Faces[ti]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]
		cp, dSq := closestPointOnTriangle3D(p, v0, v1, v2)
		if dSq < bestDistSq {
			bestDistSq = dSq
			bestClosest = cp
			bestTri = ti
		}
	}
	dist := float32(math.Sqrt(float64(bestDistSq)))

	if bestTri < 0 {
		return dist // no triangle found, outside
	}

	// Points within shellThickness of the surface are always inside.
	// This ensures thin/non-watertight features get a minimum-thickness shell.
	if dist < shellThickness {
		return -dist
	}

	// For points further from the surface, use closest-surface-normal for sign.
	f := model.Faces[bestTri]
	v0 := model.Vertices[f[0]]
	v1 := model.Vertices[f[1]]
	v2 := model.Vertices[f[2]]
	e1 := [3]float32{v1[0] - v0[0], v1[1] - v0[1], v1[2] - v0[2]}
	e2 := [3]float32{v2[0] - v0[0], v2[1] - v0[1], v2[2] - v0[2]}
	normal := [3]float32{
		e1[1]*e2[2] - e1[2]*e2[1],
		e1[2]*e2[0] - e1[0]*e2[2],
		e1[0]*e2[1] - e1[1]*e2[0],
	}

	dx := p[0] - bestClosest[0]
	dy := p[1] - bestClosest[1]
	dz := p[2] - bestClosest[2]
	dot := dx*normal[0] + dy*normal[1] + dz*normal[2]

	if dot < 0 {
		return -dist // inside
	}
	return dist // outside
}

// Remesh generates a hexagonal voxel shell of the input model using marching
// prisms for smooth isosurface extraction.
func Remesh(model *loader.LoadedModel, pal [][3]uint8, cfg Config, dither bool) (*loader.LoadedModel, []int32, error) {
	if len(model.Vertices) == 0 || len(model.Faces) == 0 {
		return nil, nil, fmt.Errorf("empty model")
	}

	nozzle := cfg.NozzleDiameter
	layerH := cfg.LayerHeight
	// Hex flat-to-flat spacing. Using 1.5× nozzle diameter because at 1.0×
	// the hexes are too small for the slicer to fill reliably — bottom/top
	// surfaces don't become solid. Tested with a 0.4mm nozzle: 1.5× (0.6mm)
	// gives solid first layers while preserving good color resolution.
	hexFlat := nozzle * 1.5
	size := hexFlat / float32(math.Sqrt(3)) // center-to-vertex

	// 1. Bounding box.
	minV, maxV := computeBounds(model.Vertices)
	pad := hexFlat * 2
	minV[0] -= pad
	minV[1] -= pad
	minV[2] -= pad
	maxV[0] += pad
	maxV[1] += pad
	maxV[2] += pad

	// Grid dimensions.
	colStep := 1.5 * size            // horizontal spacing
	rowStep := hexFlat                // vertical spacing (flat-to-flat)
	nCols := int(math.Ceil(float64(maxV[0]-minV[0])/float64(colStep))) + 1
	nRows := int(math.Ceil(float64(maxV[1]-minV[1])/float64(rowStep))) + 1
	nLayers := int(math.Ceil(float64(maxV[2]-minV[2])/float64(layerH))) + 1

	fmt.Printf("  Hex grid: %d cols x %d rows x %d layers\n", nCols, nRows, nLayers)

	// 2. Spatial index.
	si := newSpatialIndex(model, hexFlat*2)

	// Precompute hex vertex offsets (flat-top hexagon).
	var hexOffsetsX, hexOffsetsY [6]float32
	for i := 0; i < 6; i++ {
		angle := float64(i) * math.Pi / 3.0
		hexOffsetsX[i] = size * float32(math.Cos(angle))
		hexOffsetsY[i] = size * float32(math.Sin(angle))
	}

	// Helper to compute vertex position.
	vertPos := func(col, row, vertLayer, corner int) [3]float32 {
		cx := minV[0] + float32(col)*colStep
		cy := minV[1] + float32(row)*rowStep
		if col%2 == 1 {
			cy += rowStep / 2
		}
		z := minV[2] + float32(vertLayer)*layerH - layerH/2
		if corner < 6 {
			return [3]float32{cx + hexOffsetsX[corner], cy + hexOffsetsY[corner], z}
		}
		return [3]float32{cx, cy, z}
	}

	// Round position to avoid floating-point dedup issues.
	snapPos := func(p [3]float32) [3]float32 {
		const scale = 1e3
		return [3]float32{
			float32(math.Round(float64(p[0])*scale)) / scale,
			float32(math.Round(float64(p[1])*scale)) / scale,
			float32(math.Round(float64(p[2])*scale)) / scale,
		}
	}

	// 3. Ray-parity voxelization to determine active hexes and their colors.
	var hexes []activeHex
	colorBuf := newSearchBuf(len(model.Faces))
	colorRadius := hexFlat * 3

	for col := 0; col < nCols; col++ {
		cx := minV[0] + float32(col)*colStep
		for row := 0; row < nRows; row++ {
			cy := minV[1] + float32(row)*rowStep
			if col%2 == 1 {
				cy += rowStep / 2
			}

			// Find all Z-ray intersections with the mesh at (cx, cy).
			cands := si.candidates(cx, cy)
			var hits []surfaceHit
			for _, ti := range cands {
				f := model.Faces[ti]
				v0 := model.Vertices[f[0]]
				v1 := model.Vertices[f[1]]
				v2 := model.Vertices[f[2]]

				inside, bary := pointInTriangleXY(cx, cy, v0, v1, v2)
				if !inside {
					continue
				}

				z := bary[0]*v0[2] + bary[1]*v1[2] + bary[2]*v2[2]
				hits = append(hits, surfaceHit{z: z, meshIdx: model.FaceMeshIdx[ti]})
			}

			if len(hits) == 0 {
				continue
			}

			// Compute active layers as union of per-mesh Z-ray parity.
			// This prevents separate mesh objects from contaminating each other's
			// inside/outside determination.
			activeLayers := make(map[int]struct{})
			meshHits := make(map[int32][]float32)
			for _, h := range hits {
				meshHits[h.meshIdx] = append(meshHits[h.meshIdx], h.z)
			}
			for _, mHits := range meshHits {
				sort.Slice(mHits, func(i, j int) bool { return mHits[i] < mHits[j] })
				// Deduplicate near-coincident hits within this mesh.
				deduped := mHits[:1]
				for i := 1; i < len(mHits); i++ {
					if mHits[i]-deduped[len(deduped)-1] > layerH/2 {
						deduped = append(deduped, mHits[i])
					}
				}
				if len(deduped)%2 != 0 {
					deduped = deduped[:len(deduped)-1]
				}
				for p := 0; p+1 < len(deduped); p += 2 {
					enterZ := deduped[p]
					exitZ := deduped[p+1]
					layMin := int(math.Ceil(float64(enterZ-minV[2]) / float64(layerH)))
					layMax := int(math.Floor(float64(exitZ-minV[2]) / float64(layerH)))
					if layMin < 0 {
						layMin = 0
					}
					if layMax >= nLayers {
						layMax = nLayers - 1
					}
					for layer := layMin; layer <= layMax; layer++ {
						activeLayers[layer] = struct{}{}
					}
				}
			}

			// Also activate layers at each hit Z, regardless of parity.
			// This ensures thin features with non-watertight meshes (where
			// Z-ray parity fails due to gaps) still get a surface shell.
			for _, h := range hits {
				layer := int(math.Round(float64(h.z-minV[2]) / float64(layerH)))
				if layer >= 0 && layer < nLayers {
					activeLayers[layer] = struct{}{}
				}
			}

			for layer := range activeLayers {
				cz := minV[2] + float32(layer)*layerH
				clr := sampleNearestColor(
					[3]float32{cx, cy, cz},
					model, si, colorRadius, colorBuf)
				hexes = append(hexes, activeHex{
					col: col, row: row, layer: layer,
					cx: cx, cy: cy, cz: cz,
					color: clr,
				})
			}
		}
	}

	// Proximity-based supplementary voxelization: for each triangle in the
	// mesh, activate all hex cells that overlap the triangle's bounding box.
	// This catches thin/non-watertight features that Z-ray parity misses.
	proximitySet := make(map[hexKey]struct{})
	for fi := range model.Faces {
		f := model.Faces[fi]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]
		// Triangle bounding box.
		tMinX := min(v0[0], min(v1[0], v2[0]))
		tMaxX := max(v0[0], max(v1[0], v2[0]))
		tMinY := min(v0[1], min(v1[1], v2[1]))
		tMaxY := max(v0[1], max(v1[1], v2[1]))
		tMinZ := min(v0[2], min(v1[2], v2[2]))
		tMaxZ := max(v0[2], max(v1[2], v2[2]))
		// Map to hex grid range with one cell padding.
		colMin := int(math.Floor(float64(tMinX-minV[0])/float64(colStep))) - 1
		colMax := int(math.Ceil(float64(tMaxX-minV[0])/float64(colStep))) + 1
		layerMin := int(math.Floor(float64(tMinZ-minV[2])/float64(layerH))) - 1
		layerMax := int(math.Ceil(float64(tMaxZ-minV[2])/float64(layerH))) + 1
		if colMin < 0 {
			colMin = 0
		}
		if colMax >= nCols {
			colMax = nCols - 1
		}
		if layerMin < 0 {
			layerMin = 0
		}
		if layerMax >= nLayers {
			layerMax = nLayers - 1
		}
		for col := colMin; col <= colMax; col++ {
			rowOff := float32(0)
			if col%2 == 1 {
				rowOff = rowStep / 2
			}
			rMin := int(math.Floor(float64(tMinY-minV[1]-rowOff)/float64(rowStep))) - 1
			rMax := int(math.Ceil(float64(tMaxY-minV[1]-rowOff)/float64(rowStep))) + 1
			if rMin < 0 {
				rMin = 0
			}
			if rMax >= nRows {
				rMax = nRows - 1
			}
			for row := rMin; row <= rMax; row++ {
				hcx := minV[0] + float32(col)*colStep
				hcy := minV[1] + float32(row)*rowStep + rowOff
				// Check if hex center is within hex spacing of triangle bbox.
				if hcx < tMinX-hexFlat || hcx > tMaxX+hexFlat ||
					hcy < tMinY-hexFlat || hcy > tMaxY+hexFlat {
					continue
				}
				for layer := layerMin; layer <= layerMax; layer++ {
					proximitySet[hexKey{col, row, layer}] = struct{}{}
				}
			}
		}
	}
	// Add proximity hexes that weren't already found by Z-ray.
	existingHexes := make(map[hexKey]struct{}, len(hexes))
	for _, h := range hexes {
		existingHexes[hexKey{h.col, h.row, h.layer}] = struct{}{}
	}
	proximityAdded := 0
	for k := range proximitySet {
		if _, exists := existingHexes[k]; !exists {
			cx := minV[0] + float32(k.col)*colStep
			cy := minV[1] + float32(k.row)*rowStep
			if k.col%2 == 1 {
				cy += rowStep / 2
			}
			cz := minV[2] + float32(k.layer)*layerH
			clr := sampleNearestColor(
				[3]float32{cx, cy, cz},
				model, si, colorRadius, colorBuf)
			hexes = append(hexes, activeHex{
				col: k.col, row: k.row, layer: k.layer,
				cx: cx, cy: cy, cz: cz,
				color: clr,
			})
			proximityAdded++
		}
	}

	hexes = deduplicateHexes(hexes)
	if proximityAdded > 0 {
		fmt.Printf("  %d active hex prisms (%d added by surface proximity)\n", len(hexes), proximityAdded)
	} else {
		fmt.Printf("  %d active hex prisms\n", len(hexes))
	}
	if len(hexes) == 0 {
		return nil, nil, fmt.Errorf("no active hex prisms found")
	}

	// Build hex assignment map for color lookup during marching prisms.
	hexAssignMap := make(map[hexKey]int, len(hexes))
	activeSet := make(map[hexKey]struct{}, len(hexes))
	for i, h := range hexes {
		k := hexKey{h.col, h.row, h.layer}
		hexAssignMap[k] = i
		activeSet[k] = struct{}{}
	}

	// Expand active set by one ring of neighbors. The surface passes through
	// boundary cells where some vertices are inside and some outside. Cells
	// just outside the ray-parity active set may have inside-vertices and
	// need to be processed by marching prisms.
	// Lateral neighbor offsets by column parity.
	lateralOffsets := [2][6][2]int{
		// Even column
		{{+1, 0}, {0, +1}, {-1, 0}, {-1, -1}, {0, -1}, {+1, -1}},
		// Odd column
		{{+1, +1}, {0, +1}, {-1, +1}, {-1, 0}, {0, -1}, {+1, 0}},
	}
	expandedSet := make(map[hexKey]struct{}, len(hexes)*2)
	for k := range activeSet {
		expandedSet[k] = struct{}{}
	}
	// Expand by 2 rings to cover diagonal neighbors (lateral+vertical).
	// Without this, cells at edges (e.g. top rim of a cylinder) where
	// the surface curves through diagonally adjacent cells are missed.
	for ring := 0; ring < 2; ring++ {
		snapshot := make([]hexKey, 0, len(expandedSet))
		for k := range expandedSet {
			snapshot = append(snapshot, k)
		}
		for _, k := range snapshot {
			expandedSet[hexKey{k.col, k.row, k.layer - 1}] = struct{}{}
			expandedSet[hexKey{k.col, k.row, k.layer + 1}] = struct{}{}
			parity := k.col & 1
			for _, off := range lateralOffsets[parity] {
				expandedSet[hexKey{k.col + off[0], k.row + off[1], k.layer}] = struct{}{}
			}
		}
	}
	fmt.Printf("  %d cells in expanded set (from %d active)\n", len(expandedSet), len(hexes))

	// 4. Compute SDF at vertices of expanded cell set.
	fmt.Println("  Computing SDF at cell vertices...")
	searchRadius := hexFlat * 3
	shellThickness := layerH // vertices within one layer height of the surface are forced inside

	// Collect unique vertex positions.
	uniqueSet := make(map[[3]float32]struct{})
	for k := range expandedSet {
		for _, vl := range [2]int{k.layer, k.layer + 1} {
			for corner := 0; corner <= 6; corner++ {
				uniqueSet[snapPos(vertPos(k.col, k.row, vl, corner))] = struct{}{}
			}
		}
	}
	uniqueVerts := make([][3]float32, 0, len(uniqueSet))
	for pos := range uniqueSet {
		uniqueVerts = append(uniqueVerts, pos)
	}

	// Compute SDF in parallel.
	nWorkers := runtime.NumCPU()
	sdfValues := make([]float32, len(uniqueVerts))
	var wg sync.WaitGroup
	chunkSize := (len(uniqueVerts) + nWorkers - 1) / nWorkers
	for w := 0; w < nWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if end > len(uniqueVerts) {
			end = len(uniqueVerts)
		}
		if start >= end {
			break
		}
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			buf := newSearchBuf(len(model.Faces))
			for i := start; i < end; i++ {
				sdfValues[i] = computeSDF(uniqueVerts[i], model, si, searchRadius, shellThickness, buf)
			}
		}(start, end)
	}
	wg.Wait()

	sdfMap := make(map[[3]float32]float32, len(uniqueVerts))
	for i, pos := range uniqueVerts {
		sdfMap[pos] = sdfValues[i]
	}
	fmt.Printf("  %d unique SDF vertices computed\n", len(sdfMap))

	// 5. Palette assignment / dithering.
	var assignments []int32
	if dither {
		assignments = ditherHexes(hexes, pal)
	} else {
		hexColors := make([][3]uint8, len(hexes))
		for i, h := range hexes {
			hexColors[i] = h.color
		}
		assignments = palette.AssignPalette(hexColors, pal)
	}

	// 6. Marching prisms isosurface extraction.
	fmt.Println("  Extracting isosurface with marching prisms...")
	outVerts := make([][3]float32, 0)
	outFaces := make([][3]uint32, 0)
	outAssignments := make([]int32, 0)

	// Vertex deduplication by snapped position.
	vertexMap := make(map[[3]float32]uint32)
	getVertex := func(pos [3]float32) uint32 {
		snapped := snapPos(pos)
		if idx, ok := vertexMap[snapped]; ok {
			return idx
		}
		idx := uint32(len(outVerts))
		vertexMap[snapped] = idx
		outVerts = append(outVerts, pos)
		return idx
	}

	// For each cell in the expanded set, decompose into 6 wedges and run
	// marching prisms. Use the nearest active cell's color assignment.
	for k := range expandedSet {
		// Determine color assignment: use this cell if active, else nearest active neighbor.
		assignment := int32(0)
		if hi, ok := hexAssignMap[k]; ok {
			assignment = assignments[hi]
		} else {
			// Find nearest active neighbor for color.
			parity := k.col & 1
			found := false
			// Check vertical neighbors first (same column).
			for _, dl := range []int{-1, 1} {
				nk := hexKey{k.col, k.row, k.layer + dl}
				if hi, ok := hexAssignMap[nk]; ok {
					assignment = assignments[hi]
					found = true
					break
				}
			}
			if !found {
				for _, off := range lateralOffsets[parity] {
					nk := hexKey{k.col + off[0], k.row + off[1], k.layer}
					if hi, ok := hexAssignMap[nk]; ok {
						assignment = assignments[hi]
						break
					}
				}
			}
		}

		botLayer := k.layer
		topLayer := k.layer + 1

		// Get SDF values at all 14 positions.
		// Use snapped positions for interpolation so that shared vertices
		// between adjacent hex prisms produce identical results.
		var sdfCornerBot, sdfCornerTop [6]float32
		var posCornerBot, posCornerTop [6][3]float32
		for c := 0; c < 6; c++ {
			posCornerBot[c] = snapPos(vertPos(k.col, k.row, botLayer, c))
			posCornerTop[c] = snapPos(vertPos(k.col, k.row, topLayer, c))
			sdfCornerBot[c] = sdfMap[posCornerBot[c]]
			sdfCornerTop[c] = sdfMap[posCornerTop[c]]
		}
		posCenterBot := snapPos(vertPos(k.col, k.row, botLayer, 6))
		posCenterTop := snapPos(vertPos(k.col, k.row, topLayer, 6))
		sdfCenterBot := sdfMap[posCenterBot]
		sdfCenterTop := sdfMap[posCenterTop]

		// Process 6 wedges.
		for w := 0; w < 6; w++ {
			next := (w + 1) % 6

			// VTK wedge convention:
			// v0=center_bot, v1=corner_w_bot, v2=corner_next_bot
			// v3=center_top, v4=corner_w_top, v5=corner_next_top
			wedgePos := [6][3]float32{
				posCenterBot, posCornerBot[w], posCornerBot[next],
				posCenterTop, posCornerTop[w], posCornerTop[next],
			}
			wedgeSDF := [6]float32{
				sdfCenterBot, sdfCornerBot[w], sdfCornerBot[next],
				sdfCenterTop, sdfCornerTop[w], sdfCornerTop[next],
			}

			// Build case index: bit i set if vertex i is inside (SDF < 0).
			caseIdx := 0
			for i := 0; i < 6; i++ {
				if wedgeSDF[i] < 0 {
					caseIdx |= 1 << i
				}
			}

			triEdges := wedgeTriTable[caseIdx]
			if len(triEdges) == 0 {
				continue
			}

			// For each edge in the triangulation, interpolate vertex position.
			for t := 0; t+2 < len(triEdges); t += 3 {
				var triVerts [3]uint32
				for k := 0; k < 3; k++ {
					edge := triEdges[t+k]
					ea := wedgeEdges[edge][0]
					eb := wedgeEdges[edge][1]
					posA := wedgePos[ea]
					posB := wedgePos[eb]
					sdfA := wedgeSDF[ea]
					sdfB := wedgeSDF[eb]

					// Canonicalize edge direction so shared edges between
					// adjacent wedges always interpolate the same way.
					// Without this, A→B vs B→A gives different float results.
					if posA[0] > posB[0] || (posA[0] == posB[0] && posA[1] > posB[1]) ||
						(posA[0] == posB[0] && posA[1] == posB[1] && posA[2] > posB[2]) {
						posA, posB = posB, posA
						sdfA, sdfB = sdfB, sdfA
					}

					// Interpolate along edge at zero-crossing.
					var interp [3]float32
					denom := sdfB - sdfA
					if denom == 0 {
						// Both same sign at zero — midpoint.
						interp = [3]float32{
							(posA[0] + posB[0]) / 2,
							(posA[1] + posB[1]) / 2,
							(posA[2] + posB[2]) / 2,
						}
					} else {
						frac := -sdfA / denom
						frac = clampF(frac, 0, 1)
						interp = [3]float32{
							posA[0] + frac*(posB[0]-posA[0]),
							posA[1] + frac*(posB[1]-posA[1]),
							posA[2] + frac*(posB[2]-posA[2]),
						}
					}
					triVerts[k] = getVertex(interp)
				}
				// Skip degenerate triangles (two or more vertices at the same snapped position).
				if triVerts[0] == triVerts[1] || triVerts[1] == triVerts[2] || triVerts[0] == triVerts[2] {
					continue
				}
				outFaces = append(outFaces, [3]uint32{triVerts[0], triVerts[1], triVerts[2]})
				outAssignments = append(outAssignments, assignment)
			}
		}
	}

	fmt.Printf("  %d vertices, %d faces after marching prisms\n", len(outVerts), len(outFaces))

	// Merge coplanar same-material triangles to reduce face count.
	if !cfg.NoMerge {
		before := len(outFaces)
		outFaces, outAssignments = mergeCoplanarTriangles(outVerts, outFaces, outAssignments)
		fmt.Printf("  %d faces after coplanar merge (%.0f%% reduction)\n",
			len(outFaces), 100*float64(before-len(outFaces))/float64(before))
	}

	// Build output model.
	uvs := make([][2]float32, len(outVerts))
	faceTex := make([]int32, len(outFaces))

	placeholder := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	placeholder.SetNRGBA(0, 0, color.NRGBA{128, 128, 128, 255})
	var textures []image.Image
	if len(model.Textures) > 0 {
		textures = model.Textures[:1]
	} else {
		textures = []image.Image{placeholder}
	}

	return &loader.LoadedModel{
		Vertices:       outVerts,
		Faces:          outFaces,
		UVs:            uvs,
		Textures:       textures,
		FaceTextureIdx: faceTex,
	}, outAssignments, nil
}

// sampleNearestColor finds the closest surface point to p, then samples the
// texture color there. This gives each hex cell a color based on the actual
// nearby surface rather than just the Z-ray enter/exit points.
func sampleNearestColor(p [3]float32, model *loader.LoadedModel, si *spatialIndex, radius float32, buf *searchBuf) [3]uint8 {
	cands := si.candidatesRadiusZ(p[0], p[1], radius, p[2], radius, buf)
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

		a := dot3(e0, e0)
		b := dot3(e0, e1)
		c := dot3(e1, e1)
		dd := dot3(e0, d)
		e := dot3(e1, d)

		det := a*c - b*b
		s := b*e - c*dd
		t := b*dd - a*e

		if s+t <= det {
			if s < 0 {
				if t < 0 {
					if dd < 0 {
						t = 0
						s = clampF(-dd/a, 0, 1)
					} else {
						s = 0
						t = clampF(-e/c, 0, 1)
					}
				} else {
					s = 0
					t = clampF(-e/c, 0, 1)
				}
			} else if t < 0 {
				t = 0
				s = clampF(-dd/a, 0, 1)
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
					s = clampF(numer/denom, 0, 1)
					t = 1 - s
				} else {
					s = 0
					t = clampF(-e/c, 0, 1)
				}
			} else if t < 0 {
				tmp0 := b + e
				tmp1 := a + dd
				if tmp1 > tmp0 {
					numer := tmp1 - tmp0
					denom := a - 2*b + c
					t = clampF(numer/denom, 0, 1)
					s = 1 - t
				} else {
					t = 0
					s = clampF(-dd/a, 0, 1)
				}
			} else {
				numer := (c + e) - (b + dd)
				if numer <= 0 {
					s = 0
				} else {
					denom := a - 2*b + c
					s = clampF(numer/denom, 0, 1)
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
		return [3]uint8{128, 128, 128}
	}

	texIdx := model.FaceTextureIdx[bestTri]
	if texIdx < 0 || int(texIdx) >= len(model.Textures) {
		return [3]uint8{128, 128, 128}
	}

	// Barycentric coordinates: (1-s-t, s, t) in the v0/v1/v2 basis.
	bary := [3]float32{1 - bestS - bestT, bestS, bestT}
	f := model.Faces[bestTri]
	uv0 := model.UVs[f[0]]
	uv1 := model.UVs[f[1]]
	uv2 := model.UVs[f[2]]

	u := bary[0]*uv0[0] + bary[1]*uv1[0] + bary[2]*uv2[0]
	v := bary[0]*uv0[1] + bary[1]*uv1[1] + bary[2]*uv2[1]

	return bilinearSample(model.Textures[texIdx], u, v)
}

func deduplicateHexes(hexes []activeHex) []activeHex {
	seen := make(map[hexKey]int, len(hexes))
	var result []activeHex

	for _, h := range hexes {
		k := hexKey{h.col, h.row, h.layer}
		if _, ok := seen[k]; !ok {
			seen[k] = len(result)
			result = append(result, h)
		}
	}
	return result
}

// ditherHexes applies Floyd-Steinberg error diffusion over hexes in spatial order.
func ditherHexes(hexes []activeHex, pal [][3]uint8) []int32 {
	order := make([]int, len(hexes))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		ha, hb := hexes[order[a]], hexes[order[b]]
		if ha.layer != hb.layer {
			return ha.layer < hb.layer
		}
		if ha.row != hb.row {
			return ha.row < hb.row
		}
		return ha.col < hb.col
	})

	assignments := make([]int32, len(hexes))
	errBuf := make([][3]float32, len(hexes)+4)

	for i, idx := range order {
		r := clampF(float32(hexes[idx].color[0])+errBuf[i][0], 0, 255)
		g := clampF(float32(hexes[idx].color[1])+errBuf[i][1], 0, 255)
		b := clampF(float32(hexes[idx].color[2])+errBuf[i][2], 0, 255)

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

		chosen := pal[bestIdx]
		eR := r - float32(chosen[0])
		eG := g - float32(chosen[1])
		eB := b - float32(chosen[2])

		weights := [4]float32{7.0 / 16.0, 5.0 / 16.0, 3.0 / 16.0, 1.0 / 16.0}
		for k := 0; k < 4 && i+1+k < len(hexes); k++ {
			errBuf[i+1+k][0] += eR * weights[k]
			errBuf[i+1+k][1] += eG * weights[k]
			errBuf[i+1+k][2] += eB * weights[k]
		}
	}

	return assignments
}

// --- Marching Prisms (Wedge) Lookup Tables ---
// From VTK's vtkWedge.cxx. Vertex convention:
// 0,1,2 = bottom triangle; 3,4,5 = top triangle (0↔3, 1↔4, 2↔5).

// wedgeEdges defines the 9 edges of a triangular prism by vertex pairs.
var wedgeEdges = [9][2]int{
	{0, 1}, // edge 0: bottom
	{1, 2}, // edge 1: bottom
	{2, 0}, // edge 2: bottom
	{3, 4}, // edge 3: top
	{4, 5}, // edge 4: top
	{5, 3}, // edge 5: top
	{0, 3}, // edge 6: vertical
	{1, 4}, // edge 7: vertical
	{2, 5}, // edge 8: vertical
}

// wedgeTriTable is the marching prisms triangle table. Each entry is a list
// of edge indices in triples forming output triangles.
// Case index is a 6-bit mask: bit i set if vertex i is inside (SDF < 0).
var wedgeTriTable = [64][]int{
	/*  0 */ {},
	/*  1 */ {0, 6, 2},
	/*  2 */ {0, 1, 7},
	/*  3 */ {6, 1, 7, 6, 2, 1},
	/*  4 */ {1, 2, 8},
	/*  5 */ {6, 1, 0, 6, 8, 1},
	/*  6 */ {0, 2, 8, 7, 0, 8},
	/*  7 */ {7, 6, 8},
	/*  8 */ {3, 5, 6},
	/*  9 */ {3, 5, 0, 5, 2, 0},
	/* 10 */ {0, 1, 7, 6, 3, 5},
	/* 11 */ {1, 7, 3, 1, 3, 5, 1, 5, 2},
	/* 12 */ {2, 8, 1, 6, 3, 5},
	/* 13 */ {0, 3, 1, 1, 3, 5, 1, 5, 8},
	/* 14 */ {6, 3, 5, 0, 8, 7, 0, 2, 8},
	/* 15 */ {7, 3, 5, 7, 5, 8},
	/* 16 */ {7, 4, 3},
	/* 17 */ {7, 4, 3, 0, 6, 2},
	/* 18 */ {0, 1, 3, 1, 4, 3},
	/* 19 */ {1, 4, 3, 1, 3, 6, 1, 6, 2},
	/* 20 */ {7, 4, 3, 2, 8, 1},
	/* 21 */ {7, 4, 3, 6, 1, 0, 6, 8, 1},
	/* 22 */ {0, 4, 3, 0, 8, 4, 0, 2, 8},
	/* 23 */ {6, 8, 3, 3, 8, 4},
	/* 24 */ {6, 7, 4, 6, 4, 5},
	/* 25 */ {0, 7, 5, 7, 4, 5, 2, 0, 5},
	/* 26 */ {1, 6, 0, 1, 5, 6, 1, 4, 5},
	/* 27 */ {2, 1, 5, 5, 1, 4},
	/* 28 */ {2, 8, 1, 6, 7, 5, 7, 4, 5},
	/* 29 */ {0, 7, 5, 7, 4, 5, 0, 5, 1, 1, 5, 8},
	/* 30 */ {0, 2, 8, 0, 8, 4, 0, 4, 5, 0, 5, 6},
	/* 31 */ {8, 4, 5},
	/* 32 */ {4, 8, 5},
	/* 33 */ {4, 8, 5, 0, 6, 2},
	/* 34 */ {4, 8, 5, 0, 1, 7},
	/* 35 */ {4, 8, 5, 6, 1, 7, 6, 2, 1},
	/* 36 */ {1, 5, 4, 2, 5, 1},
	/* 37 */ {1, 5, 4, 1, 6, 5, 1, 0, 6},
	/* 38 */ {5, 4, 7, 5, 7, 0, 5, 0, 2},
	/* 39 */ {6, 4, 7, 6, 5, 4},
	/* 40 */ {6, 3, 8, 3, 4, 8},
	/* 41 */ {0, 3, 4, 0, 4, 8, 0, 8, 2},
	/* 42 */ {7, 0, 1, 6, 3, 4, 6, 4, 8},
	/* 43 */ {1, 7, 3, 1, 3, 2, 2, 3, 8, 8, 3, 4},
	/* 44 */ {2, 6, 1, 6, 3, 1, 3, 4, 1},
	/* 45 */ {0, 3, 1, 1, 3, 4},
	/* 46 */ {7, 0, 4, 4, 0, 2, 4, 2, 3, 3, 2, 6},
	/* 47 */ {7, 3, 4},
	/* 48 */ {7, 8, 5, 7, 5, 3},
	/* 49 */ {0, 6, 2, 7, 8, 5, 7, 5, 3},
	/* 50 */ {0, 1, 3, 1, 5, 3, 1, 8, 5},
	/* 51 */ {2, 1, 6, 6, 1, 3, 5, 1, 8, 3, 1, 5},
	/* 52 */ {1, 3, 7, 1, 5, 3, 1, 2, 5},
	/* 53 */ {1, 0, 6, 1, 6, 5, 1, 5, 7, 7, 5, 3},
	/* 54 */ {0, 2, 5, 0, 5, 3},
	/* 55 */ {3, 6, 5},
	/* 56 */ {7, 8, 6},
	/* 57 */ {0, 7, 8, 0, 8, 2},
	/* 58 */ {0, 1, 6, 1, 8, 6},
	/* 59 */ {2, 1, 8},
	/* 60 */ {6, 7, 1, 6, 1, 2},
	/* 61 */ {0, 7, 1},
	/* 62 */ {0, 2, 6},
	/* 63 */ {},
}

func computeBounds(verts [][3]float32) ([3]float32, [3]float32) {
	minV := verts[0]
	maxV := verts[0]
	for _, v := range verts[1:] {
		for i := 0; i < 3; i++ {
			if v[i] < minV[i] {
				minV[i] = v[i]
			}
			if v[i] > maxV[i] {
				maxV[i] = v[i]
			}
		}
	}
	return minV, maxV
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

func minf(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

func maxf(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

// triNormal returns the unit normal of a triangle.
func triNormal(a, b, c [3]float32) [3]float32 {
	ab := [3]float32{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
	ac := [3]float32{c[0] - a[0], c[1] - a[1], c[2] - a[2]}
	n := [3]float32{
		ab[1]*ac[2] - ab[2]*ac[1],
		ab[2]*ac[0] - ab[0]*ac[2],
		ab[0]*ac[1] - ab[1]*ac[0],
	}
	l := float32(math.Sqrt(float64(n[0]*n[0] + n[1]*n[1] + n[2]*n[2])))
	if l < 1e-12 {
		return [3]float32{}
	}
	return [3]float32{n[0] / l, n[1] / l, n[2] / l}
}

func cross3f(a, b [3]float32) [3]float32 {
	return [3]float32{
		a[1]*b[2] - a[2]*b[1],
		a[2]*b[0] - a[0]*b[2],
		a[0]*b[1] - a[1]*b[0],
	}
}

// mergeCoplanarTriangles reduces triangle count by finding connected groups of
// coplanar same-material triangles, extracting each group's boundary polygon,
// and re-triangulating with ear clipping. Boundary edges/vertices are always
// preserved so no non-manifold edges are introduced.
func mergeCoplanarTriangles(verts [][3]float32, faces [][3]uint32, assignments []int32) ([][3]uint32, []int32) {
	nFaces := len(faces)

	// Build edge → face adjacency.
	type edgeKey struct{ a, b uint32 }
	makeEdge := func(a, b uint32) edgeKey {
		if a > b {
			a, b = b, a
		}
		return edgeKey{a, b}
	}
	edgeFaces := make(map[edgeKey][]int32, nFaces*3)
	for fi, f := range faces {
		for i := 0; i < 3; i++ {
			e := makeEdge(f[i], f[(i+1)%3])
			edgeFaces[e] = append(edgeFaces[e], int32(fi))
		}
	}

	// Compute unit normals.
	normals := make([][3]float32, nFaces)
	for fi, f := range faces {
		normals[fi] = triNormal(verts[f[0]], verts[f[1]], verts[f[2]])
	}

	// Flood-fill to find coplanar same-material groups.
	const cosThresh float32 = 0.9999
	groupID := make([]int, nFaces)
	for i := range groupID {
		groupID[i] = -1
	}
	var groups [][]int

	for fi := 0; fi < nFaces; fi++ {
		if groupID[fi] >= 0 {
			continue
		}
		gid := len(groups)
		group := []int{fi}
		groupID[fi] = gid

		queue := []int{fi}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			f := faces[cur]
			for i := 0; i < 3; i++ {
				e := makeEdge(f[i], f[(i+1)%3])
				for _, nfi := range edgeFaces[e] {
					if int(nfi) == cur || groupID[nfi] >= 0 {
						continue
					}
					if assignments[nfi] != assignments[fi] {
						continue
					}
					dot := normals[fi][0]*normals[nfi][0] + normals[fi][1]*normals[nfi][1] + normals[fi][2]*normals[nfi][2]
					if dot < cosThresh {
						continue
					}
					groupID[nfi] = gid
					group = append(group, int(nfi))
					queue = append(queue, int(nfi))
				}
			}
		}
		groups = append(groups, group)
	}

	// Process each group: extract boundary, ear-clip if beneficial.
	newFaces := make([][3]uint32, 0, nFaces)
	newAssignments := make([]int32, 0, nFaces)
	replaced := make([]bool, nFaces)

	for _, group := range groups {
		if len(group) < 2 {
			continue
		}

		// Identify boundary edges: edges with only 1 face in this group.
		groupSet := make(map[int]bool, len(group))
		for _, fi := range group {
			groupSet[fi] = true
		}

		type dirEdge struct{ from, to uint32 }
		var boundary []dirEdge

		for _, fi := range group {
			f := faces[fi]
			for i := 0; i < 3; i++ {
				a, b := f[i], f[(i+1)%3]
				e := makeEdge(a, b)
				isBoundary := true
				for _, nfi := range edgeFaces[e] {
					if int(nfi) != fi && groupSet[int(nfi)] {
						isBoundary = false
						break
					}
				}
				if isBoundary {
					boundary = append(boundary, dirEdge{a, b})
				}
			}
		}

		if len(boundary) < 3 {
			continue
		}

		// Build directed adjacency for boundary walk.
		nextMap := make(map[uint32]uint32, len(boundary))
		valid := true
		for _, de := range boundary {
			if _, exists := nextMap[de.from]; exists {
				valid = false // branching boundary, not a simple loop
				break
			}
			nextMap[de.from] = de.to
		}
		if !valid {
			continue
		}

		// Walk the boundary loop.
		start := boundary[0].from
		loop := []uint32{start}
		cur := nextMap[start]
		loopOK := true
		for cur != start {
			loop = append(loop, cur)
			next, ok := nextMap[cur]
			if !ok || len(loop) > len(boundary)+1 {
				loopOK = false
				break
			}
			cur = next
		}
		if !loopOK || len(loop) != len(boundary) {
			continue
		}

		// Skip if ear clipping won't reduce triangle count.
		if len(loop)-2 >= len(group) {
			continue
		}

		// Project boundary to 2D using the group normal.
		n := normals[group[0]]
		var u, v [3]float32
		if n[0]*n[0] < 0.81 { // abs(n[0]) < 0.9
			u = cross3f(n, [3]float32{1, 0, 0})
		} else {
			u = cross3f(n, [3]float32{0, 1, 0})
		}
		uLen := float32(math.Sqrt(float64(u[0]*u[0] + u[1]*u[1] + u[2]*u[2])))
		u[0] /= uLen
		u[1] /= uLen
		u[2] /= uLen
		v = cross3f(n, u)

		pts := make([][2]float64, len(loop))
		for i, vi := range loop {
			p := verts[vi]
			pts[i] = [2]float64{
				float64(p[0]*u[0] + p[1]*u[1] + p[2]*u[2]),
				float64(p[0]*v[0] + p[1]*v[1] + p[2]*v[2]),
			}
		}

		earTris := earClip(pts)
		if earTris == nil || len(earTris) >= len(group) {
			continue
		}

		// Replace group with ear-clipped triangles.
		for _, fi := range group {
			replaced[fi] = true
		}
		for _, tri := range earTris {
			v0, v1, v2 := loop[tri[0]], loop[tri[1]], loop[tri[2]]
			// Skip degenerate triangles (same index or same position).
			if v0 == v1 || v1 == v2 || v0 == v2 {
				continue
			}
			if verts[v0] == verts[v1] || verts[v1] == verts[v2] || verts[v0] == verts[v2] {
				continue
			}
			newFaces = append(newFaces, [3]uint32{v0, v1, v2})
			newAssignments = append(newAssignments, assignments[group[0]])
		}
	}

	// Add unreplaced faces.
	for fi, f := range faces {
		if !replaced[fi] {
			newFaces = append(newFaces, f)
			newAssignments = append(newAssignments, assignments[fi])
		}
	}

	return newFaces, newAssignments
}

// earClip triangulates a 2D polygon using ear clipping.
// Returns triangle indices into the pts array, or nil on failure.
func earClip(pts [][2]float64) [][3]int {
	n := len(pts)
	if n < 3 {
		return nil
	}
	if n == 3 {
		return [][3]int{{0, 1, 2}}
	}

	// Ensure counter-clockwise winding.
	area := 0.0
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		area += pts[i][0]*pts[j][1] - pts[j][0]*pts[i][1]
	}

	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	if area < 0 {
		for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
			idx[i], idx[j] = idx[j], idx[i]
		}
	}

	var result [][3]int
	rem := make([]int, len(idx))
	copy(rem, idx)

	for len(rem) > 3 {
		found := false
		for i := 0; i < len(rem); i++ {
			prev := (i + len(rem) - 1) % len(rem)
			next := (i + 1) % len(rem)

			a := pts[rem[prev]]
			b := pts[rem[i]]
			c := pts[rem[next]]

			// Convexity check (CCW cross product > 0).
			cross := (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0])
			if cross < 1e-10 {
				continue
			}

			// Ensure no other vertex lies inside triangle abc.
			isEar := true
			for j := 0; j < len(rem); j++ {
				if j == prev || j == i || j == next {
					continue
				}
				if pointInTri2D(pts[rem[j]], a, b, c) {
					isEar = false
					break
				}
			}
			if !isEar {
				continue
			}

			result = append(result, [3]int{rem[prev], rem[i], rem[next]})
			rem = append(rem[:i], rem[i+1:]...)
			found = true
			break
		}
		if !found {
			return nil
		}
	}

	if len(rem) == 3 {
		result = append(result, [3]int{rem[0], rem[1], rem[2]})
	}
	return result
}

func pointInTri2D(p, a, b, c [2]float64) bool {
	d1 := (p[0]-b[0])*(a[1]-b[1]) - (a[0]-b[0])*(p[1]-b[1])
	d2 := (p[0]-c[0])*(b[1]-c[1]) - (b[0]-c[0])*(p[1]-c[1])
	d3 := (p[0]-a[0])*(c[1]-a[1]) - (c[0]-a[0])*(p[1]-a[1])
	hasNeg := d1 < 0 || d2 < 0 || d3 < 0
	hasPos := d1 > 0 || d2 > 0 || d3 > 0
	return !(hasNeg && hasPos)
}
