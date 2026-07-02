package cellslicer

import (
	"math"

	clipper "github.com/ctessum/go.clipper"
	colorful "github.com/lucasb-eyer/go-colorful"
)

// colorcut.go segments a slab's coverTarget into monochrome sub-regions
// whose boundaries follow high-contrast colour edges, so the cell
// partition can tile each region independently and land cell boundaries
// on colour boundaries (a checkerboard tiles into pure black / pure
// white cells instead of straddling cells that average to grey).
//
// The hard invariant is that no returned region is smaller than one
// cell: a colour feature narrower than cellSize cannot be honoured
// without producing a sub-minimum cell, so it is merged into a
// neighbour (and that boundary is simply not cut). This is the
// resolution floor — seed placement cannot beat it — and it lives here,
// in segmentation, before any footprint is cut.

// ColorRegions partitions coverTarget into monochrome sub-region
// footprints. sample(x, y) returns the printed-surface colour at the
// slab-plane point and whether a surface was hit there.
//
// contrastDeltaE is the cut threshold in standard CIE76 ΔE units:
// neighbouring samples join into one region while their perceptual
// distance is <= contrastDeltaE, so a smooth gradient (every step small)
// stays one region and only a colour jump exceeding the threshold becomes
// a boundary. 0 cuts on any difference; ~15-25 ignores soft shading and
// cuts only crisp edges.
//
// Guarantees:
//   - every returned region admits a disk of diameter ~cellSize (no
//     region is thinner than one cell anywhere it matters — sub-cell
//     features are merged into a neighbour);
//   - the returned regions are disjoint and their union is coverTarget
//     (clipped exactly to it), so tiling each one tiles the whole shell.
//
// Returns nil when there is nothing to gain (no surface sampled, or the
// whole region is one colour) — the caller then uses the plain
// single-region partition, which is bit-identical to the old behaviour.
func ColorRegions(coverTarget *Footprint, cellSize float32, contrastDeltaE float64, sample func(x, y float32) ([3]uint8, bool)) []*Footprint {
	if coverTarget == nil || len(coverTarget.Loops) == 0 || cellSize <= 0 || sample == nil {
		return nil
	}
	g := buildColorGrid(coverTarget, cellSize, contrastDeltaE, sample)
	if g == nil || g.insideCount == 0 {
		return nil
	}
	g.labelComponents()
	g.enforceMinSize(cellSize)
	regions := g.buildRegionFootprints(coverTarget)
	if len(regions) <= 1 {
		// One colour (or one surviving region) — no boundary to honour.
		return nil
	}
	return regions
}

// PartitionSlabAnalyticColor is the colour-aware twin of
// PartitionSlabAnalytic. It computes the same band/cap region algebra,
// then segments coverTarget by colour (ColorRegions) and tiles each
// monochrome sub-region independently so cell boundaries land on
// colour boundaries. When there is no boundary worth honouring
// (single colour, or every candidate edge is sub-cell), it falls back
// to the plain partition and is byte-for-byte identical to it.
//
// sample(x, y) returns the printed-surface colour at the slab plane.
// contrastDeltaE is the ColorRegions cut threshold (standard CIE76 ΔE).
func PartitionSlabAnalyticColor(fpCur, fpBelow, fpAbove *Footprint, cellSize float32, contrastDeltaE float64, sample func(x, y float32) ([3]uint8, bool)) ([]Cell, *Footprint, PartitionStats) {
	var stats PartitionStats
	if fpCur == nil || len(fpCur.Loops) == 0 {
		return nil, nil, stats
	}
	// Same shell algebra as PartitionSlabAnalytic (shared helper), so the
	// no-cut fallback below stays byte-identical to the plain path.
	innerCap, coverTarget := slabCoverRegions(fpCur, fpBelow, fpAbove, cellSize)

	regions := ColorRegions(coverTarget, cellSize, contrastDeltaE, sample)
	if len(regions) == 0 {
		// No colour boundary to honour — defer to the plain path so the
		// no-cut case stays bit-identical to the pre-existing behaviour.
		return PartitionSlabAnalytic(fpCur, fpBelow, fpAbove, cellSize)
	}

	pxArea := (cellSize / 4) * (cellSize / 4)
	var cells []Cell
	for _, region := range regions {
		// A region's cap fill is only where it overlaps the exposed cap;
		// a pure-wall region (band only) gets ring seeds and no cap seeds.
		capMask := FootprintIntersect(innerCap, region)
		cells = append(cells, tileColorRegion(region, capMask, cellSize, pxArea, &stats)...)
	}
	stats.Final = len(cells)
	// Tag silhouette edges for open-ending. Colour-cut edges are interior
	// to fpCur, so MarkOuterEdges' rule-2 (half-space outside the edge must
	// be outside fpCur) leaves them closed — exactly what we want.
	MarkOuterEdges(cells, fpCur)
	return cells, coverTarget, stats
}

