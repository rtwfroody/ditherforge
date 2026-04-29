package squarevoxel

import (
	"context"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/split"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// makeColorCubeModel returns a 50mm × 50mm × 50mm cube whose face
// colors encode a position (0,0,0) → red, (50,0,0) → green, (0,50,0)
// → blue, (50,50,0) → magenta etc. via per-face base colors. Used to
// verify that color sampling lands at the expected place after a
// transform round-trip.
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

// TestVoxelize_SplitInfoNilUnchanged — passing splitInfo=nil should
// produce results bit-identical to the pre-phase-4 single-mesh path.
// Spot check via cell count and HalfIdx == 0 on every cell.
func TestVoxelize_SplitInfoNilUnchanged(t *testing.T) {
	cube := makeColorCubeModel(20, [4]uint8{200, 100, 50, 255})
	res, err := VoxelizeTwoGrids(
		context.Background(),
		cube, cube,
		nil, nil,
		2, 2, 0.4,
		progress.NullTracker{},
		nil,
		nil, // splitInfo
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

// TestVoxelize_SplitInfoTagsHalves — passing splitInfo with two
// trivially-translated halves produces a mix of HalfIdx=0 and
// HalfIdx=1 cells, each in the spatial region they belong to.
func TestVoxelize_SplitInfoTagsHalves(t *testing.T) {
	// Half 0 sits at x=[0,20], half 1 at x=[25,45]. Both built via
	// makeColorCubeModel so they have full parallel arrays. Identity
	// inverse transforms (color sampling on each half hits its own
	// mesh).
	half0 := makeColorCubeModel(20, [4]uint8{255, 0, 0, 255})
	half1 := makeColorCubeModel(20, [4]uint8{0, 255, 0, 255})
	for i := range half1.Vertices {
		half1.Vertices[i][0] += 25
	}
	// colorModel is the concatenation: 8 vertices and 12 faces from
	// half 0, then 8 vertices (offset) and 12 faces (offset) from
	// half 1. All parallel arrays are concatenated in lockstep.
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
		Halves:           [2]*loader.LoadedModel{half0, half1},
		InverseTransform: [2]split.Transform{split.IdentityTransform, split.IdentityTransform},
	}

	res, err := VoxelizeTwoGrids(
		context.Background(),
		nil, // model unused when splitInfo != nil
		colorModel,
		nil, nil,
		2, 2, 0.4,
		progress.NullTracker{},
		nil,
		splitInfo,
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

// TestVoxelize_SplitInfoInverseTransform — when the geometry mesh is
// translated in bed coords but the inverse transform brings it back
// to the original frame, color sampling should hit the original
// model. This is the load-bearing assertion: voxelize uses the
// inverse transform to find the right place to look up colors when
// the geometry mesh has been laid out away from the original.
func TestVoxelize_SplitInfoInverseTransform(t *testing.T) {
	// Original cube at x=[0, 20], coloured red.
	colorModel := makeColorCubeModel(20, [4]uint8{255, 0, 0, 255})

	// Geometry mesh translated by +100 in x (as if Layout moved it
	// way over). Identical shape, just shifted.
	geom := &loader.LoadedModel{
		Vertices: make([][3]float32, len(colorModel.Vertices)),
		Faces:    append([][3]uint32(nil), colorModel.Faces...),
	}
	for i, v := range colorModel.Vertices {
		geom.Vertices[i] = [3]float32{v[0] + 100, v[1], v[2]}
	}

	// Forward transform: orig → bed adds (+100, 0, 0). Voxelize calls
	// ApplyInverse on this to map bed back to orig.
	invXform := split.Transform{
		Rotation:    [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1},
		Translation: [3]float64{100, 0, 0},
	}
	splitInfo := &SplitInfo{
		Halves:           [2]*loader.LoadedModel{geom, geom},
		InverseTransform: [2]split.Transform{invXform, invXform},
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
	)
	if err != nil {
		t.Fatalf("VoxelizeTwoGrids: %v", err)
	}
	if len(res.Cells) == 0 {
		t.Fatal("no active cells")
	}
	// Every cell should have sampled red from the original cube
	// (since the inverse transform maps back to where colorModel
	// lives).
	red := 0
	for _, c := range res.Cells {
		if c.Color[0] > 200 && c.Color[1] < 50 && c.Color[2] < 50 {
			red++
		}
	}
	if red < len(res.Cells)*8/10 {
		t.Errorf("only %d/%d cells sampled red — inverse transform may not be applied correctly", red, len(res.Cells))
	}
}

// TestVoxelize_SplitInfoRequiresColorModel — passing splitInfo
// without an explicit colorModel should error.
func TestVoxelize_SplitInfoRequiresColorModel(t *testing.T) {
	half := makeColorCubeModel(20, [4]uint8{0, 0, 0, 255})
	splitInfo := &SplitInfo{
		Halves:           [2]*loader.LoadedModel{half, half},
		InverseTransform: [2]split.Transform{split.IdentityTransform, split.IdentityTransform},
	}
	_, err := VoxelizeTwoGrids(
		context.Background(),
		nil, nil, // no model, no colorModel
		nil, nil,
		2, 2, 0.4,
		progress.NullTracker{},
		nil,
		splitInfo,
	)
	if err == nil {
		t.Fatal("expected error when split path runs without colorModel")
	}
}

// TestVoxelize_ActiveCellHalfIdxFieldExists — sanity check the ActiveCell
// field is wired (catches a typo or accidental rename downstream).
func TestVoxelize_ActiveCellHalfIdxFieldExists(t *testing.T) {
	c := voxel.ActiveCell{HalfIdx: 1}
	if c.HalfIdx != 1 {
		t.Errorf("HalfIdx field not stored: %d", c.HalfIdx)
	}
}
