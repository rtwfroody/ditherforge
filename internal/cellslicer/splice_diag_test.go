// Diagnostic for the cross-slab vertex splice (commit 1aa2f01).
//
// For one Phase-1 run on the building model, walks every shared Z
// plane between adjacent slabs and reports:
//
//   - vertex count in each side's seen3D at that exact int-Z;
//   - intersection / symmetric-diff counts;
//   - of the symmetric-diff orphans, how many lie *strictly interior*
//     to some neighbour cellPiece edge in int64 collinearity space
//     (the same test splicePoly3DEdges uses). After commit 21b7b25 the
//     cap path emits one cellPiece per earcut triangle for non-convex
//     cells, so this set now includes earcut-diagonal edges as well
//     as true cell-boundary edges. That matches what splice actually
//     iterates over, but inflates the "splice CAN fix" count vs the
//     prior diag's cell-boundary-only semantic.
//
// Interpretation:
//
//   - High symmetric-diff with most orphans on-edge → splice should
//     handle these; if T-junctions persist anyway, the bug is in
//     splicePoly3DEdges or Phase 2's iteration over splice3D.
//   - High symmetric-diff with many orphans NOT on-edge → either
//     (a) Z-quantization drift past 1µm dropped vertices out of the
//     planeZ==zb filter, or (b) the two slabs' cells cover different
//     XY spans on the shared plane (actual missing geometry, not a
//     T-junction).
//
// Gated by env var SPLICE_DIAG=1 so it doesn't run in normal CI.
// Caches the geomModel (post-alphawrap, post-decimate) under /tmp so
// re-runs skip the expensive setup.

package cellslicer

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/alphawrap"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

const (
	spliceDiagModelPath = "../../tests/objects/low_poly_building.glb"
	// Cell sizes mirror production for the building model's settings
	// (tests/objects/low_poly_building.json): nozzle 0.4mm × upper
	// scale 1.25 = 0.5mm for upper slabs; nozzle 0.4mm × layer0
	// adhesion scale 2 = 0.8mm for slab 0. Production picks per-slab
	// via cellSizeForSlab(i) in internal/pipeline/run.go; the diag
	// applies the same policy.
	spliceDiagCellSizeUpper  = float32(0.5)
	spliceDiagCellSizeLayer0 = float32(0.8)
	spliceDiagLayerH         = float32(0.2)
	// Bump spliceDiagCacheVersion whenever any cached-pipeline-affecting
	// constant or upstream code (alphawrap.Wrap, voxel.DecimateMesh,
	// loader.ScaleModel, the scale/normalize policy) changes — the gob
	// is path-keyed otherwise and a stale cache would silently feed
	// the diagnostic the wrong mesh.
	spliceDiagCacheVersion = "v2-cs050-lh02-alpha040"
)

// spliceDiagCellSizeForSlab mirrors internal/pipeline/run.go's
// cellSizeForSlab closure: slab 0 uses the layer-0 adhesion scale,
// upper slabs use the regular scale.
func spliceDiagCellSizeForSlab(i int) float32 {
	if i == 0 {
		return spliceDiagCellSizeLayer0
	}
	return spliceDiagCellSizeUpper
}

func spliceDiagCachePath() string {
	return "/tmp/_splice_diag_building_geom_" + spliceDiagCacheVersion + ".gob"
}

