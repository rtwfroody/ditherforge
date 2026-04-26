package voxel

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// Helper: shoot a sticker at a mesh and return the decal. Fails the test on
// any error.
func runStickerUnfold(
	t *testing.T,
	model *loader.LoadedModel,
	center [3]float64,
	normal [3]float64,
	up [3]float64,
	scale float64,
	imgSize int,
) *StickerDecal {
	t.Helper()
	adj := BuildTriAdjacency(model)
	si := NewSpatialIndex(model, 1)
	seedTri := FindSeedTriangle(center, model, si)
	if seedTri < 0 {
		t.Fatal("no seed triangle")
	}
	img := image.NewNRGBA(image.Rect(0, 0, imgSize, imgSize))
	for y := 0; y < imgSize; y++ {
		for x := 0; x < imgSize; x++ {
			img.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
		}
	}
	decal, err := BuildStickerDecal(context.Background(), model, adj, img,
		seedTri, center, normal, up, scale, 0, 0, nil)
	if err != nil {
		t.Fatalf("BuildStickerDecal: %v", err)
	}
	return decal
}

// rasterizeUVCoverage rasterizes the decal's per-triangle UVs into a grid×grid
// boolean buffer, returning what fraction of cells are covered overall and
// per quadrant. Quadrants are: 0=lower-left, 1=lower-right, 2=upper-left,
// 3=upper-right (UV: u increases right, v increases up).
func rasterizeUVCoverage(decal *StickerDecal, grid int) (overall float32, perQuad [4]float32) {
	covered := make([]bool, grid*grid)
	for _, uvs := range decal.TriUVs {
		minU, maxU := uvs[0][0], uvs[0][0]
		minV, maxV := uvs[0][1], uvs[0][1]
		for _, uv := range uvs[1:] {
			if uv[0] < minU {
				minU = uv[0]
			}
			if uv[0] > maxU {
				maxU = uv[0]
			}
			if uv[1] < minV {
				minV = uv[1]
			}
			if uv[1] > maxV {
				maxV = uv[1]
			}
		}
		if maxU <= 0 || minU >= 1 || maxV <= 0 || minV >= 1 {
			continue
		}
		px0 := int(math.Floor(float64(minU) * float64(grid)))
		py0 := int(math.Floor(float64(minV) * float64(grid)))
		px1 := int(math.Ceil(float64(maxU) * float64(grid)))
		py1 := int(math.Ceil(float64(maxV) * float64(grid)))
		if px0 < 0 {
			px0 = 0
		}
		if py0 < 0 {
			py0 = 0
		}
		if px1 > grid {
			px1 = grid
		}
		if py1 > grid {
			py1 = grid
		}
		tri := [3][2]float32{
			{uvs[0][0] * float32(grid), uvs[0][1] * float32(grid)},
			{uvs[1][0] * float32(grid), uvs[1][1] * float32(grid)},
			{uvs[2][0] * float32(grid), uvs[2][1] * float32(grid)},
		}
		const baryEps = float32(1e-3)
		for py := py0; py < py1; py++ {
			cy := float32(py) + 0.5
			for px := px0; px < px1; px++ {
				cx := float32(px) + 0.5
				bary, ok := barycentric2D(cx, cy, tri)
				if !ok {
					continue
				}
				if bary[0] < -baryEps || bary[1] < -baryEps || bary[2] < -baryEps {
					continue
				}
				covered[py*grid+px] = true
			}
		}
	}
	var totalCells int
	var quadCells [4]int
	var totalCovered int
	var quadCovered [4]int
	half := grid / 2
	for py := 0; py < grid; py++ {
		for px := 0; px < grid; px++ {
			q := 0
			if px >= half {
				q |= 1
			}
			if py >= half {
				q |= 2
			}
			totalCells++
			quadCells[q]++
			if covered[py*grid+px] {
				totalCovered++
				quadCovered[q]++
			}
		}
	}
	overall = float32(totalCovered) / float32(totalCells)
	for q := 0; q < 4; q++ {
		perQuad[q] = float32(quadCovered[q]) / float32(quadCells[q])
	}
	return overall, perQuad
}

