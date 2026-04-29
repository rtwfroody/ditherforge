package split

import (
	"fmt"
)

// recoverLoops walks each half's cut-edge list into a set of closed
// loops in vertex-index space. Each midpoint vertex on a watertight
// input belongs to exactly two cut edges, so the walk is determined by
// "the next edge whose start equals my end."
//
// Returns loops[half] = list of closed sequences of vertex indices in
// halves[half]. The first vertex is repeated implicitly at the end of
// each loop (i.e. we return open polylines that close).
func (b *cutBuilder) recoverLoops() ([2][][]uint32, error) {
	var out [2][][]uint32
	for h := 0; h < 2; h++ {
		loops, err := buildLoops(b.cutEdges[h])
		if err != nil {
			return out, fmt.Errorf("split.Cut: half %d: %w", h, err)
		}
		out[h] = loops
	}
	return out, nil
}

// buildLoops takes a list of directed edges and reconstructs closed
// loops by following the unique outgoing edge at each visited vertex.
// On a manifold cut polygon every vertex has exactly one outgoing edge
// in the input list (because the cut polygon is itself a 1-manifold:
// each midpoint sees exactly one face on each side, contributing one
// outgoing and one incoming edge to a given half's wound boundary).
func buildLoops(edges [][2]uint32) ([][]uint32, error) {
	if len(edges) == 0 {
		return nil, nil
	}
	// next[v] = w means the edge starting at v goes to w. If v already
	// has a next, the input is non-manifold along the cut.
	next := make(map[uint32]uint32, len(edges))
	for _, e := range edges {
		if _, dup := next[e[0]]; dup {
			return nil, fmt.Errorf("non-manifold cut polygon: vertex %d has multiple outgoing edges", e[0])
		}
		next[e[0]] = e[1]
	}
	visited := make(map[uint32]bool, len(edges))

	var loops [][]uint32
	for _, e := range edges {
		if visited[e[0]] {
			continue
		}
		var loop []uint32
		v := e[0]
		for {
			if visited[v] {
				if v != loop[0] {
					return nil, fmt.Errorf("cut-polygon walk closed at non-start vertex %d", v)
				}
				break
			}
			visited[v] = true
			loop = append(loop, v)
			w, ok := next[v]
			if !ok {
				return nil, fmt.Errorf("cut-polygon walk hit dead end at vertex %d", v)
			}
			v = w
		}
		loops = append(loops, loop)
	}

	// Sanity: every input edge's start should now be visited.
	for _, e := range edges {
		if !visited[e[0]] {
			return nil, fmt.Errorf("cut-polygon walk missed vertex %d", e[0])
		}
	}
	return loops, nil
}
