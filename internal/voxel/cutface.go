package voxel

// A cut face is the new interior surface a split exposes. Its interior is
// hidden once the two halves are reassembled, so dithering it wastes print
// time (a stippled region forces a filament swap every layer and inflates
// the triangle count) with no visual benefit. The perimeter band IS
// visible at the reassembled seam, so it keeps its real dithered color;
// the interior is flat-filled with one exact filament by the Clip stage.

// ClassifyCutFaceInterior splits the cut-face cells (isCut[i] == true) into
// a rim band and an interior, returning a mask where interior[i] is true
// for the cells the Clip stage should flat-fill (the deep, effectively
// hidden part) and false for the rim cells it should leave dithered.
//
// The rim band is measured topologically in cell hops over the adjacency
// graph: a cut-face cell touching a non-cut-face cell is a rim seed (it
// sits on the model's exterior surface, i.e. the visible seam), and every
// cut-face cell within bandHops of a seed stays in the band. Remaining
// cut-face cells — including any the rim BFS never reaches (a cut face with
// no exterior-touching seed is fully interior) — are interior. Measuring in
// hops (rather than mm) is orientation-independent, so it behaves
// identically for axis-aligned and tilted cuts.
//
// Returns nil when there are no interior cells (e.g. the whole cut face is
// within bandHops of its perimeter), so callers can cheaply skip the rest
// of the special-casing. The Clip stage colors the interior with a single
// global filament, so this classifier deliberately does not assign
// per-cell colors.
func ClassifyCutFaceInterior(neighbors [][]Neighbor, isCut []bool, bandHops int) []bool {
	n := len(neighbors)
	if n == 0 || len(isCut) != n {
		return nil
	}
	if bandHops < 0 {
		bandHops = 0
	}

	// Hop distance from the rim, BFS through cut-face cells only.
	const unreached = 1 << 30
	dist := make([]int, n)
	for i := range dist {
		dist[i] = unreached
	}
	queue := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if !isCut[i] {
			continue
		}
		for _, nb := range neighbors[i] {
			if nb.Idx >= 0 && nb.Idx < n && !isCut[nb.Idx] {
				dist[i] = 0
				queue = append(queue, i)
				break
			}
		}
	}
	for qi := 0; qi < len(queue); qi++ {
		u := queue[qi]
		for _, nb := range neighbors[u] {
			v := nb.Idx
			if v < 0 || v >= n || !isCut[v] {
				continue
			}
			if dist[v] > dist[u]+1 {
				dist[v] = dist[u] + 1
				queue = append(queue, v)
			}
		}
	}

	interior := make([]bool, n)
	anyInterior := false
	for i := 0; i < n; i++ {
		// dist[i] > bandHops covers both cut-face cells beyond the band
		// AND cut-face cells the rim BFS never reached (a cut face with
		// no exterior-touching seed — fully interior).
		if isCut[i] && dist[i] > bandHops {
			interior[i] = true
			anyInterior = true
		}
	}
	if !anyInterior {
		return nil
	}
	return interior
}
