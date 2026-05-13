package minislicer

import (
	"math"
	"runtime"
	"sync"

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

	// Per-layer cap-tile section IDs, partitioned by Kind. The
	// cap emitter Clipper-clips each rectangle to the exposed
	// region and paints with the section's dithered color.
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

	// Build per-layer mesh chunks in parallel — per-tile Clipper
	// dominates cap emission and each layer is independent. Each
	// worker emits into a local chunk whose face indices are
	// chunk-local; we concatenate chunks in layer order at the end
	// and offset the indices accordingly.
	chunks := make([]meshChunk, len(layers))
	workCh := make(chan int, len(layers))
	for li := range layers {
		workCh <- li
	}
	close(workCh)
	nw := runtime.GOMAXPROCS(0)
	if nw > len(layers) {
		nw = len(layers)
	}
	if nw < 1 {
		nw = 1
	}
	var wg sync.WaitGroup
	for w := 0; w < nw; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for li := range workCh {
				layer := &layers[li]
				if len(layer.Loops) == 0 {
					continue
				}
				zBot := layer.Z - layerH/2
				zTop := layer.Z + layerH/2

				ch := &chunks[li]

				// Walls for this layer.
				for lp := range layer.Loops {
					loop := &layer.Loops[lp]
					ids := loopSecs[loopKey{li, lp}]
					if len(ids) == 0 || sections[ids[0]].Kind != KindRibbon {
						continue
					}
					emitLoopWallChunk(ch, loop, ids, sections, assignments,
						zBot, zTop, loop.IsHole)
				}

				// Caps for this layer.
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
				emitClippedCapTilesChunk(ch, layer, wallLoops[li], aboveLoops,
					zTop, true, topByLayer[li], +1,
					sections, assignments, fallback, layerLoopRibbons, li)
				emitClippedCapTilesChunk(ch, layer, wallLoops[li], belowLoops,
					zBot, false, botByLayer[li], -1,
					sections, assignments, fallback, layerLoopRibbons, li)
			}
		}()
	}
	wg.Wait()

	// Concatenate chunks in layer order, offsetting face indices
	// so they land in the final flat vertex slice.
	totalV, totalF := 0, 0
	for i := range chunks {
		totalV += len(chunks[i].verts)
		totalF += len(chunks[i].faces)
	}
	m.Vertices = make([][3]float32, 0, totalV)
	m.Faces = make([][3]uint32, 0, totalF)
	faceAssign = make([]int32, 0, totalF)
	faceSection = make([]int32, 0, totalF)
	for i := range chunks {
		baseV := uint32(len(m.Vertices))
		m.Vertices = append(m.Vertices, chunks[i].verts...)
		for _, tr := range chunks[i].faces {
			m.Faces = append(m.Faces, [3]uint32{tr[0] + baseV, tr[1] + baseV, tr[2] + baseV})
		}
		faceAssign = append(faceAssign, chunks[i].colors...)
		faceSection = append(faceSection, chunks[i].sections...)
	}

	return m, faceAssign, faceSection
}

// meshChunk is a per-layer accumulator the parallel workers emit
// into. Face indices are local to verts; the main loop offsets
// them when stitching chunks together.
type meshChunk struct {
	verts    [][3]float32
	faces    [][3]uint32
	colors   []int32
	sections []int32
}

func (c *meshChunk) addVert(x, y, z float32) uint32 {
	idx := uint32(len(c.verts))
	c.verts = append(c.verts, [3]float32{x, y, z})
	return idx
}

func (c *meshChunk) addFace(a, b, d uint32, col, sec int32) {
	c.faces = append(c.faces, [3]uint32{a, b, d})
	c.colors = append(c.colors, col)
	c.sections = append(c.sections, sec)
}

// ribbonRef is a ribbon section's id and XY midpoint. Used by the
// cap colorer to find the nearest ribbon section for triangles
// that don't land inside any tile rectangle.
type ribbonRef struct {
	sid int32
	mid Point2
}

