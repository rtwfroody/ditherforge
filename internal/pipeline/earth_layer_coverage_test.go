package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/cellslicer"
	"github.com/rtwfroody/ditherforge/internal/progress"
)

// pointInPoly is an even-odd ray cast (+X), matching the cellslicer's
// own pointInPolygon. Used to test cell-Outer membership.
func pointInPoly(pts []cellslicer.Point2, x, y float32) bool {
	inside := false
	n := len(pts)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		if (pts[i][1] > y) != (pts[j][1] > y) {
			xi := (pts[j][0]-pts[i][0])*(y-pts[i][1])/(pts[j][1]-pts[i][1]) + pts[i][0]
			if x < xi {
				inside = !inside
			}
		}
	}
	return inside
}

// enclosedHoleFraction rasterizes a slab's footprint and returns the
// fraction of in-footprint sample points that fall in an *enclosed*
// uncovered region — uncovered points walled off from the footprint
// boundary by covered cells. This is the "a cell that should be
// populated but isn't" signal: a gap in the cell tiling surrounded by
// cells, as opposed to the legitimate hollow interior of an annular
// (surface-only) slab, which connects to the outside and is excluded.
//
// Classification per grid point: wall = inside some cell; empty =
// inside the footprint but no cell, OR outside the footprint. A flood
// fill of empty points from the grid border reaches every uncovered
// region that opens to the outside; any in-footprint uncovered point
// the fill never reaches is enclosed.
func enclosedHoleFraction(s *cellslicer.Slab, step float32) (frac float64, inFP int) {
	minX, minY, maxX, maxY, ok := s.Footprint.Bounds()
	if !ok || step <= 0 {
		return 0, 0
	}
	// One-step margin so the border row/col is guaranteed outside the
	// footprint, giving the flood fill an exterior seed all around.
	minX -= step
	minY -= step
	maxX += step
	maxY += step
	nx := int((maxX-minX)/step) + 1
	ny := int((maxY-minY)/step) + 1
	if nx < 3 || ny < 3 {
		return 0, 0
	}

	const (
		wall    = 0 // inside a cell
		empty   = 1 // in footprint, no cell — or outside footprint
		outside = 2 // outside footprint specifically (a subset of empty)
	)
	grid := make([]uint8, nx*ny)
	for j := 0; j < ny; j++ {
		y := minY + float32(j)*step
		for i := 0; i < nx; i++ {
			x := minX + float32(i)*step
			if !s.Footprint.Contains(x, y) {
				grid[j*nx+i] = outside
				continue
			}
			inFP++
			covered := false
			for ci := range s.Cells {
				if pointInPoly(s.Cells[ci].Outer, x, y) {
					covered = true
					break
				}
			}
			if covered {
				grid[j*nx+i] = wall
			} else {
				grid[j*nx+i] = empty
			}
		}
	}
	if inFP == 0 {
		return 0, 0
	}

	// Flood fill non-wall cells from the border (all outside).
	reached := make([]bool, nx*ny)
	stack := make([]int, 0, nx*ny/4)
	push := func(idx int) {
		if grid[idx] != wall && !reached[idx] {
			reached[idx] = true
			stack = append(stack, idx)
		}
	}
	for i := 0; i < nx; i++ {
		push(i)
		push((ny-1)*nx + i)
	}
	for j := 0; j < ny; j++ {
		push(j * nx)
		push(j*nx + nx - 1)
	}
	for len(stack) > 0 {
		idx := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		x, y := idx%nx, idx/nx
		if x > 0 {
			push(idx - 1)
		}
		if x < nx-1 {
			push(idx + 1)
		}
		if y > 0 {
			push(idx - nx)
		}
		if y < ny-1 {
			push(idx + nx)
		}
	}

	enclosed := 0
	for idx, g := range grid {
		if g == empty && !reached[idx] {
			enclosed++
		}
	}
	return float64(enclosed) / float64(inFP), inFP
}

