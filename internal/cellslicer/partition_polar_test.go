package cellslicer

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

// makeCircleFootprint builds a 1-loop Footprint with `n` CCW vertices
// sampled on a circle of radius r centered at (cx, cy). Used to model
// a near-circular polar slab cap.
func makeCircleFootprint(cx, cy, r float32, n int) *Footprint {
	pts := make([]Point2, n)
	for i := 0; i < n; i++ {
		a := 2 * math.Pi * float64(i) / float64(n)
		pts[i] = Point2{cx + r*float32(math.Cos(a)), cy + r*float32(math.Sin(a))}
	}
	loop := FootprintLoop{Points: pts}
	loop.computeBbox()
	return &Footprint{Loops: []FootprintLoop{loop}}
}

// pointToSegDist returns the perpendicular distance from (x,y) to the
// segment a-b (clamped to endpoints).
func pointToSegDist(x, y, ax, ay, bx, by float32) float32 {
	dx, dy := bx-ax, by-ay
	l2 := dx*dx + dy*dy
	if l2 == 0 {
		return float32(math.Hypot(float64(x-ax), float64(y-ay)))
	}
	t := ((x-ax)*dx + (y-ay)*dy) / l2
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	px := ax + t*dx
	py := ay + t*dy
	return float32(math.Hypot(float64(x-px), float64(y-py)))
}

// makeNoisyCircleFootprint is like makeCircleFootprint but adds
// per-vertex radial jitter and occasional very-short edges, to mimic
// what an alpha-wrapped contour looks like in a near-pole slab.
func makeNoisyCircleFootprint(cx, cy, r float32, n int, jitter float32, seed int64) *Footprint {
	pts := make([]Point2, 0, n)
	rng := uint64(seed)
	// Linear-congruential PRNG so the test is reproducible.
	next := func() float32 {
		rng = rng*6364136223846793005 + 1442695040888963407
		return float32(rng>>32) / float32(1<<32)
	}
	for i := 0; i < n; i++ {
		a := 2 * math.Pi * float64(i) / float64(n)
		rr := r + (next()-0.5)*2*jitter
		pts = append(pts, Point2{cx + rr*float32(math.Cos(a)), cy + rr*float32(math.Sin(a))})
		// Occasionally insert a duplicate vertex (zero-length edge),
		// like chainSegments produces when two crossings hash to the
		// same quantized key. Picks ~1% of vertices.
		if next() < 0.01 {
			pts = append(pts, pts[len(pts)-1])
		}
	}
	loop := FootprintLoop{Points: pts}
	loop.computeBbox()
	return &Footprint{Loops: []FootprintLoop{loop}}
}

// TestPartitionPolarAnnulus stresses the failure pattern we see in
// the earth/top render at slab 245: a near-circular fp with a nested
// (smaller) fp_above. The capMask is the annulus between the two
// circles, and every in-capMask pixel should end up owned by some
// cell. We assert that no pixel in the annulus stays unassigned.
func TestPartitionPolarAnnulus(t *testing.T) {
	const (
		cellSize = 0.4
		pxSize   = 0.1
		rOuter   = 12.0
		rInner   = 10.0
		nPts     = 720
	)
	fp := makeCircleFootprint(0, 0, rOuter, nPts)
	fpAbove := makeCircleFootprint(0, 0, rInner, nPts)

	_, raster := PartitionSlabRaster(fp, nil, fpAbove, cellSize, pxSize)
	if raster == nil {
		t.Fatal("partition returned nil raster")
	}

	// Count unowned pixels inside the annulus (i.e. inside the outer
	// circle but at radial distance > rInner from center).
	annulusUnowned := 0
	annulusTotal := 0
	for py := 0; py < raster.H; py++ {
		y := raster.OriginY + (float32(py)+0.5)*raster.PxSize
		row := py * raster.W
		for px := 0; px < raster.W; px++ {
			x := raster.OriginX + (float32(px)+0.5)*raster.PxSize
			d := float32(math.Hypot(float64(x), float64(y)))
			// Annulus: between rInner and rOuter, with a half-pxSize
			// guard band on each side so we don't accuse pixels that
			// are partially outside the analytic annulus.
			if d <= rInner+pxSize*0.5 || d >= rOuter-pxSize*0.5 {
				continue
			}
			annulusTotal++
			if raster.CellID[row+px] == NoCellID {
				annulusUnowned++
			}
		}
	}
	t.Logf("annulus pixels: total=%d unowned=%d (%.1f%%)",
		annulusTotal, annulusUnowned, 100*float64(annulusUnowned)/float64(annulusTotal))
	if annulusUnowned > 0 {
		t.Fatalf("partition left %d / %d annulus pixels unowned — see ring gap artifact",
			annulusUnowned, annulusTotal)
	}
}

