package pipeline

import (
	"testing"

	"github.com/rtwfroody/ditherforge/internal/diskcache"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/progress"
)

// TestApplyBaseColorClearsRemnantAfterDiskHit reproduces the "texture
// remnant after switching back to solid" bug.
//
// The StageLoad disk blob can capture a ColorModel whose FaceBaseColor was
// baked by a MaterialX override (applyBaseColor mutates it in place right
// after the async cache.set encodes it — a race against the immutable-after-
// set contract). The appliedBaseColor* tracking fields that say "this is
// baked, not pristine" are unexported, so gob drops them: the decoded
// loadOutput carries baked colors but pristine-looking markers.
//
// On the next run with no base-color override (the user switched back to
// "solid"), applyBaseColor must NOT trust those markers — it has to restore
// FaceBaseColor from the pristine parse cache. Before the fix it took the
// "already pristine" early-out and left the baked colors in place, so the
// input preview still showed a blocky remnant of the texture.
func TestApplyBaseColorClearsRemnantAfterDiskHit(t *testing.T) {
	c := NewStageCache()
	d, err := diskcache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c.SetDisk(d)
	defer c.WaitForDiskWrites()

	path := makeFakeInput(t)
	opts := Options{Input: path, Scale: 1, NozzleDiameter: 0.4, LayerHeight: 0.2, Dither: "none"}

	// Pristine parse output: two untextured faces, flat gray base color.
	gray := [4]uint8{128, 128, 128, 255}
	pristine := &loader.LoadedModel{
		Vertices:      [][3]float32{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}, {1, 1, 0}},
		Faces:         [][3]uint32{{0, 1, 2}, {1, 3, 2}},
		FaceBaseColor: [][4]uint8{gray, gray},
		NoTextureMask: []bool{true, true},
	}
	c.set(StageParse, opts, pristine)
	c.WaitForDiskWrites()

	// A loadOutput whose ColorModel has been baked by a MaterialX override,
	// exactly as the in-memory object looks after applyBaseColor ran with a
	// texture active. Persist it as the StageLoad blob and read it back so
	// the unexported appliedBaseColor* markers are dropped on the round-trip
	// — reproducing the poisoned on-disk state.
	baked := loader.CloneForEdit(pristine)
	baked.FaceBaseColor[0] = [4]uint8{200, 30, 30, 255}
	baked.FaceBaseColor[1] = [4]uint8{30, 200, 30, 255}
	c.set(StageLoad, opts, &loadOutput{
		Model:                     baked,
		ColorModel:                baked,
		InputMesh:                 buildInputMeshData(baked),
		appliedBaseColorMaterialX: "/some/texture.mtlx", // dropped by gob (unexported)
		markersValid:              true,                 // dropped by gob (unexported)
	})
	c.WaitForDiskWrites()

	lo := c.getLoad(opts)
	if lo == nil {
		t.Fatal("StageLoad cache miss; expected the blob we just wrote")
	}

	// User switched back to "solid": no base-color override at all.
	applyBaseColor(c, lo, opts, progress.NullTracker{})

	for i, got := range lo.ColorModel.FaceBaseColor {
		if got != gray {
			t.Errorf("face %d FaceBaseColor = %v; texture remnant not cleared (want pristine gray %v)", i, got, gray)
		}
	}
}