func TestSpliceBoundaryDiag(t *testing.T) {
	if os.Getenv("SPLICE_DIAG") == "" {
		t.Skip("set SPLICE_DIAG=1 to run (heavy: loads building, runs alpha-wrap+decimate; cached at " + spliceDiagCachePath() + ")")
	}
	t.Logf("diag params: cacheVersion=%s cellSizeUpper=%.3f cellSizeLayer0=%.3f layerH=%.3f model=%s",
		spliceDiagCacheVersion, spliceDiagCellSizeUpper, spliceDiagCellSizeLayer0, spliceDiagLayerH, spliceDiagModelPath)
	geom := loadOrBuildBuildingGeom(t)
	t.Logf("geom: %d verts, %d faces", len(geom.Vertices), len(geom.Faces))

	// Mirror production's slab/footprint/partition setup (see
	// internal/pipeline/run.go ~ L595-L658). The diag previously used
	// PartitionModel — the old polygon path — which produced
	// different cell shapes (notably more non-convex arcs) than the
	// raster path the pipeline actually uses; orphan counts then
	// reflected the wrong codepath.
	zMin, zMax := modelZRange(geom)
	if zMax <= zMin {
		t.Fatal("degenerate Z range")
	}
	planes := SlabBoundaryPlanes(zMin, zMax, spliceDiagLayerH)
	layers := SliceMesh(geom, planes)
	nSlabs := len(layers) - 1
	if nSlabs < 1 {
		t.Fatal("no slabs produced")
	}
	footprints := make([]*Footprint, nSlabs)
	for i := range footprints {
		footprints[i] = ComputeFootprint(layers[i].Loops, layers[i+1].Loops)
	}
	slabs := make([]Slab, nSlabs)
	for i := 0; i < nSlabs; i++ {
		var fpBelow, fpAbove *Footprint
		if i > 0 {
			fpBelow = footprints[i-1]
		}
		if i+1 < nSlabs {
			fpAbove = footprints[i+1]
		}
		cells, _, _ := PartitionSlabRaster(footprints[i], fpBelow, fpAbove, spliceDiagCellSizeForSlab(i), 0)
		slabs[i] = Slab{
			Index:     i,
			ZBot:      planes[i],
			ZTop:      planes[i+1],
			BotLayer:  &layers[i],
			TopLayer:  &layers[i+1],
			Footprint: footprints[i],
			Cells:     cells,
		}
	}
	t.Logf("partition: %d slabs", len(slabs))

	phase1 := runPhase1ForDiag(geom, slabs)

	// The collinearity check is O(orphans × pieces × edges) per
	// boundary — fine when most cells emit a single cellPiece, but
	// after the cap path switched to per-cell-triangle clipping for
	// non-convex cells (clip2d_subdivide.go:clipSlabPolyToCellPrism3D)
	// the piece count multiplies and a full run on the building model
	// blows past 10 minutes. Opt in with SPLICE_DIAG_CHECK_EDGE=1 when
	// you need that signal; orphan-count totals are always emitted.
	checkEdge := os.Getenv("SPLICE_DIAG_CHECK_EDGE") != ""

	planeZInt := func(z float32) int64 {
		return int64(math.Round(float64(z) * clipperScale))
	}

	type boundaryStat struct {
		si              int
		zPlane          float32
		below, above    int
		intersection    int
		belowOnly       int
		aboveOnly       int
		belowOnlyOnEdge int
		aboveOnlyOnEdge int
	}

	var boundaries []boundaryStat
	for si := 0; si+1 < len(slabs); si++ {
		zi := planeZInt(slabs[si].ZTop)
		if zi != planeZInt(slabs[si+1].ZBot) {
			t.Logf("boundary %d Z mismatch (%d vs %d) — should not happen", si, zi, planeZInt(slabs[si+1].ZBot))
			continue
		}
		below := planeSubset(phase1[si].seen3D, zi)
		above := planeSubset(phase1[si+1].seen3D, zi)
		if len(below) == 0 && len(above) == 0 {
			continue
		}
		bs := boundaryStat{si: si, zPlane: slabs[si].ZTop, below: len(below), above: len(above)}
		for p := range below {
			if _, ok := above[p]; ok {
				bs.intersection++
				continue
			}
			bs.belowOnly++
			if checkEdge && vertexLiesStrictlyOnAnyPlanarEdge(p, phase1[si+1].pieces, zi) {
				bs.belowOnlyOnEdge++
			}
		}
		for p := range above {
			if _, ok := below[p]; ok {
				continue
			}
			bs.aboveOnly++
			if checkEdge && vertexLiesStrictlyOnAnyPlanarEdge(p, phase1[si].pieces, zi) {
				bs.aboveOnlyOnEdge++
			}
		}
		boundaries = append(boundaries, bs)
	}

	var totBelow, totAbove, totIsec, totBOnly, totAOnly, totBOnEdge, totAOnEdge int
	for _, b := range boundaries {
		totBelow += b.below
		totAbove += b.above
		totIsec += b.intersection
		totBOnly += b.belowOnly
		totAOnly += b.aboveOnly
		totBOnEdge += b.belowOnlyOnEdge
		totAOnEdge += b.aboveOnlyOnEdge
	}
	totOrphans := totBOnly + totAOnly
	totSplicable := totBOnEdge + totAOnEdge
	t.Logf("=== aggregate over %d non-empty boundaries ===", len(boundaries))
	t.Logf("  total vertices on shared planes: below=%d above=%d intersection=%d", totBelow, totAbove, totIsec)
	t.Logf("  orphans (symmetric diff):        %d  (belowOnly=%d aboveOnly=%d)", totOrphans, totBOnly, totAOnly)
	if checkEdge {
		t.Logf("  orphans collinear with neighbour edge (splice CAN fix):  %d", totSplicable)
		t.Logf("  orphans NOT on any neighbour edge (splice CANNOT fix):   %d", totOrphans-totSplicable)
	} else {
		t.Logf("  (per-orphan collinearity check skipped; set SPLICE_DIAG_CHECK_EDGE=1 to enable, but expect minutes on large models)")
	}

	sort.Slice(boundaries, func(i, j int) bool {
		oi := boundaries[i].belowOnly + boundaries[i].aboveOnly
		oj := boundaries[j].belowOnly + boundaries[j].aboveOnly
		return oi > oj
	})
	n := 10
	if n > len(boundaries) {
		n = len(boundaries)
	}
	t.Logf("=== worst %d boundaries by orphan count ===", n)
	t.Logf("  si    zPlane    below  above  isect  bOnly(onEdge)  aOnly(onEdge)")
	for i := 0; i < n; i++ {
		b := boundaries[i]
		t.Logf("  %4d  %7.4f  %5d  %5d  %5d  %5d(%5d)   %5d(%5d)",
			b.si, b.zPlane, b.below, b.above, b.intersection,
			b.belowOnly, b.belowOnlyOnEdge, b.aboveOnly, b.aboveOnlyOnEdge)
	}

	if len(boundaries) > 0 {
		b := boundaries[0]
		t.Logf("=== chain dump for worst boundary si=%d z=%.4f ===", b.si, b.zPlane)
		dumpPlaneChains(t, phase1[b.si].seen3D, phase1[b.si+1].seen3D, planeZInt(b.zPlane))
	}
}