// TestEarthTopLayerFullyCovered guards the top cap of earth.glb against
// partition coverage gaps. The topmost slab of a sphere is a solid disc
// of surface (no hollow interior), so its footprint should be tiled
// edge-to-edge by cells. An enclosed uncovered region there means the
// slab partition dropped a cell — a gap visible as a transparent patch
// on the model's top and as a red cell in the per-slab debug view.
//
// Regression guard for the hex-lattice edge gap: generateHexCellsRaw
// once emitted only hexes whose centres fell inside inner.Bounds(),
// leaving a thin uncovered sliver at the +X/+Y extreme of the cap where
// the footprint reached past the last in-bounds hex. Padding the
// lattice by one cellSize closed it; this test fails if it reopens.
func TestEarthTopLayerFullyCovered(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (-short)")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	size := float32(50)
	opts := Options{
		Input:                 filepath.Join(repoRoot, "tests", "objects", "earth.glb"),
		ObjectIndex:           -1,
		NumColors:             4,
		NozzleDiameter:        0.4,
		LayerHeight:           0.2,
		Scale:                 1,
		Size:                  &size,
		Force:                 true,
		Dither:                "riemersma",
		ColorSnap:             5,
		ShowSampledColors:     true,
		Layer0AdhesionXYScale: 2,
		UpperLayerXYScale:     1.25,
	}

	cache := NewStageCache()
	r := &pipelineRun{
		ctx:     context.Background(),
		cache:   cache,
		opts:    opts,
		tracker: progress.NullTracker{},
	}
	vo, err := r.Voxelize()
	if err != nil {
		t.Fatalf("voxelize: %v", err)
	}
	if len(vo.CellSlabs) == 0 {
		t.Fatal("no slabs produced")
	}

	// The top cap is the topmost slab with footprint geometry.
	top := -1
	for i := len(vo.CellSlabs) - 1; i >= 0; i-- {
		if s := &vo.CellSlabs[i]; s.Footprint != nil && len(s.Footprint.Loops) > 0 && len(s.Cells) > 0 {
			top = i
			break
		}
	}
	if top < 0 {
		t.Fatal("no slab with footprint geometry")
	}
	s := &vo.CellSlabs[top]

	// Sanity: the top slab must be a solid cap (cells nearly fill the
	// footprint), otherwise the enclosed-hole signal isn't meaningful
	// — an annular slab's hollow centre is uncovered by design.
	var fpA, cellA float64
	for _, lp := range s.Footprint.Loops {
		a := shoelaceArea(lp.Points)
		if lp.IsHole {
			fpA -= a
		} else {
			fpA += a
		}
	}
	for ci := range s.Cells {
		cellA += shoelaceArea(s.Cells[ci].Outer)
	}
	coverRatio := 0.0
	if fpA > 0 {
		coverRatio = cellA / fpA
	}
	t.Logf("top slab %d: z=[%.2f,%.2f] cells=%d fpArea=%.3f cellArea=%.3f cover=%.1f%%",
		top, s.ZBot, s.ZTop, len(s.Cells), fpA, cellA, coverRatio*100)
	if coverRatio < 0.90 {
		t.Fatalf("top slab %d is not a solid cap (cover=%.1f%%); test premise broken — earth's top slab should be a filled disc", top, coverRatio*100)
	}

	holeFrac, inFP := enclosedHoleFraction(s, vo.CellSize/8)
	t.Logf("top slab %d: enclosed-hole fraction = %.3f%% (footprint sample points=%d)", top, holeFrac*100, inFP)

	const limit = 0.0005 // 0.05% — a solid cap should tile edge-to-edge
	if holeFrac > limit {
		dumpSlabSVGOnFailure(t, vo, top)
		t.Errorf("top slab %d has an enclosed coverage gap: %.3f%% of the footprint is walled-off uncovered area (limit %.3f%%) — the slab partition dropped a cell that should be filled",
			top, holeFrac*100, limit*100)
	}
}

// shoelaceArea returns the unsigned area of a simple polygon ring
// given without a closing duplicate vertex.
func shoelaceArea(pts []cellslicer.Point2) float64 {
	if len(pts) < 3 {
		return 0
	}
	var a float64
	for i := range pts {
		j := (i + 1) % len(pts)
		a += float64(pts[i][0])*float64(pts[j][1]) - float64(pts[j][0])*float64(pts[i][1])
	}
	if a < 0 {
		a = -a
	}
	return a / 2
}

// dumpSlabSVGOnFailure writes the failing slab's debug SVG (with the
// uncovered region highlighted in red) to $DF_TEST_DUMP_DIR or a temp
// dir, so a developer can eyeball the gap that tripped the test.
func dumpSlabSVGOnFailure(t *testing.T, vo *voxelizeOutput, slabIdx int) {
	t.Helper()
	dir := os.Getenv("DF_TEST_DUMP_DIR")
	if dir == "" {
		dir = t.TempDir()
	} else {
		_ = os.MkdirAll(dir, 0o755)
	}
	svg := cellslicer.RenderSlabDebugSVG(vo.CellSlabs, vo.CellSamples, slabIdx, cellslicer.DebugSVGOptions{
		CellSizeMM:          vo.CellSize,
		FillBackgroundWhite: true,
		DrawEdges:           true,
		DrawFootprint:       true,
		DrawContours:        true,
		HighlightUncovered:  true,
	})
	if svg == "" {
		return
	}
	p := filepath.Join(dir, fmt.Sprintf("earth_top_slab_%03d.svg", slabIdx))
	if err := os.WriteFile(p, []byte(svg), 0o644); err != nil {
		t.Logf("could not write debug SVG: %v", err)
		return
	}
	t.Logf("debug SVG (red = uncovered gap) written to %s", p)
}
