package voxel

import (
	"context"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// makeTestImage creates a solid colored image with full alpha.
func makeTestImage(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// makeFlatQuadModel creates a flat quad (two triangles) on the XY plane at z=0.
// The quad spans from (0,0,0) to (size,size,0).
func makeFlatQuadModel(size float32) *loader.LoadedModel {
	return &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 0},
			{size, 0, 0},
			{size, size, 0},
			{0, size, 0},
		},
		Faces: [][3]uint32{
			{0, 1, 2},
			{0, 2, 3},
		},
		FaceBaseColor: [][4]uint8{
			{128, 128, 128, 255},
			{128, 128, 128, 255},
		},
	}
}

func TestFindSeedTriangle(t *testing.T) {
	model := makeFlatQuadModel(10)
	si := NewSpatialIndex(model, 2)

	// Click near center of the quad — should find a triangle.
	tri := FindSeedTriangle([3]float64{5, 5, 0}, model, si)
	if tri < 0 {
		t.Fatal("expected to find a seed triangle")
	}
	if tri != 0 && tri != 1 {
		t.Fatalf("expected tri 0 or 1, got %d", tri)
	}

	// Click far away — should still find something (expanding radius).
	tri = FindSeedTriangle([3]float64{100, 100, 0}, model, si)
	if tri < 0 {
		t.Fatal("expected to find a seed triangle even far from mesh")
	}
}

func TestBuildStickerDecalBasic(t *testing.T) {
	model := makeFlatQuadModel(10)
	adj := BuildTriAdjacency(model)
	img := makeTestImage(4, 4, color.NRGBA{255, 0, 0, 255})

	// Place sticker at center of quad, normal pointing up (+Z).
	center := [3]float64{5, 5, 0}
	normal := [3]float64{0, 0, 1}
	up := [3]float64{0, 1, 0}
	scale := 3.0

	si := NewSpatialIndex(model, 2)
	seedTri := FindSeedTriangle(center, model, si)
	if seedTri < 0 {
		t.Fatal("no seed triangle found")
	}

	decal, err := BuildStickerDecal(context.Background(), model, adj, img, seedTri, center, normal, up, scale, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(decal.TriUVs) == 0 {
		t.Fatal("expected some triangles in decal, got 0")
	}
	t.Logf("Decal covers %d triangles", len(decal.TriUVs))
}

func TestBuildStickerDecalDoesNotWrapThrough(t *testing.T) {
	// Build a box-like mesh: 6 faces (12 triangles). Place a sticker on one face.
	// The decal should not wrap to the opposite face.
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			// Front face (z=1)
			{0, 0, 1}, {1, 0, 1}, {1, 1, 1}, {0, 1, 1},
			// Back face (z=0)
			{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0},
		},
		Faces: [][3]uint32{
			// Front
			{0, 1, 2}, {0, 2, 3},
			// Back
			{5, 4, 7}, {5, 7, 6},
			// Right
			{1, 5, 6}, {1, 6, 2},
			// Left
			{4, 0, 3}, {4, 3, 7},
			// Top
			{3, 2, 6}, {3, 6, 7},
			// Bottom
			{4, 5, 1}, {4, 1, 0},
		},
		FaceBaseColor: make([][4]uint8, 12),
	}
	for i := range model.FaceBaseColor {
		model.FaceBaseColor[i] = [4]uint8{128, 128, 128, 255}
	}

	adj := BuildTriAdjacency(model)
	img := makeTestImage(4, 4, color.NRGBA{255, 0, 0, 255})

	// Place sticker on front face, centered, scale covers just the front face.
	center := [3]float64{0.5, 0.5, 1}
	normal := [3]float64{0, 0, 1}
	up := [3]float64{0, 1, 0}
	scale := 0.8

	si := NewSpatialIndex(model, 0.5)
	seedTri := FindSeedTriangle(center, model, si)
	if seedTri < 0 {
		t.Fatal("no seed triangle found")
	}

	decal, err := BuildStickerDecal(context.Background(), model, adj, img, seedTri, center, normal, up, scale, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Check that no back-face triangles (indices 2,3) are in the decal.
	for _, backTri := range []int32{2, 3} {
		if _, ok := decal.TriUVs[backTri]; ok {
			t.Errorf("back face triangle %d should not be in decal", backTri)
		}
	}
	t.Logf("Decal covers %d triangles (front face has 2)", len(decal.TriUVs))
}

