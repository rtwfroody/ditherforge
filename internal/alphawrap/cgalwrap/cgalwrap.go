package cgalwrap

/*
#cgo CXXFLAGS: -std=c++17 -O3 -DNDEBUG
#cgo darwin CXXFLAGS: -I/opt/homebrew/include -I/usr/local/include
#cgo linux LDFLAGS: -lgmp -lmpfr
#cgo darwin LDFLAGS: /opt/homebrew/lib/libmpfr.a /opt/homebrew/lib/libgmp.a
#include "wrap.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// AlphaWrap calls CGAL's alpha_wrap_3 on a triangle soup and returns the
// wrapped mesh as flat vertex/face arrays.
func AlphaWrap(vertices [][3]float32, faces [][3]uint32, alpha, offset float64) ([][3]float32, [][3]uint32, error) {
	nv := len(vertices)
	nf := len(faces)

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

	r := C.aw_alpha_wrap(
		&cVerts[0], C.int(nv),
		&cFaces[0], C.int(nf),
		C.double(alpha), C.double(offset))
	defer C.aw_free(r)

	if r.error != nil {
		return nil, nil, fmt.Errorf("CGAL alpha_wrap: %s", C.GoString(r.error))
	}

	onv := int(r.num_vertices)
	onf := int(r.num_faces)
	if onv == 0 || onf == 0 {
		return nil, nil, fmt.Errorf("CGAL alpha_wrap produced empty mesh")
	}

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
