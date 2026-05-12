package minislicer

import (
	"math"

	clipper "github.com/ctessum/go.clipper"
	"github.com/rtwfroody/ditherforge/internal/loader"
)

// loopKey identifies one loop within one layer. Used as a section
// lookup key.
type loopKey struct{ layer, loop int }

// BuildPrintableMesh assembles a 3D triangle mesh from the per-layer
// contour partition.
//
// Geometry per layer:
//   - Walls: one tube per loop (outer or hole), painted per ribbon
//     section. Outer-loop walls face outward; hole-loop walls face
//     inward into the cavity.
//   - Caps: one watertight cap surface per slab boundary, covering
//     ONLY the region exposed to air on that side — i.e. the
//     symmetric-difference annulus between this layer's footprint
//     and the adjacent layer's footprint. Computed via Clipper
//     polygon difference, then earcut-triangulated. Each triangle's
//     color comes from the nearest cap-tile section's dithered
//     palette index, so the cap surface carries the dither pattern
//     directly in its triangulation — no separate "floating tiles"
//     above an earcut underneath, no internal coplanar caps between
//     adjacent layers.
//
// The output is a flat triangle list packed into a LoadedModel
// (Vertices + Faces only) plus a parallel `assignments` slice with
// one palette index per face.
func BuildPrintableMesh(layers []Layer, sections []Section, assignments []int32, layerH float32) (*loader.LoadedModel, []int32) {
	m, faceAssign, _ := BuildPrintableMeshFull(layers, sections, assignments, layerH)
	return m, faceAssign
}

