package cellslicer

import (
	"fmt"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/manifoldbool"
)

// mergeGroup is one set of cells within a single slab that share a kind
// and a color and are clipped together as one merged prism.
type mergeGroup struct {
	slabIdx int
	// repGlobal is the group's representative global cell index — the
	// smallest global index among its members. Faces produced by the
	// group are tagged with this in ClipResult.FaceCellIdx, so a
	// downstream cellIdx→color lookup keyed on the representative still
	// yields the group's color.
	repGlobal int
	// cellIdxs are the member cells' indices within slabs[slabIdx].Cells.
	cellIdxs []int
}

// groupConnectedSameColorCells partitions each slab's cells into merge
// groups of CONNECTED, same-kind, same-color cells. Two cells are
// connected when they share a directed half-edge in reverse (the exact
// int2D bucket test MarkOuterEdges uses), so only spatially adjacent
// cells merge. cellColor is indexed by global cell index (the flattened
// CellSlabs order, matching ClipMeshToCellsManifold's FaceCellIdx space)
// and must have one entry per cell.
//
// Connectivity (not just same-color-anywhere-in-slab) matters for two
// reasons. It is what removes the internal seam between neighbours. And
// it keeps each group a single tiled region, so its boundary comes out
// clean from mergedGroupContours' half-edge cancellation and its Manifold
// intersection stays localized to one blob — grouping all of a slab's
// scattered same-color speckle into one prism would instead hand Manifold
// a slab-spanning shape with thousands of disjoint pieces. Non-adjacent
// same-color cells stay in separate groups and clip independently,
// exactly as the per-cell path would. A missed adjacency (T-junction
// where endpoints don't bucket-match) only under-merges — never a false
// merge.
//
// The first (smallest-index) cell in each component is its
// representative; faces from the group are tagged with it.
func groupConnectedSameColorCells(slabs []Slab, cellColor []int32) []mergeGroup {
	var groups []mergeGroup
	globalBase := 0
	for si := range slabs {
		cells := slabs[si].Cells
		n := len(cells)

		// Union-find over local cell indices.
		parent := make([]int, n)
		for i := range parent {
			parent[i] = i
		}
		find := func(x int) int {
			for parent[x] != x {
				parent[x] = parent[parent[x]] // path halving
				x = parent[x]
			}
			return x
		}
		union := func(a, b int) {
			ra, rb := find(a), find(b)
			if ra != rb {
				parent[ra] = rb
			}
		}

		// Map each directed half-edge to the cell that owns it, then
		// union a cell with the owner of any reverse half-edge that
		// matches in kind and color.
		owner := make(map[dirEdge]int, n*4)
		for ci := range cells {
			outer := cells[ci].Outer
			m := len(outer)
			for k := 0; k < m; k++ {
				owner[dirEdgeOf(outer[k], outer[(k+1)%m])] = ci
			}
		}
		for ci := range cells {
			outer := cells[ci].Outer
			m := len(outer)
			for k := 0; k < m; k++ {
				cj, ok := owner[dirEdgeOf(outer[k], outer[(k+1)%m]).reverse()]
				if !ok || cj == ci {
					continue
				}
				if cells[cj].Kind != cells[ci].Kind {
					continue
				}
				if cellColor[globalBase+cj] != cellColor[globalBase+ci] {
					continue
				}
				union(ci, cj)
			}
		}

		// Gather components. Iterating ci ascending makes the first cell
		// seen for a root its smallest-index member → the representative.
		rootGroup := make(map[int]int, n)
		for ci := 0; ci < n; ci++ {
			r := find(ci)
			if gIdx, ok := rootGroup[r]; ok {
				groups[gIdx].cellIdxs = append(groups[gIdx].cellIdxs, ci)
			} else {
				rootGroup[r] = len(groups)
				groups = append(groups, mergeGroup{
					slabIdx:   si,
					repGlobal: globalBase + ci,
					cellIdxs:  []int{ci},
				})
			}
		}
		globalBase += n
	}
	return groups
}