// triUVWinding returns the signed area × 2 of a 2D triangle. Positive =
// counter-clockwise, the orientation we want preserved by LSCM.
func triUVWinding(uvs [3][2]float32) float32 {
	x0, y0 := uvs[0][0], uvs[0][1]
	x1, y1 := uvs[1][0], uvs[1][1]
	x2, y2 := uvs[2][0], uvs[2][1]
	return (x1-x0)*(y2-y0) - (x2-x0)*(y1-y0)
}

// edgeLengthRatios returns max(d2/d3) and min(d2/d3) over all output
// triangle edges, where d3 is the 3D edge length and d2 is the UV edge
// length scaled by the rect's tangent extent (so a perfect isometric unfold
// yields ratio 1.0).
func edgeLengthRatios(model *loader.LoadedModel, decal *StickerDecal, halfW, halfH float32) (maxR, minR float64) {
	maxR = 0
	minR = math.MaxFloat64
	any := false
	for ti, uvs := range decal.TriUVs {
		f := model.Faces[ti]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]
		// UV → tangent units: (u-0.5)*2*halfW, (v-0.5)*2*halfH
		tc := [3][2]float32{
			{(uvs[0][0]*2 - 1) * halfW, (uvs[0][1]*2 - 1) * halfH},
			{(uvs[1][0]*2 - 1) * halfW, (uvs[1][1]*2 - 1) * halfH},
			{(uvs[2][0]*2 - 1) * halfW, (uvs[2][1]*2 - 1) * halfH},
		}
		pairs := [][2]int{{0, 1}, {1, 2}, {2, 0}}
		verts3D := [3][3]float32{v0, v1, v2}
		for _, p := range pairs {
			a, b := p[0], p[1]
			pa := verts3D[a]
			pb := verts3D[b]
			d3 := math.Sqrt(float64(
				(pa[0]-pb[0])*(pa[0]-pb[0]) +
					(pa[1]-pb[1])*(pa[1]-pb[1]) +
					(pa[2]-pb[2])*(pa[2]-pb[2])))
			d2 := math.Sqrt(float64(
				(tc[a][0]-tc[b][0])*(tc[a][0]-tc[b][0]) +
					(tc[a][1]-tc[b][1])*(tc[a][1]-tc[b][1])))
			if d3 < 1e-6 {
				continue
			}
			r := d2 / d3
			if r > maxR {
				maxR = r
			}
			if r < minR {
				minR = r
			}
			any = true
		}
	}
	if !any {
		return 0, 0
	}
	return maxR, minR
}

// TestStickerUnfoldFlatPlaneFullCoverage: a sticker on a flat plane should
// cover all four quadrants of its rect roughly evenly (the rect maps to a
// chunk of the plane via the trivial isometric map). Catches the "right
// half only" / "upper-right quadrant only" failure modes.
func TestStickerUnfoldFlatPlaneFullCoverage(t *testing.T) {
	model := makeFlatGrid(20, 20)
	decal := runStickerUnfold(t, model,
		[3]float64{10, 10, 0},
		[3]float64{0, 0, 1},
		[3]float64{0, 1, 0},
		4, 16)

	overall, perQuad := rasterizeUVCoverage(decal, 64)
	if overall < 0.85 {
		t.Errorf("overall coverage = %.2f, want ≥ 0.85", overall)
	}
	for q, c := range perQuad {
		if c < 0.50 {
			t.Errorf("quadrant %d coverage = %.2f, want ≥ 0.50", q, c)
		}
	}
	t.Logf("flat plane: overall=%.2f, quads=%v", overall, perQuad)
}

// TestStickerUnfoldNoTriangleFlips: every output triangle must have a
// consistent winding (all positive or all negative). LSCM cannot produce
// flips by construction; this test guards against accidental sign flips
// from coordinate-system mistakes elsewhere in the pipeline.
func TestStickerUnfoldNoTriangleFlips(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model *loader.LoadedModel
		ctr   [3]float64
		nrm   [3]float64
		up    [3]float64
		scale float64
	}{
		{
			"flat plane",
			makeFlatGrid(20, 20),
			[3]float64{10, 10, 0}, [3]float64{0, 0, 1}, [3]float64{0, 1, 0}, 4,
		},
		{
			"cylinder arc",
			makeCylinderArc(5, 4, 90, 16),
			[3]float64{5, 0, 0}, [3]float64{1, 0, 0}, [3]float64{0, 0, 1}, 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decal := runStickerUnfold(t, tc.model, tc.ctr, tc.nrm, tc.up, tc.scale, 16)
			if len(decal.TriUVs) == 0 {
				t.Fatal("no triangles in decal")
			}
			var sgn int
			for _, uvs := range decal.TriUVs {
				w := triUVWinding(uvs)
				if w > 1e-9 {
					if sgn == -1 {
						t.Errorf("found mixed windings — flip present")
						return
					}
					sgn = 1
				} else if w < -1e-9 {
					if sgn == 1 {
						t.Errorf("found mixed windings — flip present")
						return
					}
					sgn = -1
				}
			}
			t.Logf("%s: %d tris, all winding sign = %d", tc.name, len(decal.TriUVs), sgn)
		})
	}
}