// BuildPrintableMeshFull is BuildPrintableMesh plus a parallel
// faceSection slice: faceSection[i] is the index into `sections`
// that produced face i, or -1 for faces that don't trace to a
// single section. Used by the pipeline's ShowSampledColors debug
// mode to color faces by the originating section's raw sampled RGB
// instead of its dithered palette index.
func BuildPrintableMeshFull(layers []Layer, sections []Section, assignments []int32, layerH float32) (*loader.LoadedModel, []int32, []int32) {
	m := &loader.LoadedModel{}
	var faceAssign []int32
	var faceSection []int32

	loopSecs := make(map[loopKey][]int)
	for i, s := range sections {
		loopSecs[loopKey{s.LayerIdx, s.LoopIdx}] = append(loopSecs[loopKey{s.LayerIdx, s.LoopIdx}], i)
	}
	for _, ids := range loopSecs {
		sortByIndex(ids, sections)
	}

	fallback := mostCommonNonNegSafe(assignments)

	// Per-layer cap-tile sections (one slice per kind+layer), with
	// each section's CapBoundsXY rectangle indexed by tile cell.
	// Earcut triangles' centroids look up here to pick the tile
	// section painting that triangle.
	topTiles := buildLayerTileIdx(sections, KindCapTop)
	botTiles := buildLayerTileIdx(sections, KindCapBottom)
	topByLayer := make(map[int][]int32)
	botByLayer := make(map[int][]int32)
	for i, s := range sections {
		switch s.Kind {
		case KindCapTop:
			topByLayer[s.LayerIdx] = append(topByLayer[s.LayerIdx], int32(i))
		case KindCapBottom:
			botByLayer[s.LayerIdx] = append(botByLayer[s.LayerIdx], int32(i))
		}
	}

	// Per-(layer, loop) ribbon midpoints, used to find the nearest
	// ribbon section for any cap-triangle whose centroid doesn't
	// land inside a tile rectangle. Same scope rule as the
	// pre-rewrite code: a single outer loop's ribbons only —
	// otherwise unrelated outer loops in the same layer leak color
	// across each other.
	layerLoopRibbons := make(map[loopKey][]ribbonRef)
	for i, s := range sections {
		if s.Kind != KindRibbon {
			continue
		}
		k := loopKey{s.LayerIdx, s.LoopIdx}
		layerLoopRibbons[k] = append(layerLoopRibbons[k], ribbonRef{int32(i), s.Mid})
	}

	// Pre-build wall-conforming subdivided loops per (layer, loop).
	// The cap polygon feeds these to Clipper so cap-mesh vertices
	// on the cap's outer/inner boundary exactly match wall-mesh
	// vertices on the wall's top/bottom edges. Without that match
	// the wall's section-breakpoint vertices form T-junctions
	// against the cap's raw-loop edges, leaving hairline cracks
	// the camera sees background through (visible as a shimmering
	// edge that flickers as the view rotates).
	wallLoops := make([][][]Point2, len(layers))
	for li := range layers {
		lps := make([][]Point2, len(layers[li].Loops))
		for lp := range layers[li].Loops {
			loop := &layers[li].Loops[lp]
			ids := loopSecs[loopKey{li, lp}]
			if len(ids) > 0 && sections[ids[0]].Kind == KindRibbon {
				pts, _, _ := buildLoopWallSeq(loop, ids, sections, assignments)
				if len(pts) >= 3 {
					lps[lp] = pts
					continue
				}
			}
			lps[lp] = loop.Points
		}
		wallLoops[li] = lps
	}

	// Walls: every loop (outer or hole) gets a wall tube.
	for li, layer := range layers {
		zBot := layer.Z - layerH/2
		zTop := layer.Z + layerH/2
		for lp := range layer.Loops {
			loop := &layer.Loops[lp]
			ids := loopSecs[loopKey{li, lp}]
			if len(ids) == 0 || sections[ids[0]].Kind != KindRibbon {
				continue
			}
			emitLoopWall(m, &faceAssign, &faceSection, loop, ids, sections, assignments,
				zBot, zTop, loop.IsHole)
		}
	}

	// Caps: one watertight surface per slab boundary, restricted to
	// the air-facing region. Topmost/bottommost layer uses a nil
	// neighbor → full footprint exposed. The exposed region is
	// triangulated once via Earcut; per-triangle color comes from
	// the cap-tile section whose rectangle contains the triangle's
	// centroid, falling back to the nearest ribbon section of this
	// loop (orientation-matched) when no tile contains the
	// centroid — same fallback rule the pre-rewrite code applied
	// to its full earcut cap, which gives slope-derived colors on
	// thin step caps instead of XY-projected fabric colors.
	for li := range layers {
		layer := &layers[li]
		if len(layer.Loops) == 0 {
			continue
		}
		zBot := layer.Z - layerH/2
		zTop := layer.Z + layerH/2

		var aboveLoops, belowLoops [][]Point2
		for k := li + 1; k < len(layers); k++ {
			if len(layers[k].Loops) > 0 {
				aboveLoops = wallLoops[k]
				break
			}
		}
		for k := li - 1; k >= 0; k-- {
			if len(layers[k].Loops) > 0 {
				belowLoops = wallLoops[k]
				break
			}
		}

		emitLayerFaceCapUnified(m, &faceAssign, &faceSection,
			layer, wallLoops[li], aboveLoops, zTop, true, topTiles[li], topByLayer[li], +1,
			sections, assignments, fallback, layerLoopRibbons, li)
		emitLayerFaceCapUnified(m, &faceAssign, &faceSection,
			layer, wallLoops[li], belowLoops, zBot, false, botTiles[li], botByLayer[li], -1,
			sections, assignments, fallback, layerLoopRibbons, li)
	}

	return m, faceAssign, faceSection
}

// ribbonRef is a ribbon section's id and XY midpoint. Used by the
// cap colorer to find the nearest ribbon section for triangles
// that don't land inside any tile rectangle.
type ribbonRef struct {
	sid int32
	mid Point2
}