func planeSubset(seen map[int3D]struct{}, zi int64) map[int3D]struct{} {
	out := make(map[int3D]struct{})
	for p := range seen {
		if p.Z == zi {
			out[p] = struct{}{}
		}
	}
	return out
}

// vertexLiesStrictlyOnAnyPlanarEdge mirrors the int64 collinearity test
// in splicePoly3DEdges, but restricted to edges whose endpoints both
// lie on the shared Z plane (so we only test against the edges splice
// would consider on this boundary).
func vertexLiesStrictlyOnAnyPlanarEdge(p int3D, pieces []cellPiece, planeZ int64) bool {
	for _, pc := range pieces {
		n := len(pc.pts)
		if n < 2 {
			continue
		}
		var aPrev int3D
		aPrev = Quantize(pc.pts[n-1])
		for i := 0; i < n; i++ {
			b := Quantize(pc.pts[i])
			a := aPrev
			aPrev = b
			if a.Z != planeZ || b.Z != planeZ {
				continue
			}
			if a == p || b == p {
				continue
			}
			bx := b.X - a.X
			by := b.Y - a.Y
			bz := b.Z - a.Z
			ab2 := bx*bx + by*by + bz*bz
			if ab2 == 0 {
				continue
			}
			px := p.X - a.X
			py := p.Y - a.Y
			pz := p.Z - a.Z
			cx := py*bz - pz*by
			cy := pz*bx - px*bz
			cz := px*by - py*bx
			if cx != 0 || cy != 0 || cz != 0 {
				continue
			}
			t := px*bx + py*by + pz*bz
			if t > 0 && t < ab2 {
				return true
			}
		}
	}
	return false
}

