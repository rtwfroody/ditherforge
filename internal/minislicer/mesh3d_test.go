package minislicer

import "testing"

func TestBuildPrintableMeshCube(t *testing.T) {
	// One layer at z=0, unit-square loop, 4 sections.
	loop := Loop{Points: []Point2{{0, 0}, {1, 0}, {1, 1}, {0, 1}}, Z: 0}
	loop.SignedArea = signedArea(loop.Points)
	layers := []Layer{{Z: 0, LayerIdx: 0, Loops: []Loop{loop}}}
	secs := PartitionLoops(layers, 1.0)
	if len(secs) != 4 {
		t.Fatalf("got %d sections, want 4", len(secs))
	}
	assigns := []int32{0, 1, 2, 3}

	mesh, faceAssign := BuildPrintableMesh(layers, secs, assigns, 0.5)
	// Wall verts: 4 wall pts × 2 (lo+hi) = 8.
	// Cap verts: 2 (top + bot centroids).
	wantVerts := 10
	if len(mesh.Vertices) != wantVerts {
		t.Errorf("got %d verts, want %d", len(mesh.Vertices), wantVerts)
	}
	// Wall faces: 4 quads × 2 tris = 8.
	// Cap faces: 4 (bot fan) + 4 (top fan) = 8.
	wantFaces := 16
	if len(mesh.Faces) != wantFaces {
		t.Errorf("got %d faces, want %d", len(mesh.Faces), wantFaces)
	}
	if len(faceAssign) != wantFaces {
		t.Fatalf("got %d face assignments, want %d", len(faceAssign), wantFaces)
	}
	// First 8 should be wall (≥0); last 8 should be caps (=-1).
	for i := 0; i < 8; i++ {
		if faceAssign[i] < 0 {
			t.Errorf("wall face %d: got -1, want palette index", i)
		}
	}
	for i := 8; i < 16; i++ {
		if faceAssign[i] != -1 {
			t.Errorf("cap face %d: got %d, want -1", i, faceAssign[i])
		}
	}
}

func TestBuildPrintableMeshReversesCWLoops(t *testing.T) {
	// Same square but in CW order; emit should still produce
	// valid geometry (and the same vertex/face counts).
	loop := Loop{Points: []Point2{{0, 0}, {0, 1}, {1, 1}, {1, 0}}, Z: 0}
	loop.SignedArea = signedArea(loop.Points)
	if loop.SignedArea > 0 {
		t.Fatalf("expected CW loop with negative signed area, got %g", loop.SignedArea)
	}
	layers := []Layer{{Z: 0, LayerIdx: 0, Loops: []Loop{loop}}}
	secs := PartitionLoops(layers, 1.0)
	assigns := []int32{0, 1, 2, 3}
	mesh, _ := BuildPrintableMesh(layers, secs, assigns, 0.5)
	if len(mesh.Vertices) != 10 || len(mesh.Faces) != 16 {
		t.Errorf("CW loop: got verts=%d faces=%d, want 10/16", len(mesh.Vertices), len(mesh.Faces))
	}
}