// emitLayerFaceCap emits the cap geometry for one slab face of one
// layer. The exposed region is the layer's footprint minus the
// neighbor's footprint (full footprint when neighbor == nil). The
// cap is subdivided by per-section tile rectangles so each
// section paints its own quad and the dither pattern remains
// tile-aligned; tiles straddling the loop or neighbor boundary
// are Clipper-intersected to clip them exactly. Any uncovered
// residue (e.g. tiny slivers along the loop boundary where a
// tile-center test had dropped the tile) is emitted with
// `fallback` and faceSection = -1 to keep the cap watertight.
// emitLayerFaceCapUnified emits the cap geometry for one slab face
// of one layer as a single watertight surface. The exposed region
// (this layer's footprint minus the neighbor's footprint, or the
// full footprint when neighbor == nil) is triangulated once with
// Earcut, then each triangle's centroid picks a color:
//
//   - if the centroid falls inside a cap-tile section's rectangle,
//     use that section's dithered color.
//   - otherwise pick the orientation-preferred nearest ribbon
//     section in (layer, loop)'s ribbons, mirroring the legacy
//     earcut-cap colorer.
//
// This gives a watertight single-surface cap with tile-precision
// color where the dither grid covers, and slope-derived colors on
// thin step caps where no tile center landed. capDir is +1 for
// top caps, -1 for bottom caps; passed through to ribbon picking
// so cap and source-triangle orientation match.
func emitLayerFaceCapUnified(
	m *loader.LoadedModel,
	faceAssign *[]int32,
	faceSection *[]int32,
	layer *Layer,
	layerLoops [][]Point2,
	neighborLoops [][]Point2,
	z float32,
	isTop bool,
	tiles *layerTileIdx,
	tileIDs []int32,
	capDir float32,
	sections []Section,
	assignments []int32,
	fallback int32,
	ribbonsByLoop map[loopKey][]ribbonRef,
	layerIdx int,
) {
	// Build the exposed-region polygon (this layer's footprint
	// minus the neighbor's). This is the Clipper Paths form so we
	// can intersect tile rectangles against it without going back
	// through CapRegion.
	subj := pointSetsToClipperPaths(layerLoops)
	if len(subj) == 0 {
		return
	}
	var exposed clipper.Paths
	if len(neighborLoops) > 0 {
		nbr := pointSetsToClipperPaths(neighborLoops)
		if len(nbr) > 0 {
			exposed = clipperOp(subj, nbr, clipper.CtDifference)
		} else {
			exposed = subj
		}
	} else {
		exposed = subj
	}
	if len(exposed) == 0 {
		return
	}

	// Set up the ribbon-fallback colorer once per layer face. It
	// applies only to leftover triangles where no tile-section
	// rectangle contains the centroid.
	ribbonColorByLoop := make(map[int]func(p Point2) (int32, int32))
	getRibbonColorAt := func(p Point2) (int32, int32) {
		bestLoop := -1
		bestDistSq := float32(math.MaxFloat32)
		for lp := range layer.Loops {
			if layer.Loops[lp].IsHole {
				continue
			}
			pts := layer.Loops[lp].Points
			for _, q := range pts {
				dx := q[0] - p[0]
				dy := q[1] - p[1]
				d := dx*dx + dy*dy
				if d < bestDistSq {
					bestDistSq = d
					bestLoop = lp
				}
			}
		}
		if bestLoop < 0 {
			return -1, fallback
		}
		fn, ok := ribbonColorByLoop[bestLoop]
		if !ok {
			fn = makeRibbonColorPicker(
				ribbonsByLoop[loopKey{layerIdx, bestLoop}],
				sections, assignments, fallback, capDir,
			)
			ribbonColorByLoop[bestLoop] = fn
		}
		return fn(p)
	}

	// Emit per cap-tile section: clip its rectangle to the exposed
	// region, earcut the clipped piece, paint with the section's
	// dithered color. This gives the cap surface a tile-aligned
	// color grid that matches the dither pattern, instead of a
	// unified earcut blurring many tiles into one big triangle.
	allRects := make(clipper.Paths, 0, len(tileIDs))
	for _, sid := range tileIDs {
		s := sections[sid]
		col := assignments[sid]
		if col < 0 {
			col = fallback
		}
		rect := clipper.Paths{rectClipperPath(s.CapBoundsXY)}
		piece := clipperOp(rect, exposed, clipper.CtIntersection)
		allRects = append(allRects, rect[0])
		if len(piece) == 0 {
			continue
		}
		for _, r := range clipperPathsToRegions(piece) {
			emitCapTriangulation(m, faceAssign, faceSection, r, z, isTop, sid, col)
		}
	}

	// Leftover: parts of the exposed region not covered by any
	// tile rectangle. Union all tile rects then subtract from
	// exposed in one pass; emit each leftover piece with the
	// loop-scoped ribbon-fallback color so thin slope-cap rings
	// blend into the surrounding step caps' color rather than
	// flashing a stark fallback.
	var leftover clipper.Paths
	if len(allRects) > 0 {
		c := clipper.NewClipper(clipper.IoPreserveCollinear)
		c.AddPaths(allRects, clipper.PtSubject, true)
		unioned, ok := c.Execute1(clipper.CtUnion, clipper.PftNonZero, clipper.PftNonZero)
		if ok && len(unioned) > 0 {
			d := clipper.NewClipper(clipper.IoPreserveCollinear)
			d.AddPaths(exposed, clipper.PtSubject, true)
			d.AddPaths(unioned, clipper.PtClip, true)
			leftover, _ = d.Execute1(clipper.CtDifference, clipper.PftEvenOdd, clipper.PftNonZero)
		}
	} else {
		leftover = exposed
	}
	for _, region := range clipperPathsToRegions(leftover) {
		// Skip slivers below ~1µm² to avoid feeding Earcut
		// degenerate polygons it can spin on.
		if regionArea(region) < 1e-3 {
			continue
		}
		verts, tris := Earcut(region.Outer, region.Holes)
		if len(tris) == 0 {
			continue
		}
		baseV := uint32(len(m.Vertices))
		for _, p := range verts {
			m.Vertices = append(m.Vertices, [3]float32{p[0], p[1], z})
		}
		for _, tr := range tris {
			a := verts[tr[0]]
			b := verts[tr[1]]
			c := verts[tr[2]]
			cx := (a[0] + b[0] + c[0]) / 3
			cy := (a[1] + b[1] + c[1]) / 3
			sid, col := getRibbonColorAt(Point2{cx, cy})
			if isTop {
				m.Faces = append(m.Faces, [3]uint32{baseV + tr[0], baseV + tr[1], baseV + tr[2]})
			} else {
				m.Faces = append(m.Faces, [3]uint32{baseV + tr[0], baseV + tr[2], baseV + tr[1]})
			}
			*faceAssign = append(*faceAssign, col)
			*faceSection = append(*faceSection, sid)
		}
	}
}