// tileColorRegion tiles one monochrome sub-region with the existing
// seed families, treating the region's whole boundary (silhouette part
// AND colour-cut part) as a ring boundary: ring seeds inset cellSize/2
// from every edge keep the boundary cell off the cut so it can't become
// a sliver, and the cell against the cut grows to span from that inset
// seed out to the edge — the "spend a cell to honour the boundary"
// behaviour, for free from the existing inset logic.
func tileColorRegion(region, capMask *Footprint, cellSize, pxArea float32, stats *PartitionStats) []Cell {
	// Thin the ring seeds to a min spacing. A region only cellSize wide
	// (a wall band, the common colour-cut case) makes ringSeeds inset
	// cellSize/2 from BOTH long edges onto the same centreline, emitting a
	// near-coincident double row; coincident seeds have an undefined
	// Voronoi bisector, so their cells don't clip each other and overlap
	// (~2× area). Greedy Poisson-disk thinning — the same pass
	// concentricCapSeeds runs for cap seeds — collapses the double row to
	// a single centreline row, giving clean non-overlapping cells ≈cellSize.
	// Wider regions' ring seeds are already ≥cellSize apart, so thinning is
	// a no-op there.
	ringS := thinSeedsBySpacing(ringSeeds(region, cellSize), cellSize)
	capS := concentricCapSeeds(region, capMask, cellSize, ringS)
	stats.RawRing += len(ringS)
	stats.RawHex += len(capS)
	kinds := make([]CellKind, 0, len(ringS)+len(capS))
	for range ringS {
		kinds = append(kinds, KindRing)
	}
	for range capS {
		kinds = append(kinds, KindHex)
	}
	seeds := make([]Point2, 0, len(ringS)+len(capS))
	seeds = append(seeds, ringS...)
	seeds = append(seeds, capS...)
	return voronoiCells(seeds, kinds, region, cellSize, pxArea)
}

// thinSeedsBySpacing greedily drops any seed within minSeedSpacingFrac·
// cellSize of an already-kept seed (Poisson-disk thinning), preserving
// input order so the first of a near-coincident pair survives. The grid
// is seeded with the first point for a valid bucket origin, then grown
// incrementally as seeds are accepted.
func thinSeedsBySpacing(seeds []Point2, cellSize float32) []Point2 {
	if len(seeds) < 2 {
		return seeds
	}
	minSpacing := minSeedSpacingFrac * cellSize
	kept := make([]Point2, 0, len(seeds))
	kept = append(kept, seeds[0])
	g := newSeedGrid([]Point2{seeds[0]}, cellSize)
	for _, p := range seeds[1:] {
		if g.hasCloserThan(p, minSpacing) {
			continue
		}
		g.add(p)
		kept = append(kept, p)
	}
	return kept
}