func TestCompositeStickerColor(t *testing.T) {
	img := makeTestImage(4, 4, color.NRGBA{255, 0, 0, 255})

	decal := &StickerDecal{
		Image: img,
		TriUVs: map[int32][3][2]float32{
			0: {
				{0, 0},
				{1, 0},
				{0.5, 1},
			},
		},
	}

	base := [4]uint8{128, 128, 128, 255}
	// Barycentric at center of triangle.
	bary := [3]float32{1.0 / 3, 1.0 / 3, 1.0 / 3}

	result := CompositeStickerColor(base, 0, bary, []*StickerDecal{decal})

	// Should be red (sticker is fully opaque red).
	if result[0] < 200 {
		t.Errorf("expected red channel > 200, got %d", result[0])
	}
	if result[1] > 50 || result[2] > 50 {
		t.Errorf("expected low green/blue, got g=%d b=%d", result[1], result[2])
	}
}

func TestCompositeStickerColorTransparent(t *testing.T) {
	img := makeTestImage(4, 4, color.NRGBA{255, 0, 0, 0}) // fully transparent

	decal := &StickerDecal{
		Image: img,
		TriUVs: map[int32][3][2]float32{
			0: {{0, 0}, {1, 0}, {0.5, 1}},
		},
	}

	base := [4]uint8{128, 128, 128, 255}
	bary := [3]float32{1.0 / 3, 1.0 / 3, 1.0 / 3}

	result := CompositeStickerColor(base, 0, bary, []*StickerDecal{decal})

	// Transparent sticker should not change the base color.
	if result != base {
		t.Errorf("transparent sticker should not change base, got %v", result)
	}
}

func TestCompositeStickerColorNoDecal(t *testing.T) {
	// Triangle not in any decal — color should be unchanged.
	base := [4]uint8{128, 128, 128, 255}
	result := CompositeStickerColor(base, 99, [3]float32{0.33, 0.33, 0.34}, nil)
	if result != base {
		t.Errorf("no decal should leave base unchanged, got %v", result)
	}
}

// TestBuildStickerDecalSubdividesPathologicalTriangle: a mesh with a single
// triangle whose 3D edges are multiples of the sticker extent. Without
// subdivision, DEM would hand the triangle a single UV that spans far outside
// [0,1]², and the occupancy rasterizer would hand back a decal with one
// triangle whose sticker coverage is visually wrong. With subdivision,
// acceptTriSubdividing should produce multiple sub-triangles, each with a UV
// diameter small enough to fit inside the sticker rect.
func TestBuildStickerDecalSubdividesPathologicalTriangle(t *testing.T) {
	t.Parallel()
	// One big triangle, 20 units wide in 3D; sticker is 4 units wide,
	// so halfW = halfH = 2 and the subdivision threshold is 2.
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			{-10, -10, 0},
			{10, -10, 0},
			{0, 10, 0},
		},
		Faces:         [][3]uint32{{0, 1, 2}},
		FaceBaseColor: [][4]uint8{{128, 128, 128, 255}},
	}
	adj := BuildTriAdjacency(model)
	img := makeTestImage(4, 4, color.NRGBA{255, 0, 0, 255})

	center := [3]float64{0, 0, 0}
	normal := [3]float64{0, 0, 1}
	up := [3]float64{0, 1, 0}
	scale := 4.0

	si := NewSpatialIndex(model, 2)
	seedTri := FindSeedTriangle(center, model, si)
	if seedTri < 0 {
		t.Fatal("no seed triangle found")
	}

	decal, err := BuildStickerDecal(context.Background(), model, adj, img,
		seedTri, center, normal, up, scale, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Subdivision must have produced more than the one original triangle.
	if len(decal.TriUVs) < 2 {
		t.Fatalf("expected subdivision to yield multiple triangles, got %d",
			len(decal.TriUVs))
	}

	// Every triangle's UV footprint must fit inside the sticker rect —
	// that's the invariant subdivision is supposed to enforce. Without
	// subdivision the single original triangle's UV span would be 5×
	// the rect width.
	const maxUVSpan = 1.1 // a tiny slop on [0,1]²
	for triIdx, uvs := range decal.TriUVs {
		minU, maxU := uvs[0][0], uvs[0][0]
		minV, maxV := uvs[0][1], uvs[0][1]
		for _, uv := range uvs[1:] {
			if uv[0] < minU {
				minU = uv[0]
			}
			if uv[0] > maxU {
				maxU = uv[0]
			}
			if uv[1] < minV {
				minV = uv[1]
			}
			if uv[1] > maxV {
				maxV = uv[1]
			}
		}
		if maxU-minU > maxUVSpan || maxV-minV > maxUVSpan {
			t.Errorf("tri %d UV span exceeds rect: span=(%.3f, %.3f) uvs=%v",
				triIdx, maxU-minU, maxV-minV, uvs)
		}
		// The AABB must also actually overlap [0,1]² — the gate in
		// acceptTriSubdividing uses rect overlap, so a tri that slipped
		// through must have a non-empty intersection with the rect.
		if maxU < 0 || minU > 1 || maxV < 0 || minV > 1 {
			t.Errorf("tri %d UV AABB does not overlap [0,1]²: uvs=%v",
				triIdx, uvs)
		}
	}
	t.Logf("subdivided decal covers %d triangles", len(decal.TriUVs))
}