// makeRibbonColorPicker returns a closure that picks the nearest
// orientation-preferred ribbon section for color fallback on cap
// triangles. capDir = +1 for top caps (prefer source-tri normal
// pointing up); -1 for bottom caps. Mirrors the pre-rewrite
// makeColorAt rule that kept salmon cut-surface stripes off
// dome-shaped caps.
func makeRibbonColorPicker(
	ribbons []ribbonRef,
	sections []Section,
	assignments []int32,
	fallback int32,
	capDir float32,
) func(p Point2) (int32, int32) {
	const alignedThresh = 0.05
	return func(p Point2) (int32, int32) {
		if len(ribbons) == 0 {
			return -1, fallback
		}
		bestSid := int32(-1)
		bestColor := fallback
		bestSq := float32(math.MaxFloat32)
		bestAligned := false
		for _, r := range ribbons {
			dx := r.mid[0] - p[0]
			dy := r.mid[1] - p[1]
			d := dx*dx + dy*dy
			aligned := sections[r.sid].SrcTriNormalZ*capDir > alignedThresh
			switch {
			case aligned && !bestAligned:
				bestSq = d
				bestSid = r.sid
				bestAligned = true
				if assignments[r.sid] >= 0 {
					bestColor = assignments[r.sid]
				}
			case aligned == bestAligned && d < bestSq:
				bestSq = d
				bestSid = r.sid
				if assignments[r.sid] >= 0 {
					bestColor = assignments[r.sid]
				}
			}
		}
		return bestSid, bestColor
	}
}

