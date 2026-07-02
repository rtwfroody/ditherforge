package cellslicer

import (
	"container/heap"
	"math"
	"sort"

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
// in segmentation, before any footprint is cut. It is enforced in two
// steps: enforceMinSize merges away whole regions that admit no
// cellSize disk anywhere, then reassignShallowNodes cedes sub-cell
// tendrils of the survivors (thin protrusions a whole-region merge
// can't see) to the neighbouring region that can tile them.

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
//   - every returned region admits a disk of diameter ~cellSize, and no
//     region keeps a sub-cell tendril of its own colour dangling between
//     other regions or the silhouette — whole sub-cell features merge
//     into a neighbour (enforceMinSize) and sub-cell protrusions of
//     surviving regions are ceded to the nearest neighbouring region,
//     where they thicken a boundary instead of tiling into slivers
//     (reassignShallowNodes). Exceptions, both bounded: isolated
//     sub-cell coverTarget islands are kept undersized rather than
//     dropped (a hole is worse), and a thin spike of coverTarget itself
//     stays with its parent region (no segmentation can widen the
//     footprint — the plain partition tiles it the same way);
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
	g.reassignShallowNodes(cellSize)
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

	// Incremental bookkeeping so a pass is no longer a full-grid rescan
	// (the old version rebuilt the deep set, the area map, and rewrote the
	// whole label array every merge — quadratic on a noisy surface with
	// thousands of initial labels over a 10⁵–10⁶ node grid).
	//
	//   - s (mergeState) holds each label's node list, area, and colour
	//     accumulator; a merge touches only the victim's nodes and folds
	//     its area/colour into the target in O(victim) instead of O(grid).
	//
	//   - deep is MONOTONE: merging can only make a label deep, never
	//     un-deep — a deep node's disk holds only own-label and outside
	//     nodes, and relabelling never injects a foreign label into it — so
	//     it is carried across passes. Only the merge target's nodes near
	//     the seam can newly qualify, so each pass re-examines just those
	//     rather than rescanning the whole grid with a per-node disk scan.
	//
	//   - skip = deep ∪ frozen, grow-only. frozen holds sub-cell islands
	//     with no neighbour to merge into (a single-colour island smaller
	//     than a cell, filling its own disconnected coverTarget piece).
	//     They keep their label rather than being dropped: dropping would
	//     leave that piece of coverTarget with no region and hence no cells
	//     — an uncovered hole in the printed shell, violating the
	//     disjoint-union==coverTarget invariant. One undersized cell there
	//     beats a hole. Frozen labels are excluded from future victim
	//     selection so the loop terminates.
	//
	// No iteration cap: each pass either merges a victim (label count −1) or
	// freezes one (a label is frozen at most once), so the fixpoint is
	// reached in O(initial label count) passes.
	s := g.newMergeState()
	deep := g.deepLabels(rCells, r2)
	skip := make(map[int32]bool, len(deep))
	for lab := range deep {
		skip[lab] = true
	}

	// Victim selection is a min-heap keyed by (area, label) rather than a
	// per-pass linear scan of every surviving label — that scan, repeated
	// once per merge, was itself O(labels²) and kept the whole routine
	// quadratic even after the grid rescans were removed. Entries are never
	// removed on merge; instead a pop is validated against the live area map
	// (a merged-away or resized label yields a stale entry, discarded), and
	// the target's grown area is re-pushed. Total pushes ≤ initial labels +
	// merges, so the loop is O(labels·log labels). The heap orders by (area
	// asc, label asc) — identical to the old scan's tie-break — so the merge
	// sequence, and thus the final cell count, is unchanged and cache-stable.
	h := make(areaHeap, 0, len(s.area))
	for lab, a := range s.area {
		h = append(h, areaItem{area: a, label: lab})
	}
	heap.Init(&h)

	for {
		// Pop the smallest non-deep, non-frozen component (smallest features
		// are the surest sub-cell noise), skipping stale/skipped entries.
		victim := int32(-1)
		for h.Len() > 0 {
			it := heap.Pop(&h).(areaItem)
			if skip[it.label] {
				continue
			}
			if a, ok := s.area[it.label]; !ok || a != it.area {
				continue // stale: label merged away or resized since pushed
			}
			victim = it.label
			break
		}
		if victim < 0 {
			break // every surviving component is deep or frozen
		}
		target := g.mergeTarget(victim, s)
		if target < 0 {
			skip[victim] = true // freeze: no neighbour to merge into
			continue
		}
		vnodes := s.merge(g, victim, target)
		heap.Push(&h, areaItem{area: s.area[target], label: target})
		// Deep is monotone and only the target can newly qualify, so update
		// it in place instead of recomputing deepLabels over the whole grid.
		if !deep[target] && g.targetBecameDeep(vnodes, target, rCells, r2) {
			deep[target] = true
			skip[target] = true
		}
	}
}

// areaItem is one heap entry: a label and the area it had when pushed.
// enforceMinSize re-pushes a label whenever its area grows, so several
// entries for one label may coexist; the live area map disambiguates the
// current one from stale leftovers at pop time.
type areaItem struct {
	area  int
	label int32
}

