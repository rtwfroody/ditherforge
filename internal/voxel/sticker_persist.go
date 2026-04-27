package voxel

import (
	"bytes"
	"encoding/gob"
	"image"
	"image/png"
)

// stickerDecalOnDisk is the on-disk gob form of StickerDecal. The Image
// field is replaced by PNG bytes since gob can't serialize image.Image.
type stickerDecalOnDisk struct {
	ImagePNG     []byte
	TriUVs       map[int32][3][2]float32
	LSCMResidual float64
}

// GobEncode lets gob serialize a *StickerDecal. Image is PNG-encoded.
func (d *StickerDecal) GobEncode() ([]byte, error) {
	od := stickerDecalOnDisk{
		TriUVs:       d.TriUVs,
		LSCMResidual: d.LSCMResidual,
	}
	if d.Image != nil {
		var buf bytes.Buffer
		if err := png.Encode(&buf, d.Image); err != nil {
			return nil, err
		}
		od.ImagePNG = buf.Bytes()
	}
	var out bytes.Buffer
	if err := gob.NewEncoder(&out).Encode(od); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// GobDecode lets gob deserialize a *StickerDecal. The image is decoded from
// the PNG bytes back to a concrete image type (whatever png.Decode picks).
func (d *StickerDecal) GobDecode(data []byte) error {
	var od stickerDecalOnDisk
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&od); err != nil {
		return err
	}
	d.TriUVs = od.TriUVs
	d.LSCMResidual = od.LSCMResidual
	if len(od.ImagePNG) == 0 {
		d.Image = nil
		return nil
	}
	img, err := png.Decode(bytes.NewReader(od.ImagePNG))
	if err != nil {
		return err
	}
	d.Image = img
	return nil
}

// Compile-time check: image.Image is concrete enough for png.Decode → gob
// roundtrip.
var _ image.Image = (*image.NRGBA)(nil)
