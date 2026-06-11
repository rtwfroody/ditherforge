package cellslicer

import (
	"context"

	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// wallMesh builds a closed faceted tube of `nseg` sides between z=zLo and
// z=zHi, with each wall quad split into two triangles spanning the FULL
// height (one triangle row). This reproduces the real failure input: a
// coarse vertical wall meshed with tall triangles, which SlabSurfaceFoot-
// prints slices into many bands. radialFn(z) gives the radius at height z
// so callers can make a straight wall (constant) or a cone (z-dependent).
func wallMesh(nseg int, zLo, zHi float32, radialFn func(z float32) float32) *loader.LoadedModel {
	m := &loader.LoadedModel{}
	ring := func(z float32) []uint32 {
		r := radialFn(z)
		idx := make([]uint32, nseg)
		for i := 0; i < nseg; i++ {
			a := 2 * math.Pi * float64(i) / float64(nseg)
			idx[i] = uint32(len(m.Vertices))
			m.Vertices = append(m.Vertices, [3]float32{
				r * float32(math.Cos(a)), r * float32(math.Sin(a)), z,
			})
		}
		return idx
	}
	lo := ring(zLo)
	hi := ring(zHi)
	for i := 0; i < nseg; i++ {
		j := (i + 1) % nseg
		// Two tall triangles per side, spanning the whole zLo..zHi height.
		m.Faces = append(m.Faces,
			[3]uint32{lo[i], lo[j], hi[j]},
			[3]uint32{lo[i], hi[j], hi[i]},
		)
	}
	return m
}

// boxWallMesh builds the four vertical side walls of an axis-aligned box
// of half-extent `half`, between z=zLo and z=zHi, each face one tall quad
// (two triangles). Each face lies in a plane of constant x or y, so its
// band-slice XY projection is *exactly* collinear (signedArea == 0, not
// merely tiny) — the case that exposes whether drop accounting counts
// exactly-zero-area slivers.
func boxWallMesh(half, zLo, zHi float32) *loader.LoadedModel {
	m := &loader.LoadedModel{}
	corners := [4][2]float32{{-half, -half}, {half, -half}, {half, half}, {-half, half}}
	v := func(x, y, z float32) uint32 {
		m.Vertices = append(m.Vertices, [3]float32{x, y, z})
		return uint32(len(m.Vertices) - 1)
	}
	for i := 0; i < 4; i++ {
		p, q := corners[i], corners[(i+1)%4]
		bl, br := v(p[0], p[1], zLo), v(q[0], q[1], zLo)
		tr, tl := v(q[0], q[1], zHi), v(p[0], p[1], zHi)
		m.Faces = append(m.Faces, [3]uint32{bl, br, tr}, [3]uint32{bl, tr, tl})
	}
	return m
}

// TestSurfaceFootprintBoxObservable guards the observability for axis-
// aligned walls. Their slivers have area EXACTLY 0, so a `> 0` drop gate
// would silently exclude them and the run would log nothing — defeating
// the whole "never silent" goal. We require the drop to be both effective
// (no surface loops leak) AND counted (Dropped > 0).
func TestSurfaceFootprintBoxObservable(t *testing.T) {
	model := boxWallMesh(5, 0, 10)
	planes := SlabBoundaryPlanes(0, 10, 0.2)

	fps, drop := SlabSurfaceFootprints(context.Background(), model, planes)

	loops := 0
	for _, fp := range fps {
		if fp != nil {
			loops += len(fp.Loops)
		}
	}
	if loops != 0 {
		t.Errorf("axis-aligned box wall produced %d surface loops; want 0", loops)
	}
	if drop.Dropped == 0 {
		t.Errorf("box wall discarded slivers were not counted (Dropped=0); " +
			"exactly-zero-area drops are slipping past the accounting, so the run logs nothing")
	}
}

// TestSurfaceFootprintVerticalWallNoSlivers locks the fix: a straight
// (vertical) wall must not inject any surface-projection loops. Its XY
// silhouette between planes equals its silhouette at the planes, so every
// band slice is a degenerate sliver that triBandXYPath must discard. A
// regression that lets them through reintroduces the uneven-ring-cell bug.
func TestSurfaceFootprintVerticalWallNoSlivers(t *testing.T) {
	const nseg = 64
	model := wallMesh(nseg, 0, 10, func(float32) float32 { return 10 })
	planes := SlabBoundaryPlanes(0, 10, 0.2)

	fps, drop := SlabSurfaceFootprints(context.Background(), model, planes)

	loops := 0
	for _, fp := range fps {
		if fp != nil {
			loops += len(fp.Loops)
		}
	}
	if loops != 0 {
		t.Errorf("vertical wall produced %d surface-projection loops; want 0 "+
			"(degenerate slivers leaked through the triBandXYPath filter)", loops)
	}
	if drop.Dropped == 0 {
		t.Errorf("expected the filter to discard the vertical wall's slivers, but Dropped=0")
	}
	// The whole point: a vertical wall's discarded slices are collinear, so
	// the largest single discard must stay far below a dither pixel. If this
	// ever climbs to pixel scale the filter is eating real extent.
	px := float32(0.13*0.13) / 16 // ~(cellSize/4)^2 at a 0.13mm-ish feature
	if drop.AreaMax > px {
		t.Errorf("largest discarded vertical-wall sliver = %.6g mm², exceeds pixel %.6g mm²; "+
			"filter may be discarding real coverage", drop.AreaMax, px)
	}
}

// TestSurfaceFootprintConeKeepsCoverage is the other guard: a sloped
// (conical) wall genuinely moves radially with Z, so each band slice has
// real XY area and MUST survive the filter — otherwise the fix would punch
// holes in tapered surfaces. We assert the surface projection covers a band
// the bounding-plane contours alone would miss.
func TestSurfaceFootprintConeKeepsCoverage(t *testing.T) {
	const nseg = 64
	// Radius grows from 4mm at the base to 10mm at the top: a 31°-from-
	// vertical cone, well within the "real bulge" regime.
	cone := func(z float32) float32 { return 4 + 0.6*z }
	model := wallMesh(nseg, 0, 10, cone)
	planes := SlabBoundaryPlanes(0, 10, 0.2)

	fps, drop := SlabSurfaceFootprints(context.Background(), model, planes)

	// Pick a mid slab; its surface projection must be a real annulus, not
	// empty, and must reach out toward the top-of-band radius the lower
	// plane contour does not cover.
	mid := len(fps) / 2
	if fps[mid] == nil || len(fps[mid].Loops) == 0 {
		t.Fatalf("cone slab %d has no surface footprint; the filter discarded real tapered coverage", mid)
	}
	var area float32
	for i := range fps[mid].Loops {
		area += absf32(signedArea(fps[mid].Loops[i].Points))
	}
	if area < 1.0 {
		t.Errorf("cone slab %d surface area = %.4g mm², implausibly small — real coverage was filtered out", mid, area)
	}
	// A cone's slices are not collinear, so most projections should be KEPT,
	// not dropped. Guard against the filter over-rejecting tapered walls.
	if drop.Dropped > drop.Considered/2 {
		t.Errorf("cone dropped %d/%d projections; filter is over-rejecting real tapered coverage",
			drop.Dropped, drop.Considered)
	}
}
