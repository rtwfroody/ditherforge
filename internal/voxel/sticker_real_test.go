package voxel

import (
	"context"
	"image/color"
	"math"
	"testing"
)

// TestStickerUnfoldOnDirtyMesh: regression against the failure profile
// observed in real-world decimated meshes (top.stl / base.stl from the
// user's test set). Those meshes have ~2-4% near-degenerate triangles,
// a handful of huge tris from flat-region decimation, and a few
// duplicate faces. The clean LSCM-on-clean-mesh path works fine; the
// hard cases come from those artifacts. We reproduce them synthetically
// via dirtifyMesh on a filleted bowl rim — same geometric class as
// top.stl, plus the same kind of mesh dirt.
//
// The test catches three distinct regressions at once:
//   - BFS over-expansion via giant tris (tcRunaway / 3D-distance / area filters)
//   - LSCM CG non-convergence on sliver-poisoned matrices (sliver filter)
//   - Pin choice when the seed itself is a dropped sliver (pin fallback)
func TestStickerUnfoldOnDirtyMesh(t *testing.T) {
	const (
		rOuter = 50.0
		rInner = 45.0
		h      = 14.0
		fillet = 2.0
		segs   = 96
		fSegs  = 8
	)
	model := makeFilletedBowlRim(rOuter, rInner, h, fillet, segs, fSegs)
	// 2% sliver fraction, deterministic seed.
	dirtifyMesh(model, 1234, 0.02)
	t.Logf("dirty mesh: %d verts, %d faces", len(model.Vertices), len(model.Faces))

	clickCenter := [3]float64{rOuter, 0, 0}
	normal := [3]float64{1, 0, 0}
	up := [3]float64{0, 0, 1}
	scale := 27.0

	stickerImg := makeStickerTestPattern(64, 64)
	adj := BuildTriAdjacency(model)
	si := NewSpatialIndex(model, 5)
	seedTri := FindSeedTriangle(clickCenter, model, si)
	if seedTri < 0 {
		t.Fatal("no seed triangle")
	}

	unfoldDecal, err := BuildStickerDecal(context.Background(), model, adj, stickerImg,
		seedTri, clickCenter, normal, up, scale, 0, 0, nil)
	if err != nil {
		t.Fatalf("unfold: %v", err)
	}
	projDecal, err := BuildStickerDecalProjection(context.Background(), model, stickerImg,
		clickCenter, normal, up, scale, 0, nil)
	if err != nil {
		t.Fatalf("projection: %v", err)
	}

	bg := color.NRGBA{30, 30, 30, 255}
	face := color.NRGBA{200, 180, 150, 255}
	const renderW, renderH = 800, 600
	camDir := [3]float64{1, 0, 0.4}

	if _, err := renderDecalToPNG(model, unfoldDecal, stickerImg, camDir,
		renderW, renderH, bg, face,
		"testdata/sticker-renders/dirty_mesh_unfold.png"); err != nil {
		t.Fatalf("render unfold: %v", err)
	}
	if _, err := renderDecalToPNG(model, projDecal, stickerImg, camDir,
		renderW, renderH, bg, face,
		"testdata/sticker-renders/dirty_mesh_projection.png"); err != nil {
		t.Fatalf("render projection: %v", err)
	}

	un := len(unfoldDecal.TriUVs)
	pn := len(projDecal.TriUVs)
	t.Logf("counts: unfold=%d projection=%d", un, pn)

	if un == 0 {
		t.Fatal("unfold produced empty decal — sticker placement totally failed")
	}
	if un > 3*pn {
		t.Errorf("unfold over-expanded: unfold=%d, projection=%d", un, pn)
	}
	matched := 0
	var sumCentroidDiff, maxCentroidDiff float64
	for ti, uvU := range unfoldDecal.TriUVs {
		uvP, ok := projDecal.TriUVs[ti]
		if !ok {
			continue
		}
		matched++
		cuU := (uvU[0][0] + uvU[1][0] + uvU[2][0]) / 3
		cvU := (uvU[0][1] + uvU[1][1] + uvU[2][1]) / 3
		cuP := (uvP[0][0] + uvP[1][0] + uvP[2][0]) / 3
		cvP := (uvP[0][1] + uvP[1][1] + uvP[2][1]) / 3
		dC := math.Sqrt(float64((cuU-cuP)*(cuU-cuP) + (cvU-cvP)*(cvU-cvP)))
		sumCentroidDiff += dC
		if dC > maxCentroidDiff {
			maxCentroidDiff = dC
		}
	}
	matchFrac := float64(matched) / float64(un)
	meanCentroidDiff := 0.0
	if matched > 0 {
		meanCentroidDiff = sumCentroidDiff / float64(matched)
	}
	t.Logf("matched: %d / %d (%.1f%%); centroid diff mean=%.3f max=%.3f",
		matched, un, 100*matchFrac, meanCentroidDiff, maxCentroidDiff)
	if matchFrac < 0.50 {
		t.Errorf("only %.1f%% of unfold tris are in projection — BFS leaked",
			100*matchFrac)
	}
}
