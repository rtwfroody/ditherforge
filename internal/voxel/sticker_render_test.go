package voxel

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// renderDecalToPNG writes an orthographic image of the model with each
// decal triangle filled by sampling the sticker image at the per-pixel
// barycentric UV; non-decal triangles render in a Lambert-shaded
// faceColor (lit from the camera direction so the shape is legible).
// Useful for eyeballing what a sticker actually looks like on a mesh in
// tests.
//
// camDir: vector pointing FROM the scene TOWARD the camera. The camera
// looks along -camDir. e.g. {1,0,0} means the camera sits on the +X axis
// looking toward -X (so +X faces of the model are visible).
//
// Outputs the image at out (PNG). Returns the resolved absolute path.
func renderDecalToPNG(
	model *loader.LoadedModel,
	decal *StickerDecal,
	stickerImg image.Image,
	camDir [3]float64,
	imgW, imgH int,
	bgColor color.NRGBA,
	faceColor color.NRGBA,
	out string,
) (string, error) {
	// Build an orthonormal basis. Depth axis n points toward the camera
	// (away from scene), so a vertex's d = pos·n is largest when closest
	// to the camera.
	n := normalize3(camDir)
	var u [3]float64
	if math.Abs(n[1]) < 0.9 {
		u = normalize3(cross3([3]float64{0, 1, 0}, n))
	} else {
		u = normalize3(cross3([3]float64{1, 0, 0}, n))
	}
	v := normalize3(cross3(n, u))

	// Project all vertices to 2D + depth.
	type pv struct{ u, v, d float32 }
	proj := make([]pv, len(model.Vertices))
	minU, maxU := float32(math.Inf(1)), float32(math.Inf(-1))
	minV, maxV := float32(math.Inf(1)), float32(math.Inf(-1))
	for i, p := range model.Vertices {
		du := float64(p[0])*u[0] + float64(p[1])*u[1] + float64(p[2])*u[2]
		dv := float64(p[0])*v[0] + float64(p[1])*v[1] + float64(p[2])*v[2]
		dd := float64(p[0])*n[0] + float64(p[1])*n[1] + float64(p[2])*n[2]
		proj[i] = pv{float32(du), float32(dv), float32(dd)}
		if proj[i].u < minU {
			minU = proj[i].u
		}
		if proj[i].u > maxU {
			maxU = proj[i].u
		}
		if proj[i].v < minV {
			minV = proj[i].v
		}
		if proj[i].v > maxV {
			maxV = proj[i].v
		}
	}
	if maxU == minU || maxV == minV {
		return "", fmt.Errorf("renderDecalToPNG: empty projection bounds")
	}
	pad := float32(0.05)
	w := maxU - minU
	h := maxV - minV
	minU -= w * pad
	maxU += w * pad
	minV -= h * pad
	maxV += h * pad

	// Fit while preserving aspect.
	rangeU := maxU - minU
	rangeV := maxV - minV
	scaleU := float32(imgW) / rangeU
	scaleV := float32(imgH) / rangeV
	scale := scaleU
	if scaleV < scale {
		scale = scaleV
	}

	img := image.NewNRGBA(image.Rect(0, 0, imgW, imgH))
	for y := 0; y < imgH; y++ {
		for x := 0; x < imgW; x++ {
			img.SetNRGBA(x, y, bgColor)
		}
	}
	depthBuf := make([]float32, imgW*imgH)
	for i := range depthBuf {
		depthBuf[i] = float32(math.Inf(-1))
	}

	stBounds := stickerImg.Bounds()
	stW := stBounds.Dx()
	stH := stBounds.Dy()

	// Lambert shade non-decal faces so the mesh shape is legible. Light
	// direction = camera direction.
	shadeFace := func(faceNormal [3]float64) color.NRGBA {
		l := faceNormal[0]*n[0] + faceNormal[1]*n[1] + faceNormal[2]*n[2]
		if l < 0 {
			l = 0
		}
		// Ambient + diffuse to keep back faces visible too.
		k := 0.25 + 0.75*l
		return color.NRGBA{
			uint8(float64(faceColor.R) * k),
			uint8(float64(faceColor.G) * k),
			uint8(float64(faceColor.B) * k),
			faceColor.A,
		}
	}

	for ti := range model.Faces {
		f := model.Faces[ti]
		a := proj[f[0]]
		b := proj[f[1]]
		c := proj[f[2]]
		fn := faceNormal32(model, int32(ti))
		fnD := [3]float64{float64(fn[0]), float64(fn[1]), float64(fn[2])}
		px0 := (a.u-minU)*scale + (float32(imgW)-rangeU*scale)*0.5
		py0 := float32(imgH) - 1 - ((a.v-minV)*scale + (float32(imgH)-rangeV*scale)*0.5)
		px1 := (b.u-minU)*scale + (float32(imgW)-rangeU*scale)*0.5
		py1 := float32(imgH) - 1 - ((b.v-minV)*scale + (float32(imgH)-rangeV*scale)*0.5)
		px2 := (c.u-minU)*scale + (float32(imgW)-rangeU*scale)*0.5
		py2 := float32(imgH) - 1 - ((c.v-minV)*scale + (float32(imgH)-rangeV*scale)*0.5)

		minX := int(math.Floor(float64(min(px0, min(px1, px2)))))
		maxX := int(math.Ceil(float64(max(px0, max(px1, px2)))))
		minY := int(math.Floor(float64(min(py0, min(py1, py2)))))
		maxY := int(math.Ceil(float64(max(py0, max(py1, py2)))))
		if minX < 0 {
			minX = 0
		}
		if minY < 0 {
			minY = 0
		}
		if maxX > imgW {
			maxX = imgW
		}
		if maxY > imgH {
			maxY = imgH
		}

		uvs, hasDecal := decal.TriUVs[int32(ti)]
		tri := [3][2]float32{{px0, py0}, {px1, py1}, {px2, py2}}

		shaded := shadeFace(fnD)
		for py := minY; py < maxY; py++ {
			for px := minX; px < maxX; px++ {
				bary, ok := barycentric2D(float32(px)+0.5, float32(py)+0.5, tri)
				if !ok || bary[0] < 0 || bary[1] < 0 || bary[2] < 0 {
					continue
				}
				d := bary[0]*a.d + bary[1]*b.d + bary[2]*c.d
				idx := py*imgW + px
				// Keep largest d (closest to camera, since n points toward camera).
				if d < depthBuf[idx] {
					continue
				}
				depthBuf[idx] = d
				var fillC color.NRGBA
				if hasDecal {
					su := bary[0]*uvs[0][0] + bary[1]*uvs[1][0] + bary[2]*uvs[2][0]
					sv := bary[0]*uvs[0][1] + bary[1]*uvs[1][1] + bary[2]*uvs[2][1]
					if su < 0 || su > 1 || sv < 0 || sv > 1 {
						fillC = shaded
					} else {
						sx := int(su*float32(stW-1)) + stBounds.Min.X
						sy := int((1-sv)*float32(stH-1)) + stBounds.Min.Y
						r, g, bb, aa := stickerImg.At(sx, sy).RGBA()
						if aa < 0x0100 {
							fillC = shaded
						} else {
							fillC = color.NRGBA{
								uint8(r * 0xFF / aa),
								uint8(g * 0xFF / aa),
								uint8(bb * 0xFF / aa),
								uint8(aa >> 8),
							}
						}
					}
				} else {
					fillC = shaded
				}
				img.SetNRGBA(px, py, fillC)
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(out)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		return "", err
	}
	abs, _ := filepath.Abs(out)
	return abs, nil
}

// makeStickerTestPattern produces a sticker image with high-contrast
// quadrant colors (red TL, green TR, blue BL, yellow BR) plus a black
// "+" through the centre. Easy to visually identify the mapping.
func makeStickerTestPattern(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var c color.NRGBA
			switch {
			case x < w/2 && y < h/2:
				c = color.NRGBA{255, 0, 0, 255} // red TL
			case x >= w/2 && y < h/2:
				c = color.NRGBA{0, 200, 0, 255} // green TR
			case x < w/2 && y >= h/2:
				c = color.NRGBA{0, 0, 255, 255} // blue BL
			default:
				c = color.NRGBA{255, 255, 0, 255} // yellow BR
			}
			// "+" cross
			if (x >= w/2-1 && x <= w/2) || (y >= h/2-1 && y <= h/2) {
				c = color.NRGBA{0, 0, 0, 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// makeFilletedBowlRim builds a synthetic mesh that mirrors top.json: an
// annular ring (outer wall + top + inner wall + bottom) with rounded
// fillets of radius f at every corner instead of sharp 90° folds.
//
// Reproducing the actual failure requires fillets — sharp 90° corners are
// stopped by a per-step dihedral filter, but on a fillet each ring step
// only changes the normal by ~90°/fSegs, well below any reasonable
// per-step threshold. BFS walks around the fillet ring-by-ring and ends
// up on the top / inner wall.
//
// The cross-section profile is built once in (r, z) space then revolved
// around the z-axis with `segs` angular steps.
func makeFilletedBowlRim(rOuter, rInner, h, f float32, segs, fSegs int) *loader.LoadedModel {
	type rz struct{ r, z float32 }
	var profile []rz

	addArc := func(rc, zc, startAng float32) {
		for k := 1; k < fSegs; k++ {
			a := float64(startAng) + float64(k)*math.Pi/2/float64(fSegs)
			profile = append(profile, rz{
				rc + f*float32(math.Cos(a)),
				zc + f*float32(math.Sin(a)),
			})
		}
	}

	// Walk the cross-section CCW in (r, z): outer wall → top → inner wall →
	// bottom → back to start. Each wall endpoint is also the start/end of
	// an adjacent fillet, recorded once.
	profile = append(profile, rz{rOuter, -h/2 + f}) // outer wall bot
	profile = append(profile, rz{rOuter, h/2 - f})  // outer wall top
	addArc(rOuter-f, h/2-f, 0)                      // fillet TR (α 0→π/2)
	profile = append(profile, rz{rOuter - f, h / 2})
	profile = append(profile, rz{rInner + f, h / 2})
	addArc(rInner+f, h/2-f, math.Pi/2)              // fillet TL (α π/2→π)
	profile = append(profile, rz{rInner, h/2 - f})  // inner wall top
	profile = append(profile, rz{rInner, -h/2 + f}) // inner wall bot
	addArc(rInner+f, -h/2+f, math.Pi)               // fillet BL (α π→3π/2)
	profile = append(profile, rz{rInner + f, -h / 2})
	profile = append(profile, rz{rOuter - f, -h / 2})
	addArc(rOuter-f, -h/2+f, 3*math.Pi/2)           // fillet BR (α 3π/2→2π)
	// last point is implicit (loops back to profile[0]).

	nP := len(profile)
	verts := make([][3]float32, 0, segs*nP)
	for i := 0; i < segs; i++ {
		theta := 2 * math.Pi * float64(i) / float64(segs)
		ct := float32(math.Cos(theta))
		st := float32(math.Sin(theta))
		for _, p := range profile {
			verts = append(verts, [3]float32{p.r * ct, p.r * st, p.z})
		}
	}
	idx := func(i, j int) uint32 { return uint32((i%segs)*nP + j) }

	var faces [][3]uint32
	for i := 0; i < segs; i++ {
		i2 := i + 1
		for j := 0; j < nP; j++ {
			j2 := (j + 1) % nP
			a := idx(i, j)
			b := idx(i2, j)
			c := idx(i, j2)
			d := idx(i2, j2)
			// Wind so the outward face faces away from z-axis (towards +r),
			// matching the un-filleted version.
			faces = append(faces, [3]uint32{a, b, d}, [3]uint32{a, d, c})
		}
	}
	return &loader.LoadedModel{Vertices: verts, Faces: faces}
}

// makeBowlRim builds a synthetic mesh resembling top.json's failing
// geometry: a thin annular ring (outer cylinder + top annulus + inner
// cylinder + bottom annulus). All 90° folds, manifold, no caps; the
// region a sticker placed on the OUTSIDE rim would cover spans the outer
// cylinder only — it should NOT bleed onto the top, inside, or bottom.
func makeBowlRim(rOuter, rInner, h float32, segs int) *loader.LoadedModel {
	var verts [][3]float32
	for i := 0; i < segs; i++ {
		theta := 2 * math.Pi * float64(i) / float64(segs)
		x := rOuter * float32(math.Cos(theta))
		y := rOuter * float32(math.Sin(theta))
		verts = append(verts, [3]float32{x, y, -h / 2}) // outerBot
	}
	for i := 0; i < segs; i++ {
		theta := 2 * math.Pi * float64(i) / float64(segs)
		x := rOuter * float32(math.Cos(theta))
		y := rOuter * float32(math.Sin(theta))
		verts = append(verts, [3]float32{x, y, h / 2}) // outerTop
	}
	for i := 0; i < segs; i++ {
		theta := 2 * math.Pi * float64(i) / float64(segs)
		x := rInner * float32(math.Cos(theta))
		y := rInner * float32(math.Sin(theta))
		verts = append(verts, [3]float32{x, y, -h / 2}) // innerBot
	}
	for i := 0; i < segs; i++ {
		theta := 2 * math.Pi * float64(i) / float64(segs)
		x := rInner * float32(math.Cos(theta))
		y := rInner * float32(math.Sin(theta))
		verts = append(verts, [3]float32{x, y, h / 2}) // innerTop
	}
	outerBot := func(i int) uint32 { return uint32(i % segs) }
	outerTop := func(i int) uint32 { return uint32(segs + (i % segs)) }
	innerBot := func(i int) uint32 { return uint32(2*segs + (i % segs)) }
	innerTop := func(i int) uint32 { return uint32(3*segs + (i % segs)) }

	var faces [][3]uint32
	for i := 0; i < segs; i++ {
		// Outer wall (normal outward).
		faces = append(faces,
			[3]uint32{outerBot(i), outerBot(i + 1), outerTop(i + 1)},
			[3]uint32{outerBot(i), outerTop(i + 1), outerTop(i)})
		// Inner wall (normal inward).
		faces = append(faces,
			[3]uint32{innerBot(i), innerTop(i + 1), innerBot(i + 1)},
			[3]uint32{innerBot(i), innerTop(i), innerTop(i + 1)})
		// Top annulus (normal +Z).
		faces = append(faces,
			[3]uint32{outerTop(i), outerTop(i + 1), innerTop(i + 1)},
			[3]uint32{outerTop(i), innerTop(i + 1), innerTop(i)})
		// Bottom annulus (normal -Z).
		faces = append(faces,
			[3]uint32{outerBot(i), innerBot(i + 1), outerBot(i + 1)},
			[3]uint32{outerBot(i), innerBot(i), innerBot(i + 1)})
	}
	return &loader.LoadedModel{Vertices: verts, Faces: faces}
}

// TestStickerUnfoldOnFilletedBowlRim is the closest synthetic match to
// the user's failing top.json: a bowl-rim mesh whose corners are rounded
// fillets instead of 90° folds. Each fillet step's dihedral is ~90°/fSegs
// — small enough that a per-step dihedral cap cannot stop BFS from
// walking around the fillet onto the top/inner panels.
//
// Renders both unfold and projection to PNG so the failure mode (and any
// fix) is visible at a glance.
func TestStickerUnfoldOnFilletedBowlRim(t *testing.T) {
	const (
		rOuter = 50.0
		rInner = 45.0
		h      = 14.0
		fillet = 2.0
		segs   = 96
		fSegs  = 8
	)
	model := makeFilletedBowlRim(rOuter, rInner, h, fillet, segs, fSegs)

	clickCenter := [3]float64{rOuter, 0, 0}
	normal := [3]float64{1, 0, 0}
	up := [3]float64{0, 0, 1}
	scale := 27.0 // user's reported sticker scale

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

	bg := color.NRGBA{40, 40, 40, 255}
	face := color.NRGBA{200, 200, 200, 255}
	camDir := [3]float64{1, 0, 0.6}
	const renderW, renderH = 600, 400

	unfoldPath, err := renderDecalToPNG(model, unfoldDecal, stickerImg,
		camDir, renderW, renderH, bg, face,
		"testdata/sticker-renders/filleted_bowl_rim_unfold.png")
	if err != nil {
		t.Fatalf("render unfold: %v", err)
	}
	projPath, err := renderDecalToPNG(model, projDecal, stickerImg,
		camDir, renderW, renderH, bg, face,
		"testdata/sticker-renders/filleted_bowl_rim_projection.png")
	if err != nil {
		t.Fatalf("render projection: %v", err)
	}
	t.Logf("renders: unfold=%s  projection=%s", unfoldPath, projPath)

	// Behavioral assertion: every triangle in the unfold decal must also
	// be in the projection decal (since projection is the ground truth
	// for "tris physically reachable by the sticker"), and the per-face
	// UV layouts should agree to within a tolerance. This is what
	// catches BFS over-expansion regardless of the specific shape.
	un := len(unfoldDecal.TriUVs)
	pn := len(projDecal.TriUVs)
	if un > 2*pn {
		t.Errorf("unfold over-expanded: unfold=%d tris, projection=%d", un, pn)
	}
	missingFromProj := 0
	for ti := range unfoldDecal.TriUVs {
		if _, ok := projDecal.TriUVs[ti]; !ok {
			missingFromProj++
		}
	}
	// Allow a small number of unfold-only tris (boundary differences
	// between the two methods are expected); flag anything large.
	if missingFromProj > pn/4 {
		t.Errorf("unfold included %d tris missing from projection (>25%% of projection coverage %d)",
			missingFromProj, pn)
	}

	const uvTol = 0.15
	maxCentroidDiff := float32(0)
	matched := 0
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
		dC := float32(math.Sqrt(float64((cuU-cuP)*(cuU-cuP) + (cvU-cvP)*(cvU-cvP))))
		if dC > maxCentroidDiff {
			maxCentroidDiff = dC
		}
	}
	t.Logf("filleted bowl rim: unfold=%d, proj=%d, matched=%d, missing-from-proj=%d, max centroid diff=%.3f",
		un, pn, matched, missingFromProj, maxCentroidDiff)
	if maxCentroidDiff > uvTol {
		t.Errorf("max centroid UV diff = %.3f > tolerance %.3f", maxCentroidDiff, uvTol)
	}
}

// TestStickerUnfoldOnBowlRim is the synthetic reproduction of the
// top.json failure: a bowl-rim mesh with 90° folds at outer/top/inner
// transitions. A sticker on the outer wall should stay on the outer
// wall — not bleed across the rim onto the top or inner surfaces.
//
// Renders both unfold and projection to PNG so you can eyeball the
// difference. Test artifacts are written under testdata/sticker-renders/.
func TestStickerUnfoldOnBowlRim(t *testing.T) {
	const (
		rOuter = 50.0
		rInner = 45.0
		h      = 14.0
		segs   = 96
	)
	model := makeBowlRim(rOuter, rInner, h, segs)

	// Click on the outer wall, midway up.
	clickCenter := [3]float64{rOuter, 0, 0}
	normal := [3]float64{1, 0, 0}
	up := [3]float64{0, 0, 1}
	scale := 27.0 // matches user's reported sticker scale

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

	// Render both for visual inspection. 3/4 view from above and to +X so
	// the outer wall, top annulus, and a hint of the inner wall are all
	// visible — same vantage as the user's screenshots of top.json.
	bg := color.NRGBA{40, 40, 40, 255}
	face := color.NRGBA{200, 200, 200, 255}
	camDir := [3]float64{1, 0, 0.6}
	const renderW, renderH = 600, 400

	unfoldPath, err := renderDecalToPNG(model, unfoldDecal, stickerImg,
		camDir, renderW, renderH, bg, face,
		"testdata/sticker-renders/bowl_rim_unfold.png")
	if err != nil {
		t.Fatalf("render unfold: %v", err)
	}
	projPath, err := renderDecalToPNG(model, projDecal, stickerImg,
		camDir, renderW, renderH, bg, face,
		"testdata/sticker-renders/bowl_rim_projection.png")
	if err != nil {
		t.Fatalf("render projection: %v", err)
	}
	t.Logf("renders: unfold=%s  projection=%s", unfoldPath, projPath)

	// Behavioral assertion: unfold must not include any triangle that
	// belongs to the inner wall, top annulus, or bottom annulus. Outer
	// wall vertices are at radius rOuter; everything else is closer to
	// the axis or has |z| ≈ h/2 with smaller radius.
	for ti := range unfoldDecal.TriUVs {
		f := model.Faces[ti]
		// Compute centroid radial distance from z-axis.
		cx := (model.Vertices[f[0]][0] + model.Vertices[f[1]][0] + model.Vertices[f[2]][0]) / 3
		cy := (model.Vertices[f[0]][1] + model.Vertices[f[1]][1] + model.Vertices[f[2]][1]) / 3
		cz := (model.Vertices[f[0]][2] + model.Vertices[f[1]][2] + model.Vertices[f[2]][2]) / 3
		radial := float32(math.Sqrt(float64(cx*cx + cy*cy)))
		// Outer wall has radial≈rOuter and any z. Other surfaces have
		// either smaller radial (inner) or are at z≈±h/2 with smaller radial
		// (top/bottom annulus).
		isOuter := math.Abs(float64(radial-rOuter)) < 1.0
		_ = cz
		if !isOuter {
			t.Errorf("unfold leaked off outer wall: tri %d centroid (r=%.2f, z=%.2f)",
				ti, radial, cz)
		}
	}

	// Coverage parity: unfold and projection should cover roughly the same
	// area on the outer wall.
	un := len(unfoldDecal.TriUVs)
	pn := len(projDecal.TriUVs)
	if un > 3*pn || pn > 3*un {
		t.Errorf("decal coverage mismatch: unfold=%d, projection=%d", un, pn)
	}
}