// layerTileIdx maps (col, row) → cap-tile section index for one
// layer of one kind. Looking up a point: convert (x, y) to (col,
// row) using the tile's grid origin (derived from any one tile's
// CapBoundsXY) and look up the section. Returns -1 if the point
// is outside the indexed cells or no section covers it.
type layerTileIdx struct {
	originX, originY float32
	cellW            float32
	hasOrigin        bool
	cells            map[[2]int]int32 // (col, row) → section id
}

// buildLayerTileIdx groups cap-tile sections of one kind by their
// layer and builds a per-layer (col, row)→section map. The map's
// grid origin is fixed at the first tile encountered for that
// layer; subsequent tiles (which should share an origin since they
// were emitted from the same partition pass) snap to it.
func buildLayerTileIdx(sections []Section, kind SectionKind) map[int]*layerTileIdx {
	out := make(map[int]*layerTileIdx)
	for i, s := range sections {
		if s.Kind != kind {
			continue
		}
		idx := out[s.LayerIdx]
		if idx == nil {
			cellW := s.CapBoundsXY[2] - s.CapBoundsXY[0]
			if cellW <= 0 {
				cellW = 1
			}
			idx = &layerTileIdx{
				originX:   s.CapBoundsXY[0] - float32(s.TileCol)*cellW,
				originY:   s.CapBoundsXY[1] - float32(s.TileRow)*cellW,
				cellW:     cellW,
				hasOrigin: true,
				cells:     map[[2]int]int32{},
			}
			out[s.LayerIdx] = idx
		}
		idx.cells[[2]int{s.TileCol, s.TileRow}] = int32(i)
	}
	return out
}

// lookup returns (sid, col) for the cap-tile section whose
// rectangle contains (x, y), or (-1, fallback-not-used) if none
// does. col is the section's dithered palette index, with -1
// substituted by the section's assignment-or-fallback policy
// — but here we only return col when sid >= 0, so the caller
// applies its own fallback.
func (g *layerTileIdx) lookup(x, y float32, sections []Section, assignments []int32) (int32, int32) {
	if g == nil || !g.hasOrigin {
		return -1, 0
	}
	c := int((x - g.originX) / g.cellW)
	r := int((y - g.originY) / g.cellW)
	if sid, ok := g.cells[[2]int{c, r}]; ok {
		col := assignments[sid]
		return sid, col
	}
	return -1, 0
}

// centroidOf returns the mean-of-vertices centroid approximation
// of `points` (a representative interior point). Sufficient for
// color lookup on a small fallback region.
func centroidOf(points []Point2) Point2 {
	if len(points) == 0 {
		return Point2{}
	}
	var sx, sy float32
	for _, p := range points {
		sx += p[0]
		sy += p[1]
	}
	return Point2{sx / float32(len(points)), sy / float32(len(points))}
}

// globalCapIdx is a coarse XY spatial bucket of cap-tile section
// midpoints across all layers, restricted to one Kind. Used to
// pick a color for fallback cap regions in layers where no tile
// section landed inside (e.g. a sub-cellSize annulus on a steep
// slope) — the nearest cap section's color produces a coherent
// roof-like patch rather than the model's overall fallback color.
type globalCapIdx struct {
	ids     []int32
	cellSz  float32
	minX    float32
	minY    float32
	cols    int
	rows    int
	buckets [][]int32 // section indices per (col, row)
}

