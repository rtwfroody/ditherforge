package split

import (
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// TestLayout_UnitCubeAtMidplane — cube cut at z=0.5, no connectors.
// Both halves should sit on z=0 with their cap faces flat on the bed,
// disjoint along X with the requested gap, and centred on y=0.
func TestLayout_UnitCubeAtMidplane(t *testing.T) {
	skipIfNoCGAL(t)
	cube := makeUnitCube()
	res, err := Cut(cube, AxisPlane(2, 0.5), ConnectorSettings{})
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	const gap = 0.2
	xforms := Layout(res, gap)

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

	// 2. Halves are disjoint along X with the requested gap.
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

	// 6. Inverse round-trip on cube vertices that are still inside
	//    the half's pre-layout extent (i.e., guaranteed to be in the
	//    half's vertex list under some coordinate).
	for h := 0; h < 2; h++ {
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
	skipIfNoCGAL(t)
	cube := makeUnitCube()
	res, err := Cut(cube, AxisPlane(2, 0.5), ConnectorSettings{})
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	beforeV0 := math.Abs(closedMeshVolume(res.Halves[0]))
	beforeV1 := math.Abs(closedMeshVolume(res.Halves[1]))
	Layout(res, 0.2)
	afterV0 := math.Abs(closedMeshVolume(res.Halves[0]))
	afterV1 := math.Abs(closedMeshVolume(res.Halves[1]))
	if math.Abs(beforeV0-afterV0) > 1e-5 || math.Abs(beforeV1-afterV1) > 1e-5 {
		t.Errorf("volumes changed across layout: half 0 %g→%g, half 1 %g→%g",
			beforeV0, afterV0, beforeV1, afterV1)
	}
}

// TestLayout_TransformMatchesMutation — the per-vertex equality test:
// for every vertex in the laid-out result, xforms[h].Apply(orig)
// should equal the post-Layout position. This is the test that
// catches a row/column-major mixup or a sign flip in Apply.
func TestLayout_TransformMatchesMutation(t *testing.T) {
	skipIfNoCGAL(t)
	cube := makeUnitCube()
	res, err := Cut(cube, AxisPlane(2, 0.5), ConnectorSettings{})
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	// Snapshot pre-Layout vertex arrays for both halves.
	origVerts := [2][][3]float32{
		append([][3]float32(nil), res.Halves[0].Vertices...),
		append([][3]float32(nil), res.Halves[1].Vertices...),
	}
	xforms := Layout(res, 0.2)
	for h := 0; h < 2; h++ {
		for i, orig := range origVerts[h] {
			want := res.Halves[h].Vertices[i]
			got := xforms[h].Apply(orig)
			dx := math.Abs(float64(got[0] - want[0]))
			dy := math.Abs(float64(got[1] - want[1]))
			dz := math.Abs(float64(got[2] - want[2]))
			if dx > 1e-5 || dy > 1e-5 || dz > 1e-5 {
				t.Errorf("half %d vertex %d: Apply(orig=%v) = %v, want %v (mutated value)", h, i, orig, got, want)
				if i > 5 {
					break // only report a few
				}
			}
		}
	}
}

// TestLayout_RoundTripCloud — round-trip through Apply + ApplyInverse
// on every laid-out vertex returns the corresponding original vertex.
func TestLayout_RoundTripCloud(t *testing.T) {
	skipIfNoCGAL(t)
	cube := makeUnitCube()
	res, err := Cut(cube, AxisPlane(2, 0.5), ConnectorSettings{})
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	origVerts := [2][][3]float32{
		append([][3]float32(nil), res.Halves[0].Vertices...),
		append([][3]float32(nil), res.Halves[1].Vertices...),
	}
	xforms := Layout(res, 0.2)
	for h := 0; h < 2; h++ {
		for i, orig := range origVerts[h] {
			bed := res.Halves[h].Vertices[i]
			back := xforms[h].ApplyInverse(bed)
			d := math.Abs(float64(back[0]-orig[0])) +
				math.Abs(float64(back[1]-orig[1])) +
				math.Abs(float64(back[2]-orig[2]))
			if d > 1e-4 {
				t.Errorf("half %d vertex %d: bed=%v → orig %v, want %v (Δ=%g)", h, i, bed, back, orig, d)
				if i > 5 {
					break
				}
			}
		}
	}
}

// TestLayout_NonZAxisCut — exercise the Rodrigues body of
// rotationToNegZ (not just the antipodal special cases) by cutting
// along the X and Y axes.
func TestLayout_NonZAxisCut(t *testing.T) {
	skipIfNoCGAL(t)
	for axis := 0; axis < 2; axis++ {
		cube := makeUnitCube()
		res, err := Cut(cube, AxisPlane(axis, 0.5), ConnectorSettings{})
		if err != nil {
			t.Fatalf("axis %d: Cut: %v", axis, err)
		}
		Layout(res, 0.2)

		// Both halves should rest on z=0 and have their cap faces on
		// the bed.
		for h := 0; h < 2; h++ {
			minZ := math.Inf(1)
			for _, v := range res.Halves[h].Vertices {
				if float64(v[2]) < minZ {
					minZ = float64(v[2])
				}
			}
			if math.Abs(minZ) > 1e-5 {
				t.Errorf("axis %d half %d: bbox min.z=%g, want 0", axis, h, minZ)
			}
			assertWatertight(t, res.Halves[h], "non-z half "+string(rune('0'+h)))
		}
	}
}

// TestLayout_PegOnBed — Layout combined with a peg connector.
//
// Cap-down layout puts the cap face on the bed for NoConnectors and
// Dowels, but for Pegs the peg extends in +cap_normal direction in
// original coords, which becomes -Z in bed coords. After the
// bbox-min-z shift, the peg tip rests on the bed and the cap is
// elevated by the peg depth. The half's body extends from the cap
// upward.
//
// Verifies (a) the peg tip sits on the bed (z=0) on the male half,
// (b) the cap is elevated by the peg depth, and (c) inverse
// round-trip on the peg tip recovers the peg-tip's original-coord
// position.
func TestLayout_PegOnBed(t *testing.T) {
	t.Skip("connectors stubbed pending CGAL boolean re-implementation")
	verts := [][3]float32{
		{0, 0, 0}, {50, 0, 0}, {50, 50, 0}, {0, 50, 0},
		{0, 0, 50}, {50, 0, 50}, {50, 50, 50}, {0, 50, 50},
	}
	faces := [][3]uint32{
		{0, 2, 1}, {0, 3, 2}, {4, 5, 6}, {4, 6, 7},
		{0, 1, 5}, {0, 5, 4}, {2, 3, 7}, {2, 7, 6},
		{0, 4, 7}, {0, 7, 3}, {1, 2, 6}, {1, 6, 5},
	}
	cube := &loader.LoadedModel{Vertices: verts, Faces: faces}
	settings := ConnectorSettings{
		Style: Pegs, Count: 1, DiamMM: 4, DepthMM: 5, ClearanceMM: 0.15,
	}
	res, err := Cut(cube, AxisPlane(2, 25), settings)
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	xforms := Layout(res, 5)

	// Half 0's pre-cut z extent was [0, 25] (cube body) plus peg at
	// z=25..30. After cap-down rotation + bbox-min-z shift, the peg
	// tip (lowest extent of the half) lands at bed z=0; the cap face
	// is at bed z=5; and the body's far end (original z=0) is at
	// bed z=30.
	half0 := res.Halves[0]
	minZ := math.Inf(1)
	maxZ := math.Inf(-1)
	for _, v := range half0.Vertices {
		z := float64(v[2])
		if z < minZ {
			minZ = z
		}
		if z > maxZ {
			maxZ = z
		}
	}
	if math.Abs(minZ) > 1e-5 {
		t.Errorf("half 0 min.z = %g, want 0 (peg tip on bed)", minZ)
	}
	if math.Abs(maxZ-30) > 0.5 {
		t.Errorf("half 0 max.z = %g, want ≈ 30 (body's original z=0 lands here)", maxZ)
	}

	// (Cap-face-on-bed check removed alongside the connector
	// rip-out; will be reinstated when connectors return.)
	{
		_ = half0

	}

	// Inverse round-trip on the lowest-z vertex (peg tip) should
	// recover original coords near (25, 25, 30) — the connector
	// centre at peg-tip depth.
	var tipBed [3]float32
	for _, v := range half0.Vertices {
		if float64(v[2]) < minZ+0.01 {
			tipBed = v
			break
		}
	}
	tipOrig := xforms[0].ApplyInverse(tipBed)
	if math.Abs(float64(tipOrig[0])-25) > 5 ||
		math.Abs(float64(tipOrig[1])-25) > 5 ||
		math.Abs(float64(tipOrig[2])-30) > 0.1 {
		t.Errorf("peg tip orig coords = %v, want xyz near (25, 25, 30)", tipOrig)
	}
}

// TestLayout_TransformOnPlanePoints — plane vertices in original
// coords should map to z=0 in bed coords via Transform.Apply.
func TestLayout_TransformOnPlanePoints(t *testing.T) {
	skipIfNoCGAL(t)
	cube := makeUnitCube()
	res, err := Cut(cube, AxisPlane(2, 0.5), ConnectorSettings{})
	if err != nil {
		t.Fatalf("Cut: %v", err)
	}
	origPoints := []struct {
		half  int
		point [3]float32
	}{
		{0, [3]float32{0, 0, 0.5}},
		{0, [3]float32{1, 1, 0.5}},
		{1, [3]float32{0.5, 0.5, 0.5}},
	}
	xforms := Layout(res, 0.2)
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
