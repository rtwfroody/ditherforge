package pipeline

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeMtlxTempFile drops a tiny .mtlx file into a freshly-created
// temp dir and returns its path. Test helper for the MaterialX cache
// tests below — the contents don't have to be a valid graph since
// settingsForStage only stat()s the file.
func writeMtlxTempFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "graph.mtlx")
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatalf("write mtlx: %v", err)
	}
	return p
}

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
// contract for the MaterialX base-color override: changing the path,
// the file's bytes (via mtime/size), the tile size, or the triplanar
// sharpness must each independently invalidate the voxelize cache
// because the per-voxel sampler reads them.
func TestVoxelizeStageKeyDependsOnMaterialX(t *testing.T) {
	c := NewStageCache()
	mtlxA := writeMtlxTempFile(t, "<materialx version=\"1.39\"/>")
	mtlxB := writeMtlxTempFile(t, "<materialx version=\"1.39\"><nodegraph/></materialx>")
	base := Options{
		Input:                                "model.glb",
		BaseColorMaterialX:                   mtlxA,
		BaseColorMaterialXTileMM:             10,
		BaseColorMaterialXTriplanarSharpness: 4,
	}
	pathChanged := base
	pathChanged.BaseColorMaterialX = mtlxB
	tileChanged := base
	tileChanged.BaseColorMaterialXTileMM = 20
	sharpChanged := base
	sharpChanged.BaseColorMaterialXTriplanarSharpness = 8

	if c.stageFnv(StageVoxelize, base) == c.stageFnv(StageVoxelize, pathChanged) {
		t.Error("StageVoxelize key did not change when BaseColorMaterialX path changed")
	}
	if c.stageFnv(StageVoxelize, base) == c.stageFnv(StageVoxelize, tileChanged) {
		t.Error("StageVoxelize key did not change when BaseColorMaterialXTileMM changed")
	}
	if c.stageFnv(StageVoxelize, base) == c.stageFnv(StageVoxelize, sharpChanged) {
		t.Error("StageVoxelize key did not change when BaseColorMaterialXTriplanarSharpness changed")
	}

	// Edit-in-place: same path, larger file. Different size must
	// invalidate even though the path string is unchanged.
	beforeEdit := c.stageFnv(StageVoxelize, base)
	if err := os.WriteFile(mtlxA, []byte("<materialx version=\"1.39\"><x/></materialx>"), 0644); err != nil {
		t.Fatalf("rewrite mtlx: %v", err)
	}
	// Bump mtime forward a hair to defeat filesystems that only stamp
	// at second granularity.
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(mtlxA, future, future)
	afterEdit := c.stageFnv(StageVoxelize, base)
	if beforeEdit == afterEdit {
		t.Error("StageVoxelize key did not change after rewriting the .mtlx file (mtime/size hash broken)")
	}
}

// TestStickerStageKeyDependsOnMaterialX is the sticker-stage analogue
// of TestStickerStageKeyDependsOnBaseColor for the MaterialX override.
// runSticker deep-clones lo.ColorModel into so.Model with whatever
// pattern was baked into FaceBaseColor by the per-face preview bake;
// any change to the underlying .mtlx must invalidate that cached
// clone.
func TestStickerStageKeyDependsOnMaterialX(t *testing.T) {
	c := NewStageCache()
	mtlxA := writeMtlxTempFile(t, "<materialx version=\"1.39\"/>")
	mtlxB := writeMtlxTempFile(t, "<materialx version=\"1.39\"><nodegraph/></materialx>")
	base := Options{
		Input:                    "model.glb",
		BaseColorMaterialX:       mtlxA,
		BaseColorMaterialXTileMM: 10,
		Stickers: []Sticker{
			{ImagePath: "sticker.png", Mode: "unfold", Scale: 1, MaxAngle: 90},
		},
	}
	pathChanged := base
	pathChanged.BaseColorMaterialX = mtlxB
	tileChanged := base
	tileChanged.BaseColorMaterialXTileMM = 20

	if c.stageFnv(StageSticker, base) == c.stageFnv(StageSticker, pathChanged) {
		t.Error("StageSticker key did not change when BaseColorMaterialX path changed; " +
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
	mtlxA := writeMtlxTempFile(t, "<materialx version=\"1.39\"/>")
	mtlxB := writeMtlxTempFile(t, "<materialx version=\"1.39\"><nodegraph/></materialx>")
	base := Options{
		Input:                                "model.glb",
		BaseColorMaterialX:                   mtlxA,
		BaseColorMaterialXTileMM:             10,
		BaseColorMaterialXTriplanarSharpness: 4,
	}
	changed := base
	changed.BaseColorMaterialX = mtlxB
	changed.BaseColorMaterialXTileMM = 20
	changed.BaseColorMaterialXTriplanarSharpness = 8

	if c.stageFnv(StageLoad, base) != c.stageFnv(StageLoad, changed) {
		t.Error("StageLoad key changed on MaterialX change; load cache should survive")
	}
	if c.stageFnv(StageDecimate, base) != c.stageFnv(StageDecimate, changed) {
		t.Error("StageDecimate key changed on MaterialX change; decimate cache should survive")
	}
}
