package pipeline

import (
	"bytes"
	"encoding/gob"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// loadOutput's three meshes alias each other in some configurations.
// Gob serialization doesn't preserve pointer identity across struct
// fields, so a naive round-trip would emit three copies and decode them
// as three distinct pointers — a real correctness regression because
// applyBaseColor (pipeline.go) and voxel.SampleNearestColorWithSticker
// (color.go) both branch on whether these pointers are equal.
//
// the Load stage body produces exactly these three configurations:
//
//   1. Alpha-wrap off:           Model == ColorModel == SampleModel
//   2. Alpha-wrap on, inflate:   Model != ColorModel, SampleModel != ColorModel,
//                                Model != SampleModel
//   3. Alpha-wrap on, no infl.:  Model != ColorModel, SampleModel == ColorModel
//
// The on-disk shape stores ColorModel always, plus distinct copies of
// Model and SampleModel only when they differ from ColorModel. On
// decode, nil distinct fields restore the alias.
//
// Invariant: SampleModel ∈ {Model, ColorModel}. The encoding does NOT
// handle a hypothetical "Model == SampleModel but != ColorModel"
// configuration — both fields would round-trip as distinct copies,
// silently losing the Model==SampleModel aliasing. If the Load stage body ever
// produces that configuration, this encoder needs a third alias bit.

type loadOutputOnDisk struct {
	ColorModel     *loader.LoadedModel
	ModelDistinct  *loader.LoadedModel // nil = aliases ColorModel
	SampleDistinct *loader.LoadedModel // nil = aliases ColorModel
	InputMesh      *MeshData
	PreviewScale   float32
	ExtentMM       float32
	// The applied-base-color triple (appliedBaseColor,
	// appliedBaseColorMaterialX, appliedBaseColorMaterialXTileMM) is
	// intentionally not persisted: cache.set is called inside
	// the Load stage body (before applyBaseColor runs), so the disk version's
	// "applied" state is always pristine. On disk hit applyBaseColor
	// sees the pristine triple and skips the reset-from-parse path
	// (since lo.ColorModel is already pristine); only an in-session
	// base-color change after lo's been mutated triggers a reset, and
	// that path is satisfied by the in-memory parse cache.
}

func (lo *loadOutput) GobEncode() ([]byte, error) {
	od := loadOutputOnDisk{
		ColorModel:   lo.ColorModel,
		InputMesh:    lo.InputMesh,
		PreviewScale: lo.PreviewScale,
		ExtentMM:     lo.ExtentMM,
	}
	if lo.Model != lo.ColorModel {
		od.ModelDistinct = lo.Model
	}
	if lo.SampleModel != lo.ColorModel {
		od.SampleDistinct = lo.SampleModel
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(od); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (lo *loadOutput) GobDecode(data []byte) error {
	var od loadOutputOnDisk
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&od); err != nil {
		return err
	}
	lo.ColorModel = od.ColorModel
	lo.InputMesh = od.InputMesh
	lo.PreviewScale = od.PreviewScale
	lo.ExtentMM = od.ExtentMM
	if od.ModelDistinct != nil {
		lo.Model = od.ModelDistinct
	} else {
		lo.Model = lo.ColorModel
	}
	if od.SampleDistinct != nil {
		lo.SampleModel = od.SampleDistinct
	} else {
		lo.SampleModel = lo.ColorModel
	}
	return nil
}
