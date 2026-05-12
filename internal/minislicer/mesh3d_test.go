package minislicer

import "testing"

// TestBuildPrintableMeshCube exercises the earcut-cap layout on a
// single-layer unit square cube. With 4 wall sections we get:
//   wall: 4 wall pts × 2 (lo/hi) = 8 verts, 4 quads × 2 tris = 8 tris
//   top cap (earcut): 4 outer verts, 2 tris
//   bottom cap (earcut): 4 outer verts, 2 tris
// total: 16 verts, 12 tris.
func TestBuildPrintableMeshCube(t *testing.T) {
	loop := Loop{Points: []Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}}, Z: 0}
	loop.SignedArea = signedArea(loop.Points)
	layers := []Layer{{Z: 0, LayerIdx: 0, Loops: []Loop{loop}}}
	secs := PartitionLoops(layers, 1.0)
	if len(secs) != 4 {
		t.Fatalf("got %d sections, want 4", len(secs))
	}
	assigns := []int32{0, 1, 2, 3}

	mesh, faceAssign := BuildPrintableMesh(layers, secs, assigns, 0.5)
	wantVerts := 16
	wantFaces := 12
	if len(mesh.Vertices) != wantVerts {
		t.Errorf("got %d verts, want %d", len(mesh.Vertices), wantVerts)
	}
	if len(mesh.Faces) != wantFaces {
		t.Errorf("got %d faces, want %d", len(mesh.Faces), wantFaces)
	}
	if len(faceAssign) != wantFaces {
		t.Fatalf("got %d face assignments, want %d", len(faceAssign), wantFaces)
	}
	// First 8 faces are walls (non-negative); next 4 are caps
	// (fallback = most common assignment = any of 0..3 here since
	// all are unique with count 1; impl returns whichever it counts
	// first — accept any non-negative).
	for i := 0; i < 8; i++ {
		if faceAssign[i] < 0 {
			t.Errorf("wall face %d: got -1, want palette index", i)
		}
	}
	for i := 8; i < 12; i++ {
		if faceAssign[i] < 0 {
			t.Errorf("cap face %d: got -1, want non-negative fallback", i)
		}
	}
}

// TestBuildPrintableMeshReversesCWLoops mirrors the cube test but
// with a CW input; should produce the same geometry counts.
func TestBuildPrintableMeshReversesCWLoops(t *testing.T) {
	loop := Loop{Points: []Point2{{0, 0}, {0, 1}, {1, 1}, {1, 0}}, Z: 0}
	loop.SignedArea = signedArea(loop.Points)
	if loop.SignedArea > 0 {
		t.Fatalf("expected CW loop with negative signed area, got %g", loop.SignedArea)
	}
	layers := []Layer{{Z: 0, LayerIdx: 0, Loops: []Loop{loop}}}
	secs := PartitionLoops(layers, 1.0)
	assigns := []int32{0, 1, 2, 3}
	mesh, _ := BuildPrintableMesh(layers, secs, assigns, 0.5)
	if len(mesh.Vertices) != 16 || len(mesh.Faces) != 12 {
		t.Errorf("CW loop: got verts=%d faces=%d, want 16/12", len(mesh.Vertices), len(mesh.Faces))
	}
}

// TestBuildPrintableMeshWithHole verifies a single-layer outer +
// hole renders walls for both and an earcut cap that excludes the
// hole. The cap area should be (outer area - hole area).
func TestBuildPrintableMeshWithHole(t *testing.T) {
	outer := Loop{Points: []Point2{{0, 0}, {10, 0}, {10, 10}, {0, 10}}, Z: 0}
	outer.SignedArea = signedArea(outer.Points)
	hole := Loop{Points: []Point2{{3, 3}, {3, 7}, {7, 7}, {7, 3}}, Z: 0}
	hole.SignedArea = signedArea(hole.Points)
	layers := []Layer{{Z: 0, LayerIdx: 0, Loops: []Loop{outer, hole}}}
	// Run classify to populate IsHole / HasHoleChild fields.
	classifyHoles(layers[0].Loops)
	if layers[0].Loops[0].IsHole || !layers[0].Loops[0].HasHoleChild {
		t.Fatalf("outer classification: IsHole=%v HasHoleChild=%v",
			layers[0].Loops[0].IsHole, layers[0].Loops[0].HasHoleChild)
	}
	if !layers[0].Loops[1].IsHole {
		t.Fatalf("hole IsHole=false")
	}
	secs := PartitionLoops(layers, 2.5)
	if len(secs) == 0 {
		t.Fatal("no sections")
	}
	assigns := make([]int32, len(secs))
	mesh, faceAssign := BuildPrintableMesh(layers, secs, assigns, 0.5)
	if len(mesh.Faces) == 0 {
		t.Fatal("no faces")
	}
	// Sum the area of cap faces (those tagged with fallback non-neg
	// for the interior color; here all assignments are 0 so easy).
	// Find cap faces: those at z = layerH/2 or -layerH/2.
	var topArea, botArea float64
	for i, tr := range mesh.Faces {
		a := mesh.Vertices[tr[0]]
		b := mesh.Vertices[tr[1]]
		c := mesh.Vertices[tr[2]]
		// Walls span [zBot, zTop]; caps lie on one Z plane.
		if a[2] == b[2] && b[2] == c[2] {
			area := float64((b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0]))
			if a[2] > 0 {
				topArea += area / 2
			} else {
				botArea += area / 2
			}
			_ = i
			_ = faceAssign
		}
	}
	wantArea := 100.0 - 16.0
	if topArea < wantArea-0.5 || topArea > wantArea+0.5 {
		t.Errorf("top cap area = %g, want ≈ %g", topArea, wantArea)
	}
	// Bottom cap winding is reversed so its summed signed area is
	// negative; compare magnitude.
	if -botArea < wantArea-0.5 || -botArea > wantArea+0.5 {
		t.Errorf("bottom cap area magnitude = %g, want ≈ %g", -botArea, wantArea)
	}
}

