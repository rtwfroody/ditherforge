// Package manifoldbool is a cgo binding around Manifold's C ABI
// (libmanifoldc) for the operations the ditherforge cellslicer needs:
// build a Manifold from a triangle mesh, extrude a 2D polygon into a
// prism, translate, intersect, and read back the result mesh.
//
// Manifold (https://github.com/elalish/manifold) is "exact predicates,
// approximate construction" — it preserves topology aggressively and
// guarantees the output of a Boolean op is itself a closed orientable
// 2-manifold. That's the property we want from the cellslicer clip
// stage: today the bespoke splice/earcut path produces stitched output
// with thousands of boundary edges; Manifold's intersection cannot.
//
// Inputs must be watertight. Position-dedup at 1µm (see the cellslicer
// Quantize / Snap API) is normally enough to satisfy that — the
// dominant failure mode is texture-UV-seam vertex duplicates that
// share a position but live as separate vertices.
//
// Memory management: every result object owns a heap buffer of
// manifold_*_size() bytes allocated via C.malloc. Close() runs the
// matching manifold_destruct_* (in-place destructor) and frees the
// buffer. Garbage-collecting the Go wrapper without Close() leaks
// roughly that many bytes per object, so the cellslicer code uses
// defer Close() on every Manifold it creates.
package manifoldbool

/*
#cgo LDFLAGS: -lmanifoldc -lmanifold
#cgo CFLAGS: -O2
#include <stdlib.h>
#include <manifold/manifoldc.h>
*/
import "C"

import (
	"fmt"
	"runtime"
	"unsafe"
)

// Manifold owns a libmanifoldc ManifoldManifold object plus the
// caller-allocated memory backing it. Close() is mandatory.
type Manifold struct {
	ptr *C.ManifoldManifold
	mem unsafe.Pointer
}

// Close runs the in-place C++ destructor (when ptr is set) and frees
// the backing buffer (when mem is set). Safe to call multiple times;
// nil-safe. ptr and mem are released independently so a constructor
// that allocated mem but failed to populate ptr — libmanifoldc can
// return NULL pair members from manifold_split_* under degenerate
// inputs — still releases the buffer rather than leaking it.
func (m *Manifold) Close() {
	if m == nil {
		return
	}
	if m.ptr != nil {
		C.manifold_destruct_manifold(m.ptr)
		m.ptr = nil
	}
	if m.mem != nil {
		C.free(m.mem)
		m.mem = nil
	}
}

// alloc returns a fresh manifold_manifold_size() buffer. Callers wrap
// it in &Manifold{ptr, mem} after invoking a libmanifoldc constructor.
func alloc() unsafe.Pointer {
	return C.malloc(C.manifold_manifold_size())
}