// colorGrid is a regular sampling lattice over coverTarget's bounding
// box. Each node carries its sampled colour (and a miss flag where the
// ray found no surface) and, after labelComponents, its connected-
// component label. Off-region nodes are inside=false and never labelled.
type colorGrid struct {
	pitch      float32 // node spacing in mm
	minX, minY float32 // world coords of node (0,0)
	cols, rows int
	inside     []bool     // len cols*rows
	col        [][3]uint8 // sampled colour, valid where inside && !miss
	miss       []bool     // ray found no surface at this node
	label      []int32    // component id, -1 until labelled

	contrast    float64 // CIE76 ΔE join threshold
	insideCount int
}

// deltaE76 is the CIE76 (Euclidean CIELAB) perceptual distance between
// two sRGB triplets, in standard ΔE units (go-colorful's Lab is scaled
// 1/100, so ×100 restores the familiar 0..100 range used by colorSnap).
func deltaE76(a, b [3]uint8) float64 {
	ca := colorful.Color{R: float64(a[0]) / 255, G: float64(a[1]) / 255, B: float64(a[2]) / 255}
	cb := colorful.Color{R: float64(b[0]) / 255, G: float64(b[1]) / 255, B: float64(b[2]) / 255}
	return ca.DistanceLab(cb) * 100
}

func footprintBbox(fp *Footprint) (minX, minY, maxX, maxY float32) {
	first := true
	for i := range fp.Loops {
		lp := &fp.Loops[i]
		if first {
			minX, minY, maxX, maxY = lp.MinX, lp.MinY, lp.MaxX, lp.MaxY
			first = false
			continue
		}
		if lp.MinX < minX {
			minX = lp.MinX
		}
		if lp.MinY < minY {
			minY = lp.MinY
		}
		if lp.MaxX > maxX {
			maxX = lp.MaxX
		}
		if lp.MaxY > maxY {
			maxY = lp.MaxY
		}
	}
	return
}

// colorGridPitchFrac sets node spacing relative to cellSize. cellSize/4
// puts ~2 grid cells in the min-size erosion radius (cellSize/2), enough
// to resolve a one-cell-wide band without exploding node count.
const colorGridPitchFrac = 0.25

func buildColorGrid(coverTarget *Footprint, cellSize float32, contrastDeltaE float64, sample func(x, y float32) ([3]uint8, bool)) *colorGrid {
	minX, minY, maxX, maxY := footprintBbox(coverTarget)
	pitch := cellSize * colorGridPitchFrac
	if pitch <= 0 {
		return nil
	}
	// One node margin so boundary nodes' squares cover the footprint edge.
	cols := int((maxX-minX)/pitch) + 2
	rows := int((maxY-minY)/pitch) + 2
	if cols < 1 || rows < 1 {
		return nil
	}
	g := &colorGrid{
		pitch:    pitch,
		minX:     minX,
		minY:     minY,
		cols:     cols,
		rows:     rows,
		inside:   make([]bool, cols*rows),
		col:      make([][3]uint8, cols*rows),
		miss:     make([]bool, cols*rows),
		label:    make([]int32, cols*rows),
		contrast: contrastDeltaE,
	}
	for r := 0; r < rows; r++ {
		y := minY + float32(r)*pitch
		for c := 0; c < cols; c++ {
			x := minX + float32(c)*pitch
			idx := r*cols + c
			g.label[idx] = -1
			if !coverTarget.Contains(x, y) {
				continue
			}
			g.inside[idx] = true
			g.insideCount++
			col, ok := sample(x, y)
			if !ok {
				g.miss[idx] = true
			} else {
				g.col[idx] = col
			}
		}
	}
	return g
}

