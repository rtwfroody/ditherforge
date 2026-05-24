// 2D per-slab clip: cuts each model triangle into per-cell fragments
// without any 3D boolean.
//
// Pipeline (per source triangle):
//
//  1. sliceTriangleToSlab — Sutherland-Hodgman against the slab's
//     z=zBot / z=zTop planes. Yields one planar 3D sub-polygon
//     (3-to-7 vertices, convex) that lives in [zBot, zTop] and the
//     source triangle's plane.
//
//  2. clipPolyToCells — intersect that sub-polygon against each
//     candidate cell's outer polygon. Internally dispatches to a
//     Clipper 2D path (when the sub-polygon has measurable XY area;
//     Z is recovered from the source plane equation) or a vertical-
//     scan path (when its XY projection is degenerate, i.e. the
//     source triangle was near-vertical). Both paths emit cellPieces
//     with full 3D vertices.
//
//  3. appendCellPiece — splice each cell-piece against the slab-wide
//     3D vertex union (to eliminate T-junctions), triangulate, and
//     emit faces tagged with the cell index.
//
// Replaces the per-cell CGAL clip_surface path that used to live in
// clip.go: a 1.2M-cell pipeline runs in seconds instead of hours,
// with no CGAL setup amortization or thread-safety concerns.

package cellslicer

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// debugHoles is the single read of the DITHERFORGE_HOLE_REPORT env
// var. Used to gate the cellslicer's diagnostic instrumentation
// (Phase 0/1/2 hole reports, per-phase timing, per-worker progress
// goroutine, polygon-size histogram). Re-reading per call would put
// a syscall in the hot inner loops; a single init-time read keeps
// the no-op path actually no-op.
var debugHoles = os.Getenv("DITHERFORGE_HOLE_REPORT") != ""

// reportHolesByPos counts boundary / non-manifold edges with vertices
// keyed by 1µm Clipper bucket (so coincident-position vertices match
// across pieces that haven't gone through the cross-piece dedup yet).
// Gated on DITHERFORGE_HOLE_REPORT=1; no-op otherwise. Used inside
// ClipMeshToCells2D to bisect which sub-stage introduces boundary
// edges.
//
// slabZSet, when non-nil, classifies boundary edges further: an edge
// is "on a slab plane" when both endpoints' Z (in 1µm buckets) is in
// the set. Splits the boundary count into in-slab vs slab-plane to
// distinguish cross-slab mismatch (splice's job) from in-slab cell-
// fragmentation mismatch.
func reportHolesByPos(stage string, verts [][3]float32, faces [][3]uint32, slabZSet map[int64]struct{}) {
	if os.Getenv("DITHERFORGE_HOLE_REPORT") == "" {
		return
	}
	type ek struct{ A, B int3D }
	mk := func(a, b int3D) ek {
		if a.X > b.X || (a.X == b.X && a.Y > b.Y) || (a.X == b.X && a.Y == b.Y && a.Z > b.Z) {
			a, b = b, a
		}
		return ek{a, b}
	}
	counts := make(map[ek]int, len(faces)*2)
	for _, f := range faces {
		va := int3DOf(verts[f[0]])
		vb := int3DOf(verts[f[1]])
		vc := int3DOf(verts[f[2]])
		if va != vb {
			counts[mk(va, vb)]++
		}
		if vb != vc {
			counts[mk(vb, vc)]++
		}
		if vc != va {
			counts[mk(vc, va)]++
		}
	}
	var boundary, manifold, nonManifold int
	var boundaryOnSlab, boundaryInSlab int
	for e, c := range counts {
		switch {
		case c == 1:
			boundary++
			if slabZSet != nil {
				_, ok1 := slabZSet[e.A.Z]
				_, ok2 := slabZSet[e.B.Z]
				if ok1 && ok2 {
					boundaryOnSlab++
				} else {
					boundaryInSlab++
				}
			}
		case c == 2:
			manifold++
		default:
			nonManifold++
		}
	}
	if slabZSet != nil {
		fmt.Fprintf(os.Stderr, "  [hole-report] %s: %d faces, %d edges, boundary=%d (onSlabPlane=%d inSlab=%d) manifold=%d nonManifold=%d\n",
			stage, len(faces), len(counts), boundary, boundaryOnSlab, boundaryInSlab, manifold, nonManifold)
	} else {
		fmt.Fprintf(os.Stderr, "  [hole-report] %s: %d faces, %d edges, boundary=%d manifold=%d nonManifold=%d\n",
			stage, len(faces), len(counts), boundary, manifold, nonManifold)
	}
}

