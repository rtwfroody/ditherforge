package cellslicer

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"

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
//
// The per-slab pre-split (see runSplit) runs in its own goroutine so the
// clip worker pool can start immediately and pick up each slab's jobs as
// soon as that slab's piece is produced, rather than waiting for the whole
// bottom-up split chain to finish first. perSlab[si] is written exactly
// once — by the split goroutine — and ready[si] is closed immediately
// after, so a consumer that has observed ready[si] closed sees the final
// perSlab[si] with no further synchronisation. src, srcID, perSlab length,
// order and ready are all fixed before the goroutine starts.
type slabSrc struct {
	src     *manifoldbool.Manifold
	srcID   int32
	perSlab []*manifoldbool.Manifold
	// order is the slab indices in bottom-up Z order — the order runSplit
	// emits pieces in, and the order the clip dispatcher releases jobs in.
	order []int
	// ready[si] is closed once perSlab[si] is final (either the split
	// produced it, or the split failed/was cancelled and abandoned it —
	// waitSlab distinguishes via splitErr). In the no-split case every
	// channel is pre-closed.
	ready []chan struct{}

	// splitCancel stops the split goroutine on teardown; nil in the
	// no-split case. splitDone is closed when the goroutine exits (or
	// pre-closed in the no-split case), so close() can free pieces only
	// once the writer has stopped. splitErr (guarded by splitMu) holds
	// the first split error, surfaced to consumers via waitSlab.
	splitCancel context.CancelFunc
	splitDone   chan struct{}
	splitMu     sync.Mutex
	splitErr    error
}

// ClipProgress carries optional progress callbacks for the clip entry
// points. Both fields may be nil, as may the *ClipProgress itself —
// either disables reporting. Callbacks receive (done, total) for their
// phase; Jobs is invoked from multiple worker goroutines with done
// values drawn from a shared atomic counter.
type ClipProgress struct {
	// SlabSplit ticks once per Z-plane cut while the source mesh is
	// pre-split into per-slab Manifolds (sequential; see splitSrcBySlabs).
	SlabSplit func(done, total int)
	// Jobs ticks once per completed clip job — one per cell on the
	// per-cell path, one per merged group on the merged path. total is
	// the job count, known only once grouping is done.
	Jobs func(done, total int)
}

// slabSplit and jobs are nil-safe accessors so call sites don't fan out
// into nil checks. A nil receiver returns a nil callback.
func (p *ClipProgress) slabSplit() func(done, total int) {
	if p == nil {
		return nil
	}
	return p.SlabSplit
}

func (p *ClipProgress) jobs() func(done, total int) {
	if p == nil {
		return nil
	}
	return p.Jobs
}

// buildSlabSrc dedups the model, builds its source Manifold, and starts
// pre-splitting it into one Manifold per slab (see the per-slab rationale
// in ClipMeshToCellsManifold). The split runs in a background goroutine
// (runSplit) so the clip worker pool can begin as soon as the first slab
// piece is ready; runClipJobs gates each slab's jobs on its piece via
// waitSlab. The caller must call close() when done — it stops the split
// goroutine and frees every piece.
//
// The slab contiguity/order check is done synchronously here so a
// malformed slab list surfaces as an error from buildSlabSrc, exactly as
// before, rather than only once a worker touches the piece. onSplit (may
// be nil) ticks once per plane cut, now from the split goroutine.
func buildSlabSrc(ctx context.Context, model *loader.LoadedModel, slabs []Slab, onSplit func(done, total int)) (*slabSrc, error) {
	verts, faces := DedupVertsByPosition(model.Vertices, model.Faces)
	if len(verts) == 0 || len(faces) == 0 {
		return nil, fmt.Errorf("cellslicer/manifold: source mesh has no faces")
	}
	src, err := manifoldbool.FromMesh(verts, faces)
	if err != nil {
		return nil, fmt.Errorf("cellslicer/manifold: build source Manifold: %w", err)
	}
	order, err := slabZOrder(slabs)
	if err != nil {
		src.Close()
		return nil, fmt.Errorf("cellslicer/manifold: pre-split src by slab: %w", err)
	}
	ss := &slabSrc{
		src:       src,
		srcID:     src.OriginalID(),
		perSlab:   make([]*manifoldbool.Manifold, len(slabs)),
		order:     order,
		ready:     make([]chan struct{}, len(slabs)),
		splitDone: make(chan struct{}),
	}
	for i := range ss.ready {
		ss.ready[i] = make(chan struct{})
	}
	if len(slabs) <= 1 {
		// One (or zero) slabs — no split needed; workers use src
		// directly via the nil perSlab sentinel. Everything is ready now
		// and there is no goroutine to stop.
		for i := range ss.ready {
			close(ss.ready[i])
		}
		close(ss.splitDone)
		return ss, nil
	}
	splitCtx, cancel := context.WithCancel(ctx)
	ss.splitCancel = cancel
	go ss.runSplit(splitCtx, slabs, onSplit)
	return ss, nil
}