// labelComponents grows 4-connected regions where each step joins a node
// to its neighbour only while their perceptual distance stays within the
// contrast threshold (miss nodes join other miss nodes). Comparing each
// neighbour to the EXPANDING node (not the seed) means a smooth gradient
// — every adjacent step small — flows into a single region, while a
// colour jump exceeding the threshold halts growth and starts a new one.
// Explicit-stack flood fill (slab grids can be large).
func (g *colorGrid) labelComponents() {
	var next int32
	stack := make([]int, 0, 256)
	for start := range g.inside {
		if !g.inside[start] || g.label[start] != -1 {
			continue
		}
		lab := next
		next++
		g.label[start] = lab
		stack = stack[:0]
		stack = append(stack, start)
		for len(stack) > 0 {
			idx := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			r := idx / g.cols
			c := idx % g.cols
			for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nc, nr := c+d[0], r+d[1]
				if nc < 0 || nc >= g.cols || nr < 0 || nr >= g.rows {
					continue
				}
				nidx := nr*g.cols + nc
				if !g.inside[nidx] || g.label[nidx] != -1 {
					continue
				}
				if !g.joins(idx, nidx) {
					continue
				}
				g.label[nidx] = lab
				stack = append(stack, nidx)
			}
		}
	}
}

// joins reports whether neighbour node b grows into node a's region:
// miss joins only miss; surface joins surface within the ΔE threshold.
func (g *colorGrid) joins(a, b int) bool {
	if g.miss[a] || g.miss[b] {
		return g.miss[a] && g.miss[b]
	}
	return deltaE76(g.col[a], g.col[b]) <= g.contrast
}

// enforceMinSize repeatedly merges any component that admits no disk of
// radius cellSize/2 (a "deep" node — one whose neighbourhood within that
// radius is entirely its own label) into the neighbour it shares the
// most 4-adjacency with. A node near coverTarget's own boundary stays
// eligible to be deep: only differently-labelled inside nodes
// disqualify it, never the region edge. Iterates because absorbing a
// thin sliver can leave the merged target still needing a re-check (it
// won't shrink, but the bookkeeping is simplest as a fixpoint).
func (g *colorGrid) enforceMinSize(cellSize float32) {
	radius := cellSize * 0.5
	rCells := int(radius/g.pitch + 0.999)
	if rCells < 1 {
		rCells = 1
	}
	r2 := radius * radius

	// frozen holds sub-cell components that have no neighbour to merge
	// into (a single-colour island smaller than a cell, filling its own
	// disconnected coverTarget piece). They keep their label rather than
	// being dropped: dropping would leave that piece of coverTarget with
	// no region and hence no cells — an uncovered hole in the printed
	// shell, violating the disjoint-union==coverTarget invariant. One
	// undersized cell there beats a hole. Frozen labels are excluded from
	// future victim selection so the loop terminates.
	//
	// No iteration cap: each pass either merges a victim (total label
	// count −1) or freezes one (a label is frozen at most once), so the
	// fixpoint is reached in O(initial label count) passes.
	frozen := make(map[int32]bool)
	for {
		skip := g.deepLabels(rCells, r2)
		for lab := range frozen {
			skip[lab] = true
		}
		// Pick the smallest non-deep, non-frozen component and merge it
		// first (smallest features are the surest sub-cell noise).
		victim, target := g.pickMergeVictim(skip)
		if victim < 0 {
			break // every surviving component is deep or frozen
		}
		if target < 0 {
			frozen[victim] = true
			continue
		}
		g.relabel(victim, target)
	}
}

// deepLabels returns the set of labels that have at least one deep node.
func (g *colorGrid) deepLabels(rCells int, r2 float32) map[int32]bool {
	deep := make(map[int32]bool)
	for idx := range g.inside {
		if !g.inside[idx] {
			continue
		}
		lab := g.label[idx]
		if lab < 0 || deep[lab] {
			continue // already known deep
		}
		if g.isDeep(idx, rCells, r2) {
			deep[lab] = true
		}
	}
	return deep
}

func (g *colorGrid) isDeep(idx int, rCells int, r2 float32) bool {
	lab := g.label[idx]
	r := idx / g.cols
	c := idx % g.cols
	for dr := -rCells; dr <= rCells; dr++ {
		nr := r + dr
		if nr < 0 || nr >= g.rows {
			continue
		}
		for dc := -rCells; dc <= rCells; dc++ {
			nc := c + dc
			if nc < 0 || nc >= g.cols {
				continue
			}
			dx := float32(dc) * g.pitch
			dy := float32(dr) * g.pitch
			if dx*dx+dy*dy > r2 {
				continue
			}
			nidx := nr*g.cols + nc
			// Only a differently-labelled inside node breaks depth.
			// Outside-coverTarget cells never disqualify, so a region can
			// be deep right up against the silhouette edge.
			if g.inside[nidx] && g.label[nidx] != lab {
				return false
			}
		}
	}
	return true
}

