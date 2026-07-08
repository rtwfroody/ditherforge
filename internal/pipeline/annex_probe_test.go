package pipeline

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"sort"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/cellslicer"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// TestAnnexProbe is a throwaway diagnostic: it loads the building, builds
// a spatial index over the color model, and produces a top-down map of
// the topmost surface color + Z. Run with:
//
//	DF_ANNEX_PROBE=/tmp/probe go test ./internal/pipeline/ -run TestAnnexProbe -v
func TestAnnexProbe(t *testing.T) {
	out := os.Getenv("DF_ANNEX_PROBE")
	if out == "" {
		t.Skip("set DF_ANNEX_PROBE=/dir to run")
	}
	_ = os.MkdirAll(out, 0o755)

	size := float32(50)
	opts := Options{
		Input:          "../../tests/objects/low_poly_building.glb",
		ObjectIndex:    -1,
		NumColors:      6,
		NozzleDiameter: 0.4,
		LayerHeight:    0.2,
		Dither:         "riemersma",
		Force:          true,
		Scale:          1,
		Size:           &size,
		MeshRepair:     RepairAlphaWrap,
	}
	r := &pipelineRun{ctx: context.Background(), cache: NewStageCache(), opts: opts, tracker: progress.NullTracker{}}
	lo, err := r.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cm := lo.ColorModel
	wrapped := lo.Model

	// Bounds from the color model.
	var minX, minY, maxX, maxY, maxZ float32
	minX, minY = cm.Vertices[0][0], cm.Vertices[0][1]
	maxX, maxY = minX, minY
	for _, v := range cm.Vertices {
		if v[0] < minX {
			minX = v[0]
		}
		if v[0] > maxX {
			maxX = v[0]
		}
		if v[1] < minY {
			minY = v[1]
		}
		if v[1] > maxY {
			maxY = v[1]
		}
		if v[2] > maxZ {
			maxZ = v[2]
		}
	}
	t.Logf("color bounds X[%.1f,%.1f] Y[%.1f,%.1f] maxZ=%.1f", minX, minY, maxX, maxY, maxZ)

	siColor := voxel.NewSpatialIndex(cm, 0.4)
	siWrap := voxel.NewSpatialIndex(wrapped, 0.4)
	bufC := voxel.NewSearchBuf(len(cm.Faces))
	bufW := voxel.NewSearchBuf(len(wrapped.Faces))

	const N = 256
	colImg := image.NewRGBA(image.Rect(0, 0, N, N))
	zImg := image.NewRGBA(image.Rect(0, 0, N, N))
	wzImg := image.NewRGBA(image.Rect(0, 0, N, N))

	// topmost surface: scan Z from maxZ down; first XY-near hit with alpha.
	topHit := func(si *voxel.SpatialIndex, mdl interface{}, buf *voxel.SearchBuf, x, y float32, useColor bool) (float32, [4]uint8, bool) {
		for z := maxZ + 1; z >= -1; z -= 0.25 {
			var rgba [4]uint8
			if useColor {
				rgba = voxel.SampleNearestColor([3]float32{x, y, z}, cm, si, 0.25, buf, nil, nil)
			} else {
				rgba = voxel.SampleNearestColor([3]float32{x, y, z}, wrapped, si, 0.25, buf, nil, nil)
			}
			// SampleNearestColor returns exactly {128,128,128,255} when no
			// triangle is within the Z-bounded radius — treat that as a miss.
			if rgba[3] >= 128 && !(rgba[0] == 128 && rgba[1] == 128 && rgba[2] == 128) {
				return z, rgba, true
			}
		}
		return 0, [4]uint8{}, false
	}

	for j := 0; j < N; j++ {
		for i := 0; i < N; i++ {
			x := minX + (float32(i)+0.5)/N*(maxX-minX)
			y := maxY - (float32(j)+0.5)/N*(maxY-minY) // flip so +Y is up
			z, rgba, ok := topHit(siColor, nil, bufC, x, y, true)
			if ok {
				colImg.Set(i, j, color.RGBA{rgba[0], rgba[1], rgba[2], 255})
				zg := uint8(z / maxZ * 255)
				zImg.Set(i, j, color.RGBA{zg, zg, zg, 255})
			}
			zw, _, okw := topHit(siWrap, nil, bufW, x, y, false)
			if okw {
				zg := uint8(zw / maxZ * 255)
				wzImg.Set(i, j, color.RGBA{zg, zg, zg, 255})
			}
		}
	}
	// Z-profile along a vertical scan line (varying Y at fixed X) through
	// the annex, comparing original topmost Z vs wrapped topmost Z. Reveals
	// whether the wrap sealed/raised the annex roof.
	xMid := (minX + maxX) / 2
	t.Logf("Z-profile at X=%.1f (Y top->bottom): origZ / wrapZ", xMid)
	for j := 0; j < N; j += 6 {
		y := maxY - (float32(j)+0.5)/N*(maxY-minY)
		x := xMid
		zo, co, oko := topHit(siColor, nil, bufC, x, y, true)
		zw, _, okw := topHit(siWrap, nil, bufW, x, y, false)
		t.Logf("  Y=%6.1f  orig=%5.1f(ok=%v) wrap=%5.1f(ok=%v)  origColor=(%3d,%3d,%3d)",
			y, zo, oko, zw, okw, co[0], co[1], co[2])
	}

	writePNGFile(t, out+"/probe_color.png", colImg)
	writePNGFile(t, out+"/probe_origZ.png", zImg)
	writePNGFile(t, out+"/probe_wrapZ.png", wzImg)

	// Now run the actual pipeline Voxelize and paint each cell sample at
	// its centroid into a top-down image (keep the highest-Z sample per
	// pixel = topmost, matching the probe). Diff against probe_color to
	// localize where the pipeline diverges from the ideal topmost color.
	vo, err := r.Voxelize()
	if err != nil {
		t.Fatalf("Voxelize: %v", err)
	}
	pipeImg := image.NewRGBA(image.Rect(0, 0, N, N))
	pipeZ := make([]float32, N*N)
	for k := range pipeZ {
		pipeZ[k] = -1e9
	}
	for gi := range vo.CellSamples {
		s := &vo.CellSamples[gi]
		if !s.Alpha {
			continue
		}
		sl := &vo.CellSlabs[s.SlabIdx]
		if s.CellIdx >= len(sl.Cells) || sl.Cells[s.CellIdx].Kind != 1 { // hex caps only
			continue
		}
		x, y, z := s.Centroid[0], s.Centroid[1], s.Centroid[2]
		ci := int((x - minX) / (maxX - minX) * N)
		cj := int((maxY - y) / (maxY - minY) * N)
		for dj := -1; dj <= 1; dj++ { // 3x3 brush so sparse caps fill in
			for di := -1; di <= 1; di++ {
				i, j := ci+di, cj+dj
				if i < 0 || i >= N || j < 0 || j >= N {
					continue
				}
				idx := j*N + i
				if z > pipeZ[idx] {
					pipeZ[idx] = z
					pipeImg.Set(i, j, color.RGBA{s.Color[0], s.Color[1], s.Color[2], 255})
				}
			}
		}
	}
	// Hole map: for each pixel, the original topmost surface Z (origZ
	// from earlier scan) vs the highest cap-sample Z at that pixel
	// (pipeZ, hex-only). A pixel with a real top surface but whose
	// highest cap sits >1mm below it (or has no cap at all) is an
	// uncapped roof → a hole in the top view. Paint:
	//   green  = capped within 1mm of the top
	//   red    = top surface exists but no cap within 1mm (HOLE)
	//   black  = no surface
	holeImg := image.NewRGBA(image.Rect(0, 0, N, N))
	// recompute hex-only highest cap Z per pixel.
	capZpix := make([]float32, N*N)
	for k := range capZpix {
		capZpix[k] = -1e9
	}
	for gi := range vo.CellSamples {
		s := &vo.CellSamples[gi]
		if !s.Alpha {
			continue
		}
		sl := &vo.CellSlabs[s.SlabIdx]
		if s.CellIdx >= len(sl.Cells) || sl.Cells[s.CellIdx].Kind != 1 {
			continue
		}
		i := int((s.Centroid[0] - minX) / (maxX - minX) * N)
		j := int((maxY - s.Centroid[1]) / (maxY - minY) * N)
		if i < 0 || i >= N || j < 0 || j >= N {
			continue
		}
		idx := j*N + i
		if s.Centroid[2] > capZpix[idx] {
			capZpix[idx] = s.Centroid[2]
		}
	}
	nHole := 0
	for j := 0; j < N; j++ {
		for i := 0; i < N; i++ {
			x := minX + (float32(i)+0.5)/N*(maxX-minX)
			y := maxY - (float32(j)+0.5)/N*(maxY-minY)
			topZ, _, ok := topHit(siColor, nil, bufC, x, y, true)
			if !ok {
				continue
			}
			cz := capZpix[j*N+i]
			if cz > topZ-1.2 {
				holeImg.Set(i, j, color.RGBA{0, 180, 0, 255})
			} else {
				holeImg.Set(i, j, color.RGBA{220, 0, 0, 255})
				nHole++
			}
		}
	}
	writePNGFile(t, out+"/hole_map.png", holeImg)
	t.Logf("hole-map: %d/%d pixels uncapped (top surface but no cap within 1.2mm)", nHole, N*N)

	writePNGFile(t, out+"/pipe_color.png", pipeImg)

	// Per-cap-cell error: compare each cap (hex) sample to the ideal
	// topmost-surface color at its centroid XY. Report cells where the
	// pipeline sampled far from the topmost color, with the Z gap between
	// the cap (slab) Z and the true topmost surface Z.
	type badCell struct {
		x, y, capZ, topZ, dz float32
		got, want            [3]uint8
		dErr                 int
	}
	var bad []badCell
	nCap, nBad := 0, 0
	for gi := range vo.CellSamples {
		s := &vo.CellSamples[gi]
		if !s.Alpha {
			continue
		}
		sl := &vo.CellSlabs[s.SlabIdx]
		if s.CellIdx >= len(sl.Cells) || sl.Cells[s.CellIdx].Kind != 1 { // KindHex==1
			continue
		}
		nCap++
		topZ, rgba, ok := topHit(siColor, nil, bufC, s.Centroid[0], s.Centroid[1], true)
		if !ok {
			continue
		}
		dr := abs8(s.Color[0], rgba[0]) + abs8(s.Color[1], rgba[1]) + abs8(s.Color[2], rgba[2])
		// Only flag caps that sit AT the topmost surface (so the
		// comparison is apples-to-apples) but still sampled a far-off
		// colour — those are real roof-sampling bugs, not interior floors.
		if dr > 120 && s.Centroid[2] >= topZ-1.5 {
			nBad++
			if len(bad) < 40 {
				bad = append(bad, badCell{
					x: s.Centroid[0], y: s.Centroid[1], capZ: s.Centroid[2], topZ: topZ,
					dz: s.Centroid[2] - topZ, got: s.Color, want: [3]uint8{rgba[0], rgba[1], rgba[2]}, dErr: dr,
				})
			}
		}
	}
	t.Logf("cap cells: %d total, %d with color error >120 vs ideal topmost", nCap, nBad)
	for _, b := range bad {
		t.Logf("  BAD cap (%6.2f,%6.2f) capZ=%5.2f topZ=%5.2f dz=%+5.2f got=(%3d,%3d,%3d) want=(%3d,%3d,%3d)",
			b.x, b.y, b.capZ, b.topZ, b.dz, b.got[0], b.got[1], b.got[2], b.want[0], b.want[1], b.want[2])
	}
	t.Logf("wrote probe_color/origZ/wrapZ + pipe_color to %s", out)

	// === Slab-vs-roof analysis (tests "roof falls between slab boundaries") ===
	// Slab Z structure near the annex roof (annex top ~ Z15.5-17).
	t.Logf("--- slab Z structure (slabs with ZBot in [10,20]) ---")
	for si := range vo.CellSlabs {
		sl := &vo.CellSlabs[si]
		if sl.ZBot < 10 || sl.ZBot > 20 {
			continue
		}
		// count hex caps in the annex region (Y<-9) on this slab
		nHex := 0
		for ci := range sl.Cells {
			if sl.Cells[ci].Kind != 1 {
				continue
			}
			nHex++
		}
		t.Logf("  slab %d ZBot=%6.3f ZTop=%6.3f mid=%6.3f span=%.3f hexCells=%d",
			sl.Index, sl.ZBot, sl.ZTop, 0.5*(sl.ZBot+sl.ZTop), sl.ZTop-sl.ZBot, nHex)
	}

	// Per-probe-point: true roof Z, the slab that brackets it, and whether
	// that slab (or any slab) has a hex cap within cellSize of the XY.
	annexProbes := [][2]float32{
		{-21, -12.5}, {-15, -12.5}, {-10, -12.5}, // grey field Z~15.5
		{-15, -16.5}, {-10, -16.5}, // teal field Z~17
		{-21, -21.8}, {-10, -21.8}, // cream cornice Z~16
	}
	t.Logf("--- annex roof probe points: roof Z vs nearest hex cap ---")
	for _, p := range annexProbes {
		px, py := p[0], p[1]
		topZ, rgba, ok := topHit(siColor, nil, bufC, px, py, true)
		if !ok {
			t.Logf("  (%6.2f,%6.2f) no surface", px, py)
			continue
		}
		// bracketing slab
		bracket := -1
		for si := range vo.CellSlabs {
			sl := &vo.CellSlabs[si]
			if topZ >= sl.ZBot && topZ < sl.ZTop {
				bracket = sl.Index
				break
			}
		}
		// nearest hex cap (any slab) within 1.5mm XY
		var bestD float32 = 1e9
		bestZ := float32(0)
		var bestCol [3]uint8
		bestSlab := -1
		for gi := range vo.CellSamples {
			s := &vo.CellSamples[gi]
			if !s.Alpha {
				continue
			}
			sl := &vo.CellSlabs[s.SlabIdx]
			if s.CellIdx >= len(sl.Cells) || sl.Cells[s.CellIdx].Kind != 1 {
				continue
			}
			dx := s.Centroid[0] - px
			dy := s.Centroid[1] - py
			d := dx*dx + dy*dy
			if d < bestD {
				bestD = d
				bestZ = s.Centroid[2]
				bestCol = s.Color
				bestSlab = sl.Index
			}
		}
		dist := float32(math.Sqrt(float64(bestD)))
		var bz, bt float32
		if bracket >= 0 {
			bz = vo.CellSlabs[bracket].ZBot
			bt = vo.CellSlabs[bracket].ZTop
		}
		t.Logf("  (%6.2f,%6.2f) roofZ=%5.2f want=(%3d,%3d,%3d) | bracketSlab=%d[%.2f,%.2f] | nearestHex slab=%d capZ=%5.2f dXY=%.2f dz=%+.2f got=(%3d,%3d,%3d)",
			px, py, topZ, rgba[0], rgba[1], rgba[2], bracket, bz, bt,
			bestSlab, bestZ, dist, bestZ-topZ, bestCol[0], bestCol[1], bestCol[2])
	}

	// Enumerate EVERY hex cap within 1.0mm XY of two annex points, sorted
	// by slab, to see which slab/Z actually caps that spot (if any).
	enumPts := [][2]float32{{-10, -12.5}, {-10, -21.8}}
	for _, p := range enumPts {
		px, py := p[0], p[1]
		topZ, rgba, _ := topHit(siColor, nil, bufC, px, py, true)
		t.Logf("--- hex caps within 1.0mm of (%.2f,%.2f) roofZ=%.2f want=(%d,%d,%d) ---", px, py, topZ, rgba[0], rgba[1], rgba[2])
		type hit struct {
			slab int
			z, d float32
			col  [3]uint8
		}
		var hits []hit
		for gi := range vo.CellSamples {
			s := &vo.CellSamples[gi]
			if !s.Alpha {
				continue
			}
			sl := &vo.CellSlabs[s.SlabIdx]
			if s.CellIdx >= len(sl.Cells) || sl.Cells[s.CellIdx].Kind != 1 {
				continue
			}
			dx := s.Centroid[0] - px
			dy := s.Centroid[1] - py
			d := float32(math.Sqrt(float64(dx*dx + dy*dy)))
			if d > 1.0 {
				continue
			}
			hits = append(hits, hit{sl.Index, s.Centroid[2], d, s.Color})
		}
		// bucket by slab
		bySlab := map[int]int{}
		minZ, maxZ := float32(1e9), float32(-1e9)
		for _, h := range hits {
			bySlab[h.slab]++
			if h.z < minZ {
				minZ = h.z
			}
			if h.z > maxZ {
				maxZ = h.z
			}
		}
		t.Logf("    %d caps total, Z range [%.2f,%.2f]", len(hits), minZ, maxZ)
		for _, h := range hits {
			t.Logf("    slab=%d capZ=%.2f dXY=%.2f col=(%d,%d,%d)", h.slab, h.z, h.d, h.col[0], h.col[1], h.col[2])
		}
	}

	// === Replicate slab 77's region algebra on the wrapped mesh ===
	// Reconstruct the exact slab boundary planes from vo.CellSlabs, reslice
	// the wrapped geometry, and walk the PartitionSlabAnalytic algebra for
	// slabs around the annex roof, testing Contains() at the field point.
	nSlabs := len(vo.CellSlabs)
	planes := make([]float32, nSlabs+1)
	for i := range vo.CellSlabs {
		planes[vo.CellSlabs[i].Index] = vo.CellSlabs[i].ZBot
	}
	planes[nSlabs] = vo.CellSlabs[nSlabs-1].ZTop
	layers := cellslicer.SliceMesh(wrapped, planes)
	fps := make([]*cellslicer.Footprint, nSlabs)
	for i := 0; i < nSlabs; i++ {
		fps[i] = cellslicer.ComputeFootprint(layers[i].Loops, layers[i+1].Loops)
	}
	fieldX, fieldY := float32(0.8), float32(-12.5)
	t.Logf("=== geometry probe at field (%.1f,%.1f) ===", fieldX, fieldY)
	// Slice the ORIGINAL color model at the same planes for comparison.
	olayers := cellslicer.SliceMesh(cm, planes)
	ofps := make([]*cellslicer.Footprint, nSlabs)
	for i := 0; i < nSlabs; i++ {
		ofps[i] = cellslicer.ComputeFootprint(olayers[i].Loops, olayers[i+1].Loops)
	}
	// Full vertical structure of the WRAPPED vs ORIGINAL footprint at the
	// field XY: list Z-intervals where each slab footprint contains it.
	report := func(tag string, arr []*cellslicer.Footprint) {
		prev := false
		any := false
		for i := 0; i < nSlabs; i++ {
			c := arr[i] != nil && arr[i].Contains(fieldX, fieldY)
			if c != prev {
				if c {
					t.Logf("  %s CONTAINS field from Z=%.2f", tag, planes[i])
					any = true
				} else {
					t.Logf("  %s STOPS containing field at Z=%.2f", tag, planes[i])
				}
				prev = c
			}
		}
		if prev {
			t.Logf("  %s contains field up to top Z=%.2f", tag, planes[nSlabs])
		}
		if !any {
			t.Logf("  %s NEVER contains field at any Z", tag)
		}
	}
	report("wrapped", fps)
	report("original", ofps)
	// Sanity: a point deep in the MAIN building (caps fine) must be solid.
	for _, sp := range [][2]float32{{0, 5}, {0, 10}, {-5, 0}, {5, 0}} {
		nSolid := 0
		for i := 0; i < nSlabs; i++ {
			if fps[i] != nil && fps[i].Contains(sp[0], sp[1]) {
				nSolid++
			}
		}
		t.Logf("  SANITY main pt (%.1f,%.1f): wrapped footprint present in %d/%d slabs", sp[0], sp[1], nSolid, nSlabs)
	}
	// Raw triangle dump: every face whose centroid is within 1.5mm XY of
	// the field point, with its Z range — ground truth for what geometry
	// exists there in each mesh.
	dumpTris := func(tag string, mdl *loader.LoadedModel) {
		var zmin, zmax float32 = 1e9, -1e9
		n := 0
		// also bucket by whether the triangle is near-horizontal
		nHoriz := 0
		for _, f := range mdl.Faces {
			a := mdl.Vertices[f[0]]
			b := mdl.Vertices[f[1]]
			c := mdl.Vertices[f[2]]
			cx := (a[0] + b[0] + c[0]) / 3
			cy := (a[1] + b[1] + c[1]) / 3
			dx := cx - fieldX
			dy := cy - fieldY
			if dx*dx+dy*dy > 1.5*1.5 {
				continue
			}
			n++
			tzmin := minf(a[2], minf(b[2], c[2]))
			tzmax := maxf(a[2], maxf(b[2], c[2]))
			if tzmin < zmin {
				zmin = tzmin
			}
			if tzmax > zmax {
				zmax = tzmax
			}
			if tzmax-tzmin < 0.05 { // near-horizontal
				nHoriz++
			}
		}
		t.Logf("  %s: %d tris within 1.5mm of field, Z[%.2f,%.2f], %d near-horizontal", tag, n, zmin, zmax, nHoriz)
	}
	dumpTris("original", cm)
	dumpTris("wrapped", wrapped)

	// Vertical ray through the field point: list every triangle whose XY
	// projection contains (fieldX,fieldY), with the Z of the triangle's
	// plane at that point. Sorted, these are the surface crossings; for a
	// watertight solid they alternate enter/exit, so consecutive pairs are
	// solid Z-intervals. This is ground truth for "is there a thin solid
	// plate here after alpha-wrap".
	rayZ := func(tag string, mdl *loader.LoadedModel, px, py float32) {
		var zs []float64
		for _, f := range mdl.Faces {
			a := mdl.Vertices[f[0]]
			b := mdl.Vertices[f[1]]
			c := mdl.Vertices[f[2]]
			// barycentric point-in-triangle in XY
			d := float64((b[1]-c[1])*(a[0]-c[0]) + (c[0]-b[0])*(a[1]-c[1]))
			if d == 0 {
				continue
			}
			l1 := float64((b[1]-c[1])*(px-c[0])+(c[0]-b[0])*(py-c[1])) / d
			l2 := float64((c[1]-a[1])*(px-c[0])+(a[0]-c[0])*(py-c[1])) / d
			l3 := 1 - l1 - l2
			if l1 < 0 || l2 < 0 || l3 < 0 {
				continue
			}
			z := l1*float64(a[2]) + l2*float64(b[2]) + l3*float64(c[2])
			zs = append(zs, z)
		}
		sort.Float64s(zs)
		t.Logf("  %s ray crossings at (%.2f,%.2f): n=%d %v", tag, px, py, len(zs), fmtZs(zs))
	}
	rayZ("wrapped courtyard", wrapped, fieldX, fieldY)
	rayZ("original courtyard", cm, fieldX, fieldY)
	rayZ("wrapped main", wrapped, 0, 5)
	rayZ("wrapped main2", wrapped, -5, 0)

	// === projectXY load measurement ===
	// For each triangle, find the slab fully containing its Z-range (i.e.
	// the triangles the bounding-plane slices miss). Count them per slab,
	// and how many are near-horizontal (the ones we'd actually project).
	pl64 := make([]float64, len(planes))
	for i, p := range planes {
		pl64[i] = float64(p)
	}
	slabOf := func(z float64) int { // slab whose [planes[k],planes[k+1]] contains z
		k := sort.SearchFloat64s(pl64, z) - 1
		return k
	}
	interiorPerSlab := make([]int, nSlabs)
	horizPerSlab := make([]int, nSlabs)
	totalInterior, totalHoriz := 0, 0
	for _, f := range wrapped.Faces {
		a := wrapped.Vertices[f[0]]
		b := wrapped.Vertices[f[1]]
		c := wrapped.Vertices[f[2]]
		zmin := float64(minf(a[2], minf(b[2], c[2])))
		zmax := float64(maxf(a[2], maxf(b[2], c[2])))
		ks, ke := slabOf(zmin), slabOf(zmax)
		if ks != ke || ks < 0 || ks >= nSlabs {
			continue // crosses a plane (already in contour) or out of range
		}
		totalInterior++
		interiorPerSlab[ks]++
		// near-horizontal: |unit normal .z|
		ux, uy, uz := triNormal(a, b, c)
		_ = ux
		_ = uy
		if uz > 0.9 || uz < -0.9 {
			totalHoriz++
			horizPerSlab[ks]++
		}
	}
	maxI, maxIslab, maxH, maxHslab := 0, -1, 0, -1
	nSlabsWithHoriz := 0
	for i := 0; i < nSlabs; i++ {
		if interiorPerSlab[i] > maxI {
			maxI, maxIslab = interiorPerSlab[i], i
		}
		if horizPerSlab[i] > maxH {
			maxH, maxHslab = horizPerSlab[i], i
		}
		if horizPerSlab[i] > 0 {
			nSlabsWithHoriz++
		}
	}
	t.Logf("=== projectXY load: %d wrapped faces, %d slabs ===", len(wrapped.Faces), nSlabs)
	t.Logf("  fully-interior tris: %d total (max %d on slab %d)", totalInterior, maxI, maxIslab)
	t.Logf("  near-horizontal interior tris: %d total (max %d on slab %d); %d/%d slabs have any",
		totalHoriz, maxH, maxHslab, nSlabsWithHoriz, nSlabs)

	// Is the field point inside a HOLE of the wrapped footprint (i.e. an
	// open courtyard/well)? Scan loops of slabs around the roof Z whose
	// bbox covers the point.
	for _, si := range []int{75, 76, 77, 78} {
		fp := fps[si]
		if fp == nil {
			continue
		}
		for li := range fp.Loops {
			l := &fp.Loops[li]
			if fieldX < l.MinX || fieldX > l.MaxX || fieldY < l.MinY || fieldY > l.MaxY {
				continue
			}
			t.Logf("  slab %d loop %d isHole=%v bbox X[%.2f,%.2f]Y[%.2f,%.2f] contains=%v npts=%d",
				si, li, l.IsHole, l.MinX, l.MaxX, l.MinY, l.MaxY, l.Contains(fieldX, fieldY), len(l.Points))
		}
	}

	// Classify the field point per slab: SOLID (in outer, no hole),
	// HOLE (in outer AND a hole), or OUT. Find the open-shaft Z extent.
	classify := func(fp *cellslicer.Footprint) string {
		if fp == nil {
			return "OUT"
		}
		inOuter, inHole := false, false
		for li := range fp.Loops {
			l := &fp.Loops[li]
			if l.Contains(fieldX, fieldY) {
				if l.IsHole {
					inHole = true
				} else {
					inOuter = true
				}
			}
		}
		switch {
		case inOuter && inHole:
			return "HOLE"
		case inOuter:
			return "SOLID"
		default:
			return "OUT"
		}
	}
	classTrace := func(tag string, arr []*cellslicer.Footprint) {
		prevC := ""
		for i := 0; i < nSlabs; i++ {
			c := classify(arr[i])
			if c != prevC {
				t.Logf("  %s field class: Z=%.2f -> %s", tag, planes[i], c)
				prevC = c
			}
		}
	}
	classTrace("wrapped", fps)
	classTrace("original", ofps)
}

// fmtZs renders sorted Z crossings compactly, rounded to 0.01.
func fmtZs(zs []float64) string {
	s := ""
	for i, z := range zs {
		if i > 0 {
			s += " "
		}
		s += fmt.Sprintf("%.2f", z)
	}
	return s
}

// triNormal returns the unit normal of triangle a,b,c.
func triNormal(a, b, c [3]float32) (float32, float32, float32) {
	ux, uy, uz := b[0]-a[0], b[1]-a[1], b[2]-a[2]
	vx, vy, vz := c[0]-a[0], c[1]-a[1], c[2]-a[2]
	nx := uy*vz - uz*vy
	ny := uz*vx - ux*vz
	nz := ux*vy - uy*vx
	l := float32(math.Sqrt(float64(nx*nx + ny*ny + nz*nz)))
	if l == 0 {
		return 0, 0, 0
	}
	return nx / l, ny / l, nz / l
}

func minf(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}
func maxf(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func abs8(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func writePNGFile(t *testing.T, path string, img image.Image) {
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
