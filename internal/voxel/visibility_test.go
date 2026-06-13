package voxel

import (
	"context"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// appendBox adds an axis-aligned box [lo, hi] to model as 12 triangles
// with outward-facing CCW winding, and returns the face index range
// [first, first+12).
func appendBox(model *loader.LoadedModel, lo, hi [3]float32) (first int) {
	base := uint32(len(model.Vertices))
	first = len(model.Faces)
	for _, c := range [][3]float32{
		{lo[0], lo[1], lo[2]}, {hi[0], lo[1], lo[2]},
		{hi[0], hi[1], lo[2]}, {lo[0], hi[1], lo[2]},
		{lo[0], lo[1], hi[2]}, {hi[0], lo[1], hi[2]},
		{hi[0], hi[1], hi[2]}, {lo[0], hi[1], hi[2]},
	} {
		model.Vertices = append(model.Vertices, c)
	}
	quads := [][4]uint32{
		{0, 3, 2, 1}, // bottom (z=lo)
		{4, 5, 6, 7}, // top (z=hi)
		{0, 1, 5, 4}, // y=lo
		{2, 3, 7, 6}, // y=hi
		{1, 2, 6, 5}, // x=hi
		{3, 0, 4, 7}, // x=lo
	}
	for _, q := range quads {
		model.Faces = append(model.Faces,
			[3]uint32{base + q[0], base + q[1], base + q[2]},
			[3]uint32{base + q[0], base + q[2], base + q[3]})
	}
	return first
}

func faceVisibility(t *testing.T, model *loader.LoadedModel) []bool {
	t.Helper()
	bvh, err := BuildRayBVH(context.Background(), model)
	if err != nil {
		t.Fatalf("BuildRayBVH: %v", err)
	}
	vis := make([]bool, len(model.Faces))
	for fi := range model.Faces {
		vis[fi] = bvh.FaceVisible(fi)
	}
	return vis
}

// A cube fully enclosed inside a larger cube: every outer face is
// exterior-visible, every inner face is hidden.
func TestFaceVisibilityNestedCube(t *testing.T) {
	model := &loader.LoadedModel{}
	outer := appendBox(model, [3]float32{0, 0, 0}, [3]float32{10, 10, 10})
	inner := appendBox(model, [3]float32{4, 4, 4}, [3]float32{6, 6, 6})

	vis := faceVisibility(t, model)
	for fi := outer; fi < outer+12; fi++ {
		if !vis[fi] {
			t.Errorf("outer face %d: want visible", fi)
		}
	}
	for fi := inner; fi < inner+12; fi++ {
		if vis[fi] {
			t.Errorf("inner face %d: want hidden", fi)
		}
	}
}

// A thin cap hovering just under the top of a closed cube (the
// flood-fill pocket geometry): the cap is hidden even though it sits a
// tiny distance from the visible top.
func TestFaceVisibilityPocketCap(t *testing.T) {
	model := &loader.LoadedModel{}
	outer := appendBox(model, [3]float32{0, 0, 0}, [3]float32{10, 10, 10})
	// Pocket fill: a slab whose top sits 0.05 under the cube top.
	cap_ := appendBox(model, [3]float32{0.05, 0.05, 0.05}, [3]float32{9.95, 9.95, 9.95})

	vis := faceVisibility(t, model)
	for fi := outer; fi < outer+12; fi++ {
		if !vis[fi] {
			t.Errorf("outer face %d: want visible", fi)
		}
	}
	for fi := cap_; fi < cap_+12; fi++ {
		if vis[fi] {
			t.Errorf("pocket face %d: want hidden", fi)
		}
	}
}

// With the outer box's top removed, the inner cube can see out through
// the opening: its top faces must be classified visible.
func TestFaceVisibilityOpenBox(t *testing.T) {
	model := &loader.LoadedModel{}
	outer := appendBox(model, [3]float32{0, 0, 0}, [3]float32{10, 10, 10})
	// Remove the outer top quad (faces outer+2, outer+3 per appendBox
	// quad order: bottom 0-1, top 2-3, ...).
	topA, topB := outer+2, outer+3
	model.Faces = append(model.Faces[:topA], model.Faces[topB+1:]...)
	inner := appendBox(model, [3]float32{4, 4, 4}, [3]float32{6, 6, 6})

	vis := faceVisibility(t, model)
	// Inner top faces (quad index 1 → faces inner+2, inner+3) see the
	// sky through the opening.
	for _, fi := range []int{inner + 2, inner + 3} {
		if !vis[fi] {
			t.Errorf("inner top face %d: want visible through opening", fi)
		}
	}
	// The inner bottom faces sit under the inner cube: upward rays are
	// blocked by the cube itself, everything else by the outer box —
	// they stay hidden.
	for _, fi := range []int{inner, inner + 1} {
		if vis[fi] {
			t.Errorf("inner bottom face %d: want hidden", fi)
		}
	}
}

// Degenerate (zero-area) faces are reported visible so their sampling
// behavior is unchanged.
func TestFaceVisibilityDegenerate(t *testing.T) {
	model := &loader.LoadedModel{}
	appendBox(model, [3]float32{0, 0, 0}, [3]float32{10, 10, 10})
	base := uint32(len(model.Vertices))
	model.Vertices = append(model.Vertices,
		[3]float32{5, 5, 5}, [3]float32{5, 5, 5}, [3]float32{5, 5, 5})
	model.Faces = append(model.Faces, [3]uint32{base, base + 1, base + 2})

	vis := faceVisibility(t, model)
	if !vis[len(model.Faces)-1] {
		t.Errorf("degenerate face: want visible (legacy behavior)")
	}
}

// SampleNearestColor must prefer the nearest exterior-visible face
// over a nearer hidden one, and fall back to hidden faces when no
// visible face is in range.
func TestSampleNearestColorPrefersVisible(t *testing.T) {
	model := &loader.LoadedModel{}
	outer := appendBox(model, [3]float32{0, 0, 0}, [3]float32{10, 10, 10})
	cap_ := appendBox(model, [3]float32{0.2, 0.2, 0.2}, [3]float32{9.8, 9.8, 9.8})
	model.FaceBaseColor = make([][4]uint8, len(model.Faces))
	for fi := outer; fi < outer+12; fi++ {
		model.FaceBaseColor[fi] = [4]uint8{0, 0, 255, 255} // outer: blue
	}
	for fi := cap_; fi < cap_+12; fi++ {
		model.FaceBaseColor[fi] = [4]uint8{255, 0, 0, 255} // pocket: red
	}

	si := NewSpatialIndex(model, 2)
	buf := NewSearchBuf(len(model.Faces))

	// Sample point just under the pocket cap top: the red cap (at
	// z=9.8) is nearer than the blue outer top (z=10).
	p := [3]float32{5, 5, 9.75}

	// Without visibility: nearest wins — red.
	got := SampleNearestColor(p, model, si, 1.0, buf, nil, nil)
	if got != [4]uint8{255, 0, 0, 255} {
		t.Fatalf("without visibility: got %v, want red (nearest)", got)
	}

	// With visibility: the hidden cap is skipped — blue.
	si.FaceVisible = faceVisibility(t, model)
	got = SampleNearestColor(p, model, si, 1.0, buf, nil, nil)
	if got != [4]uint8{0, 0, 255, 255} {
		t.Fatalf("with visibility: got %v, want blue (nearest visible)", got)
	}

	// Fallback: a point deep inside, far (> radius in Z) from any
	// visible face but within range of the hidden cap bottom, still
	// samples the hidden geometry rather than returning a miss.
	pDeep := [3]float32{5, 5, 0.5}
	got = SampleNearestColor(pDeep, model, si, 0.4, buf, nil, nil)
	if got != [4]uint8{255, 0, 0, 255} {
		t.Fatalf("fallback: got %v, want red (nearest hidden when no visible in range)", got)
	}
}