// TestStickerUnfoldCylinderArcIsometric: a sticker on the side of a wide-
// arc open cylinder must come out near-isometric (every edge length ratio
// ≈ 1). This is the regression test for top.json's stretched-on-cylinder
// symptom — an open arc subset of the same kind of geometry.
func TestStickerUnfoldCylinderArcIsometric(t *testing.T) {
	const r = 5.0
	const arcDeg = 90 // far wider than a real sticker would span
	model := makeCylinderArc(r, 4, arcDeg, 32)
	scale := 3.0
	decal := runStickerUnfold(t, model,
		[3]float64{r, 0, 0},
		[3]float64{1, 0, 0},
		[3]float64{0, 0, 1},
		scale, 16)

	if len(decal.TriUVs) == 0 {
		t.Fatal("decal empty")
	}
	halfW := float32(scale / 2)
	halfH := float32(scale / 2)
	maxR, minR := edgeLengthRatios(model, decal, halfW, halfH)
	if maxR > 1.10 || minR < 0.90 {
		t.Errorf("cylinder isometry ratio out of [0.90, 1.10]: max=%.3f min=%.3f", maxR, minR)
	}
	t.Logf("cylinder arc: max=%.3f min=%.3f over %d tris", maxR, minR, len(decal.TriUVs))
}

// TestStickerUnfoldSphereBoundedDistortion: a sticker on a sphere
// (positive Gaussian curvature, isometric map impossible) must come out
// with bounded distortion — no edges blowing up or collapsing
// catastrophically. The conformal map of a sphere is essentially
// stereographic, so edges far from the pin can compress; we check the
// distortion stays in a tolerable window.
func TestStickerUnfoldSphereBoundedDistortion(t *testing.T) {
	const r = 5.0
	model := makeUVSphere(r, 16, 32)
	// Use a small sticker relative to sphere radius so curvature distortion
	// stays modest (rect covers ≈ 12° arc).
	scale := 1.0
	decal := runStickerUnfold(t, model,
		[3]float64{0, 0, r}, // click at north pole
		[3]float64{0, 0, 1},
		[3]float64{0, 1, 0},
		scale, 16)

	if len(decal.TriUVs) == 0 {
		t.Fatal("decal empty")
	}
	halfW := float32(scale / 2)
	halfH := float32(scale / 2)
	maxR, minR := edgeLengthRatios(model, decal, halfW, halfH)
	// Bounded distortion: no edge stretched beyond 3× or compressed below
	// 1/3. That window is loose enough to accept stereographic-style
	// stretch on a small sphere section but catches catastrophic failures.
	if maxR > 3 || minR < 1.0/3 {
		t.Errorf("sphere distortion outside [1/3, 3]: max=%.3f min=%.3f", maxR, minR)
	}
	t.Logf("sphere: max=%.3f min=%.3f over %d tris", maxR, minR, len(decal.TriUVs))
}

