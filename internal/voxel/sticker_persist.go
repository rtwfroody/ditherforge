package voxel

import (
	"bytes"
	"encoding/gob"

	"github.com/rtwfroody/ditherforge/internal/imageraw"
)

// stickerDecalOnDisk is the on-disk gob form of StickerDecal.
type stickerDecalOnDisk struct {
	Image        imageraw.Tex
	TriUVs       map[int32][3][2]float32
	LSCMResidual float64
}

// GobEncode lets gob serialize a *StickerDecal.
func (d *StickerDecal) GobEncode() ([]byte, error) {
	od := stickerDecalOnDisk{
		Image:        imageraw.FromImage(d.Image),
		TriUVs:       d.TriUVs,
		LSCMResidual: d.LSCMResidual,
	}
	var out bytes.Buffer
	if err := gob.NewEncoder(&out).Encode(od); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// GobDecode lets gob deserialize a *StickerDecal. Image comes back as a
// concrete *image.NRGBA.
func (d *StickerDecal) GobDecode(data []byte) error {
	var od stickerDecalOnDisk
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&od); err != nil {
		return err
	}
	d.TriUVs = od.TriUVs
	d.LSCMResidual = od.LSCMResidual
	d.Image = imageraw.ToImage(od.Image)
	return nil
}
