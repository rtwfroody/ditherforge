package split

import (
	"math"
	"os"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/cgalclip"
	"github.com/rtwfroody/ditherforge/internal/loader"
)

// TestMain skips every test in this package when the binary wasn't
// built with the cgal tag — Cut delegates to CGAL and there's no
// usable fallback. CI builds with `-tags cgal` so coverage is
// preserved; local builds without CGAL skip cleanly instead of
// failing every Cut-touching test.
func TestMain(m *testing.M) {
	if !cgalclip.HasCGAL {
		// Print a single skip notice and exit 0 so `go test ./...`
		// stays green on dev machines without CGAL installed.
		println("split tests skipped: cgal build tag not set")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

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
	res, err := Cut(cube, AxisPlane(2, 0.5), ConnectorSettings{})
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
	}
}

func TestCut_SphereAtEquator(t *testing.T) {
	sphere := makeIcosphere(2)
	areaBefore := surfaceArea(sphere)
	// Cut slightly off the equator: subdividing the icosahedron lands
	// many vertices exactly on z=0, and Cut requires no on-plane
	// vertices.
	res, err := Cut(sphere, AxisPlane(2, 0.01), ConnectorSettings{})
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

// TestCut_TangentPlaneFails verifies that a plane lying exactly on a
// boundary face produces an empty-half error from CGAL. Previous
// hand-rolled code had an on-plane vertex snap that nudged the cut
// just inside the face, producing a thin sliver; CGAL is strict and
// reports the empty half cleanly.
func TestCut_TangentPlaneFails(t *testing.T) {
	cube := makeUnitCube()
	_, err := Cut(cube, AxisPlane(2, 1), ConnectorSettings{})
	if err == nil {
		t.Fatal("Cut: expected error for tangent plane")
	}
}

func TestCut_MissingMeshFails(t *testing.T) {
	cube := makeUnitCube()
	_, err := Cut(cube, AxisPlane(2, 10), ConnectorSettings{})
	if err == nil {
		t.Fatal("Cut: expected error for plane that misses the mesh")
	}
}

func TestCut_NonUnitNormalFails(t *testing.T) {
	cube := makeUnitCube()
	_, err := Cut(cube, Plane{Normal: [3]float64{2, 0, 0}, D: 0.5}, ConnectorSettings{})
	if err == nil {
		t.Fatal("Cut: expected error for non-unit normal")
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

// makeStackedCubes returns a 1×1×2 watertight mesh formed by stacking
// two unit cubes along Z. The four "middle" vertices share z=0
// exactly — cutting at z=0 exercises the on-plane snap path with
// genuinely-interior vertices (geometry on both sides of the cut).
func makeStackedCubes() *loader.LoadedModel {
	v := [][3]float32{
		{0, 0, -1}, {1, 0, -1}, {1, 1, -1}, {0, 1, -1}, // 0..3 bottom
		{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0}, //     4..7 middle (on z=0)
		{0, 0, 1}, {1, 0, 1}, {1, 1, 1}, {0, 1, 1}, //     8..11 top
	}
	f := [][3]uint32{
		// bottom face
		{0, 2, 1}, {0, 3, 2},
		// bottom-cube side walls (linking 0..3 to 4..7)
		{0, 1, 5}, {0, 5, 4},
		{1, 2, 6}, {1, 6, 5},
		{2, 3, 7}, {2, 7, 6},
		{3, 0, 4}, {3, 4, 7},
		// top-cube side walls (linking 4..7 to 8..11)
		{4, 5, 9}, {4, 9, 8},
		{5, 6, 10}, {5, 10, 9},
		{6, 7, 11}, {6, 11, 10},
		{7, 4, 8}, {7, 8, 11},
		// top face
		{8, 9, 10}, {8, 10, 11},
	}
	return &loader.LoadedModel{Vertices: v, Faces: f}
}

// TestCut_OnPlaneVertexSnapsOff verifies that interior vertices lying
// exactly on the cut plane are silently snapped off it (along the
// plane normal, by sub-micron amount) so the cut succeeds. Uses a
// stacked-cubes mesh whose middle quad has all four vertices at z=0;
// without snap, the cap-polygon walker would see a degree-4 junction
// at each of those vertices and break.
func TestCut_OnPlaneVertexSnapsOff(t *testing.T) {
	mesh := makeStackedCubes()
	originalVerts := append([][3]float32(nil), mesh.Vertices...)
	res, err := Cut(mesh, AxisPlane(2, 0), ConnectorSettings{})
	if err != nil {
		t.Fatalf("expected snap-off to recover, got error: %v", err)
	}
	if res == nil || res.Halves[0] == nil || res.Halves[1] == nil {
		t.Fatal("expected both halves to be populated after snap-off")
	}
	// Caller's mesh must be unmodified (snap is supposed to happen on
	// a shallow clone).
	for i, v := range mesh.Vertices {
		if v != originalVerts[i] {
			t.Errorf("Cut mutated input vertex %d: got %v, want %v", i, v, originalVerts[i])
		}
	}
	// Both halves should be well-formed and have material on their side
	// of the plane.
	assertWatertight(t, res.Halves[0], "half 0")
	assertWatertight(t, res.Halves[1], "half 1")
}

// TestCut_MultiComponentSupported covers the non-nested two-component
// case (a barbell-like cross-section where one cut plane catches two
// disjoint cube lobes). Each component triangulates as its own cap
// region so both halves still close watertight.
func TestCut_MultiComponentSupported(t *testing.T) {
	// Two unit cubes side by side at x=[0,1] and x=[3,4]. Cutting at
	// z=0.5 catches both, producing two disjoint cap polygons per
	// half — neither nested inside the other.
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
	res, err := Cut(pair, AxisPlane(2, 0.5), ConnectorSettings{})
	if err != nil {
		t.Fatalf("expected multi-component cut to succeed, got %v", err)
	}
	if res == nil || res.Halves[0] == nil || res.Halves[1] == nil {
		t.Fatal("expected both halves to be populated")
	}
	// Each half should be watertight even though its cap has two
	// disjoint cross-section regions.
	for h := 0; h < 2; h++ {
		assertWatertight(t, res.Halves[h], "multi-comp half "+string(rune('0'+h)))
	}
}

func TestCut_PolygonWithHoles(t *testing.T) {
	hollow := makeHollowCube()
	// Cut at z=0.1 (off-axis to avoid degenerate alignment with face
	// boundaries of the inner cube).
	res, err := Cut(hollow, AxisPlane(2, 0.1), ConnectorSettings{})
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