func dumpPlaneChains(t *testing.T, below, above map[int3D]struct{}, planeZ int64) {
	type xy struct{ X, Y int64 }
	collect := func(m map[int3D]struct{}) []xy {
		var out []xy
		for p := range m {
			if p.Z == planeZ {
				out = append(out, xy{p.X, p.Y})
			}
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].X != out[j].X {
				return out[i].X < out[j].X
			}
			return out[i].Y < out[j].Y
		})
		return out
	}
	belowXY := collect(below)
	aboveXY := collect(above)
	belowSet := make(map[xy]bool, len(belowXY))
	for _, p := range belowXY {
		belowSet[p] = true
	}
	aboveSet := make(map[xy]bool, len(aboveXY))
	for _, p := range aboveXY {
		aboveSet[p] = true
	}
	tag := func(p xy, otherHas map[xy]bool) string {
		if otherHas[p] {
			return "  "
		}
		return "* "
	}
	limit := 60
	t.Logf("below (%d total; * = not in above):", len(belowXY))
	for i, p := range belowXY {
		if i >= limit {
			t.Logf("  ... (%d more)", len(belowXY)-i)
			break
		}
		t.Logf("  %s[%4d] (%.4f, %.4f)", tag(p, aboveSet), i, float64(p.X)/clipperScale, float64(p.Y)/clipperScale)
	}
	t.Logf("above (%d total; * = not in below):", len(aboveXY))
	for i, p := range aboveXY {
		if i >= limit {
			t.Logf("  ... (%d more)", len(aboveXY)-i)
			break
		}
		t.Logf("  %s[%4d] (%.4f, %.4f)", tag(p, belowSet), i, float64(p.X)/clipperScale, float64(p.Y)/clipperScale)
	}
}

type slabPhase1Diag struct {
	pieces []cellPiece
	seen3D map[int3D]struct{}
}

// runPhase1ForDiag mirrors Phase 1 of ClipMeshToCells2D, fanned out
// across runtime.NumCPU() workers (same parallelism as production —
// running sequentially on the building model takes >30 minutes vs ~3
// in parallel, since slabs are independent in Phase 1).
//
// WARNING — load-bearing mirror: any change to Phase 1 in clip2d.go
// (slabPoly pre-slice loop, cell-index build, clipPolyToCells call
// signature, seen3D semantics) MUST be reflected here or this
// diagnostic silently reports against a stale algorithm. Keep the
// two in lockstep until/unless Phase 1 is extracted into a shared
// helper that both production and this test can call.
func runPhase1ForDiag(model *loader.LoadedModel, slabs []Slab) []slabPhase1Diag {
	slabPolys := make([][]slabPoly, len(slabs))
	for ti := range model.Faces {
		f := model.Faces[ti]
		a := model.Vertices[f[0]]
		b := model.Vertices[f[1]]
		c := model.Vertices[f[2]]
		zMin := minf3(a[2], b[2], c[2])
		zMax := maxf3(a[2], b[2], c[2])
		siLo, siHi := 0, len(slabs)-1
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

	cellIndices := make([]*slabCellIndex, len(slabs))
	for si := range slabs {
		if len(slabs[si].Cells) > 0 {
			cellIndices[si] = buildSlabCellIndex(&slabs[si])
		}
	}

	out := make([]slabPhase1Diag, len(slabs))
	nWorkers := runtime.NumCPU()
	if nWorkers > len(slabs) {
		nWorkers = len(slabs)
	}
	if nWorkers < 1 {
		nWorkers = 1
	}
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
				for _, p := range slabPolys[si] {
					pieces, candidates = clipPolyToCells(p, si, slabs, idx, pieces, seen3D, candidates)
				}
				out[si] = slabPhase1Diag{pieces: pieces, seen3D: seen3D}
			}
		}()
	}
	wg.Wait()
	return out
}

