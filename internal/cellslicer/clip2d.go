// 2D per-slab Clipper-based clip: bypasses CGAL by exploiting the fact
// that each slab bounds Z so 3D Boolean degenerates to a 2D polygon
// intersection between each model triangle (projected onto XY) and
// each cell's XY polygon, lifted back to 3D via the triangle's plane.
//
// Replaces the per-cell CGAL clip_surface path in clip.go for
// production scale: a 1.2M-cell pipeline runs in seconds instead of
// hours, with no CGAL setup amortization or thread-safety concerns.

package cellslicer

import (
	"math"
	"runtime"
	"sync"

	clipper "github.com/ctessum/go.clipper"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/minislicer"
)

// slabTri is a triangle that lies entirely within a slab's Z range.
// PlaneA*x + PlaneB*y + PlaneC*z + PlaneD = 0 describes its supporting
// plane; PlaneC == 0 marks a perfectly vertical triangle (zero XY
// area — these are dropped during sliceToSlab since they can't
// contribute to a cap-style fragment).
type slabTri struct {
	V0, V1, V2 [3]float32
	PlaneA     float32
	PlaneB     float32
	PlaneC     float32
	PlaneD     float32
}

// ClipMeshToCells2D produces a per-cell-tagged mesh fragment in the
// same shape as ClipMeshToCells, but without CGAL: for each slab,
// each model triangle is clipped to the slab's Z range and then
// each cell's XY polygon clips the triangle's XY projection, with
// resulting 2D polygons lifted back to 3D via the triangle's plane
// equation.
//
// Per-cell work is run in parallel across runtime.NumCPU() worker
// goroutines (safe — pure Go, no CGAL).
func ClipMeshToCells2D(model *loader.LoadedModel, slabs []Slab, triIdx *TriXYZIndex) (ClipResult, error) {
	// Pre-flatten cell jobs with global indices.
	type job struct {
		globalIdx int
		slabIdx   int
		cellIdx   int
	}
	type result struct {
		globalIdx int
		verts     [][3]float32
		faces     [][3]uint32
	}
	var jobs []job
	offsets := make([]int, len(slabs)+1)
	for si := range slabs {
		offsets[si+1] = offsets[si] + len(slabs[si].Cells)
		for ci := range slabs[si].Cells {
			jobs = append(jobs, job{globalIdx: offsets[si] + ci, slabIdx: si, cellIdx: ci})
		}
	}
	results := make([]result, len(jobs))

	// Pre-slice every model triangle into per-slab pieces, keyed by
	// slab index. Each piece's Z range is bounded by the slab.
	// We iterate every model triangle once and assign its pieces
	// to whichever slab Z-overlaps it; for the building scale this
	// is cheaper than a per-slab XY query (no extra spatial filter
	// payoff since we want every Z-overlapping tri).
	slabTris := make([][]slabTri, len(slabs))
	for ti := range model.Faces {
		f := model.Faces[ti]
		a := model.Vertices[f[0]]
		b := model.Vertices[f[1]]
		c := model.Vertices[f[2]]
		zMin := minf3(a[2], b[2], c[2])
		zMax := maxf3(a[2], b[2], c[2])
		// Find the slab range this triangle's Z spans. Slabs are
		// uniform in Z, so binary-search bounds.
		siLo := 0
		siHi := len(slabs) - 1
		for siLo <= siHi && slabs[siLo].ZTop < zMin {
			siLo++
		}
		for siHi >= siLo && slabs[siHi].ZBot > zMax {
			siHi--
		}
		for si := siLo; si <= siHi; si++ {
			if si < 0 || si >= len(slabs) {
				continue
			}
			s := &slabs[si]
			pieces := sliceTriangleToSlab(a, b, c, s.ZBot, s.ZTop)
			slabTris[si] = append(slabTris[si], pieces...)
		}
	}
	_ = triIdx

	nWorkers := runtime.NumCPU()
	if nWorkers > len(jobs) {
		nWorkers = len(jobs)
	}
	if nWorkers < 1 {
		nWorkers = 1
	}
	jobCh := make(chan int, len(jobs))
	for i := range jobs {
		jobCh <- i
	}
	close(jobCh)
	var wg sync.WaitGroup
	for w := 0; w < nWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ji := range jobCh {
				j := jobs[ji]
				s := &slabs[j.slabIdx]
				cell := &s.Cells[j.cellIdx]
				tris := slabTris[j.slabIdx]
				if len(tris) == 0 {
					continue
				}
				verts, faces := clipCellTris(tris, cell.Outer)
				if len(faces) == 0 {
					continue
				}
				results[ji] = result{
					globalIdx: j.globalIdx,
					verts:     verts,
					faces:     faces,
				}
			}
		}()
	}
	wg.Wait()

	totalV, totalF := 0, 0
	for _, r := range results {
		totalV += len(r.verts)
		totalF += len(r.faces)
	}
	cr := ClipResult{
		Verts:       make([][3]float32, 0, totalV),
		Faces:       make([][3]uint32, 0, totalF),
		FaceCellIdx: make([]int32, 0, totalF),
	}
	for _, r := range results {
		if len(r.faces) == 0 {
			continue
		}
		base := uint32(len(cr.Verts))
		cr.Verts = append(cr.Verts, r.verts...)
		for _, f := range r.faces {
			cr.Faces = append(cr.Faces, [3]uint32{f[0] + base, f[1] + base, f[2] + base})
			cr.FaceCellIdx = append(cr.FaceCellIdx, int32(r.globalIdx))
		}
	}
	return cr, nil
}

