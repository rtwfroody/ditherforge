package cellslicer

// DITHERFORGE_CLIP_COVER_PROBE: per-group diagnostic that disambiguates the
// two remaining white-hole mechanisms in the merged-cell clip:
//
//	(a) contour under-reach — the surface that's dropped lies OUTSIDE the
//	    group's prism contour (a real coverage gap), or
//	(b) CSG coincidence crack — the surface IS inside the group's contour but
//	    the Manifold intersection drops it anyway (numerical, fixed by an
//	    epsilon offset).
//
// For each group it rasterizes, into a fine XY grid over the contour bbox:
//   - expected: up-facing source top-cap surface present at the pixel,
//   - inContour: pixel inside the prism contour (even-odd over all loops),
//   - covered: the clip OUTPUT covers the pixel.
//
// The decisive number is over pixels that are expected AND inContour: how
// many are NOT covered by the output. That is surface the contour DOES
// enclose but the CSG dropped → mechanism (b). If it is ~0, the holes are
// not inside contours and the cause is (a)/seams instead.

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"sync"

	"github.com/rtwfroody/ditherforge/internal/manifoldbool"
)

var clipCoverProbe = os.Getenv("DITHERFORGE_CLIP_COVER_PROBE") != ""

// up-facing top-cap classification and grid resolution.
const (
	clipCoverUpNz   = 0.0  // face is up-facing (top surface) when unit nz > this
	clipCoverStep   = 0.01 // mm grid pitch (≈ 0.6× dither pixel)
	clipCoverMaxDim = 1200 // cap grid dimension; coarsen step beyond this
)

type clipCoverAcc struct {
	mu sync.Mutex

	groups         int
	groupsWithCap  int
	expectedPx     int     // up-facing cap px inside contour
	coveredPx      int     // ...covered by output
	droppedPx      int     // ...NOT covered by output → (b)
	droppedAreaMM2 float64 // Σ droppedPx · step²
	maxGroupDrop   int     // worst single group dropped px
	maxGroupSlabZ  float32 // its zBot, for locating it
}

var clipCover clipCoverAcc

// per-slab cached up-facing source cap faces, keyed by the slab Manifold
// pointer (one entry per slab, shared by all its groups).
var clipCoverSrcCache sync.Map // *manifoldbool.Manifold -> *clipCoverFaces

type clipCoverFaces struct {
	once sync.Once
	tris [][3][3]float32 // up-facing source triangles (world XY/Z)
}

func clipCoverCapTris(src *manifoldbool.Manifold, srcID int32) [][3][3]float32 {
	v, ok := clipCoverSrcCache.LoadOrStore(src, &clipCoverFaces{})
	cf := v.(*clipCoverFaces)
	cf.once.Do(func() {
		sv, sf := src.ToMeshFiltered(srcID)
		cf.tris = upFacingTris(sv, sf)
		_ = ok
	})
	return cf.tris
}

// upFacingTris returns the triangles whose unit normal points up (nz >
// clipCoverUpNz) — i.e. the top-cap surface visible from directly above.
func upFacingTris(v [][3]float32, f [][3]uint32) [][3][3]float32 {
	out := make([][3][3]float32, 0, len(f))
	for _, t := range f {
		a, b, c := v[t[0]], v[t[1]], v[t[2]]
		// n = (b-a) × (c-a); we only need nz and |n|.
		e1x, e1y := b[0]-a[0], b[1]-a[1]
		e2x, e2y := c[0]-a[0], c[1]-a[1]
		nz := e1x*e2y - e1y*e2x // z component of cross
		e1z := b[2] - a[2]
		e2z := c[2] - a[2]
		nx := e1y*e2z - e1z*e2y
		ny := e1z*e2x - e1x*e2z
		l := float32(math.Sqrt(float64(nx*nx + ny*ny + nz*nz)))
		if l == 0 {
			continue
		}
		if nz/l > clipCoverUpNz {
			out = append(out, [3][3]float32{a, b, c})
		}
	}
	return out
}