// TestStickerUnfoldVsProjectionOnCylinder applies the same sticker via
// both unfold and projection modes to the same spot on an open cylinder
// arc, and compares the per-face UV mappings. On a developable cylinder
// section spanning ≤ ~45° in the sticker direction, the chord-vs-arc
// difference is small — two methods that both produce *valid* mappings
// should land within a small tolerance of each other for every face that
// appears in both decals.
//
// This is the regression catch for the "unfold output looks awful" failure
// mode on real cylinder-like meshes (top.json / base.json): unfold's UVs
// drift far from where projection would land them, even though projection
// is known to look reasonable on these meshes.
func TestStickerUnfoldVsProjectionOnCylinder(t *testing.T) {
	// Cylinder: r=5, h=4, 90° arc spanning -45°..+45° around +X axis,
	// 32 segs along the arc. Triangulation density ~5×5/(0.18×0.13)
	// per face, fine enough that BFS sees plenty of detail without
	// dominating the test runtime.
	const (
		r      = 5.0
		h      = 4.0
		arcDeg = 90
		segs   = 32
	)
	model := makeCylinderArc(r, h, arcDeg, segs)

	// Click at the apex of the arc (center of the +X face).
	clickCenter := [3]float64{r, 0, 0}
	normal := [3]float64{1, 0, 0}
	up := [3]float64{0, 0, 1}
	// Sticker scale 3 → halfW=1.5 → tangent span covers asin(1.5/5)≈17.5°
	// each direction. Total ≈ 35°, comfortably under the user's 45° case.
	scale := 3.0

	adj := BuildTriAdjacency(model)
	si := NewSpatialIndex(model, 1)
	seedTri := FindSeedTriangle(clickCenter, model, si)
	if seedTri < 0 {
		t.Fatal("no seed triangle found")
	}
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
		}
	}

	unfoldDecal, err := BuildStickerDecal(context.Background(), model, adj, img,
		seedTri, clickCenter, normal, up, scale, 0, 0, nil)
	if err != nil {
		t.Fatalf("BuildStickerDecal (unfold): %v", err)
	}
	projDecal, err := BuildStickerDecalProjection(context.Background(), model, img,
		clickCenter, normal, up, scale, 0, nil)
	if err != nil {
		t.Fatalf("BuildStickerDecal (projection): %v", err)
	}
	if len(unfoldDecal.TriUVs) == 0 {
		t.Fatal("unfold decal empty")
	}
	if len(projDecal.TriUVs) == 0 {
		t.Fatal("projection decal empty")
	}

	// For each face appearing in both decals, compare UV centroids and the
	// per-vertex UV values. A 45°-arc cylinder section has at most ~4%
	// chord-vs-arc UV deviation; we set the tolerance generously at 0.10
	// (10% of the unit rect) to allow for boundary effects and accept
	// only catastrophic disagreement as failure.
	const uvTol = 0.10
	nMatched := 0
	maxCentroidDiff := float32(0)
	maxVertDiff := float32(0)
	for ti, uvU := range unfoldDecal.TriUVs {
		uvP, ok := projDecal.TriUVs[ti]
		if !ok {
			continue
		}
		nMatched++
		// Centroid comparison.
		cuU := (uvU[0][0] + uvU[1][0] + uvU[2][0]) / 3
		cvU := (uvU[0][1] + uvU[1][1] + uvU[2][1]) / 3
		cuP := (uvP[0][0] + uvP[1][0] + uvP[2][0]) / 3
		cvP := (uvP[0][1] + uvP[1][1] + uvP[2][1]) / 3
		dC := float32(math.Sqrt(float64((cuU-cuP)*(cuU-cuP) + (cvU-cvP)*(cvU-cvP))))
		if dC > maxCentroidDiff {
			maxCentroidDiff = dC
		}
		// Per-vertex comparison.
		for k := 0; k < 3; k++ {
			du := uvU[k][0] - uvP[k][0]
			dv := uvU[k][1] - uvP[k][1]
			d := float32(math.Sqrt(float64(du*du + dv*dv)))
			if d > maxVertDiff {
				maxVertDiff = d
			}
		}
	}

	if nMatched == 0 {
		t.Fatal("no faces in both decals")
	}
	t.Logf("matched %d faces; max centroid diff = %.3f; max vertex diff = %.3f",
		nMatched, maxCentroidDiff, maxVertDiff)

	if maxCentroidDiff > uvTol {
		t.Errorf("max centroid UV diff = %.3f > tolerance %.3f — unfold disagrees with projection on cylinder",
			maxCentroidDiff, uvTol)
	}
	if maxVertDiff > uvTol*2 {
		t.Errorf("max per-vertex UV diff = %.3f > tolerance %.3f", maxVertDiff, uvTol*2)
	}
}

