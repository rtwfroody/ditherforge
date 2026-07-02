package cellslicer

import (
	"math"
	"testing"
)

// enforceMinSizeOld is a verbatim reference of the pre-optimisation
// enforceMinSize: it recomputes the deep set, rebuilds the area map, and
// rewrites the whole label array on every merge. Kept in the test binary
// only, so the optimised version can be checked for bit-identical output
// and benchmarked against it.
func (g *colorGrid) enforceMinSizeOld(cellSize float32) {
	radius := cellSize * 0.5
	rCells := int(radius/g.pitch + 0.999)
	if rCells < 1 {
		rCells = 1
	}
	r2 := radius * radius
	frozen := make(map[int32]bool)
	for {
		skip := g.deepLabels(rCells, r2)
		for lab := range frozen {
			skip[lab] = true
		}
		victim, target := g.pickMergeVictimOld(skip)
		if victim < 0 {
			break
		}
		if target < 0 {
			frozen[victim] = true
			continue
		}
		g.relabelOld(victim, target)
	}
}

func (g *colorGrid) pickMergeVictimOld(skip map[int32]bool) (victim, target int32) {
	area := make(map[int32]int)
	for idx := range g.inside {
		if g.inside[idx] && g.label[idx] >= 0 {
			area[g.label[idx]]++
		}
	}
	victim = -1
	bestArea := int(^uint(0) >> 1)
	for lab, a := range area {
		if skip[lab] {
			continue
		}
		if a < bestArea || (a == bestArea && lab < victim) {
			bestArea = a
			victim = lab
		}
	}
	if victim < 0 {
		return -1, -1
	}
	adj := make(map[int32]int)
	for idx := range g.inside {
		if !g.inside[idx] || g.label[idx] != victim {
			continue
		}
		r := idx / g.cols
		c := idx % g.cols
		for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			nc, nr := c+d[0], r+d[1]
			if nc < 0 || nc >= g.cols || nr < 0 || nr >= g.rows {
				continue
			}
			nidx := nr*g.cols + nc
			nl := g.label[nidx]
			if g.inside[nidx] && nl >= 0 && nl != victim {
				adj[nl]++
			}
		}
	}
	mean := g.labelMeanColorsOld()
	vCol, vHas := mean[victim]
	target = -1
	var bestDE float64
	var bestAdj int
	for nl, n := range adj {
		dE := math.MaxFloat64
		if nCol, ok := mean[nl]; ok && vHas {
			dE = deltaE76(vCol, nCol)
		}
		better := false
		switch {
		case target < 0:
			better = true
		case dE != bestDE:
			better = dE < bestDE
		case n != bestAdj:
			better = n > bestAdj
		case area[nl] != area[target]:
			better = area[nl] < area[target]
		default:
			better = nl < target
		}
		if better {
			bestDE, bestAdj, target = dE, n, nl
		}
	}
	return victim, target
}

func (g *colorGrid) labelMeanColorsOld() map[int32][3]uint8 {
	sum := make(map[int32][3]uint64)
	cnt := make(map[int32]int)
	for idx := range g.inside {
		if !g.inside[idx] || g.miss[idx] {
			continue
		}
		lab := g.label[idx]
		if lab < 0 {
			continue
		}
		c := g.col[idx]
		s := sum[lab]
		s[0] += uint64(c[0])
		s[1] += uint64(c[1])
		s[2] += uint64(c[2])
		sum[lab] = s
		cnt[lab]++
	}
	mean := make(map[int32][3]uint8, len(cnt))
	for lab, n := range cnt {
		s := sum[lab]
		mean[lab] = [3]uint8{uint8(s[0] / uint64(n)), uint8(s[1] / uint64(n)), uint8(s[2] / uint64(n))}
	}
	return mean
}

func (g *colorGrid) relabelOld(from, to int32) {
	for idx := range g.label {
		if g.inside[idx] && g.label[idx] == from {
			g.label[idx] = to
		}
	}
}