// pickMergeVictim returns the smallest labelled component not in skip and
// the neighbour label it should merge into (the perceptually closest one,
// i.e. smallest CIE76 ΔE to the victim's mean colour). target is -1 when
// the victim has no labelled neighbour. victim is -1 when every component
// is in skip.
func (g *colorGrid) pickMergeVictim(skip map[int32]bool) (victim, target int32) {
	area := make(map[int32]int)
	for idx := range g.inside {
		if g.inside[idx] && g.label[idx] >= 0 {
			area[g.label[idx]]++
		}
	}
	victim = -1
	bestArea := int(^uint(0) >> 1)
	for lab, a := range area {
		if skip[lab] {
			continue
		}
		// Smallest area wins; ties broken by smallest label id so the
		// choice is independent of Go's randomized map iteration order.
		// Without the tie-break the merge sequence (and thus the final
		// cell count) varies run to run, which desyncs the voxelize /
		// palette caches and panics the dither stage on a partial bust.
		if a < bestArea || (a == bestArea && lab < victim) {
			bestArea = a
			victim = lab
		}
	}
	if victim < 0 {
		return -1, -1
	}
	// Count shared adjacency with each neighbouring label.
	adj := make(map[int32]int)
	for idx := range g.inside {
		if !g.inside[idx] || g.label[idx] != victim {
			continue
		}
		r := idx / g.cols
		c := idx % g.cols
		for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			nc, nr := c+d[0], r+d[1]
			if nc < 0 || nc >= g.cols || nr < 0 || nr >= g.rows {
				continue
			}
			nidx := nr*g.cols + nc
			nl := g.label[nidx]
			if g.inside[nidx] && nl >= 0 && nl != victim {
				adj[nl]++
			}
		}
	}
	// Choose the merge target by perceptual similarity: absorb the victim
	// into the neighbour whose mean colour is closest (smallest CIE76 ΔE),
	// so the boundary that SURVIVES the merge is the highest-contrast one. A
	// thin dark strip between a white and a black region thus merges into
	// black — leaving the crisp white↔dark cut — rather than into whichever
	// side it happens to touch most (which could leave the survivor at the
	// low-contrast dark↔black edge). ΔE is deterministic, so the merge
	// sequence and final cell count stay reproducible (a hard requirement:
	// non-determinism desyncs the voxelize/palette caches and panics dither).
	//
	// Ties on colour fall back to the old geometry order: most shared
	// 4-adjacency, then smaller area, then smaller label. Preferring the
	// SMALLER neighbour on a tie keeps victims attaching to nascent regions
	// rather than feeding a runaway blob, nucleating multiple bands. A
	// colour-unknown neighbour (all-miss, no surface) or an all-miss victim
	// gets ΔE = +Inf, so it sorts last and the geometry order decides.
	mean := g.labelMeanColors()
	vCol, vHas := mean[victim]
	target = -1
	var bestDE float64
	var bestAdj int
	for nl, n := range adj {
		dE := math.MaxFloat64
		if nCol, ok := mean[nl]; ok && vHas {
			dE = deltaE76(vCol, nCol)
		}
		better := false
		switch {
		case target < 0:
			better = true
		case dE != bestDE:
			better = dE < bestDE
		case n != bestAdj:
			better = n > bestAdj
		case area[nl] != area[target]:
			better = area[nl] < area[target]
		default:
			better = nl < target
		}
		if better {
			bestDE, bestAdj, target = dE, n, nl
		}
	}
	return victim, target
}

