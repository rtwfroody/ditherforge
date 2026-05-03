package squarevoxel

import (
	"context"
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/split"
)

// makeColorCubeModel returns a `side`-mm cube with the given uniform
// per-face base color, parallel-array conformant.
func makeColorCubeModel(side float32, baseColor [4]uint8) *loader.LoadedModel {
	verts := [][3]float32{
		{0, 0, 0}, {side, 0, 0}, {side, side, 0}, {0, side, 0},
		{0, 0, side}, {side, 0, side}, {side, side, side}, {0, side, side},
	}
	faces := [][3]uint32{
		{0, 2, 1}, {0, 3, 2},
		{4, 5, 6}, {4, 6, 7},
		{0, 1, 5}, {0, 5, 4},
		{2, 3, 7}, {2, 7, 6},
		{0, 4, 7}, {0, 7, 3},
		{1, 2, 6}, {1, 6, 5},
	}
	noTexture := make([]bool, len(faces))
	for i := range noTexture {
		noTexture[i] = true
	}
	baseColors := make([][4]uint8, len(faces))
	for i := range baseColors {
		baseColors[i] = baseColor
	}
	faceTexIdx := make([]int32, len(faces))
	faceAlpha := make([]float32, len(faces))
	for i := range faceAlpha {
		faceAlpha[i] = 1
	}
	return &loader.LoadedModel{
		Vertices:       verts,
		Faces:          faces,
		FaceTextureIdx: faceTexIdx,
		FaceAlpha:      faceAlpha,
		FaceBaseColor:  baseColors,
		NoTextureMask:  noTexture,
	}
}

// translatedModel returns a deep-copy of m with all vertices shifted by
// (dx, dy, dz). Parallel arrays are reused (read-only after construction).
func translatedModel(m *loader.LoadedModel, dx, dy, dz float32) *loader.LoadedModel {
	out := *m
	out.Vertices = make([][3]float32, len(m.Vertices))
	for i, v := range m.Vertices {
		out.Vertices[i] = [3]float32{v[0] + dx, v[1] + dy, v[2] + dz}
	}
	return &out
}

// TestVoxelize_SplitInfoNilUnchanged — passing splitInfo=nil should
// produce the legacy single-mesh result. HalfIdx is 0 on every cell.
func TestVoxelize_SplitInfoNilUnchanged(t *testing.T) {
	cube := makeColorCubeModel(20, [4]uint8{200, 100, 50, 255})
	res, err := VoxelizeTwoGrids(
		context.Background(),
		cube, cube,
		nil, nil,
		2, 2, 0.4,
		progress.NullTracker{},
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("VoxelizeTwoGrids: %v", err)
	}
	if len(res.Cells) == 0 {
		t.Fatal("no active cells")
	}
	for _, c := range res.Cells {
		if c.HalfIdx != 0 {
			t.Errorf("unsplit cell has HalfIdx=%d, want 0", c.HalfIdx)
			break
		}
	}
}

// TestVoxelize_SplitInfoTagsHalves — two spatially-separated halves
// each with its own translated geometry mesh. Verifies HalfIdx
// tagging by location and that cells from each half land in their
// expected x-range.
func TestVoxelize_SplitInfoTagsHalves(t *testing.T) {
	half0 := makeColorCubeModel(20, [4]uint8{255, 0, 0, 255})
	half1 := translatedModel(makeColorCubeModel(20, [4]uint8{0, 255, 0, 255}), 25, 0, 0)
	colorModel := &loader.LoadedModel{
		Vertices:       append(append([][3]float32(nil), half0.Vertices...), half1.Vertices...),
		FaceTextureIdx: append(append([]int32(nil), half0.FaceTextureIdx...), half1.FaceTextureIdx...),
		FaceAlpha:      append(append([]float32(nil), half0.FaceAlpha...), half1.FaceAlpha...),
		FaceBaseColor:  append(append([][4]uint8(nil), half0.FaceBaseColor...), half1.FaceBaseColor...),
		NoTextureMask:  append(append([]bool(nil), half0.NoTextureMask...), half1.NoTextureMask...),
	}
	colorModel.Faces = append([][3]uint32(nil), half0.Faces...)
	off := uint32(len(half0.Vertices))
	for _, f := range half1.Faces {
		colorModel.Faces = append(colorModel.Faces, [3]uint32{f[0] + off, f[1] + off, f[2] + off})
	}
	splitInfo := &SplitInfo{
		Halves: [2]*loader.LoadedModel{half0, half1},
		Xform:  [2]split.Transform{split.IdentityTransform, split.IdentityTransform},
	}
	res, err := VoxelizeTwoGrids(
		context.Background(),
		nil,
		colorModel,
		nil, nil,
		2, 2, 0.4,
		progress.NullTracker{},
		nil,
		splitInfo,
		nil,
	)
	if err != nil {
		t.Fatalf("VoxelizeTwoGrids: %v", err)
	}
	var nHalf0, nHalf1 int
	for _, c := range res.Cells {
		switch c.HalfIdx {
		case 0:
			nHalf0++
			if c.Cx > 25 {
				t.Errorf("half-0 cell at x=%g, expected x<25", c.Cx)
			}
		case 1:
			nHalf1++
			if c.Cx < 20 {
				t.Errorf("half-1 cell at x=%g, expected x>20", c.Cx)
			}
		default:
			t.Errorf("unexpected HalfIdx %d on cell at x=%g", c.HalfIdx, c.Cx)
		}
	}
	if nHalf0 == 0 || nHalf1 == 0 {
		t.Errorf("got %d half-0 cells and %d half-1 cells, want both > 0", nHalf0, nHalf1)
	}
}

