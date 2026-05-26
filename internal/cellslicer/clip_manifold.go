package cellslicer

import (
	"fmt"
	"math"
	"runtime"
	"sync"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/manifoldbool"
)

// OpenEdgeBloat is the outward distance (mm) applied to each open
// cell-Outer edge when building the per-cell prism for Manifold
// clipping. Cells at the slab partition's outer boundary have one or
// more edges flagged with Cell.OuterEdgeOpen[i] = true; we model the
// "infinity" semantics of those edges by pushing them outward by
// OpenEdgeBloat × cellSize so the resulting closed prism reaches well
// past any model geometry on the open side.
//
// The relevant scale here is the alphawrap step size (typically much
// smaller than cellSize) — that's how far model geometry can nudge
// outside the slab footprint. 5×cellSize is comfortably above that
// while keeping the detour at a sensible scale in the debug SVG view
// (large enough to be visually distinct from closed edges, small
// enough that the bloated cell isn't dwarfing the rest of the layer).
// bloatOpenEdges further caps the per-cell displacement at the cell
// bbox's max side to prevent self-intersection on thin cells; see
// the comment there for details.
//
// devscripts/manifold_clip/open_edge.py demonstrates the convergence
// for the global bloat-vs-bbox case.
const OpenEdgeBloat = 5.0

// ClipMeshToCellsManifold is the Manifold-backed replacement for the
// bespoke ClipMeshToCells2D pipeline. For each cell it:
//
//  1. Builds the cell's 2D polygon, replacing any open-edge run with
//     a 5×cellSize outward bloat (see OpenEdgeBloat).
//  2. Extrudes that polygon between [zBot, zTop] into a closed prism.
//  3. Intersects the source Manifold with the prism via
//     manifold_intersection. The result is watertight by construction.
//  4. Reads the resulting triangles back and tags each face with the
//     cell's global index.
//
// model must describe a closed orientable surface; this function will
// position-dedup it at 1µm before constructing the source Manifold, so
// UV-seam duplicates (typical of textured GLB exports) collapse to one
// vertex each. A genuinely non-watertight input (e.g. raw
// low_poly_building.glb pre-alphawrap) will surface as a clear error
// at FromMesh.
//
// triIdx is accepted for signature parity with ClipMeshToCells2D but
// currently unused — Manifold builds a single source object once and
// the per-cell prism's BVH path is internal. We may add candidate
// pruning back later for very large meshes.
//
// cellSize is the dither cell size in mm, used to scale OpenEdgeBloat.
// Pass the same value supplied to PartitionModel.
func ClipMeshToCellsManifold(model *loader.LoadedModel, slabs []Slab, triIdx *TriXYZIndex, cellSize float32) (ClipResult, error) {
	_ = triIdx

	verts, faces := DedupVertsByPosition(model.Vertices, model.Faces)
	if len(verts) == 0 || len(faces) == 0 {
		return ClipResult{}, fmt.Errorf("cellslicer/manifold: source mesh has no faces")
	}
	src, err := manifoldbool.FromMesh(verts, faces)
	if err != nil {
		return ClipResult{}, fmt.Errorf("cellslicer/manifold: build source Manifold: %w", err)
	}
	defer src.Close()
	srcID := src.OriginalID()

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
	globalOffsets := make([]int, len(slabs)+1)
	for si := range slabs {
		globalOffsets[si+1] = globalOffsets[si] + len(slabs[si].Cells)
	}
	jobs := make([]job, 0, globalOffsets[len(slabs)])
	for si := range slabs {
		for ci := range slabs[si].Cells {
			jobs = append(jobs, job{globalIdx: globalOffsets[si] + ci, slabIdx: si, cellIdx: ci})
		}
	}
	results := make([]result, len(jobs))

	// Manifold itself is thread-safe across independent Manifold
	// objects; per-cell work touches src read-only and writes into
	// its own slot in results. The libmanifoldc allocator is
	// process-wide, but boolean ops do not share mutable state
	// between separate calls.
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
	var (
		wg      sync.WaitGroup
		errMu   sync.Mutex
		firstE  error
	)
	for w := 0; w < nWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ji := range jobCh {
				j := jobs[ji]
				s := &slabs[j.slabIdx]
				cell := &s.Cells[j.cellIdx]
				v, f, cerr := clipOneCellManifold(src, srcID, cell, s.ZBot, s.ZTop, cellSize)
				if cerr != nil {
					errMu.Lock()
					if firstE == nil {
						firstE = fmt.Errorf("cell %d (slab=%d,cell=%d): %w", j.globalIdx, j.slabIdx, j.cellIdx, cerr)
					}
					errMu.Unlock()
					continue
				}
				if len(f) == 0 {
					continue
				}
				results[ji] = result{globalIdx: j.globalIdx, verts: v, faces: f}
			}
		}()
	}
	wg.Wait()
	if firstE != nil {
		return ClipResult{}, firstE
	}

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

