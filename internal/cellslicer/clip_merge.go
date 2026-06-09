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

// maxMergeGroupCells caps how many cells a single merge group may contain.
// It is a safety backstop, not the primary complexity limiter — the real
// limit is the per-admission cleanliness check in growSameColorCellGroups
// (a candidate is only admitted if the merged footprint still traces as a
// single non-pinched contour). The cap keeps the incremental re-trace cheap
// and stops any one prism from growing slab-spanning, which is what the old
// union-find behaviour did before it punched holes in the output mesh.
//
// 32 is the measured knee: sweeping 4→8→16→32→64→128 on earth/glyphid/
// low_poly_building, output triangle count drops steeply through 32 then
// flattens (building −8.3% at 32 vs −8.6% at 64, −8.7% at 128; glyphid
// −9.5% at 32). At 32 the fraction of groups still pegged at the cap is
// tiny (building 227/61937, glyphid 58/23594, earth 0/46781), so almost no
// group wants to grow further. Clip wall-time is roughly flat across the
// sweep — bigger groups cost more per Manifold boolean, cancelling the win
// from fewer groups — so the benefit is triangle count, not speed. The
// area-equivalence test (TestCellMergeMatchesPerCell) confirms cap 32 keeps
// merged↔per-cell surface area within 3e-5 (no holes, no shape drift).
const maxMergeGroupCells = 32

// growSameColorCellGroups partitions each slab's cells into merge groups by
// greedy region growing, gated on footprint cleanliness. A member must be
// same-kind, same-color, and share a footprint edge with the group — one
// cell owns a directed half-edge whose reverse is owned by the other (the
// exact int2D bucket test MarkOuterEdges uses), so only spatially adjacent
// cells merge. cellColor is indexed by global cell index (the flattened
// CellSlabs order, matching ClipMeshToCellsManifold's FaceCellIdx space)
// and must have one entry per cell.
//
// Growing is greedy: cells are walked in ascending index; each unclaimed
// cell seeds a group, then edge-neighbours are admitted one at a time as
// long as (a) the group stays under maxMergeGroupCells and (b) the union of
// its members still traces as a single clean (non-pinched) contour, tested
// by running mergedGroupContours on the candidate group. A candidate that
// would pinch the boundary is skipped, not blacklisted — another neighbour
// may still be admissible — and a cell with no admissible neighbour stays
// in a group of one.
//
// Gating on cleanliness (rather than a fixed pair count) is what keeps the
// merged shapes easy to clip: the same pinch test that the clip path falls
// back on is moved earlier, to admission time, so pinched groups are never
// built in the first place and every clean merge that fits under the cap is
// kept. A missed adjacency only leaves a cell solo — never a false merge.
//
// The seed is always the group's representative: cells are walked ascending
// and only unclaimed cells are admitted, so every admitted member has a
// larger index than the seed. Faces from the group are tagged with the
// representative's global cell index.
func growSameColorCellGroups(slabs []Slab, cellColor []int32) []mergeGroup {
	var groups []mergeGroup
	globalBase := 0
	for si := range slabs {
		cells := slabs[si].Cells
		n := len(cells)

		// Map each directed half-edge to the cell that owns it, so a cell
		// can find the neighbour across any of its footprint edges.
		owner := make(map[dirEdge]int, n*4)
		for ci := range cells {
			outer := cells[ci].Outer
			m := len(outer)
			for k := 0; k < m; k++ {
				owner[dirEdgeOf(outer[k], outer[(k+1)%m])] = ci
			}
		}

		claimed := make([]bool, n)
		for ci := 0; ci < n; ci++ {
			if claimed[ci] {
				continue
			}
			claimed[ci] = true
			members := []int{ci}

			// Admit edge-neighbours one at a time while the merged
			// footprint stays clean and the group is under the cap. Each
			// admission can expose new neighbours, so rescan all members
			// after every accept (the inner loop breaks back to here).
			for len(members) < maxMergeGroupCells {
				cand := -1
			scan:
				for _, mi := range members {
					outer := cells[mi].Outer
					m := len(outer)
					for k := 0; k < m; k++ {
						cj, ok := owner[dirEdgeOf(outer[k], outer[(k+1)%m]).reverse()]
						if !ok || claimed[cj] {
							continue
						}
						if cells[cj].Kind != cells[ci].Kind {
							continue
						}
						if cellColor[globalBase+cj] != cellColor[globalBase+ci] {
							continue
						}
						// Admit only if the union stays a single clean
						// (non-pinched) contour.
						trial := append(append([]int(nil), members...), cj)
						if _, clean := mergedGroupContours(cells, trial); !clean {
							continue
						}
						cand = cj
						break scan
					}
				}
				if cand < 0 {
					break
				}
				claimed[cand] = true
				members = append(members, cand)
			}

			groups = append(groups, mergeGroup{
				slabIdx:   si,
				repGlobal: globalBase + ci,
				cellIdxs:  members,
			})
		}
		globalBase += n
	}
	return groups
}

