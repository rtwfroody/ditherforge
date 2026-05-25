package cellslicer

import (
	"runtime"
	"sync"

	"github.com/rtwfroody/ditherforge/internal/cgalbool"
	"github.com/rtwfroody/ditherforge/internal/loader"
)

// ClipResult is the aggregated output of ClipMeshToCells: a single
// concatenated triangle mesh with per-face cell provenance.
//
// FaceCellIdx[i] is the global cell index (matches the flat
// CellSamples order in SampleCells output) of the cell whose prism
// produced face i. Downstream pipeline code maps that to a dithered
// palette index.
//
// The mesh is open (clip_volume=false) — faces are the part of the
// source mesh's surface that falls inside each cell's prism. Output
// vertices are deduplicated across cells by ClipMeshToCells2D (1µm
// Clipper-integer quantization via Quantize). The CGAL-based
// ClipMeshToCells does NOT dedup across cells — its consumers can
// run a coplanar/duplicate merge later if compactness matters.
type ClipResult struct {
	Verts       [][3]float32
	Faces       [][3]uint32
	FaceCellIdx []int32
}

// ClipMeshToCells produces a per-cell-tagged mesh fragment by, for
// each cell in slabs, doing:
//
//	1. Look up candidate triangles whose bbox overlaps the cell's
//	   XY × [zBot, zTop] prism via triIdx.
//	2. Build a small open sub-mesh from those triangles.
//	3. Build the cell's closed prism mesh (BuildPrismMesh).
//	4. CGAL clip_surface(sub_mesh, prism) → fragments inside the cell.
//	5. Append fragments to the result with FaceCellIdx tagged to the
//	   cell's global index.
//
// Per-cell work is run in parallel across runtime.NumCPU() worker
// goroutines; the result is reduced into a single ClipResult in
// deterministic global-cell-index order.
//
// model must be the closed orientable mesh that triIdx was built
// over (typically the alpha-wrapped geometry mesh). Cells with no
// candidate triangles, no overlap with the model, or that CGAL
// reports as empty contribute nothing and are silently skipped.
func ClipMeshToCells(model *loader.LoadedModel, slabs []Slab, triIdx *TriXYZIndex) (ClipResult, error) {
	// Pre-flatten the cell list with global indices so workers can
	// scatter results into a stable array.
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
	globalOffsets := make([]int, len(slabs)+1)
	for si := range slabs {
		globalOffsets[si+1] = globalOffsets[si] + len(slabs[si].Cells)
		for ci := range slabs[si].Cells {
			jobs = append(jobs, job{globalIdx: globalOffsets[si] + ci, slabIdx: si, cellIdx: ci})
		}
	}
	results := make([]result, len(jobs))

	// CGAL's PMP::clip is not reliably safe under concurrent calls
	// (we see SIGSEGV under -p > 1 on real models). Serialize for
	// now; if benchmarks demand parallelism the next move is a
	// worker-process pool over the cgo boundary.
	nWorkers := 1
	_ = runtime.NumCPU
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
				cMinX, cMinY, cMaxX, cMaxY := polyBounds(cell.Outer)
				cands := triIdx.Candidates(cMinX, cMinY, cMaxX, cMaxY, s.ZBot, s.ZTop)
				if len(cands) == 0 {
					continue
				}
				sub := buildSubMesh(model, cands)
				prism := BuildPrismMesh(cell.Outer, s.ZBot, s.ZTop)
				if prism == nil || sub == nil {
					continue
				}
				out, err := cgalbool.ClipSurface(sub, prism)
				if err != nil || out == nil || len(out.Faces) == 0 {
					continue
				}
				results[ji] = result{
					globalIdx: j.globalIdx,
					verts:     out.Vertices,
					faces:     out.Faces,
				}
			}
		}()
	}
	wg.Wait()

	// Reduce: concatenate in deterministic order (input order ==
	// global-cell-index order since we walked slabs/cells).
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

// buildSubMesh extracts a small open mesh containing only the given
// triangle indices from model, with vertices remapped to a dense
// range. Returns nil when tris is empty.
func buildSubMesh(model *loader.LoadedModel, tris []int32) *loader.LoadedModel {
	if len(tris) == 0 {
		return nil
	}
	vmap := make(map[uint32]uint32, len(tris)*3)
	verts := make([][3]float32, 0, len(tris)*3)
	faces := make([][3]uint32, 0, len(tris))
	intern := func(vi uint32) uint32 {
		if j, ok := vmap[vi]; ok {
			return j
		}
		j := uint32(len(verts))
		vmap[vi] = j
		verts = append(verts, model.Vertices[vi])
		return j
	}
	for _, ti := range tris {
		f := model.Faces[ti]
		faces = append(faces, [3]uint32{intern(f[0]), intern(f[1]), intern(f[2])})
	}
	return &loader.LoadedModel{Vertices: verts, Faces: faces}
}