// probeGroupCoverage rasterizes the group's expected cap / contour / output
// and accumulates the in-contour-but-dropped tally. contours are the prism
// XY loops; capTris the slab's up-facing source triangles; outV/outF the
// group's clip output surface.
func probeGroupCoverage(contours [][][2]float32, capTris [][3][3]float32, outV [][3]float32, outF [][3]uint32, zBot float32) {
	// Contour bbox.
	minx, miny := float32(math.Inf(1)), float32(math.Inf(1))
	maxx, maxy := float32(math.Inf(-1)), float32(math.Inf(-1))
	for _, loop := range contours {
		for _, p := range loop {
			minx, maxx = minf32(minx, p[0]), maxf32(maxx, p[0])
			miny, maxy = minf32(miny, p[1]), maxf32(maxy, p[1])
		}
	}
	if !(maxx > minx && maxy > miny) {
		return
	}
	step := float32(clipCoverStep)
	if d := maxf32(maxx-minx, maxy-miny); d/step > clipCoverMaxDim {
		step = d / clipCoverMaxDim
	}
	nx := int((maxx-minx)/step) + 1
	ny := int((maxy-miny)/step) + 1
	if nx < 1 || ny < 1 {
		return
	}
	n := nx * ny
	inC := make([]bool, n)
	expect := make([]bool, n)
	covered := make([]bool, n)

	idx := func(ix, iy int) int { return iy*nx + ix }
	cx := func(ix int) float32 { return minx + (float32(ix)+0.5)*step }
	cy := func(iy int) float32 { return miny + (float32(iy)+0.5)*step }

	// inContour: even-odd point-in-polygon across all loops (holes excluded).
	for iy := 0; iy < ny; iy++ {
		py := cy(iy)
		for ix := 0; ix < nx; ix++ {
			if pointInLoops(contours, cx(ix), py) {
				inC[idx(ix, iy)] = true
			}
		}
	}
	rasterTris(capTris, minx, miny, step, nx, ny, expect)
	// "covered" must mean an UP-FACING output face: a white hole is where the
	// up-facing exterior is dropped but the down-facing interior survives at
	// the same XY, so plain XY coverage would falsely read as covered.
	rasterTris(upFacingTris(outV, outF), minx, miny, step, nx, ny, covered)

	ePx, cPx, dPx := 0, 0, 0
	for i := 0; i < n; i++ {
		if expect[i] && inC[i] {
			ePx++
			if covered[i] {
				cPx++
			} else {
				dPx++
			}
		}
	}

	clipCover.mu.Lock()
	clipCover.groups++
	if ePx > 0 {
		clipCover.groupsWithCap++
	}
	clipCover.expectedPx += ePx
	clipCover.coveredPx += cPx
	clipCover.droppedPx += dPx
	clipCover.droppedAreaMM2 += float64(dPx) * float64(step) * float64(step)
	if dPx > clipCover.maxGroupDrop {
		clipCover.maxGroupDrop = dPx
		clipCover.maxGroupSlabZ = zBot
	}
	clipCover.mu.Unlock()
}

// rasterTris marks grid pixels whose center lies in any of the (world-XY)
// triangles.
func rasterTris(tris [][3][3]float32, minx, miny, step float32, nx, ny int, mark []bool) {
	for _, t := range tris {
		ax, ay := t[0][0], t[0][1]
		bx, by := t[1][0], t[1][1]
		cx, cy := t[2][0], t[2][1]
		rasterOneTri(ax, ay, bx, by, cx, cy, minx, miny, step, nx, ny, mark)
	}
}

func rasterOutTris(v [][3]float32, f [][3]uint32, minx, miny, step float32, nx, ny int, mark []bool) {
	for _, t := range f {
		a, b, c := v[t[0]], v[t[1]], v[t[2]]
		rasterOneTri(a[0], a[1], b[0], b[1], c[0], c[1], minx, miny, step, nx, ny, mark)
	}
}

func rasterOneTri(ax, ay, bx, by, cx, cy, minx, miny, step float32, nx, ny int, mark []bool) {
	lox := minf32(ax, minf32(bx, cx))
	hix := maxf32(ax, maxf32(bx, cx))
	loy := minf32(ay, minf32(by, cy))
	hiy := maxf32(ay, maxf32(by, cy))
	ix0 := int((lox - minx) / step)
	ix1 := int((hix-minx)/step) + 1
	iy0 := int((loy - miny) / step)
	iy1 := int((hiy-miny)/step) + 1
	if ix0 < 0 {
		ix0 = 0
	}
	if iy0 < 0 {
		iy0 = 0
	}
	if ix1 > nx {
		ix1 = nx
	}
	if iy1 > ny {
		iy1 = ny
	}
	// Barycentric sign test at pixel centers.
	d := (by-cy)*(ax-cx) + (cx-bx)*(ay-cy)
	if d == 0 {
		return
	}
	for iy := iy0; iy < iy1; iy++ {
		py := miny + (float32(iy)+0.5)*step
		for ix := ix0; ix < ix1; ix++ {
			px := minx + (float32(ix)+0.5)*step
			l1 := ((by-cy)*(px-cx) + (cx-bx)*(py-cy)) / d
			l2 := ((cy-ay)*(px-cx) + (ax-cx)*(py-cy)) / d
			l3 := 1 - l1 - l2
			if l1 >= -1e-5 && l2 >= -1e-5 && l3 >= -1e-5 {
				mark[iy*nx+ix] = true
			}
		}
	}
}

// pointInLoops: even-odd rule across all contour loops (outer + holes), so
// holes are correctly excluded regardless of loop winding.
func pointInLoops(loops [][][2]float32, x, y float32) bool {
	in := false
	for _, loop := range loops {
		m := len(loop)
		for k := 0; k < m; k++ {
			a, b := loop[k], loop[(k+1)%m]
			if (a[1] > y) != (b[1] > y) {
				t := (y - a[1]) / (b[1] - a[1])
				if x < a[0]+t*(b[0]-a[0]) {
					in = !in
				}
			}
		}
	}
	return in
}

