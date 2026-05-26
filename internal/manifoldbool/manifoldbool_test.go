package manifoldbool

import (
	"math"
	"testing"
)

// cubeMesh returns a closed [-h..h]³ cube as XYZ-only triangle soup
// with CCW outward winding. Lifted from devscripts/bench_cell_prism_cgal/
// makeCube and reused as the universal "watertight unit input" for
// these tests.
func cubeMesh(edge float32) ([][3]float32, [][3]uint32) {
	h := edge / 2
	v := [][3]float32{
		{-h, -h, -h}, {h, -h, -h}, {h, h, -h}, {-h, h, -h},
		{-h, -h, h}, {h, -h, h}, {h, h, h}, {-h, h, h},
	}
	f := [][3]uint32{
		{0, 2, 1}, {0, 3, 2},
		{4, 5, 6}, {4, 6, 7},
		{0, 1, 5}, {0, 5, 4},
		{2, 3, 7}, {2, 7, 6},
		{0, 4, 7}, {0, 7, 3},
		{1, 2, 6}, {1, 6, 5},
	}
	return v, f
}

func TestFromMeshAcceptsClosedCube(t *testing.T) {
	v, f := cubeMesh(2)
	m, err := FromMesh(v, f)
	if err != nil {
		t.Fatalf("FromMesh: %v", err)
	}
	defer m.Close()
	if m.IsEmpty() {
		t.Fatalf("Manifold reported empty after cube import")
	}
	if got, want := m.NumTri(), 12; got != want {
		t.Errorf("NumTri = %d, want %d", got, want)
	}
	if vol := m.Volume(); math.Abs(vol-8) > 1e-6 {
		t.Errorf("Volume = %g, want 8 (2³)", vol)
	}
}

func TestFromMeshRejectsOpenMesh(t *testing.T) {
	v, f := cubeMesh(2)
	// Drop one face → mesh is now open.
	f = f[:len(f)-1]
	m, err := FromMesh(v, f)
	if err == nil {
		m.Close()
		t.Fatalf("FromMesh accepted an open mesh; expected error")
	}
}

func TestExtrudePolygonProducesCube(t *testing.T) {
	// Unit square centred at origin → 1×1×2 prism between z=-1 and z=1.
	poly := [][2]float32{
		{-0.5, -0.5}, {0.5, -0.5}, {0.5, 0.5}, {-0.5, 0.5},
	}
	m, err := ExtrudePolygon(poly, -1, 1)
	if err != nil {
		t.Fatalf("ExtrudePolygon: %v", err)
	}
	defer m.Close()
	if m.IsEmpty() {
		t.Fatal("ExtrudePolygon: empty result")
	}
	if vol := m.Volume(); math.Abs(vol-2) > 1e-6 {
		t.Errorf("Volume = %g, want 2", vol)
	}
}

func TestIntersectionOverlappingCubes(t *testing.T) {
	// Two unit cubes overlapping by 50% along X.
	v, f := cubeMesh(1)
	a, err := FromMesh(v, f)
	if err != nil {
		t.Fatalf("FromMesh a: %v", err)
	}
	defer a.Close()

	v2, f2 := cubeMesh(1)
	for i := range v2 {
		v2[i][0] += 0.5
	}
	b, err := FromMesh(v2, f2)
	if err != nil {
		t.Fatalf("FromMesh b: %v", err)
	}
	defer b.Close()

	c, err := Intersection(a, b)
	if err != nil {
		t.Fatalf("Intersection: %v", err)
	}
	defer c.Close()
	if c.IsEmpty() {
		t.Fatal("Intersection: empty result, expected ~0.5 volume")
	}
	if vol := c.Volume(); math.Abs(vol-0.5) > 1e-6 {
		t.Errorf("Volume = %g, want 0.5", vol)
	}
}

func TestIntersectionDisjointReturnsEmpty(t *testing.T) {
	v, f := cubeMesh(1)
	a, err := FromMesh(v, f)
	if err != nil {
		t.Fatalf("FromMesh a: %v", err)
	}
	defer a.Close()

	v2, f2 := cubeMesh(1)
	for i := range v2 {
		v2[i][0] += 10
	}
	b, err := FromMesh(v2, f2)
	if err != nil {
		t.Fatalf("FromMesh b: %v", err)
	}
	defer b.Close()

	c, err := Intersection(a, b)
	if err != nil {
		t.Fatalf("Intersection: %v", err)
	}
	defer c.Close()
	if !c.IsEmpty() {
		t.Errorf("Intersection: not empty (verts=%d tris=%d), expected empty",
			c.NumVert(), c.NumTri())
	}
}

