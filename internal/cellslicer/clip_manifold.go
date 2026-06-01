package cellslicer

import (
	"fmt"
	"math"
	"runtime"
	"sort"
	"sync"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/manifoldbool"
)

// ClipResult is the aggregated output of ClipMeshToCellsManifold: a
// single concatenated triangle mesh with per-face cell provenance.
//
// FaceCellIdx[i] is the global cell index (matches the flat
// CellSamples order in SampleCells output) of the cell whose prism
// produced face i. Downstream pipeline code maps that to a dithered
// palette index.
//
// The mesh is the surface-only intersection of the source mesh with
// each cell prism (clip_volume=false equivalent): the prism's walls
// are filtered out via Manifold's run_original_id mechanism, leaving
// only inherited source-mesh triangles.
type ClipResult struct {
	Verts       [][3]float32
	Faces       [][3]uint32
	FaceCellIdx []int32
	// CellRep, when non-nil, has one entry per cell (global flattened
	// index, same space as FaceCellIdx) giving the representative cell
	// index its merge-group was tagged with in FaceCellIdx. It is set
	// only by the merged clip (ClipMeshToMergedCellsManifold): cells in
	// the same same-kind/same-color group share a representative, and
	// only that representative appears in FaceCellIdx. Downstream
	// per-cell coverage diagnostics use CellRep[gi] to look up whether
	// gi's group produced any faces. nil for the per-cell clip, where
	// every cell is its own representative.
	CellRep []int32
}

// OpenEdgeBloatMM is the outward distance (mm) applied to each open
// cell-Outer edge when building the per-cell prism for Manifold clipping.
// Cells at the slab partition's outer boundary have one or more edges
// flagged with Cell.OuterEdgeOpen[i] = true; we push those edges outward
// by this much so the prism wall sits just past the model surface there
// rather than exactly on it.
//
// This is a floating-point margin, NOT a coverage mechanism. The cell
// footprint is the XY projection of the slab surface, so the surface never
// extends past the footprint boundary by a meaningful amount — there is no
// distant geometry to "reach out and grab". The only failure mode left is
// surface lying exactly on the vertical prism wall, which the Manifold
// intersection can drop (the boolean dedups coincident geometry at 1µm),
// leaving pinhole gaps that punch through to the back surface. A few µm of
// outward nudge moves that surface safely inside the prism.
//
// 5µm is comfortably above Manifold's 1µm dedup tolerance and small enough
// to add no visible skirt — merged and per-cell clips converge to the same
// surface to floating-point. An earlier design bloated by 5×cellSize to
// chase surface that supposedly nudged outside the footprint; with the
// XY-projected footprint that surface doesn't exist, and the large bloat
// only inflated every clip by a ~12% outward skirt (and, because the cap
// scaled with the polygon's bbox, made the merged clip diverge from the
// per-cell one). bloatOpenEdges still caps displacement at the cell bbox's
// max side as a self-intersection guard for thin cells, but at 5µm that
// cap never binds.
const OpenEdgeBloatMM = 0.005

// ClipMeshToCellsManifold is the Manifold-backed per-cell clip. For
// each cell it:
//
//  1. Builds the cell's 2D polygon, nudging any open-edge run outward by
//     a small floating-point margin (see OpenEdgeBloatMM).
//  2. Extrudes that polygon between [zBot, zTop] into a closed prism.
//  3. Intersects the per-slab source Manifold (pre-split from the
//     full model via splitSrcBySlabs) with the prism via
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
// slabSrc holds the source Manifold and its per-slab pre-split pieces,
// shared by the per-cell and merged clip entry points.
type slabSrc struct {
	src     *manifoldbool.Manifold
	srcID   int32
	perSlab []*manifoldbool.Manifold
}

// buildSlabSrc dedups the model, builds its source Manifold, and
// pre-splits it into one Manifold per slab (see the per-slab rationale
// in ClipMeshToCellsManifold). The caller must call close() when done.
func buildSlabSrc(model *loader.LoadedModel, slabs []Slab) (*slabSrc, error) {
	verts, faces := DedupVertsByPosition(model.Vertices, model.Faces)
	if len(verts) == 0 || len(faces) == 0 {
		return nil, fmt.Errorf("cellslicer/manifold: source mesh has no faces")
	}
	src, err := manifoldbool.FromMesh(verts, faces)
	if err != nil {
		return nil, fmt.Errorf("cellslicer/manifold: build source Manifold: %w", err)
	}
	// Pre-split src into one Manifold per slab. Each per-cell
	// Intersection only walks the BVH of its own slab, cutting cgo and
	// boolean cost on tall models with many slabs. The per-slab
	// Manifold's own OriginalID is derived (-1); src's faces still
	// carry srcID via per-face run_original_id, so ToMeshFiltered(srcID)
	// downstream still recovers source-surface-only output and drops
	// the plane-cut faces split_by_plane adds.
	perSlab, err := splitSrcBySlabs(src, slabs)
	if err != nil {
		src.Close()
		return nil, fmt.Errorf("cellslicer/manifold: pre-split src by slab: %w", err)
	}
	return &slabSrc{src: src, srcID: src.OriginalID(), perSlab: perSlab}, nil
}

