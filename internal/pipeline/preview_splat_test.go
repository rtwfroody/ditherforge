package pipeline

import (
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/cellslicer"
)

// TestBuildCellSplatPreview checks the instant colored-preview builder: one
// colored quad (two triangles) per visible cell, each quad centered on its
// cell centroid, lying in the plane perpendicular to the cell normal, and
// carrying the cell's dithered palette color.
func TestBuildCellSplatPreview(t *testing.T) {
	// Three cells with distinct centroids, normals, and areas.
	samples := []cellslicer.CellSample{
		{Centroid: [3]float32{0, 0, 0}, Normal: [3]float32{0, 0, 1}, Area: 4},   // 2mm side
		{Centroid: [3]float32{10, 0, 0}, Normal: [3]float32{1, 0, 0}, Area: 1},  // 1mm side
		{Centroid: [3]float32{0, 10, 5}, Normal: [3]float32{0, 1, 0}, Area: 16}, // 4mm side
	}
	vo := &voxelizeOutput{
		CellSamples:   samples,
		VisibleToCell: []int{0, 1, 2},
	}
	palette := [][3]uint8{
		{255, 0, 0},
		{0, 255, 0},
		{0, 0, 255},
	}
	assignments := []int32{0, 1, 2}

	md := buildCellSplatPreview(vo, assignments, palette, nil)

	// Two triangles per visible cell.
	nVis := len(vo.VisibleToCell)
	if got, want := len(md.Faces)/3, nVis*2; got != want {
		t.Fatalf("face count = %d, want %d", got, want)
	}
	if got, want := len(md.Vertices)/3, nVis*4; got != want {
		t.Fatalf("vertex count = %d, want %d", got, want)
	}
	if len(md.FaceColors) != len(md.Faces) {
		t.Fatalf("FaceColors length %d != Faces length %d", len(md.FaceColors), len(md.Faces))
	}

	// All vertices finite.
	for i, v := range md.Vertices {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			t.Fatalf("vertex[%d] is not finite: %v", i, v)
		}
	}

	// Per-cell checks: color matches palette, quad centroid ≈ cell centroid
	// (within the along-normal jitter, |j| ≤ 0.02mm), and the quad plane is
	// perpendicular to the cell normal.
	for vi := 0; vi < nVis; vi++ {
		cs := samples[vo.VisibleToCell[vi]]
		want := palette[assignments[vi]]

		// Both triangles of this quad carry the palette color.
		for tri := 0; tri < 2; tri++ {
			fi := vi*2 + tri
			r := md.FaceColors[fi*3]
			g := md.FaceColors[fi*3+1]
			b := md.FaceColors[fi*3+2]
			if r != uint16(want[0]) || g != uint16(want[1]) || b != uint16(want[2]) {
				t.Errorf("cell %d tri %d color = (%d,%d,%d), want (%d,%d,%d)",
					vi, tri, r, g, b, want[0], want[1], want[2])
			}
		}

		// The four quad corners are verts [vi*4 .. vi*4+3].
		base := vi * 4
		var centroid [3]float32
		corners := [4][3]float32{}
		for k := 0; k < 4; k++ {
			corners[k] = [3]float32{
				md.Vertices[(base+k)*3],
				md.Vertices[(base+k)*3+1],
				md.Vertices[(base+k)*3+2],
			}
			for a := 0; a < 3; a++ {
				centroid[a] += corners[k][a] / 4
			}
		}
		for a := 0; a < 3; a++ {
			if d := math.Abs(float64(centroid[a] - cs.Centroid[a])); d > 0.021 {
				t.Errorf("cell %d centroid[%d] off by %.4f (> jitter tolerance)", vi, a, d)
			}
		}

		// Quad lies in the plane ⊥ normal: two in-plane edges cross to a
		// vector parallel to the (unit) cell normal.
		e1 := sub3(corners[1], corners[0])
		e2 := sub3(corners[3], corners[0])
		nrm := splatNormalize(splatCross(e1, e2))
		want3 := splatNormalize(cs.Normal)
		dot := math.Abs(float64(nrm[0]*want3[0] + nrm[1]*want3[1] + nrm[2]*want3[2]))
		if dot < 0.999 {
			t.Errorf("cell %d quad plane not ⊥ normal: |dot| = %.5f", vi, dot)
		}
	}
}

// TestBuildCellSplatPreview_SplitNormalXform checks that a split half's
// forward rigid transform rotates the cell normal into bed coords. A 90°
// rotation about +Z should turn a +X color-model normal into a +Y bed normal.
func TestBuildCellSplatPreview_SplitNormalXform(t *testing.T) {
	vo := &voxelizeOutput{
		CellSamples:   []cellslicer.CellSample{{Centroid: [3]float32{0, 0, 0}, Normal: [3]float32{1, 0, 0}, Area: 4, HalfIdx: 0}},
		VisibleToCell: []int{0},
	}
	palette := [][3]uint8{{200, 100, 50}}
	assignments := []int32{0}

	// Rotate +90° about Z (x->y, y->-x) plus a translation (must not affect
	// the direction rotation).
	xform := func(halfIdx byte, n [3]float32) [3]float32 {
		return [3]float32{-n[1], n[0], n[2]}
	}
	md := buildCellSplatPreview(vo, assignments, palette, xform)

	corners := [4][3]float32{}
	for k := 0; k < 4; k++ {
		corners[k] = [3]float32{md.Vertices[k*3], md.Vertices[k*3+1], md.Vertices[k*3+2]}
	}
	e1 := sub3(corners[1], corners[0])
	e2 := sub3(corners[3], corners[0])
	nrm := splatNormalize(splatCross(e1, e2))
	// Expected bed normal is +Y.
	if math.Abs(float64(nrm[1])) < 0.999 {
		t.Errorf("transformed quad normal = %v, want parallel to +Y", nrm)
	}
}

func sub3(a, b [3]float32) [3]float32 {
	return [3]float32{a[0] - b[0], a[1] - b[1], a[2] - b[2]}
}