func loadOrBuildBuildingGeom(t *testing.T) *loader.LoadedModel {
	if data, err := os.ReadFile(spliceDiagCachePath()); err == nil {
		var m loader.LoadedModel
		if derr := m.GobDecode(data); derr == nil {
			t.Logf("loaded cached geom model from %s", spliceDiagCachePath())
			return &m
		} else {
			t.Logf("cache decode failed (%v); rebuilding", derr)
		}
	}

	abs, err := filepath.Abs(spliceDiagModelPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("building geom model from %s (this may take a few minutes)", abs)
	raw, err := loader.LoadGLB(abs, -1)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	model := loader.CloneForEdit(raw)
	scale := float32(1000)
	ext := modelMaxExtentForDiag(model) * scale
	totalScale := scale * (50 / ext)
	loader.ScaleModel(model, totalScale)
	normalizeZForDiag(model)

	// Mirror production's decimate budget: pipeline/run.go's
	// stage_decimate uses voxelCellSizes(opts).UpperXY, not the
	// slab-0 size, for the budget that gates DecimateMesh.
	cellSize := spliceDiagCellSizeUpper
	budget := float64(cellSize/2) * float64(cellSize/2)

	preDec, err := voxel.DecimateMesh(context.Background(), model, 1, cellSize, budget, false, progress.NullTracker{})
	if err != nil {
		t.Fatalf("pre-decimate: %v", err)
	}
	alpha := float32(0.4)
	offset := alpha / 30
	wrapped, err := alphawrap.Wrap(preDec, alpha, offset)
	if err != nil {
		t.Fatalf("alpha-wrap: %v", err)
	}
	postDec, err := voxel.DecimateMesh(context.Background(), wrapped, 1, cellSize, budget, false, progress.NullTracker{})
	if err != nil {
		t.Fatalf("post-decimate: %v", err)
	}
	stageDec, err := voxel.DecimateMesh(context.Background(), postDec, 1, cellSize, budget, false, progress.NullTracker{})
	if err != nil {
		t.Fatalf("stage-decimate: %v", err)
	}

	if data, eerr := stageDec.GobEncode(); eerr == nil {
		if werr := os.WriteFile(spliceDiagCachePath(), data, 0o644); werr == nil {
			t.Logf("cached geom model at %s (%d bytes)", spliceDiagCachePath(), len(data))
		} else {
			t.Logf("cache write failed: %v", werr)
		}
	} else {
		t.Logf("cache encode failed: %v", eerr)
	}
	return stageDec
}

func modelMaxExtentForDiag(m *loader.LoadedModel) float32 {
	if len(m.Vertices) == 0 {
		return 0
	}
	mn, mx := m.Vertices[0], m.Vertices[0]
	for _, v := range m.Vertices[1:] {
		for k := 0; k < 3; k++ {
			if v[k] < mn[k] {
				mn[k] = v[k]
			}
			if v[k] > mx[k] {
				mx[k] = v[k]
			}
		}
	}
	e := mx[0] - mn[0]
	if y := mx[1] - mn[1]; y > e {
		e = y
	}
	if z := mx[2] - mn[2]; z > e {
		e = z
	}
	return e
}

func normalizeZForDiag(m *loader.LoadedModel) {
	if len(m.Vertices) == 0 {
		return
	}
	mn := m.Vertices[0][2]
	for _, v := range m.Vertices[1:] {
		if v[2] < mn {
			mn = v[2]
		}
	}
	for i := range m.Vertices {
		m.Vertices[i][2] -= mn
	}
}
