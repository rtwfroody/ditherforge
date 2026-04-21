package voxel

import (
	"context"
	"image/color"
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// TestCGSolveSmallSPD verifies the CG solver on a tiny hand-checked system:
// [[ 2 -1  0 ]        [ 1 ]
//  [-1  2 -1 ]  x  =  [ 0 ]
//  [ 0 -1  2 ]]       [ 0 ]
// Exact solution: x = [0.75, 0.5, 0.25].
func TestCGSolveSmallSPD(t *testing.T) {
	diag := []float32{2, 2, 2}
	nbrs := [][]lapEntry{
		{{1, -1}},
		{{0, -1}, {2, -1}},
		{{1, -1}},
	}
	b := []float32{1, 0, 0}
	x0 := []float32{0, 0, 0}
	x := cgSolve(diag, nbrs, b, x0, 50)
	want := []float32{0.75, 0.5, 0.25}
	for i, v := range want {
		if math.Abs(float64(x[i]-v)) > 1e-5 {
			t.Errorf("x[%d]=%g want %g", i, x[i], v)
		}
	}
}

// TestArapFlatFanStaysPut: on a perfectly flat fan mesh where DEM already
// gives an isometric 2D layout, ARAP should be (near-)idempotent — every
// per-triangle rotation is identity and the solver's RHS equals L*uv so the
// UVs don't move.
func TestArapFlatFanStaysPut(t *testing.T) {
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 0},
			{10, 0, 0},
			{5, 8.66, 0},
			{-5, 8.66, 0},
			{-10, 0, 0},
			{-5, -8.66, 0},
			{5, -8.66, 0},
		},
		Faces: [][3]uint32{
			{0, 1, 2}, {0, 2, 3}, {0, 3, 4},
			{0, 4, 5}, {0, 5, 6}, {0, 6, 1},
		},
		FaceBaseColor: make([][4]uint8, 6),
	}

	vertUV := make(map[[3]float32][2]float32)
	for _, v := range model.Vertices {
		vertUV[SnapPos(v)] = [2]float32{v[0], v[1]}
	}

	accepted := []int32{0, 1, 2, 3, 4, 5}
	region := buildArapRegion(model, accepted, vertUV, 0)

	// Snapshot pre-ARAP UVs.
	before := make([][2]float32, region.nV)
	copy(before, region.uv)

	region.Solve(5, 30)

	for i, u := range region.uv {
		dx := u[0] - before[i][0]
		dy := u[1] - before[i][1]
		if math.Sqrt(float64(dx*dx+dy*dy)) > 1e-3 {
			t.Errorf("vertex %d drifted by (%g,%g) despite flat mesh", i, dx, dy)
		}
	}
}

// TestArapReducesDistortion: start with a deliberately compressed UV layout
// on a flat mesh (but keep seed pinned at truth) and confirm ARAP pulls the
// rest of the vertices toward the isometric positions.
func TestArapReducesDistortion(t *testing.T) {
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 0},
			{10, 0, 0},
			{5, 8.66, 0},
			{-5, 8.66, 0},
			{-10, 0, 0},
			{-5, -8.66, 0},
			{5, -8.66, 0},
		},
		Faces: [][3]uint32{
			{0, 1, 2}, {0, 2, 3}, {0, 3, 4},
			{0, 4, 5}, {0, 5, 6}, {0, 6, 1},
		},
		FaceBaseColor: make([][4]uint8, 6),
	}

	// Seed truth for pinning: vertices 0, 1, 2 get correct UVs.
	// All other vertices start compressed to ~10% of their true position.
	vertUV := make(map[[3]float32][2]float32)
	for i, v := range model.Vertices {
		if i <= 2 {
			vertUV[SnapPos(v)] = [2]float32{v[0], v[1]}
		} else {
			vertUV[SnapPos(v)] = [2]float32{v[0] * 0.1, v[1] * 0.1}
		}
	}

	accepted := []int32{0, 1, 2, 3, 4, 5}
	region := buildArapRegion(model, accepted, vertUV, 0)

	// Energy = sum over triangles & edges of w * |uvEdge - refEdge|^2.
	// (All rotations are identity on a flat mesh, so this is the residual.)
	energy := func() float32 {
		var e float32
		for ti, tri := range region.tris {
			ref := region.refX[ti]
			w := region.wTri[ti]
			// Local rotation (2D closed form).
			var m00, m01, m10, m11 float32
			for k := 0; k < 3; k++ {
				k1 := (k + 1) % 3
				rEdge := [2]float32{ref[k1][0] - ref[k][0], ref[k1][1] - ref[k][1]}
				uEdge := [2]float32{region.uv[tri[k1]][0] - region.uv[tri[k]][0],
					region.uv[tri[k1]][1] - region.uv[tri[k]][1]}
				m00 += w[k] * rEdge[0] * uEdge[0]
				m01 += w[k] * rEdge[0] * uEdge[1]
				m10 += w[k] * rEdge[1] * uEdge[0]
				m11 += w[k] * rEdge[1] * uEdge[1]
			}
			theta := math.Atan2(float64(m01-m10), float64(m00+m11))
			c := float32(math.Cos(theta))
			s := float32(math.Sin(theta))
			for k := 0; k < 3; k++ {
				k1 := (k + 1) % 3
				rEdge := [2]float32{ref[k1][0] - ref[k][0], ref[k1][1] - ref[k][1]}
				uEdge := [2]float32{region.uv[tri[k1]][0] - region.uv[tri[k]][0],
					region.uv[tri[k1]][1] - region.uv[tri[k]][1]}
				rotRX := c*rEdge[0] - s*rEdge[1]
				rotRY := s*rEdge[0] + c*rEdge[1]
				dx := uEdge[0] - rotRX
				dy := uEdge[1] - rotRY
				e += w[k] * (dx*dx + dy*dy)
			}
		}
		return e
	}

	e0 := energy()
	region.Solve(10, 50)
	e1 := energy()
	if e1 >= e0 {
		t.Fatalf("ARAP did not reduce energy: %g -> %g", e0, e1)
	}
	if e1 > e0*0.2 {
		t.Errorf("ARAP reduced energy only modestly: %g -> %g (want > 5× drop)", e0, e1)
	}
}