// buildGlobalCapIdx builds the index for a single Kind. Bucket
// size is the cap-tile cellSize derived from any section's
// CapBoundsXY.
func buildGlobalCapIdx(sections []Section, kind SectionKind) *globalCapIdx {
	var ids []int32
	for i, s := range sections {
		if s.Kind == kind {
			ids = append(ids, int32(i))
		}
	}
	if len(ids) == 0 {
		return &globalCapIdx{}
	}
	first := sections[ids[0]]
	cell := first.CapBoundsXY[2] - first.CapBoundsXY[0]
	if cell <= 0 {
		cell = 1
	}
	minX := float32(math.MaxFloat32)
	minY := float32(math.MaxFloat32)
	maxX := float32(-math.MaxFloat32)
	maxY := float32(-math.MaxFloat32)
	for _, id := range ids {
		m := sections[id].Mid
		if m[0] < minX {
			minX = m[0]
		}
		if m[1] < minY {
			minY = m[1]
		}
		if m[0] > maxX {
			maxX = m[0]
		}
		if m[1] > maxY {
			maxY = m[1]
		}
	}
	cols := int((maxX-minX)/cell) + 1
	rows := int((maxY-minY)/cell) + 1
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	buckets := make([][]int32, cols*rows)
	for _, id := range ids {
		m := sections[id].Mid
		c := int((m[0] - minX) / cell)
		r := int((m[1] - minY) / cell)
		if c < 0 {
			c = 0
		}
		if c >= cols {
			c = cols - 1
		}
		if r < 0 {
			r = 0
		}
		if r >= rows {
			r = rows - 1
		}
		buckets[r*cols+c] = append(buckets[r*cols+c], id)
	}
	return &globalCapIdx{ids: ids, cellSz: cell, minX: minX, minY: minY, cols: cols, rows: rows, buckets: buckets}
}

// lookup returns (sid, col) for the nearest cap section to (x, y)
// in XY distance, or (-1, fallback) if the index is empty. Expands
// the bucket search radius until it finds a candidate, then linear-
// scans nearby buckets for the actual nearest.
func (g *globalCapIdx) lookup(p Point2, sections []Section, assignments []int32, fallback int32) (int32, int32) {
	if g == nil || len(g.ids) == 0 {
		return -1, fallback
	}
	x, y := p[0], p[1]
	c := int((x - g.minX) / g.cellSz)
	r := int((y - g.minY) / g.cellSz)
	best := int32(-1)
	bestSq := float32(math.MaxFloat32)
	radius := 1
	for {
		for dr := -radius; dr <= radius; dr++ {
			for dc := -radius; dc <= radius; dc++ {
				cc, rr := c+dc, r+dr
				if cc < 0 || cc >= g.cols || rr < 0 || rr >= g.rows {
					continue
				}
				for _, id := range g.buckets[rr*g.cols+cc] {
					m := sections[id].Mid
					dx := m[0] - x
					dy := m[1] - y
					d := dx*dx + dy*dy
					if d < bestSq {
						bestSq = d
						best = id
					}
				}
			}
		}
		if best >= 0 || radius > g.cols+g.rows {
			break
		}
		radius++
	}
	if best < 0 {
		return -1, fallback
	}
	col := assignments[best]
	if col < 0 {
		col = fallback
	}
	return best, col
}

// insideLoopsXY returns true when (x, y) is inside the solid region
// of `loops` using even-odd nesting: containment by an odd number of
// loops. Returns false when loops is empty (treating "no layer" as
// all-air).
func insideLoopsXY(loops []Loop, x, y float32) bool {
	count := 0
	for i := range loops {
		if len(loops[i].Points) < 3 {
			continue
		}
		if pointInPolygon(loops[i].Points, x, y) {
			count++
		}
	}
	return (count & 1) == 1
}

// emitCapQuad emits a single axis-aligned rectangle as 2 triangles
// at the given Z, with winding chosen for the face direction. This
// is the fast path for fully-exposed cap tiles — no Clipper or
// earcut needed.
func emitCapQuad(
	m *loader.LoadedModel,
	faceAssign *[]int32,
	faceSection *[]int32,
	bounds [4]float32,
	z float32,
	isTop bool,
	sid, col int32,
) {
	x0, y0, x1, y1 := bounds[0], bounds[1], bounds[2], bounds[3]
	baseV := uint32(len(m.Vertices))
	m.Vertices = append(m.Vertices,
		[3]float32{x0, y0, z},
		[3]float32{x1, y0, z},
		[3]float32{x1, y1, z},
		[3]float32{x0, y1, z})
	if isTop {
		m.Faces = append(m.Faces,
			[3]uint32{baseV, baseV + 1, baseV + 2},
			[3]uint32{baseV, baseV + 2, baseV + 3})
	} else {
		m.Faces = append(m.Faces,
			[3]uint32{baseV, baseV + 2, baseV + 1},
			[3]uint32{baseV, baseV + 3, baseV + 2})
	}
	*faceAssign = append(*faceAssign, col, col)
	*faceSection = append(*faceSection, sid, sid)
}

