// Package rectclip is the CGO binding for Clipper2's RectClip64 —
// a specialised polygon-vs-axis-aligned-rectangle clipper. The
// Go-facing API lives one directory up.
package rectclip

/*
#cgo CXXFLAGS: -std=c++17 -O3 -DNDEBUG -I${SRCDIR}/clipper2
#include "rectclip.h"
*/
import "C"

import (
	"runtime"
	"unsafe"
)

// State holds a cached set of polygon paths in C++ memory. Call
// SetPaths to load the polygon once, then Clip many times against
// it (one per cap-tile rect). Call Free when done.
type State struct {
	p *C.rc_state
}

// New allocates a fresh State.
func New() *State {
	s := &State{p: C.rc_new()}
	runtime.SetFinalizer(s, func(s *State) {
		if s.p != nil {
			C.rc_free(s.p)
			s.p = nil
		}
	})
	return s
}

// Free releases the underlying C++ state. After Free, no other
// method may be called on this State.
func (s *State) Free() {
	if s.p == nil {
		return
	}
	C.rc_free(s.p)
	s.p = nil
	runtime.SetFinalizer(s, nil)
}

// SetPaths replaces the cached polygon with `paths`. Each path is
// a closed loop in int64 coordinates; don't repeat the first point
// at the end.
func (s *State) SetPaths(paths [][][2]int64) {
	total := 0
	for _, p := range paths {
		total += len(p)
	}
	if len(paths) == 0 || total == 0 {
		C.rc_set_paths(s.p, 0, nil, 0, nil)
		return
	}
	sizes := make([]C.int32_t, len(paths))
	pts := make([]C.int64_t, total*2)
	off := 0
	for i, p := range paths {
		sizes[i] = C.int32_t(len(p))
		for j, xy := range p {
			pts[(off+j)*2] = C.int64_t(xy[0])
			pts[(off+j)*2+1] = C.int64_t(xy[1])
		}
		off += len(p)
	}
	C.rc_set_paths(s.p,
		C.int(len(paths)), &sizes[0],
		C.int(total), &pts[0])
}

// Clip clips the cached polygon against the rect [x0..x1] × [y0..y1]
// and returns the resulting paths.
func (s *State) Clip(x0, y0, x1, y1 int64) [][][2]int64 {
	r := C.rc_clip(s.p,
		C.int64_t(x0), C.int64_t(y0),
		C.int64_t(x1), C.int64_t(y1))
	defer C.rc_free_result(r)
	if r.num_paths == 0 {
		return nil
	}
	sizes := unsafe.Slice((*int32)(unsafe.Pointer(r.path_sizes)), int(r.num_paths))
	pts := unsafe.Slice((*int64)(unsafe.Pointer(r.points)), int(r.total_points)*2)
	out := make([][][2]int64, r.num_paths)
	off := 0
	for i, n := range sizes {
		path := make([][2]int64, n)
		for j := 0; j < int(n); j++ {
			path[j] = [2]int64{pts[(off+j)*2], pts[(off+j)*2+1]}
		}
		out[i] = path
		off += int(n)
	}
	return out
}