// sliceTriangleToSlab clips triangle (a,b,c) against the half-spaces
// z >= zBot and z <= zTop and returns the resulting pieces as
// slabTris. The result is a triangle fan over the sub-polygon (1–3
// triangles). Returns empty for vertical triangles (PlaneC == 0)
// since their XY projection has zero area and they can't produce
// cap fragments.
func sliceTriangleToSlab(a, b, c [3]float32, zBot, zTop float32) []slabTri {
	// Drop fully outside.
	zMin := minf3(a[2], b[2], c[2])
	zMax := maxf3(a[2], b[2], c[2])
	if zMax < zBot || zMin > zTop {
		return nil
	}
	// Plane of (a, b, c): normal = (b-a) × (c-a); plane eqn n·p = n·a.
	nx := (b[1]-a[1])*(c[2]-a[2]) - (b[2]-a[2])*(c[1]-a[1])
	ny := (b[2]-a[2])*(c[0]-a[0]) - (b[0]-a[0])*(c[2]-a[2])
	nz := (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0])
	if absf(nz) < 1e-9 {
		return nil // vertical triangle, no XY footprint
	}
	d := -(nx*a[0] + ny*a[1] + nz*a[2])
	// Build the sub-polygon by clipping against z >= zBot then z <= zTop.
	poly := [][3]float32{a, b, c}
	poly = clipPolygonByZHalfSpace(poly, zBot, true /* keep z >= zBot */)
	if len(poly) < 3 {
		return nil
	}
	poly = clipPolygonByZHalfSpace(poly, zTop, false /* keep z <= zTop */)
	if len(poly) < 3 {
		return nil
	}
	// Fan-triangulate (the slabbed sub-polygon is convex — Z-plane
	// clipping of a triangle produces a convex polygon).
	tris := make([]slabTri, 0, len(poly)-2)
	for i := 1; i < len(poly)-1; i++ {
		tris = append(tris, slabTri{
			V0: poly[0], V1: poly[i], V2: poly[i+1],
			PlaneA: nx, PlaneB: ny, PlaneC: nz, PlaneD: d,
		})
	}
	return tris
}

// clipPolygonByZHalfSpace clips polygon by a Z half-space.
//
//	keepGreater = true  → keep z >= zPlane
//	keepGreater = false → keep z <= zPlane
//
// Standard Sutherland-Hodgman.
func clipPolygonByZHalfSpace(poly [][3]float32, zPlane float32, keepGreater bool) [][3]float32 {
	if len(poly) == 0 {
		return nil
	}
	out := make([][3]float32, 0, len(poly)+2)
	inside := func(p [3]float32) bool {
		if keepGreater {
			return p[2] >= zPlane
		}
		return p[2] <= zPlane
	}
	n := len(poly)
	for i := 0; i < n; i++ {
		s := poly[(i-1+n)%n]
		e := poly[i]
		sIn := inside(s)
		eIn := inside(e)
		if eIn {
			if !sIn {
				out = append(out, lerpAtZ(s, e, zPlane))
			}
			out = append(out, e)
		} else if sIn {
			out = append(out, lerpAtZ(s, e, zPlane))
		}
	}
	return out
}