// mostCommonNonNegSafe returns the most frequent non-negative
// palette index in a, or 0 if there are no non-negative entries.
func mostCommonNonNegSafe(a []int32) int32 {
	counts := map[int32]int{}
	for _, v := range a {
		if v >= 0 {
			counts[v]++
		}
	}
	var best int32
	bestN := -1
	for k, n := range counts {
		if n > bestN {
			best = k
			bestN = n
		}
	}
	return best
}

// emitCapTriangulation triangulates one polygon-with-holes region
// of a cap surface at the given Z and appends the triangles to m.
// isTop selects the winding so the normal faces +Z (top cap) or -Z
// (bottom cap). All emitted triangles share (sid, col); a section
// id of -1 marks a leftover-sliver region the caller has no
// section for.
func emitCapTriangulation(
	m *loader.LoadedModel,
	faceAssign *[]int32,
	faceSection *[]int32,
	region CapRegion,
	z float32,
	isTop bool,
	sid int32,
	col int32,
) {
	verts, tris := Earcut(region.Outer, region.Holes)
	if len(tris) == 0 {
		return
	}
	baseV := uint32(len(m.Vertices))
	for _, p := range verts {
		m.Vertices = append(m.Vertices, [3]float32{p[0], p[1], z})
	}
	for _, tr := range tris {
		if isTop {
			m.Faces = append(m.Faces, [3]uint32{baseV + tr[0], baseV + tr[1], baseV + tr[2]})
		} else {
			m.Faces = append(m.Faces, [3]uint32{baseV + tr[0], baseV + tr[2], baseV + tr[1]})
		}
		*faceAssign = append(*faceAssign, col)
		*faceSection = append(*faceSection, sid)
	}
}

// snapToClipperGrid rounds a Point2 to the same int-coordinate
// grid that pointSetsToClipperPaths quantizes onto when feeding
// Clipper. The wall emitter snaps its subdivided vertex sequence
// through this so cap vertices coming back out of Clipper (which
// are float32-of-int round-tripped) match wall vertex positions
// bit-exactly. Without the snap, an arc-length section breakpoint
// like 3.0769231 mm becomes 3.077 mm after Clipper round-trip and
// the cap stops sharing vertices with the wall — recreating the
// T-junction shimmer.
func snapToClipperGrid(p Point2) Point2 {
	return Point2{
		float32(math.Round(float64(p[0])*clipperScale) / clipperScale),
		float32(math.Round(float64(p[1])*clipperScale) / clipperScale),
	}
}

// buildLoopWallSeq walks one loop in original arc order, inserting
// a vertex at each ribbon section's StartArc and at every original
// loop vertex strictly inside a section's arc range. Vertices are
// snapped to the Clipper grid so cap geometry (which goes through
// Clipper int-coordinate round-trip) matches wall geometry
// vertex-for-vertex at the shared slab boundary. Returns the
// polygon vertex sequence plus parallel slices of per-vertex color
// (the section's palette assignment) and section id.
func buildLoopWallSeq(
	loop *Loop,
	sectionIDs []int,
	sections []Section,
	assignments []int32,
) (pts []Point2, cols []int32, secs []int32) {
	src := loop.Points
	n := len(src)
	if n < 3 || len(sectionIDs) == 0 {
		return nil, nil, nil
	}
	cum := loopCumLen(src)
	pts = make([]Point2, 0, n*2)
	cols = make([]int32, 0, n*2)
	secs = make([]int32, 0, n*2)
	for _, sid := range sectionIDs {
		s := sections[sid]
		color := assignments[sid]
		pts = append(pts, snapToClipperGrid(pointAtArc(src, cum, s.StartArc)))
		cols = append(cols, color)
		secs = append(secs, int32(sid))
		for i := 0; i < n; i++ {
			a := cum[i]
			if a > s.StartArc+1e-5 && a < s.EndArc-1e-5 {
				pts = append(pts, snapToClipperGrid(src[i]))
				cols = append(cols, color)
				secs = append(secs, int32(sid))
			}
		}
	}
	return pts, cols, secs
}