// areaHeap is a min-heap over areaItem ordered by (area asc, label asc) —
// the same total order the old linear victim scan used, so the smallest
// label wins ties deterministically (cache-stable merge sequence).
type areaHeap []areaItem

func (h areaHeap) Len() int { return len(h) }
func (h areaHeap) Less(i, j int) bool {
	if h[i].area != h[j].area {
		return h[i].area < h[j].area
	}
	return h[i].label < h[j].label
}
func (h areaHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *areaHeap) Push(x any)   { *h = append(*h, x.(areaItem)) }
func (h *areaHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
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

// mergeTarget returns the neighbour label victim should merge into: the
// perceptually closest one (smallest CIE76 ΔE to victim's mean colour), or
// -1 when victim has no labelled neighbour. Only victim's own nodes are
// scanned, so the cost is O(victim), not O(grid).
func (g *colorGrid) mergeTarget(victim int32, s *mergeState) int32 {
	// Count shared adjacency with each neighbouring label by scanning only
	// the victim's own nodes (the smallest component), not the whole grid.
	adj := make(map[int32]int)
	for _, idx := range s.nodes[victim] {
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
	vCol, vHas := s.mean(victim)
	target := int32(-1)
	var bestDE float64
	var bestAdj int
	for nl, n := range adj {
		dE := math.MaxFloat64
		if nCol, ok := s.mean(nl); ok && vHas {
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
		case s.area[nl] != s.area[target]:
			better = s.area[nl] < s.area[target]
		default:
			better = nl < target
		}
		if better {
			bestDE, bestAdj, target = dE, n, nl
		}
	}
	return target
}

// mergeState is the incrementally-maintained bookkeeping enforceMinSize
// carries across passes so no operation rescans the whole grid: per-label
// inside-node lists (a merge rewrites only the victim's nodes), areas, and
// a colour accumulator (sum/cnt) yielding each label's mean colour.
type mergeState struct {
	nodes map[int32][]int     // inside node indices per label
	area  map[int32]int       // == len(nodes[lab])
	sum   map[int32][3]uint64 // Σ colour over the label's non-miss nodes
	cnt   map[int32]int       // # non-miss nodes (mean = sum/cnt)
}

func (g *colorGrid) newMergeState() *mergeState {
	s := &mergeState{
		nodes: make(map[int32][]int),
		area:  make(map[int32]int),
		sum:   make(map[int32][3]uint64),
		cnt:   make(map[int32]int),
	}
	for idx := range g.inside {
		if !g.inside[idx] {
			continue
		}
		lab := g.label[idx]
		if lab < 0 {
			continue
		}
		s.nodes[lab] = append(s.nodes[lab], idx)
		s.area[lab]++
		if !g.miss[idx] {
			c := g.col[idx]
			su := s.sum[lab]
			su[0] += uint64(c[0])
			su[1] += uint64(c[1])
			su[2] += uint64(c[2])
			s.sum[lab] = su
			s.cnt[lab]++
		}
	}
	return s
}

// mean returns the mean sampled colour of a label over its non-miss nodes,
// and false when the label is entirely miss (no surface hit anywhere) —
// callers treat that as colour-unknown.
func (s *mergeState) mean(lab int32) ([3]uint8, bool) {
	n := s.cnt[lab]
	if n == 0 {
		return [3]uint8{}, false
	}
	su := s.sum[lab]
	return [3]uint8{uint8(su[0] / uint64(n)), uint8(su[1] / uint64(n)), uint8(su[2] / uint64(n))}, true
}

// merge relabels every victim node to target on the grid and folds the
// victim's area/colour bookkeeping into target, all in O(victim) rather
// than a full-grid rewrite. It returns the relabelled node indices so the
// caller can re-test target deepness locally. victim is dropped from state.
func (s *mergeState) merge(g *colorGrid, victim, target int32) []int {
	vnodes := s.nodes[victim]
	for _, idx := range vnodes {
		g.label[idx] = target
	}
	s.nodes[target] = append(s.nodes[target], vnodes...)
	s.area[target] += s.area[victim]
	st, sv := s.sum[target], s.sum[victim]
	st[0] += sv[0]
	st[1] += sv[1]
	st[2] += sv[2]
	s.sum[target] = st
	s.cnt[target] += s.cnt[victim]
	delete(s.nodes, victim)
	delete(s.area, victim)
	delete(s.sum, victim)
	delete(s.cnt, victim)
	return vnodes
}

// targetBecameDeep reports whether target now admits a deep node as a
// result of just absorbing the given (already-relabelled) victim nodes. A
// node's deepness depends only on labels within rCells of it, so the only
// nodes whose deepness can have changed are target nodes within that radius
// of a relabelled node — this checks exactly that candidate set (a
// Chebyshev-box superset of the disk; extra candidates are harmless because
// isDeep is ground truth). Short-circuits on the first deep node found.
func (g *colorGrid) targetBecameDeep(vnodes []int, target int32, rCells int, r2 float32) bool {
	seen := make(map[int]bool)
	for _, v := range vnodes {
		vr := v / g.cols
		vc := v % g.cols
		for dr := -rCells; dr <= rCells; dr++ {
			nr := vr + dr
			if nr < 0 || nr >= g.rows {
				continue
			}
			for dc := -rCells; dc <= rCells; dc++ {
				nc := vc + dc
				if nc < 0 || nc >= g.cols {
					continue
				}
				nidx := nr*g.cols + nc
				if seen[nidx] || !g.inside[nidx] || g.label[nidx] != target {
					continue
				}
				seen[nidx] = true
				if g.isDeep(nidx, rCells, r2) {
					return true
				}
			}
		}
	}
	return false
}

// reassignShallowNodes cedes sub-cell tendrils to a region that can tile
// them. enforceMinSize guarantees only that every surviving label admits
// a cellSize disk SOMEWHERE — a label shaped like a fat blob with a thin
// strip running along a colour edge or the silhouette is "deep" and
// survives whole, and the strip then tiles into sub-cellSize sliver
// cells via ringSeeds' thin-feature fallback.
//
// The pass keeps each label's core — the nodes within the min-size disk
// radius of a deep node of their own label, i.e. the label's
// morphological opening by that disk — and reassigns every remaining
// shallow node to the geodesically nearest core (multi-source BFS,
// 4-connected through inside nodes). The cases fall out without special
// handling:
//
//   - a tendril running alongside another region flows into that
//     neighbour: its nodes are > radius from their own core by
//     definition of shallow, but only a node or two from the
//     neighbour's. The receiver only gets FATTER there (it absorbs area
//     adjacent to its body), so no new sliver is created;
//   - a thin silhouette spike (outside on both sides) stays with its
//     parent — the parent core is the only one reachable. The footprint
//     itself is sub-cell there, which no segmentation can fix; the
//     plain partition tiles it the same way;
//   - frozen islands (no deep node in their component) are unreachable
//     and keep their label, so no node is ever dropped and the regions'
//     disjoint-union==coverTarget invariant holds.
//
// On a straight cut every node is already core (deep nodes sit just
// inside the boundary and their disks reach back to it), so the common
// no-tendril case exits after one linear scan.
//
// Determinism (cache-critical: a nondeterministic partition desyncs the
// voxelize/palette caches and panics dither): BFS claims are resolved
// per distance layer with the smallest claiming label winning, and each
// layer is applied in sorted node order, so the result is independent
// of map iteration order.
func (g *colorGrid) reassignShallowNodes(cellSize float32) {
	// With fewer than two surviving labels there is no other basin for a
	// tendril to flow into.
	firstLab := int32(-1)
	multi := false
	for idx, lab := range g.label {
		if !g.inside[idx] || lab < 0 {
			continue
		}
		if firstLab < 0 {
			firstLab = lab
		} else if lab != firstLab {
			multi = true
			break
		}
	}
	if !multi {
		return
	}

	radius := cellSize * 0.5
	rCells := int(radius/g.pitch + 0.999)
	if rCells < 1 {
		rCells = 1
	}
	r2 := radius * radius

	// Core = the deep nodes dilated by the same disk isDeep uses. A deep
	// node's disk holds only own-label inside nodes (that is the
	// definition), so marking every inside node in it stays within the
	// label.
	core := make([]bool, len(g.label))
	for idx := range g.label {
		if !g.inside[idx] || g.label[idx] < 0 || !g.isDeep(idx, rCells, r2) {
			continue
		}
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
				if g.inside[nidx] {
					core[nidx] = true
				}
			}
		}
	}

	anyShallow := false
	for idx := range g.label {
		if g.inside[idx] && g.label[idx] >= 0 && !core[idx] {
			anyShallow = true
			break
		}
	}
	if !anyShallow {
		return
	}

	// Multi-source BFS from the core boundary. assigned doubles as the
	// visited set; core nodes are terminal sources and never reassigned.
	assigned := core
	frontier := make([]int, 0, 1024)
	for idx := range g.label {
		if !core[idx] {
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
			if g.inside[nidx] && !core[nidx] {
				frontier = append(frontier, idx)
				break
			}
		}
	}
	for len(frontier) > 0 {
		claims := make(map[int]int32)
		for _, idx := range frontier {
			lab := g.label[idx]
			r := idx / g.cols
			c := idx % g.cols
			for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nc, nr := c+d[0], r+d[1]
				if nc < 0 || nc >= g.cols || nr < 0 || nr >= g.rows {
					continue
				}
				nidx := nr*g.cols + nc
				if !g.inside[nidx] || assigned[nidx] {
					continue
				}
				if cur, ok := claims[nidx]; !ok || lab < cur {
					claims[nidx] = lab
				}
			}
		}
		if len(claims) == 0 {
			break
		}
		next := make([]int, 0, len(claims))
		for nidx := range claims {
			next = append(next, nidx)
		}
		sort.Ints(next)
		for _, nidx := range next {
			g.label[nidx] = claims[nidx]
			assigned[nidx] = true
		}
		frontier = next
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