// ClipMeshToMergedCellsManifold is the merged-cell counterpart of
// ClipMeshToCellsManifold. It groups adjacent same-kind, same-color cells
// within each slab (greedy region growing, capped and gated on footprint
// cleanliness; see growSameColorCellGroups) and clips the model against
// each group's merged prism in a single Manifold intersection, rather than
// one intersection per cell. cellColor supplies each cell's color (any
// int32 label whose equality defines "same color"; -1 is an ordinary label
// — adjacent same-kind -1 cells group like any other shared color). The
// result's FaceCellIdx tags each face with its group's representative
// global cell index, and CellRep maps every cell to that representative so
// per-cell coverage diagnostics still work.
//
// Output coverage matches the per-cell clip, including along OPEN
// (footprint-boundary) edges: both paths nudge open edges outward by the
// same fixed OpenEdgeBloatMM margin, so the merged silhouette lands in the
// same place as the per-cell one. The only difference from the per-cell
// clip is fewer, larger boolean ops and fewer internal seams between
// same-color cells.
func ClipMeshToMergedCellsManifold(model *loader.LoadedModel, slabs []Slab, cellColor []int32) (ClipResult, error) {
	return ClipMeshToMergedCellsManifoldProgress(model, slabs, cellColor, nil)
}

// ClipMeshToMergedCellsManifoldProgress is ClipMeshToMergedCellsManifold
// with optional progress reporting (prog may be nil).
func ClipMeshToMergedCellsManifoldProgress(model *loader.LoadedModel, slabs []Slab, cellColor []int32, prog *ClipProgress) (ClipResult, error) {
	nCells := 0
	for si := range slabs {
		nCells += len(slabs[si].Cells)
	}
	if len(cellColor) != nCells {
		return ClipResult{}, fmt.Errorf("cellslicer/manifold: cellColor has %d entries, want %d (one per cell)", len(cellColor), nCells)
	}

	ss, err := buildSlabSrc(model, slabs, prog.slabSplit())
	if err != nil {
		return ClipResult{}, err
	}
	defer ss.close()

	groups := growSameColorCellGroups(slabs, cellColor)

	// One job per merged group; faces are tagged with the group's
	// representative global cell index.
	cr, err := runClipJobs(len(groups), func(i int) (int, [][3]float32, [][3]uint32, error) {
		g := &groups[i]
		s := &slabs[g.slabIdx]
		v, f, cerr := clipOneGroupManifold(ss.slabManifold(g.slabIdx), ss.srcID, s.Cells, g.cellIdxs, s.ZBot, s.ZTop)
		if cerr != nil {
			return 0, nil, nil, fmt.Errorf("group %d (slab=%d, rep=%d): %w", i, g.slabIdx, g.repGlobal, cerr)
		}
		return g.repGlobal, v, f, nil
	}, prog.jobs())
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
func clipOneGroupManifold(src *manifoldbool.Manifold, srcID int32, cells []Cell, cellIdxs []int, zBot, zTop float32) ([][3]float32, [][3]uint32, error) {
	contours, clean := mergedGroupContours(cells, cellIdxs)
	if !clean {
		// Pinch fallback: clip members one at a time, like the per-cell
		// path. Coverage matches per-cell exactly (each member keeps its
		// own bbox-capped bloat); only the same-color internal seams the
		// merge would have removed come back, for this group alone.
		var allV [][3]float32
		var allF [][3]uint32
		for _, ci := range cellIdxs {
			cv, cf, cerr := clipOneCellManifold(src, srcID, &cells[ci], zBot, zTop)
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
// (footprint-boundary) edges along the pair's outer boundary is pushed out
// by the fixed OpenEdgeBloatMM margin as one continuous miter, so the
// paired cells' bloat can't overlap (the shared internal edge is already
// gone). Because the bloat distance is a fixed few-µm margin (not scaled
// by the polygon's size), the merged silhouette lands in exactly the same
// place as the per-cell clip's.
//
// The bool return is "clean": false when the surviving boundary
// self-touches at a vertex (a pinch — more than one outgoing boundary
// edge), which the single-successor trace below can't follow without
// dropping a sub-loop. The caller falls back to per-cell clipping in
// that case rather than emit a shrunken prism. On the clean path it is
// true (contours may still be nil if nothing usable).
func mergedGroupContours(cells []Cell, cellIdxs []int) ([][][2]float32, bool) {
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
		bloated := bloatOpenEdges(pts, flags, OpenEdgeBloatMM)
		if len(bloated) >= 3 {
			contours = append(contours, bloated)
		}
	}
	if len(contours) == 0 {
		return nil, true
	}
	return contours, true
}