// TestBuildStickerDecalRespectsCallerIsolation: guards the shared-Faces
// aliasing regression that caused an alpha-wrap + sticker crash before the
// pipeline started deep-cloning via loader.DeepCloneForMutation. When
// alpha-wrap is on, InflateAlongNormals → CloneForEdit produces a
// SampleModel that SHARES its Faces backing with ColorModel. If the sticker
// stage mutates ColorModel.Faces in place, those writes reach SampleModel
// and the voxelizer panics indexing SampleModel.Vertices with midpoint
// indices that only exist in ColorModel.Vertices.
//
// This test proves the property at the BuildStickerDecal boundary: given a
// deep-cloned "scratch" model, the subdivision must not touch any shallow
// sibling that still holds the pre-clone Faces backing. It catches a
// regression where DeepCloneForMutation reverts to a shallow copy. It does
// NOT catch the orthogonal regression of `runSticker` (or a future caller)
// dropping the DeepCloneForMutation call and passing lo.ColorModel directly
// — that would require a pipeline-layer smoke test.
func TestBuildStickerDecalRespectsCallerIsolation(t *testing.T) {
	t.Parallel()
	// Pathological 20-unit triangle under a 4-unit sticker — guaranteed to
	// trigger subdivision, which overwrites model.Faces[0] and appends new
	// faces. Without caller isolation those writes would reach the sibling.
	orig := &loader.LoadedModel{
		Vertices: [][3]float32{
			{-10, -10, 0},
			{10, -10, 0},
			{0, 10, 0},
		},
		Faces:         [][3]uint32{{0, 1, 2}},
		FaceBaseColor: [][4]uint8{{128, 128, 128, 255}},
	}

	// Simulate the alpha-wrap layout: a shallow clone that shares the
	// Faces backing with orig. InflateAlongNormals does exactly this —
	// it uses CloneForEdit which only duplicates Vertices/FaceBaseColor.
	sibling := loader.CloneForEdit(orig)
	// Confirm the alias up front: if CloneForEdit ever starts deep-copying
	// Faces, this whole test becomes vacuous, so fail loudly here rather
	// than silently passing later.
	orig.Faces[0][0] = 42
	if sibling.Faces[0][0] != 42 {
		t.Fatal("test setup broken: CloneForEdit should share Faces backing")
	}
	orig.Faces[0][0] = 0

	siblingFacesLenBefore := len(sibling.Faces)
	// Snapshot by value — [3]uint32 is an array, not a slice, so this is a
	// genuine copy that survives any later mutation of sibling.Faces[0].
	siblingFace0Before := sibling.Faces[0]

	// Mirror what runSticker does: deep-clone before letting the BFS mutate.
	scratch := loader.DeepCloneForMutation(orig)
	adj := BuildTriAdjacency(scratch)
	img := makeTestImage(4, 4, color.NRGBA{255, 0, 0, 255})

	center := [3]float64{0, 0, 0}
	normal := [3]float64{0, 0, 1}
	up := [3]float64{0, 1, 0}
	scale := 4.0

	si := NewSpatialIndex(scratch, 2)
	seedTri := FindSeedTriangle(center, scratch, si)
	if seedTri < 0 {
		t.Fatal("no seed triangle found")
	}
	decal, err := BuildStickerDecal(context.Background(), scratch, adj, img,
		seedTri, center, normal, up, scale, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Sanity: confirm the BFS actually subdivided (otherwise the test
	// isn't exercising the mutation path that triggered the bug).
	if len(decal.TriUVs) < 2 {
		t.Fatalf("expected subdivision (multiple decal tris), got %d — test "+
			"geometry is not exercising the mutation path",
			len(decal.TriUVs))
	}
	if len(scratch.Faces) <= siblingFacesLenBefore {
		t.Fatalf("scratch.Faces did not grow from subdivision (got %d, want > %d)",
			len(scratch.Faces), siblingFacesLenBefore)
	}

	// The actual regression assertions: sibling's shared-backing slices
	// must be untouched.
	if len(sibling.Faces) != siblingFacesLenBefore {
		t.Errorf("sibling.Faces length changed: %d → %d (subdivision leaked across aliased backing)",
			siblingFacesLenBefore, len(sibling.Faces))
	}
	if sibling.Faces[0] != siblingFace0Before {
		t.Errorf("sibling.Faces[0] corrupted by subdivision: %v → %v",
			siblingFace0Before, sibling.Faces[0])
	}
	// And the indices in sibling.Faces must still be in range for
	// sibling.Vertices — the specific crash was an out-of-bounds index
	// created when a subdivided midpoint index got written through. Stop
	// at the first violation; one is plenty to identify the regression.
	for i, f := range sibling.Faces {
		for j, vi := range f {
			if int(vi) >= len(sibling.Vertices) {
				t.Fatalf("sibling.Faces[%d][%d]=%d exceeds len(sibling.Vertices)=%d",
					i, j, vi, len(sibling.Vertices))
			}
		}
	}
}

// TestBuildStickerDecalProjectionDoesNotLeakToBackWall builds a hollow
// box (front wall and a parallel back wall, both facing +Z toward the
// projector) and applies a projection-mode sticker to the front. The
// back wall is far enough behind that it must not appear in the decal.
//
// Regression: an earlier centroid-only occlusion test failed for tall
// thin candidates whose centroid sat outside the sticker rect, and for
// candidates whose centroid landed in tangent-space cracks of the front
// mesh tiling. The pixel-grained depth-buffer test plus the depth-cluster
// gap filter together guarantee the back wall is fully culled.
func TestBuildStickerDecalProjectionDoesNotLeakToBackWall(t *testing.T) {
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 1}, {2, 0, 1}, {2, 2, 1}, {0, 2, 1},
			{0, 0, -2}, {2, 0, -2}, {2, 2, -2}, {0, 2, -2},
		},
		Faces: [][3]uint32{
			{0, 1, 2}, {0, 2, 3}, // front wall, normal +Z
			{4, 5, 6}, {4, 6, 7}, // back wall, normal +Z (also faces projector)
		},
		FaceBaseColor: [][4]uint8{
			{128, 128, 128, 255}, {128, 128, 128, 255},
			{128, 128, 128, 255}, {128, 128, 128, 255},
		},
	}
	img := makeTestImage(8, 8, color.NRGBA{255, 0, 0, 255})

	center := [3]float64{1, 1, 1}
	normal := [3]float64{0, 0, 1}
	up := [3]float64{0, 1, 0}
	scale := 1.5

	decal, err := BuildStickerDecalProjection(
		context.Background(), model, img, center, normal, up, scale, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, backTri := range []int32{2, 3} {
		if _, ok := decal.TriUVs[backTri]; ok {
			t.Errorf("back wall tri %d leaked into projection decal", backTri)
		}
	}
	frontFound := 0
	for _, frontTri := range []int32{0, 1} {
		if _, ok := decal.TriUVs[frontTri]; ok {
			frontFound++
		}
	}
	if frontFound == 0 {
		t.Error("expected front wall in decal, got none")
	}
}

