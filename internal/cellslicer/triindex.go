package cellslicer

import (
	"math"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// TriXYZIndex is a uniform XY grid over a model's triangle bboxes
// plus per-triangle Z bounds, used by the cellslicer Clip stage to
// rapidly find candidate triangles for each cell's prism. Each grid
// cell holds the triangle indices whose XY bbox overlaps that grid
// cell. The Z component is checked per-triangle at query time.
//
// CellSize should be on the same order as the cell partition's
// cellSize (or larger) — much smaller than the model's largest tri
// edge would cause big tris to populate many cells.
type TriXYZIndex struct {
	Model    *loader.LoadedModel
	CellSize float32
	MinX     float32
	MinY     float32
	Cols     int
	Rows     int
	// Cells is a flat row-major grid of triangle index lists.
	// Cells[row*Cols+col] = triangle indices whose XY bbox covers
	// that grid cell.
	Cells [][]int32
	// TriZMin / TriZMax are the per-triangle Z bounds (parallel to
	// Model.Faces). Caller pre-filters by these before issuing
	// candidate sets.
	TriZMin []float32
	TriZMax []float32
}

// NewTriXYZIndex builds a TriXYZIndex over model. cellSize sets the
// grid pitch; pass the cellslicer's cellSize (or a small multiple
// for sparser indexing). A typical model has a few hundred to a few
// thousand triangles; the grid is built O(N) with N = face count.
func NewTriXYZIndex(model *loader.LoadedModel, cellSize float32) *TriXYZIndex {
	if cellSize <= 0 {
		cellSize = 1
	}
	if model == nil || len(model.Faces) == 0 {
		return &TriXYZIndex{Model: model, CellSize: cellSize}
	}
	minX, minY, maxX, maxY := float32(math.Inf(1)), float32(math.Inf(1)), float32(math.Inf(-1)), float32(math.Inf(-1))
	for _, f := range model.Faces {
		for _, vi := range f {
			v := model.Vertices[vi]
			if v[0] < minX {
				minX = v[0]
			}
			if v[1] < minY {
				minY = v[1]
			}
			if v[0] > maxX {
				maxX = v[0]
			}
			if v[1] > maxY {
				maxY = v[1]
			}
		}
	}
	// Cap the grid at a reasonable resolution to avoid OOM on
	// pathological models.
	cols := int(math.Ceil(float64((maxX-minX)/cellSize))) + 1
	rows := int(math.Ceil(float64((maxY-minY)/cellSize))) + 1
	const maxDim = 4096
	if cols > maxDim {
		cols = maxDim
	}
	if rows > maxDim {
		rows = maxDim
	}
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	idx := &TriXYZIndex{
		Model:    model,
		CellSize: cellSize,
		MinX:     minX,
		MinY:     minY,
		Cols:     cols,
		Rows:     rows,
		Cells:    make([][]int32, cols*rows),
		TriZMin:  make([]float32, len(model.Faces)),
		TriZMax:  make([]float32, len(model.Faces)),
	}
	for ti, f := range model.Faces {
		v0, v1, v2 := model.Vertices[f[0]], model.Vertices[f[1]], model.Vertices[f[2]]
		txMin := minf3(v0[0], v1[0], v2[0])
		txMax := maxf3(v0[0], v1[0], v2[0])
		tyMin := minf3(v0[1], v1[1], v2[1])
		tyMax := maxf3(v0[1], v1[1], v2[1])
		idx.TriZMin[ti] = minf3(v0[2], v1[2], v2[2])
		idx.TriZMax[ti] = maxf3(v0[2], v1[2], v2[2])
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
		for r := r0; r <= r1; r++ {
			for c := c0; c <= c1; c++ {
				idx.Cells[r*cols+c] = append(idx.Cells[r*cols+c], int32(ti))
			}
		}
	}
	return idx
}

// Candidates returns triangle indices whose XY bbox overlaps the
// query rectangle [xMin, xMax] × [yMin, yMax] AND whose Z bbox
// overlaps [zMin, zMax]. The returned slice is freshly allocated;
// callers may sort/dedupe as needed.
func (idx *TriXYZIndex) Candidates(xMin, yMin, xMax, yMax, zMin, zMax float32) []int32 {
	if idx == nil || len(idx.Cells) == 0 {
		return nil
	}
	c0 := int((xMin - idx.MinX) / idx.CellSize)
	c1 := int((xMax - idx.MinX) / idx.CellSize)
	r0 := int((yMin - idx.MinY) / idx.CellSize)
	r1 := int((yMax - idx.MinY) / idx.CellSize)
	if c0 < 0 {
		c0 = 0
	}
	if r0 < 0 {
		r0 = 0
	}
	if c1 >= idx.Cols {
		c1 = idx.Cols - 1
	}
	if r1 >= idx.Rows {
		r1 = idx.Rows - 1
	}
	if c1 < 0 || r1 < 0 || c0 > c1 || r0 > r1 {
		return nil
	}
	// Dedupe via a marker slice; large tris can overlap multiple
	// grid cells.
	seen := make(map[int32]struct{})
	out := []int32{}
	for r := r0; r <= r1; r++ {
		row := r * idx.Cols
		for c := c0; c <= c1; c++ {
			for _, ti := range idx.Cells[row+c] {
				if _, dup := seen[ti]; dup {
					continue
				}
				seen[ti] = struct{}{}
				if idx.TriZMax[ti] < zMin || idx.TriZMin[ti] > zMax {
					continue
				}
				out = append(out, ti)
			}
		}
	}
	return out
}

func minf3(a, b, c float32) float32 {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

func maxf3(a, b, c float32) float32 {
	if a > b {
		if a > c {
			return a
		}
		return c
	}
	if b > c {
		return b
	}
	return c
}
