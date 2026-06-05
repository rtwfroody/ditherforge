package cellslicer

import (
	clipper "github.com/ctessum/go.clipper"
)

// footprintLoopIndex localizes the per-cell footprint clip in voronoiCells.
// The full slab footprint can carry hundreds of vertices across many loops
// (the coverTarget is band ∪ cap, plus holes and disjoint components), but
// each Voronoi cell is a ~cellSize convex polygon that only a few of those
// loops can touch. Instead of clipping every cell against all the loops, we
// clip it against just the loops whose bounding box overlaps the cell.
//
// The selection is winding-EXACT, so in exact arithmetic this is identical
// to clipping each cell against the whole footprint:
//
//   - The loops are passed as their ORIGINAL Clipper paths, never
//     re-processed. (An earlier version intersected the footprint with a
//     window rectangle; that re-ran the footprint through a Clipper Execute,
//     which re-resolved the band∪cap union's near-coincident edges and
//     perturbed cell boundaries by ~1µm. Selecting raw loops avoids any
//     re-processing — each loop is AddPath'd exactly as the whole-footprint
//     path would AddPath it.)
//
//   - Dropping a loop whose bbox is disjoint from the cell bbox changes
//     nothing: such a loop neither crosses the cell (so contributes no
//     boundary edge) nor encloses it (an enclosing loop's bbox must contain
//     the cell), so its NonZero winding contribution over every cell point
//     is zero. Thus cell ∩ (overlapping loops) = cell ∩ (all loops),
//     vertices included.
//
// In practice it is bit-identical on footprints whose loops don't share
// degenerate geometry (verified equal to the whole-footprint clip — cells
// AND adjacency edges — on earth.glb and low_poly_building.glb). The one
// residual: when coverTarget carries COINCIDENT edges (two loops sharing a
// segment, e.g. a band∪cap union Clipper didn't fully dissolve), Clipper's
// per-cell intersection can tie-break that degeneracy differently depending
// on which loops are in the passed subset. On glyphid_praetorian that nudges
// 8 of ~51k cells by ≤~1µm (total Δarea +0.0008 mm², area conserved to
// 0.000007%); the shifts sit on footprint-boundary edges within the 5µm
// open-edge bloat, and at an exact coincidence the "right" vertex is itself
// ill-defined, so neither result is more correct. The winding logic is
// guarded on clean fixtures by TestFootprintLoopClipMatchesWholeFootprint.
//
// Localization comes from dropping far loops — holes, separate components,
// and the far reaches of the band — so it helps in proportion to how much of
// the footprint's vertex count lives in loops a given cell doesn't touch. An
// enclosing loop (e.g. the model silhouette around an interior cell) is
// always kept; a slab whose vertices are all in one such loop sees little
// speedup. Measured Voxelize speedup: earth ~8%, building ~41%, glyphid ~49%.
type footprintLoopIndex struct {
	loops []loopClip
}

// loopClip is one footprint loop: its original Clipper path plus its mm
// bounding box, for the per-cell overlap test.
type loopClip struct {
	path                   clipper.Path
	minX, minY, maxX, maxY float32
}

// newFootprintLoopIndex captures fp's loops as raw Clipper paths keyed by
// their bounding boxes. footprintToClipperPaths emits one path per
// fp.Loops entry in order, and each loop's bbox is already populated by
// computeBbox (set when the footprint was collected), so the two line up
// index-for-index.
func newFootprintLoopIndex(fp *Footprint) *footprintLoopIndex {
	paths := footprintToClipperPaths(fp)
	li := &footprintLoopIndex{loops: make([]loopClip, len(fp.Loops))}
	for i := range fp.Loops {
		lp := &fp.Loops[i]
		li.loops[i] = loopClip{
			path: paths[i],
			minX: lp.MinX, minY: lp.MinY, maxX: lp.MaxX, maxY: lp.MaxY,
		}
	}
	return li
}

// clipFor returns the footprint loops whose bbox overlaps the cell's
// bounding box [minx,maxx]×[miny,maxy], as raw Clipper paths to clip the
// cell against. The cell MUST lie inside that bbox (it does — the bbox is
// computed from the cell's own vertices). Loops are shared read-only across
// cells (Clipper.AddPath copies the geometry it ingests), so the returned
// slice may alias the stored paths.
func (li *footprintLoopIndex) clipFor(minx, miny, maxx, maxy float32) clipper.Paths {
	var out clipper.Paths
	for i := range li.loops {
		l := &li.loops[i]
		if l.minX <= maxx && l.maxX >= minx && l.minY <= maxy && l.maxY >= miny {
			out = append(out, l.path)
		}
	}
	return out
}
