package minislicer

import "testing"

// TestVerifyPasses confirms a normal partition + arbitrary
// per-section colors yields all patches >= cellSize. Each section
// IS at least cellSize long by construction, so every group of 1+
// sections of the same color is too.
func TestVerifyPasses(t *testing.T) {
	loop := Loop{Points: []Point2{{0, 0}, {4, 0}, {4, 1}, {0, 1}}, Z: 0}
	loop.RefreshDerived()
	layers := []Layer{{Z: 0, LayerIdx: 0, Loops: []Loop{loop}}}
	secs := PartitionLoops(layers, 1.0)
	// Each section gets a distinct color (worst case for patch
	// length: every patch is exactly one section).
	assigns := make([]int32, len(secs))
	for i := range assigns {
		assigns[i] = int32(i)
	}
	_, ok := VerifyPatchLengths(secs, layers, assigns, 1.0)
	if !ok {
		t.Errorf("expected ok=true; partitioner guarantees section length >= cellSize")
	}
}

// TestVerifyDetectsShort fakes a short section (forced to <
// cellSize) and confirms VerifyPatchLengths flags it.
func TestVerifyDetectsShort(t *testing.T) {
	loop := Loop{Points: []Point2{{0, 0}, {4, 0}, {4, 1}, {0, 1}}, Z: 0}
	loop.RefreshDerived()
	layers := []Layer{{Z: 0, LayerIdx: 0, Loops: []Loop{loop}}}
	secs := PartitionLoops(layers, 1.0)
	if len(secs) < 4 {
		t.Fatalf("got %d sections, want >= 4", len(secs))
	}
	// Manually mutate one section's length to be sub-cellSize, all
	// distinct colors. The mutated section should be flagged.
	secs[0].Length = 0.1
	assigns := make([]int32, len(secs))
	for i := range assigns {
		assigns[i] = int32(i)
	}
	reports, ok := VerifyPatchLengths(secs, layers, assigns, 1.0)
	if ok {
		t.Errorf("expected ok=false")
	}
	totalShort := 0
	for _, r := range reports {
		totalShort += r.NumPatchesShort
	}
	if totalShort != 1 {
		t.Errorf("got %d short patches, want 1", totalShort)
	}
}

// TestVerifyExemptsTinyLoop confirms a loop with perimeter < cellSize
// is not flagged (its single section is unavoidably sub-cellSize).
func TestVerifyExemptsTinyLoop(t *testing.T) {
	// Triangle with perimeter ~3.0, cellSize=10.
	loop := Loop{Points: []Point2{{0, 0}, {1, 0}, {0.5, 0.866}}, Z: 0}
	loop.RefreshDerived()
	layers := []Layer{{Z: 0, LayerIdx: 0, Loops: []Loop{loop}}}
	secs := PartitionLoops(layers, 10.0)
	if len(secs) != 1 {
		t.Fatalf("got %d sections, want 1 (single sub-cellSize section)", len(secs))
	}
	assigns := []int32{0}
	_, ok := VerifyPatchLengths(secs, layers, assigns, 10.0)
	if !ok {
		t.Errorf("expected ok=true (tiny loop should be exempt)")
	}
}
