package pipeline

import (
	"bytes"
	"encoding/gob"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// loadOutput's two meshes alias each other when alpha-wrap is off.
// Gob serialization doesn't preserve pointer identity across struct
// fields, so a naive round-trip would emit two copies and decode them
// as two distinct pointers — a real correctness regression because
// applyBaseColor (pipeline.go) branches on whether these pointers are
// equal.
//
// The on-disk shape stores ColorModel always, plus a distinct copy of
// Model only when it differs from ColorModel. On decode, a nil
// ModelDistinct restores the alias.

type loadOutputOnDisk struct {
	ColorModel    *loader.LoadedModel
	ModelDistinct *loader.LoadedModel // nil = aliases ColorModel
	PreviewScale  float32
	ExtentMM      float32
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
		PreviewScale: lo.PreviewScale,
		ExtentMM:     lo.ExtentMM,
	}
	if lo.Model != lo.ColorModel {
		od.ModelDistinct = lo.Model
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
	lo.PreviewScale = od.PreviewScale
	lo.ExtentMM = od.ExtentMM
	if od.ModelDistinct != nil {
		lo.Model = od.ModelDistinct
	} else {
		lo.Model = lo.ColorModel
	}
	return nil
}
