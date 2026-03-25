// Package hexvoxel generates a hexagonal voxel shell of a textured mesh.
// Each hexagonal prism in the output corresponds to one nozzle-width column
// at one layer height, matching what a slicer actually deposits.
package hexvoxel

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"

	"github.com/rtwfroody/text2filament/internal/loader"
	"github.com/rtwfroody/text2filament/internal/palette"
)

// Config holds parameters for hexagonal voxel remeshing.
type Config struct {
	NozzleDiameter float32 // flat-to-flat hex width in mm
	LayerHeight    float32 // Z extrusion per layer in mm
}

// surfaceHit records where a hex column center intersects the original mesh.
type surfaceHit struct {
	z        float32
	triIdx   int
	bary     [3]float32 // barycentric coordinates
	texIdx   int32
}

// activeHex represents one hex prism to generate.
type activeHex struct {
	col, row, layer int
	cx, cy, cz      float32
	color           [3]uint8
}

// spatialIndex is a 2D uniform grid for fast triangle lookup by XY position.
type spatialIndex struct {
	cells    [][]int32 // cell index → list of triangle indices
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

	si := &spatialIndex{
		cells:    make([][]int32, cols*rows),
		minX:     minX,
		minY:     minY,
		cellSize: cellSize,
		cols:     cols,
		rows:     rows,
	}

	// Insert each triangle into overlapping cells.
	for fi, f := range model.Faces {
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]

		txMin := minf(v0[0], minf(v1[0], v2[0]))
		txMax := maxf(v0[0], maxf(v1[0], v2[0]))
		tyMin := minf(v0[1], minf(v1[1], v2[1]))
		tyMax := maxf(v0[1], maxf(v1[1], v2[1]))

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
// Uses a set to avoid duplicates.
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

// closestPointOnSegmentXY returns the closest point on segment AB to point P,
// all in XY. Returns the parameter t in [0,1].
func closestPointOnSegmentXY(px, py, ax, ay, bx, by float32) (float32, float32, float32) {
	dx := bx - ax
	dy := by - ay
	lenSq := dx*dx + dy*dy
	if lenSq == 0 {
		return ax, ay, 0
	}
	t := ((px-ax)*dx + (py-ay)*dy) / lenSq
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return ax + t*dx, ay + t*dy, t
}

// closestPointOnTriangleXY finds the closest point on triangle (v0,v1,v2)'s
// XY projection to (px,py). Returns the closest XY point, its barycentric
// coords on the triangle, and the squared distance.
func closestPointOnTriangleXY(px, py float32, v0, v1, v2 [3]float32) ([3]float32, float32) {
	// First check if point is inside triangle.
	inside, bary := pointInTriangleXY(px, py, v0, v1, v2)
	if inside {
		return bary, 0
	}

	// Otherwise find closest point on each edge.
	type edgeResult struct {
		distSq float32
		bary   [3]float32
	}

	edges := [3][2]int{{0, 1}, {1, 2}, {2, 0}}
	triVerts := [3][3]float32{v0, v1, v2}

	bestDistSq := float32(math.MaxFloat32)
	var bestBary [3]float32

	for _, e := range edges {
		a := triVerts[e[0]]
		b := triVerts[e[1]]
		_, _, t := closestPointOnSegmentXY(px, py, a[0], a[1], b[0], b[1])

		// Convert edge parameter to barycentric.
		var baryC [3]float32
		baryC[e[0]] = 1 - t
		baryC[e[1]] = t
		// Third vertex has weight 0.

		cpx := baryC[0]*v0[0] + baryC[1]*v1[0] + baryC[2]*v2[0]
		cpy := baryC[0]*v0[1] + baryC[1]*v1[1] + baryC[2]*v2[1]
		dx := px - cpx
		dy := py - cpy
		distSq := dx*dx + dy*dy

		if distSq < bestDistSq {
			bestDistSq = distSq
			bestBary = baryC
		}
	}

	return bestBary, bestDistSq
}

// bilinearSample samples a texture at normalized UV coordinates.
// Duplicated from sample.go to keep code paths separate.
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

// Remesh generates a hexagonal voxel shell of the input model.
func Remesh(model *loader.LoadedModel, pal [][3]uint8, cfg Config, dither bool) (*loader.LoadedModel, []int32, error) {
	if len(model.Vertices) == 0 || len(model.Faces) == 0 {
		return nil, nil, fmt.Errorf("empty model")
	}

	nozzle := cfg.NozzleDiameter
	layerH := cfg.LayerHeight
	size := nozzle / float32(math.Sqrt(3)) // center-to-vertex

	// 1. Bounding box.
	minV, maxV := computeBounds(model.Vertices)
	pad := nozzle
	minV[0] -= pad
	minV[1] -= pad
	minV[2] -= pad
	maxV[0] += pad
	maxV[1] += pad
	maxV[2] += pad

	// Grid dimensions.
	colStep := 1.5 * size                // horizontal spacing
	rowStep := nozzle                     // vertical spacing (flat-to-flat)
	nCols := int(math.Ceil(float64(maxV[0]-minV[0])/float64(colStep))) + 1
	nRows := int(math.Ceil(float64(maxV[1]-minV[1])/float64(rowStep))) + 1
	nLayers := int(math.Ceil(float64(maxV[2]-minV[2])/float64(layerH))) + 1

	fmt.Printf("  Hex grid: %d cols x %d rows x %d layers\n", nCols, nRows, nLayers)

	// 2. Spatial index.
	si := newSpatialIndex(model, nozzle*2)

	// 3. Surface detection: for each hex column, find triangles whose XY bbox
	// overlaps the hex's XY bbox. The hex circumradius (center-to-vertex) is
	// `size`; any triangle with a point within that distance of the hex center
	// could intersect the hex footprint.
	type columnKey struct{ col, row int }
	columnHits := make(map[columnKey][]surfaceHit)
	proximitySq := size * size // hex circumradius squared

	for col := 0; col < nCols; col++ {
		cx := minV[0] + float32(col)*colStep
		for row := 0; row < nRows; row++ {
			cy := minV[1] + float32(row)*rowStep
			if col%2 == 1 {
				cy += rowStep / 2
			}

			cands := si.candidatesRadius(cx, cy, size)
			for _, ti := range cands {
				f := model.Faces[ti]
				v0 := model.Vertices[f[0]]
				v1 := model.Vertices[f[1]]
				v2 := model.Vertices[f[2]]

				bary, distSq := closestPointOnTriangleXY(cx, cy, v0, v1, v2)
				if distSq > proximitySq {
					continue
				}

				// The triangle spans a Z range. Generate a hit for every
				// layer the triangle covers, not just the single Z at the
				// closest XY point. This fills in vertical/steep walls.
				triZMin := minf(v0[2], minf(v1[2], v2[2]))
				triZMax := maxf(v0[2], maxf(v1[2], v2[2]))
				layerMin := int(math.Floor(float64(triZMin-minV[2]) / float64(layerH)))
				layerMax := int(math.Floor(float64(triZMax-minV[2]) / float64(layerH)))
				if layerMin < 0 {
					layerMin = 0
				}
				if layerMax >= nLayers {
					layerMax = nLayers - 1
				}

				key := columnKey{col, row}
				for layer := layerMin; layer <= layerMax; layer++ {
					columnHits[key] = append(columnHits[key], surfaceHit{
						z:      minV[2] + float32(layer)*layerH,
						triIdx: int(ti),
						bary:   bary,
						texIdx: model.FaceTextureIdx[ti],
					})
				}
			}
		}
	}

	fmt.Printf("  %d hex columns with surface hits\n", len(columnHits))

	// 4. Mark active hex prisms and sample colors.
	var hexes []activeHex

	for key, hits := range columnHits {
		cx := minV[0] + float32(key.col)*colStep
		cy := minV[1] + float32(key.row)*rowStep
		if key.col%2 == 1 {
			cy += rowStep / 2
		}

		for _, hit := range hits {
			layer := int(math.Round(float64(hit.z-minV[2]) / float64(layerH)))
			if layer < 0 {
				layer = 0
			}
			if layer >= nLayers {
				layer = nLayers - 1
			}

			cz := minV[2] + float32(layer)*layerH

			// Sample texture color.
			col := sampleHitColor(model, hit)

			hexes = append(hexes, activeHex{
				col: key.col, row: key.row, layer: layer,
				cx: cx, cy: cy, cz: cz,
				color: col,
			})
		}
	}

	// Deduplicate hexes at the same (col, row, layer) — keep closest hit.
	hexes = deduplicateHexes(hexes)

	fmt.Printf("  %d active hex prisms\n", len(hexes))
	if len(hexes) == 0 {
		return nil, nil, fmt.Errorf("no active hex prisms found; model may be too small or outside bounds")
	}

	// Count isolated hexes (no direct neighbors) as a diagnostic.
	isolated := countIsolatedHexes(hexes)
	fmt.Printf("  %d isolated hexes (no neighbors)\n", isolated)

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

	// 6. Triangulate hex prisms and expand assignments.
	outModel, faceAssignments := triangulateHexes(hexes, assignments, size, layerH, model.Textures)

	return outModel, faceAssignments, nil
}

func sampleHitColor(model *loader.LoadedModel, hit surfaceHit) [3]uint8 {
	if hit.texIdx < 0 || int(hit.texIdx) >= len(model.Textures) {
		return [3]uint8{128, 128, 128} // no texture, gray
	}

	f := model.Faces[hit.triIdx]
	uv0 := model.UVs[f[0]]
	uv1 := model.UVs[f[1]]
	uv2 := model.UVs[f[2]]

	u := hit.bary[0]*uv0[0] + hit.bary[1]*uv1[0] + hit.bary[2]*uv2[0]
	v := hit.bary[0]*uv0[1] + hit.bary[1]*uv1[1] + hit.bary[2]*uv2[1]

	return bilinearSample(model.Textures[hit.texIdx], u, v)
}

// countIsolatedHexes counts hexes that have no direct neighbors (same layer,
// adjacent hex column, or same column ±1 layer).
func countIsolatedHexes(hexes []activeHex) int {
	type hexKey struct{ col, row, layer int }
	occupied := make(map[hexKey]struct{}, len(hexes))
	for _, h := range hexes {
		occupied[hexKey{h.col, h.row, h.layer}] = struct{}{}
	}

	isolated := 0
	for _, h := range hexes {
		hasNeighbor := false
		// Same-layer hex neighbors. For a hex grid with odd-column offset:
		// Even column neighbors: (±1, 0), (0, ±1), (-1,-1), (+1,-1)
		// Odd column neighbors:  (±1, 0), (0, ±1), (-1,+1), (+1,+1)
		var neighborOffsets [][2]int
		if h.col%2 == 0 {
			neighborOffsets = [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}, {-1, -1}, {1, -1}}
		} else {
			neighborOffsets = [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}, {-1, 1}, {1, 1}}
		}
		for _, off := range neighborOffsets {
			if _, ok := occupied[hexKey{h.col + off[0], h.row + off[1], h.layer}]; ok {
				hasNeighbor = true
				break
			}
		}
		if !hasNeighbor {
			// Also check above/below.
			if _, ok := occupied[hexKey{h.col, h.row, h.layer - 1}]; ok {
				hasNeighbor = true
			} else if _, ok := occupied[hexKey{h.col, h.row, h.layer + 1}]; ok {
				hasNeighbor = true
			}
		}
		if !hasNeighbor {
			isolated++
		}
	}
	return isolated
}