// TestVoxelize_SplitInfoInverseTransformDistinctHalves — the
// production scenario: half 0 in one bed location, half 1 in
// another, each with its own Xform, single colorModel in original
// coords. Voxelize must apply the right inverse transform per cell.
func TestVoxelize_SplitInfoInverseTransformDistinctHalves(t *testing.T) {
	// Original cube at x=[0, 20], coloured red.
	colorModel := makeColorCubeModel(20, [4]uint8{255, 0, 0, 255})

	// Two halves: half 0 translated +100 in x (bed-coord position),
	// half 1 translated +200 in x. In real Layout output the two
	// halves would have different geometry (one half each); for this
	// test we use the same shape translated to two bed-coord places.
	geom0 := translatedModel(colorModel, 100, 0, 0)
	geom1 := translatedModel(colorModel, 200, 0, 0)
	xform0 := split.Transform{
		Rotation:    [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1},
		Translation: [3]float64{100, 0, 0},
	}
	xform1 := split.Transform{
		Rotation:    [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1},
		Translation: [3]float64{200, 0, 0},
	}
	splitInfo := &SplitInfo{
		Halves: [2]*loader.LoadedModel{geom0, geom1},
		Xform:  [2]split.Transform{xform0, xform1},
	}
	res, err := VoxelizeTwoGrids(
		context.Background(),
		nil,
		colorModel,
		nil, nil,
		2, 2, 0.4,
		progress.NullTracker{},
		nil,
		splitInfo,
		nil,
	)
	if err != nil {
		t.Fatalf("VoxelizeTwoGrids: %v", err)
	}
	var redInHalf0, redInHalf1 int
	for _, c := range res.Cells {
		isRed := c.Color[0] > 200 && c.Color[1] < 50 && c.Color[2] < 50
		switch c.HalfIdx {
		case 0:
			if isRed {
				redInHalf0++
			}
			if c.Cx < 90 || c.Cx > 130 {
				t.Errorf("half-0 cell at x=%g, expected near 100..120", c.Cx)
			}
		case 1:
			if isRed {
				redInHalf1++
			}
			if c.Cx < 190 || c.Cx > 230 {
				t.Errorf("half-1 cell at x=%g, expected near 200..220", c.Cx)
			}
		}
	}
	if redInHalf0 == 0 {
		t.Error("half 0 sampled no red cells; per-half inverse transform may be wrong")
	}
	if redInHalf1 == 0 {
		t.Error("half 1 sampled no red cells; per-half inverse transform may be wrong")
	}
}

