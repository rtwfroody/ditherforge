package split

import (
	"math"
	"testing"
)

// TestLayout_UnitCubeAtMidplane — cube cut at z=0.5, no connectors.
// Both halves should sit on z=0 with their cap faces flat on the bed,
// disjoint along X with the requested gap, and centred on y=0.
func TestLayout_UnitCubeAtMidplane(t *testing.T) {
	cube := makeUnitCube()
	res, err := Cut(cube, AxisPlane(2, 0.5), ConnectorSettings{})
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	const gap = 0.2
	xforms := Layout(res, AxisPlane(2, 0.5), gap)

	// 1. Both halves rest on z=0.
	for h := 0; h < 2; h++ {
		minZ := math.Inf(1)
		for _, v := range res.Halves[h].Vertices {
			if float64(v[2]) < minZ {
				minZ = float64(v[2])
			}
		}
		if math.Abs(minZ) > 1e-5 {
			t.Errorf("half %d: bbox min.z = %g, want 0", h, minZ)
		}
	}

	// 2. Cap faces lie flat on the bed.
	for h := 0; h < 2; h++ {
		for _, fi := range res.CapFaces[h] {
			f := res.Halves[h].Faces[fi]
			for _, vi := range f {
				z := res.Halves[h].Vertices[vi][2]
				if math.Abs(float64(z)) > 1e-5 {
					t.Errorf("half %d cap face %d: vertex %d at z=%g, want 0", h, fi, vi, z)
				}
			}
		}
	}

	// 3. Halves are disjoint along X with the requested gap.
	bbox := func(h int) (minX, maxX float64) {
		minX = math.Inf(1)
		maxX = math.Inf(-1)
		for _, v := range res.Halves[h].Vertices {
			if float64(v[0]) < minX {
				minX = float64(v[0])
			}
			if float64(v[0]) > maxX {
				maxX = float64(v[0])
			}
		}
		return
	}
	min0, max0 := bbox(0)
	min1, max1 := bbox(1)
	if math.Abs(min0) > 1e-5 {
		t.Errorf("half 0 min.x = %g, want 0", min0)
	}
	if max0+gap > min1+1e-5 {
		t.Errorf("halves overlap in x: half0.max=%g + gap=%g >= half1.min=%g", max0, gap, min1)
	}
	if math.Abs(min1-(max0+gap)) > 1e-5 {
		t.Errorf("gap between halves: half1.min=%g, want %g (= half0.max %g + gap %g)", min1, max0+gap, max0, gap)
	}
	_ = max1

	// 4. Both halves centred on y=0.
	for h := 0; h < 2; h++ {
		minY := math.Inf(1)
		maxY := math.Inf(-1)
		for _, v := range res.Halves[h].Vertices {
			if float64(v[1]) < minY {
				minY = float64(v[1])
			}
			if float64(v[1]) > maxY {
				maxY = float64(v[1])
			}
		}
		if math.Abs(minY+maxY) > 1e-5 {
			t.Errorf("half %d not centred on y=0: minY=%g maxY=%g", h, minY, maxY)
		}
	}

	// 5. Both halves remain watertight after layout.
	for h := 0; h < 2; h++ {
		assertWatertight(t, res.Halves[h], "laid-out half "+string(rune('0'+h)))
	}

	// 6. Inverse round-trip: starting from an arbitrary point in the
	//    half's vertex space, ApplyInverse(Apply(p)) should recover p
	//    (within float precision). We use p = a vertex of the
	//    original cube.
	for h := 0; h < 2; h++ {
		// Use the original cube vertex (0.5, 0.5, 0.0 + h*0.5) as the
		// test point in original coords (it lies inside half h).
		var p [3]float32
		if h == 0 {
			p = [3]float32{0.5, 0.5, 0}
		} else {
			p = [3]float32{0.5, 0.5, 1}
		}
		pBed := xforms[h].Apply(p)
		pBack := xforms[h].ApplyInverse(pBed)
		dx := math.Abs(float64(pBack[0] - p[0]))
		dy := math.Abs(float64(pBack[1] - p[1]))
		dz := math.Abs(float64(pBack[2] - p[2]))
		if dx > 1e-5 || dy > 1e-5 || dz > 1e-5 {
			t.Errorf("half %d inverse round-trip: %v → %v → %v (Δ %g,%g,%g)", h, p, pBed, pBack, dx, dy, dz)
		}
	}
}