// slabManifold returns the per-slab source Manifold for slab si, or src
// itself when splitSrcBySlabs took its no-split early return (len(slabs)
// ≤ 1, leaving perSlab[si] nil).
func (s *slabSrc) slabManifold(si int) *manifoldbool.Manifold {
	if m := s.perSlab[si]; m != nil {
		return m
	}
	return s.src
}

func (s *slabSrc) close() {
	closeSlabManifolds(s.perSlab)
	s.src.Close()
}

func ClipMeshToCellsManifold(model *loader.LoadedModel, slabs []Slab) (ClipResult, error) {
	ss, err := buildSlabSrc(model, slabs)
	if err != nil {
		return ClipResult{}, err
	}
	defer ss.close()

	// One job per cell, tagged with its global flattened-CellSlabs index.
	type cellRef struct{ globalIdx, slabIdx, cellIdx int }
	globalOffsets := SlabGlobalOffsets(slabs)
	refs := make([]cellRef, 0, globalOffsets[len(slabs)])
	for si := range slabs {
		for ci := range slabs[si].Cells {
			refs = append(refs, cellRef{globalOffsets[si] + ci, si, ci})
		}
	}
	return runClipJobs(len(refs), func(i int) (int, [][3]float32, [][3]uint32, error) {
		r := refs[i]
		s := &slabs[r.slabIdx]
		v, f, cerr := clipOneCellManifold(ss.slabManifold(r.slabIdx), ss.srcID, &s.Cells[r.cellIdx], s.ZBot, s.ZTop)
		if cerr != nil {
			return 0, nil, nil, fmt.Errorf("cell %d (slab=%d,cell=%d): %w", r.globalIdx, r.slabIdx, r.cellIdx, cerr)
		}
		return r.globalIdx, v, f, nil
	})
}

// runClipJobs is the shared worker-pool engine behind both clip entry
// points (per-cell and merged-cell). It runs n independent clip jobs
// across NumCPU workers and concatenates the surviving meshes — in job
// order, into one unified vertex table — tagging every face with the
// global cell index its job returned.
//
// clip(i) produces the surface-only mesh for job i and the rep cell
// index to tag its faces with; it must already wrap any error with job
// context. clip is called concurrently for distinct i: Manifold is
// thread-safe across independent objects, source Manifolds are touched
// read-only, and each call's output goes to its own result slot, so no
// locking is needed inside clip.
//
// CellRep is left nil; the merged clip populates it after this returns.
func runClipJobs(n int, clip func(i int) (rep int, verts [][3]float32, faces [][3]uint32, err error)) (ClipResult, error) {
	type result struct {
		rep   int
		verts [][3]float32
		faces [][3]uint32
	}
	results := make([]result, n)

	nWorkers := runtime.NumCPU()
	if nWorkers > n {
		nWorkers = n
	}
	if nWorkers < 1 {
		nWorkers = 1
	}
	jobCh := make(chan int, n)
	for i := 0; i < n; i++ {
		jobCh <- i
	}
	close(jobCh)
	var (
		wg     sync.WaitGroup
		errMu  sync.Mutex
		firstE error
	)
	for w := 0; w < nWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ji := range jobCh {
				rep, v, f, cerr := clip(ji)
				if cerr != nil {
					errMu.Lock()
					if firstE == nil {
						firstE = cerr
					}
					errMu.Unlock()
					continue
				}
				if len(f) == 0 {
					continue
				}
				results[ji] = result{rep: rep, verts: v, faces: f}
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
			cr.FaceCellIdx = append(cr.FaceCellIdx, int32(r.rep))
		}
	}
	return cr, nil
}