func TestIntersectionSurfaceOnlyFilter(t *testing.T) {
	// 2 mm cube ∩ 1×1×1 cube concentric at origin. Boolean output is
	// the inner 1mm cube (8 verts, 12 tris): 6 faces from the prism's
	// walls inside the source, but ALSO some shared coplanar tris.
	// ToMeshFiltered(srcID) should return only the faces inherited
	// from the 2mm "source" cube — surfaces of the source that fell
	// inside the prism. Because the inner prism is wholly INSIDE the
	// source (not touching its walls), srcID-faces is expected to be
	// ZERO and prismID-faces is expected to be 12 (the full prism).
	srcVerts, srcFaces := cubeMesh(2)
	src, err := FromMesh(srcVerts, srcFaces)
	if err != nil {
		t.Fatalf("FromMesh src: %v", err)
	}
	defer src.Close()
	srcID := src.OriginalID()
	t.Logf("src.OriginalID() = %d", srcID)

	prismVerts, prismFaces := cubeMesh(1)
	prism, err := FromMesh(prismVerts, prismFaces)
	if err != nil {
		t.Fatalf("FromMesh prism: %v", err)
	}
	defer prism.Close()
	prismID := prism.OriginalID()
	t.Logf("prism.OriginalID() = %d", prismID)

	if srcID < 0 || prismID < 0 {
		t.Fatalf("OriginalIDs must be non-negative; got src=%d prism=%d", srcID, prismID)
	}
	if srcID == prismID {
		t.Fatalf("src and prism should have distinct OriginalIDs; both got %d", srcID)
	}

	out, err := Intersection(src, prism)
	if err != nil {
		t.Fatalf("Intersection: %v", err)
	}
	defer out.Close()
	t.Logf("intersection: tris=%d verts=%d, vol=%g", out.NumTri(), out.NumVert(), out.Volume())

	vAll, fAll := out.ToMesh()
	t.Logf("ToMesh unfiltered: %d verts, %d faces", len(vAll), len(fAll))

	vSrc, fSrc := out.ToMeshFiltered(srcID)
	t.Logf("ToMeshFiltered(src=%d): %d verts, %d faces", srcID, len(vSrc), len(fSrc))

	vPrism, fPrism := out.ToMeshFiltered(prismID)
	t.Logf("ToMeshFiltered(prism=%d): %d verts, %d faces", prismID, len(vPrism), len(fPrism))

	// Sanity: unfiltered should be the full intersection. Filtered
	// by srcID + filtered by prismID should partition the faces.
	if len(fSrc)+len(fPrism) != len(fAll) {
		t.Errorf("filter partition: src(%d) + prism(%d) = %d, want %d (total)",
			len(fSrc), len(fPrism), len(fSrc)+len(fPrism), len(fAll))
	}
}

func TestToMeshRoundTrip(t *testing.T) {
	v, f := cubeMesh(2)
	m, err := FromMesh(v, f)
	if err != nil {
		t.Fatalf("FromMesh: %v", err)
	}
	defer m.Close()

	verts, faces := m.ToMesh()
	if len(verts) != 8 {
		t.Errorf("ToMesh verts = %d, want 8", len(verts))
	}
	if len(faces) != 12 {
		t.Errorf("ToMesh faces = %d, want 12", len(faces))
	}
	// Every output vertex must be one of the 8 cube corners. Catches
	// the case where the ToMesh reader was pulling uninitialised
	// memory instead of the actual mesh data — the symptom was Z
	// coordinates orders of magnitude past the input bbox.
	for _, vv := range verts {
		ok := false
		for _, c := range v {
			if math.Abs(float64(vv[0]-c[0])) < 1e-5 &&
				math.Abs(float64(vv[1]-c[1])) < 1e-5 &&
				math.Abs(float64(vv[2]-c[2])) < 1e-5 {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("ToMesh emitted unexpected vertex %v; expected one of the 8 cube corners", vv)
		}
	}
}
