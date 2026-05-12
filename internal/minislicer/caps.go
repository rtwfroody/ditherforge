package minislicer

import (
	"math"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// RefineCapSrcTriangles replaces each cap-tile section's
// SrcTriIdx (initially the closest loop-edge's triangle) with
// the model triangle whose XY projection actually contains the
// cap tile's center. That triangle is the one the cap tile
// "sits under" (top cap) or "sits over" (bottom cap); sampling
// against it gives the right barycentric and UV at (cx, cy)
// rather than landing on the nearest loop-edge — which for a
// low-poly model with sparse edges per layer collapses many
// distinct cap tiles onto the same edge → same UV → wide flat
// patches of one texture sample in the rendered output.
//
// When no model triangle projects to (cx, cy) (rare; usually a
// numerical edge case), the existing nearestEdgeTri value is
// left alone.
func RefineCapSrcTriangles(model *loader.LoadedModel, sections []Section) {
	if model == nil || len(model.Faces) == 0 {
		return
	}
	idx := buildXYTriIndex(model)
	for i := range sections {
		s := &sections[i]
		if s.Kind != KindCapTop && s.Kind != KindCapBottom {
			continue
		}
		if tri := idx.findContaining(model, s.Mid[0], s.Mid[1], s.Z); tri >= 0 {
			s.SrcTriIdx = tri
		}
	}
}

// xyTriIndex bins triangles into a uniform XY grid by bbox so
// per-tile point-in-triangle queries skip irrelevant triangles.
type xyTriIndex struct {
	minX, minY float32
	cellSize   float32
	cols, rows int
	cells      [][]int32
}

func buildXYTriIndex(model *loader.LoadedModel) *xyTriIndex {
	if len(model.Vertices) == 0 || len(model.Faces) == 0 {
		return &xyTriIndex{cellSize: 1, cols: 1, rows: 1, cells: [][]int32{{}}}
	}
	minX, minY := float32(math.MaxFloat32), float32(math.MaxFloat32)
	maxX, maxY := float32(-math.MaxFloat32), float32(-math.MaxFloat32)
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
	// Target ~sqrt(faces) cells per side, so each cell holds O(√F)
	// triangles on average — keeps containment tests cheap without
	// the index itself bloating memory.
	side := int(math.Sqrt(float64(len(model.Faces))))
	if side < 4 {
		side = 4
	}
	wx := maxX - minX
	wy := maxY - minY
	maxW := wx
	if wy > maxW {
		maxW = wy
	}
	cellSize := maxW / float32(side)
	if cellSize <= 0 {
		cellSize = 1
	}
	cols := int(math.Ceil(float64(wx/cellSize))) + 1
	rows := int(math.Ceil(float64(wy/cellSize))) + 1
	cells := make([][]int32, cols*rows)
	for ti, f := range model.Faces {
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]
		tx0 := minf32(v0[0], minf32(v1[0], v2[0]))
		tx1 := maxf32(v0[0], maxf32(v1[0], v2[0]))
		ty0 := minf32(v0[1], minf32(v1[1], v2[1]))
		ty1 := maxf32(v0[1], maxf32(v1[1], v2[1]))
		c0 := int((tx0 - minX) / cellSize)
		c1 := int((tx1 - minX) / cellSize)
		r0 := int((ty0 - minY) / cellSize)
		r1 := int((ty1 - minY) / cellSize)
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
		for r := r0; r <= r1; r++ {
			for c := c0; c <= c1; c++ {
				cells[r*cols+c] = append(cells[r*cols+c], int32(ti))
			}
		}
	}
	return &xyTriIndex{minX: minX, minY: minY, cellSize: cellSize, cols: cols, rows: rows, cells: cells}
}

// findContaining returns the triangle whose XY projection
// contains (x, y) and whose plane evaluates closest to z. -1 if
// no triangle covers (x, y).
func (g *xyTriIndex) findContaining(model *loader.LoadedModel, x, y, z float32) int32 {
	c := int((x - g.minX) / g.cellSize)
	r := int((y - g.minY) / g.cellSize)
	if c < 0 || c >= g.cols || r < 0 || r >= g.rows {
		return -1
	}
	candidates := g.cells[r*g.cols+c]
	best := int32(-1)
	bestDZ := float32(math.MaxFloat32)
	for _, ti := range candidates {
		f := model.Faces[ti]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]
		if !pointInTriangleXY(x, y, v0, v1, v2) {
			continue
		}
		pz, ok := planeZAtXY(v0, v1, v2, x, y)
		if !ok {
			continue
		}
		dz := pz - z
		if dz < 0 {
			dz = -dz
		}
		if dz < bestDZ {
			bestDZ = dz
			best = ti
		}
	}
	return best
}

