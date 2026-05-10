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