// TestBuildStickerDecalUnfoldCoversCurvedSurface builds a small curved
// strip (a fan of triangles forming a shallow cylindrical arc) and
// applies an unfold-mode sticker. ARAP relaxation legitimately produces
// small UV overlaps between adjacent triangles on a curved surface, so
// post-ARAP coverage must remain near-complete.
//
// Regression: an earlier occupancy rasterizer rejected any triangle
// whose UV footprint was already majority-claimed, intending to catch
// fold-backs. On real meshes with skinny tessellation that threshold
// rejected ~half of legitimate coverage. The rasterizer was removed; this
// test guards the high-coverage property.
func TestBuildStickerDecalUnfoldCoversCurvedSurface(t *testing.T) {
	const (
		nCols   = 32
		R       = float64(5)
		halfArc = float64(0.6) // radians; ±halfArc from the +Z apex
		dx      = float32(0.5) // strip extent in X
	)
	verts := make([][3]float32, 0, 2*(nCols+1))
	for i := 0; i <= nCols; i++ {
		theta := -halfArc + 2*halfArc*float64(i)/float64(nCols)
		y := float32(R * math.Sin(theta))
		z := float32(R * math.Cos(theta))
		verts = append(verts, [3]float32{0, y, z})
		verts = append(verts, [3]float32{dx, y, z})
	}
	faces := make([][3]uint32, 0, 2*nCols)
	for i := 0; i < nCols; i++ {
		a := uint32(2 * i)
		b := uint32(2*i + 1)
		c := uint32(2*i + 2)
		d := uint32(2*i + 3)
		faces = append(faces, [3]uint32{a, b, c}, [3]uint32{b, d, c})
	}
	origFaceCount := len(faces)
	model := &loader.LoadedModel{
		Vertices:      verts,
		Faces:         faces,
		FaceBaseColor: make([][4]uint8, origFaceCount),
	}
	for i := range model.FaceBaseColor {
		model.FaceBaseColor[i] = [4]uint8{128, 128, 128, 255}
	}

	adj := BuildTriAdjacency(model)
	img := makeTestImage(8, 8, color.NRGBA{0, 255, 0, 255})

	center := [3]float64{float64(dx) / 2, 0, R}
	normal := [3]float64{0, 0, 1}
	up := [3]float64{1, 0, 0} // align tangent U with X (strip axis)
	scale := 2.5

	si := NewSpatialIndex(model, 0.5)
	seedTri := FindSeedTriangle(center, model, si)
	if seedTri < 0 {
		t.Fatal("no seed triangle found")
	}

	decal, err := BuildStickerDecal(context.Background(), model, adj, img,
		seedTri, center, normal, up, scale, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Count *original* faces whose 3D centroid Y lies within the rect's
	// V extent. Subdivision grows the face slice, so iterate only over
	// the original prefix.
	wantInRect := 0
	for fi := 0; fi < origFaceCount; fi++ {
		f := faces[fi]
		var cy float32
		for k := 0; k < 3; k++ {
			cy += verts[f[k]][1]
		}
		cy /= 3
		if math.Abs(float64(cy)) <= scale/2 {
			wantInRect++
		}
	}
	if wantInRect == 0 {
		t.Fatal("test setup error: no triangles in rect")
	}

	got := 0
	for tri := range decal.TriUVs {
		if int(tri) < origFaceCount {
			got++
		}
	}
	cov := float64(got) / float64(wantInRect)
	if cov < 0.80 {
		t.Errorf("unfold coverage too low: %d/%d original-face tris = %.1f%%; expected >= 80%%",
			got, wantInRect, 100*cov)
	}
}