// TestBuildStickerDecalCoverageCylinderStrip: on a curved strip, the full
// pipeline (DEM + ARAP) should cover most of the strip rather than collapse
// to a small near-seed cluster (the naive-DEM failure mode).
func TestBuildStickerDecalCoverageCylinderStrip(t *testing.T) {
	// Build a half-cylinder strip: radius 20, height 30, 24 angular segments
	// over π radians. Arc length ≈ 20π ≈ 62.8, height 30 — so a square
	// sticker of scale 50 should cover ~80% of the strip's triangles.
	const R = float32(20)
	const H = float32(30)
	const NA = 24
	const NH = 8
	var verts [][3]float32
	vertAt := func(ai, hi int) uint32 {
		return uint32(hi*(NA+1) + ai)
	}
	for hi := 0; hi <= NH; hi++ {
		z := -H/2 + H*float32(hi)/NH
		for ai := 0; ai <= NA; ai++ {
			theta := math.Pi * float64(ai) / NA
			x := R * float32(math.Cos(theta))
			y := R * float32(math.Sin(theta))
			verts = append(verts, [3]float32{x, y, z})
		}
	}
	var faces [][3]uint32
	for hi := 0; hi < NH; hi++ {
		for ai := 0; ai < NA; ai++ {
			a := vertAt(ai, hi)
			b := vertAt(ai+1, hi)
			c := vertAt(ai+1, hi+1)
			d := vertAt(ai, hi+1)
			faces = append(faces, [3]uint32{a, b, c})
			faces = append(faces, [3]uint32{a, c, d})
		}
	}
	model := &loader.LoadedModel{
		Vertices:      verts,
		Faces:         faces,
		FaceBaseColor: make([][4]uint8, len(faces)),
	}
	adj := BuildTriAdjacency(model)
	img := makeTestImage(4, 4, color.NRGBA{255, 0, 0, 255})

	// Sticker centered on the middle of the strip (theta = π/2).
	center := [3]float64{0, float64(R), 0}
	normal := [3]float64{0, 1, 0}
	up := [3]float64{0, 0, 1}
	scale := 50.0

	si := NewSpatialIndex(model, 4)
	seed := FindSeedTriangle(center, model, si)
	if seed < 0 {
		t.Fatal("no seed triangle found")
	}

	decal, err := BuildStickerDecal(context.Background(), model, adj, img,
		seed, center, normal, up, scale, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Coverage check: expect at least 40% of mesh triangles to end up in
	// the decal. Naive DEM (no ARAP) collapses to a single-digit percentage
	// on this mesh; the old per-tri BFS achieved ~60-70%. 40% is a lenient
	// lower bound to keep the test stable across minor ARAP tuning.
	ratio := float64(len(decal.TriUVs)) / float64(len(faces))
	if ratio < 0.40 {
		t.Errorf("coverage %.1f%% too low (want ≥ 40%%); ARAP may not be relaxing the DEM layout",
			ratio*100)
	}
	t.Logf("coverage: %d / %d tris = %.1f%%", len(decal.TriUVs), len(faces), ratio*100)
}