// TestLayout_PreservesVolume — total volume after layout = total
// volume before layout. Rotations and translations are isometries, so
// the per-half volume should be invariant.
func TestLayout_PreservesVolume(t *testing.T) {
	cube := makeUnitCube()
	res, err := Cut(cube, AxisPlane(2, 0.5), ConnectorSettings{})
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	beforeV0 := math.Abs(closedMeshVolume(res.Halves[0]))
	beforeV1 := math.Abs(closedMeshVolume(res.Halves[1]))
	Layout(res, AxisPlane(2, 0.5), 0.2)
	afterV0 := math.Abs(closedMeshVolume(res.Halves[0]))
	afterV1 := math.Abs(closedMeshVolume(res.Halves[1]))
	if math.Abs(beforeV0-afterV0) > 1e-5 || math.Abs(beforeV1-afterV1) > 1e-5 {
		t.Errorf("volumes changed across layout: half 0 %g→%g, half 1 %g→%g",
			beforeV0, afterV0, beforeV1, afterV1)
	}
}

// TestLayout_TransformOnPlanePoints — the cap face vertices, before
// layout, are at z=0.5 in original coords. After layout, the
// transform should map them to z=0 (on the bed). Verifies that
// Transform.Apply matches the in-place mutation Layout performs.
func TestLayout_TransformOnPlanePoints(t *testing.T) {
	cube := makeUnitCube()
	res, err := Cut(cube, AxisPlane(2, 0.5), ConnectorSettings{})
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	// Snapshot a few original-coord points on the cut plane (z=0.5).
	origPoints := []struct {
		half  int
		point [3]float32
	}{
		{0, [3]float32{0, 0, 0.5}},
		{0, [3]float32{1, 1, 0.5}},
		{1, [3]float32{0.5, 0.5, 0.5}},
	}
	xforms := Layout(res, AxisPlane(2, 0.5), 0.2)
	for _, op := range origPoints {
		pBed := xforms[op.half].Apply(op.point)
		if math.Abs(float64(pBed[2])) > 1e-5 {
			t.Errorf("plane point %v in half %d → bed %v: z != 0", op.point, op.half, pBed)
		}
	}
}

// TestRotationToNegZ_AlignsCorrectly — sanity check the rotation
// utility: applying the rotation to the input cap normal should
// produce (0, 0, −1) within float precision, for several axis
// choices.
func TestRotationToNegZ_AlignsCorrectly(t *testing.T) {
	cases := []struct {
		name string
		a    [3]float64
	}{
		{"+Z", [3]float64{0, 0, 1}},
		{"-Z", [3]float64{0, 0, -1}},
		{"+X", [3]float64{1, 0, 0}},
		{"-X", [3]float64{-1, 0, 0}},
		{"+Y", [3]float64{0, 1, 0}},
		{"-Y", [3]float64{0, -1, 0}},
	}
	for _, c := range cases {
		R := rotationToNegZ(c.a)
		got := applyRotation(R, [3]float32{float32(c.a[0]), float32(c.a[1]), float32(c.a[2])})
		want := [3]float32{0, 0, -1}
		dx := math.Abs(float64(got[0] - want[0]))
		dy := math.Abs(float64(got[1] - want[1]))
		dz := math.Abs(float64(got[2] - want[2]))
		if dx > 1e-5 || dy > 1e-5 || dz > 1e-5 {
			t.Errorf("%s: rotation maps to %v, want %v", c.name, got, want)
		}
	}
}