// TestStickerUnfoldVsProjectionLargeStickerOnClosedCylinder pushes the
// scale up so the sticker covers a substantial fraction of the cylinder
// (~90° span). Conformal flattening of that much curvature is no longer
// near-isometric; we only require unfold and projection to agree to
// within a moderate tolerance, but a wraparound bug or completely
// scrambled UV layout would show a much larger discrepancy.
func TestStickerUnfoldVsProjectionLargeStickerOnClosedCylinder(t *testing.T) {
	const (
		r    = 5.0
		h    = 4.0
		segs = 128
	)
	model := makeClosedCylinder(r, h, segs)

	clickCenter := [3]float64{r, 0, 0}
	normal := [3]float64{1, 0, 0}
	up := [3]float64{0, 0, 1}
	// Scale 7 → halfW=3.5, asin(3.5/5)≈44° → ~88° total span.
	scale := 7.0

	adj := BuildTriAdjacency(model)
	si := NewSpatialIndex(model, 1)
	seedTri := FindSeedTriangle(clickCenter, model, si)
	if seedTri < 0 {
		t.Fatal("no seed tri")
	}
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
		}
	}

	unfoldDecal, err := BuildStickerDecal(context.Background(), model, adj, img,
		seedTri, clickCenter, normal, up, scale, 0, 0, nil)
	if err != nil {
		t.Fatalf("unfold: %v", err)
	}
	projDecal, err := BuildStickerDecalProjection(context.Background(), model, img,
		clickCenter, normal, up, scale, 0, nil)
	if err != nil {
		t.Fatalf("projection: %v", err)
	}

	// No back-side wraparound.
	for ti := range unfoldDecal.TriUVs {
		f := model.Faces[ti]
		cx := (model.Vertices[f[0]][0] + model.Vertices[f[1]][0] + model.Vertices[f[2]][0]) / 3
		if cx < -0.5 {
			t.Errorf("unfold includes back-side tri %d (cx=%.2f)", ti, cx)
		}
	}

	// Coverage parity: unfold shouldn't be wildly more numerous than projection.
	// Both should cover similar portions of the front of the cylinder.
	un := len(unfoldDecal.TriUVs)
	pn := len(projDecal.TriUVs)
	if un > 2*pn || pn > 2*un {
		t.Errorf("decal coverage mismatch: unfold=%d, projection=%d (one is >2× the other)", un, pn)
	}

	// UV agreement on matched faces. Larger tolerance than the small-sticker
	// test because conformal stretching becomes meaningful at this span.
	const uvTol = 0.20
	maxCentroidDiff := float32(0)
	nMatched := 0
	for ti, uvU := range unfoldDecal.TriUVs {
		uvP, ok := projDecal.TriUVs[ti]
		if !ok {
			continue
		}
		nMatched++
		cuU := (uvU[0][0] + uvU[1][0] + uvU[2][0]) / 3
		cvU := (uvU[0][1] + uvU[1][1] + uvU[2][1]) / 3
		cuP := (uvP[0][0] + uvP[1][0] + uvP[2][0]) / 3
		cvP := (uvP[0][1] + uvP[1][1] + uvP[2][1]) / 3
		dC := float32(math.Sqrt(float64((cuU-cuP)*(cuU-cuP) + (cvU-cvP)*(cvU-cvP))))
		if dC > maxCentroidDiff {
			maxCentroidDiff = dC
		}
	}

	t.Logf("large sticker: unfold=%d, proj=%d, matched=%d, max centroid diff=%.3f",
		un, pn, nMatched, maxCentroidDiff)

	if maxCentroidDiff > uvTol {
		t.Errorf("max centroid UV diff = %.3f > tolerance %.3f", maxCentroidDiff, uvTol)
	}
}

// TestStickerUnfoldVsProjectionOnClosedCylinder is the same comparison as
// TestStickerUnfoldVsProjectionOnCylinder, but on a closed cylinder (full
// 360°). The user's failing case: a sticker spanning ~45° of a closed
// cylindrical surface. BFS must only include the ~45° region; if it
// over-expands and walks all the way around, LSCM has too much input to
// flatten conformally and the layout becomes a visible mess.
//
// Tested at several segment densities so a regression that only shows up
// with denser triangulation is still caught.
func TestStickerUnfoldVsProjectionOnClosedCylinder(t *testing.T) {
	for _, segs := range []int{32, 64, 128, 256} {
		t.Run(fmt.Sprintf("segs=%d", segs), func(t *testing.T) {
			runClosedCylinderComparison(t, segs)
		})
	}
}

