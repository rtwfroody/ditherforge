package cellslicer

import (
	"math"
	"sort"

	clipper "github.com/ctessum/go.clipper"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// BuildAdjacency returns the cell-to-cell adjacency graph across all
// slabs as a flat [][]voxel.Neighbor indexed by global cell index.
// Within a slab, adjacency comes from rasterizing the cell polygons
// at pxSize and scanning the resulting cellID grid for boundary
// transitions — imperfect on diagonal sliver pairs but cheap and
// good enough for dither error diffusion. Across slabs i ↔ i+1,
// adjacency is the set of cell pairs whose XY polygons have non-zero
// overlap area, found via an X-axis interval index over slab i+1's
// cells.
//
// Edge weight is uniform (1.0); the dither code normalizes by sum-
// of-weight so the absolute scale doesn't matter.
//
// pxSize is the raster pixel size used for within-slab adjacency
// (mm). Pass 0 to default to cellSize / 4 — fine resolution to
// distinguish hex cells of radius cellSize/√3.
func BuildAdjacency(slabs []Slab, cellSize, pxSize float32) [][]voxel.Neighbor {
	pxSize = resolveAdjPxSize(pxSize, cellSize)
	globalOffsets := SlabGlobalOffsets(slabs)
	neighbors := make([][]voxel.Neighbor, globalOffsets[len(slabs)])

	// Within-slab adjacency.
	for si := range slabs {
		AddWithinSlabAdjacency(&slabs[si], globalOffsets[si], cellSize, pxSize, neighbors)
	}

	// Cross-slab adjacency.
	for si := 0; si < len(slabs)-1; si++ {
		AddCrossSlabAdjacency(&slabs[si], globalOffsets[si], &slabs[si+1], globalOffsets[si+1], neighbors)
	}

	return neighbors
}

// SlabGlobalOffsets returns globalOffsets where globalOffsets[i] is
// the cumulative cell count in slabs[0..i) and globalOffsets[len] is
// the total cell count. Useful for converting per-slab local cell
// indices into the global adjacency indexing.
func SlabGlobalOffsets(slabs []Slab) []int {
	off := make([]int, len(slabs)+1)
	for i := range slabs {
		off[i+1] = off[i] + len(slabs[i].Cells)
	}
	return off
}

// AddWithinSlabAdjacency is the per-slab within-cell adjacency pass
// exposed for callers that schedule slabs across goroutines. The
// neighbors slice must be sized for the full global cell count;
// this call only writes into the [baseGlobal, baseGlobal+len(s.Cells))
// range, so concurrent calls on different slabs are race-free.
//
// pxSize <= 0 picks the cellSize/4 default that BuildAdjacency uses.
func AddWithinSlabAdjacency(s *Slab, baseGlobal int, cellSize, pxSize float32, neighbors [][]voxel.Neighbor) {
	if len(s.Cells) == 0 {
		return
	}
	pxSize = resolveAdjPxSize(pxSize, cellSize)
	addWithinSlabAdjacency(s, baseGlobal, cellSize, pxSize, neighbors)
}

// AddCrossSlabAdjacency is the per-pair cross-slab adjacency pass.
// neighbors must be sized for the full global cell count; this call
// writes into both slabs' index ranges, so concurrent calls on
// overlapping pairs (i,i+1) and (i+1,i+2) are NOT safe without
// external synchronization on the shared slab's neighbor rows.
func AddCrossSlabAdjacency(a *Slab, baseA int, b *Slab, baseB int, neighbors [][]voxel.Neighbor) {
	if len(a.Cells) == 0 || len(b.Cells) == 0 {
		return
	}
	addCrossSlabAdjacency(a, baseA, b, baseB, neighbors)
}

func resolveAdjPxSize(pxSize, cellSize float32) float32 {
	if pxSize > 0 {
		return pxSize
	}
	return cellSize / 4
}

// addWithinSlabAdjacency rasterizes cells at pxSize, scans the grid
// for differing neighboring cellIDs, and registers each (i,j) pair
// once in `neighbors` with the cells' global indices (baseGlobal +
// cellIdx). De-dups via a per-slab map so the result is symmetric.
func addWithinSlabAdjacency(s *Slab, baseGlobal int, cellSize, pxSize float32, neighbors [][]voxel.Neighbor) {
	if s.Footprint == nil {
		return
	}
	minX, minY, maxX, maxY, ok := s.Footprint.Bounds()
	if !ok {
		return
	}
	margin := cellSize
	minX -= margin
	minY -= margin
	maxX += margin
	maxY += margin
	w := int(math.Ceil(float64((maxX-minX)/pxSize))) + 2
	h := int(math.Ceil(float64((maxY-minY)/pxSize))) + 2
	if w < 1 || h < 1 {
		return
	}
	cellIDs := rasterizeCellsForDebug(s.Cells, minX, minY, pxSize, 1, w, h)

	type key struct{ a, b int32 }
	seen := map[key]struct{}{}
	emit := func(a, b int32) {
		if a == b {
			return
		}
		if a > b {
			a, b = b, a
		}
		k := key{a, b}
		if _, dup := seen[k]; dup {
			return
		}
		seen[k] = struct{}{}
		ga := baseGlobal + int(a)
		gb := baseGlobal + int(b)
		neighbors[ga] = append(neighbors[ga], voxel.Neighbor{Idx: gb, Weight: 1})
		neighbors[gb] = append(neighbors[gb], voxel.Neighbor{Idx: ga, Weight: 1})
	}
	for py := 0; py < h; py++ {
		row := py * w
		for px := 0; px < w; px++ {
			id := cellIDs[row+px]
			if id < 0 {
				continue
			}
			if px+1 < w {
				nid := cellIDs[row+px+1]
				if nid >= 0 && nid != id {
					emit(int32(id), int32(nid))
				}
			}
			if py+1 < h {
				nid := cellIDs[(py+1)*w+px]
				if nid >= 0 && nid != id {
					emit(int32(id), int32(nid))
				}
			}
		}
	}
}

// addCrossSlabAdjacency finds cell pairs in slab A × slab B whose
// XY polygons overlap (area > 0) and emits each pair into
// `neighbors` keyed by global index. Slab-B cells are pre-sorted by
// minX so each slab-A query walks only the candidates whose X
// interval intersects via a single binary-search-then-scan pass.
// Y-bbox is checked cheaply before invoking Clipper.
func addCrossSlabAdjacency(a *Slab, baseA int, b *Slab, baseB int, neighbors [][]voxel.Neighbor) {
	type bIndex struct {
		idx        int
		minX, maxX float32
		minY, maxY float32
	}
	bIdx := make([]bIndex, len(b.Cells))
	for i := range b.Cells {
		minX, minY, maxX, maxY := polyBounds(b.Cells[i].Outer)
		bIdx[i] = bIndex{idx: i, minX: minX, maxX: maxX, minY: minY, maxY: maxY}
	}
	sort.Slice(bIdx, func(i, j int) bool { return bIdx[i].minX < bIdx[j].minX })
	bMinX := make([]float32, len(bIdx))
	for i := range bIdx {
		bMinX[i] = bIdx[i].minX
	}

	for ai := range a.Cells {
		ca := &a.Cells[ai]
		aMinX, aMinY, aMaxX, aMaxY := polyBounds(ca.Outer)
		// Candidates have minX <= aMaxX. Upper bound via binary search.
		hi := sort.Search(len(bMinX), func(i int) bool { return bMinX[i] > aMaxX })
		// Walk candidates, accept if maxX >= aMinX AND Y bboxes
		// overlap; then Clipper-intersect.
		ga := baseA + ai
		for ci := 0; ci < hi; ci++ {
			cand := bIdx[ci]
			if cand.maxX < aMinX {
				continue
			}
			if cand.maxY < aMinY || cand.minY > aMaxY {
				continue
			}
			if polyOverlapArea(ca.Outer, b.Cells[cand.idx].Outer) > 0 {
				gb := baseB + cand.idx
				neighbors[ga] = append(neighbors[ga], voxel.Neighbor{Idx: gb, Weight: 1})
				neighbors[gb] = append(neighbors[gb], voxel.Neighbor{Idx: ga, Weight: 1})
			}
		}
	}
}

// polyOverlapArea returns the Clipper intersection area of two
// closed polygons in mm². Returns 0 on failure or no overlap.
func polyOverlapArea(a, b []Point2) float64 {
	c := clipper.NewClipper(clipper.IoNone)
	c.AddPaths(clipper.Paths{pointsToClipperPath(a)}, clipper.PtSubject, true)
	c.AddPaths(clipper.Paths{pointsToClipperPath(b)}, clipper.PtClip, true)
	result, ok := c.Execute1(clipper.CtIntersection, clipper.PftNonZero, clipper.PftNonZero)
	if !ok || len(result) == 0 {
		return 0
	}
	var area float64
	for _, path := range result {
		area += math.Abs(clipper.Area(path))
	}
	// Clipper paths are scaled by clipperScale on both axes, so
	// area is scaled by clipperScale². Convert back to mm².
	return area / (clipperScale * clipperScale)
}
