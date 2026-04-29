package squarevoxel

import (
	"context"
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/split"
)

// makeWatertightCube returns a closed-watertight `side`-mm cube with
// 12 triangles. Vertices on shared edges are deduped (single vertex
// table) so split.Cut can walk the cut polygon without dead ends.
func makeWatertightCube(side float32) *loader.LoadedModel {
	v := [][3]float32{
		{0, 0, 0}, {side, 0, 0}, {side, side, 0}, {0, side, 0},
		{0, 0, side}, {side, 0, side}, {side, side, side}, {0, side, side},
	}
	f := [][3]uint32{
		{0, 2, 1}, {0, 3, 2},
		{4, 5, 6}, {4, 6, 7},
		{0, 1, 5}, {0, 5, 4},
		{2, 3, 7}, {2, 7, 6},
		{0, 4, 7}, {0, 7, 3},
		{1, 2, 6}, {1, 6, 5},
	}
	return &loader.LoadedModel{Vertices: v, Faces: f}
}

// TestDecimate_HalfPreservesCapPlanarity is the load-bearing
// validation for phase 5: when a Split-produced half is decimated,
// vertices that started on the cap plane (z = cut height) must stay
// on the cap plane within tight tolerance. This is the design's
// no-extension assumption — that QEM's planar-affinity bias prevents
// cap-perimeter collapses without needing an explicit pinned-vertex
// extension to voxel.Decimate.
func TestDecimate_HalfPreservesCapPlanarity(t *testing.T) {
	// Build a subdivided cube with enough faces to have something to
	// decimate, cut horizontally at z=0.5, then decimate each half.
	cube := makeWatertightCube(1) // 4×4 quad grid per face → 192 tris
	res, err := split.Cut(cube, split.AxisPlane(2, 0.51), split.ConnectorSettings{})
	if err != nil {
		t.Fatalf("split.Cut: %v", err)
	}

	// Decimate each half to ~70% of its face count: enough to remove
	// some triangles but not so aggressive that the cap collapses
	// entirely (which would be valid behavior for over-decimation,
	// not a planarity regression).
	for h := 0; h < 2; h++ {
		half := res.Halves[h]
		origFaces := len(half.Faces)
		target := origFaces * 70 / 100
		dec, err := DecimateMesh(context.Background(), half, target, 0.1, false, progress.NullTracker{})
		if err != nil {
			t.Fatalf("half %d: DecimateMesh: %v", h, err)
		}
		if len(dec.Faces) >= origFaces {
			t.Errorf("half %d: decimation didn't reduce face count: %d → %d (target %d)", h, origFaces, len(dec.Faces), target)
		}
		// Cap-perimeter vertices were on the plane z = 0.5 in the
		// pre-Layout coordinate frame. After decimation, every
		// surviving vertex that started near z=0.5 should still be
		// near z=0.5. We check by sampling: of the surviving
		// vertices that lie within 1e-4 of z=0.5, none should drift
		// further than 1e-4 (i.e., the cap plane is preserved as a
		// hard feature).
		nearCap := 0
		for _, v := range dec.Vertices {
			z := float64(v[2])
			if math.Abs(z-0.51) < 1e-4 {
				nearCap++
			} else if math.Abs(z-0.51) < 1e-2 {
				// In the [1e-4, 1e-2) band: vertex is near the cap
				// but drifted off-plane. This is the regression.
				t.Errorf("half %d: cap-region vertex drifted off plane: z=%g (want |z-0.51|<1e-4 or |z-0.51|>1e-2)", h, z)
			}
		}
		if nearCap < 4 {
			t.Errorf("half %d: only %d vertices near cap plane after decimation; cap may have collapsed entirely", h, nearCap)
		}
	}
}

// TestDecimateHalves_ProportionalTargets — the wrapper splits the
// total target between halves proportionally to face count and
// returns a decimated mesh per half.
func TestDecimateHalves_ProportionalTargets(t *testing.T) {
	cube := makeWatertightCube(1)
	res, err := split.Cut(cube, split.AxisPlane(2, 0.51), split.ConnectorSettings{})
	if err != nil {
		t.Fatalf("split.Cut: %v", err)
	}
	totalFaces := len(res.Halves[0].Faces) + len(res.Halves[1].Faces)
	target := totalFaces * 50 / 100 // decimate to ~50% total
	out, err := DecimateHalves(context.Background(), res.Halves, target, 0.1, false, progress.NullTracker{})
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
// helper returns each half unmodified, matching DecimateMesh's
// noSimplify behavior.
func TestDecimateHalves_NoSimplifyPassthrough(t *testing.T) {
	cube := makeWatertightCube(1)
	res, err := split.Cut(cube, split.AxisPlane(2, 0.51), split.ConnectorSettings{})
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