// FromMesh builds a Manifold from a triangle mesh and tags it with a
// fresh original-mesh ID via manifold_reserve_ids. The ID is
// retrievable through OriginalID() and propagates to per-face
// run_original_id entries in any Boolean's output MeshGL — which is
// how ToMeshFiltered can recover surface-only output after an
// Intersection.
//
// Verts is Nx3 in XYZ; faces are CCW. The mesh must be 2-manifold
// once positions are merged at the precision Manifold expects
// (essentially exact float32 match) — see the package doc note about
// 1µm position-dedup.
//
// Returns an error (non-NoError status) if Manifold rejects the input.
func FromMesh(verts [][3]float32, faces [][3]uint32) (*Manifold, error) {
	if len(verts) == 0 || len(faces) == 0 {
		return nil, fmt.Errorf("manifoldbool: empty mesh input")
	}

	flatV := make([]C.float, len(verts)*3)
	for i, v := range verts {
		flatV[i*3] = C.float(v[0])
		flatV[i*3+1] = C.float(v[1])
		flatV[i*3+2] = C.float(v[2])
	}
	flatF := make([]C.uint32_t, len(faces)*3)
	for i, f := range faces {
		flatF[i*3] = C.uint32_t(f[0])
		flatF[i*3+1] = C.uint32_t(f[1])
		flatF[i*3+2] = C.uint32_t(f[2])
	}

	// Reserve a fresh original-mesh ID and stamp every triangle with
	// it via a single "run" spanning all faces. Without this the
	// Manifold's OriginalID() is -1 (derived) and Boolean output has
	// no run-original-id info — ToMeshFiltered then drops everything.
	//
	// The small arrays referenced by ManifoldMeshGLOptions must live
	// in C memory: cgo prohibits passing a Go pointer that itself
	// contains a Go pointer (the *options* struct on the Go side
	// would carry pointers into Go slices). C.malloc keeps them in
	// non-moving memory for the duration of the call.
	reservedID := C.manifold_reserve_ids(1)
	runIdxC := (*C.uint32_t)(C.malloc(C.size_t(2 * 4)))
	defer C.free(unsafe.Pointer(runIdxC))
	unsafe.Slice(runIdxC, 2)[0] = 0
	unsafe.Slice(runIdxC, 2)[1] = C.uint32_t(len(faces) * 3)
	runOrigIDC := (*C.uint32_t)(C.malloc(C.size_t(1 * 4)))
	defer C.free(unsafe.Pointer(runOrigIDC))
	unsafe.Slice(runOrigIDC, 1)[0] = reservedID
	optsMem := C.malloc(C.size_t(unsafe.Sizeof(C.ManifoldMeshGLOptions{})))
	defer C.free(optsMem)
	opts := (*C.ManifoldMeshGLOptions)(optsMem)
	*opts = C.ManifoldMeshGLOptions{} // zero out
	opts.run_indices = runIdxC
	opts.run_indices_length = 2
	opts.run_original_ids = runOrigIDC
	opts.run_original_ids_length = 1

	meshMem := C.malloc(C.manifold_meshgl_size())
	defer C.free(meshMem)
	mg := C.manifold_meshgl_w_options(
		meshMem,
		(*C.float)(unsafe.Pointer(&flatV[0])),
		C.size_t(len(verts)),
		3,
		(*C.uint32_t)(unsafe.Pointer(&flatF[0])),
		C.size_t(len(faces)),
		opts,
	)
	defer C.manifold_destruct_meshgl(mg)
	runtime.KeepAlive(flatV)
	runtime.KeepAlive(flatF)

	mem := alloc()
	ptr := C.manifold_of_meshgl(mem, mg)
	m := &Manifold{ptr: ptr, mem: mem}
	if err := m.statusErr(); err != nil {
		m.Close()
		return nil, err
	}
	// Promote to "original" — assigns a fresh original_id derived
	// from manifoldID, and that ID propagates to per-face
	// run_original_id entries in any Boolean output. Without this
	// the Manifold returned from manifold_of_meshgl carries
	// originalID=-1 (derived), which would defeat ToMeshFiltered.
	asOrig := m.asOriginal()
	m.Close()
	return asOrig, nil
}

// asOriginal returns a fresh Manifold equivalent to m but with the
// original_id set to m's manifoldID, so this becomes the "root" tag
// for downstream boolean ops. Caller takes ownership of the returned
// Manifold; the receiver is unchanged (but no longer load-bearing
// for this lineage).
func (m *Manifold) asOriginal() *Manifold {
	mem := alloc()
	ptr := C.manifold_as_original(mem, m.ptr)
	return &Manifold{ptr: ptr, mem: mem}
}

