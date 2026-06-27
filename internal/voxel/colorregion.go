package voxel

import (
	"context"

	"github.com/rtwfroody/ditherforge/internal/progress"
	colorful "github.com/lucasb-eyer/go-colorful"
)

// colorDeltaE76 is the CIE76 (Euclidean CIELAB) perceptual distance
// between two sRGB triplets, in standard ΔE units. It matches
// cellslicer.deltaE76 exactly (go-colorful's Lab is scaled 1/100, so the
// ×100 restores the familiar 0..100 range), so a region-confined dither
// and color-aware-cells segmentation agree on what counts as a colour
// boundary when given the same threshold.
func colorDeltaE76(a, b [3]uint8) float64 {
	ca := colorful.Color{R: float64(a[0]) / 255, G: float64(a[1]) / 255, B: float64(a[2]) / 255}
	cb := colorful.Color{R: float64(b[0]) / 255, G: float64(b[1]) / 255, B: float64(b[2]) / 255}
	return ca.DistanceLab(cb) * 100
}

// CutNeighborsByColor returns a copy of the adjacency graph with every
// edge removed whose endpoint cell colours differ by more than deltaE
// (CIE76). This is the dual of flood-fill colour segmentation: the
// connected components of the cut graph are exactly the ΔE-gated colour
// regions, so dithering over the cut graph can never diffuse quantization
// error across a colour boundary (e.g. a grey region into an adjacent
// solid black or white one).
//
// The input graph is left untouched (it is typically the cached voxelize
// adjacency); per-cell neighbour slices are freshly allocated. Symmetry
// of the input is preserved because the ΔE test is symmetric.
func CutNeighborsByColor(cells []ActiveCell, neighbors [][]Neighbor, deltaE float64) [][]Neighbor {
	out := make([][]Neighbor, len(neighbors))
	for i, nbrs := range neighbors {
		var kept []Neighbor
		for _, nb := range nbrs {
			if colorDeltaE76(cells[i].Color, cells[nb.Idx].Color) <= deltaE {
				kept = append(kept, nb)
			}
		}
		out[i] = kept
	}
	return out
}

// colorComponents labels each cell with the index of its connected
// component in the (already colour-cut) adjacency graph, via union-find.
// Returns a slice of length len(neighbors) of dense component ids
// (0..k-1) and the component count k. The graph is treated as undirected;
// edges are unioned in both directions so an asymmetric input still yields
// correct components.
func colorComponents(neighbors [][]Neighbor) ([]int, int) {
	n := len(neighbors)
	parent := make([]int, n)
	rank := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]] // path halving
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		if rank[ra] < rank[rb] {
			ra, rb = rb, ra
		}
		parent[rb] = ra
		if rank[ra] == rank[rb] {
			rank[ra]++
		}
	}
	for i, nbrs := range neighbors {
		for _, nb := range nbrs {
			union(i, nb.Idx)
		}
	}
	labels := make([]int, n)
	dense := make(map[int]int, n)
	k := 0
	for i := range labels {
		root := find(i)
		id, ok := dense[root]
		if !ok {
			id = k
			dense[root] = id
			k++
		}
		labels[i] = id
	}
	return labels, k
}

// CellDither is the common shape of the per-cell dither kernels
// (Riemersma, RiemersmaPair, FloydSteinberg, …) once their extra knobs
// are bound in a closure: it maps cells + an adjacency graph to a
// per-cell palette assignment.
type CellDither func(ctx context.Context, cells []ActiveCell, pal [][3]uint8, palAlpha []float32, neighbors [][]Neighbor, tracker progress.Tracker) ([]int32, error)

// DitherPerComponent runs dither independently on each connected
// component of the supplied (colour-cut) adjacency graph and stitches the
// results back into a single assignment slice indexed like cells.
//
// Plain edge-cutting already confines pure error-diffusion dithers
// (dizzy / Floyd-Steinberg) to a region, because they only ever push
// error along graph edges. The space-filling dithers (Riemersma) instead
// carry a sliding error window along a tour that jumps freely between
// cells, so cutting edges is not enough — the window would leak across a
// boundary on a jump. Running them per component gives each region its own
// tour and a fresh window, which is the tour-dither equivalent of
// "dither within this colour region only".
func DitherPerComponent(ctx context.Context, cells []ActiveCell, pal [][3]uint8, palAlpha []float32, neighbors [][]Neighbor, tracker progress.Tracker, fn CellDither) ([]int32, error) {
	labels, k := colorComponents(neighbors)
	// Bucket global cell indices by component.
	members := make([][]int, k)
	for i, lbl := range labels {
		members[lbl] = append(members[lbl], i)
	}
	result := make([]int32, len(cells))
	for _, idxs := range members {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Build the component's local cell slice and a neighbour graph
		// remapped to local indices (every neighbour of a component cell
		// is in the same component, so no edge is dropped here).
		local := make(map[int]int, len(idxs))
		for li, gi := range idxs {
			local[gi] = li
		}
		subCells := make([]ActiveCell, len(idxs))
		subNbrs := make([][]Neighbor, len(idxs))
		for li, gi := range idxs {
			subCells[li] = cells[gi]
			var sn []Neighbor
			for _, nb := range neighbors[gi] {
				if lj, ok := local[nb.Idx]; ok {
					sn = append(sn, Neighbor{Idx: lj, Weight: nb.Weight})
				}
			}
			subNbrs[li] = sn
		}
		sub, err := fn(ctx, subCells, pal, palAlpha, subNbrs, tracker)
		if err != nil {
			return nil, err
		}
		for li, gi := range idxs {
			result[gi] = sub[li]
		}
	}
	return result, nil
}