// pointInTriangleXY tests whether (px, py) lies inside the 2D
// projection of triangle (v0, v1, v2) onto the XY plane.
func pointInTriangleXY(px, py float32, v0, v1, v2 [3]float32) bool {
	// Signed-area test via cross products. Robust to winding.
	s0 := (v1[0]-v0[0])*(py-v0[1]) - (v1[1]-v0[1])*(px-v0[0])
	s1 := (v2[0]-v1[0])*(py-v1[1]) - (v2[1]-v1[1])*(px-v1[0])
	s2 := (v0[0]-v2[0])*(py-v2[1]) - (v0[1]-v2[1])*(px-v2[0])
	hasNeg := s0 < 0 || s1 < 0 || s2 < 0
	hasPos := s0 > 0 || s1 > 0 || s2 > 0
	return !(hasNeg && hasPos)
}

// planeZAtXY returns the Z of the triangle's plane evaluated at
// (x, y). ok=false if the triangle is degenerate or its plane is
// nearly vertical (so a horizontal slice through (x, y) doesn't
// have a well-defined z).
func planeZAtXY(v0, v1, v2 [3]float32, x, y float32) (float32, bool) {
	ex := v1[0] - v0[0]
	ey := v1[1] - v0[1]
	ez := v1[2] - v0[2]
	fx := v2[0] - v0[0]
	fy := v2[1] - v0[1]
	fz := v2[2] - v0[2]
	nx := ey*fz - ez*fy
	ny := ez*fx - ex*fz
	nz := ex*fy - ey*fx
	if nz > -1e-6 && nz < 1e-6 {
		return 0, false
	}
	// Plane: nx*(X-v0x) + ny*(Y-v0y) + nz*(Z-v0z) = 0
	// → Z = v0z - (nx*(x-v0x) + ny*(y-v0y)) / nz
	z := v0[2] - (nx*(x-v0[0])+ny*(y-v0[1]))/nz
	return z, true
}

// PartitionTopCap tiles the top face of `layer` wherever it's
// exposed — solid in `layer` and air in `neighborAbove` (the layer
// directly above). Pass neighborAbove == nil for the topmost layer
// (no layer above → all-air → every tile inside layer is exposed).
//
// `layerH` is the slab thickness; the section's Z is at the slab's
// upper face (no depth-bias offset — cap geometry is emitted by the
// mesh builder as one watertight surface on the exact slab face,
// and tile sections supply per-triangle color via nearest-Mid
// lookup, so there is no overlap to disambiguate).
//
// loopIdxBase shifts the per-loop LoopIdx for cap sections so they
// don't collide with ribbon-section loop indices.
func PartitionTopCap(layer Layer, neighborAbove *Layer, layerH, cellSize float32, loopIdxBase int) []Section {
	return partitionCap(layer, neighborAbove, layerH/2, cellSize, loopIdxBase, KindCapTop)
}

// PartitionBottomCap is the bottom-face counterpart. Pass
// neighborBelow == nil for the bottommost layer.
func PartitionBottomCap(layer Layer, neighborBelow *Layer, layerH, cellSize float32, loopIdxBase int) []Section {
	return partitionCap(layer, neighborBelow, -layerH/2, cellSize, loopIdxBase, KindCapBottom)
}

// insideSolid uses even-odd nesting: a point is inside the solid
// region of `layer` iff an odd number of layer.Loops contain it.
// Returns false when layer == nil (treating "no layer" as all-air,
// so all tiles in the calling layer are exposed at that face).
func insideSolid(layer *Layer, x, y float32) bool {
	if layer == nil {
		return false
	}
	count := 0
	for i := range layer.Loops {
		if len(layer.Loops[i].Points) < 3 {
			continue
		}
		if pointInPolygon(layer.Loops[i].Points, x, y) {
			count++
		}
	}
	return (count & 1) == 1
}

