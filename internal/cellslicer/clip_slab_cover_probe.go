package cellslicer

// DITHERFORGE_SLAB_COVER_PROBE: per-slab diagnostic for the mechanism-(a)
// white holes (surface present in a slab's manifold but enclosed by NO
// group contour). The clip-cover-probe gates on a group's OWN contour and
// so is blind to surface that no cell covers; this probe removes that gate.
//
// For each slab it walks the up-facing srcID cap triangles the split left
// in that slab and tests each centroid against the union of that slab's
// group contours. Cap area whose centroid is inside no contour is
// "uncovered" — a hole. To test the cell/slab-assignment desync directly,
// it also reports how much of that uncovered area falls inside an ADJACENT
// slab's contours: if the surface a slab owns post-split is covered only by
// the neighbour's cells, cell-gen and the quantized split disagree on which
// slab owns it.

import (
	"fmt"
	"math"
	"os"
	"sort"
)

var slabCoverProbe = os.Getenv("DITHERFORGE_SLAB_COVER_PROBE") != ""

type slabGroupContour struct {
	loops                  [][][2]float32
	minx, miny, maxx, maxy float32
}

func slabContourBBox(loops [][][2]float32) (minx, miny, maxx, maxy float32) {
	minx, miny = float32(math.Inf(1)), float32(math.Inf(1))
	maxx, maxy = float32(math.Inf(-1)), float32(math.Inf(-1))
	for _, lp := range loops {
		for _, p := range lp {
			minx, maxx = minf32(minx, p[0]), maxf32(maxx, p[0])
			miny, maxy = minf32(miny, p[1]), maxf32(maxy, p[1])
		}
	}
	return
}

func capTriArea(t [3][3]float32) float32 {
	e1x, e1y, e1z := t[1][0]-t[0][0], t[1][1]-t[0][1], t[1][2]-t[0][2]
	e2x, e2y, e2z := t[2][0]-t[0][0], t[2][1]-t[0][1], t[2][2]-t[0][2]
	cx := e1y*e2z - e1z*e2y
	cy := e1z*e2x - e1x*e2z
	cz := e1x*e2y - e1y*e2x
	return float32(math.Sqrt(float64(cx*cx+cy*cy+cz*cz))) / 2
}

func pointInAnySlabContour(gc []slabGroupContour, x, y float32) bool {
	for i := range gc {
		c := &gc[i]
		if x < c.minx || x > c.maxx || y < c.miny || y > c.maxy {
			continue
		}
		if pointInLoops(c.loops, x, y) {
			return true
		}
	}
	return false
}

func reportSlabCoverProbe(ss *slabSrc, slabs []Slab, groups []mergeGroup) {
	if !slabCoverProbe {
		return
	}
	// Collect each slab's group contours (the prism footprints).
	bySlab := make([][]slabGroupContour, len(slabs))
	for gi := range groups {
		g := &groups[gi]
		s := &slabs[g.slabIdx]
		loops, _ := mergedGroupContours(s.Cells, g.cellIdxs)
		if len(loops) == 0 {
			continue
		}
		minx, miny, maxx, maxy := slabContourBBox(loops)
		bySlab[g.slabIdx] = append(bySlab[g.slabIdx], slabGroupContour{loops, minx, miny, maxx, maxy})
	}

	type slabStat struct {
		si                          int
		zBot                        float32
		capArea, uncovArea, neighAr float64
		uncovTris                   int
	}
	var stats []slabStat
	var totCap, totUncov, totNeigh float64
	for si := range slabs {
		src := ss.slabManifold(si)
		if src == nil {
			continue
		}
		caps := clipCoverCapTris(src, ss.srcID) // up-facing srcID tris in this slab
		if len(caps) == 0 {
			continue
		}
		gc := bySlab[si]
		var capArea, uncov, neigh float64
		var uncovTris int
		for _, t := range caps {
			cx := (t[0][0] + t[1][0] + t[2][0]) / 3
			cy := (t[0][1] + t[1][1] + t[2][1]) / 3
			a := float64(capTriArea(t))
			capArea += a
			if pointInAnySlabContour(gc, cx, cy) {
				continue
			}
			uncov += a
			uncovTris++
			if (si > 0 && pointInAnySlabContour(bySlab[si-1], cx, cy)) ||
				(si+1 < len(slabs) && pointInAnySlabContour(bySlab[si+1], cx, cy)) {
				neigh += a
			}
		}
		totCap += capArea
		if uncov > 0 {
			totUncov += uncov
			totNeigh += neigh
			stats = append(stats, slabStat{si, slabs[si].ZBot, capArea, uncov, neigh, uncovTris})
		}
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].uncovArea > stats[j].uncovArea })
	fmt.Printf("  [slab-cover-probe] total up-facing cap=%.3f mm²; uncovered (no contour in own slab)=%.4f mm² across %d slabs; of that, %.4f mm² IS covered by an adjacent slab's contour (assignment desync)\n",
		totCap, totUncov, len(stats), totNeigh)
	for i, s := range stats {
		if i >= 15 {
			break
		}
		fmt.Printf("    slab %d zBot=%.4f: cap=%.3f uncov=%.4f mm² (%d tris), neighbour-covered=%.4f mm²\n",
			s.si, s.zBot, s.capArea, s.uncovArea, s.uncovTris, s.neighAr)
	}
}
