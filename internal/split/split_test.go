package split

import (
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// makeUnitCube builds a closed watertight unit cube spanning [0,1]^3
// with 12 triangles (2 per face). All faces are CCW from the outside.
func makeUnitCube() *loader.LoadedModel {
	v := [][3]float32{
		{0, 0, 0}, // 0
		{1, 0, 0}, // 1
		{1, 1, 0}, // 2
		{0, 1, 0}, // 3
		{0, 0, 1}, // 4
		{1, 0, 1}, // 5
		{1, 1, 1}, // 6
		{0, 1, 1}, // 7
	}
	f := [][3]uint32{
		// bottom (z=0), normal -z
		{0, 2, 1}, {0, 3, 2},
		// top (z=1), normal +z
		{4, 5, 6}, {4, 6, 7},
		// y=0, normal -y
		{0, 1, 5}, {0, 5, 4},
		// y=1, normal +y
		{2, 3, 7}, {2, 7, 6},
		// x=0, normal -x
		{0, 4, 7}, {0, 7, 3},
		// x=1, normal +x
		{1, 2, 6}, {1, 6, 5},
	}
	return &loader.LoadedModel{
		Vertices: v,
		Faces:    f,
	}
}

// makeIcosphere returns a unit-radius icosphere centred at the origin
// with `subdiv` levels of subdivision (subdiv=0 is the base
// icosahedron, ≈20 faces; subdiv=2 is ≈320 faces). Always closed and
// watertight.
func makeIcosphere(subdiv int) *loader.LoadedModel {
	t := float32((1 + math.Sqrt(5)) / 2)
	verts := [][3]float32{
		{-1, t, 0}, {1, t, 0}, {-1, -t, 0}, {1, -t, 0},
		{0, -1, t}, {0, 1, t}, {0, -1, -t}, {0, 1, -t},
		{t, 0, -1}, {t, 0, 1}, {-t, 0, -1}, {-t, 0, 1},
	}
	for i := range verts {
		x, y, z := float64(verts[i][0]), float64(verts[i][1]), float64(verts[i][2])
		l := math.Sqrt(x*x + y*y + z*z)
		verts[i] = [3]float32{float32(x / l), float32(y / l), float32(z / l)}
	}
	faces := [][3]uint32{
		{0, 11, 5}, {0, 5, 1}, {0, 1, 7}, {0, 7, 10}, {0, 10, 11},
		{1, 5, 9}, {5, 11, 4}, {11, 10, 2}, {10, 7, 6}, {7, 1, 8},
		{3, 9, 4}, {3, 4, 2}, {3, 2, 6}, {3, 6, 8}, {3, 8, 9},
		{4, 9, 5}, {2, 4, 11}, {6, 2, 10}, {8, 6, 7}, {9, 8, 1},
	}
	for s := 0; s < subdiv; s++ {
		mid := make(map[uint64]uint32)
		midpoint := func(a, b uint32) uint32 {
			lo, hi := a, b
			if lo > hi {
				lo, hi = hi, lo
			}
			key := uint64(lo)<<32 | uint64(hi)
			if idx, ok := mid[key]; ok {
				return idx
			}
			va, vb := verts[a], verts[b]
			m := [3]float32{
				(va[0] + vb[0]) / 2,
				(va[1] + vb[1]) / 2,
				(va[2] + vb[2]) / 2,
			}
			x, y, z := float64(m[0]), float64(m[1]), float64(m[2])
			l := math.Sqrt(x*x + y*y + z*z)
			m = [3]float32{float32(x / l), float32(y / l), float32(z / l)}
			idx := uint32(len(verts))
			verts = append(verts, m)
			mid[key] = idx
			return idx
		}
		var newFaces [][3]uint32
		for _, f := range faces {
			a := midpoint(f[0], f[1])
			b := midpoint(f[1], f[2])
			c := midpoint(f[2], f[0])
			newFaces = append(newFaces,
				[3]uint32{f[0], a, c},
				[3]uint32{f[1], b, a},
				[3]uint32{f[2], c, b},
				[3]uint32{a, b, c},
			)
		}
		faces = newFaces
	}
	return &loader.LoadedModel{Vertices: verts, Faces: faces}
}

// edgeKey32 is a small undirected edge key used by the watertight check.
type edgeKey32 struct{ a, b uint32 }

func edgeOf(a, b uint32) edgeKey32 {
	if a < b {
		return edgeKey32{a, b}
	}
	return edgeKey32{b, a}
}

// assertWatertight verifies every edge of model.Faces has exactly two
// incident faces. Returns the count of non-2 edges (0 = watertight).
func assertWatertight(t *testing.T, model *loader.LoadedModel, name string) {
	t.Helper()
	counts := make(map[edgeKey32]int)
	for _, f := range model.Faces {
		counts[edgeOf(f[0], f[1])]++
		counts[edgeOf(f[1], f[2])]++
		counts[edgeOf(f[2], f[0])]++
	}
	bad := 0
	for k, c := range counts {
		if c != 2 {
			if bad < 5 {
				t.Errorf("%s: edge %v has %d incident faces, want 2", name, k, c)
			}
			bad++
		}
	}
	if bad > 0 {
		t.Fatalf("%s: %d edges are non-manifold", name, bad)
	}
}

// closedMeshVolume returns the signed volume enclosed by a closed
// triangle mesh, using the divergence theorem (sum of tetrahedron
// volumes from origin). Positive when the mesh winds CCW from outside.
func closedMeshVolume(m *loader.LoadedModel) float64 {
	var v float64
	for _, f := range m.Faces {
		a := m.Vertices[f[0]]
		b := m.Vertices[f[1]]
		c := m.Vertices[f[2]]
		v += float64(a[0])*(float64(b[1])*float64(c[2])-float64(b[2])*float64(c[1])) -
			float64(a[1])*(float64(b[0])*float64(c[2])-float64(b[2])*float64(c[0])) +
			float64(a[2])*(float64(b[0])*float64(c[1])-float64(b[1])*float64(c[0]))
	}
	return v / 6
}

// surfaceArea returns the total surface area of a triangle mesh.
func surfaceArea(m *loader.LoadedModel) float64 {
	var a float64
	for _, f := range m.Faces {
		p := m.Vertices[f[0]]
		q := m.Vertices[f[1]]
		r := m.Vertices[f[2]]
		ux := float64(q[0] - p[0])
		uy := float64(q[1] - p[1])
		uz := float64(q[2] - p[2])
		vx := float64(r[0] - p[0])
		vy := float64(r[1] - p[1])
		vz := float64(r[2] - p[2])
		nx := uy*vz - uz*vy
		ny := uz*vx - ux*vz
		nz := ux*vy - uy*vx
		a += 0.5 * math.Sqrt(nx*nx+ny*ny+nz*nz)
	}
	return a
}

func TestCut_UnitCubeAtMidplane(t *testing.T) {
	cube := makeUnitCube()
	res, err := Cut(cube, AxisPlane(2, 0.5))
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	for h := 0; h < 2; h++ {
		assertWatertight(t, res.Halves[h], "half "+string(rune('0'+h)))
	}
	for h := 0; h < 2; h++ {
		v := closedMeshVolume(res.Halves[h])
		if math.Abs(math.Abs(v)-0.5) > 1e-5 {
			t.Errorf("half %d: |volume|=%g, want 0.5", h, math.Abs(v))
		}
		if len(res.CapFaces[h]) < 2 {
			t.Errorf("half %d: cap has %d faces, want >=2", h, len(res.CapFaces[h]))
		}
	}
}

func TestCut_SphereAtEquator(t *testing.T) {
	sphere := makeIcosphere(2)
	areaBefore := surfaceArea(sphere)
	// Cut slightly off the equator: subdividing the icosahedron lands
	// many vertices exactly on z=0, and Cut requires no on-plane
	// vertices.
	res, err := Cut(sphere, AxisPlane(2, 0.01))
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	for h := 0; h < 2; h++ {
		assertWatertight(t, res.Halves[h], "hemisphere "+string(rune('0'+h)))
	}
	areaAfter := surfaceArea(res.Halves[0]) + surfaceArea(res.Halves[1])
	// areaAfter is original sphere area + 2× cap area (both halves
	// have the same cap polygon). The cap's area is roughly π for a
	// unit sphere cut at the equator.
	expected := areaBefore + 2*math.Pi
	if math.Abs(areaAfter-expected)/expected > 0.05 {
		t.Errorf("sphere area after cut = %g, want ≈ %g (5%% tol)", areaAfter, expected)
	}
}

func TestCut_TangentPlaneFails(t *testing.T) {
	cube := makeUnitCube()
	// z=1 hits the top face exactly: vertices on that face have side==0,
	// rest have side<0. No cut polygon, no cap.
	_, err := Cut(cube, AxisPlane(2, 1))
	if err == nil {
		t.Fatal("Cut: expected error for tangent plane, got nil")
	}
}

func TestCut_MissingMeshFails(t *testing.T) {
	cube := makeUnitCube()
	_, err := Cut(cube, AxisPlane(2, 10))
	if err == nil {
		t.Fatal("Cut: expected error for plane that misses the mesh")
	}
}

func TestCut_NonUnitNormalFails(t *testing.T) {
	cube := makeUnitCube()
	_, err := Cut(cube, Plane{Normal: [3]float64{2, 0, 0}, D: 0.5})
	if err == nil {
		t.Fatal("Cut: expected error for non-unit normal")
	}
}

func TestCut_PreservesUVsAcrossSplit(t *testing.T) {
	cube := makeUnitCube()
	// Add UVs: u = x, v = y (so any midpoint at z=0.5 should have UV
	// equal to the linear interp of its endpoints' (x,y)).
	cube.UVs = make([][2]float32, len(cube.Vertices))
	for i, p := range cube.Vertices {
		cube.UVs[i] = [2]float32{p[0], p[1]}
	}
	res, err := Cut(cube, AxisPlane(2, 0.5))
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	// Every vertex in each half whose Z is near 0.5 (a midpoint) must
	// have UV ≈ (x, y).
	for h := 0; h < 2; h++ {
		half := res.Halves[h]
		if half.UVs == nil {
			t.Fatalf("half %d: UVs is nil, expected non-nil", h)
		}
		if len(half.UVs) != len(half.Vertices) {
			t.Fatalf("half %d: len(UVs)=%d, len(Vertices)=%d", h, len(half.UVs), len(half.Vertices))
		}
		for i, v := range half.Vertices {
			if math.Abs(float64(v[2])-0.5) < 1e-5 {
				gotU, gotV := half.UVs[i][0], half.UVs[i][1]
				if math.Abs(float64(gotU-v[0])) > 1e-5 || math.Abs(float64(gotV-v[1])) > 1e-5 {
					t.Errorf("half %d vertex %d: UV=(%g,%g), want (%g,%g)",
						h, i, gotU, gotV, v[0], v[1])
				}
			}
		}
	}
}

// makeHollowCube returns a cube of side 2 (centred at origin) with an
// internal cube cavity of side 1 (also centred). The inner cube's
// faces are wound INVERTED so the combined mesh remains watertight
// with a closed cavity inside.
func makeHollowCube() *loader.LoadedModel {
	outer := func(s float32) ([][3]float32, [][3]uint32) {
		v := [][3]float32{
			{-s, -s, -s}, {s, -s, -s}, {s, s, -s}, {-s, s, -s},
			{-s, -s, s}, {s, -s, s}, {s, s, s}, {-s, s, s},
		}
		f := [][3]uint32{
			{0, 2, 1}, {0, 3, 2}, // -z
			{4, 5, 6}, {4, 6, 7}, // +z
			{0, 1, 5}, {0, 5, 4}, // -y
			{2, 3, 7}, {2, 7, 6}, // +y
			{0, 4, 7}, {0, 7, 3}, // -x
			{1, 2, 6}, {1, 6, 5}, // +x
		}
		return v, f
	}
	innerFlipped := func(s float32) ([][3]float32, [][3]uint32) {
		v, f := outer(s)
		// Flip winding so the inner surface's normal points inward
		// (creating an enclosed void).
		for i := range f {
			f[i][1], f[i][2] = f[i][2], f[i][1]
		}
		return v, f
	}
	ov, of := outer(1)
	iv, ifaces := innerFlipped(0.25)
	offset := uint32(len(ov))
	for i := range ifaces {
		ifaces[i][0] += offset
		ifaces[i][1] += offset
		ifaces[i][2] += offset
	}
	return &loader.LoadedModel{
		Vertices: append(ov, iv...),
		Faces:    append(of, ifaces...),
	}
}

// TestCut_OnPlaneVertexFails verifies that a cut passing exactly
// through a vertex of the model is rejected. The user-facing remedy
// (offset the cut slightly) lives in the error message.
func TestCut_OnPlaneVertexFails(t *testing.T) {
	cube := makeUnitCube()
	// z=0 hits all four bottom-face vertices.
	_, err := Cut(cube, AxisPlane(2, 0))
	if err == nil {
		t.Fatal("expected error when cut plane passes through model vertices")
	}
}

// TestCut_CapFacesLieOnPlane checks that every cap-face vertex lies
// within epsilon of the cut plane. A cap that bulges off the plane
// would silently break the watertight contract for downstream stages.
func TestCut_CapFacesLieOnPlane(t *testing.T) {
	cube := makeUnitCube()
	res, err := Cut(cube, AxisPlane(2, 0.5))
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	for h := 0; h < 2; h++ {
		half := res.Halves[h]
		seen := make(map[uint32]bool)
		for _, fi := range res.CapFaces[h] {
			f := half.Faces[fi]
			for _, vi := range f {
				if seen[vi] {
					continue
				}
				seen[vi] = true
				v := half.Vertices[vi]
				if math.Abs(float64(v[2])-0.5) > 1e-5 {
					t.Errorf("half %d cap face %d vertex %d at z=%g, want z≈0.5", h, fi, vi, v[2])
				}
			}
		}
	}
}

// TestCut_PreservesVertexColors covers the lerpU8 path. Mid-cut
// midpoint vertices should have a vertex color that's the linear
// interpolation of their two source endpoints' colors.
func TestCut_PreservesVertexColors(t *testing.T) {
	cube := makeUnitCube()
	cube.VertexColors = make([][4]uint8, len(cube.Vertices))
	// Bottom (z=0) vertices red, top (z=1) vertices blue.
	for i, p := range cube.Vertices {
		if p[2] < 0.5 {
			cube.VertexColors[i] = [4]uint8{255, 0, 0, 255}
		} else {
			cube.VertexColors[i] = [4]uint8{0, 0, 255, 255}
		}
	}
	res, err := Cut(cube, AxisPlane(2, 0.5))
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	// Every midpoint vertex (Z ≈ 0.5) should have color (127.5, 0,
	// 127.5, 255) — give or take rounding.
	for h := 0; h < 2; h++ {
		half := res.Halves[h]
		if half.VertexColors == nil {
			t.Fatalf("half %d: VertexColors is nil", h)
		}
		for i, v := range half.Vertices {
			if math.Abs(float64(v[2])-0.5) < 1e-5 {
				c := half.VertexColors[i]
				if c[0] < 120 || c[0] > 135 || c[1] != 0 || c[2] < 120 || c[2] > 135 || c[3] != 255 {
					t.Errorf("half %d midpoint vertex %d: color %v, want ≈(127, 0, 128, 255)", h, i, c)
				}
			}
		}
	}
}

// TestCut_MultiComponentRejected covers the non-nested two-component
// case (a barbell-like shape where one cut plane catches both lobes).
// Phase 1 doesn't support this; it must produce a clear error.
func TestCut_MultiComponentRejected(t *testing.T) {
	// Build a barbell: two unit cubes at x=±2 connected by a thin
	// rectangular bar at y∈[-0.1,0.1], z∈[0.45,0.55]. Cut at x=0
	// (perpendicular to the bar) to bisect both — actually we want a
	// horizontal cut through both cube lobes that doesn't include
	// the bar. Simpler construction: two coaxial cubes spaced apart
	// in z, joined by a thin column. We cheat by using two
	// disconnected meshes — Phase 1 already rejects non-watertight
	// inputs in subtler ways, but Cut + cap should error out before
	// triangulation when the cut produces two separate loops.
	//
	// Concretely: build two unit cubes side by side at x=[0,1] and
	// x=[2,3], connected by a thin neck at y∈[0.4,0.6], z∈[0.4,0.6]
	// from x=1 to x=2. Cut horizontally at z=0.5 catches both cubes
	// (loops outside the neck cross-section) and the neck (loop
	// inside it). The two cube cross-sections are non-nested.
	//
	// Building this is fiddly; for now use two disconnected unit
	// cubes (not watertight as one mesh, but topologically two
	// closed components). The Cut will produce two independent cap
	// loops, neither inside the other.
	cube1 := makeUnitCube()
	cube2v := make([][3]float32, len(cube1.Vertices))
	for i, p := range cube1.Vertices {
		cube2v[i] = [3]float32{p[0] + 3, p[1], p[2]}
	}
	cube2f := make([][3]uint32, len(cube1.Faces))
	off := uint32(len(cube1.Vertices))
	for i, f := range cube1.Faces {
		cube2f[i] = [3]uint32{f[0] + off, f[1] + off, f[2] + off}
	}
	pair := &loader.LoadedModel{
		Vertices: append(cube1.Vertices, cube2v...),
		Faces:    append(cube1.Faces, cube2f...),
	}
	_, err := Cut(pair, AxisPlane(2, 0.5))
	if err == nil {
		t.Fatal("expected error for non-nested multi-component cut")
	}
}

func TestCut_PolygonWithHoles(t *testing.T) {
	hollow := makeHollowCube()
	// Cut at z=0.1 (off-axis to avoid degenerate alignment with face
	// boundaries of the inner cube).
	res, err := Cut(hollow, AxisPlane(2, 0.1))
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	for h := 0; h < 2; h++ {
		assertWatertight(t, res.Halves[h], "hollow half "+string(rune('0'+h)))
	}
	// Each half's volume = (2×2×2 outer half - 0.5×0.5×0.5 inner half).
	// Outer cube volume of side-2 cut at z=0.1 yields halves of
	// volumes 2×2×1.1 = 4.4 and 2×2×0.9 = 3.6. Inner cube volume of
	// side-0.5 cut at z=0.1 yields halves of 0.5×0.5×0.35 = 0.0875
	// and 0.5×0.5×0.15 = 0.0375.
	// So expected enclosed volumes:
	//   half 0 (z<0.1): 4.4 - 0.0875 = 4.3125
	//   half 1 (z>0.1): 3.6 - 0.0375 = 3.5625
	expectedVol := []float64{4.3125, 3.5625}
	for h := 0; h < 2; h++ {
		v := math.Abs(closedMeshVolume(res.Halves[h]))
		if math.Abs(v-expectedVol[h]) > 0.01 {
			t.Errorf("hollow half %d: volume = %g, want ≈ %g", h, v, expectedVol[h])
		}
	}
}
