//go:build !draco

package loader

import (
	"github.com/qmuntal/gltf"
)

// decodeDraco is a stub for builds without the draco tag.
// Always returns false; the caller tracks skipped Draco primitives.
func decodeDraco(doc *gltf.Document, prim *gltf.Primitive) ([][3]float32, [][2]float32, [][4]uint8, []uint32, bool) {
	return nil, nil, nil, nil, false
}
