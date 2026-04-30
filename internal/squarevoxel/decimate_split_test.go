package squarevoxel

import (
	"context"
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/cgalclip"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/split"
)

// skipIfNoCGAL skips a test that needs split.Cut when the binary
// wasn't built with the cgal tag. split delegates the geometry to
// CGAL via internal/cgalclip; without CGAL there's no usable cut.
func skipIfNoCGAL(t *testing.T) {
	t.Helper()
	if !cgalclip.HasCGAL {
		t.Skip("split.Cut requires the cgal build tag")
	}
}

// makeIcosphere returns a unit-radius icosphere centred at the
// origin with `subdiv` subdivision passes. subdiv=2 → 320 triangles,
// enough for QEM to have meaningful work to do during decimation.
// Always closed and watertight, with shared vertices between adjacent
// triangles (so split.Cut can walk the cut polygon without dead ends).
func makeIcosphere(subdiv int) *loader.LoadedModel {
	t := float32((1 + math.Sqrt(5)) / 2)
	verts := [][3]float32{
		{-1, t, 0}, {1, t, 0}, {-1, -t, 0}, {1, -t, 0},
		{0, -1, t}, {0, 1, t}, {0, -1, -t}, {0, 1, -t},
		{t, 0, -1}, {t, 0, 1}, {-t, 0, -1}, {-t, 0, 1},
	}
	for i := range verts {
		x, y, z := float64(verts[i][0]), float64(verts[i][1]), float64(verts[i][2])
		l := math.Sqrt(x*x + y*y + z*z)
		verts[i] = [3]float32{float32(x / l), float32(y / l), float32(z / l)}
	}
	faces := [][3]uint32{
		{0, 11, 5}, {0, 5, 1}, {0, 1, 7}, {0, 7, 10}, {0, 10, 11},
		{1, 5, 9}, {5, 11, 4}, {11, 10, 2}, {10, 7, 6}, {7, 1, 8},
		{3, 9, 4}, {3, 4, 2}, {3, 2, 6}, {3, 6, 8}, {3, 8, 9},
		{4, 9, 5}, {2, 4, 11}, {6, 2, 10}, {8, 6, 7}, {9, 8, 1},
	}
	for s := 0; s < subdiv; s++ {
		mid := make(map[uint64]uint32)
		midpoint := func(a, b uint32) uint32 {
			lo, hi := a, b
			if lo > hi {
				lo, hi = hi, lo
			}
			key := uint64(lo)<<32 | uint64(hi)
			if idx, ok := mid[key]; ok {
				return idx
			}
			va, vb := verts[a], verts[b]
			m := [3]float32{(va[0] + vb[0]) / 2, (va[1] + vb[1]) / 2, (va[2] + vb[2]) / 2}
			x, y, z := float64(m[0]), float64(m[1]), float64(m[2])
			l := math.Sqrt(x*x + y*y + z*z)
			m = [3]float32{float32(x / l), float32(y / l), float32(z / l)}
			idx := uint32(len(verts))
			verts = append(verts, m)
			mid[key] = idx
			return idx
		}
		var newFaces [][3]uint32
		for _, f := range faces {
			a := midpoint(f[0], f[1])
			b := midpoint(f[1], f[2])
			c := midpoint(f[2], f[0])
			newFaces = append(newFaces,
				[3]uint32{f[0], a, c},
				[3]uint32{f[1], b, a},
				[3]uint32{f[2], c, b},
				[3]uint32{a, b, c},
			)
		}
		faces = newFaces
	}
	return &loader.LoadedModel{Vertices: verts, Faces: faces}
}