func runClosedCylinderComparison(t *testing.T, segs int) {
	// Closed cylinder: r=5, h=4, segs around. Sticker centered at (5,0,0).
	// Scale 4 → halfW=2 → tangent span asin(2/5)≈23.6° each direction,
	// ~47° total — close to the user's 45° description.
	const (
		r = 5.0
		h = 4.0
	)
	model := makeClosedCylinder(r, h, segs)

	clickCenter := [3]float64{r, 0, 0}
	normal := [3]float64{1, 0, 0}
	up := [3]float64{0, 0, 1}
	scale := 4.0

	adj := BuildTriAdjacency(model)
	si := NewSpatialIndex(model, 1)
	seedTri := FindSeedTriangle(clickCenter, model, si)
	if seedTri < 0 {
		t.Fatal("no seed triangle found")
	}
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
		}
	}

	unfoldDecal, err := BuildStickerDecal(context.Background(), model, adj, img,
		seedTri, clickCenter, normal, up, scale, 0, 0, nil)
	if err != nil {
		t.Fatalf("BuildStickerDecal (unfold): %v", err)
	}
	projDecal, err := BuildStickerDecalProjection(context.Background(), model, img,
		clickCenter, normal, up, scale, 0, nil)
	if err != nil {
		t.Fatalf("BuildStickerDecal (projection): %v", err)
	}

	// Expectation A: unfold should NOT include triangles on the back of
	// the cylinder. Any triangle with x < 0 (back half) appearing in
	// the unfold decal is a wraparound failure.
	for ti := range unfoldDecal.TriUVs {
		f := model.Faces[ti]
		cx := (model.Vertices[f[0]][0] + model.Vertices[f[1]][0] + model.Vertices[f[2]][0]) / 3
		if cx < -0.5 {
			t.Errorf("unfold decal includes back-side tri %d (centroid x=%.2f) — BFS wraparound", ti, cx)
		}
	}

	// Expectation B: per-face UV agreement between unfold and projection,
	// for faces that appear in both.
	const uvTol = 0.10
	nMatched := 0
	maxCentroidDiff := float32(0)
	for ti, uvU := range unfoldDecal.TriUVs {
		uvP, ok := projDecal.TriUVs[ti]
		if !ok {
			continue
		}
		nMatched++
		cuU := (uvU[0][0] + uvU[1][0] + uvU[2][0]) / 3
		cvU := (uvU[0][1] + uvU[1][1] + uvU[2][1]) / 3
		cuP := (uvP[0][0] + uvP[1][0] + uvP[2][0]) / 3
		cvP := (uvP[0][1] + uvP[1][1] + uvP[2][1]) / 3
		dC := float32(math.Sqrt(float64((cuU-cuP)*(cuU-cuP) + (cvU-cvP)*(cvU-cvP))))
		if dC > maxCentroidDiff {
			maxCentroidDiff = dC
		}
	}

	t.Logf("closed cylinder: unfold=%d, proj=%d, matched=%d, max centroid diff=%.3f",
		len(unfoldDecal.TriUVs), len(projDecal.TriUVs), nMatched, maxCentroidDiff)

	if nMatched == 0 {
		t.Fatal("no faces in both decals")
	}
	if maxCentroidDiff > uvTol {
		t.Errorf("max centroid UV diff = %.3f > tolerance %.3f", maxCentroidDiff, uvTol)
	}
}