// fanTriangulate returns a triangle list for a polygon via a fan from
// vertex 0. Only used by reportHolesByPos to triangulate cellPieces
// for counting; not for any production geometry.
func fanTriangulate(n int) [][3]uint32 {
	if n < 3 {
		return nil
	}
	out := make([][3]uint32, 0, n-2)
	for i := 1; i < n-1; i++ {
		out = append(out, [3]uint32{0, uint32(i), uint32(i + 1)})
	}
	return out
}

// slabPoly is one source triangle clipped against a slab's Z range.
// Vertices are stored in mesh coords (full 3D), wound in the source
// triangle's order. The polygon is planar (it lives in the source
// triangle's plane) and convex (Z-clipping a triangle with two
// half-spaces preserves convexity).
//
// Normal is the source triangle's facing direction, cross-product
// of its edges. Not unit-normalized — only its direction is used
// downstream (winding decisions in appendCellPiece, dominant-axis
// pick for Earcut projection, and the cap Z-lift's plane equation,
// which is invariant to a uniform scale of n).
type slabPoly struct {
	Pts    [][3]float32
	Normal [3]float32
}

// ClipMeshToCells2D returns a mesh whose faces are fragments of the
// input model, each tagged with the global cell index it falls in.
// For each slab, every model triangle is Z-clipped to the slab and
// then 2D-clipped against each candidate cell's outer polygon.
//
// Runs as two slab-parallel passes with a barrier between them:
// Phase 1 (clip slabPolys, build per-slab seen3D) has no cross-slab
// dependency; Phase 2 (splice + emit) needs every slab's Phase 1
// seen3D in order to contribute neighbour boundary vertices on the
// shared Z planes. Each pass uses runtime.NumCPU() workers; details
// of the splice/triangulation are in clip2d_subdivide.go.
func ClipMeshToCells2D(model *loader.LoadedModel, slabs []Slab, triIdx *TriXYZIndex) (ClipResult, error) {
	offsets := make([]int, len(slabs)+1)
	for si := range slabs {
		offsets[si+1] = offsets[si] + len(slabs[si].Cells)
	}

	// Pre-slice every model triangle into per-slab pieces.
	slabPolys := make([][]slabPoly, len(slabs))
	for ti := range model.Faces {
		f := model.Faces[ti]
		a := model.Vertices[f[0]]
		b := model.Vertices[f[1]]
		c := model.Vertices[f[2]]
		zMin := minf3(a[2], b[2], c[2])
		zMax := maxf3(a[2], b[2], c[2])
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
			if poly := sliceTriangleToSlab(a, b, c, s.ZBot, s.ZTop); poly != nil {
				slabPolys[si] = append(slabPolys[si], *poly)
			}
		}
	}
	_ = triIdx

	// Set of 1µm-quantized slab plane Z values (used by the
	// hole-reports below to split boundary edges into "on a slab
	// plane" vs "in slab interior" — the former should be splice's
	// responsibility, the latter shouldn't exist at all).
	slabZSet := make(map[int64]struct{})
	for _, s := range slabs {
		slabZSet[int64(math.Round(float64(s.ZBot)*clipperScale))] = struct{}{}
		slabZSet[int64(math.Round(float64(s.ZTop)*clipperScale))] = struct{}{}
	}

	// hole-report: post slab-Z-clip mesh. Source triangles spanning
	// multiple slabs get cut at Z=plane; the cuts on slab i's top and
	// slab i+1's bottom should land on identical XY positions because
	// lerpAtZ writes z=plane verbatim and the X,Y interpolation uses
	// the same source-triangle edge endpoints. If this mesh has
	// boundary edges, the slab Z-clip itself is dropping or
	// fragmenting geometry.
	if debugHoles {
		var sliceVerts [][3]float32
		var sliceFaces [][3]uint32
		for _, polys := range slabPolys {
			for _, p := range polys {
				base := uint32(len(sliceVerts))
				sliceVerts = append(sliceVerts, p.Pts...)
				for _, tri := range fanTriangulate(len(p.Pts)) {
					sliceFaces = append(sliceFaces, [3]uint32{tri[0] + base, tri[1] + base, tri[2] + base})
				}
			}
		}
		reportHolesByPos("phase0 (slabPolys fan-triangulated)", sliceVerts, sliceFaces, slabZSet)
	}

	// Per-slab cell-bbox indices, built once. Re-used by every slab
	// polygon during candidate-cell lookup.
	cellIndices := make([]*slabCellIndex, len(slabs))
	for si := range slabs {
		if len(slabs[si].Cells) > 0 {
			cellIndices[si] = buildSlabCellIndex(&slabs[si])
		}
	}

	// Two-pass per-slab parallelism with a barrier between phases:
	//
	//   Phase 1 (all slabs concurrent): clip every slabPoly against
	//   candidate cells, collecting cellPieces and a slab-wide seen3D
	//   set. No cross-slab dependencies.
	//
	//   Phase 2 (all slabs concurrent, after barrier): splice each
	//   cellPiece against (own seen3D) ∪ (neighbour-below's vertices
	//   on the shared zBot plane) ∪ (neighbour-above's vertices on
	//   the shared zTop plane), then Earcut and emit. Splice is read-
	//   only against the frozen seen3D maps, so no synchronization is
	//   needed once Phase 1 is done.
	//
	// The slab-wide seen3D eliminates within-slab T-junctions (e.g.
	// cube cap's STL diagonal between two source tris). The cross-slab
	// boundary contribution eliminates T-junctions on the shared Z
	// plane between adjacent slabs, whose cell partitions differ.
	type slabPhase1 struct {
		pieces []cellPiece
		seen3D map[int3D]struct{}
		// Parallel to pieces; only populated under
		// DITHERFORGE_HOLE_REPORT. 0 = clipPolyToCellsCap (3D cap
		// clip), 1 = clipPolyToCellsVertical (wall-scan clip).
		pieceOrigin []uint8
	}
	type slabResult struct {
		verts        [][3]float32
		faces        [][3]uint32
		localCellIdx []int32 // slab-local cell idx for each face
	}
	// Memory note: every slab's Phase 1 result lives until the barrier
	// completes (Phase 2 needs the neighbour seen3D maps). The old
	// fused-worker code recycled each slab's intermediate immediately;
	// this version's peak live set is roughly the sum across all slabs.
	// Bounded by the eventual output mesh size, so not concerning for
	// printable-object workloads — revisit if a memory regression
	// shows up on a very large model.
	phase1 := make([]slabPhase1, len(slabs))
	results := make([]slabResult, len(slabs))

	nWorkers := runtime.NumCPU()
	if nWorkers > len(slabs) {
		nWorkers = len(slabs)
	}
	if nWorkers < 1 {
		nWorkers = 1
	}

	// Phase 1 — clip slabPolys, build seen3D.
	//
	// NOTE: splice_diag_test.go:runPhase1ForDiag mirrors this loop for
	// the SPLICE_DIAG diagnostic — the inner clipPolyToCells call and
	// the seen3D semantics AND the worker fan-out (NumCPU goroutines,
	// jobCh, per-worker candidates buffer). Any change to either layer
	// here must be reflected there or the diagnostic will silently
	// report against a stale algorithm.
	tPhase1 := time.Now()
	{
		jobCh := make(chan int, len(slabs))
		for si := range slabs {
			jobCh <- si
		}
		close(jobCh)
		var wg sync.WaitGroup
		for w := 0; w < nWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var candidates []int
				for si := range jobCh {
					idx := cellIndices[si]
					if idx == nil {
						continue
					}
					seen3D := make(map[int3D]struct{}, 64)
					var pieces []cellPiece
					var pieceOrigin []uint8
					// (uses package-level debugHoles)
					for _, p := range slabPolys[si] {
						before := len(pieces)
						pieces, candidates = clipPolyToCells(p, si, slabs, idx, pieces, seen3D, candidates)
						if debugHoles {
							origin := uint8(0)
							if isPolyXYDegenerate(p.Pts) {
								origin = 1
							}
							for j := before; j < len(pieces); j++ {
								pieceOrigin = append(pieceOrigin, origin)
							}
						}
					}
					phase1[si] = slabPhase1{pieces: pieces, seen3D: seen3D, pieceOrigin: pieceOrigin}
				}
			}()
		}
		wg.Wait()
	}
	if debugHoles {
		fmt.Fprintf(os.Stderr, "  [hole-report] phase1 elapsed: %.1fs\n", time.Since(tPhase1).Seconds())
	}

	// Filter helper: returns the int3D Z value for a slab boundary
	// plane, so Phase 2 workers can pick neighbour seen3D entries on
	// the shared plane with an exact == on the 1µm-quantized Z.
	//
	// The exact-equality filter relies on:
	//   - clipPolygonByZHalfSpace's lerpAtZ writes z = zPlane verbatim
	//     (slab Z-clip output).
	//   - clipPolyToCellsCap clips slabPolys against cell prisms in 3D
	//     via Sutherland-Hodgman (clipPolyByPlaneXY), whose lerp
	//     interpolates Z linearly along an edge. The slabPoly is
	//     planar; any new vertex from intersecting one of its edges
	//     with a vertical cell face lies on that same plane, and
	//     linear lerp between two on-plane endpoints stays on the
	//     plane exactly. Combined with the slab Z-clip placing the
	//     top/bottom slab-plane vertices at zPlane verbatim, no
	//     boundary vertex drifts off the plane — no clamp needed.
	//     (See commit 21b7b25 for context: the previous Clipper-2D-
	//     then-re-lift cap path drifted by |grad_xy(z)| × 1µm on
	//     slanted near-walls, which is what motivated dropping it.)
	//   - clipPolyByPlaneXY's lerpAtPlaneXY interpolates Z linearly;
	//     both endpoints on the slab plane → exact plane Z out.
	//
	// A vertex that drifts past 1µm and slips the filter would just
	// fail to participate in the cross-slab splice for that neighbour
	// (manifoldness degrades locally; geometry stays valid).
	planeZInt := func(z float32) int64 {
		return int64(math.Round(float64(z) * clipperScale))
	}

	// Phase 2 — splice + emit, with neighbour boundary contributions.
	tPhase2 := time.Now()
	var phase2Pieces, phase2CandIters, phase2VertsIn, phase2VertsOut uint64
	var phase2SlabsDone uint64
	// Per-worker state so the progress printer can identify a hung
	// worker. workerSlab[w] = the slab index that worker w is currently
	// processing (or -1 when idle). workerPieceIdx[w] = the index of
	// the piece within that slab that the worker is currently in
	// (-1 when between pieces). workerPieceN[w] = the spliced polygon
	// size for that piece (so we can see if it's stuck on something
	// non-trivial). All updated by the worker before calling
	// appendCellPiece and read by the progress goroutine; uses atomic
	// to avoid torn reads. Sized to nWorkers.
	workerSlab := make([]int64, nWorkers)
	workerPieceIdx := make([]int64, nWorkers)
	workerPieceN := make([]int64, nWorkers)
	// workerStep: 0=idle, 1=grid.query, 2=splice, 3=earcut/fan, 4=emit-tris
	workerStep := make([]int64, nWorkers)
	// workerSplicedN: post-splice polygon size (if known).
	workerSplicedN := make([]int64, nWorkers)
	for i := range workerSlab {
		workerSlab[i] = -1
		workerPieceIdx[i] = -1
	}
	stopProgress := make(chan struct{})
	if debugHoles {
		go func() {
			tick := time.NewTicker(5 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-stopProgress:
					return
				case <-tick.C:
					// Re-check stop before emitting so a tick that
					// races with close(stopProgress) doesn't print a
					// progress line after the final elapsed report.
					select {
					case <-stopProgress:
						return
					default:
					}
					hist := [9]uint64{}
					for i := range hist {
						hist[i] = atomic.LoadUint64(&phase2NHist[i])
					}
					fmt.Fprintf(os.Stderr,
						"  [hole-report] phase2 progress %.0fs slabsDone=%d/%d pieces=%d nMax=%d nPathological=%d Nhist=%v\n",
						time.Since(tPhase2).Seconds(),
						atomic.LoadUint64(&phase2SlabsDone), len(slabs),
						atomic.LoadUint64(&phase2Pieces),
						atomic.LoadUint64(&phase2NMaxSeen),
						atomic.LoadUint64(&phase2NPathological),
						hist)
					// Per-worker: who's still active and on what.
					stepName := []string{"idle", "grid.query", "splice", "earcut/fan", "emit-tris"}
					for w := 0; w < nWorkers; w++ {
						si := atomic.LoadInt64(&workerSlab[w])
						if si < 0 {
							continue
						}
						pi := atomic.LoadInt64(&workerPieceIdx[w])
						pn := atomic.LoadInt64(&workerPieceN[w])
						st := atomic.LoadInt64(&workerStep[w])
						sp := atomic.LoadInt64(&workerSplicedN[w])
						stName := "?"
						if st >= 0 && int(st) < len(stepName) {
							stName = stepName[st]
						}
						fmt.Fprintf(os.Stderr, "    worker %d: slab=%d piece=%d pieceN=%d splicedN=%d step=%s\n",
							w, si, pi, pn, sp, stName)
					}
				}
			}
		}()
	}
	{
		jobCh := make(chan int, len(slabs))
		for si := range slabs {
			jobCh <- si
		}
		close(jobCh)
		var wg sync.WaitGroup
		for w := 0; w < nWorkers; w++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()
				for si := range jobCh {
					atomic.StoreInt64(&workerSlab[workerID], int64(si))
					atomic.StoreInt64(&workerPieceIdx[workerID], -1)
					p1 := phase1[si]
					if len(p1.pieces) == 0 {
						atomic.StoreInt64(&workerSlab[workerID], -1)
						continue
					}
					splice3D := make([]int3D, 0, len(p1.seen3D))
					for p := range p1.seen3D {
						splice3D = append(splice3D, p)
					}
					// Neighbour below: vertices on slabs[si].ZBot.
					if si > 0 {
						zb := planeZInt(slabs[si].ZBot)
						for p := range phase1[si-1].seen3D {
							if p.Z == zb {
								splice3D = append(splice3D, p)
							}
						}
					}
					// Neighbour above: vertices on slabs[si].ZTop.
					if si+1 < len(slabs) {
						zt := planeZInt(slabs[si].ZTop)
						for p := range phase1[si+1].seen3D {
							if p.Z == zt {
								splice3D = append(splice3D, p)
							}
						}
					}

					// stepPtr / splicedNPtr feed the per-worker progress
					// reporter; nil-out in non-debug runs so
					// appendCellPiece's hot loop skips three atomic
					// stores per piece.
					var stepPtr, splicedNPtr *int64
					if debugHoles {
						stepPtr = &workerStep[workerID]
						splicedNPtr = &workerSplicedN[workerID]
					}
					var res slabResult
					for pi, pc := range p1.pieces {
						if debugHoles {
							atomic.StoreInt64(&workerPieceIdx[workerID], int64(pi))
							atomic.StoreInt64(&workerPieceN[workerID], int64(len(pc.pts)))
							atomic.StoreInt64(&workerSplicedN[workerID], 0)
							atomic.AddUint64(&phase2Pieces, 1)
							atomic.AddUint64(&phase2CandIters, uint64(len(splice3D)*len(pc.pts)))
							atomic.AddUint64(&phase2VertsIn, uint64(len(pc.pts)))
						}
						before := len(res.verts)
						res.verts, res.faces, res.localCellIdx = appendCellPiece(pc, splice3D, res.verts, res.faces, res.localCellIdx, stepPtr, splicedNPtr)
						if debugHoles {
							atomic.AddUint64(&phase2VertsOut, uint64(len(res.verts)-before))
						}
					}
					results[si] = res
					atomic.AddUint64(&phase2SlabsDone, 1)
					atomic.StoreInt64(&workerSlab[workerID], -1)
				}
			}(w)
		}
		wg.Wait()
	}
	close(stopProgress)
	if debugHoles {
		pieces := atomic.LoadUint64(&phase2Pieces)
		candIters := atomic.LoadUint64(&phase2CandIters)
		vertsIn := atomic.LoadUint64(&phase2VertsIn)
		vertsOut := atomic.LoadUint64(&phase2VertsOut)
		fmt.Fprintf(os.Stderr, "  [hole-report] phase2 elapsed: %.1fs pieces=%d candIters=%d avgCands/edge=%.1f vertsIn=%d vertsOut=%d expansion=%.1fx nMax=%d nPathological=%d\n",
			time.Since(tPhase2).Seconds(),
			pieces, candIters,
			float64(candIters)/math.Max(1, float64(vertsIn)),
			vertsIn, vertsOut,
			float64(vertsOut)/math.Max(1, float64(vertsIn)),
			atomic.LoadUint64(&phase2NMaxSeen),
			atomic.LoadUint64(&phase2NPathological))
	}

	// hole-report: Phase 1 output (per-cell 3D polygons, fan-triangulated
	// just for counting), and Phase 2 output (per-slab triangulated
	// pieces before cross-piece dedup). Position-keyed because both
	// stages have duplicate vertices across cellPieces.
	if debugHoles {
		var phase1Verts [][3]float32
		var phase1Faces [][3]uint32
		var capConvVerts [][3]float32
		var capConvFaces [][3]uint32
		var capNonConvVerts [][3]float32
		var capNonConvFaces [][3]uint32
		var vertVerts [][3]float32
		var vertFaces [][3]uint32
		var convexCells, nonConvexCells int
		// Memoize per-(slab,cell) convexity so we don't pay isConvex
		// once per cellPiece (non-convex cells emit many pieces).
		type sc struct{ si, ci int32 }
		convexCache := make(map[sc]bool)
		for si, p1 := range phase1 {
			for i, pc := range p1.pieces {
				baseAll := uint32(len(phase1Verts))
				phase1Verts = append(phase1Verts, pc.pts...)
				origin := uint8(0)
				if i < len(p1.pieceOrigin) {
					origin = p1.pieceOrigin[i]
				}
				cellConvex := false
				if origin == 0 {
					k := sc{int32(si), pc.cellIdx}
					v, ok := convexCache[k]
					if !ok {
						// Mirror clipSlabPolyToCellPrism3D's dispatch
						// byte-for-byte: reverse to CCW first, then
						// check convexity. Otherwise the census
						// disagrees with which path the production
						// code actually took.
						o := slabs[si].Cells[pc.cellIdx].Outer
						if !isCCW(o) {
							rev := make([]Point2, len(o))
							for j, q := range o {
								rev[len(o)-1-j] = q
							}
							o = rev
						}
						v = isConvex(o)
						convexCache[k] = v
						if v {
							convexCells++
						} else {
							nonConvexCells++
						}
					}
					cellConvex = v
				}
				var baseOrigin uint32
				var emitTo *[][3]uint32
				switch {
				case origin == 1:
					baseOrigin = uint32(len(vertVerts))
					vertVerts = append(vertVerts, pc.pts...)
					emitTo = &vertFaces
				case cellConvex:
					baseOrigin = uint32(len(capConvVerts))
					capConvVerts = append(capConvVerts, pc.pts...)
					emitTo = &capConvFaces
				default:
					baseOrigin = uint32(len(capNonConvVerts))
					capNonConvVerts = append(capNonConvVerts, pc.pts...)
					emitTo = &capNonConvFaces
				}
				for _, tri := range fanTriangulate(len(pc.pts)) {
					phase1Faces = append(phase1Faces, [3]uint32{tri[0] + baseAll, tri[1] + baseAll, tri[2] + baseAll})
					*emitTo = append(*emitTo, [3]uint32{tri[0] + baseOrigin, tri[1] + baseOrigin, tri[2] + baseOrigin})
				}
			}
		}
		reportHolesByPos("phase1 (cellPieces fan-triangulated)", phase1Verts, phase1Faces, slabZSet)
		fmt.Fprintf(os.Stderr, "  [hole-report] cell-convexity census: convex=%d non-convex=%d (cap-path only)\n", convexCells, nonConvexCells)
		reportHolesByPos("phase1 cap convex cells", capConvVerts, capConvFaces, slabZSet)
		reportHolesByPos("phase1 cap non-convex cells", capNonConvVerts, capNonConvFaces, slabZSet)
		reportHolesByPos("phase1 vertical-path only", vertVerts, vertFaces, slabZSet)

		var phase2Verts [][3]float32
		var phase2Faces [][3]uint32
		for _, r := range results {
			base := uint32(len(phase2Verts))
			phase2Verts = append(phase2Verts, r.verts...)
			for _, f := range r.faces {
				phase2Faces = append(phase2Faces, [3]uint32{f[0] + base, f[1] + base, f[2] + base})
			}
		}
		reportHolesByPos("phase2 (post-splice, post-Earcut, pre-dedup)", phase2Verts, phase2Faces, slabZSet)
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
	for si, r := range results {
		if len(r.faces) == 0 {
			continue
		}
		base := uint32(len(cr.Verts))
		off := int32(offsets[si])
		cr.Verts = append(cr.Verts, r.verts...)
		for i, f := range r.faces {
			cr.Faces = append(cr.Faces, [3]uint32{f[0] + base, f[1] + base, f[2] + base})
			cr.FaceCellIdx = append(cr.FaceCellIdx, off+r.localCellIdx[i])
		}
	}

	// Cross-piece vertex dedup. appendCellPiece emits fresh vertex
	// IDs per cell-fragment, so adjacent fragments sharing a boundary
	// vertex (guaranteed coincident by the slab-wide seen3D splice in
	// Clipper integer space) end up with distinct vertex IDs. Without
	// dedup, downstream slicing reads each fragment in isolation and
	// the first-layer cross-section comes out as N disconnected
	// segments → Orca reports "empty initial layer". Dedup by
	// int3DOf (1µm-quantized 3D position) — same key the splice set
	// uses, so coincident-coord verts hash equal. Cross-slab dedup
	// works for free because slabs[k].ZTop and slabs[k+1].ZBot come
	// from the same planes[k+1] float32.
	if len(cr.Verts) > 0 {
		seen := make(map[int3D]uint32, len(cr.Verts)/3)
		remap := make([]uint32, len(cr.Verts))
		// In-place compaction: kept aliases cr.Verts. Safe because
		// len(kept) <= i+1 throughout, so the append's write at
		// kept[len(kept)] never overtakes the range loop's read at
		// cr.Verts[i+1].
		kept := cr.Verts[:0]
		for i, v := range cr.Verts {
			key := int3DOf(v)
			id, ok := seen[key]
			if !ok {
				id = uint32(len(kept))
				seen[key] = id
				kept = append(kept, v)
			}
			remap[i] = id
		}
		cr.Verts = kept
		for i, f := range cr.Faces {
			cr.Faces[i] = [3]uint32{remap[f[0]], remap[f[1]], remap[f[2]]}
		}
	}
	return cr, nil
}

