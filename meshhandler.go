//go:build amd64 || arm64 || 386 || arm

package main

import (
	"encoding/binary"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"unsafe"

	"github.com/rtwfroody/ditherforge/internal/pipeline"
)

// meshHandler serves mesh data as binary over HTTP. Mesh data is stored
// in memory and served at /mesh/{id}. The binary format is:
//
//	Header (20 bytes):
//	  uint32 nVertices  (number of float32 values, i.e. vertex count * 3)
//	  uint32 nFaces     (number of uint32 values, i.e. face count * 3)
//	  uint32 nColors    (number of uint16 values, i.e. face count * 3)
//	  uint32 nUVs       (number of float32 values, i.e. vertex count * 2; 0 if absent)
//	  uint32 nTexIdx    (number of int32 values, i.e. face count; 0 if absent)
//	Body:
//	  float32[nVertices]
//	  uint32[nFaces]
//	  uint16[nColors]
//	  float32[nUVs]       (omitted if nUVs == 0)
//	  int32[nTexIdx]      (omitted if nTexIdx == 0)
//	Textures:
//	  uint32 nTextures
//	  For each texture:
//	    uint32 length
//	    []byte base64-encoded JPEG data
func float32SliceToBytes(s []float32) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(s)*4)
}

func uint32SliceToBytes(s []uint32) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(s)*4)
}

func uint16SliceToBytes(s []uint16) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(s)*2)
}

func int32SliceToBytes(s []int32) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(s)*4)
}

type meshHandler struct {
	mu     sync.RWMutex
	meshes map[string]*pipeline.MeshData
	nextID int
}

func newMeshHandler() *meshHandler {
	return &meshHandler{meshes: make(map[string]*pipeline.MeshData)}
}

// Store saves mesh data and returns an ID that can be used to fetch it.
func (h *meshHandler) Store(mesh *pipeline.MeshData) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	id := fmt.Sprintf("m%d", h.nextID)
	h.meshes[id] = mesh
	return id
}

// Remove deletes stored mesh data.
func (h *meshHandler) Remove(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.meshes, id)
}

func (h *meshHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Expect path like /mesh/m123
	path := strings.TrimPrefix(r.URL.Path, "/mesh/")
	if path == r.URL.Path {
		http.NotFound(w, r)
		return
	}

	// Hold the read lock for the entire response to prevent Remove() from
	// freeing mesh data while we're writing it out.
	h.mu.RLock()
	defer h.mu.RUnlock()
	mesh, ok := h.meshes[path]
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")

	nVerts := uint32(len(mesh.Vertices))
	nFaces := uint32(len(mesh.Faces))
	nColors := uint32(len(mesh.FaceColors))
	nUVs := uint32(len(mesh.UVs))
	nTexIdx := uint32(len(mesh.FaceTextureIdx))

	// Header.
	var hdr [20]byte
	binary.LittleEndian.PutUint32(hdr[0:], nVerts)
	binary.LittleEndian.PutUint32(hdr[4:], nFaces)
	binary.LittleEndian.PutUint32(hdr[8:], nColors)
	binary.LittleEndian.PutUint32(hdr[12:], nUVs)
	binary.LittleEndian.PutUint32(hdr[16:], nTexIdx)
	w.Write(hdr[:])

	// Write typed slices as raw bytes (little-endian on LE platforms,
	// which covers all supported targets).
	w.Write(float32SliceToBytes(mesh.Vertices))
	w.Write(uint32SliceToBytes(mesh.Faces))
	w.Write(uint16SliceToBytes(mesh.FaceColors))
	w.Write(float32SliceToBytes(mesh.UVs))
	w.Write(int32SliceToBytes(mesh.FaceTextureIdx))

	// Textures (base64 strings).
	nTex := uint32(0)
	if mesh.Textures != nil {
		nTex = uint32(len(mesh.Textures))
	}
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], nTex)
	w.Write(tmp[:])
	for _, tex := range mesh.Textures {
		b := []byte(tex)
		binary.LittleEndian.PutUint32(tmp[:], uint32(len(b)))
		w.Write(tmp[:])
		w.Write(b)
	}
}
