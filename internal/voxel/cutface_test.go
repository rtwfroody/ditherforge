package voxel

import "testing"

// chainNeighbors builds a graph of `total` cells whose first `pathLen`
// cells form an undirected path 0-1-...-(pathLen-1), each edge weight 1.
// Cells at index >= pathLen start isolated. Optionally appends extra
// directed edges from extra (use it to attach exterior cells).
func chainNeighbors(total, pathLen int, extra map[int][]int) [][]Neighbor {
	nb := make([][]Neighbor, total)
	add := func(a, b int) {
		nb[a] = append(nb[a], Neighbor{Idx: b, Weight: 1})
	}
	for i := 0; i+1 < pathLen; i++ {
		add(i, i+1)
		add(i+1, i)
	}
	for a, bs := range extra {
		for _, b := range bs {
			add(a, b)
		}
	}
	return nb
}

// A chain of 11 cells: 0..9 are the cut face, cell 10 is exterior
// (non-cut) attached to cell 0. With bandHops=2, cells 0,1,2 are the rim
// band (left dithered) and 3..9 are interior (flat-filled).
func TestClassifyCutFaceInteriorRimBand(t *testing.T) {
	const n = 11
	isCut := make([]bool, n)
	for i := 0; i < 10; i++ {
		isCut[i] = true
	}
	// cells 0..9 are the cut-face path; cell 10 is exterior, attached
	// only to cell 0 (so cell 0 is the single rim seed).
	nb := chainNeighbors(n, 10, map[int][]int{0: {10}, 10: {0}})

	interior := ClassifyCutFaceInterior(nb, isCut, 2)
	if interior == nil {
		t.Fatal("expected an interior mask, got nil")
	}
	// Band: cells 0,1,2 (dist 0,1,2) stay dithered (not interior).
	for _, i := range []int{0, 1, 2} {
		if interior[i] {
			t.Errorf("cell %d should be in the rim band, not interior", i)
		}
	}
	// Interior: cells 3..9.
	for i := 3; i < 10; i++ {
		if !interior[i] {
			t.Errorf("cell %d should be interior", i)
		}
	}
	// Exterior (non-cut) cell is never interior.
	if interior[10] {
		t.Errorf("exterior cell 10 should not be interior")
	}
}

// A cut face with no exterior-touching cell (no rim seed) is fully
// interior: every cut cell is flat-fillable.
func TestClassifyCutFaceInteriorNoSeedAllInterior(t *testing.T) {
	const n = 6
	isCut := make([]bool, n)
	for i := range isCut {
		isCut[i] = true
	}
	nb := chainNeighbors(n, n, nil)

	interior := ClassifyCutFaceInterior(nb, isCut, 2)
	if interior == nil {
		t.Fatal("expected an interior mask, got nil")
	}
	for i := 0; i < n; i++ {
		if !interior[i] {
			t.Errorf("cell %d should be interior (no rim seed exists)", i)
		}
	}
}

// A band wide enough to cover the whole cut face leaves no interior, so
// the classifier returns nil (caller skips the special case entirely).
func TestClassifyCutFaceInteriorBandCoversAll(t *testing.T) {
	const n = 5
	isCut := make([]bool, n)
	for i := 0; i < 4; i++ {
		isCut[i] = true
	}
	nb := chainNeighbors(n, 4, map[int][]int{0: {4}, 4: {0}})

	if interior := ClassifyCutFaceInterior(nb, isCut, 10); interior != nil {
		t.Errorf("expected nil when band covers the whole cut face, got %v", interior)
	}
}
