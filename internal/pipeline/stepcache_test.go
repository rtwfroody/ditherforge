package pipeline

import "testing"

// TestStickerStageKeyDependsOnBaseColor guards a cache-coherency contract:
// runSticker deep-clones lo.ColorModel into so.Model, including
// FaceBaseColor. The per-run applyBaseColor reapplies the override to
// lo.ColorModel/lo.SampleModel but not to so.Model. So a base-color change
// must invalidate the sticker stage; otherwise voxelize samples colors from
// a stale so.Model.FaceBaseColor.
func TestStickerStageKeyDependsOnBaseColor(t *testing.T) {
	c := NewStageCache()
	base := Options{
		Input:     "model.glb",
		BaseColor: "#FF0000",
		Stickers: []Sticker{
			{ImagePath: "sticker.png", Mode: "unfold", Scale: 1, MaxAngle: 90},
		},
	}
	changed := base
	changed.BaseColor = "#00FF00"

	if c.stageFnv(StageSticker, base) == c.stageFnv(StageSticker, changed) {
		t.Fatal("StageSticker key did not change when BaseColor changed; " +
			"runSticker's so.Model.FaceBaseColor would be stale on a cached run")
	}
}

// TestLoadAndDecimateStageKeysIndependentOfBaseColor protects the design
// intent stated in voxelizeSettings's doc comment: load/decimate caches
// survive base-color changes because applyBaseColor patches the cached
// ColorModel/SampleModel in place.
func TestLoadAndDecimateStageKeysIndependentOfBaseColor(t *testing.T) {
	c := NewStageCache()
	base := Options{Input: "model.glb", BaseColor: "#FF0000"}
	changed := base
	changed.BaseColor = "#00FF00"

	if c.stageFnv(StageLoad, base) != c.stageFnv(StageLoad, changed) {
		t.Error("StageLoad key changed on BaseColor change; load cache should survive")
	}
	if c.stageFnv(StageDecimate, base) != c.stageFnv(StageDecimate, changed) {
		t.Error("StageDecimate key changed on BaseColor change; decimate cache should survive")
	}
}

// TestVoxelizeStageKeyDependsOnBaseColor is a sanity check that the existing
// voxelize invalidation rule still holds.
func TestVoxelizeStageKeyDependsOnBaseColor(t *testing.T) {
	c := NewStageCache()
	base := Options{Input: "model.glb", BaseColor: "#FF0000"}
	changed := base
	changed.BaseColor = "#00FF00"

	if c.stageFnv(StageVoxelize, base) == c.stageFnv(StageVoxelize, changed) {
		t.Fatal("StageVoxelize key did not change when BaseColor changed")
	}
}

// TestVoxelizeStageKeyDependsOnMaterialX guards the same invalidation
// contract for the procedural base-color override: changing the .mtlx
// content or its tile size must invalidate the voxelize cache because
// the per-voxel sampler reads them.
func TestVoxelizeStageKeyDependsOnMaterialX(t *testing.T) {
	c := NewStageCache()
	base := Options{
		Input:                    "model.glb",
		BaseColorMaterialX:       "<materialx version=\"1.39\"/>",
		BaseColorMaterialXTileMM: 10,
	}
	contentChanged := base
	contentChanged.BaseColorMaterialX = "<materialx version=\"1.39\"><nodegraph/></materialx>"
	tileChanged := base
	tileChanged.BaseColorMaterialXTileMM = 20

	if c.stageFnv(StageVoxelize, base) == c.stageFnv(StageVoxelize, contentChanged) {
		t.Error("StageVoxelize key did not change when BaseColorMaterialX content changed")
	}
	if c.stageFnv(StageVoxelize, base) == c.stageFnv(StageVoxelize, tileChanged) {
		t.Error("StageVoxelize key did not change when BaseColorMaterialXTileMM changed")
	}
}

// TestStickerStageKeyDependsOnMaterialX is the sticker-stage analogue
// of TestStickerStageKeyDependsOnBaseColor for the procedural override.
// runSticker deep-clones lo.ColorModel into so.Model with whatever
// procedural pattern was baked into FaceBaseColor by the per-face
// preview bake; changing the .mtlx must invalidate that cached clone.
func TestStickerStageKeyDependsOnMaterialX(t *testing.T) {
	c := NewStageCache()
	base := Options{
		Input:                    "model.glb",
		BaseColorMaterialX:       "<materialx version=\"1.39\"/>",
		BaseColorMaterialXTileMM: 10,
		Stickers: []Sticker{
			{ImagePath: "sticker.png", Mode: "unfold", Scale: 1, MaxAngle: 90},
		},
	}
	contentChanged := base
	contentChanged.BaseColorMaterialX = "<materialx version=\"1.39\"><nodegraph/></materialx>"
	tileChanged := base
	tileChanged.BaseColorMaterialXTileMM = 20

	if c.stageFnv(StageSticker, base) == c.stageFnv(StageSticker, contentChanged) {
		t.Error("StageSticker key did not change when BaseColorMaterialX content changed; " +
			"so.Model.FaceBaseColor would be stale on a cached run")
	}
	if c.stageFnv(StageSticker, base) == c.stageFnv(StageSticker, tileChanged) {
		t.Error("StageSticker key did not change when BaseColorMaterialXTileMM changed")
	}
}

// TestLoadAndDecimateStageKeysIndependentOfMaterialX mirrors the design
// intent that the per-run applyBaseColor patches caches in place — load
// and decimate must survive .mtlx changes the same way they survive hex
// changes.
func TestLoadAndDecimateStageKeysIndependentOfMaterialX(t *testing.T) {
	c := NewStageCache()
	base := Options{
		Input:                    "model.glb",
		BaseColorMaterialX:       "<materialx version=\"1.39\"/>",
		BaseColorMaterialXTileMM: 10,
	}
	changed := base
	changed.BaseColorMaterialX = "<materialx version=\"1.39\"><nodegraph/></materialx>"
	changed.BaseColorMaterialXTileMM = 20

	if c.stageFnv(StageLoad, base) != c.stageFnv(StageLoad, changed) {
		t.Error("StageLoad key changed on MaterialX change; load cache should survive")
	}
	if c.stageFnv(StageDecimate, base) != c.stageFnv(StageDecimate, changed) {
		t.Error("StageDecimate key changed on MaterialX change; decimate cache should survive")
	}
}