// sliceTriangleToSlab clips triangle (a,b,c) against the half-spaces
// z >= zBot and z <= zTop and returns the resulting planar 3D
// sub-polygon, or nil if the triangle does not overlap the slab.
// The output polygon's vertices stay in the source triangle's plane;
// downstream code chooses how to project to 2D for cell clipping.
func sliceTriangleToSlab(a, b, c [3]float32, zBot, zTop float32) *slabPoly {
	// Drop fully outside.
	zMin := minf3(a[2], b[2], c[2])
	zMax := maxf3(a[2], b[2], c[2])
	if zMax < zBot || zMin > zTop {
		return nil
	}
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
	return &slabPoly{
		Pts:    poly,
		Normal: triangleNormal(a, b, c),
	}
}

// isPolyXYDegenerate reports whether the slab-clipped polygon's XY
// projection has insufficient area for the Clipper-based cap clip
// (which lifts Z from the source plane equation: z = (d - n.x*x -
// n.y*y) / n.z, where n.z is proportional to the XY signed area).
// For polygons that come from a near-vertical source triangle, n.z
// is near zero and the lift is ill-conditioned; route to the
// vertical-scan path instead.
//
// The relative threshold uses max(xRange, yRange)² as the scale, not
// bbox-area, so it survives the axis-aligned case: a triangle on a
// Y=constant or X=constant plane (a flat cube wall) collapses its
// XY bbox to zero area in one dimension, which would otherwise zero
// out a bbox-relative threshold and let float-precision noise (~3e-5
// from shoelace cancellation on a 20-unit polygon) slip past,
// dropping every wall fragment in that slab. Found 2026-05-15 on the
// cube's -Y face.
func isPolyXYDegenerate(pts [][3]float32) bool {
	if len(pts) < 3 {
		return true
	}
	areaXY := polygonXYSignedArea(pts)
	xMin, yMin := pts[0][0], pts[0][1]
	xMax, yMax := xMin, yMin
	for _, p := range pts[1:] {
		if p[0] < xMin {
			xMin = p[0]
		} else if p[0] > xMax {
			xMax = p[0]
		}
		if p[1] < yMin {
			yMin = p[1]
		} else if p[1] > yMax {
			yMax = p[1]
		}
	}
	scale := xMax - xMin
	if yr := yMax - yMin; yr > scale {
		scale = yr
	}
	return absf(areaXY) < 1e-6*scale*scale || absf(areaXY) < 1e-12
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

func polyBoundsP2(pts []Point2) (minX, minY, maxX, maxY float32) {
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