// TestStickerUnfoldVsProjectionOnSphere applies a sticker at the apex of
// a UV sphere via both modes and compares. A sphere has K>0 so isometric
// is impossible; conformal (LSCM) and projection both stretch differently
// but should still agree to within a generous tolerance for a small enough
// sticker (rect ≪ sphere radius). Catches catastrophic LSCM failures on
// curved (non-developable) input meshes.
func TestStickerUnfoldVsProjectionOnSphere(t *testing.T) {
	const r = 5.0
	model := makeUVSphere(r, 16, 32)

	clickCenter := [3]float64{0, 0, r}
	normal := [3]float64{0, 0, 1}
	up := [3]float64{0, 1, 0}
	scale := 1.5 // halfW=0.75 → ~9° angular radius

	adj := BuildTriAdjacency(model)
	si := NewSpatialIndex(model, 1)
	seedTri := FindSeedTriangle(clickCenter, model, si)
	if seedTri < 0 {
		t.Fatal("no seed tri")
	}
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
		}
	}

	unfoldDecal, err := BuildStickerDecal(context.Background(), model, adj, img,
		seedTri, clickCenter, normal, up, scale, 0, 0, nil)
	if err != nil {
		t.Fatalf("unfold: %v", err)
	}
	projDecal, err := BuildStickerDecalProjection(context.Background(), model, img,
		clickCenter, normal, up, scale, 0, nil)
	if err != nil {
		t.Fatalf("projection: %v", err)
	}

	// Unfold should not include faces on the opposite hemisphere (z<0).
	for ti := range unfoldDecal.TriUVs {
		f := model.Faces[ti]
		cz := (model.Vertices[f[0]][2] + model.Vertices[f[1]][2] + model.Vertices[f[2]][2]) / 3
		if cz < 0 {
			t.Errorf("unfold includes far-side sphere tri %d (cz=%.2f)", ti, cz)
		}
	}

	// Coverage parity.
	un := len(unfoldDecal.TriUVs)
	pn := len(projDecal.TriUVs)
	if un > 2*pn || pn > 2*un {
		t.Errorf("decal coverage mismatch: unfold=%d, proj=%d", un, pn)
	}

	// UV agreement.
	const uvTol = 0.15
	maxCentroidDiff := float32(0)
	nMatched := 0
	for ti, uvU := range unfoldDecal.TriUVs {
		uvP, ok := projDecal.TriUVs[ti]
		if !ok {
			continue
		}
		nMatched++
		cuU := (uvU[0][0] + uvU[1][0] + uvU[2][0]) / 3
		cvU := (uvU[0][1] + uvU[1][1] + uvU[2][1]) / 3
		cuP := (uvP[0][0] + uvP[1][0] + uvP[2][0]) / 3
		cvP := (uvP[0][1] + uvP[1][1] + uvP[2][1]) / 3
		dC := float32(math.Sqrt(float64((cuU-cuP)*(cuU-cuP) + (cvU-cvP)*(cvU-cvP))))
		if dC > maxCentroidDiff {
			maxCentroidDiff = dC
		}
	}

	t.Logf("sphere: unfold=%d, proj=%d, matched=%d, max centroid diff=%.3f",
		un, pn, nMatched, maxCentroidDiff)

	if maxCentroidDiff > uvTol {
		t.Errorf("max centroid UV diff = %.3f > tolerance %.3f", maxCentroidDiff, uvTol)
	}
}

// makeUVSphere builds a UV sphere of radius r with `rings` latitude rings
// (north pole + rings-1 latitude bands + south pole) and `segs` segments.
func makeUVSphere(r float32, rings, segs int) *loader.LoadedModel {
	var verts [][3]float32
	verts = append(verts, [3]float32{0, 0, r})
	for j := 1; j < rings; j++ {
		phi := math.Pi * float64(j) / float64(rings)
		z := float32(math.Cos(phi)) * r
		sinPhi := float32(math.Sin(phi)) * r
		for i := 0; i < segs; i++ {
			theta := 2 * math.Pi * float64(i) / float64(segs)
			x := sinPhi * float32(math.Cos(theta))
			y := sinPhi * float32(math.Sin(theta))
			verts = append(verts, [3]float32{x, y, z})
		}
	}
	southIdx := uint32(len(verts))
	verts = append(verts, [3]float32{0, 0, -r})

	var faces [][3]uint32
	for i := 0; i < segs; i++ {
		v1 := uint32(1 + i)
		v2 := uint32(1 + (i+1)%segs)
		faces = append(faces, [3]uint32{0, v1, v2})
	}
	for j := 0; j < rings-2; j++ {
		for i := 0; i < segs; i++ {
			a := uint32(1 + j*segs + i)
			b := uint32(1 + j*segs + (i+1)%segs)
			c := uint32(1 + (j+1)*segs + i)
			d := uint32(1 + (j+1)*segs + (i+1)%segs)
			faces = append(faces, [3]uint32{a, b, d}, [3]uint32{a, d, c})
		}
	}
	base := uint32(1 + (rings-2)*segs)
	for i := 0; i < segs; i++ {
		v1 := base + uint32((i+1)%segs)
		v2 := base + uint32(i)
		faces = append(faces, [3]uint32{southIdx, v1, v2})
	}
	return &loader.LoadedModel{Vertices: verts, Faces: faces}
}