// slabDumpJSON mirrors the JSON layout that pipeline.dumpSlabIfRequested
// writes. We use it to replay a captured earth slab inside an isolated
// partition test.
type slabDumpJSON struct {
	SlabIndex int     `json:"slab_index"`
	ZBot      float32 `json:"z_bot"`
	ZTop      float32 `json:"z_top"`
	Footprint *struct {
		Loops []struct {
			Points [][2]float32 `json:"points"`
			IsHole bool         `json:"is_hole"`
		} `json:"loops"`
	} `json:"footprint"`
	FpBelow *struct {
		Loops []struct {
			Points [][2]float32 `json:"points"`
			IsHole bool         `json:"is_hole"`
		} `json:"loops"`
	} `json:"fp_below"`
	FpAbove *struct {
		Loops []struct {
			Points [][2]float32 `json:"points"`
			IsHole bool         `json:"is_hole"`
		} `json:"loops"`
	} `json:"fp_above"`
}

func footprintFromDump(loops []struct {
	Points [][2]float32 `json:"points"`
	IsHole bool         `json:"is_hole"`
}) *Footprint {
	out := &Footprint{}
	for _, lp := range loops {
		pts := make([]Point2, len(lp.Points))
		for i, p := range lp.Points {
			pts[i] = Point2{p[0], p[1]}
		}
		l := FootprintLoop{Points: pts, IsHole: lp.IsHole}
		l.computeBbox()
		out.Loops = append(out.Loops, l)
	}
	return out
}

// TestPartitionDumpedSlab replays slab 245 of earth.glb (captured via
// CELLSLICER_DUMP_SLAB=245 in the GUI) and counts in-fp pixels that
// stay unassigned. Skipped if /tmp/slab245.json doesn't exist.
func TestPartitionDumpedSlab(t *testing.T) {
	path := os.Getenv("CELLSLICER_DUMP_PATH")
	if path == "" {
		path = "/tmp/slab245.json"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("dump file not present: %v", err)
	}
	var d slabDumpJSON
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatalf("json: %v", err)
	}
	if d.Footprint == nil {
		t.Fatal("dump missing footprint")
	}
	fp := footprintFromDump(d.Footprint.Loops)
	var fpBelow, fpAbove *Footprint
	if d.FpBelow != nil {
		fpBelow = footprintFromDump(d.FpBelow.Loops)
	}
	if d.FpAbove != nil {
		fpAbove = footprintFromDump(d.FpAbove.Loops)
	}
	const cellSize float32 = 0.4
	const pxSize float32 = 0.1
	cells, raster := PartitionSlabRaster(fp, fpBelow, fpAbove, cellSize, pxSize)
	if raster == nil {
		t.Fatal("partition returned nil raster")
	}
	t.Logf("slab %d zBot=%g zTop=%g fp.loops=%d cells=%d raster=%dx%d",
		d.SlabIndex, d.ZBot, d.ZTop, len(fp.Loops), len(cells), raster.W, raster.H)
	t.Logf("fp pts: %d", len(fp.Loops[0].Points))
	if fpAbove != nil && len(fpAbove.Loops) > 0 {
		t.Logf("fpAbove pts: %d", len(fpAbove.Loops[0].Points))
	}

	// Pixels inside fp but outside fpAbove (the capMask polygon-set,
	// pre-rasterization): every such pixel must end up owned by some
	// cell, otherwise the surface in that pixel will be dropped at
	// clip time.
	totalCap := 0
	unowned := 0
	for py := 0; py < raster.H; py++ {
		y := raster.OriginY + (float32(py)+0.5)*raster.PxSize
		row := py * raster.W
		for px := 0; px < raster.W; px++ {
			x := raster.OriginX + (float32(px)+0.5)*raster.PxSize
			inFp := false
			for i := range fp.Loops {
				if fp.Loops[i].Contains(x, y) {
					inFp = !inFp
				}
			}
			if !inFp {
				continue
			}
			inAbove := false
			if fpAbove != nil {
				for i := range fpAbove.Loops {
					if fpAbove.Loops[i].Contains(x, y) {
						inAbove = !inAbove
					}
				}
			}
			if inAbove {
				continue
			}
			totalCap++
			if raster.CellID[row+px] == NoCellID {
				unowned++
			}
		}
	}
	t.Logf("capMask pixels: total=%d unowned=%d (%.2f%%)",
		totalCap, unowned, 100*float64(unowned)/float64(totalCap))
	if unowned > 0 {
		t.Errorf("captured slab leaves %d / %d capMask pixels unowned", unowned, totalCap)
	}

	// Cross-check: for every pixel owned by a cell, the cell.Outer
	// polygon must enclose that pixel's centre. If not, the SVG/GUI
	// renderer (which fills cell.Outer) will leave the pixel visually
	// uncovered even though the raster owns it.
	notEnclosed := 0
	notEnclosedInCap := 0
	worstCell := -1
	worstCellCount := 0
	cellMisses := make([]int, len(cells))
	for py := 0; py < raster.H; py++ {
		y := raster.OriginY + (float32(py)+0.5)*raster.PxSize
		row := py * raster.W
		for px := 0; px < raster.W; px++ {
			id := raster.CellID[row+px]
			if id < 0 || int(id) >= len(cells) {
				continue
			}
			x := raster.OriginX + (float32(px)+0.5)*raster.PxSize
			if !pointInPolygon(cells[id].Outer, x, y) {
				notEnclosed++
				cellMisses[id]++
				// Was this pixel in capMask?
				inFp := false
				for i := range fp.Loops {
					if fp.Loops[i].Contains(x, y) {
						inFp = !inFp
					}
				}
				inAbove := false
				if fpAbove != nil {
					for i := range fpAbove.Loops {
						if fpAbove.Loops[i].Contains(x, y) {
							inAbove = !inAbove
						}
					}
				}
				if inFp && !inAbove {
					notEnclosedInCap++
				}
			}
		}
	}
	for ci, n := range cellMisses {
		if n > worstCellCount {
			worstCellCount = n
			worstCell = ci
		}
	}
	worstOuterPts := 0
	if worstCell >= 0 {
		worstOuterPts = len(cells[worstCell].Outer)
	}
	t.Logf("pixels owned but not enclosed by Outer: total=%d in-capMask=%d worst-cell=%d misses=%d outer-pts=%d",
		notEnclosed, notEnclosedInCap, worstCell, worstCellCount, worstOuterPts)

	// Connected-components check for the worst cell. Flood-fill on the
	// pixels it owns and report how many disjoint groups exist.
	if worstCell >= 0 {
		owned := make([][2]int, 0)
		marked := make(map[[2]int]bool)
		for py := 0; py < raster.H; py++ {
			for px := 0; px < raster.W; px++ {
				if raster.CellID[py*raster.W+px] == int32(worstCell) {
					owned = append(owned, [2]int{px, py})
					marked[[2]int{px, py}] = false
				}
			}
		}
		components := 0
		componentSizes := []int{}
		for _, p := range owned {
			if marked[p] {
				continue
			}
			components++
			size := 0
			stack := [][2]int{p}
			for len(stack) > 0 {
				q := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if _, ok := marked[q]; !ok || marked[q] {
					continue
				}
				marked[q] = true
				size++
				for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
					n := [2]int{q[0] + d[0], q[1] + d[1]}
					if v, ok := marked[n]; ok && !v {
						stack = append(stack, n)
					}
				}
			}
			componentSizes = append(componentSizes, size)
		}
		t.Logf("worst cell %d owns %d pixels in %d disjoint components: %v",
			worstCell, len(owned), components, componentSizes)
	}

	if notEnclosedInCap > 0 {
		t.Fatalf("%d capMask pixels owned but rendered uncovered by cell.Outer", notEnclosedInCap)
	}
}