// TestDecimate_HalfPreservesCapPlanarity is the load-bearing
// validation for phase 5: when a Split-produced half is decimated,
// cap-perimeter vertices stay near the cap plane within a tolerance
// scaled by cellSize. This validates the design's no-extension
// assumption — that QEM's planar-affinity bias keeps cap-region
// vertices on (or very near) the cut plane without needing an
// explicit pinned-vertex extension to voxel.Decimate.
//
// Uses a subdivision-2 icosphere (~320 tris) so the simplifier has
// meaningful work: decimating to 50% means ~80 collapses per half,
// enough for cap-perimeter edges to genuinely compete in the heap
// against body edges.
//
// The threshold is `0.1 × cellSize` — a real fixture run shows
// observed drift up to ~3% of cellSize (1.5 μm at cellSize=50 μm),
// well below printer resolution but non-zero. A regression that
// disabled QEM's planar bias would produce drift on the order of
// cellSize itself (10x more), so this threshold catches that.
func TestDecimate_HalfPreservesCapPlanarity(t *testing.T) {
	skipIfNoCGAL(t)
	const cutZ = 0.1
	const cellSize = 0.05
	sphere := makeIcosphere(2)
	res, err := split.Cut(sphere, split.AxisPlane(2, cutZ), split.ConnectorSettings{})
	if err != nil {
		t.Fatalf("split.Cut: %v", err)
	}

	for h := 0; h < 2; h++ {
		half := res.Halves[h]
		origFaces := len(half.Faces)
		target := origFaces * 50 / 100
		dec, err := DecimateMesh(context.Background(), half, target, cellSize, false, progress.NullTracker{})
		if err != nil {
			t.Fatalf("half %d: DecimateMesh: %v", h, err)
		}
		if len(dec.Faces) >= origFaces {
			t.Errorf("half %d: decimation didn't reduce face count: %d → %d (target %d)", h, origFaces, len(dec.Faces), target)
		}

		// Any vertex that ended up within 1.0 × cellSize of the cap
		// plane is in the cap region (vs. the far surface of the
		// half). Within that region, no vertex should be more than
		// 0.1 × cellSize off the plane. A real regression in the
		// planar-affinity bias would drag cap-region vertices by
		// roughly cellSize, well outside this band.
		nearRegion := float64(cellSize)
		maxDrift := 0.1 * float64(cellSize)
		capRegionVerts := 0
		for _, v := range dec.Vertices {
			off := math.Abs(float64(v[2]) - cutZ)
			if off < nearRegion {
				capRegionVerts++
				if off > maxDrift {
					t.Errorf("half %d: cap-region vertex z=%g drift %g > maxDrift %g (cellSize=%g)", h, v[2], off, maxDrift, cellSize)
				}
			}
		}
		if capRegionVerts < 4 {
			t.Errorf("half %d: only %d cap-region vertices survived; cap may have collapsed entirely", h, capRegionVerts)
		}
	}
}

// TestDecimateHalves_ProportionalTargets — the wrapper splits the
// total target between halves proportionally to face count and
// returns a decimated mesh per half.
func TestDecimateHalves_ProportionalTargets(t *testing.T) {
	skipIfNoCGAL(t)
	sphere := makeIcosphere(2)
	res, err := split.Cut(sphere, split.AxisPlane(2, 0.1), split.ConnectorSettings{})
	if err != nil {
		t.Fatalf("split.Cut: %v", err)
	}
	totalFaces := len(res.Halves[0].Faces) + len(res.Halves[1].Faces)
	target := totalFaces * 50 / 100
	out, err := DecimateHalves(context.Background(), res.Halves, target, 0.05, false, progress.NullTracker{})
	if err != nil {
		t.Fatalf("DecimateHalves: %v", err)
	}
	for i := 0; i < 2; i++ {
		if out[i] == nil {
			t.Errorf("half %d: nil output", i)
			continue
		}
		if len(out[i].Faces) >= len(res.Halves[i].Faces) {
			t.Errorf("half %d: decimation didn't reduce face count: %d → %d", i, len(res.Halves[i].Faces), len(out[i].Faces))
		}
	}
}

// TestDecimateHalves_NoSimplifyPassthrough — when noSimplify=true the
// helper returns each half unmodified (identity equality).
func TestDecimateHalves_NoSimplifyPassthrough(t *testing.T) {
	skipIfNoCGAL(t)
	sphere := makeIcosphere(1)
	res, err := split.Cut(sphere, split.AxisPlane(2, 0.1), split.ConnectorSettings{})
	if err != nil {
		t.Fatalf("split.Cut: %v", err)
	}
	out, err := DecimateHalves(context.Background(), res.Halves, 1, 0.1, true, progress.NullTracker{})
	if err != nil {
		t.Fatalf("DecimateHalves: %v", err)
	}
	for i := 0; i < 2; i++ {
		if out[i] != res.Halves[i] {
			t.Errorf("half %d: noSimplify didn't return the input unchanged", i)
		}
	}
}
