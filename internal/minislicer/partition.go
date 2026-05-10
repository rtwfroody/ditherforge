package minislicer

import "math"

// PartitionLoops divides each layer's loops into Sections of arc
// length >= cellSize. The success criterion ("each colored patch is
// at least cellSize long") is realized at this stage: a section IS a
// colored patch (worst case, since each is at least cellSize long
// and dither cannot make a sub-section patch).
//
// For a loop with perimeter L:
//   - N = floor(L / cellSize) is the section count, but at least 1.
//   - Each section has length L/N >= cellSize when N >= 1 and L >= cellSize.
//   - When L < cellSize the loop emits a single sub-cellSize section;
//     this is unavoidable for tiny features and the slicer would drop
//     them anyway, but we still emit them so they appear in the
//     graph and visualization.
//
// Returns a flat slice of all sections across all layers, indexed
// per-layer/per-loop so the partitioner is the source of truth for
// section identity (LayerIdx, LoopIdx, Index).
func PartitionLoops(layers []Layer, cellSize float32) []Section {
	if cellSize <= 0 {
		return nil
	}
	var sections []Section
	for li := range layers {
		layer := &layers[li]
		for lp := range layer.Loops {
			loop := &layer.Loops[lp]
			loopSecs := partitionLoop(loop, layer.LayerIdx, lp, cellSize)
			sections = append(sections, loopSecs...)
		}
	}
	return sections
}

// partitionLoop splits a single loop's perimeter into sections.
func partitionLoop(loop *Loop, layerIdx, loopIdx int, cellSize float32) []Section {
	n := len(loop.Points)
	if n < 3 {
		return nil
	}
	// Edge lengths around the loop (edge i connects points[i] →
	// points[i+1]); cumLen[i] is the arc-length at points[i] from
	// the start (cumLen[0] == 0). cumLen[n] == perimeter.
	cumLen := make([]float32, n+1)
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		dx := float64(loop.Points[j][0] - loop.Points[i][0])
		dy := float64(loop.Points[j][1] - loop.Points[i][1])
		cumLen[i+1] = cumLen[i] + float32(math.Sqrt(dx*dx+dy*dy))
	}
	perim := cumLen[n]
	if perim <= 0 {
		return nil
	}

	// Section count: as many cellSize-or-longer pieces as fit, with
	// a floor of 1.
	nSec := int(math.Floor(float64(perim / cellSize)))
	if nSec < 1 {
		nSec = 1
	}
	step := perim / float32(nSec)

	out := make([]Section, nSec)
	for s := 0; s < nSec; s++ {
		startArc := float32(s) * step
		endArc := startArc + step
		midArc := startArc + 0.5*step
		out[s] = Section{
			LayerIdx: layerIdx,
			LoopIdx:  loopIdx,
			Index:    s,
			StartArc: startArc,
			EndArc:   endArc,
			Length:   step,
			Mid:      pointAtArc(loop.Points, cumLen, midArc),
			Z:        loop.Z,
		}
	}
	return out
}

// pointAtArc returns the XY point on a loop at the given arc-length
// position. cumLen has length len(points)+1 with cumLen[0]=0 and
// cumLen[n]=perimeter. arc is wrapped into [0, perimeter).
func pointAtArc(points []Point2, cumLen []float32, arc float32) Point2 {
	n := len(points)
	perim := cumLen[n]
	if perim <= 0 {
		return points[0]
	}
	// Wrap into [0, perim).
	for arc < 0 {
		arc += perim
	}
	for arc >= perim {
		arc -= perim
	}
	// Find segment with cumLen[i] <= arc < cumLen[i+1].
	// Linear scan is fine; loops are small (typically tens of points).
	for i := 0; i < n; i++ {
		if cumLen[i+1] >= arc {
			seg := cumLen[i+1] - cumLen[i]
			if seg <= 0 {
				return points[i]
			}
			t := (arc - cumLen[i]) / seg
			j := (i + 1) % n
			return Point2{
				points[i][0] + t*(points[j][0]-points[i][0]),
				points[i][1] + t*(points[j][1]-points[i][1]),
			}
		}
	}
	// Should be unreachable; return last vertex as fallback.
	return points[n-1]
}