// TestStackedCubeNoInternalCaps verifies that a multi-layer column
// of identical-footprint loops produces exactly one top cap and one
// bottom cap (no internal coplanar cap surfaces between adjacent
// layers). This is the watertight-cap invariant the Clipper-based
// rewrite was meant to deliver: surfaces only on the air-facing
// boundary of the print, never buried inside the solid.
func TestStackedCubeNoInternalCaps(t *testing.T) {
	const nLayers = 5
	const layerH = float32(0.2)
	pts := []Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	layers := make([]Layer, nLayers)
	for i := 0; i < nLayers; i++ {
		loop := Loop{Points: pts, Z: float32(i) * layerH}
		loop.SignedArea = signedArea(loop.Points)
		layers[i] = Layer{Z: loop.Z, LayerIdx: i, Loops: []Loop{loop}}
	}
	secs := PartitionLoops(layers, 1.0)
	// No top/bottom cap tiles for this minimal test — the only
	// caps come from BuildPrintableMesh's per-slab-boundary
	// exposed-region triangulation.
	assigns := make([]int32, len(secs))
	mesh, _ := BuildPrintableMesh(layers, secs, assigns, layerH)

	// Count faces by Z (caps are coplanar at a single Z; walls
	// span two Z values). Expected: caps at the topmost top face
	// (z = (nLayers-0.5)*layerH) and bottommost bottom face
	// (z = -0.5*layerH), nothing in between.
	capsByZ := map[float32]int{}
	for _, tr := range mesh.Faces {
		a := mesh.Vertices[tr[0]]
		b := mesh.Vertices[tr[1]]
		c := mesh.Vertices[tr[2]]
		if a[2] == b[2] && b[2] == c[2] {
			capsByZ[a[2]]++
		}
	}
	if len(capsByZ) != 2 {
		t.Fatalf("expected exactly 2 cap Z-planes (top + bottom of stack), got %d: %v", len(capsByZ), capsByZ)
	}
	wantBot := -layerH / 2
	wantTop := float32(nLayers-1)*layerH + layerH/2
	if _, ok := capsByZ[wantBot]; !ok {
		t.Errorf("missing bottom cap at z=%g; have %v", wantBot, capsByZ)
	}
	if _, ok := capsByZ[wantTop]; !ok {
		t.Errorf("missing top cap at z=%g; have %v", wantTop, capsByZ)
	}
}

// TestSteppedPyramidEmitsStepCaps verifies that two layers with
// different footprints (a smaller square stacked on a larger one)
// emit a step cap at their interface — covering the annulus where
// the lower layer's top face is air-facing. The interior overlap
// (where the smaller layer covers the larger one) must NOT emit a
// cap, and the buried bottom of the upper layer must NOT emit
// either.
func TestSteppedPyramidEmitsStepCaps(t *testing.T) {
	const layerH = float32(0.2)
	bigPts := []Point2{{0, 0}, {2, 0}, {2, 2}, {0, 2}}
	smallPts := []Point2{{0.5, 0.5}, {1.5, 0.5}, {1.5, 1.5}, {0.5, 1.5}}
	big := Loop{Points: bigPts, Z: 0}
	big.SignedArea = signedArea(big.Points)
	small := Loop{Points: smallPts, Z: layerH}
	small.SignedArea = signedArea(small.Points)
	layers := []Layer{
		{Z: 0, LayerIdx: 0, Loops: []Loop{big}},
		{Z: layerH, LayerIdx: 1, Loops: []Loop{small}},
	}
	secs := PartitionLoops(layers, 1.0)
	assigns := make([]int32, len(secs))
	mesh, _ := BuildPrintableMesh(layers, secs, assigns, layerH)

	// At z = +layerH/2 (the shared slab boundary), there should
	// be exactly one cap surface facing +Z, covering the annulus
	// big - small. The buried part (overlap of big and small)
	// must contribute zero geometry — that's the watertightness
	// fix this rewrite was meant to deliver.
	shareZ := layerH / 2
	var stepArea float64
	var nStepFaces int
	for _, tr := range mesh.Faces {
		a := mesh.Vertices[tr[0]]
		b := mesh.Vertices[tr[1]]
		c := mesh.Vertices[tr[2]]
		if a[2] == shareZ && b[2] == shareZ && c[2] == shareZ {
			nStepFaces++
			signed := float64((b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0]))
			stepArea += signed / 2
		}
	}
	if nStepFaces == 0 {
		t.Fatalf("expected step-cap faces at z=%g, found none", shareZ)
	}
	// Big square area is 4, small square area is 1, annulus area
	// is 3. Top cap of layer 0 emits +Z (positive signed area);
	// any leakage from a buried internal cap would either inflate
	// this or contribute negative area. Allow Clipper int-rounding
	// slop.
	if stepArea < 2.95 || stepArea > 3.05 {
		t.Errorf("step-cap area = %g, want ≈ 3 (annulus big - small); internal cap geometry leaking?", stepArea)
	}
}
