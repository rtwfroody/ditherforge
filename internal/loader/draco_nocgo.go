//go:build !cgo

package loader

import (
	"fmt"

	"github.com/qmuntal/gltf"
)

// decodeDraco is a stub for builds without CGO (draco requires a C library).
// It detects Draco-compressed primitives and returns a clear error message.
func decodeDraco(doc *gltf.Document, prim *gltf.Primitive) ([][3]float32, [][2]float32, [][4]uint8, []uint32, bool) {
	if _, ok := prim.Extensions["KHR_draco_mesh_compression"]; ok {
		fmt.Println("Warning: this model uses Draco mesh compression, which is only supported on Linux builds. Skipping primitive.")
	}
	return nil, nil, nil, nil, false
}