// clipOneCellManifold builds the cell prism, intersects it with src,
// and returns the surface-only mesh — only the faces inherited from
// src survive, so the cell prism's own walls (which the boolean
// intersection adds where the prism cuts through the model volume)
// are stripped before return. Empty results (the cell doesn't
// overlap the model) are returned as (nil, nil, nil).
func clipOneCellManifold(src *manifoldbool.Manifold, srcID int32, cell *Cell, zBot, zTop, cellSize float32) ([][3]float32, [][3]uint32, error) {
	poly := bloatOpenEdges(cell.Outer, cell.OuterEdgeOpen, OpenEdgeBloat*cellSize)
	if len(poly) < 3 {
		return nil, nil, nil
	}
	prism, err := manifoldbool.ExtrudePolygon(poly, zBot, zTop)
	if err != nil {
		return nil, nil, fmt.Errorf("extrude cell polygon: %w", err)
	}
	defer prism.Close()
	out, err := manifoldbool.Intersection(src, prism)
	if err != nil {
		return nil, nil, fmt.Errorf("intersection: %w", err)
	}
	defer out.Close()
	if out.IsEmpty() {
		return nil, nil, nil
	}
	v, f := out.ToMeshFiltered(srcID)
	return v, f, nil
}

// bloatOpenEdges returns a fresh polygon copy where every contiguous
// run of open edges has been pushed outward. Vertices whose incident
// edges are all closed remain unchanged.
//
// The polygon's CCW orientation determines "outward" (right of each
// edge direction). For each open edge i (from outer[i] to outer[i+1]),
// the outward unit normal is (dy, -dx) / |d|.
//
// Construction:
//
//   - A vertex with two closed incident edges: keep original.
//   - A vertex with two open incident edges (interior of an open run):
//     emit the miter offset where the two offset lines meet, capped at
//     miterLimit to handle near-anti-parallel normals gracefully.
//   - A vertex on a closed→open or open→closed transition: emit TWO
//     vertices, the original and the bloated, so the polygon walks
//     perpendicularly out to the partition boundary.
//
// The displacement is capped at the cell's bbox max-side so a 1mm-wide
// ring cell with a 5×cellSize=5mm raw bloat doesn't push its open edge
// past its opposite closed edge (which would produce a self-
// intersecting polygon and either a Manifold rejection or a degenerate
// prism).
//
// openFlags may be nil — in that case every edge is treated as closed
// and the result is just a defensive copy of outer.
//
// Always returns a freshly-allocated slice (never aliases outer), so
// callers can hand the result to other code without surprises.
func bloatOpenEdges(outer []Point2, openFlags []bool, bloat float32) [][2]float32 {
	n := len(outer)
	if n < 3 {
		return copyOuter(outer)
	}
	if bloat <= 0 || len(openFlags) == 0 || len(openFlags) != n {
		// len(openFlags) != n is defensive: if a caller wired the
		// flags wrong, the safer behaviour is a no-bloat copy than a
		// malformed prism.
		return copyOuter(outer)
	}
	anyOpen := false
	for _, b := range openFlags {
		if b {
			anyOpen = true
			break
		}
	}
	if !anyOpen {
		return copyOuter(outer)
	}

	// Per-cell bloat cap. Pin the displacement to no more than the
	// cell's bbox max-side so the open edge never lands past a
	// non-incident edge — a guarantee even for future non-convex
	// cells or pathological partition geometries. The downside is
	// that small (< raw-bloat-sized) cells get a smaller bloat than
	// the configured 5×cellSize; that's fine because the geometry
	// we're trying to catch only nudges past the partition boundary
	// by sub-cellSize amounts (alphawrap step size).
	minX, minY, maxX, maxY := polyBounds(outer)
	maxSide := maxX - minX
	if dy := maxY - minY; dy > maxSide {
		maxSide = dy
	}
	if maxSide <= 0 {
		return copyOuter(outer)
	}
	if bloat > maxSide {
		bloat = maxSide
	}

	// Precompute outward unit normals for every edge. For closed
	// edges we keep them zero so the per-vertex consumer naturally
	// ignores closed-edge contributions.
	normals := make([][2]float32, n)
	for i := 0; i < n; i++ {
		if !openFlags[i] {
			continue
		}
		j := (i + 1) % n
		dx := outer[j][0] - outer[i][0]
		dy := outer[j][1] - outer[i][1]
		length := float32(math.Sqrt(float64(dx*dx + dy*dy)))
		if length == 0 {
			continue
		}
		// CCW outward: rotate edge direction -90° → (dy, -dx).
		normals[i] = [2]float32{dy / length, -dx / length}
	}

	// miterLimit: cap the offset length at this multiple of bloat
	// before falling back to a bevel. 4 is the OpenGL/SVG default,
	// well above any realistic cellslicer corner (60° hex corner has
	// miter factor ≈ 1.15; a 90° corner has √2 ≈ 1.41).
	const miterLimit float32 = 4

	out := make([][2]float32, 0, n+4)
	for i := 0; i < n; i++ {
		prevEdge := (i + n - 1) % n
		nextEdge := i
		prevOpen := openFlags[prevEdge]
		nextOpen := openFlags[nextEdge]
		p := outer[i]
		switch {
		case !prevOpen && !nextOpen:
			out = append(out, [2]float32{p[0], p[1]})
		case prevOpen && nextOpen:
			// Interior of an open run — miter offset. Closed-form
			// for unit normals: m = (n1+n2) / (1 + n1·n2), scaled by
			// bloat. When edges meet at angle θ, |m| = 1/cos(θ/2).
			n1, n2 := normals[prevEdge], normals[nextEdge]
			sumX := n1[0] + n2[0]
			sumY := n1[1] + n2[1]
			denom := 1 + (n1[0]*n2[0] + n1[1]*n2[1])
			if denom <= 1e-6 {
				// Nearly anti-parallel — miter would explode.
				// Bevel: emit two displaced vertices, one per edge.
				out = append(out, [2]float32{p[0] + n1[0]*bloat, p[1] + n1[1]*bloat})
				out = append(out, [2]float32{p[0] + n2[0]*bloat, p[1] + n2[1]*bloat})
				continue
			}
			mx := sumX / denom * bloat
			my := sumY / denom * bloat
			if mLen := float32(math.Hypot(float64(mx), float64(my))); mLen > miterLimit*bloat {
				scale := miterLimit * bloat / mLen
				mx *= scale
				my *= scale
			}
			out = append(out, [2]float32{p[0] + mx, p[1] + my})
		case !prevOpen && nextOpen:
			// closed → open transition: emit original first, then
			// the bloated vertex on the open side.
			out = append(out, [2]float32{p[0], p[1]})
			out = append(out, [2]float32{p[0] + normals[nextEdge][0]*bloat, p[1] + normals[nextEdge][1]*bloat})
		default: // prevOpen && !nextOpen
			// open → closed transition: emit bloated first (close out
			// the open detour), then return to the original.
			out = append(out, [2]float32{p[0] + normals[prevEdge][0]*bloat, p[1] + normals[prevEdge][1]*bloat})
			out = append(out, [2]float32{p[0], p[1]})
		}
	}
	return out
}

// copyOuter returns a fresh [][2]float32 copy of a Point2 polygon.
// Used by bloatOpenEdges's no-op early returns so they never alias
// cell.Outer.
func copyOuter(outer []Point2) [][2]float32 {
	out := make([][2]float32, len(outer))
	for i, p := range outer {
		out[i] = [2]float32{p[0], p[1]}
	}
	return out
}
