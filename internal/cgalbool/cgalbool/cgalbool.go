// Package cgalbool is the CGO binding layer for CGAL's
// Polygon_mesh_processing::corefine_and_compute_{union,difference}.
// The Go-side public API lives one directory up.
package cgalbool

/*
#cgo CXXFLAGS: -std=c++17 -O3 -DNDEBUG
#cgo darwin CXXFLAGS: -I/opt/homebrew/include -I/usr/local/include
#cgo linux LDFLAGS: -lgmp -lmpfr
#cgo darwin LDFLAGS: /opt/homebrew/lib/libmpfr.a /opt/homebrew/lib/libgmp.a
#include "cgalbool.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// Op selects the boolean operation to perform.
type Op int

const (
	Union Op = iota
	Difference
)

// Compute runs the requested boolean op on (a, b). Inputs are triangle
// soups. Both must describe closed (or orientable) meshes. Returns the
// result as flat (vertices, faces) arrays.
func Compute(
	aVerts [][3]float32, aFaces [][3]uint32,
	bVerts [][3]float32, bFaces [][3]uint32,
	op Op,
) ([][3]float32, [][3]uint32, error) {
	if len(aVerts) == 0 || len(aFaces) == 0 {
		return nil, nil, fmt.Errorf("cgalbool: input A is empty")
	}
	if len(bVerts) == 0 || len(bFaces) == 0 {
		return nil, nil, fmt.Errorf("cgalbool: input B is empty")
	}

	aV := make([]C.float, len(aVerts)*3)
	for i, v := range aVerts {
		aV[i*3] = C.float(v[0])
		aV[i*3+1] = C.float(v[1])
		aV[i*3+2] = C.float(v[2])
	}
	aF := make([]C.int, len(aFaces)*3)
	for i, f := range aFaces {
		aF[i*3] = C.int(f[0])
		aF[i*3+1] = C.int(f[1])
		aF[i*3+2] = C.int(f[2])
	}
	bV := make([]C.float, len(bVerts)*3)
	for i, v := range bVerts {
		bV[i*3] = C.float(v[0])
		bV[i*3+1] = C.float(v[1])
		bV[i*3+2] = C.float(v[2])
	}
	bF := make([]C.int, len(bFaces)*3)
	for i, f := range bFaces {
		bF[i*3] = C.int(f[0])
		bF[i*3+1] = C.int(f[1])
		bF[i*3+2] = C.int(f[2])
	}

	var r C.struct_CResult
	switch op {
	case Union:
		r = C.cb_union(
			&aV[0], C.int(len(aVerts)), &aF[0], C.int(len(aFaces)),
			&bV[0], C.int(len(bVerts)), &bF[0], C.int(len(bFaces)))
	case Difference:
		r = C.cb_difference(
			&aV[0], C.int(len(aVerts)), &aF[0], C.int(len(aFaces)),
			&bV[0], C.int(len(bVerts)), &bF[0], C.int(len(bFaces)))
	default:
		return nil, nil, fmt.Errorf("cgalbool: unknown op %d", op)
	}
	defer C.cb_free(r)

	if r.error != nil {
		return nil, nil, fmt.Errorf("cgalbool: %s", C.GoString(r.error))
	}

	onv := int(r.num_vertices)
	onf := int(r.num_faces)
	outVerts := make([][3]float32, onv)
	rv := unsafe.Slice((*C.float)(unsafe.Pointer(r.vertices)), onv*3)
	for i := range onv {
		outVerts[i] = [3]float32{float32(rv[i*3]), float32(rv[i*3+1]), float32(rv[i*3+2])}
	}
	outFaces := make([][3]uint32, onf)
	rf := unsafe.Slice((*C.int)(unsafe.Pointer(r.faces)), onf*3)
	for i := range onf {
		outFaces[i] = [3]uint32{uint32(rf[i*3]), uint32(rf[i*3+1]), uint32(rf[i*3+2])}
	}
	return outVerts, outFaces, nil
}
