package pipeline

import "testing"

// TestStickerStageKeyDependsOnBaseColor guards a cache-coherency contract:
// runSticker deep-clones lo.ColorModel into so.Model, including
// FaceBaseColor. The per-run applyBaseColor reapplies the override to
// lo.ColorModel/lo.SampleModel but not to so.Model. So a base-color change
// must invalidate the sticker stage; otherwise voxelize samples colors from
// a stale so.Model.FaceBaseColor.
func TestStickerStageKeyDependsOnBaseColor(t *testing.T) {
	base := Options{
		Input:     "model.glb",
		BaseColor: "#FF0000",
		Stickers: []Sticker{
			{ImagePath: "sticker.png", Mode: "unfold", Scale: 1, MaxAngle: 90},
		},
	}
	changed := base
	changed.BaseColor = "#00FF00"

	if stageKey(StageSticker, base) == stageKey(StageSticker, changed) {
		t.Fatal("StageSticker key did not change when BaseColor changed; " +
			"runSticker's so.Model.FaceBaseColor would be stale on a cached run")
	}
}

// TestLoadAndDecimateStageKeysIndependentOfBaseColor protects the design
// intent stated in voxelizeSettings's doc comment: load/decimate caches
// survive base-color changes because applyBaseColor patches the cached
// ColorModel/SampleModel in place.
func TestLoadAndDecimateStageKeysIndependentOfBaseColor(t *testing.T) {
	base := Options{Input: "model.glb", BaseColor: "#FF0000"}
	changed := base
	changed.BaseColor = "#00FF00"

	if stageKey(StageLoad, base) != stageKey(StageLoad, changed) {
		t.Error("StageLoad key changed on BaseColor change; load cache should survive")
	}
	if stageKey(StageDecimate, base) != stageKey(StageDecimate, changed) {
		t.Error("StageDecimate key changed on BaseColor change; decimate cache should survive")
	}
}

// TestVoxelizeStageKeyDependsOnBaseColor is a sanity check that the existing
// voxelize invalidation rule still holds.
func TestVoxelizeStageKeyDependsOnBaseColor(t *testing.T) {
	base := Options{Input: "model.glb", BaseColor: "#FF0000"}
	changed := base
	changed.BaseColor = "#00FF00"

	if stageKey(StageVoxelize, base) == stageKey(StageVoxelize, changed) {
		t.Fatal("StageVoxelize key did not change when BaseColor changed")
	}
}