// --- seam vs open-edge bloat discriminator ---
//
// DITHERFORGE_SEAM_BLOAT=X grows the merged-group contour outward by X mm on
// its NON-open edges (the seams it shares with adjacent groups);
// DITHERFORGE_OPEN_BLOAT=X overrides the outward bloat on its open
// (footprint-boundary) edges. Running each alone localizes the white-hole
// drops: if seam-bloat closes the interior diagonal-line holes and open-bloat
// does not, the drops are hairline CSG cracks at the seams BETWEEN adjacent
// group prisms (neighbors don't overlap), not the outer footprint boundary.
// EXPERIMENTAL FIX BACKED OUT (session 6): defaulting seamBloatMM to overlap
// adjacent merged-group prisms closed ~19% of the holes (1.269%→1.023% at
// 0.1mm) but exploded non-manifold edges 14× (1358→18807) and boundary edges
// +65% — it trades a cosmetic hole for an unprintable non-manifold mesh,
// because overlapping prisms duplicate the shared surface. Kept env-only for
// diagnosis. A clean fix must close seams WITHOUT overlap (e.g. one clip op
// per slab over the whole footprint, then assign face colors), since the gap
// has real geometric width and no overlap-free bloat can cover it.
var seamBloatMM = envF32("DITHERFORGE_SEAM_BLOAT")
var openBloatMM = envF32("DITHERFORGE_OPEN_BLOAT")

func envF32(k string) float32 {
	s := os.Getenv(k)
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 32)
	if err != nil {
		return 0
	}
	return float32(v)
}

// bloatEdgesExperimental offsets the contour with a per-edge-class distance
// (open edges by openBloatMM-or-default, seam edges by seamBloatMM). Returns
// used=false when neither knob is set, so the caller keeps the default path.
func bloatEdgesExperimental(outer []Point2, openFlags []bool) ([][2]float32, bool) {
	if seamBloatMM <= 0 && openBloatMM <= 0 {
		return nil, false
	}
	n := len(outer)
	if n < 3 {
		out := make([][2]float32, n)
		for i := range outer {
			out[i] = [2]float32{outer[i][0], outer[i][1]}
		}
		return out, true
	}
	ob := float32(OpenEdgeBloatMM)
	if openBloatMM > 0 {
		ob = openBloatMM
	}
	d := make([]float32, n)
	nm := make([][2]float32, n)
	for i := 0; i < n; i++ {
		open := openFlags != nil && i < len(openFlags) && openFlags[i]
		if open {
			d[i] = ob
		} else {
			d[i] = seamBloatMM
		}
		j := (i + 1) % n
		dx := outer[j][0] - outer[i][0]
		dy := outer[j][1] - outer[i][1]
		l := float32(math.Sqrt(float64(dx*dx + dy*dy)))
		if l == 0 {
			continue
		}
		nm[i] = [2]float32{dy / l, -dx / l} // CCW outward
	}
	out := make([][2]float32, n)
	for i := 0; i < n; i++ {
		pe := (i + n - 1) % n
		ne := i
		dot := nm[pe][0]*nm[ne][0] + nm[pe][1]*nm[ne][1]
		denom := 1 + dot
		mx := nm[pe][0]*d[pe] + nm[ne][0]*d[ne]
		my := nm[pe][1]*d[pe] + nm[ne][1]*d[ne]
		if denom > 1e-6 {
			mx /= denom
			my /= denom
		}
		out[i] = [2]float32{outer[i][0] + mx, outer[i][1] + my}
	}
	return out, true
}

func reportClipCoverProbe() {
	if !clipCoverProbe {
		return
	}
	clipCover.mu.Lock()
	defer clipCover.mu.Unlock()
	exp := clipCover.expectedPx
	frac := 0.0
	if exp > 0 {
		frac = 100 * float64(clipCover.droppedPx) / float64(exp)
	}
	fmt.Printf("  [clip-cover-probe] %d groups (%d with cap); "+
		"top-cap px inside own contour: expected=%d covered=%d DROPPED=%d (%.3f%% in-contour drop, %.4f mm²); "+
		"worst group dropped=%d px @ zBot=%.2f\n",
		clipCover.groups, clipCover.groupsWithCap,
		exp, clipCover.coveredPx, clipCover.droppedPx, frac, clipCover.droppedAreaMM2,
		clipCover.maxGroupDrop, clipCover.maxGroupSlabZ)
	fmt.Printf("  [clip-cover-probe] INTERPRETATION: high in-contour drop%% → (b) CSG coincidence crack "+
		"(contour covers surface, intersection drops it); near-zero → (a) contour/seam under-reach\n")
}
