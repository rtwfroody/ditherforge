package voxel

import (
	"math"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// SpatialIndex is a 2D uniform grid for fast triangle lookup by XY position.
type SpatialIndex struct {
	cells    [][]int32
	TriZMin  []float32 // per-triangle Z min
	TriZMax  []float32 // per-triangle Z max
	minX     float32
	minY     float32
	cellSize float32
	cols     int
	rows     int
}

// NewSpatialIndex builds a spatial index over the model's triangles.
func NewSpatialIndex(model *loader.LoadedModel, cellSize float32) *SpatialIndex {
	if len(model.Vertices) == 0 {
		return &SpatialIndex{cellSize: cellSize}
	}

	minX, minY := float32(math.Inf(1)), float32(math.Inf(1))
	maxX, maxY := float32(math.Inf(-1)), float32(math.Inf(-1))
	for _, v := range model.Vertices {
		if v[0] < minX {
			minX = v[0]
		}
		if v[0] > maxX {
			maxX = v[0]
		}
		if v[1] < minY {
			minY = v[1]
		}
		if v[1] > maxY {
			maxY = v[1]
		}
	}

	minX -= cellSize
	minY -= cellSize
	maxX += cellSize
	maxY += cellSize

	cols := int(math.Ceil(float64(maxX-minX)/float64(cellSize))) + 1
	rows := int(math.Ceil(float64(maxY-minY)/float64(cellSize))) + 1

	nTris := len(model.Faces)
	si := &SpatialIndex{
		cells:    make([][]int32, cols*rows),
		TriZMin:  make([]float32, nTris),
		TriZMax:  make([]float32, nTris),
		minX:     minX,
		minY:     minY,
		cellSize: cellSize,
		cols:     cols,
		rows:     rows,
	}

	for fi, f := range model.Faces {
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]

		txMin := Minf(v0[0], Minf(v1[0], v2[0]))
		txMax := Maxf(v0[0], Maxf(v1[0], v2[0]))
		tyMin := Minf(v0[1], Minf(v1[1], v2[1]))
		tyMax := Maxf(v0[1], Maxf(v1[1], v2[1]))
		si.TriZMin[fi] = Minf(v0[2], Minf(v1[2], v2[2]))
		si.TriZMax[fi] = Maxf(v0[2], Maxf(v1[2], v2[2]))

		c0 := int((txMin - minX) / cellSize)
		c1 := int((txMax - minX) / cellSize)
		r0 := int((tyMin - minY) / cellSize)
		r1 := int((tyMax - minY) / cellSize)

		if c0 < 0 {
			c0 = 0
		}
		if r0 < 0 {
			r0 = 0
		}
		if c1 >= cols {
			c1 = cols - 1
		}
		if r1 >= rows {
			r1 = rows - 1
		}

		for c := c0; c <= c1; c++ {
			for r := r0; r <= r1; r++ {
				idx := r*cols + c
				si.cells[idx] = append(si.cells[idx], int32(fi))
			}
		}
	}

	return si
}

// Candidates returns triangle indices that might overlap the given XY point.
func (si *SpatialIndex) Candidates(x, y float32) []int32 {
	c := int((x - si.minX) / si.cellSize)
	r := int((y - si.minY) / si.cellSize)
	if c < 0 || c >= si.cols || r < 0 || r >= si.rows {
		return nil
	}
	return si.cells[r*si.cols+c]
}

// SearchBuf is a reusable buffer for spatial index queries, avoiding per-call
// map allocations. Each goroutine should have its own SearchBuf.
type SearchBuf struct {
	seen   []uint32
	gen    uint32
	result []int32
}

// NewSearchBuf creates a new search buffer for nTris triangles.
func NewSearchBuf(nTris int) *SearchBuf {
	return &SearchBuf{seen: make([]uint32, nTris)}
}

// CandidatesRadiusZ returns triangle indices from cells within XY radius whose
// Z range is within zRadius of the query Z.
func (si *SpatialIndex) CandidatesRadiusZ(x, y, xyRadius, z, zRadius float32, buf *SearchBuf) []int32 {
	c0 := int((x - xyRadius - si.minX) / si.cellSize)
	c1 := int((x + xyRadius - si.minX) / si.cellSize)
	r0 := int((y - xyRadius - si.minY) / si.cellSize)
	r1 := int((y + xyRadius - si.minY) / si.cellSize)

	if c0 < 0 {
		c0 = 0
	}
	if r0 < 0 {
		r0 = 0
	}
	if c1 >= si.cols {
		c1 = si.cols - 1
	}
	if r1 >= si.rows {
		r1 = si.rows - 1
	}

	buf.gen++
	if buf.gen == 0 {
		for i := range buf.seen {
			buf.seen[i] = 0
		}
		buf.gen = 1
	}
	buf.result = buf.result[:0]

	zLo := z - zRadius
	zHi := z + zRadius
	gen := buf.gen
	for c := c0; c <= c1; c++ {
		for r := r0; r <= r1; r++ {
			for _, ti := range si.cells[r*si.cols+c] {
				if buf.seen[ti] == gen {
					continue
				}
				buf.seen[ti] = gen
				if si.TriZMax[ti] < zLo || si.TriZMin[ti] > zHi {
					continue
				}
				buf.result = append(buf.result, ti)
			}
		}
	}
	return buf.result
}