// emitClippedCapTilesChunk emits the cap geometry for one slab
// face of one layer into a meshChunk. The exposed region is this
// layer's footprint minus the neighbor's footprint (full footprint
// when neighbor == nil); we subdivide it tile-by-tile so the
// dither pattern stays at tile resolution rather than blurring
// across earcut's giant triangles.
//
// For each cap-tile section:
//   - if all four corners of the tile rectangle pass the inside-
//     layer / outside-neighbor test, emit the rectangle as a quad
//     directly (no Clipper call needed — the rect is already fully
//     inside the exposed region).
//   - otherwise Clipper-intersect the rect with the exposed region
//     and earcut the result.
//
// After all tile pieces, any leftover sliver of the exposed region
// (the staircase-to-loop gap where partition didn't drop a tile
// center) is subtracted and triangulated with a loop-scoped
// ribbon-derived fallback color, so thin slope-cap rings blend in
// with the surrounding step caps instead of flashing a stark
// most-common color. capDir is +1 for top caps, -1 for bottom
// caps; it routes to the orientation-matched ribbon picker.
func emitClippedCapTilesChunk(
	ch *meshChunk,
	layer *Layer,
	layerLoops [][]Point2,
	neighborLoops [][]Point2,
	z float32,
	isTop bool,
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

	// Emit per cap-tile section: rect ∩ exposed → emit. Fully-
	// exposed tiles (all 4 corners pass the inside-layer /
	// outside-neighbor test) skip Clipper entirely and emit as
	// quads — that's the common case for interior tiles in a flat
	// top/bottom cap, where Clipper-clipping every one of
	// thousands of tiles otherwise dominates the Clip stage's
	// runtime. Bbox pre-check on each polygon membership test
	// makes the corner classification near-free for tiles well
	// inside or outside loop boundaries.
	type bbox struct{ minX, minY, maxX, maxY float32 }
	pointsBBox := func(pts []Point2) bbox {
		if len(pts) == 0 {
			return bbox{}
		}
		b := bbox{pts[0][0], pts[0][1], pts[0][0], pts[0][1]}
		for _, p := range pts[1:] {
			if p[0] < b.minX {
				b.minX = p[0]
			}
			if p[0] > b.maxX {
				b.maxX = p[0]
			}
			if p[1] < b.minY {
				b.minY = p[1]
			}
			if p[1] > b.maxY {
				b.maxY = p[1]
			}
		}
		return b
	}
	layerLoopPts := make([][]Point2, 0, len(layer.Loops))
	layerLoopBB := make([]bbox, 0, len(layer.Loops))
	for i := range layer.Loops {
		if len(layer.Loops[i].Points) < 3 {
			continue
		}
		layerLoopPts = append(layerLoopPts, layer.Loops[i].Points)
		layerLoopBB = append(layerLoopBB, pointsBBox(layer.Loops[i].Points))
	}
	neighborBB := make([]bbox, 0, len(neighborLoops))
	for _, pts := range neighborLoops {
		if len(pts) < 3 {
			continue
		}
		neighborBB = append(neighborBB, pointsBBox(pts))
	}
	insideEvenOdd := func(loops [][]Point2, bbs []bbox, x, y float32) bool {
		count := 0
		for i, pts := range loops {
			b := bbs[i]
			if x < b.minX || x > b.maxX || y < b.minY || y > b.maxY {
				continue
			}
			if pointInPolygon(pts, x, y) {
				count++
			}
		}
		return (count & 1) == 1
	}
	allRects := make(clipper.Paths, 0, len(tileIDs))
	cornerExposed := func(x, y float32) bool {
		if !insideEvenOdd(layerLoopPts, layerLoopBB, x, y) {
			return false
		}
		if len(neighborLoops) == 0 {
			return true
		}
		return !insideEvenOdd(neighborLoops, neighborBB, x, y)
	}
	for _, sid := range tileIDs {
		s := sections[sid]
		col := assignments[sid]
		if col < 0 {
			col = fallback
		}
		b := s.CapBoundsXY
		allRects = append(allRects, rectClipperPath(b))
		if cornerExposed(b[0], b[1]) && cornerExposed(b[2], b[1]) &&
			cornerExposed(b[2], b[3]) && cornerExposed(b[0], b[3]) {
			// Fast path: tile is entirely inside the exposed
			// region. Emit as a single quad, no Clipper call.
			emitCapQuadChunk(ch, b, z, isTop, sid, col)
			continue
		}
		// Boundary tile: clip against the exposed region.
		rect := clipper.Paths{rectClipperPath(b)}
		piece := clipperOp(rect, exposed, clipper.CtIntersection)
		if len(piece) == 0 {
			continue
		}
		for _, r := range clipperPathsToRegions(piece) {
			emitCapTriangulationChunk(ch, r, z, isTop, sid, col)
		}
	}

	// Leftover: parts of the exposed region not covered by any
	// tile rectangle (a thin annulus where partition's 5-point
	// exposure test dropped every sample inside a sub-cellSize
	// step). Pass all rects directly as the clip with NonZero
	// fill so adjacent shared edges don't cancel under even-odd —
	// one Clipper call instead of an explicit union + difference.
	var leftover clipper.Paths
	if len(allRects) > 0 {
		d := clipper.NewClipper(clipper.IoPreserveCollinear)
		d.AddPaths(exposed, clipper.PtSubject, true)
		d.AddPaths(allRects, clipper.PtClip, true)
		leftover, _ = d.Execute1(clipper.CtDifference, clipper.PftEvenOdd, clipper.PftNonZero)
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
		baseV := uint32(len(ch.verts))
		for _, p := range verts {
			ch.verts = append(ch.verts, [3]float32{p[0], p[1], z})
		}
		for _, tr := range tris {
			a := verts[tr[0]]
			b := verts[tr[1]]
			c := verts[tr[2]]
			cx := (a[0] + b[0] + c[0]) / 3
			cy := (a[1] + b[1] + c[1]) / 3
			sid, col := getRibbonColorAt(Point2{cx, cy})
			if isTop {
				ch.addFace(baseV+tr[0], baseV+tr[1], baseV+tr[2], col, sid)
			} else {
				ch.addFace(baseV+tr[0], baseV+tr[2], baseV+tr[1], col, sid)
			}
		}
	}
}