// emitLoopWall appends only the wall geometry for one loop — no
// caps. faceAssign receives one palette index per triangle (each
// quad contributes two).
//
// Outer loops want CCW orientation (interior = print material; wall
// normal faces outward). Hole loops want CW orientation (interior =
// cavity; wall normal faces into the cavity). The polygon is
// re-oriented as needed before walking.
func emitLoopWall(
	m *loader.LoadedModel,
	faceAssign *[]int32,
	faceSection *[]int32,
	loop *Loop,
	sectionIDs []int,
	sections []Section,
	assignments []int32,
	zBot, zTop float32,
	isHole bool,
) {
	if len(loop.Points) < 3 {
		return
	}
	// Sections' StartArc / EndArc live in the loop's *original* arc
	// frame (the order it was chained + partitioned in). Walls must
	// be assembled in that same frame so per-section arc lookups
	// stay valid: a section's color is sampled at its loop-arc
	// midpoint, and we want that color painted on the wall span
	// covering the same arc range. If we reversed pts for winding
	// here, a section at arc 0..2 in the original frame would end
	// up painted across the OPPOSITE arc on the wall, mirroring
	// the texture around the loop. For symmetric shapes (a sliced
	// sphere, say) this looks like an X-axis flip in the rendered
	// output. Instead we walk pts in original order and flip the
	// triangle winding below to control outward/inward normals.
	ccw := loop.SignedArea > 0
	wantCCW := !isHole
	flipWinding := ccw != wantCCW

	wallPts, wallColors, wallSections := buildLoopWallSeq(loop, sectionIDs, sections, assignments)
	if len(wallPts) < 3 {
		return
	}

	w := len(wallPts)
	baseV := uint32(len(m.Vertices))
	for _, p := range wallPts {
		m.Vertices = append(m.Vertices, [3]float32{p[0], p[1], zBot})
		m.Vertices = append(m.Vertices, [3]float32{p[0], p[1], zTop})
	}
	for i := 0; i < w; i++ {
		j := (i + 1) % w
		i0 := baseV + uint32(2*i)
		i1 := baseV + uint32(2*i+1)
		j0 := baseV + uint32(2*j)
		j1 := baseV + uint32(2*j+1)
		if flipWinding {
			m.Faces = append(m.Faces, [3]uint32{i0, j1, j0})
			m.Faces = append(m.Faces, [3]uint32{i0, i1, j1})
		} else {
			m.Faces = append(m.Faces, [3]uint32{i0, j0, j1})
			m.Faces = append(m.Faces, [3]uint32{i0, j1, i1})
		}
		col := wallColors[i]
		sec := wallSections[i]
		*faceAssign = append(*faceAssign, col, col)
		*faceSection = append(*faceSection, sec, sec)
	}
}

// SafeAssignments substitutes -1 entries (interior faces) with a
// fallback palette index so downstream tools that don't tolerate
// negative assignments behave correctly. Returns a new slice.
func SafeAssignments(assignments []int32, fallback int32) []int32 {
	out := make([]int32, len(assignments))
	for i, a := range assignments {
		if a < 0 {
			out[i] = fallback
		} else {
			out[i] = a
		}
	}
	return out
}

// MeshExtents returns the XYZ bbox of m's vertices for diagnostics.
func MeshExtents(m *loader.LoadedModel) (mn, mx [3]float32) {
	if len(m.Vertices) == 0 {
		return
	}
	mn = m.Vertices[0]
	mx = m.Vertices[0]
	for _, v := range m.Vertices {
		for k := 0; k < 3; k++ {
			if v[k] < mn[k] {
				mn[k] = v[k]
			}
			if v[k] > mx[k] {
				mx[k] = v[k]
			}
		}
	}
	return
}