// ExtrudePolygon extrudes a single CCW outer polygon between zBot and
// zTop. No holes (the cellslicer cells are simply connected).
func ExtrudePolygon(poly [][2]float32, zBot, zTop float32) (*Manifold, error) {
	if len(poly) < 3 {
		return nil, fmt.Errorf("manifoldbool: polygon needs ≥3 points, got %d", len(poly))
	}
	if zTop <= zBot {
		return nil, fmt.Errorf("manifoldbool: zTop %g must exceed zBot %g", zTop, zBot)
	}

	// All polygon data lives in C memory: manifold_simple_polygon
	// and manifold_polygons may retain references to the supplied
	// arrays past the call's return, and cgo's pointer rules forbid
	// passing Go memory that contains nested pointers (the polygons
	// wrapper would carry a Go pointer to a Go pointer). C.malloc
	// keeps the arrays in non-moving memory and out of the cgo
	// pinning checker's way.
	ptsC := (*C.ManifoldVec2)(C.malloc(C.size_t(len(poly)) * C.size_t(unsafe.Sizeof(C.ManifoldVec2{}))))
	defer C.free(unsafe.Pointer(ptsC))
	ptsSlice := unsafe.Slice(ptsC, len(poly))
	for i, p := range poly {
		ptsSlice[i].x = C.double(p[0])
		ptsSlice[i].y = C.double(p[1])
	}
	spMem := C.malloc(C.manifold_simple_polygon_size())
	defer C.free(spMem)
	sp := C.manifold_simple_polygon(spMem, ptsC, C.size_t(len(poly)))
	defer C.manifold_destruct_simple_polygon(sp)

	psMem := C.malloc(C.manifold_polygons_size())
	defer C.free(psMem)
	simplePtrsC := (**C.ManifoldSimplePolygon)(C.malloc(C.size_t(unsafe.Sizeof(sp))))
	defer C.free(unsafe.Pointer(simplePtrsC))
	*simplePtrsC = sp
	ps := C.manifold_polygons(psMem, simplePtrsC, C.size_t(1))
	defer C.manifold_destruct_polygons(ps)

	height := C.double(zTop - zBot)
	extMem := alloc()
	extPtr := C.manifold_extrude(extMem, ps, height, 0, 0.0, 1.0, 1.0)
	ext := &Manifold{ptr: extPtr, mem: extMem}
	if err := ext.statusErr(); err != nil {
		ext.Close()
		return nil, err
	}
	if zBot != 0 {
		shifted := ext.translate(0, 0, float64(zBot))
		ext.Close()
		return shifted, nil
	}
	return ext, nil
}

// translate returns a fresh Manifold positioned at (dx, dy, dz). The
// receiver is unchanged.
func (m *Manifold) translate(dx, dy, dz float64) *Manifold {
	mem := alloc()
	ptr := C.manifold_translate(mem, m.ptr, C.double(dx), C.double(dy), C.double(dz))
	return &Manifold{ptr: ptr, mem: mem}
}

// Intersection returns a ∩ b. Both inputs survive the call. Returns
// an empty Manifold (NumTri==0) when the intersection is empty.
func Intersection(a, b *Manifold) (*Manifold, error) {
	mem := alloc()
	ptr := C.manifold_intersection(mem, a.ptr, b.ptr)
	out := &Manifold{ptr: ptr, mem: mem}
	if err := out.statusErr(); err != nil {
		out.Close()
		return nil, err
	}
	return out, nil
}

// SplitByPlane splits m at the plane normal·p = offset. Returns
// (above, below) where above contains the portion with normal·p > offset
// and below contains normal·p < offset. New "cut" faces created on the
// plane carry a fresh original-mesh ID, distinct from any per-face ID
// inherited from m — so a downstream ToMeshFiltered(m.OriginalID())
// still recovers only m's original surface.
//
// Either return value may be empty (NumTri==0) if m lies entirely on
// one side of the plane. The caller owns both returned Manifolds and
// must Close() each. The input m survives the call.
func SplitByPlane(m *Manifold, nx, ny, nz, offset float64) (*Manifold, *Manifold, error) {
	if m == nil || m.ptr == nil {
		return nil, nil, fmt.Errorf("manifoldbool: SplitByPlane on nil Manifold")
	}
	memAbove := alloc()
	memBelow := alloc()
	pair := C.manifold_split_by_plane(memAbove, memBelow, m.ptr,
		C.double(nx), C.double(ny), C.double(nz), C.double(offset))
	above := &Manifold{ptr: pair.first, mem: memAbove}
	below := &Manifold{ptr: pair.second, mem: memBelow}
	if err := above.statusErr(); err != nil {
		above.Close()
		below.Close()
		return nil, nil, err
	}
	if err := below.statusErr(); err != nil {
		above.Close()
		below.Close()
		return nil, nil, err
	}
	return above, below, nil
}

// NumTri returns the triangle count of the underlying mesh.
func (m *Manifold) NumTri() int {
	if m == nil || m.ptr == nil {
		return 0
	}
	return int(C.manifold_num_tri(m.ptr))
}

// NumVert returns the vertex count.
func (m *Manifold) NumVert() int {
	if m == nil || m.ptr == nil {
		return 0
	}
	return int(C.manifold_num_vert(m.ptr))
}

