package fwnrepair

import (
	"context"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/manifoldbool"
)

// scaleToMaxExtent uniformly scales a model so its largest bbox extent is
// maxMM, matching what the pipeline does before mesh repair.
func scaleToMaxExtent(t *testing.T, m *loader.LoadedModel, maxMM float32) {
	t.Helper()
	if len(m.Vertices) == 0 {
		t.Fatal("model has no vertices")
	}
	lo, hi := m.Vertices[0], m.Vertices[0]
	for _, v := range m.Vertices {
		for d := 0; d < 3; d++ {
			if v[d] < lo[d] {
				lo[d] = v[d]
			}
			if v[d] > hi[d] {
				hi[d] = v[d]
			}
		}
	}
	ext := hi[0] - lo[0]
	for d := 1; d < 3; d++ {
		if e := hi[d] - lo[d]; e > ext {
			ext = e
		}
	}
	if ext > 0 {
		loader.ScaleModel(m, maxMM/ext)
	}
}

// TestRepairOpenBottomBuildingIsManifold is the regression for the
// grid-boundary clipping bug: the low-poly building is open at its base,
// so its generalized winding number is still ≈1 at the padded grid's
// z-min sample plane. Before the outer-shell clamp in evalSlice the 0.5
// isosurface was cut open where it met that boundary, producing 144
// single-use (crack) edges that made the mesh non-2-manifold and got it
// rejected by manifoldbool. The repair must now be watertight and
// boolean-ready. Pitch 1.0mm reproduces the original failure while
// keeping the test fast.
func TestRepairOpenBottomBuildingIsManifold(t *testing.T) {
	m, err := loader.LoadGLB("../../tests/objects/low_poly_building.glb", -1)
	if err != nil {
		t.Fatalf("load building fixture: %v", err)
	}
	scaleToMaxExtent(t, m, 50)

	out, _, _, err := Repair(context.Background(), m, 1.0, 1.0)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}

	boundary, nonManifold, vol := meshStats(t, out.Vertices, out.Faces)
	if boundary != 0 {
		t.Errorf("building output has %d boundary edges, want 0 (watertight)", boundary)
	}
	if nonManifold != 0 {
		t.Errorf("building output has %d non-manifold edges, want 0 (2-manifold)", nonManifold)
	}
	if vol <= 0 {
		t.Errorf("building signed volume = %g, want positive", vol)
	}

	mm, err := manifoldbool.FromMesh(out.Vertices, out.Faces)
	if err != nil {
		t.Fatalf("manifoldbool.FromMesh rejected repaired building: %v", err)
	}
	defer mm.Close()
	if mm.IsEmpty() {
		t.Fatal("repaired building produced an empty Manifold")
	}
}

// TestRepairedCubeIsValidManifold pushes a repaired cube through the
// real downstream consumer: manifoldbool.FromMesh accepts only ε-valid
// 2-manifold input, so a successful, non-empty, positive-volume
// Manifold confirms the repair output is boolean-ready.
func TestRepairedCubeIsValidManifold(t *testing.T) {
	v, f := cubeMesh(10)
	out, _, _, err := Repair(context.Background(), &loader.LoadedModel{Vertices: v, Faces: f}, 0.5, 0.5)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}

	m, err := manifoldbool.FromMesh(out.Vertices, out.Faces)
	if err != nil {
		t.Fatalf("manifoldbool.FromMesh rejected repaired cube: %v", err)
	}
	defer m.Close()

	if m.IsEmpty() {
		t.Fatal("repaired cube produced an empty Manifold")
	}
	if vol := m.Volume(); vol <= 0 {
		t.Errorf("Manifold volume = %g, want positive", vol)
	}
}
