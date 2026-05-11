package minislicer

import (
	"sort"

	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// BuildSectionGraph returns the per-section neighbor list for use
// with the dither kernels in package voxel.
//
// Edges:
//   - Within-loop: each section's prev/next siblings in arc order
//     (cyclic). Weight 1.0 (mirrors the voxel grid's face-adjacency
//     weight).
//   - Cross-layer: section S in layer L is adjacent to any section
//     T in layers L±1 whose midpoint is within proximityRadius of
//     S.Mid in XY. Weight 0.5 (down-weighted vs same-layer because
//     the surface tangent direction in XY is the "primary"
//     diffusion axis, and Z carries less perceptual continuity at
//     typical layer heights).
//
// proximityRadius should be roughly cellSize so that each section's
// XY footprint looks at one neighbor in each adjacent layer.
func BuildSectionGraph(sections []Section, layers []Layer, proximityRadius float32) [][]voxel.Neighbor {
	n := len(sections)
	neigh := make([][]voxel.Neighbor, n)

	// Index sections by (LayerIdx, LoopIdx, Index) for within-loop
	// neighbor lookups.
	type loopKey struct{ layer, loop int }
	loopMembers := make(map[loopKey][]int)
	for i, s := range sections {
		k := loopKey{s.LayerIdx, s.LoopIdx}
		loopMembers[k] = append(loopMembers[k], i)
	}
	for _, ids := range loopMembers {
		sort.Slice(ids, func(a, b int) bool {
			return sections[ids[a]].Index < sections[ids[b]].Index
		})
	}

	// Within-loop adjacency. Two flavors:
	//
	//   - Ribbon loops (KindRibbon): cyclic prev/next in arc order.
	//   - Cap loops (KindCapTop / KindCapBottom): 4-neighbor grid
	//     adjacency by (TileCol, TileRow). Tiles aren't cyclic, so
	//     boundary tiles simply have fewer neighbors.
	//
	// All within-loop edges have weight 1.0 — same diffusion weight as
	// the voxel grid's face-adjacency.
	//
	// TODO: there's no explicit ribbon↔cap edge within a single
	// layer. A ribbon section at layer N's perimeter and a cap tile
	// at layer N's top or bottom face are physically adjacent
	// (they meet at the prism's top-of-wall / bottom-of-wall
	// corner) but the graph only connects them implicitly via
	// cross-layer XY proximity to N±1 sections. The dither's color
	// continuity at that corner is weaker than the geometry would
	// suggest — visible as a color seam between a slope's vertical
	// wall and its horizontal cap on some inputs.
	for _, ids := range loopMembers {
		m := len(ids)
		if m < 2 {
			continue
		}
		switch sections[ids[0]].Kind {
		case KindRibbon:
			for k := 0; k < m; k++ {
				cur := ids[k]
				prev := ids[(k-1+m)%m]
				next := ids[(k+1)%m]
				if m == 2 {
					neigh[cur] = append(neigh[cur], voxel.Neighbor{Idx: prev, Weight: 1.0})
				} else {
					neigh[cur] = append(neigh[cur],
						voxel.Neighbor{Idx: prev, Weight: 1.0},
						voxel.Neighbor{Idx: next, Weight: 1.0})
				}
			}
		case KindCapTop, KindCapBottom:
			// Index by (col, row) for O(1) neighbor lookup.
			tiles := make(map[[2]int]int, m)
			for _, id := range ids {
				s := sections[id]
				tiles[[2]int{s.TileCol, s.TileRow}] = id
			}
			steps := [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}}
			for _, id := range ids {
				s := sections[id]
				for _, st := range steps {
					if nb, ok := tiles[[2]int{s.TileCol + st[0], s.TileRow + st[1]}]; ok {
						neigh[id] = append(neigh[id], voxel.Neighbor{Idx: nb, Weight: 1.0})
					}
				}
			}
		}
	}

	// Cross-layer adjacency via XY proximity. Bucket each layer's
	// sections into a coarse grid of cell pitch == proximityRadius
	// for fast range queries.
	if proximityRadius > 0 {
		layerBuckets := make(map[int]*pointGrid, len(layers))
		layerIDs := make(map[int][]int, len(layers))
		for i, s := range sections {
			layerIDs[s.LayerIdx] = append(layerIDs[s.LayerIdx], i)
		}
		for layerIdx, ids := range layerIDs {
			pg := newPointGrid(proximityRadius)
			for _, id := range ids {
				pg.add(id, sections[id].Mid)
			}
			layerBuckets[layerIdx] = pg
		}

		const crossWeight float32 = 0.5
		rsq := proximityRadius * proximityRadius
		for i, s := range sections {
			for _, dl := range []int{-1, +1} {
				other := layerBuckets[s.LayerIdx+dl]
				if other == nil {
					continue
				}
				other.queryRadius(s.Mid, proximityRadius, func(j int) {
					dx := sections[j].Mid[0] - s.Mid[0]
					dy := sections[j].Mid[1] - s.Mid[1]
					if dx*dx+dy*dy <= rsq {
						neigh[i] = append(neigh[i], voxel.Neighbor{Idx: j, Weight: crossWeight})
					}
				})
			}
		}
	}

	return neigh
}

// pointGrid is a sparse 2D bucket index over points.
type pointGrid struct {
	cell    float32
	buckets map[[2]int][]int
}

func newPointGrid(cell float32) *pointGrid {
	return &pointGrid{cell: cell, buckets: map[[2]int][]int{}}
}

func (g *pointGrid) bucketOf(p Point2) [2]int {
	return [2]int{int(p[0] / g.cell), int(p[1] / g.cell)}
}

func (g *pointGrid) add(id int, p Point2) {
	k := g.bucketOf(p)
	g.buckets[k] = append(g.buckets[k], id)
}

func (g *pointGrid) queryRadius(center Point2, radius float32, cb func(id int)) {
	span := int(radius/g.cell) + 1
	c0 := g.bucketOf(center)
	for dx := -span; dx <= span; dx++ {
		for dy := -span; dy <= span; dy++ {
			k := [2]int{c0[0] + dx, c0[1] + dy}
			for _, id := range g.buckets[k] {
				cb(id)
			}
		}
	}
}
