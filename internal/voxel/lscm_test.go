package voxel

import (
	"math"
	"testing"
)

// TestLSCMFlatPlaneIsExact: on a flat triangulated plane in the XY plane,
// LSCM with pins at two vertices' true (x,y) coordinates must reproduce the
// (x,y) coords for every other vertex (within numerical tolerance), since
// the identity map is the unique conformal layout.
func TestLSCMFlatPlaneIsExact(t *testing.T) {
	model := makeFlatGrid(10, 5) // 6×6 verts, cell=2
	tris := make([]int32, len(model.Faces))
	for i := range tris {
		tris[i] = int32(i)
	}

	// Pin two corner vertices to their true (x,y) coords.
	v0 := model.Vertices[0]                              // (0,0,0)
	v1 := model.Vertices[5]                              // (10,0,0)
	uv0 := [2]float32{v0[0], v0[1]}
	uv1 := [2]float32{v1[0], v1[1]}

	out, _, err := SolveLSCM(model, tris, v0, v1, uv0, uv1)
	if err != nil {
		t.Fatalf("LSCM: %v", err)
	}

	for _, p := range model.Vertices {
		got, ok := out[SnapPos(p)]
		if !ok {
			t.Errorf("vertex %v missing", p)
			continue
		}
		if math.Abs(float64(got[0]-p[0])) > 1e-3 || math.Abs(float64(got[1]-p[1])) > 1e-3 {
			t.Errorf("vertex %v: got (%.3f,%.3f), want (%.3f,%.3f)", p, got[0], got[1], p[0], p[1])
		}
	}
}

// TestLSCMCylinderArcIsometric: on a developable cylinder ARC patch (open,
// disk topology), LSCM must preserve all edge lengths within a small
// tolerance — a cylinder unrolls isometrically, so the conformal map
// equals the isometric map. This is the regression test for top.json's
// stretched-unfold symptom.
func TestLSCMCylinderArcIsometric(t *testing.T) {
	const r = 5.0
	const h = 4.0
	const arcDeg = 90 // 90° of cylinder, well over the ~45° a real sticker covers
	const segs = 24
	model := makeCylinderArc(r, h, arcDeg, segs)
	tris := make([]int32, len(model.Faces))
	for i := range tris {
		tris[i] = int32(i)
	}

	// Pin two adjacent bottom-row vertices to the arc length they should
	// have when unrolled. The arc step is r*Δθ; v0 sits at the leftmost
	// arc position (-arc/2 in tangent terms), v1 one step to the right.
	v0 := model.Vertices[0]
	v1 := model.Vertices[1]
	step := float64(r) * (float64(arcDeg) * math.Pi / 180) / float64(segs)
	uv0 := [2]float32{0, 0}
	uv1 := [2]float32{float32(step), 0}

	out, _, err := SolveLSCM(model, tris, v0, v1, uv0, uv1)
	if err != nil {
		t.Fatalf("LSCM: %v", err)
	}

	maxRel := 0.0
	for _, f := range model.Faces {
		for k := 0; k < 3; k++ {
			a := f[k]
			b := f[(k+1)%3]
			pa := model.Vertices[a]
			pb := model.Vertices[b]
			d3 := math.Sqrt(
				float64((pa[0]-pb[0])*(pa[0]-pb[0]) +
					(pa[1]-pb[1])*(pa[1]-pb[1]) +
					(pa[2]-pb[2])*(pa[2]-pb[2])))
			ua := out[SnapPos(pa)]
			ub := out[SnapPos(pb)]
			d2 := math.Sqrt(
				float64((ua[0]-ub[0])*(ua[0]-ub[0]) +
					(ua[1]-ub[1])*(ua[1]-ub[1])))
			if d3 < 1e-6 {
				continue
			}
			rel := math.Abs(d2-d3) / d3
			if rel > maxRel {
				maxRel = rel
			}
		}
	}

	if maxRel > 0.05 {
		t.Errorf("cylinder arc: max edge stretch ratio = %.4f, want ≤ 0.05", maxRel)
	}
	t.Logf("cylinder arc: max edge stretch ratio = %.4f", maxRel)
}
