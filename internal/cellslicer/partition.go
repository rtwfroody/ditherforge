package cellslicer

import (
	"math"

	"github.com/rtwfroody/ditherforge/internal/minislicer"
)

// boundaryMark marks an arc-length position on a FootprintLoop.
type boundaryMark struct {
	point   minislicer.Point2
	edgeIdx int
	edgeT   float32
}

// walkLoopAtCellSize emits marks at uniform arc-length spacing along
// loop. nMarks = round(perim/cellSize); actual spacing is close to
// cellSize but not exact.
func walkLoopAtCellSize(loop *FootprintLoop, cellSize float32) []boundaryMark {
	n := len(loop.Points)
	if n < 3 {
		return nil
	}
	cum := make([]float32, n+1)
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		dx := loop.Points[j][0] - loop.Points[i][0]
		dy := loop.Points[j][1] - loop.Points[i][1]
		cum[i+1] = cum[i] + hypot(dx, dy)
	}
	perim := cum[n]
	nMarks := int(math.Round(float64(perim / cellSize)))
	if nMarks < 1 {
		nMarks = 1
	}
	step := perim / float32(nMarks)
	marks := make([]boundaryMark, nMarks)
	edge := 0
	for i := 0; i < nMarks; i++ {
		target := float32(i) * step
		for edge < n && cum[edge+1] < target {
			edge++
		}
		if edge >= n {
			edge = n - 1
		}
		segLen := cum[edge+1] - cum[edge]
		var t float32
		if segLen > 0 {
			t = (target - cum[edge]) / segLen
		}
		a := loop.Points[edge]
		b := loop.Points[(edge+1)%n]
		marks[i] = boundaryMark{
			point: minislicer.Point2{
				a[0] + t*(b[0]-a[0]),
				a[1] + t*(b[1]-a[1]),
			},
			edgeIdx: edge,
			edgeT:   t,
		}
	}
	return marks
}

// extractArc returns the polyline along loop from mA to mB in the
// forward (CCW) direction, including endpoints.
func extractArc(loop *FootprintLoop, mA, mB boundaryMark) []minislicer.Point2 {
	n := len(loop.Points)
	out := []minislicer.Point2{mA.point}
	if mA.edgeIdx == mB.edgeIdx && mA.edgeT <= mB.edgeT {
		out = append(out, mB.point)
		return out
	}
	cur := (mA.edgeIdx + 1) % n
	for {
		out = append(out, loop.Points[cur])
		if cur == mB.edgeIdx {
			break
		}
		cur = (cur + 1) % n
	}
	out = append(out, mB.point)
	return out
}

// inwardNormal returns the unit normal pointing into the polygon
// interior at mark's edge (assumes CCW outer loop from Clipper non-
// zero union). 90° CCW of the tangent.
func inwardNormal(loop *FootprintLoop, m boundaryMark) [2]float32 {
	n := len(loop.Points)
	a := loop.Points[m.edgeIdx]
	b := loop.Points[(m.edgeIdx+1)%n]
	tx, ty := b[0]-a[0], b[1]-a[1]
	length := hypot(tx, ty)
	if length == 0 {
		return [2]float32{0, 0}
	}
	return [2]float32{-ty / length, tx / length}
}

// GenerateRingCells walks each outer loop of fp at cellSize spacing
// and emits one cell per consecutive pair of marks: the outer arc
// plus two perpendicular chords dropping inward by an overshoot
// depth, then Boolean-clipped to fp so the cell stays inside the
// footprint. For wide regions this yields a cellSize×cellSize ring
// cell; for narrow regions the clip absorbs the excess and gives a
// full-width trapezoid.
func GenerateRingCells(fp *Footprint, cellSize float32) []Cell {
	cells := []Cell{}
	const depthFactor = 3
	depth := depthFactor * cellSize
	for i := range fp.Loops {
		loop := &fp.Loops[i]
		if loop.IsHole {
			continue
		}
		marks := walkLoopAtCellSize(loop, cellSize)
		if len(marks) == 0 {
			continue
		}
		for k := range marks {
			mA := marks[k]
			mB := marks[(k+1)%len(marks)]
			nA := inwardNormal(loop, mA)
			nB := inwardNormal(loop, mB)
			innerB := minislicer.Point2{
				mB.point[0] + depth*nB[0],
				mB.point[1] + depth*nB[1],
			}
			innerA := minislicer.Point2{
				mA.point[0] + depth*nA[0],
				mA.point[1] + depth*nA[1],
			}
			arc := extractArc(loop, mA, mB)
			raw := make([]minislicer.Point2, 0, len(arc)+2)
			raw = append(raw, arc...)
			raw = append(raw, innerB, innerA)
			if len(raw) < 3 {
				continue
			}
			clipped := clipPolygonToFootprint(raw, fp)
			for _, c := range clipped {
				if len(c) >= 3 {
					cells = append(cells, Cell{Outer: c, Kind: KindRing})
				}
			}
		}
	}
	return cells
}

// GenerateHexCells tessellates the inward-offset footprint with
// regular hexagons of seed-to-seed spacing = cellSize. Each hex is
// the regular hexagon of radius cellSize/√3 centered on a seed,
// clipped to the inner footprint. Tiny boundary slivers are left in
// the output; downstream merge handles them.
func GenerateHexCells(inner *Footprint, cellSize float32) []Cell {
	cells := []Cell{}
	if len(inner.Loops) == 0 {
		return cells
	}
	minX, minY, maxX, maxY, _ := inner.Bounds()
	r := cellSize / float32(math.Sqrt(3))
	dx := cellSize
	dy := cellSize * float32(math.Sqrt(3)/2)
	row := 0
	for y := minY; y <= maxY; y += dy {
		offset := float32(0)
		if row%2 == 1 {
			offset = dx / 2
		}
		for x := minX + offset; x <= maxX; x += dx {
			hex := hexagonAt(x, y, r)
			clipped := clipPolygonToFootprint(hex, inner)
			for _, c := range clipped {
				if len(c) >= 3 {
					cells = append(cells, Cell{Outer: c, Kind: KindHex})
				}
			}
		}
		row++
	}
	return cells
}

func hexagonAt(cx, cy, r float32) []minislicer.Point2 {
	pts := make([]minislicer.Point2, 6)
	for k := 0; k < 6; k++ {
		angle := math.Pi/6 + float64(k)*math.Pi/3
		pts[k] = minislicer.Point2{
			cx + r*float32(math.Cos(angle)),
			cy + r*float32(math.Sin(angle)),
		}
	}
	return pts
}

// PartitionSlab partitions a single slab's footprint (derived from
// bot+top loops) into ring + hex cells. Convenience wrapper used
// when slicing is driven by the caller.
func PartitionSlab(bot, top []minislicer.Loop, cellSize float32) ([]Cell, *Footprint) {
	fp := ComputeFootprint(bot, top)
	inner := OffsetFootprint(fp, -cellSize)
	cells := GenerateRingCells(fp, cellSize)
	cells = append(cells, GenerateHexCells(inner, cellSize)...)
	return cells, fp
}
