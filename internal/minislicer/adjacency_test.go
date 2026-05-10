package minislicer

import (
	"testing"
)

func TestAdjacencyWithinLoop(t *testing.T) {
	loop := Loop{
		Points: []Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}},
		Z:      0,
	}
	loop.SignedArea = signedArea(loop.Points)
	layers := []Layer{{Z: 0, LayerIdx: 0, Loops: []Loop{loop}}}
	secs := PartitionLoops(layers, 1.0)
	if len(secs) != 4 {
		t.Fatalf("got %d sections, want 4", len(secs))
	}
	g := BuildSectionGraph(secs, layers, 0)
	for i, ns := range g {
		if len(ns) != 2 {
			t.Errorf("section %d: got %d neighbors, want 2 (prev/next)", i, len(ns))
			continue
		}
		seen := map[int]bool{}
		for _, n := range ns {
			seen[n.Idx] = true
		}
		wantPrev := (i - 1 + 4) % 4
		wantNext := (i + 1) % 4
		if !seen[wantPrev] || !seen[wantNext] {
			t.Errorf("section %d neighbors %+v missing prev=%d or next=%d", i, ns, wantPrev, wantNext)
		}
	}
}

func TestAdjacencyCrossLayer(t *testing.T) {
	// Two stacked unit squares at z=0 and z=0.5.
	loop1 := Loop{Points: []Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}}, Z: 0}
	loop1.SignedArea = signedArea(loop1.Points)
	loop2 := Loop{Points: []Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}}, Z: 0.5}
	loop2.SignedArea = signedArea(loop2.Points)
	layers := []Layer{
		{Z: 0, LayerIdx: 0, Loops: []Loop{loop1}},
		{Z: 0.5, LayerIdx: 1, Loops: []Loop{loop2}},
	}
	secs := PartitionLoops(layers, 1.0)
	if len(secs) != 8 {
		t.Fatalf("got %d sections, want 8", len(secs))
	}
	// Use proximityRadius == cellSize == 1.0; should connect each
	// section to its same-XY counterpart in the other layer.
	g := BuildSectionGraph(secs, layers, 1.0)
	for i, s := range secs {
		var crossLayer int
		for _, n := range g[i] {
			if secs[n.Idx].LayerIdx != s.LayerIdx {
				crossLayer++
			}
		}
		if crossLayer < 1 {
			t.Errorf("section %d (layer %d, mid=%v): no cross-layer neighbor", i, s.LayerIdx, s.Mid)
		}
	}
}