// ClipMeshToMergedCellsManifold is the merged-cell counterpart of
// ClipMeshToCellsManifold. It groups connected, same-kind, same-color
// cells within each slab (see groupConnectedSameColorCells) and clips
// the model against each group's merged prism in a single Manifold
// intersection, rather than one intersection per cell. cellColor supplies
// each cell's color (any int32 label whose equality defines "same color";
// -1 is an ordinary label — adjacent same-kind -1 cells merge together
// like any other shared color). The result's FaceCellIdx tags each face
// with its group's representative global cell index, and CellRep maps
// every cell to that representative so per-cell coverage diagnostics still
// work.
//
// Output coverage matches the per-cell clip in the model interior; along
// OPEN (footprint-boundary) edges the silhouette can differ slightly,
// because bloatOpenEdges caps displacement at the bbox max-side of the
// MERGED loop here vs. each individual cell's bbox in the per-cell path.
// The difference is otherwise fewer, larger boolean ops and fewer
// internal seams between same-color cells. cellSize matches the value
// passed to PartitionModel (scales the open-edge bloat).
func ClipMeshToMergedCellsManifold(model *loader.LoadedModel, slabs []Slab, cellSize float32, cellColor []int32) (ClipResult, error) {
	nCells := 0
	for si := range slabs {
		nCells += len(slabs[si].Cells)
	}
	if len(cellColor) != nCells {
		return ClipResult{}, fmt.Errorf("cellslicer/manifold: cellColor has %d entries, want %d (one per cell)", len(cellColor), nCells)
	}

	ss, err := buildSlabSrc(model, slabs)
	if err != nil {
		return ClipResult{}, err
	}
	defer ss.close()

	groups := groupConnectedSameColorCells(slabs, cellColor)

	// One job per merged group; faces are tagged with the group's
	// representative global cell index.
	cr, err := runClipJobs(len(groups), func(i int) (int, [][3]float32, [][3]uint32, error) {
		g := &groups[i]
		s := &slabs[g.slabIdx]
		v, f, cerr := clipOneGroupManifold(ss.slabManifold(g.slabIdx), ss.srcID, s.Cells, g.cellIdxs, s.ZBot, s.ZTop, cellSize)
		if cerr != nil {
			return 0, nil, nil, fmt.Errorf("group %d (slab=%d, rep=%d): %w", i, g.slabIdx, g.repGlobal, cerr)
		}
		return g.repGlobal, v, f, nil
	})
	if err != nil {
		return ClipResult{}, err
	}

	// Map every cell to its group representative so per-cell coverage
	// diagnostics still work (only the representative appears in
	// FaceCellIdx). Default each cell to itself, then overwrite members
	// with their group representative.
	cr.CellRep = make([]int32, nCells)
	for i := range cr.CellRep {
		cr.CellRep[i] = int32(i)
	}
	globalOffsets := SlabGlobalOffsets(slabs)
	for gi := range groups {
		g := &groups[gi]
		base := globalOffsets[g.slabIdx]
		for _, ci := range g.cellIdxs {
			cr.CellRep[base+ci] = int32(g.repGlobal)
		}
	}
	return cr, nil
}

// clipOneGroupManifold builds the merged prism for one group (its members'
// boundary contours from mergedGroupContours, extruded between zBot and
// zTop), intersects it with src, and returns the surface-only mesh (only
// faces inherited from src survive, via ToMeshFiltered(srcID)). Empty
// results (no member overlaps the model) are returned as (nil, nil, nil).
//
// If mergedGroupContours can't cleanly trace the group boundary (a pinch:
// the surviving boundary self-touches at a vertex), it falls back to
// clipping each member cell individually and concatenating the results —
// the same surface coverage as the per-cell path, just without the merge
// (more triangles for this one group). The caller still tags every face
// with the group representative, so the color is unchanged either way.
func clipOneGroupManifold(src *manifoldbool.Manifold, srcID int32, cells []Cell, cellIdxs []int, zBot, zTop, cellSize float32) ([][3]float32, [][3]uint32, error) {
	contours, clean := mergedGroupContours(cells, cellIdxs, cellSize)
	if !clean {
		// Pinch fallback: clip members one at a time, like the per-cell
		// path. Coverage matches per-cell exactly (each member keeps its
		// own bbox-capped bloat); only the same-color internal seams the
		// merge would have removed come back, for this group alone.
		var allV [][3]float32
		var allF [][3]uint32
		for _, ci := range cellIdxs {
			cv, cf, cerr := clipOneCellManifold(src, srcID, &cells[ci], zBot, zTop, cellSize)
			if cerr != nil {
				return nil, nil, cerr
			}
			base := uint32(len(allV))
			allV = append(allV, cv...)
			for _, t := range cf {
				allF = append(allF, [3]uint32{t[0] + base, t[1] + base, t[2] + base})
			}
		}
		return allV, allF, nil
	}
	if len(contours) == 0 {
		return nil, nil, nil
	}
	prism, err := manifoldbool.ExtrudePolygons(contours, zBot, zTop)
	if err != nil {
		return nil, nil, fmt.Errorf("extrude merged group: %w", err)
	}
	defer prism.Close()
	out, err := manifoldbool.Intersection(src, prism)
	if err != nil {
		return nil, nil, fmt.Errorf("intersection: %w", err)
	}
	defer out.Close()
	if out.IsEmpty() {
		return nil, nil, nil
	}
	v, f := out.ToMeshFiltered(srcID)
	return v, f, nil
}

