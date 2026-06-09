package pipeline

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/progress"
)

// TestSolidCubeSamplesUniformly guards the per-face colour-sampling bug
// where a solid-colour cube sampled distinctly different colours on the
// +X / +Y walls than on −X / −Y / caps.
//
// Root cause (fixed in voxel.SampleNearestColorWithSticker): a
// nearest-triangle search miss returned an *opaque* grey {128,128,128},
// which SampleSlab averaged into the cell colour. Cells whose nearest
// surface sat just beyond the search radius (inner-ring lateral cells ~1
// cell-width in from a wall) got darkened, and a grid-bucket boundary
// asymmetry made it bite the max-coordinate walls (+X/+Y) but not the
// min ones — turning a uniform cube into a two-tone one after dither.
//
// Why a dedicated test: TestSampledMatchesInput does NOT catch this. In
// ShowSampledColors mode the contaminated cells are interior (not on the
// rendered surface), so the render-based comparison sees a clean cube and
// passes even with the bug present. This test inspects the cell samples
// directly — interior cells included — which is where the contamination
// lives.
//
// The fixture cube (tests/objects/cube.stl) is a solid grey (200,200,200)
// STL run at native 20mm (no Size), matching the GUI repro coordinate
// frame that triggers the bucket-boundary miss. CellSample.Color is the
// raw sampled colour, before colour-snap/palette, so every visible cell
// must be exactly the cube's base grey.
func TestSolidCubeSamplesUniformly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (-short)")
	}
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	opts := Options{
		Input:                 filepath.Join(repoRoot, "tests", "objects", "cube.stl"),
		ObjectIndex:           -1,
		NumColors:             4,
		NozzleDiameter:        0.4,
		LayerHeight:           0.2,
		Scale:                 1, // native 20mm, no Size — the repro frame
		Force:                 true,
		Dither:                "riemersma",
		ColorSnap:             5,
		Layer0AdhesionXYScale: 2,
		UpperLayerXYScale:     1.25,
	}

	r := &pipelineRun{
		ctx:     context.Background(),
		cache:   NewStageCache(),
		opts:    opts,
		tracker: progress.NullTracker{},
	}
	vo, err := r.Voxelize()
	if err != nil {
		t.Fatalf("voxelize: %v", err)
	}
	if len(vo.CellSamples) == 0 {
		t.Fatal("no cell samples produced")
	}

	// The cube's every face is the loader's default solid grey
	// (200,200,200). Each visible cell samples a flat surface of that
	// colour, so the only way a cell drifts is the miss-fallback
	// contamination this test guards. tol=16 sits well above sampling
	// noise (clean cells are exactly 200) and well below the bug's
	// deviation (the +Y wall sampled ~154, Δ≈46).
	const base = 200
	const tol = 16

	// Bin the worst deviation by which bounding wall the cell hugs, so a
	// failure names the offending face(s) instead of a bare count.
	minX, minY := vo.CellSamples[0].Centroid[0], vo.CellSamples[0].Centroid[1]
	maxX, maxY := minX, minY
	for _, cs := range vo.CellSamples {
		x, y := cs.Centroid[0], cs.Centroid[1]
		minX, maxX = min(minX, x), max(maxX, x)
		minY, maxY = min(minY, y), max(maxY, y)
	}
	face := func(c [3]float32) string {
		d := map[string]float32{
			"+X": maxX - c[0], "-X": c[0] - minX,
			"+Y": maxY - c[1], "-Y": c[1] - minY,
		}
		best, bestD := "+X", d["+X"]
		for k, v := range d {
			if v < bestD {
				best, bestD = k, v
			}
		}
		return best
	}

	worstByFace := map[string]int{}
	nBad, nVisible := 0, 0
	var worstCS [3]uint8
	worst := 0
	for _, cs := range vo.CellSamples {
		if !cs.Alpha {
			continue // dropped (no surface hit) — not part of the print
		}
		nVisible++
		dev := 0
		for ch := 0; ch < 3; ch++ {
			d := int(cs.Color[ch]) - base
			if d < 0 {
				d = -d
			}
			if d > dev {
				dev = d
			}
		}
		if dev > tol {
			nBad++
			f := face(cs.Centroid)
			if dev > worstByFace[f] {
				worstByFace[f] = dev
			}
			if dev > worst {
				worst, worstCS = dev, cs.Color
			}
		}
	}

	// Guard against a vacuous pass: a regression that dropped every cell
	// to Alpha==false would leave nVisible==0 and nBad==0.
	if nVisible == 0 {
		t.Fatal("no visible (Alpha) cells sampled — test premise broken")
	}
	if nBad > 0 {
		t.Errorf("solid cube sampled non-uniformly: %d/%d visible cells deviate >%d from base grey (%d,%d,%d); worst cell=%v (dev %d); per-wall worst deviation: %v",
			nBad, nVisible, tol, base, base, base, worstCS, worst, worstByFace)
	}
}