// splitSrcBySlabs pre-splits src into one Manifold per slab by walking
// the slab Z planes bottom-up. The returned slice is indexed by slab
// index (not Z order). Entries may be nil — that signals "use src
// directly" and currently only happens when len(slabs) ≤ 1 (no
// splitting needed). closeSlabManifolds takes care of the nil handling.
//
// Assumes neighbouring slabs are contiguous in Z; gaps would leave a
// floating sliver between slabs that gets attached to the wrong side.
// SlabBoundaryPlanes-derived partitions satisfy this; defensive callers
// that pass arbitrary slab lists should verify it first.
func splitSrcBySlabs(src *manifoldbool.Manifold, slabs []Slab) ([]*manifoldbool.Manifold, error) {
	perSlab := make([]*manifoldbool.Manifold, len(slabs))
	if len(slabs) <= 1 {
		// One (or zero) slabs — no split needed; per-cell workers use
		// src directly via the nil sentinel.
		return perSlab, nil
	}
	// Walk slabs bottom-up. Sort indexes so we can split planes in Z
	// order even if caller passed slabs out of order.
	order := make([]int, len(slabs))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return slabs[order[i]].ZBot < slabs[order[j]].ZBot
	})
	// Verify contiguity: each slab's top must equal the next slab's
	// bottom (within a small float tolerance — slab Z planes come from
	// the same SlabBoundaryPlanes generator so exact equality is
	// expected, but defensive against rounding in alternate callers).
	// A gap leaves model geometry stranded between slabs and silently
	// attached to the wrong side by split_by_plane.
	const zEps = 1e-5
	for k := 0; k < len(order)-1; k++ {
		gap := slabs[order[k+1]].ZBot - slabs[order[k]].ZTop
		if gap < -zEps || gap > zEps {
			return nil, fmt.Errorf("non-contiguous slabs: slab %d ZTop=%g, slab %d ZBot=%g (gap=%g)",
				order[k], slabs[order[k]].ZTop, order[k+1], slabs[order[k+1]].ZBot, gap)
		}
	}
	// current holds the unallocated remainder of src as we split off
	// each slab. For k=0, current==src (caller-owned, do NOT close).
	// After the first split, current is a freshly allocated "above"
	// piece that we own and must close before re-assigning.
	current := src
	ownsCurrent := false
	rollback := func(upTo int) {
		for j := 0; j < upTo; j++ {
			if m := perSlab[order[j]]; m != nil {
				m.Close()
				perSlab[order[j]] = nil
			}
		}
		if ownsCurrent {
			current.Close()
		}
	}
	for k := 0; k < len(order)-1; k++ {
		si := order[k]
		// Plane is the boundary between slab order[k] and slab order[k+1].
		// Contiguity is checked above, so slabs[si].ZTop ==
		// slabs[order[k+1]].ZBot.
		zPlane := float64(slabs[si].ZTop)
		above, below, err := manifoldbool.SplitByPlane(current, 0, 0, 1, zPlane)
		// First iteration: current==src (caller-owned, ownsCurrent
		// false → skipped). Subsequent iterations: current is the
		// previously-allocated "above" piece we own.
		if ownsCurrent {
			current.Close()
			ownsCurrent = false
		}
		if err != nil {
			// SplitByPlane closed above/below already on error; just
			// release any previously emitted per-slab pieces.
			rollback(k)
			return nil, fmt.Errorf("split at z=%g (slab %d): %w", zPlane, si, err)
		}
		perSlab[si] = below
		current = above
		ownsCurrent = true
	}
	// The remaining "current" is the topmost slab's portion.
	perSlab[order[len(order)-1]] = current
	return perSlab, nil
}

// closeSlabManifolds closes every per-slab Manifold splitSrcBySlabs
// allocated. nil entries (the "use src directly" sentinel) are skipped;
// the caller still owns src and closes it separately.
func closeSlabManifolds(perSlab []*manifoldbool.Manifold) {
	for i, m := range perSlab {
		if m == nil {
			continue
		}
		m.Close()
		perSlab[i] = nil
	}
}

// clipOneCellManifold builds the cell prism, intersects it with src,
// and returns the surface-only mesh — only the faces inherited from
// src survive, so the cell prism's own walls (which the boolean
// intersection adds where the prism cuts through the model volume)
// are stripped before return. Empty results (the cell doesn't
// overlap the model) are returned as (nil, nil, nil).
func clipOneCellManifold(src *manifoldbool.Manifold, srcID int32, cell *Cell, zBot, zTop float32) ([][3]float32, [][3]uint32, error) {
	poly := bloatOpenEdges(cell.Outer, cell.OuterEdgeOpen, OpenEdgeBloatMM)
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
// The displacement is capped at the cell's bbox max-side so the open edge
// can never be pushed past its opposite closed edge (which would produce a
// self-intersecting polygon and either a Manifold rejection or a
// degenerate prism). At the current OpenEdgeBloatMM (a few µm) this cap
// never binds; it's a guard against a future larger bloat or a
// pathologically thin cell.
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
	// cells or pathological partition geometries. At the current few-µm
	// OpenEdgeBloatMM this never binds; it only mattered for the old
	// cell-scale bloat and is kept as a safety guard.
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
