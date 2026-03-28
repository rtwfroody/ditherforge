package voxel

import "math"

// SnapPos rounds a position to avoid floating-point dedup issues.
func SnapPos(p [3]float32) [3]float32 {
	const scale = 1e3
	return [3]float32{
		float32(math.Round(float64(p[0])*scale)) / scale,
		float32(math.Round(float64(p[1])*scale)) / scale,
		float32(math.Round(float64(p[2])*scale)) / scale,
	}
}

// VertexDedup deduplicates output vertices by snapped position.
type VertexDedup struct {
	Verts     [][3]float32
	vertexMap map[[3]float32]uint32
}

// NewVertexDedup creates a new vertex deduplicator.
func NewVertexDedup() *VertexDedup {
	return &VertexDedup{
		vertexMap: make(map[[3]float32]uint32),
	}
}

// GetVertex returns the index for the given position, creating it if needed.
func (vd *VertexDedup) GetVertex(pos [3]float32) uint32 {
	snapped := SnapPos(pos)
	if idx, ok := vd.vertexMap[snapped]; ok {
		return idx
	}
	idx := uint32(len(vd.Verts))
	vd.vertexMap[snapped] = idx
	vd.Verts = append(vd.Verts, pos)
	return idx
}