func deduplicateHexes(hexes []activeHex) []activeHex {
	type hexKey struct{ col, row, layer int }
	seen := make(map[hexKey]int, len(hexes))
	var result []activeHex

	for _, h := range hexes {
		k := hexKey{h.col, h.row, h.layer}
		if _, ok := seen[k]; !ok {
			seen[k] = len(result)
			result = append(result, h)
		}
		// Could pick the one with the "best" hit, but first-wins is fine for v1.
	}
	return result
}

// ditherHexes applies Floyd-Steinberg error diffusion over hexes in spatial
// order (layer, then row, then column).
func ditherHexes(hexes []activeHex, pal [][3]uint8) []int32 {
	// Sort hexes spatially for coherent dithering.
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
	errBuf := make([][3]float32, len(hexes)+4) // extra slots to avoid bounds checks

	for i, idx := range order {
		r := clampF(float32(hexes[idx].color[0])+errBuf[i][0], 0, 255)
		g := clampF(float32(hexes[idx].color[1])+errBuf[i][1], 0, 255)
		b := clampF(float32(hexes[idx].color[2])+errBuf[i][2], 0, 255)

		// Find nearest palette color.
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

		// Compute error and spread to next hexes.
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

// triangulateHexes generates triangle mesh geometry for all hex prisms.
// Returns a LoadedModel and per-face palette assignments.
func triangulateHexes(hexes []activeHex, hexAssignments []int32, size, layerH float32, textures []image.Image) (*loader.LoadedModel, []int32) {
	const facesPerHex = 24
	const vertsPerHex = 14

	totalVerts := len(hexes) * vertsPerHex
	totalFaces := len(hexes) * facesPerHex

	verts := make([][3]float32, 0, totalVerts)
	faces := make([][3]uint32, 0, totalFaces)
	faceAssignments := make([]int32, 0, totalFaces)

	// Precompute hex vertex offsets (flat-top hexagon).
	var hexOffsetsX, hexOffsetsY [6]float32
	for i := 0; i < 6; i++ {
		angle := float64(i) * math.Pi / 3.0 // 0, 60, 120, 180, 240, 300 degrees
		hexOffsetsX[i] = size * float32(math.Cos(angle))
		hexOffsetsY[i] = size * float32(math.Sin(angle))
	}

	halfH := layerH / 2

	for hi, h := range hexes {
		base := uint32(len(verts))
		assignment := hexAssignments[hi]

		// Generate 14 vertices: 6 top + 6 bottom + top center + bottom center.
		zTop := h.cz + halfH
		zBot := h.cz - halfH

		for i := 0; i < 6; i++ {
			verts = append(verts, [3]float32{h.cx + hexOffsetsX[i], h.cy + hexOffsetsY[i], zTop})
		}
		for i := 0; i < 6; i++ {
			verts = append(verts, [3]float32{h.cx + hexOffsetsX[i], h.cy + hexOffsetsY[i], zBot})
		}
		verts = append(verts, [3]float32{h.cx, h.cy, zTop}) // index 12: top center
		verts = append(verts, [3]float32{h.cx, h.cy, zBot}) // index 13: bottom center

		tc := base + 12 // top center
		bc := base + 13 // bottom center

		// Top hexagon (CCW from above → outward normal is +Z).
		for i := 0; i < 6; i++ {
			j := (i + 1) % 6
			faces = append(faces, [3]uint32{tc, base + uint32(i), base + uint32(j)})
			faceAssignments = append(faceAssignments, assignment)
		}

		// Bottom hexagon (CW from above → outward normal is -Z).
		for i := 0; i < 6; i++ {
			j := (i + 1) % 6
			faces = append(faces, [3]uint32{bc, base + 6 + uint32(j), base + 6 + uint32(i)})
			faceAssignments = append(faceAssignments, assignment)
		}

		// Side rectangles.
		for i := 0; i < 6; i++ {
			j := (i + 1) % 6
			ti := base + uint32(i)      // top vertex i
			tj := base + uint32(j)      // top vertex j
			bi := base + 6 + uint32(i)  // bottom vertex i
			bj := base + 6 + uint32(j)  // bottom vertex j
			faces = append(faces, [3]uint32{ti, bi, bj})
			faceAssignments = append(faceAssignments, assignment)
			faces = append(faces, [3]uint32{ti, bj, tj})
			faceAssignments = append(faceAssignments, assignment)
		}
	}

	// Build a minimal LoadedModel. UVs and textures aren't needed since
	// color assignment is already done.
	uvs := make([][2]float32, len(verts))
	faceTex := make([]int32, len(faces))

	// Use a tiny 1x1 texture as placeholder.
	placeholder := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	placeholder.SetNRGBA(0, 0, color.NRGBA{128, 128, 128, 255})
	var placeholderTextures []image.Image
	if len(textures) > 0 {
		placeholderTextures = textures[:1]
	} else {
		placeholderTextures = []image.Image{placeholder}
	}

	return &loader.LoadedModel{
		Vertices:       verts,
		Faces:          faces,
		UVs:            uvs,
		Textures:       placeholderTextures,
		FaceTextureIdx: faceTex,
	}, faceAssignments
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