// IsEmpty reports whether the Manifold has zero geometry. Empty
// Manifolds are the valid signal that an Intersection had no overlap.
func (m *Manifold) IsEmpty() bool {
	if m == nil || m.ptr == nil {
		return true
	}
	return C.manifold_is_empty(m.ptr) != 0
}

// Volume returns the enclosed signed volume in mm³ (positive for
// outward-oriented Manifolds).
func (m *Manifold) Volume() float64 {
	if m == nil || m.ptr == nil {
		return 0
	}
	return float64(C.manifold_volume(m.ptr))
}

// OriginalID returns the manifold's original-mesh tag assigned at
// FromMesh / FromMeshGL construction. The value is auto-incremented
// per call to manifold_of_meshgl, so two FromMesh calls produce
// distinct tags. Boolean ops preserve these tags on output faces via
// MeshGL's run_index / run_original_id arrays — passing the source's
// OriginalID to ToMeshFiltered yields just the faces inherited from
// that input, which is how we strip cell-prism walls from the
// intersection result and recover a surface-only output.
func (m *Manifold) OriginalID() int32 {
	if m == nil || m.ptr == nil {
		return -1
	}
	return int32(C.manifold_original_id(m.ptr))
}

// ToMesh extracts the full triangle mesh. Returns (nil, nil) for an
// empty Manifold.
func (m *Manifold) ToMesh() ([][3]float32, [][3]uint32) {
	return m.toMesh(-1)
}

// ToMeshFiltered extracts only the faces whose run_original_id ==
// keepID (typically the OriginalID() of an input Manifold). Vertices
// are restricted to those referenced by kept faces and re-indexed
// densely. Returns (nil, nil) when no face matches.
//
// This is the surface-only extraction we need after Intersection:
// boolean output is a closed solid that includes the *clipper*'s
// walls cut by the source; keepID=source.OriginalID() drops those
// walls and leaves just the source's surface inside the clipper.
func (m *Manifold) ToMeshFiltered(keepID int32) ([][3]float32, [][3]uint32) {
	return m.toMesh(keepID)
}

// toMesh is the shared backend. keepID < 0 means keep everything;
// otherwise faces whose run's original_id != keepID are dropped.
func (m *Manifold) toMesh(keepID int32) ([][3]float32, [][3]uint32) {
	if m == nil || m.ptr == nil || m.IsEmpty() {
		return nil, nil
	}
	mgMem := C.malloc(C.manifold_meshgl_size())
	defer C.free(mgMem)
	mg := C.manifold_get_meshgl(mgMem, m.ptr)
	defer C.manifold_destruct_meshgl(mg)

	nv := int(C.manifold_meshgl_num_vert(mg))
	nt := int(C.manifold_meshgl_num_tri(mg))
	nprop := int(C.manifold_meshgl_num_prop(mg))
	if nv == 0 || nt == 0 {
		return nil, nil
	}

	// manifold_meshgl_* getters return the data pointer; the void*
	// mem argument holds a small wrapper struct, NOT the data
	// buffer. Read from the returned pointer.
	vpMem := C.malloc(C.size_t(nv * nprop * 4))
	defer C.free(vpMem)
	vpPtr := C.manifold_meshgl_vert_properties(vpMem, mg)
	flatV := unsafe.Slice((*C.float)(vpPtr), nv*nprop)

	tvMem := C.malloc(C.size_t(nt * 3 * 4))
	defer C.free(tvMem)
	tvPtr := C.manifold_meshgl_tri_verts(tvMem, mg)
	flatF := unsafe.Slice((*C.uint32_t)(tvPtr), nt*3)

	// Determine the keep-mask over faces. The default branch
	// (keepID < 0) is the legacy "keep everything" mode.
	keepFace := make([]bool, nt)
	if keepID < 0 {
		for i := range keepFace {
			keepFace[i] = true
		}
	} else {
		nRuns := int(C.manifold_meshgl_run_original_id_length(mg))
		if nRuns == 0 {
			// No run info recorded — fall through and keep nothing,
			// the caller will see an empty result and can debug.
		} else {
			riMem := C.malloc(C.size_t((nRuns + 1) * 4))
			defer C.free(riMem)
			riPtr := C.manifold_meshgl_run_index(riMem, mg)
			runIdx := unsafe.Slice((*C.uint32_t)(riPtr), nRuns+1)

			oidMem := C.malloc(C.size_t(nRuns * 4))
			defer C.free(oidMem)
			oidPtr := C.manifold_meshgl_run_original_id(oidMem, mg)
			origIDs := unsafe.Slice((*C.uint32_t)(oidPtr), nRuns)

			// runIdx is in *index* units (3 indices per triangle).
			// Convert to triangle ranges.
			for r := 0; r < nRuns; r++ {
				if int32(origIDs[r]) != keepID {
					continue
				}
				fStart := int(runIdx[r]) / 3
				fEnd := int(runIdx[r+1]) / 3
				for fi := fStart; fi < fEnd; fi++ {
					keepFace[fi] = true
				}
			}
		}
	}

	// Build the output, re-indexing vertices to be dense.
	remap := make([]int32, nv)
	for i := range remap {
		remap[i] = -1
	}
	verts := make([][3]float32, 0, nv)
	getVert := func(vi int) uint32 {
		if remap[vi] >= 0 {
			return uint32(remap[vi])
		}
		off := vi * nprop
		verts = append(verts, [3]float32{
			float32(flatV[off]),
			float32(flatV[off+1]),
			float32(flatV[off+2]),
		})
		remap[vi] = int32(len(verts) - 1)
		return uint32(remap[vi])
	}

	faces := make([][3]uint32, 0, nt)
	for i := 0; i < nt; i++ {
		if !keepFace[i] {
			continue
		}
		a := getVert(int(flatF[i*3]))
		b := getVert(int(flatF[i*3+1]))
		c := getVert(int(flatF[i*3+2]))
		faces = append(faces, [3]uint32{a, b, c})
	}
	if len(faces) == 0 {
		return nil, nil
	}
	return verts, faces
}