// lerpAtZ returns the point on segment a→b at Z = z.
func lerpAtZ(a, b [3]float32, z float32) [3]float32 {
	if absf(b[2]-a[2]) < 1e-12 {
		return a
	}
	t := (z - a[2]) / (b[2] - a[2])
	return [3]float32{
		a[0] + t*(b[0]-a[0]),
		a[1] + t*(b[1]-a[1]),
		z,
	}
}

// clipCellTris clips every triangle's XY projection against the
// cell polygon and emits 3D triangles whose Z is determined by each
// source triangle's plane equation. Caller is responsible for
// concatenating per-cell results into the global mesh.
func clipCellTris(tris []slabTri, cell []minislicer.Point2) ([][3]float32, [][3]uint32) {
	cellMinX, cellMinY, cellMaxX, cellMaxY := polyBoundsP2(cell)
	cellPath := pointsToClipperPath(cell)
	var verts [][3]float32
	var faces [][3]uint32
	for _, t := range tris {
		// Triangle XY bbox prefilter.
		tMinX := minf3(t.V0[0], t.V1[0], t.V2[0])
		tMaxX := maxf3(t.V0[0], t.V1[0], t.V2[0])
		tMinY := minf3(t.V0[1], t.V1[1], t.V2[1])
		tMaxY := maxf3(t.V0[1], t.V1[1], t.V2[1])
		if tMaxX < cellMinX || tMinX > cellMaxX || tMaxY < cellMinY || tMinY > cellMaxY {
			continue
		}
		// Clip triangle's XY against the cell polygon via Clipper.
		triPath := clipper.Path{
			&clipper.IntPoint{
				X: clipper.CInt(math.Round(float64(t.V0[0]) * clipperScale)),
				Y: clipper.CInt(math.Round(float64(t.V0[1]) * clipperScale)),
			},
			&clipper.IntPoint{
				X: clipper.CInt(math.Round(float64(t.V1[0]) * clipperScale)),
				Y: clipper.CInt(math.Round(float64(t.V1[1]) * clipperScale)),
			},
			&clipper.IntPoint{
				X: clipper.CInt(math.Round(float64(t.V2[0]) * clipperScale)),
				Y: clipper.CInt(math.Round(float64(t.V2[1]) * clipperScale)),
			},
		}
		c := clipper.NewClipper(clipper.IoNone)
		c.AddPaths(clipper.Paths{triPath}, clipper.PtSubject, true)
		c.AddPaths(clipper.Paths{cellPath}, clipper.PtClip, true)
		result, ok := c.Execute1(clipper.CtIntersection, clipper.PftNonZero, clipper.PftNonZero)
		if !ok {
			continue
		}
		for _, path := range result {
			pts := clipperPathToPoints(path)
			if len(pts) < 3 {
				continue
			}
			// Earcut the polygon piece (cell × triangle clip can
			// produce non-convex pieces near the boundary).
			earVerts, earTris := minislicer.Earcut(pts, nil)
			if len(earTris) == 0 || len(earVerts) != len(pts) {
				continue
			}
			base := uint32(len(verts))
			for _, p := range pts {
				// Lift to 3D via the source plane: z = -(A*x + B*y + D) / C.
				z := -(t.PlaneA*p[0] + t.PlaneB*p[1] + t.PlaneD) / t.PlaneC
				verts = append(verts, [3]float32{p[0], p[1], z})
			}
			for _, tri := range earTris {
				faces = append(faces, [3]uint32{base + tri[0], base + tri[1], base + tri[2]})
			}
		}
	}
	return verts, faces
}

func polyBoundsP2(pts []minislicer.Point2) (minX, minY, maxX, maxY float32) {
	minX, minY = pts[0][0], pts[0][1]
	maxX, maxY = pts[0][0], pts[0][1]
	for _, p := range pts[1:] {
		if p[0] < minX {
			minX = p[0]
		}
		if p[1] < minY {
			minY = p[1]
		}
		if p[0] > maxX {
			maxX = p[0]
		}
		if p[1] > maxY {
			maxY = p[1]
		}
	}
	return
}

func absf(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}
