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
//	Sticker overlay:
//	  uint32 hasStickerData  (0 or 1)
//	  If 1:
//	    uint32 nStickerUVs
//	    float32[nStickerUVs]   (per face-vertex sticker UVs)
//	    uint32 nStickerMask
//	    uint8[nStickerMask]    (per-face sticker mask)
//	    uint32 nStickerBounds
//	    float32[nStickerBounds] (per-face [minU,maxU,minV,maxV] atlas clamp bounds)
//	    uint32 atlasLen
//	    []byte base64-encoded PNG atlas
//	MaterialX preview atlas (optional, appended after sticker section):
//	  uint32 hasBaseColorAtlas  (0 or 1)
//	  If 1:
//	    uint32 width
//	    uint32 height
//	    uint32 nUVs                  (= nFaces*6, [u,v] per face-vertex)
//	    float32[nUVs]                normalized [0,1]
//	    uint32 imageLen
//	    []byte base64-encoded PNG    (with "png:" prefix)
//	Face alpha (optional, appended after MaterialX section):
//	  uint32 hasFaceAlpha          (0 or 1)
//	  If 1:
//	    uint32 nFaceAlpha          (= face count, one byte per face, 0..255)
//	    uint8[nFaceAlpha]
//	Face translucent flag (optional, appended after face alpha):
//	  uint32 hasFaceTranslucent    (0 or 1)
//	  If 1:
//	    uint32 nFaceTranslucent    (= face count, one byte per face, 0 or 1)
//	    uint8[nFaceTranslucent]
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

	// Sticker overlay section.
	if len(mesh.StickerUVs) > 0 && mesh.StickerAtlas != "" {
		binary.LittleEndian.PutUint32(tmp[:], 1) // hasStickerData
		w.Write(tmp[:])

		binary.LittleEndian.PutUint32(tmp[:], uint32(len(mesh.StickerUVs)))
		w.Write(tmp[:])
		w.Write(float32SliceToBytes(mesh.StickerUVs))

		binary.LittleEndian.PutUint32(tmp[:], uint32(len(mesh.StickerFaceMask)))
		w.Write(tmp[:])
		w.Write(mesh.StickerFaceMask)

		binary.LittleEndian.PutUint32(tmp[:], uint32(len(mesh.StickerBounds)))
		w.Write(tmp[:])
		w.Write(float32SliceToBytes(mesh.StickerBounds))

		atlasBytes := []byte(mesh.StickerAtlas)
		binary.LittleEndian.PutUint32(tmp[:], uint32(len(atlasBytes)))
		w.Write(tmp[:])
		w.Write(atlasBytes)
	} else {
		binary.LittleEndian.PutUint32(tmp[:], 0) // no sticker data
		w.Write(tmp[:])
	}

	// MaterialX preview atlas (optional). Image as base64 PNG/JPEG
	// (matches sticker atlas convention); per-face-vertex UVs
	// indexed alongside the unpacked face buffer the frontend builds.
	if atl := mesh.BaseColorAtlas; atl != nil && atl.Image != "" && len(atl.FaceVertexUVs) > 0 {
		binary.LittleEndian.PutUint32(tmp[:], 1) // hasBaseColorAtlas
		w.Write(tmp[:])
		binary.LittleEndian.PutUint32(tmp[:], uint32(atl.Width))
		w.Write(tmp[:])
		binary.LittleEndian.PutUint32(tmp[:], uint32(atl.Height))
		w.Write(tmp[:])
		binary.LittleEndian.PutUint32(tmp[:], uint32(len(atl.FaceVertexUVs)))
		w.Write(tmp[:])
		w.Write(float32SliceToBytes(atl.FaceVertexUVs))
		imgBytes := []byte(atl.Image)
		binary.LittleEndian.PutUint32(tmp[:], uint32(len(imgBytes)))
		w.Write(tmp[:])
		w.Write(imgBytes)
	} else {
		binary.LittleEndian.PutUint32(tmp[:], 0) // no atlas
		w.Write(tmp[:])
	}

	// Per-face alpha (optional). Carries continuous per-face alpha
	// for the input preview so the renderer can blend translucent
	// faces; texture alpha is handled per-pixel via the texture
	// itself.
	if len(mesh.FaceAlpha) > 0 {
		binary.LittleEndian.PutUint32(tmp[:], 1) // hasFaceAlpha
		w.Write(tmp[:])
		binary.LittleEndian.PutUint32(tmp[:], uint32(len(mesh.FaceAlpha)))
		w.Write(tmp[:])
		w.Write(mesh.FaceAlpha)
	} else {
		binary.LittleEndian.PutUint32(tmp[:], 0) // no per-face alpha
		w.Write(tmp[:])
	}

	// Per-face translucency flag (optional). Lets the renderer split
	// opaque from translucent faces into separate draw calls so
	// depth writes from opaque geometry are not lost.
	if len(mesh.FaceTranslucent) > 0 {
		binary.LittleEndian.PutUint32(tmp[:], 1) // hasFaceTranslucent
		w.Write(tmp[:])
		binary.LittleEndian.PutUint32(tmp[:], uint32(len(mesh.FaceTranslucent)))
		w.Write(tmp[:])
		w.Write(mesh.FaceTranslucent)
	} else {
		binary.LittleEndian.PutUint32(tmp[:], 0) // no translucency flag
		w.Write(tmp[:])
	}
}