// TestVoxelize_SplitInfoNonIdentityRotation — exercises the
// non-translation part of the inverse transform. A 90° rotation
// about Y maps the cube to a rotated bed-coord cube; voxelize's
// inverse-transform should still recover red colors from the
// original colorModel.
func TestVoxelize_SplitInfoNonIdentityRotation(t *testing.T) {
	colorModel := makeColorCubeModel(20, [4]uint8{255, 0, 0, 255})

	// Forward transform: rotate 90° about Y (x → z, z → -x), then
	// translate so the rotated cube lands in positive bed coords.
	// 90° about Y rotation matrix (row-major):
	//   ( 0, 0, 1)
	//   ( 0, 1, 0)
	//   (-1, 0, 0)
	// Original cube spans (0..20, 0..20, 0..20). After rotation:
	//   x' = z (range 0..20)
	//   y' = y (range 0..20)
	//   z' = -x (range -20..0)
	// Translate by (50, 0, 50) to put the cube at bed coords
	// (50..70, 0..20, 30..50).
	xform := split.Transform{
		Rotation:    [9]float64{0, 0, 1, 0, 1, 0, -1, 0, 0},
		Translation: [3]float64{50, 0, 50},
	}
	geom := &loader.LoadedModel{
		Faces:          append([][3]uint32(nil), colorModel.Faces...),
		FaceTextureIdx: colorModel.FaceTextureIdx,
		FaceAlpha:      colorModel.FaceAlpha,
		FaceBaseColor:  colorModel.FaceBaseColor,
		NoTextureMask:  colorModel.NoTextureMask,
	}
	geom.Vertices = make([][3]float32, len(colorModel.Vertices))
	for i, v := range colorModel.Vertices {
		geom.Vertices[i] = xform.Apply(v)
	}
	splitInfo := &SplitInfo{
		Halves: [2]*loader.LoadedModel{geom, geom},
		Xform:  [2]split.Transform{xform, xform},
	}
	res, err := VoxelizeTwoGrids(
		context.Background(),
		nil,
		colorModel,
		nil, nil,
		2, 2, 0.4,
		progress.NullTracker{},
		nil,
		splitInfo,
		nil,
	)
	if err != nil {
		t.Fatalf("VoxelizeTwoGrids: %v", err)
	}
	red := 0
	for _, c := range res.Cells {
		if c.Color[0] > 200 && c.Color[1] < 50 && c.Color[2] < 50 {
			red++
		}
	}
	if red < len(res.Cells)*8/10 {
		t.Errorf("only %d/%d cells sampled red — non-identity inverse transform may be wrong", red, len(res.Cells))
	}
	// Sanity: a sample bed-coord cell, when run through ApplyInverse,
	// should land somewhere inside the original cube (0..20)^3.
	if len(res.Cells) > 0 {
		c := res.Cells[0]
		orig := xform.ApplyInverse([3]float32{c.Cx, c.Cy, c.Cz})
		for i, x := range orig {
			if x < -1 || x > 21 {
				t.Errorf("bed cell %d: ApplyInverse → %v, axis %d out of expected (-1, 21) range", 0, orig, i)
			}
			_ = math.IsNaN(float64(x))
		}
	}
}

// TestVoxelize_SplitInfoRequiresColorModel — passing splitInfo
// without an explicit colorModel should error.
func TestVoxelize_SplitInfoRequiresColorModel(t *testing.T) {
	half := makeColorCubeModel(20, [4]uint8{0, 0, 0, 255})
	splitInfo := &SplitInfo{
		Halves: [2]*loader.LoadedModel{half, half},
		Xform:  [2]split.Transform{split.IdentityTransform, split.IdentityTransform},
	}
	_, err := VoxelizeTwoGrids(
		context.Background(),
		nil, nil,
		nil, nil,
		2, 2, 0.4,
		progress.NullTracker{},
		nil,
		splitInfo,
		nil,
	)
	if err == nil {
		t.Fatal("expected error when split path runs without colorModel")
	}
}

// TestVoxelize_SplitInfoEmptyHalfRejected — an empty/degenerate half
// should be rejected with a clear error.
func TestVoxelize_SplitInfoEmptyHalfRejected(t *testing.T) {
	half := makeColorCubeModel(20, [4]uint8{0, 0, 0, 255})
	splitInfo := &SplitInfo{
		Halves: [2]*loader.LoadedModel{half, {}},
		Xform:  [2]split.Transform{split.IdentityTransform, split.IdentityTransform},
	}
	_, err := VoxelizeTwoGrids(
		context.Background(),
		nil, half,
		nil, nil,
		2, 2, 0.4,
		progress.NullTracker{},
		nil,
		splitInfo,
		nil,
	)
	if err == nil {
		t.Fatal("expected error when split half is empty")
	}
}
