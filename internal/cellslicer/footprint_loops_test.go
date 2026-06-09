package cellslicer

import (
	"math"
	"sort"
	"testing"
)

// cellCentroid returns the area-weighted centroid of a cell's Outer ring
// and its absolute area. Used by the bucket-equivalence test to match
// cells between the two clip paths independent of vertex ordering or any
// extra collinear vertices the bucketed clip may insert.
func cellCentroid(outer []Point2) (cx, cy, area float64) {
	var a, axc, ayc float64
	n := len(outer)
	for k := 0; k < n; k++ {
		p := outer[k]
		q := outer[(k+1)%n]
		cross := float64(p[0])*float64(q[1]) - float64(q[0])*float64(p[1])
		a += cross
		axc += (float64(p[0]) + float64(q[0])) * cross
		ayc += (float64(p[1]) + float64(q[1])) * cross
	}
	if a == 0 {
		// Degenerate; fall back to the vertex average.
		for _, p := range outer {
			cx += float64(p[0])
			cy += float64(p[1])
		}
		return cx / float64(n), cy / float64(n), 0
	}
	cx = axc / (3 * a)
	cy = ayc / (3 * a)
	return cx, cy, math.Abs(a / 2)
}

// TestFootprintLoopClipMatchesWholeFootprint is the correctness guard for
// the footprintLoopIndex optimisation: clipping each Voronoi cell against
// only the footprint loops whose bbox overlaps it must yield the same cells
// as clipping against the whole footprint. It runs the real
// PartitionSlabAnalytic (so both ring and hex seeds, plus the cap region,
// are exercised) with the loop index on and off and matches every cell by
// centroid+area+kind.
//
// These fixtures (squares, holes, reflex corners, disjoint components) have
// no coincident-edge degeneracies, so the match is exact to the bit — the
// loop selection is winding-exact (a dropped loop's bbox is disjoint from
// the cell, so it can't cross or enclose it). That is the property under
// test: that the bbox overlap test never drops a loop that touches a cell.
//
// On a real footprint whose loops DO share coincident edges, Clipper can
// tie-break the degeneracy differently for a loop subset, shifting a few
// cells by ≤1µm — see footprintLoopIndex's doc. Don't tighten this test to
// chase that; it is an ill-defined degeneracy, not a coverage loss.
func TestFootprintLoopClipMatchesWholeFootprint(t *testing.T) {
	const cellSize float32 = 1.0
	cases := []struct {
		name  string
		loops []Loop
	}{
		{
			name: "square",
			loops: []Loop{
				makeLoop([]Point2{{0, 0}, {20, 0}, {20, 20}, {0, 20}}, false),
			},
		},
		{
			name: "square_with_hole",
			loops: []Loop{
				makeLoop([]Point2{{0, 0}, {20, 0}, {20, 20}, {0, 20}}, false),
				makeLoop([]Point2{{8, 8}, {8, 12}, {12, 12}, {12, 8}}, true),
			},
		},
		{
			name: "thin_strip",
			loops: []Loop{
				makeLoop([]Point2{{0, 0}, {30, 0}, {30, 3}, {0, 3}}, false),
			},
		},
		{
			name: "L_shape",
			loops: []Loop{
				makeLoop([]Point2{{0, 0}, {20, 0}, {20, 10}, {10, 10}, {10, 20}, {0, 20}}, false),
			},
		},
		{
			name: "plus",
			loops: []Loop{
				makeLoop([]Point2{
					{10, 0}, {20, 0}, {20, 10}, {30, 10}, {30, 20}, {20, 20},
					{20, 30}, {10, 30}, {10, 20}, {0, 20}, {0, 10}, {10, 10},
				}, false),
			},
		},
		{
			// Two disjoint components close together: a seed's scratch box
			// reaches across the gap into the other component, the case where
			// a far footprint edge enters the box.
			name: "two_squares",
			loops: []Loop{
				makeLoop([]Point2{{0, 0}, {8, 0}, {8, 8}, {0, 8}}, false),
				makeLoop([]Point2{{10, 0}, {18, 0}, {18, 8}, {10, 8}}, false),
			},
		},
	}

	run := func(fp *Footprint, disable bool) []Cell {
		prev := disableFootprintLoopIndex
		disableFootprintLoopIndex = disable
		defer func() { disableFootprintLoopIndex = prev }()
		cells, _, _ := PartitionSlabAnalytic(fp, nil, nil, cellSize)
		return cells
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := ComputeFootprint(tc.loops, tc.loops)

			bucketed := run(fp, false)
			whole := run(fp, true)

			if len(bucketed) != len(whole) {
				t.Fatalf("cell count differs: bucketed=%d whole=%d", len(bucketed), len(whole))
			}

			type entry struct {
				cx, cy, area float64
				kind         CellKind
			}
			toEntries := func(cells []Cell) []entry {
				es := make([]entry, len(cells))
				for i := range cells {
					cx, cy, a := cellCentroid(cells[i].Outer)
					es[i] = entry{cx, cy, a, cells[i].Kind}
				}
				sort.Slice(es, func(a, b int) bool {
					if es[a].cx != es[b].cx {
						return es[a].cx < es[b].cx
					}
					if es[a].cy != es[b].cy {
						return es[a].cy < es[b].cy
					}
					return es[a].area < es[b].area
				})
				return es
			}
			eb := toEntries(bucketed)
			ew := toEntries(whole)

			const eps = 1e-4 // mm / mm²; clips are the same op, differ only in noise
			var bArea, wArea float64
			for i := range eb {
				bArea += eb[i].area
				wArea += ew[i].area
				if math.Abs(eb[i].cx-ew[i].cx) > eps ||
					math.Abs(eb[i].cy-ew[i].cy) > eps ||
					math.Abs(eb[i].area-ew[i].area) > eps ||
					eb[i].kind != ew[i].kind {
					t.Errorf("cell %d mismatch:\n bucketed c=(%.5f,%.5f) a=%.6f k=%v\n whole    c=(%.5f,%.5f) a=%.6f k=%v",
						i, eb[i].cx, eb[i].cy, eb[i].area, eb[i].kind,
						ew[i].cx, ew[i].cy, ew[i].area, ew[i].kind)
				}
			}
			if math.Abs(bArea-wArea) > 1e-3 {
				t.Errorf("total area differs: bucketed=%.6f whole=%.6f", bArea, wArea)
			}
		})
	}
}