// noisyMinSizeGrid builds a segmented colorGrid with lots of sub-cell
// components: a deterministic per-node colour hash at ~pitch feature size
// so labelComponents produces thousands of tiny labels, exercising the
// enforceMinSize merge loop heavily. size is the coverTarget edge in mm.
func noisyMinSizeGrid(size, cellSize float32) *colorGrid {
	cover := squareFootprint(size)
	// Palette of 6 well-separated colours so neighbouring buckets exceed
	// the contrast threshold and cut into distinct components.
	pal := [][3]uint8{
		{0, 0, 0}, {255, 255, 255}, {230, 20, 20},
		{20, 200, 20}, {30, 30, 240}, {240, 220, 20},
	}
	feat := cellSize * 0.25 // one grid pitch: maximally fragmented
	sample := func(x, y float32) ([3]uint8, bool) {
		ix := int(math.Floor(float64(x / feat)))
		iy := int(math.Floor(float64(y / feat)))
		// Cheap deterministic hash of the cell coordinate.
		h := uint32(ix)*2654435761 + uint32(iy)*40503 + 17
		h ^= h >> 15
		return pal[h%uint32(len(pal))], true
	}
	g := buildColorGrid(cover, cellSize, 15.0, sample)
	if g == nil {
		panic("nil grid")
	}
	g.labelComponents()
	return g
}

func cloneLabels(g *colorGrid) []int32 {
	out := make([]int32, len(g.label))
	copy(out, g.label)
	return out
}

// TestEnforceMinSizeMatchesReference asserts the optimised enforceMinSize
// produces a bit-identical label array to the original O(labels×grid)
// reference on a heavily-fragmented grid (cache determinism requirement).
func TestEnforceMinSizeMatchesReference(t *testing.T) {
	sizes := []float32{12, 24}
	if !testing.Short() {
		// The O(labels²) reference is minutes-slow on the bigger grid.
		sizes = append(sizes, 40)
	}
	for _, size := range sizes {
		g := noisyMinSizeGrid(size, 1.0)
		start := cloneLabels(g)

		// Reference run on a clone.
		ref := &colorGrid{
			pitch: g.pitch, minX: g.minX, minY: g.minY,
			cols: g.cols, rows: g.rows,
			inside: g.inside, col: g.col, miss: g.miss,
			label:       cloneLabels(g),
			contrast:    g.contrast,
			insideCount: g.insideCount,
		}
		ref.enforceMinSizeOld(1.0)

		// Optimised run on the original grid.
		g.enforceMinSize(1.0)

		if len(g.label) != len(ref.label) {
			t.Fatalf("size %v: length mismatch", size)
		}
		diffs := 0
		for i := range g.label {
			if g.label[i] != ref.label[i] {
				diffs++
			}
		}
		if diffs != 0 {
			t.Fatalf("size %v: %d/%d node labels differ between optimised and reference (started from %d labelled nodes)",
				size, diffs, len(g.label), countLabelled(start))
		}
	}
}

func countLabelled(labels []int32) int {
	n := 0
	for _, l := range labels {
		if l >= 0 {
			n++
		}
	}
	return n
}

func benchEnforceMinSize(b *testing.B, size float32, old bool) {
	// Pre-build one template grid and clone its labels per iteration so
	// each run starts from the identical fragmented segmentation.
	tmpl := noisyMinSizeGrid(size, 1.0)
	nodes := tmpl.cols * tmpl.rows
	b.ReportMetric(float64(nodes), "gridnodes")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		g := &colorGrid{
			pitch: tmpl.pitch, minX: tmpl.minX, minY: tmpl.minY,
			cols: tmpl.cols, rows: tmpl.rows,
			inside: tmpl.inside, col: tmpl.col, miss: tmpl.miss,
			label:       cloneLabels(tmpl),
			contrast:    tmpl.contrast,
			insideCount: tmpl.insideCount,
		}
		b.StartTimer()
		if old {
			g.enforceMinSizeOld(1.0)
		} else {
			g.enforceMinSize(1.0)
		}
	}
}

func BenchmarkEnforceMinSizeOld_40(b *testing.B)  { benchEnforceMinSize(b, 40, true) }
func BenchmarkEnforceMinSizeNew_40(b *testing.B)  { benchEnforceMinSize(b, 40, false) }
func BenchmarkEnforceMinSizeOld_80(b *testing.B)  { benchEnforceMinSize(b, 80, true) }
func BenchmarkEnforceMinSizeNew_80(b *testing.B)  { benchEnforceMinSize(b, 80, false) }
func BenchmarkEnforceMinSizeOld_120(b *testing.B) { benchEnforceMinSize(b, 120, true) }
func BenchmarkEnforceMinSizeNew_120(b *testing.B) { benchEnforceMinSize(b, 120, false) }