// statusErr converts a non-NoError status into a Go error. Returns
// nil for NoError (the happy path).
func (m *Manifold) statusErr() error {
	if m == nil || m.ptr == nil {
		return fmt.Errorf("manifoldbool: nil Manifold")
	}
	switch C.manifold_status(m.ptr) {
	case C.MANIFOLD_NO_ERROR:
		return nil
	case C.MANIFOLD_NON_FINITE_VERTEX:
		return fmt.Errorf("manifoldbool: non-finite vertex")
	case C.MANIFOLD_NOT_MANIFOLD:
		return fmt.Errorf("manifoldbool: input is not a 2-manifold")
	case C.MANIFOLD_VERTEX_INDEX_OUT_OF_BOUNDS:
		return fmt.Errorf("manifoldbool: vertex index out of bounds")
	case C.MANIFOLD_PROPERTIES_WRONG_LENGTH:
		return fmt.Errorf("manifoldbool: properties array wrong length")
	case C.MANIFOLD_MISSING_POSITION_PROPERTIES:
		return fmt.Errorf("manifoldbool: missing position properties")
	case C.MANIFOLD_MERGE_VECTORS_DIFFERENT_LENGTHS:
		return fmt.Errorf("manifoldbool: merge vectors of different lengths")
	case C.MANIFOLD_MERGE_INDEX_OUT_OF_BOUNDS:
		return fmt.Errorf("manifoldbool: merge index out of bounds")
	case C.MANIFOLD_TRANSFORM_WRONG_LENGTH:
		return fmt.Errorf("manifoldbool: transform wrong length")
	case C.MANIFOLD_RUN_INDEX_WRONG_LENGTH:
		return fmt.Errorf("manifoldbool: run index wrong length")
	case C.MANIFOLD_FACE_ID_WRONG_LENGTH:
		return fmt.Errorf("manifoldbool: face id wrong length")
	case C.MANIFOLD_INVALID_CONSTRUCTION:
		return fmt.Errorf("manifoldbool: invalid construction")
	case C.MANIFOLD_RESULT_TOO_LARGE:
		return fmt.Errorf("manifoldbool: result too large")
	default:
		return fmt.Errorf("manifoldbool: unknown error %d", C.manifold_status(m.ptr))
	}
}
