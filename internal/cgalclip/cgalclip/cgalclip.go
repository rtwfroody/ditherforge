// Package cgalclip is the CGO binding layer for CGAL's
// Polygon_mesh_processing::clip. The Go-side public API lives one
// directory up.
package cgalclip

/*
#cgo CXXFLAGS: -std=c++17 -O3 -DNDEBUG
#cgo darwin CXXFLAGS: -I/opt/homebrew/include -I/usr/local/include
#cgo linux LDFLAGS: -lgmp -lmpfr
#cgo darwin LDFLAGS: /opt/homebrew/lib/libmpfr.a /opt/homebrew/lib/libgmp.a
#include "clip.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// Clip cuts (vertices, faces) by the plane defined by normal·p == d
// and returns the kept half (where normal·p <= d). The output is a
// closed triangle mesh: cap surface is added automatically by CGAL's
// clip routine.
//
// Caller is responsible for ensuring the input mesh is reasonably
// well-formed (oriented or orientable triangle soup). Self-intersecting
// inputs surface a clear error rather than producing garbage.
func Clip(vertices [][3]float32, faces [][3]uint32, nx, ny, nz, d float64) ([][3]float32, [][3]uint32, error) {
	nv := len(vertices)
	nf := len(faces)
	if nv == 0 || nf == 0 {
		return nil, nil, fmt.Errorf("CGAL clip: input mesh is empty")
	}

	cVerts := make([]C.float, nv*3)
	for i, v := range vertices {
		cVerts[i*3] = C.float(v[0])
		cVerts[i*3+1] = C.float(v[1])
		cVerts[i*3+2] = C.float(v[2])
	}
	cFaces := make([]C.int, nf*3)
	for i, f := range faces {
		cFaces[i*3] = C.int(f[0])
		cFaces[i*3+1] = C.int(f[1])
		cFaces[i*3+2] = C.int(f[2])
	}

	r := C.cc_clip(
		&cVerts[0], C.int(nv),
		&cFaces[0], C.int(nf),
		C.double(nx), C.double(ny), C.double(nz), C.double(d))
	defer C.cc_free(r)

	if r.error != nil {
		return nil, nil, fmt.Errorf("CGAL clip: %s", C.GoString(r.error))
	}

	// The C side already returns an "empty mesh" error string when
	// either count is zero, so we just trust the inputs here.
	onv := int(r.num_vertices)
	onf := int(r.num_faces)

	outVerts := make([][3]float32, onv)
	rv := unsafe.Slice((*C.float)(unsafe.Pointer(r.vertices)), onv*3)
	for i := 0; i < onv; i++ {
		outVerts[i] = [3]float32{float32(rv[i*3]), float32(rv[i*3+1]), float32(rv[i*3+2])}
	}
	outFaces := make([][3]uint32, onf)
	rf := unsafe.Slice((*C.int)(unsafe.Pointer(r.faces)), onf*3)
	for i := 0; i < onf; i++ {
		outFaces[i] = [3]uint32{uint32(rf[i*3]), uint32(rf[i*3+1]), uint32(rf[i*3+2])}
	}
	return outVerts, outFaces, nil
}
