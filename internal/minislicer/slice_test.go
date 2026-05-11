package minislicer

import (
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// makeCube returns a unit-axis-aligned box from (x0,y0,z0) to
// (x1,y1,z1) with outward-facing CCW triangle winding.
func makeCube(x0, y0, z0, x1, y1, z1 float32) *loader.LoadedModel {
	v := [][3]float32{
		{x0, y0, z0}, // 0
		{x1, y0, z0}, // 1
		{x1, y1, z0}, // 2
		{x0, y1, z0}, // 3
		{x0, y0, z1}, // 4
		{x1, y0, z1}, // 5
		{x1, y1, z1}, // 6
		{x0, y1, z1}, // 7
	}
	f := [][3]uint32{
		// bottom (z=z0), normal = -Z
		{0, 2, 1}, {0, 3, 2},
		// top (z=z1), normal = +Z
		{4, 5, 6}, {4, 6, 7},
		// -Y
		{0, 1, 5}, {0, 5, 4},
		// +X
		{1, 2, 6}, {1, 6, 5},
		// +Y
		{2, 3, 7}, {2, 7, 6},
		// -X
		{3, 0, 4}, {3, 4, 7},
	}
	return &loader.LoadedModel{Vertices: v, Faces: f}
}

func TestSliceCubeMidplane(t *testing.T) {
	m := makeCube(0, 0, 0, 1, 1, 1)
	layers := SliceMesh(m, []float32{0.5})
	if len(layers) != 1 {
		t.Fatalf("got %d layers, want 1", len(layers))
	}
	loops := layers[0].Loops
	if len(loops) != 1 {
		t.Fatalf("got %d loops at z=0.5, want 1", len(loops))
	}
	loop := loops[0]
	if len(loop.Points) != 4 {
		t.Fatalf("got %d points, want 4: %+v", len(loop.Points), loop.Points)
	}
	// Loop should be a unit square.
	xMin, xMax := float32(math.Inf(1)), float32(math.Inf(-1))
	yMin, yMax := float32(math.Inf(1)), float32(math.Inf(-1))
	for _, p := range loop.Points {
		if p[0] < xMin {
			xMin = p[0]
		}
		if p[0] > xMax {
			xMax = p[0]
		}
		if p[1] < yMin {
			yMin = p[1]
		}
		if p[1] > yMax {
			yMax = p[1]
		}
	}
	if xMin != 0 || yMin != 0 || xMax != 1 || yMax != 1 {
		t.Errorf("bbox = (%g..%g)x(%g..%g), want (0..1)x(0..1)", xMin, xMax, yMin, yMax)
	}
	// Signed area magnitude == 1.0.
	if math.Abs(float64(loop.SignedArea))-1.0 > 1e-5 {
		t.Errorf("|signed area| = %g, want 1.0", loop.SignedArea)
	}
}

func TestSliceTwoIslands(t *testing.T) {
	m1 := makeCube(0, 0, 0, 1, 1, 1)
	m2 := makeCube(3, 0, 0, 4, 1, 1)
	// Concatenate face indices, offsetting m2's by len(m1.Vertices).
	merged := &loader.LoadedModel{
		Vertices: append([][3]float32{}, m1.Vertices...),
		Faces:    append([][3]uint32{}, m1.Faces...),
	}
	off := uint32(len(m1.Vertices))
	merged.Vertices = append(merged.Vertices, m2.Vertices...)
	for _, f := range m2.Faces {
		merged.Faces = append(merged.Faces, [3]uint32{f[0] + off, f[1] + off, f[2] + off})
	}
	layers := SliceMesh(merged, []float32{0.5})
	if len(layers[0].Loops) != 2 {
		t.Fatalf("got %d loops, want 2", len(layers[0].Loops))
	}
}

func TestSliceMisses(t *testing.T) {
	m := makeCube(0, 0, 0, 1, 1, 1)
	layers := SliceMesh(m, []float32{-0.1, 1.1})
	for i, l := range layers {
		if len(l.Loops) != 0 {
			t.Errorf("layer %d at z=%g: got %d loops, want 0", i, l.Z, len(l.Loops))
		}
	}
}

func TestPlanesForRange(t *testing.T) {
	planes := PlanesForRange(0, 1, 0.25)
	want := []float32{0.125, 0.375, 0.625, 0.875}
	if len(planes) != len(want) {
		t.Fatalf("got %v, want %v", planes, want)
	}
	// Per-plane offset (planeJitter, ~1e-4 mm) avoids landing
	// exactly on a model vertex; tolerate it but verify each
	// plane stays within a small neighborhood of the nominal Z.
	for i := range planes {
		if math.Abs(float64(planes[i]-want[i])) > 1e-3 {
			t.Errorf("plane %d: got %g, want ≈ %g", i, planes[i], want[i])
		}
	}
}