// emitCapQuadChunk emits a rectangle as 2 triangles into a
// meshChunk. Fast path for fully-exposed cap tiles.
func emitCapQuadChunk(
	ch *meshChunk,
	bounds [4]float32,
	z float32,
	isTop bool,
	sid, col int32,
) {
	x0, y0, x1, y1 := bounds[0], bounds[1], bounds[2], bounds[3]
	a := ch.addVert(x0, y0, z)
	b := ch.addVert(x1, y0, z)
	c := ch.addVert(x1, y1, z)
	d := ch.addVert(x0, y1, z)
	if isTop {
		ch.addFace(a, b, c, col, sid)
		ch.addFace(a, c, d, col, sid)
	} else {
		ch.addFace(a, c, b, col, sid)
		ch.addFace(a, d, c, col, sid)
	}
}

// emitCapTriangulationChunk earcuts one polygon-with-holes cap
// region into a meshChunk at the given Z.
func emitCapTriangulationChunk(
	ch *meshChunk,
	region CapRegion,
	z float32,
	isTop bool,
	sid, col int32,
) {
	verts, tris := Earcut(region.Outer, region.Holes)
	if len(tris) == 0 {
		return
	}
	baseV := uint32(len(ch.verts))
	for _, p := range verts {
		ch.verts = append(ch.verts, [3]float32{p[0], p[1], z})
	}
	for _, tr := range tris {
		if isTop {
			ch.addFace(baseV+tr[0], baseV+tr[1], baseV+tr[2], col, sid)
		} else {
			ch.addFace(baseV+tr[0], baseV+tr[2], baseV+tr[1], col, sid)
		}
	}
}

// emitLoopWallChunk emits the wall tube for one loop into a chunk.
// Outer loops want CCW (interior = material; wall normal outward);
// hole loops want CW (interior = cavity; normal into cavity).
func emitLoopWallChunk(
	ch *meshChunk,
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
	ccw := loop.SignedArea > 0
	wantCCW := !isHole
	flipWinding := ccw != wantCCW

	wallPts, wallColors, wallSections := buildLoopWallSeq(loop, sectionIDs, sections, assignments)
	if len(wallPts) < 3 {
		return
	}

	w := len(wallPts)
	baseV := uint32(len(ch.verts))
	for _, p := range wallPts {
		ch.verts = append(ch.verts,
			[3]float32{p[0], p[1], zBot},
			[3]float32{p[0], p[1], zTop})
	}
	for i := 0; i < w; i++ {
		j := (i + 1) % w
		i0 := baseV + uint32(2*i)
		i1 := baseV + uint32(2*i+1)
		j0 := baseV + uint32(2*j)
		j1 := baseV + uint32(2*j+1)
		col := wallColors[i]
		sec := wallSections[i]
		if flipWinding {
			ch.addFace(i0, j1, j0, col, sec)
			ch.addFace(i0, i1, j1, col, sec)
		} else {
			ch.addFace(i0, j0, j1, col, sec)
			ch.addFace(i0, j1, i1, col, sec)
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