// labelMeanColors returns the mean sampled colour of each label over its
// non-miss nodes. Labels that are entirely miss (no surface hit anywhere)
// have no entry — callers treat them as colour-unknown.
func (g *colorGrid) labelMeanColors() map[int32][3]uint8 {
	sum := make(map[int32][3]uint64)
	cnt := make(map[int32]int)
	for idx := range g.inside {
		if !g.inside[idx] || g.miss[idx] {
			continue
		}
		lab := g.label[idx]
		if lab < 0 {
			continue
		}
		c := g.col[idx]
		s := sum[lab]
		s[0] += uint64(c[0])
		s[1] += uint64(c[1])
		s[2] += uint64(c[2])
		sum[lab] = s
		cnt[lab]++
	}
	mean := make(map[int32][3]uint8, len(cnt))
	for lab, n := range cnt {
		s := sum[lab]
		mean[lab] = [3]uint8{uint8(s[0] / uint64(n)), uint8(s[1] / uint64(n)), uint8(s[2] / uint64(n))}
	}
	return mean
}

func (g *colorGrid) relabel(from, to int32) {
	for idx := range g.label {
		if g.inside[idx] && g.label[idx] == from {
			g.label[idx] = to
		}
	}
}

// buildRegionFootprints turns each surviving label into a Footprint:
// the union of its nodes' pitch×pitch squares (assembled as maximal
// per-row run rectangles to keep the Clipper union cheap), intersected
// with coverTarget so the region's outer edge is the true silhouette,
// not the grid staircase. Region order is by label id (deterministic).
//
// Boundary nodes additionally extend their square outward by half a pitch
// on any side facing OUTSIDE coverTarget. The bare node±half squares fall
// up to ~half a pitch short of the silhouette on the max-X and max-Y edges
// (the outermost in-region node sits below maxX/maxY and the node beyond it
// is culled for being outside coverTarget), so the grid-square union — and
// hence the region footprint after the cover intersect — under-reaches the
// true wall there. A boundary cell on a vertical wall then ends ~10µm inside
// the alpha-wrapped surface, the 5µm open-edge clip bloat can't bridge the
// gap, and the per-cell clip prism misses the wall entirely → holes on the
// max walls. Extending outward (then clamping with the cover intersect)
// makes the union reach the silhouette on all four sides, matching the plain
// partition. Convex corners (both perpendicular neighbours outside) also get
// a diagonal-quadrant rect the axis extensions miss, so corner points are
// covered too.
//
// The extension only fires where the neighbour is OUTSIDE coverTarget, never
// across a colour cut (both sides inside). It is exactly half a pitch — the
// most a node's square can fall short of the silhouette — so the outward
// reach is one full pitch (node+half + half) and an extension can never reach
// a different-label node's square, which starts 1.5 pitch away across a
// one-cell gap. Only a sub-pitch OUTSIDE neck sitting on a colour cut (a
// feature the cellSize/4 grid cannot resolve in the first place) could leave
// two extensions grazing in the off-cover sliver between them; the residual
// is sub-pitch, the same scale as Clipper's coincident-edge tie-breaks.
func (g *colorGrid) buildRegionFootprints(coverTarget *Footprint) []*Footprint {
	half := g.pitch * 0.5
	ext := half
	rectsByLabel := make(map[int32]clipper.Paths)
	for r := 0; r < g.rows; r++ {
		c := 0
		for c < g.cols {
			idx := r*g.cols + c
			lab := int32(-1)
			if g.inside[idx] {
				lab = g.label[idx]
			}
			if lab < 0 {
				c++
				continue
			}
			c0 := c
			for c+1 < g.cols {
				nidx := r*g.cols + (c + 1)
				if !g.inside[nidx] || g.label[nidx] != lab {
					break
				}
				c++
			}
			x0 := g.minX + float32(c0)*g.pitch - half
			x1 := g.minX + float32(c)*g.pitch + half
			y := g.minY + float32(r)*g.pitch
			y0 := y - half
			y1 := y + half
			rect := []Point2{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}
			rectsByLabel[lab] = append(rectsByLabel[lab], pointsToClipperPath(rect))
			c++
		}
	}

	// outside reports whether grid cell (nr,nc) lies outside coverTarget
	// (or off the grid entirely) — i.e. a silhouette boundary, not a
	// colour cut between two in-region nodes.
	outside := func(nr, nc int) bool {
		if nr < 0 || nr >= g.rows || nc < 0 || nc >= g.cols {
			return true
		}
		return !g.inside[nr*g.cols+nc]
	}
	for r := 0; r < g.rows; r++ {
		for c := 0; c < g.cols; c++ {
			idx := r*g.cols + c
			if !g.inside[idx] {
				continue
			}
			lab := g.label[idx]
			if lab < 0 {
				continue
			}
			nx := g.minX + float32(c)*g.pitch
			ny := g.minY + float32(r)*g.pitch
			addRect := func(x0, y0, x1, y1 float32) {
				rect := []Point2{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}
				rectsByLabel[lab] = append(rectsByLabel[lab], pointsToClipperPath(rect))
			}
			left := outside(r, c-1)
			right := outside(r, c+1)
			down := outside(r-1, c)
			up := outside(r+1, c)
			if left { // extend left, past the silhouette
				addRect(nx-half-ext, ny-half, nx-half, ny+half)
			}
			if right { // extend right
				addRect(nx+half, ny-half, nx+half+ext, ny+half)
			}
			if down { // extend down
				addRect(nx-half, ny-half-ext, nx+half, ny-half)
			}
			if up { // extend up
				addRect(nx-half, ny+half, nx+half, ny+half+ext)
			}
			// Convex corners: the axis extensions leave the outer diagonal
			// quadrant uncovered, so the silhouette corner falls short. Fill
			// it only at a genuine convex corner — both perpendicular
			// neighbours AND the diagonal neighbour face outside. At a
			// concave corner (e.g. the bottom of a thin slot) the diagonal
			// neighbour is the region across the slot; extending there would
			// poke a half-pitch square into it.
			if left && down && outside(r-1, c-1) {
				addRect(nx-half-ext, ny-half-ext, nx-half, ny-half)
			}
			if left && up && outside(r+1, c-1) {
				addRect(nx-half-ext, ny+half, nx-half, ny+half+ext)
			}
			if right && down && outside(r-1, c+1) {
				addRect(nx+half, ny-half-ext, nx+half+ext, ny-half)
			}
			if right && up && outside(r+1, c+1) {
				addRect(nx+half, ny+half, nx+half+ext, ny+half+ext)
			}
		}
	}

	labels := make([]int32, 0, len(rectsByLabel))
	for lab := range rectsByLabel {
		labels = append(labels, lab)
	}
	// Deterministic order: ascending label id.
	for i := 1; i < len(labels); i++ {
		for j := i; j > 0 && labels[j] < labels[j-1]; j-- {
			labels[j], labels[j-1] = labels[j-1], labels[j]
		}
	}

	var out []*Footprint
	for _, lab := range labels {
		fp := unionRectsToFootprint(rectsByLabel[lab])
		if fp == nil || len(fp.Loops) == 0 {
			continue
		}
		clipped := FootprintIntersect(fp, coverTarget)
		if clipped != nil && len(clipped.Loops) > 0 {
			out = append(out, clipped)
		}
	}
	return out
}

// unionRectsToFootprint non-zero-unions a set of axis-aligned rectangle
// paths into a Footprint (mirrors ComputeFootprint's polytree walk).
func unionRectsToFootprint(rects clipper.Paths) *Footprint {
	if len(rects) == 0 {
		return &Footprint{}
	}
	c := clipper.NewClipper(clipper.IoNone)
	c.AddPaths(rects, clipper.PtSubject, true)
	tree, ok := c.Execute2(clipper.CtUnion, clipper.PftNonZero, clipper.PftNonZero)
	if !ok || tree == nil {
		return &Footprint{}
	}
	fp := &Footprint{}
	for _, child := range tree.Childs() {
		collectFootprintLoops(child, fp)
	}
	return fp
}
