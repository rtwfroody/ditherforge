package voxel

import (
	"context"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/progress"
)

// gridQuadsDuplicated builds an n×n grid of unit cells on the z=0 plane,
// each cell emitted as its OWN quad (2 triangles) with its own 4 corner
// vertices. Vertices shared between adjacent cells therefore have
// distinct indices — exactly how the cellslicer clip concatenates
// per-cell meshes (offset vertex indices, coincident positions at the
// shared boundaries). All faces are coplanar (+z normal) and share one
// material, so a correct merge collapses the whole grid into one polygon.
func gridQuadsDuplicated(n int, color int32) (verts [][3]float32, faces [][3]uint32, assign []int32) {
	for j := 0; j < n; j++ {
		for i := 0; i < n; i++ {
			x0, y0 := float32(i), float32(j)
			x1, y1 := float32(i+1), float32(j+1)
			base := uint32(len(verts))
			verts = append(verts,
				[3]float32{x0, y0, 0},
				[3]float32{x1, y0, 0},
				[3]float32{x1, y1, 0},
				[3]float32{x0, y1, 0},
			)
			// CCW winding → +z normal.
			faces = append(faces,
				[3]uint32{base, base + 1, base + 2},
				[3]uint32{base, base + 2, base + 3},
			)
			assign = append(assign, color, color)
		}
	}
	return
}

// TestMergeCoplanar_MergesAcrossDuplicatedCellBoundary is the regression
// test for the cellslicer-clip no-op: with coincident cell-boundary
// vertices at distinct indices, the index-keyed adjacency used to treat
// every cell as an island and merge nothing (face count unchanged). After
// welding by position the whole coplanar grid is one group and collapses.
func TestMergeCoplanar_MergesAcrossDuplicatedCellBoundary(t *testing.T) {
	const n = 4
	verts, faces, assign := gridQuadsDuplicated(n, 7)

	mv, mf, ma, err := MergeCoplanarTriangles(context.Background(), verts, faces, assign, progress.NullTracker{})
	if err != nil {
		t.Fatalf("MergeCoplanarTriangles: %v", err)
	}

	if len(mf) >= len(faces) {
		t.Fatalf("merge did not reduce face count: %d -> %d (the welding fix is not taking effect)", len(faces), len(mf))
	}
	// A solid n×n square's perimeter has 4n vertices (corners + collinear
	// edge points), so ear clipping yields 4n-2 triangles — far fewer than
	// the 2n² we started with.
	if want := 4*n - 2; len(mf) > want {
		t.Errorf("expected merged grid to collapse to <= %d triangles, got %d", want, len(mf))
	}
	// Faces and assignments stay parallel; every face indexes the welded
	// vertex table; color is unchanged (single material in → single out).
	if len(mf) != len(ma) {
		t.Fatalf("faces/assignments out of sync: %d vs %d", len(mf), len(ma))
	}
	for fi, f := range mf {
		for _, vi := range f {
			if int(vi) >= len(mv) {
				t.Fatalf("face %d references vert %d out of range (verts=%d)", fi, vi, len(mv))
			}
		}
		if ma[fi] != 7 {
			t.Errorf("face %d color changed: got %d, want 7", fi, ma[fi])
		}
	}
}

// appendFaceGrid appends an n×n grid of unit-cell quads spanning the
// square at origin O with in-plane step vectors u and v (each length 1),
// each cell its own duplicated quad. Winding is CCW so the outward normal
// is u×v; pick u,v per cube face accordingly.
func appendFaceGrid(verts *[][3]float32, faces *[][3]uint32, assign *[]int32,
	O, u, v [3]float32, n int, color int32) {
	at := func(i, j int) [3]float32 {
		return [3]float32{
			O[0] + float32(i)*u[0] + float32(j)*v[0],
			O[1] + float32(i)*u[1] + float32(j)*v[1],
			O[2] + float32(i)*u[2] + float32(j)*v[2],
		}
	}
	for j := 0; j < n; j++ {
		for i := 0; i < n; i++ {
			base := uint32(len(*verts))
			*verts = append(*verts, at(i, j), at(i+1, j), at(i+1, j+1), at(i, j+1))
			*faces = append(*faces,
				[3]uint32{base, base + 1, base + 2},
				[3]uint32{base, base + 2, base + 3},
			)
			*assign = append(*assign, color, color)
		}
	}
}

// TestMergeCoplanar_WatertightCube builds a closed cube whose six faces
// are each an n×n grid of independently-vertexed cells (coincident at the
// cube edges/corners, distinct indices) — the clip-output shape. The
// merge must (a) reduce the triangle count and (b) leave the mesh
// watertight: welding the coincident seam vertices to shared indices is
// what makes CheckWatertight (which keys edges by index) pass. Each face
// gets its own color to confirm merging never smears colors across faces.
func TestMergeCoplanar_WatertightCube(t *testing.T) {
	const n = 3
	var (
		verts  [][3]float32
		faces  [][3]uint32
		assign []int32
	)
	x := func(a, b, c float32) [3]float32 { return [3]float32{a, b, c} }
	N := float32(n)
	// O, u, v chosen so u×v = outward normal for each face.
	appendFaceGrid(&verts, &faces, &assign, x(0, 0, N), x(1, 0, 0), x(0, 1, 0), n, 0) // top   +z
	appendFaceGrid(&verts, &faces, &assign, x(0, 0, 0), x(0, 1, 0), x(1, 0, 0), n, 1) // bottom -z
	appendFaceGrid(&verts, &faces, &assign, x(N, 0, 0), x(0, 1, 0), x(0, 0, 1), n, 2) // +x
	appendFaceGrid(&verts, &faces, &assign, x(0, 0, 0), x(0, 0, 1), x(0, 1, 0), n, 3) // -x
	appendFaceGrid(&verts, &faces, &assign, x(0, N, 0), x(0, 0, 1), x(1, 0, 0), n, 4) // +y
	appendFaceGrid(&verts, &faces, &assign, x(0, 0, 0), x(1, 0, 0), x(0, 0, 1), n, 5) // -y

	// The raw mesh is closed by POSITION but not by index — the inter-face
	// seams are distinct indices, so it is not watertight before welding.
	if CheckWatertight(faces).IsWatertight() {
		t.Fatal("test precondition broken: raw duplicated-vertex cube is unexpectedly index-watertight")
	}

	mv, mf, ma, err := MergeCoplanarTriangles(context.Background(), verts, faces, assign, progress.NullTracker{})
	if err != nil {
		t.Fatalf("MergeCoplanarTriangles: %v", err)
	}

	if len(mf) >= len(faces) {
		t.Errorf("merge did not reduce face count: %d -> %d", len(faces), len(mf))
	}
	if wr := CheckWatertight(mf); !wr.IsWatertight() {
		t.Errorf("merged cube is not watertight: %s", wr)
	}
	// Colors: all six originals still present, none invented.
	seen := map[int32]bool{}
	for fi, f := range mf {
		for _, vi := range f {
			if int(vi) >= len(mv) {
				t.Fatalf("face %d references vert %d out of range (verts=%d)", fi, vi, len(mv))
			}
		}
		seen[ma[fi]] = true
		if ma[fi] < 0 || ma[fi] > 5 {
			t.Errorf("face %d has color %d outside the input set 0..5", fi, ma[fi])
		}
	}
	for c := int32(0); c <= 5; c++ {
		if !seen[c] {
			t.Errorf("color %d disappeared from merged output", c)
		}
	}
}