// partitionCap is the shared body of PartitionTopCap and
// PartitionBottomCap. zOffset is added to layer.Z to get the cap
// surface's 3D Z (positive for top, negative for bottom).
//
// Iterates outer loops only (loop.IsHole == false); for each outer
// loop, tiles whose centers fall outside the loop OR inside any
// hole loop in the same layer are dropped. Tiles partially clipped
// by the loop boundary are emitted at full cellSize anyway (the
// resulting prism geometry overhangs the contour by a fraction of a
// cell, which is acceptable for the prototype).
func partitionCap(layer Layer, neighbor *Layer, zOffset, cellSize float32, loopIdxBase int, kind SectionKind) []Section {
	if cellSize <= 0 {
		return nil
	}
	z := layer.Z + zOffset
	var out []Section
	// Cache the layer's hole loops for the per-tile exclusion test.
	var holes []*Loop
	for k := range layer.Loops {
		if layer.Loops[k].IsHole {
			holes = append(holes, &layer.Loops[k])
		}
	}
	exposedAt := func(loopPts []Point2, x, y float32) bool {
		if !pointInPolygon(loopPts, x, y) {
			return false
		}
		for _, h := range holes {
			if pointInPolygon(h.Points, x, y) {
				return false
			}
		}
		return !insideSolid(neighbor, x, y)
	}
	for li, loop := range layer.Loops {
		if loop.IsHole {
			continue
		}
		// Tight loop bbox.
		xMin, yMin := float32(math.Inf(1)), float32(math.Inf(1))
		xMax, yMax := float32(math.Inf(-1)), float32(math.Inf(-1))
		for _, p := range loop.Points {
			if p[0] < xMin {
				xMin = p[0]
			}
			if p[0] > xMax {
				xMax = p[0]
			}
			if p[1] < yMin {
				yMin = p[1]
			}
			if p[1] > yMax {
				yMax = p[1]
			}
		}
		cols := int(math.Ceil(float64((xMax - xMin) / cellSize)))
		rows := int(math.Ceil(float64((yMax - yMin) / cellSize)))
		if cols < 1 {
			cols = 1
		}
		if rows < 1 {
			rows = 1
		}
		idx := 0
		capLoopIdx := loopIdxBase + li
		for j := 0; j < rows; j++ {
			for i := 0; i < cols; i++ {
				x0 := xMin + float32(i)*cellSize
				y0 := yMin + float32(j)*cellSize
				x1 := x0 + cellSize
				y1 := y0 + cellSize
				cx := (x0 + x1) * 0.5
				cy := (y0 + y1) * 0.5
				// Tile becomes a section iff some sample point
				// inside its rect is exposed. Cell center plus
				// four corners catches both interior and
				// boundary-straddling tiles; cells fully outside
				// or fully covered fail all five tests and drop.
				if !exposedAt(loop.Points, cx, cy) &&
					!exposedAt(loop.Points, x0, y0) &&
					!exposedAt(loop.Points, x1, y0) &&
					!exposedAt(loop.Points, x1, y1) &&
					!exposedAt(loop.Points, x0, y1) {
					continue
				}
				out = append(out, Section{
					LayerIdx:    layer.LayerIdx,
					LoopIdx:     capLoopIdx,
					Index:       idx,
					Kind:        kind,
					Mid:         Point2{cx, cy},
					Z:           z,
					CapBoundsXY: [4]float32{x0, y0, x1, y1},
					TileCol:     i,
					TileRow:     j,
					// Cap tile color comes from the nearest loop edge's
					// source triangle. Without this, SrcTriIdx=-1 would
					// route to SampleNearestColor's global spatial-index
					// search, which can pull in triangles from a nearby
					// but unrelated mesh region — e.g. a salmon cut-
					// surface triangle being picked for a blue dome
					// cap, producing horizontal salmon "streaks" in the
					// rendered output. Anchoring the cap to the same
					// mesh component its loop traces avoids that leak.
					SrcTriIdx: nearestEdgeTri(&loop, cx, cy),
				})
				idx++
			}
		}
	}
	return out
}

// nearestEdgeTri returns the source triangle index of the loop edge
// whose midpoint is geometrically closest (squared XY distance) to
// (x, y). Returns -1 if the loop has no EdgeTris or has fewer than
// 2 points.
//
// Used by partitionCap to anchor each cap tile to a triangle from
// the same connected mesh region as its bounding loop. The cap
// tile sits inside the polygon rather than on any one triangle's
// surface, so SampleByTriangle then samples that triangle's
// closest-point-on-triangle for (cx, cy, tileZ) — which lives on
// the same mesh component the loop traces. This avoids the global
// nearest-tri search picking up unrelated nearby geometry (e.g. a
// cut surface inside a fish dome, or a cutting board next to a
// fish footprint).
func nearestEdgeTri(loop *Loop, x, y float32) int32 {
	n := len(loop.Points)
	if n < 2 || len(loop.EdgeTris) == 0 {
		return -1
	}
	best := int32(-1)
	bestSq := float32(math.MaxFloat32)
	for i := 0; i < n; i++ {
		if i >= len(loop.EdgeTris) {
			break
		}
		j := (i + 1) % n
		mx := (loop.Points[i][0] + loop.Points[j][0]) * 0.5
		my := (loop.Points[i][1] + loop.Points[j][1]) * 0.5
		dx := mx - x
		dy := my - y
		d := dx*dx + dy*dy
		if d < bestSq {
			bestSq = d
			best = loop.EdgeTris[i]
		}
	}
	return best
}

// pointInPolygon does even-odd ray casting along +X. The polygon is
// closed (last point connects back to first); does not include
// special handling for points exactly on edges (rare for tile
// centers offset by cellSize/2).
func pointInPolygon(points []Point2, x, y float32) bool {
	inside := false
	n := len(points)
	if n < 3 {
		return false
	}
	j := n - 1
	for i := 0; i < n; i++ {
		yi := points[i][1]
		yj := points[j][1]
		if (yi > y) != (yj > y) {
			t := (y - yi) / (yj - yi)
			xCross := points[i][0] + t*(points[j][0]-points[i][0])
			if xCross > x {
				inside = !inside
			}
		}
		j = i
	}
	return inside
}
