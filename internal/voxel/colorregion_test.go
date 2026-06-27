package voxel

import (
	"context"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/progress"
)

// makeRowCells builds a 1-D row of unit cells (Col 0..n-1) each with the
// given colour, so BuildNeighbors links them face-to-face into a line.
func makeRowCells(colors [][3]uint8) []ActiveCell {
	cells := make([]ActiveCell, len(colors))
	for i, c := range colors {
		cells[i] = ActiveCell{Col: i, Color: c, Area: 1}
	}
	return cells
}

func TestCutNeighborsByColorSplitsAtBoundary(t *testing.T) {
	black := [3]uint8{0, 0, 0}
	white := [3]uint8{255, 255, 255}
	// Two blacks then two whites in a line: B-B | W-W. The middle edge
	// (black↔white) should be cut; the within-colour edges kept.
	cells := makeRowCells([][3]uint8{black, black, white, white})
	nb := BuildNeighbors(cells)
	cut := CutNeighborsByColor(cells, nb, 20)

	hasEdge := func(g [][]Neighbor, a, b int) bool {
		for _, n := range g[a] {
			if n.Idx == b {
				return true
			}
		}
		return false
	}
	if !hasEdge(cut, 0, 1) || !hasEdge(cut, 2, 3) {
		t.Fatalf("within-colour edges should survive the cut")
	}
	if hasEdge(cut, 1, 2) || hasEdge(cut, 2, 1) {
		t.Fatalf("black↔white edge should be cut at ΔE>20")
	}

	labels, k := colorComponents(cut)
	if k != 2 {
		t.Fatalf("expected 2 colour components, got %d", k)
	}
	if labels[0] != labels[1] || labels[2] != labels[3] {
		t.Fatalf("same-colour cells must share a component: %v", labels)
	}
	if labels[0] == labels[2] {
		t.Fatalf("black and white must be different components: %v", labels)
	}
}

func TestCutNeighborsByColorKeepsGradient(t *testing.T) {
	// A smooth ramp where each step is small (ΔE < threshold) stays one
	// connected region even though the ends are far apart — error should
	// still diffuse along the gradient, matching flood-fill semantics.
	ramp := make([][3]uint8, 8)
	for i := range ramp {
		v := uint8(i * 255 / (len(ramp) - 1))
		ramp[i] = [3]uint8{v, v, v}
	}
	cells := makeRowCells(ramp)
	cut := CutNeighborsByColor(cells, BuildNeighbors(cells), 20)
	_, k := colorComponents(cut)
	if k != 1 {
		t.Fatalf("a smooth gradient should remain a single region, got %d components", k)
	}
}

func TestCutNeighborsByColorDoesNotMutateInput(t *testing.T) {
	cells := makeRowCells([][3]uint8{{0, 0, 0}, {255, 255, 255}})
	nb := BuildNeighbors(cells)
	before := len(nb[0])
	_ = CutNeighborsByColor(cells, nb, 20)
	if len(nb[0]) != before {
		t.Fatalf("CutNeighborsByColor mutated the input graph")
	}
}

// TestDitherPerComponentIsolatesError checks that no quantization error
// crosses a colour boundary: a row of mid-grey cells abutting a row of
// pure black cells, dithered with a black/white palette, must leave every
// black cell assigned to black. Without confinement the grey region's
// diffused error reaches across the boundary and flips boundary black
// cells to white.
func TestDitherPerComponentIsolatesError(t *testing.T) {
	grey := [3]uint8{128, 128, 128}
	black := [3]uint8{0, 0, 0}
	colors := make([][3]uint8, 0, 16)
	for i := 0; i < 8; i++ {
		colors = append(colors, grey)
	}
	for i := 0; i < 8; i++ {
		colors = append(colors, black)
	}
	cells := makeRowCells(colors)
	pal := [][3]uint8{{0, 0, 0}, {255, 255, 255}}
	palAlpha := []float32{1, 1}
	cut := CutNeighborsByColor(cells, BuildNeighbors(cells), 20)

	assign, err := DitherPerComponent(context.Background(), cells, pal, palAlpha, cut, progress.NullTracker{},
		func(ctx context.Context, c []ActiveCell, p [][3]uint8, pa []float32, nbr [][]Neighbor, tr progress.Tracker) ([]int32, error) {
			return FloydSteinberg(ctx, c, p, pa, nbr, tr)
		})
	if err != nil {
		t.Fatalf("DitherPerComponent: %v", err)
	}
	if len(assign) != len(cells) {
		t.Fatalf("assignment length %d != %d cells", len(assign), len(cells))
	}
	for i := 8; i < 16; i++ {
		if assign[i] != 0 { // 0 == black
			t.Fatalf("black cell %d should stay black (palette 0), got %d", i, assign[i])
		}
	}
}