// slabManifold returns the per-slab source Manifold for slab si, or src
// itself when there was no split (len(slabs) ≤ 1, leaving perSlab[si]
// nil). Callers must have observed ready[si] closed (via waitSlab) first,
// so perSlab[si] is the final value the split goroutine wrote.
func (s *slabSrc) slabManifold(si int) *manifoldbool.Manifold {
	if m := s.perSlab[si]; m != nil {
		return m
	}
	return s.src
}

// waitSlab blocks until slab si's piece is ready (or the split failed or
// ctx was cancelled). It returns the split error if the split goroutine
// recorded one, ctx.Err() on cancellation, or nil when perSlab[si] is
// ready to use.
func (s *slabSrc) waitSlab(ctx context.Context, si int) error {
	select {
	case <-s.ready[si]:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.splitMu.Lock()
	err := s.splitErr
	s.splitMu.Unlock()
	return err
}

func (s *slabSrc) close() {
	// Stop the split goroutine and wait for it to exit before freeing any
	// piece — it writes perSlab concurrently, so freeing before it stops
	// would race. splitDone is pre-closed in the no-split case.
	if s.splitCancel != nil {
		s.splitCancel()
	}
	<-s.splitDone
	closeSlabManifolds(s.perSlab)
	s.src.Close()
}

func ClipMeshToCellsManifold(model *loader.LoadedModel, slabs []Slab) (ClipResult, error) {
	return ClipMeshToCellsManifoldProgress(context.Background(), model, slabs, nil)
}

// ClipMeshToCellsManifoldProgress is ClipMeshToCellsManifold with
// optional progress reporting (prog may be nil).
//
// Cancellation: checked between pre-split plane cuts and before each
// per-cell clip job; returns ctx.Err() when cancelled. A single
// in-flight Manifold boolean runs to completion (each is one short
// cgo call), so cancellation lands within one job's duration.
func ClipMeshToCellsManifoldProgress(ctx context.Context, model *loader.LoadedModel, slabs []Slab, prog *ClipProgress) (ClipResult, error) {
	ss, err := buildSlabSrc(ctx, model, slabs, prog.slabSplit())
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
	jobSlab := make([]int, len(refs))
	for i, r := range refs {
		jobSlab[i] = r.slabIdx
	}
	return runClipJobs(ctx, ss, jobSlab, func(i int) (int, [][3]float32, [][3]uint32, error) {
		r := refs[i]
		s := &slabs[r.slabIdx]
		v, f, cerr := clipOneCellManifold(ss.slabManifold(r.slabIdx), ss.srcID, &s.Cells[r.cellIdx], s.ZBot, s.ZTop)
		if cerr != nil {
			return 0, nil, nil, fmt.Errorf("cell %d (slab=%d,cell=%d): %w", r.globalIdx, r.slabIdx, r.cellIdx, cerr)
		}
		return r.globalIdx, v, f, nil
	}, prog.jobs())
}

// runClipJobs is the shared worker-pool engine behind both clip entry
// points (per-cell and merged-cell). It runs one clip job per entry of
// jobSlab across NumCPU workers and concatenates the surviving meshes —
// in job order, into one unified vertex table — tagging every face with
// the global cell index its job returned.
//
// jobSlab[i] is the slab index job i clips against (its piece is
// ss.perSlab[jobSlab[i]]). A dispatcher goroutine walks the slabs in
// split-emit (Z) order and releases each slab's jobs only once ss has
// produced that slab's pre-split piece (ss.waitSlab), so the worker pool
// starts immediately and the ~sequential pre-split (runSplit) overlaps
// the parallel clip instead of running entirely before it. Because
// results are written into their original job-index slot, this scheduling
// never changes the output bytes — only when each boolean runs.
//
// clip(i) produces the surface-only mesh for job i and the rep cell
// index to tag its faces with; it must already wrap any error with job
// context. clip is called concurrently for distinct i: Manifold is
// thread-safe across independent objects, source Manifolds are touched
// read-only, and each call's output goes to its own result slot, so no
// locking is needed inside clip.
//
// CellRep is left nil; the merged clip populates it after this returns.
//
// onJob (may be nil) ticks once per completed job with (jobs done, n).
// done values come from a shared atomic counter, so each tick carries a
// distinct increasing count — but workers may deliver them out of
// order; consumers must tolerate that (progress.Stage.Span does).
//
// Cancellation: workers check ctx before each job and stop picking up new
// ones once it is done; the dispatcher unblocks on ctx via waitSlab.
// runClipJobs then returns ctx.Err(). A split failure surfaces the same
// way — waitSlab returns it and it becomes the stage error.
//
// Panic containment: a panic in clip is captured as the job's error
// (stack preserved) instead of crashing the process — goroutine panics
// can't be recovered anywhere else, and the clip jobs call into native
// Manifold code where a malformed cell is most likely to blow up.
func runClipJobs(ctx context.Context, ss *slabSrc, jobSlab []int, clip func(i int) (rep int, verts [][3]float32, faces [][3]uint32, err error), onJob func(done, total int)) (ClipResult, error) {
	n := len(jobSlab)
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
	// Group jobs by slab, preserving ascending job-index order within each
	// slab (the original dispatch order). The dispatcher releases a slab's
	// jobs once its piece is ready.
	jobsBySlab := make([][]int, len(ss.ready))
	for ji, si := range jobSlab {
		jobsBySlab[si] = append(jobsBySlab[si], ji)
	}
	// Buffered to n so the dispatcher never blocks handing off jobs, even
	// if every worker has already exited on cancellation.
	jobCh := make(chan int, n)
	var (
		wg     sync.WaitGroup
		errMu  sync.Mutex
		firstE error
		nDone  atomic.Int64
	)
	setErr := func(e error) {
		errMu.Lock()
		if firstE == nil {
			firstE = e
		}
		errMu.Unlock()
	}
	// Dispatcher: gate each slab's jobs on its pre-split piece. Walking in
	// ss.order (Z-emit order) means the frontier of released jobs follows
	// the split, so workers always have runnable jobs while the split runs
	// ahead of them.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(jobCh)
		for _, si := range ss.order {
			if err := ss.waitSlab(ctx, si); err != nil {
				// A genuine split failure (ctx not cancelled) becomes the
				// stage error; a cancellation is reported via ctx.Err()
				// after wg.Wait, matching the worker path.
				if ctx.Err() == nil {
					setErr(err)
				}
				return
			}
			for _, ji := range jobsBySlab[si] {
				jobCh <- ji
			}
		}
	}()
	// safeClip converts a panicking clip job into an error result so
	// the worker goroutine survives (see the doc comment).
	safeClip := func(ji int) (rep int, v [][3]float32, f [][3]uint32, cerr error) {
		defer func() {
			if r := recover(); r != nil {
				cerr = fmt.Errorf("clip job %d: panic: %v\n%s", ji, r, debug.Stack())
			}
		}()
		return clip(ji)
	}
	for w := 0; w < nWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ji := range jobCh {
				if ctx.Err() != nil {
					return
				}
				rep, v, f, cerr := safeClip(ji)
				if onJob != nil {
					onJob(int(nDone.Add(1)), n)
				}
				if cerr != nil {
					setErr(cerr)
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
	// firstE before ctx.Err(): when a job error/panic and a cancellation
	// coincide, the job error carries the diagnostic signal (a panic's
	// stack in particular); ctx.Err() carries nothing. Callers that care
	// about "was this a cancel?" check ctx themselves.
	if firstE != nil {
		return ClipResult{}, firstE
	}
	if err := ctx.Err(); err != nil {
		return ClipResult{}, err
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

// slabZOrder returns the slab indices sorted bottom-up by ZBot — the
// order runSplit walks the Z planes in — after verifying the slabs are
// contiguous in Z. A gap would leave model geometry stranded between
// slabs and silently attached to the wrong side by split_by_plane;
// SlabBoundaryPlanes-derived partitions satisfy contiguity, but this is
// checked defensively for alternate callers. Returns an error on any gap.
func slabZOrder(slabs []Slab) ([]int, error) {
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
	const zEps = 1e-5
	for k := 0; k < len(order)-1; k++ {
		gap := slabs[order[k+1]].ZBot - slabs[order[k]].ZTop
		if gap < -zEps || gap > zEps {
			return nil, fmt.Errorf("non-contiguous slabs: slab %d ZTop=%g, slab %d ZBot=%g (gap=%g)",
				order[k], slabs[order[k]].ZTop, order[k+1], slabs[order[k+1]].ZBot, gap)
		}
	}
	return order, nil
}

// runSplit pre-splits src into one Manifold per slab by walking the slab
// Z planes bottom-up (s.order), writing each piece into s.perSlab and
// closing s.ready[si] as it is produced. It runs in its own goroutine so
// the clip worker pool can start clipping already-produced pieces while
// the remaining planes are still being cut — the same sequential chain of
// split_by_plane calls as before, just overlapped with the clip jobs.
//
// The split only touches src and its own private remainder ("current"),
// never a previously-emitted piece, so concurrent read-only clip ops on
// those pieces are safe.
//
// onSplit (may be nil) ticks once per plane cut with (cuts done,
// len(order)-1).
//
// Cancellation / error: on ctx cancel or a SplitByPlane failure the
// remainder we own is freed and every not-yet-produced slab's ready
// channel is closed with splitErr set, so waiters unblock and surface the
// error rather than hang. Already-emitted pieces are left intact — clip
// jobs may still be reading them; close() frees them once every worker
// has stopped.
func (s *slabSrc) runSplit(ctx context.Context, slabs []Slab, onSplit func(done, total int)) {
	defer close(s.splitDone)
	order := s.order
	// current holds the unallocated remainder of src as we split off each
	// slab. For k=0, current==src (owned by slabSrc, freed by close via
	// s.src, not here). After the first split, current is a freshly
	// allocated "above" piece we own and must close before re-assigning.
	current := s.src
	ownsCurrent := false
	// fail records the error, frees the owned remainder, and closes the
	// ready channels for every slab from Z-order position k onward (the
	// ones this run never produced) so waiters unblock.
	fail := func(err error, k int) {
		s.splitMu.Lock()
		if s.splitErr == nil {
			s.splitErr = err
		}
		s.splitMu.Unlock()
		if ownsCurrent {
			current.Close()
		}
		for j := k; j < len(order); j++ {
			close(s.ready[order[j]])
		}
	}
	for k := 0; k < len(order)-1; k++ {
		if err := ctx.Err(); err != nil {
			fail(err, k)
			return
		}
		si := order[k]
		// Plane is the boundary between slab order[k] and slab order[k+1].
		// Contiguity was checked by slabZOrder, so slabs[si].ZTop ==
		// slabs[order[k+1]].ZBot.
		zPlane := float64(slabs[si].ZTop)
		above, below, err := manifoldbool.SplitByPlane(current, 0, 0, 1, zPlane)
		// First iteration: current==src (owned by slabSrc, ownsCurrent
		// false → skipped). Subsequent iterations: current is the
		// previously-allocated "above" piece we own.
		if ownsCurrent {
			current.Close()
			ownsCurrent = false
		}
		if err != nil {
			// SplitByPlane closed above/below already on error.
			fail(fmt.Errorf("split at z=%g (slab %d): %w", zPlane, si, err), k)
			return
		}
		s.perSlab[si] = below
		close(s.ready[si])
		current = above
		ownsCurrent = true
		if onSplit != nil {
			onSplit(k+1, len(order)-1)
		}
	}
	// The remaining "current" is the topmost slab's portion.
	last := order[len(order)-1]
	s.perSlab[last] = current
	close(s.ready[last])
}

// closeSlabManifolds closes every per-slab Manifold runSplit
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
