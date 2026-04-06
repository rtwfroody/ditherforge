//go:build !draco

package loader

import (
	"fmt"
	"os"
	"sync"

	"github.com/qmuntal/gltf"
)

var dracoWarningOnce sync.Once

// decodeDraco is a stub for builds without CGO (draco requires a C library).
// It detects Draco-compressed primitives and returns a clear error message.
func decodeDraco(doc *gltf.Document, prim *gltf.Primitive) ([][3]float32, [][2]float32, [][4]uint8, []uint32, bool) {
	if _, ok := prim.Extensions["KHR_draco_mesh_compression"]; ok {
		dracoWarningOnce.Do(func() {
			fmt.Fprintln(os.Stderr, "Warning: this model uses Draco mesh compression, which requires building with -tags draco. Skipping Draco primitives.")
		})
	}
	return nil, nil, nil, nil, false
}