// TestPartitionPolarAnnulus_Noisy adds vertex jitter and occasional
// duplicate vertices to mimic alpha-wrapped earth contour at a polar
// slab. This is the failure mode I'm chasing.
func TestPartitionPolarAnnulus_Noisy(t *testing.T) {
	const (
		cellSize = 0.4
		pxSize   = 0.1
		rOuter   = 12.0
		rInner   = 10.0
		nPts     = 720
		jitter   = 0.05 // 50 µm radial jitter
	)
	fp := makeNoisyCircleFootprint(0, 0, rOuter, nPts, jitter, 1)
	fpAbove := makeNoisyCircleFootprint(0, 0, rInner, nPts, jitter, 2)

	_, raster := PartitionSlabRaster(fp, nil, fpAbove, cellSize, pxSize)
	if raster == nil {
		t.Fatal("partition returned nil raster")
	}

	annulusUnowned := 0
	annulusTotal := 0
	for py := 0; py < raster.H; py++ {
		y := raster.OriginY + (float32(py)+0.5)*raster.PxSize
		row := py * raster.W
		for px := 0; px < raster.W; px++ {
			x := raster.OriginX + (float32(px)+0.5)*raster.PxSize
			d := float32(math.Hypot(float64(x), float64(y)))
			if d <= rInner+pxSize*0.5+jitter || d >= rOuter-pxSize*0.5-jitter {
				continue
			}
			annulusTotal++
			if raster.CellID[row+px] == NoCellID {
				annulusUnowned++
			}
		}
	}
	t.Logf("noisy annulus pixels: total=%d unowned=%d (%.1f%%)",
		annulusTotal, annulusUnowned, 100*float64(annulusUnowned)/float64(annulusTotal))
	if annulusUnowned > 0 {
		t.Fatalf("noisy partition left %d / %d annulus pixels unowned",
			annulusUnowned, annulusTotal)
	}
}