// mergedGroupContours builds the extrusion contours for one group by
// cancelling the shared interior half-edges of its member cells and
// tracing the surviving boundary into loops, then bloating each loop's
// open-edge runs. Outer loops come out CCW and hole loops CW (inherited
// from the cells' CCW winding), which is what ExtrudePolygons wants.
//
// This replaces a 2D Clipper union, which is O(output-polygons²) in
// go.clipper's hole-nesting pass and hangs on large near-uniform groups
// (e.g. a flat cap with hundreds of speckle islands). Cancellation is
// O(Σ|cell.Outer|): cells tile their region with no overlap and share
// exact int2D edges (the same bucket MarkOuterEdges relies on), so an
// interior edge a→b in one cell meets its reverse b→a in the neighbour
// and both drop; the survivors are exactly the region boundary, with
// holes traced as their own (oppositely wound) loops for free.
//
// Bloat is applied to the MERGED boundary, not per cell: a run of open
// (footprint-boundary) edges along the component edge is pushed out as
// one continuous miter, so adjacent cells' bloat can't overlap (the
// internal cells are already gone). Because bloatOpenEdges caps
// displacement at the loop's bbox max-side, the merged loop's larger
// bbox permits a larger open-edge bloat than the per-cell path — so
// open-edge silhouettes legitimately differ between the two clips
// (interior coverage is identical).
//
// The bool return is "clean": false when the surviving boundary
// self-touches at a vertex (a pinch — more than one outgoing boundary
// edge), which the single-successor trace below can't follow without
// dropping a sub-loop. The caller falls back to per-cell clipping in
// that case rather than emit a shrunken prism. On the clean path it is
// true (contours may still be nil if nothing usable).
func mergedGroupContours(cells []Cell, cellIdxs []int, cellSize float32) ([][][2]float32, bool) {
	// Pass 1: record every directed half-edge so we can detect which
	// have a reverse mate within the group (interior, cancels).
	present := make(map[dirEdge]struct{}, len(cellIdxs)*4)
	for _, ci := range cellIdxs {
		outer := cells[ci].Outer
		m := len(outer)
		for k := 0; k < m; k++ {
			present[dirEdgeOf(outer[k], outer[(k+1)%m])] = struct{}{}
		}
	}

	// Pass 2: surviving boundary edges → a next-vertex map carrying the
	// float point and the source cell's open flag. starts preserves
	// insertion order for deterministic loop tracing.
	type bedge struct {
		to   int2D
		open bool
	}
	next := make(map[int2D]bedge, len(present))
	ptAt := make(map[int2D]Point2, len(present))
	starts := make([]int2D, 0, len(present))
	for _, ci := range cellIdxs {
		outer := cells[ci].Outer
		flags := cells[ci].OuterEdgeOpen
		m := len(outer)
		for k := 0; k < m; k++ {
			a, b := outer[k], outer[(k+1)%m]
			ka, kb := int2DOf(a), int2DOf(b)
			if _, mate := present[dirEdge{kb, ka}]; mate {
				continue // interior edge, cancels
			}
			if _, dup := next[ka]; dup {
				// Two surviving edges leave this vertex: the boundary
				// self-touches (pinch). The single-successor trace can't
				// follow both sub-loops, so bail and let the caller fall
				// back to per-cell clipping. Detection is on surviving
				// edges only — interior edges already `continue`d above —
				// so ordinary boundary vertices (degree 1) never trip it.
				return nil, false
			}
			open := flags != nil && k < len(flags) && flags[k]
			next[ka] = bedge{to: kb, open: open}
			ptAt[ka] = a
			starts = append(starts, ka)
		}
	}
	if len(next) == 0 {
		return nil, true
	}

	// Pass 3: trace boundary loops, then bloat each loop's open runs.
	visited := make(map[int2D]bool, len(next))
	var contours [][][2]float32
	for _, s := range starts {
		if visited[s] {
			continue
		}
		var pts []Point2
		var flags []bool
		cur := s
		for !visited[cur] {
			visited[cur] = true
			e, ok := next[cur]
			if !ok {
				break // open chain (shouldn't happen for a closed region)
			}
			// Vertex at cur, with the open flag of the edge LEAVING it.
			pts = append(pts, ptAt[cur])
			flags = append(flags, e.open)
			cur = e.to
		}
		if len(pts) < 3 {
			continue
		}
		bloated := bloatOpenEdges(pts, flags, OpenEdgeBloat*cellSize)
		if len(bloated) >= 3 {
			contours = append(contours, bloated)
		}
	}
	if len(contours) == 0 {
		return nil, true
	}
	return contours, true
}
