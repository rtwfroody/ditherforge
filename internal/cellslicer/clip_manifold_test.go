package cellslicer

import (
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// cubeModel returns an edge×edge×edge cube centred at (h,h,h) so
// min-corner sits at origin — matches the cellslicer convention of
// normalising bottom Z to 0 and keeps XY positive for convenient cell
// bookkeeping.
func cubeModel(edge float32) *loader.LoadedModel {
	h := edge / 2
	v := [][3]float32{
		{0, 0, 0}, {edge, 0, 0}, {edge, edge, 0}, {0, edge, 0},
		{0, 0, edge}, {edge, 0, edge}, {edge, edge, edge}, {0, edge, edge},
	}
	_ = h
	f := [][3]uint32{
		{0, 2, 1}, {0, 3, 2},
		{4, 5, 6}, {4, 6, 7},
		{0, 1, 5}, {0, 5, 4},
		{2, 3, 7}, {2, 7, 6},
		{0, 4, 7}, {0, 7, 3},
		{1, 2, 6}, {1, 6, 5},
	}
	return &loader.LoadedModel{Vertices: v, Faces: f}
}

// makeCubeSlab builds a single Slab with a single closed Cell over
// the full [0..edge] × [0..edge] XY region between zBot and zTop.
// All four cell edges are closed (no open boundary).
func makeCubeSlab(edge, zBot, zTop float32) Slab {
	outer := []Point2{{0, 0}, {edge, 0}, {edge, edge}, {0, edge}}
	return Slab{
		ZBot:  zBot,
		ZTop:  zTop,
		Cells: []Cell{{Outer: outer, Kind: KindHex}},
	}
}

func TestClipMeshToCellsManifoldClosedCube(t *testing.T) {
	// 10 mm cube, single cell covering the whole footprint, single
	// 2 mm slab in the middle (z=4..6). Expected output: SURFACE
	// fragments only — the four vertical strips of the cube that fall
	// inside the slab. NOT a closed box: the prism's z=4 and z=6 cap
	// faces are filtered out via run_original_id, so the result has
	// open boundary edges at those Zs (this is intentional and
	// preserves the "cells must be surface-only" invariant).
	model := cubeModel(10)
	slab := makeCubeSlab(10, 4, 6)
	slabs := []Slab{slab}

	cr, err := ClipMeshToCellsManifold(model, slabs, nil, 1.0)
	if err != nil {
		t.Fatalf("ClipMeshToCellsManifold: %v", err)
	}
	if len(cr.Faces) == 0 {
		t.Fatal("ClipMeshToCellsManifold returned 0 faces")
	}
	if len(cr.FaceCellIdx) != len(cr.Faces) {
		t.Errorf("FaceCellIdx len=%d != Faces len=%d", len(cr.FaceCellIdx), len(cr.Faces))
	}
	// All faces should map to the single cell at global index 0.
	for _, idx := range cr.FaceCellIdx {
		if idx != 0 {
			t.Errorf("FaceCellIdx contains %d, want 0", idx)
		}
	}
	// Surface-only: every vertex must sit on the cube's outer surface
	// inside the slab's Z range. So z ∈ [4-eps, 6+eps] AND at least
	// one of x ∈ {0,10} or y ∈ {0,10} (it's on a cube side wall).
	const eps = 1e-4
	for _, v := range cr.Verts {
		if v[2] < 4-eps || v[2] > 6+eps {
			t.Errorf("surface-only: vertex z=%v outside slab [4..6]", v[2])
		}
		onWall := absf(v[0]-0) < eps || absf(v[0]-10) < eps ||
			absf(v[1]-0) < eps || absf(v[1]-10) < eps
		if !onWall {
			t.Errorf("surface-only: vertex %v not on a cube side wall (x/y in {0,10})", v)
		}
	}
	// Stronger: the four wall strips should sum to exactly
	// 4 sides × 10 mm × 2 mm = 80 mm². A regression that silently
	// drops half the source faces (e.g. a future change to
	// ToMeshFiltered) would slip past the per-vertex check above
	// but fail this one.
	area := triMeshArea(cr.Verts, cr.Faces)
	if math.Abs(area-80) > 0.01 {
		t.Errorf("surface area = %.4f mm², want 80 (4 sides × 10 × 2)", area)
	}
}

// triMeshArea sums the unsigned area of every triangle in (verts,
// faces). Used by surface-only tests to assert the output IS the
// expected surface, not just that it has vertices in the right
// places.
func triMeshArea(verts [][3]float32, faces [][3]uint32) float64 {
	var sum float64
	for _, f := range faces {
		a, b, c := verts[f[0]], verts[f[1]], verts[f[2]]
		ux, uy, uz := float64(b[0]-a[0]), float64(b[1]-a[1]), float64(b[2]-a[2])
		vx, vy, vz := float64(c[0]-a[0]), float64(c[1]-a[1]), float64(c[2]-a[2])
		cx := uy*vz - uz*vy
		cy := uz*vx - ux*vz
		cz := ux*vy - uy*vx
		sum += 0.5 * math.Sqrt(cx*cx+cy*cy+cz*cz)
	}
	return sum
}

func TestClipMeshToCellsManifoldFourCells(t *testing.T) {
	// 10 mm cube, 2×2 cell grid, single 2 mm slab. Each cell should
	// get a 5×5×2 = 50 mm³ piece of the cube's slab section.
	model := cubeModel(10)
	cells := []Cell{
		{Outer: []Point2{{0, 0}, {5, 0}, {5, 5}, {0, 5}}, Kind: KindHex},
		{Outer: []Point2{{5, 0}, {10, 0}, {10, 5}, {5, 5}}, Kind: KindHex},
		{Outer: []Point2{{0, 5}, {5, 5}, {5, 10}, {0, 10}}, Kind: KindHex},
		{Outer: []Point2{{5, 5}, {10, 5}, {10, 10}, {5, 10}}, Kind: KindHex},
	}
	slabs := []Slab{{ZBot: 4, ZTop: 6, Cells: cells}}

	cr, err := ClipMeshToCellsManifold(model, slabs, nil, 1.0)
	if err != nil {
		t.Fatalf("ClipMeshToCellsManifold: %v", err)
	}
	if len(cr.Faces) == 0 {
		t.Fatal("0 faces")
	}
	// Every cell must contribute faces. Count by cell index.
	cellHasFaces := make(map[int32]bool)
	for _, idx := range cr.FaceCellIdx {
		cellHasFaces[idx] = true
	}
	for i := int32(0); i < 4; i++ {
		if !cellHasFaces[i] {
			t.Errorf("cell %d had no output faces", i)
		}
	}
	// Same total surface area as the single-cell case: the four
	// quarter-cells tile the same 4 × 10×2 wall strips.
	area := triMeshArea(cr.Verts, cr.Faces)
	if math.Abs(area-80) > 0.01 {
		t.Errorf("total surface area = %.4f mm², want 80", area)
	}
}

func TestBloatOpenEdgesNoFlagsIsIdentity(t *testing.T) {
	outer := []Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	got := bloatOpenEdges(outer, nil, 5)
	if len(got) != 4 {
		t.Fatalf("nil flags: got %d points, want 4", len(got))
	}
	for i := range got {
		if got[i][0] != outer[i][0] || got[i][1] != outer[i][1] {
			t.Errorf("nil flags vert %d: got %v, want %v", i, got[i], outer[i])
		}
	}
	// Defensive copy: callers must not be able to alias cell.Outer
	// through the returned slice (else mutating the prism polygon
	// would silently corrupt the partition data).
	got[0][0] = 999
	if outer[0][0] == 999 {
		t.Errorf("nil-flags result aliased outer; mutation leaked back")
	}
}

func TestBloatOpenEdgesAllClosed(t *testing.T) {
	outer := []Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	flags := []bool{false, false, false, false}
	got := bloatOpenEdges(outer, flags, 5)
	if len(got) != 4 {
		t.Fatalf("all closed: got %d points, want 4", len(got))
	}
}

func TestBloatOpenEdgesSingleOpen(t *testing.T) {
	// 10×10 square, edge 0 (bottom: (0,0)→(10,0)) is open. Outward
	// normal for CCW orientation = (0, -1). Bloat 5 mm pushes vertices
	// 0 and 1 down by 5 mm. The 10mm cell bbox max-side > 5, so the
	// per-cell cap doesn't kick in.
	outer := []Point2{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	flags := []bool{true, false, false, false}
	got := bloatOpenEdges(outer, flags, 5)
	want := [][2]float32{{0, 0}, {0, -5}, {10, -5}, {10, 0}, {10, 10}, {0, 10}}
	if len(got) != len(want) {
		t.Fatalf("single-open: got %d points, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if math.Abs(float64(got[i][0]-want[i][0])) > 1e-5 ||
			math.Abs(float64(got[i][1]-want[i][1])) > 1e-5 {
			t.Errorf("single-open vert %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestBloatOpenEdgesCappedOnThinCell(t *testing.T) {
	// 1×1 cell, right edge open, raw bloat 5mm. The per-cell cap
	// pins bloat at the cell's bbox max-side (1mm), guaranteeing the
	// bloated polygon stays simple even for future non-convex cell
	// shapes.
	outer := []Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	flags := []bool{false, true, false, false} // right edge (x=1) open
	got := bloatOpenEdges(outer, flags, 5)
	// Cap pins bloat at 1mm. Walking i=0,1,2,3:
	//   v0: closed→closed → (0,0)
	//   v1: closed→open   → (1,0), (2,0)
	//   v2: open→closed   → (2,1), (1,1)
	//   v3: closed→closed → (0,1)
	want := [][2]float32{{0, 0}, {1, 0}, {2, 0}, {2, 1}, {1, 1}, {0, 1}}
	if len(got) != len(want) {
		t.Fatalf("thin-cell cap: got %d points (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if math.Abs(float64(got[i][0]-want[i][0])) > 1e-5 ||
			math.Abs(float64(got[i][1]-want[i][1])) > 1e-5 {
			t.Errorf("thin-cell cap vert %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestBloatOpenEdgesMiterOnCornerRun(t *testing.T) {
	// 10×10 square, two adjacent edges open (right + top). The
	// corner between them is at v2=(10,10), an open-run interior
	// vertex. The two outward normals (right=(1,0), up=(0,1)) meet
	// at 90°. Miter offset = (n1+n2) / (1 + n1·n2) × bloat =
	// (1,1) / 1 × 5 = (5,5). Corner should move to (15,15) — NOT
	// (12.5,12.5) which is what naive averaging would produce.
	outer := []Point2{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	flags := []bool{false, true, true, false}
	got := bloatOpenEdges(outer, flags, 5)
	// Find the bloated v2 — it's the interior-of-open-run vertex.
	// Walking i=0,1,2,3:
	//   v0 (0,0): closed→closed → (0,0)
	//   v1 (10,0): closed→open (right edge) → (10,0), (15,0)
	//   v2 (10,10): open→open (interior, miter) → (15,15)
	//   v3 (0,10): open→closed → (0,15), (0,10)
	want := [][2]float32{{0, 0}, {10, 0}, {15, 0}, {15, 15}, {0, 15}, {0, 10}}
	if len(got) != len(want) {
		t.Fatalf("miter: got %d points (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if math.Abs(float64(got[i][0]-want[i][0])) > 1e-4 ||
			math.Abs(float64(got[i][1]-want[i][1])) > 1e-4 {
			t.Errorf("miter vert %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestClipMeshToCellsManifoldOpenEdgeBloatsOutward(t *testing.T) {
	// 10 mm cube. One cell covers only [0..6, 0..10], with the
	// rightmost edge marked open. Bloat at 5×cellSize = 5×1 = 5 mm
	// extends the right edge past x=11, beyond the cube — the result
	// should be the full right half of the slab (x=0..10), not just
	// x=0..6, because the open edge pulls the missing strip in.
	model := cubeModel(10)
	cell := Cell{
		Outer:         []Point2{{0, 0}, {6, 0}, {6, 10}, {0, 10}},
		OuterEdgeOpen: []bool{false, true, false, false}, // edge 1: (6,0)→(6,10)
		Kind:          KindHex,
	}
	slabs := []Slab{{ZBot: 4, ZTop: 6, Cells: []Cell{cell}}}

	cr, err := ClipMeshToCellsManifold(model, slabs, nil, 1.0)
	if err != nil {
		t.Fatalf("ClipMeshToCellsManifold: %v", err)
	}
	if len(cr.Faces) == 0 {
		t.Fatal("0 faces returned")
	}
	// Verify the output reaches at least to x=10 (the cube's far
	// edge), confirming the open-edge bloat captured the full
	// 10×10×2 = 200 mm³ slab piece, not just the unbloated 6×10×2 =
	// 120 mm³ section.
	maxX := float32(0)
	for _, v := range cr.Verts {
		if v[0] > maxX {
			maxX = v[0]
		}
	}
	if maxX < 9.99 {
		t.Errorf("open-edge bloat: maxX=%.3f, want ≥10 (cube far edge)", maxX)
	}
}
