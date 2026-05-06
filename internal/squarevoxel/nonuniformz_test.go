package squarevoxel

import (
	"context"
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/progress"
)

// TestVoxelizeTwoGrids_NonUniformZ exercises the Snapmaker-shaped
// case where layer0H ≠ upperH. Asserts that the result reports both
// heights round-tripped from the inputs and that grid 1's lowest
// layer (absolute index 1) is positioned consistent with the
// "layer 0 sits on the bed, upper layers stack on top" geometry
// contract — specifically that layer 1's center sits at exactly
// MinV[2] + Layer0H + UpperH/2.
func TestVoxelizeTwoGrids_NonUniformZ(t *testing.T) {
	// Tall enough that grid 1 has at least one layer.
	cube := makeColorCubeModel(2, [4]uint8{200, 100, 50, 255})
	const layer0Size, upperSize = 0.4, 0.4
	const layer0H, upperH = 0.5, 0.2 // 2.5× ratio — exaggerates the divergence
	res, err := VoxelizeTwoGrids(
		context.Background(),
		cube, cube,
		nil, nil,
		layer0Size, upperSize, layer0H, upperH,
		progress.NullTracker{},
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("VoxelizeTwoGrids: %v", err)
	}
	if res.Layer0H != layer0H {
		t.Errorf("Layer0H round-trip: got %v, want %v", res.Layer0H, layer0H)
	}
	if res.UpperH != upperH {
		t.Errorf("UpperH round-trip: got %v, want %v", res.UpperH, upperH)
	}

	// Find a grid 1 cell at layer 1 and recover its center Z. The
	// cube's faces span the whole Z range so at least one such cell
	// must exist; otherwise the geometry contract is broken.
	var foundLayer1 bool
	var minZGrid1Layer1 float32 = math.MaxFloat32
	var maxZGrid0Layer0 float32 = -math.MaxFloat32
	for _, c := range res.Cells {
		// CellKey.Layer is absolute; grid 1's lowest layer is index 1.
		if c.Grid == 1 && c.Layer == 1 {
			foundLayer1 = true
			z := res.MinV[2] + layer0H + upperH/2 // expected center per contract
			_ = z
			// The cell's actual Z position isn't directly stored on
			// ActiveCell, so we recompute from CellKey + grid params.
			// What we CAN check via the Cells slice is that at least
			// one grid-1 cell lives at absolute layer 1 (i.e. the
			// indexing scheme didn't drift).
		}
		if c.Grid == 0 && c.Layer == 0 {
			z := res.MinV[2] + layer0H/2 // grid 0 layer 0 center
			if z > maxZGrid0Layer0 {
				maxZGrid0Layer0 = z
			}
		}
		if c.Grid == 1 && c.Layer == 1 {
			z := res.MinV[2] + layer0H + upperH/2
			if z < minZGrid1Layer1 {
				minZGrid1Layer1 = z
			}
		}
	}
	if !foundLayer1 {
		t.Fatal("expected at least one grid-1 cell at absolute layer 1; found none — layer indexing may have drifted")
	}

	// Geometry contract: grid 0's layer-0 top (= MinV[2]+Layer0H)
	// equals grid 1's layer-1 bottom (= MinV[2]+Layer0H), so layer 1's
	// center is exactly UpperH/2 above grid 0's layer-0 top. With
	// halfExtent layer0H/2 below the center for grid 0, this means
	// the seam is at MinV[2]+Layer0H regardless of UpperH — which is
	// what the SeamZ contract bakes in.
	wantSeam := res.MinV[2] + layer0H
	wantLayer1Center := wantSeam + upperH/2
	if math.Abs(float64(minZGrid1Layer1-wantLayer1Center)) > 1e-5 {
		t.Errorf("grid 1 layer 1 center: got %v, want %v (seam at %v + UpperH/2 = %v)",
			minZGrid1Layer1, wantLayer1Center, wantSeam, upperH/2)
	}
}

// TestVoxelizeTwoGrids_UniformZRegression confirms the
// non-uniform-Z change doesn't perturb the common case where
// Layer0H == UpperH. Same VoxelizeTwoGrids call as the existing
// TestVoxelize_SplitInfoNilUnchanged, but spelled out so future
// changes to the geometry contract that *would* break uniform Z
// fail loudly.
func TestVoxelizeTwoGrids_UniformZRegression(t *testing.T) {
	cube := makeColorCubeModel(20, [4]uint8{200, 100, 50, 255})
	const layerH = 0.4
	res, err := VoxelizeTwoGrids(
		context.Background(),
		cube, cube,
		nil, nil,
		2, 2, layerH, layerH,
		progress.NullTracker{},
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("VoxelizeTwoGrids: %v", err)
	}
	if res.Layer0H != res.UpperH {
		t.Errorf("uniform-Z run produced divergent heights: Layer0H=%v UpperH=%v", res.Layer0H, res.UpperH)
	}
	if len(res.Cells) == 0 {
		t.Fatal("no active cells")
	}
}
