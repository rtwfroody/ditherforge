package main

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"sort"

	"github.com/rtwfroody/ditherforge/internal/debugrender"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
	"github.com/rtwfroody/ditherforge/internal/render"
)

// writeCellOverlay renders, in ONE shared top-down frame, the clip-input
// surface (gray), the clip cell footprints we intersect against it (blue
// edges) and the resulting holes (magenta) — so the combination of mesh
// and cells can be inspected together. The user's question: where a cell
// boundary falls on the solid input surface, does clip drop a sliver?
//
// All three layers use a single UnionBounds so they align pixel-for-pixel.
func writeCellOverlay(dir string, source, output *pipeline.MeshData, cells []pipeline.CellOutline, res int) error {
	if source == nil || output == nil {
		return fmt.Errorf("need both source (clip input) and output meshes")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	v := debugrender.View{Name: "top", Azimuth: 0, Elev: 90}
	bounds := debugrender.UnionBounds(
		debugrender.MeshDataProjectedBounds(source, v),
		debugrender.MeshDataProjectedBounds(output, v),
	)

	srcUnculled := debugrender.RenderPipelineMeshUnculledWithBounds(source, v, res, bounds)
	outUnculled := debugrender.RenderPipelineMeshUnculledWithBounds(output, v, res, bounds)
	outCulled := debugrender.RenderPipelineMeshCulledWithBounds(output, v, res, bounds)

	img := image.NewRGBA(image.Rect(0, 0, res, res))
	gray := color.RGBA{205, 205, 205, 255}
	white := color.RGBA{255, 255, 255, 255}
	magenta := color.RGBA{255, 0, 255, 255}
	blue := color.RGBA{30, 90, 230, 255}

	// Base: clip-input surface in flat gray (where the input has surface).
	for y := 0; y < res; y++ {
		for x := 0; x < res; x++ {
			_, _, _, a := srcUnculled.At(x, y).RGBA()
			if a > 0 {
				img.SetRGBA(x, y, gray)
			} else {
				img.SetRGBA(x, y, white)
			}
		}
	}

	allPts, polyLens := flattenCells(cells)
	px := render.ProjectToPixels(allPts, v.Azimuth, v.Elev, res, bounds)
	drawCells := func() {
		off := 0
		for _, n := range polyLens {
			for i := 0; i < n; i++ {
				a := px[off+i]
				b := px[off+(i+1)%n]
				drawLine(img, int(a[0]), int(a[1]), int(b[0]), int(b[1]), blue)
			}
			off += n
		}
	}
	paintHoles := func() {
		for y := 0; y < res; y++ {
			for x := 0; x < res; x++ {
				i := y*res + x
				_, _, _, ua := outUnculled.At(x, y).RGBA()
				if ua > 0 && !outCulled.HasPixel[i] {
					img.SetRGBA(x, y, magenta)
				}
			}
		}
	}

	// Variant 1: cells first, holes on top.
	drawCells()
	paintHoles()
	p1 := filepath.Join(dir, "cells_overlay.png")
	if err := debugrender.WritePNG(p1, img); err != nil {
		return err
	}

	// Variant 2: holes first, cell edges ON TOP — so blue grid shows
	// THROUGH a hole iff a cell actually covers it. Solid magenta with no
	// blue edges ⇒ genuine cell-coverage gap; blue edges crossing magenta
	// ⇒ a cell is there but clip still dropped the surface.
	for y := 0; y < res; y++ {
		for x := 0; x < res; x++ {
			_, _, _, a := srcUnculled.At(x, y).RGBA()
			if a > 0 {
				img.SetRGBA(x, y, gray)
			} else {
				img.SetRGBA(x, y, white)
			}
		}
	}
	paintHoles()
	drawCells()
	p2 := filepath.Join(dir, "cells_over_holes.png")
	if err := debugrender.WritePNG(p2, img); err != nil {
		return err
	}
	// Variant 3: ONLY the top-layer cells — the slab forming the surface
	// we look at top-down. Depth-buffer every cell by its bed-Z (highest
	// wins, since we look down -Z) and keep, per pixel, the topmost cell.
	// Edges between distinct topmost cells are the top-surface tessellation
	// with all lower-slab / interior cells removed.
	winner := topLayerCellIDs(cells, px, polyLens, res)
	for y := 0; y < res; y++ {
		for x := 0; x < res; x++ {
			_, _, _, a := srcUnculled.At(x, y).RGBA()
			if a > 0 {
				img.SetRGBA(x, y, gray)
			} else {
				img.SetRGBA(x, y, white)
			}
		}
	}
	paintHoles()
	// Edges where the topmost cell changes between neighboring pixels.
	for y := 0; y < res; y++ {
		for x := 0; x < res; x++ {
			id := winner[y*res+x]
			if id < 0 {
				continue
			}
			edge := false
			if x+1 < res && winner[y*res+x+1] != id {
				edge = true
			} else if y+1 < res && winner[(y+1)*res+x] != id {
				edge = true
			}
			if edge {
				img.SetRGBA(x, y, blue)
			}
		}
	}
	p3 := filepath.Join(dir, "cells_toplayer.png")
	if err := debugrender.WritePNG(p3, img); err != nil {
		return err
	}

	// Variant 4: top layer FILLED by the cell's slab index. If the diagonal
	// seams in variant 3 are slab boundaries, distinct slabs show as
	// distinct flat color bands and the holes land on the band edges.
	for y := 0; y < res; y++ {
		for x := 0; x < res; x++ {
			id := winner[y*res+x]
			if id < 0 {
				_, _, _, a := srcUnculled.At(x, y).RGBA()
				if a > 0 {
					img.SetRGBA(x, y, gray)
				} else {
					img.SetRGBA(x, y, white)
				}
				continue
			}
			img.SetRGBA(x, y, slabColor(cells[id].Slab))
		}
	}
	paintHoles()
	p4 := filepath.Join(dir, "cells_toplayer_byslab.png")
	if err := debugrender.WritePNG(p4, img); err != nil {
		return err
	}

	// Variant 5: SIDE PROFILE of only the top-layer cells, at their true Z,
	// auto-zoomed to the top's thin Z band so the per-slab 0.08mm steps fill
	// the frame. Long axis (bed X) horizontal, Z vertical. Colored by slab.
	// Shows whether the cells track the tilted top as a staircase of flat
	// slab treads.
	if err := writeTopLayerProfile(filepath.Join(dir, "cells_profile.png"), cells, winner, res); err != nil {
		fmt.Fprintf(os.Stderr, "profile: %v\n", err)
	}

	fmt.Printf("Wrote cell overlays to %s, %s, %s and %s — %d cells\n", p1, p2, p3, p4, len(cells))
	return nil
}

// writePerSlabViews renders, for each slab at the source top-surface
// median Z (auto-selected), what the prism
// intersection "sees": the source top surface that actually falls inside
// that slab's Z band (green = prism will capture it), surface out of band
// (gray), the cells ASSIGNED to that slab (blue outlines), and the holes
// (magenta). A blue cell sitting over gray (not green) is the user's
// hypothesis made visible: a cell in slab N whose surface has rounded into
// a neighbouring slab, so prism∩source is empty there.
func writePerSlabViews(dir string, source, output *pipeline.MeshData, cells, coverTargets []pipeline.CellOutline, slabRanges []pipeline.SlabZRange, res int) error {
	v := debugrender.View{Name: "top", Azimuth: 0, Elev: 90}
	bounds := debugrender.UnionBounds(
		debugrender.MeshDataProjectedBounds(source, v),
		debugrender.MeshDataProjectedBounds(output, v),
	)
	srcCulled := debugrender.RenderPipelineMeshCulledWithBounds(source, v, res, bounds)
	outUnculled := debugrender.RenderPipelineMeshUnculledWithBounds(output, v, res, bounds)
	outCulled := debugrender.RenderPipelineMeshCulledWithBounds(output, v, res, bounds)

	zOf := map[int][2]float32{}
	for _, s := range slabRanges {
		zOf[s.Index] = [2]float32{s.ZBot, s.ZTop}
	}
	// Pre-project all cell outlines once.
	allPts, polyLens := flattenCells(cells)
	px := render.ProjectToPixels(allPts, v.Azimuth, v.Elev, res, bounds)
	cellOff := make([]int, len(cells))
	off := 0
	for i, n := range polyLens {
		cellOff[i] = off
		off += n
	}
	// Pre-project coverTarget loops (one CellOutline per loop, tagged by slab).
	ctPts, ctLens := flattenCells(coverTargets)
	ctPx := render.ProjectToPixels(ctPts, v.Azimuth, v.Elev, res, bounds)
	ctOff := make([]int, len(coverTargets))
	o2 := 0
	for i, n := range ctLens {
		ctOff[i] = o2
		o2 += n
	}

	green := color.RGBA{60, 175, 70, 255}
	lightgray := color.RGBA{210, 210, 210, 255}
	white := color.RGBA{255, 255, 255, 255}
	magenta := color.RGBA{255, 0, 255, 255}

	// One-time mapping diagnostic.
	{
		var dmin, dmax float64 = 1e30, -1e30
		var cnt int
		for i, has := range srcCulled.HasPixel {
			if !has {
				continue
			}
			d := srcCulled.Depth[i]
			if d < dmin {
				dmin = d
			}
			if d > dmax {
				dmax = d
			}
			cnt++
		}
		fmt.Printf("  [perslab] srcCulled depth: min=%.4f max=%.4f (DepthMin=%.4f DepthMax=%.4f) over %d px; slab46 z=[%.3f,%.3f] slab60 z=[%.3f,%.3f]\n",
			dmin, dmax, srcCulled.DepthMin, srcCulled.DepthMax, cnt, zOf[46][0], zOf[46][1], zOf[60][0], zOf[60][1])
		// Cell + slab Z extents.
		var czmin, czmax float32 = 1e30, -1e30
		var smin, smax int = 1 << 30, -(1 << 30)
		for _, c := range cells {
			z := c.Pts[0][2]
			if z < czmin {
				czmin = z
			}
			if z > czmax {
				czmax = z
			}
			if c.Slab < smin {
				smin = c.Slab
			}
			if c.Slab > smax {
				smax = c.Slab
			}
		}
		var szmin, szmax float32 = 1e30, -1e30
		for _, s := range slabRanges {
			if s.ZBot < szmin {
				szmin = s.ZBot
			}
			if s.ZTop > szmax {
				szmax = s.ZTop
			}
		}
		fmt.Printf("  [perslab] cells: midZ=[%.3f,%.3f] slabIdx=[%d,%d] (%d cells); slabRanges Z=[%.3f,%.3f] (%d slabs)\n",
			czmin, czmax, smin, smax, len(cells), szmin, szmax, len(slabRanges))
	}

	// Data-driven slab selection: render the slabs at the source top-surface
	// median Z (the actual flat top), ignoring the loN/hiN hint.
	var topZs []float32
	for i, has := range srcCulled.HasPixel {
		if has {
			topZs = append(topZs, float32(-srcCulled.Depth[i]))
		}
	}
	medTopZ := medianF32(topZs)
	var slabSel []int
	for _, s := range slabRanges {
		if s.ZTop >= medTopZ-0.4 && s.ZBot <= medTopZ+0.4 {
			slabSel = append(slabSel, s.Index)
		}
	}
	sort.Ints(slabSel)
	cellsPerSlab := map[int]int{}
	for _, c := range cells {
		cellsPerSlab[c.Slab]++
	}
	fmt.Printf("  [perslab] source top median Z=%.3f → %d slabs around it: %v\n", medTopZ, len(slabSel), slabSel)
	// Top slabs by cell count — locates each half's flat-top bulk slab.
	zLook := map[int]float32{}
	for _, s := range slabRanges {
		zLook[s.Index] = s.ZBot
	}
	allCounts := map[int]int{}
	for _, c := range cells {
		allCounts[c.Slab]++
	}
	type sc struct {
		slab, n int
	}
	var scs []sc
	for s, c := range allCounts {
		scs = append(scs, sc{s, c})
	}
	sort.Slice(scs, func(i, j int) bool { return scs[i].n > scs[j].n })
	fmt.Printf("  [perslab] top slabs by cell count (slab=count@z):")
	for i := 0; i < 12 && i < len(scs); i++ {
		fmt.Printf(" %d=%d@%.2f", scs[i].slab, scs[i].n, zLook[scs[i].slab])
	}
	fmt.Println()
	for _, n := range slabSel {
		fmt.Printf("  [perslab]   slab %d: %d cells, z=[%.3f,%.3f]\n", n, cellsPerSlab[n], zOf[n][0], zOf[n][1])
	}

	for _, n := range slabSel {
		zr, ok := zOf[n]
		if !ok {
			continue
		}
		// Coverage mask: pixels inside the UNION of this slab's cell footprints.
		covered := make([]bool, res*res)
		for ci := range cells {
			if cells[ci].Slab != n {
				continue
			}
			o := cellOff[ci]
			m := polyLens[ci]
			poly := px[o : o+m]
			minX, minY, maxX, maxY := poly[0][0], poly[0][1], poly[0][0], poly[0][1]
			for _, p := range poly {
				minX, maxX = minf(minX, p[0]), maxf(maxX, p[0])
				minY, maxY = minf(minY, p[1]), maxf(maxY, p[1])
			}
			x0, x1 := clampI(int(minX), 0, res), clampI(int(maxX)+1, 0, res)
			y0, y1 := clampI(int(minY), 0, res), clampI(int(maxY)+1, 0, res)
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					if pointInPoly(poly, float64(x)+0.5, float64(y)+0.5) {
						covered[y*res+x] = true
					}
				}
			}
		}

		img := image.NewRGBA(image.Rect(0, 0, res, res))
		for y := 0; y < res; y++ {
			for x := 0; x < res; x++ {
				i := y*res + x
				if !srcCulled.HasPixel[i] {
					img.SetRGBA(x, y, white) // no source surface here at all
					continue
				}
				// GREEN = the source surface (top-down) whose Z falls in THIS
				// slab's band — i.e. the mesh this slab's manifold contains.
				// GRAY = source surface that belongs to a different slab.
				// Half-open [zBot,zTop) so each surface belongs to one slab,
				// matching the cut (slabIndexForZ / SplitByPlane).
				worldZ := float32(-srcCulled.Depth[i])
				if worldZ >= zr[0] && worldZ < zr[1] {
					img.SetRGBA(x, y, green) // this slab's mesh
				} else {
					img.SetRGBA(x, y, lightgray) // another slab's mesh
				}
			}
		}
		_ = covered
		// coverTarget (the region this slab's cells are meant to tile),
		// drawn as a translucent YELLOW fill so we can see whether it
		// reaches the holes. Even-odd over all of this slab's loops (outer
		// minus holes).
		var ctLoops [][][2]float64
		ctMinX, ctMinY := math.Inf(1), math.Inf(1)
		ctMaxX, ctMaxY := math.Inf(-1), math.Inf(-1)
		for ci := range coverTargets {
			if coverTargets[ci].Slab != n {
				continue
			}
			o := ctOff[ci]
			m := ctLens[ci]
			loop := ctPx[o : o+m]
			ctLoops = append(ctLoops, loop)
			for _, p := range loop {
				ctMinX, ctMaxX = math.Min(ctMinX, p[0]), math.Max(ctMaxX, p[0])
				ctMinY, ctMaxY = math.Min(ctMinY, p[1]), math.Max(ctMaxY, p[1])
			}
		}
		if len(ctLoops) > 0 {
			x0, x1 := clampI(int(ctMinX), 0, res), clampI(int(ctMaxX)+1, 0, res)
			y0, y1 := clampI(int(ctMinY), 0, res), clampI(int(ctMaxY)+1, 0, res)
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					if pointInLoopsEvenOdd(ctLoops, float64(x)+0.5, float64(y)+0.5) {
						blend(img, x, y, color.RGBA{255, 215, 0, 255}, 0.30)
					}
				}
			}
		}
		// Cell outlines for cells in THIS slab (so you can see the actual
		// cells present in this layer over the slab's surface).
		blue := color.RGBA{20, 60, 220, 255}
		for ci := range cells {
			if cells[ci].Slab != n {
				continue
			}
			o := cellOff[ci]
			m := polyLens[ci]
			for i := 0; i < m; i++ {
				a := px[o+i]
				b := px[o+(i+1)%m]
				drawLine(img, int(a[0]), int(a[1]), int(b[0]), int(b[1]), blue)
			}
		}
		// Holes: TRANSLUCENT magenta so cells / coverTarget underneath show.
		for y := 0; y < res; y++ {
			for x := 0; x < res; x++ {
				i := y*res + x
				_, _, _, ua := outUnculled.At(x, y).RGBA()
				if ua > 0 && !outCulled.HasPixel[i] {
					blend(img, x, y, magenta, 0.45)
				}
			}
		}
		p := filepath.Join(dir, fmt.Sprintf("slab_%04d.png", n))
		if err := debugrender.WritePNG(p, img); err != nil {
			return err
		}
	}
	fmt.Printf("Wrote %d per-slab views (GREEN=this slab's mesh, gray=other slab's mesh, YELLOW=coverTarget, blue=this slab's cells, magenta=holes; coverTarget+holes translucent) to %s\n", len(slabSel), dir)

	// Per-cell Z table: for every slab-49 cell that overlaps a hole pixel,
	// report the source-surface Z range over the cell footprint, the surface
	// Z at the cell center, and slab 49's band. Reveals whether the surface a
	// cell owns actually sits inside that cell's prism Z range [zBot,zTop).
	isHole := func(x, y int) bool {
		i := y*res + x
		_, _, _, ua := outUnculled.At(x, y).RGBA()
		return ua > 0 && !outCulled.HasPixel[i]
	}
	const targetSlab = 49
	zr := zOf[targetSlab]
	fmt.Printf("\n  [ztable] slab %d band Z=[%.3f, %.3f) — cells overlapping a hole:\n", targetSlab, zr[0], zr[1])
	fmt.Printf("  %-6s %-9s %-9s %-9s %-7s %-9s\n", "cell", "ctrZ", "minZ", "maxZ", "holePx", "inBand?")
	printed := 0
	for ci := range cells {
		if cells[ci].Slab != targetSlab {
			continue
		}
		o := cellOff[ci]
		m := polyLens[ci]
		poly := px[o : o+m]
		minXf, minYf, maxXf, maxYf := poly[0][0], poly[0][1], poly[0][0], poly[0][1]
		var pcx, pcy float64
		for _, p := range poly {
			minXf, maxXf = minf(minXf, p[0]), maxf(maxXf, p[0])
			minYf, maxYf = minf(minYf, p[1]), maxf(maxYf, p[1])
			pcx += p[0]
			pcy += p[1]
		}
		pcx /= float64(m)
		pcy /= float64(m)
		x0, x1 := clampI(int(minXf), 0, res), clampI(int(maxXf)+1, 0, res)
		y0, y1 := clampI(int(minYf), 0, res), clampI(int(maxYf)+1, 0, res)
		var zmin, zmax float32 = 1e30, -1e30
		holePx := 0
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if !pointInPoly(poly, float64(x)+0.5, float64(y)+0.5) {
					continue
				}
				i := y*res + x
				if srcCulled.HasPixel[i] {
					wz := float32(-srcCulled.Depth[i])
					if wz < zmin {
						zmin = wz
					}
					if wz > zmax {
						zmax = wz
					}
				}
				if isHole(x, y) {
					holePx++
				}
			}
		}
		if holePx == 0 {
			continue
		}
		// surface Z at the cell center pixel.
		ctrZ := float32(math.NaN())
		cxp, cyp := int(pcx), int(pcy)
		if cxp >= 0 && cxp < res && cyp >= 0 && cyp < res && srcCulled.HasPixel[cyp*res+cxp] {
			ctrZ = float32(-srcCulled.Depth[cyp*res+cxp])
		}
		inBand := ctrZ >= zr[0] && ctrZ < zr[1]
		fmt.Printf("  %-6d %-9.4f %-9.4f %-9.4f %-7d %-9v\n", ci, ctrZ, zmin, zmax, holePx, inBand)
		printed++
		if printed >= 30 {
			fmt.Printf("  ... (truncated at 30)\n")
			break
		}
	}
	if printed == 0 {
		fmt.Printf("  (no slab-%d cells overlap a hole pixel)\n", targetSlab)
	}

	// Decisive count: of the hole pixels, how many are covered by SOME cell
	// (any slab) vs by NO cell at all. Covered-but-holed ⇒ clip CSG drops
	// surface a cell footprint owns; not-covered ⇒ cell-generation gap.
	coveredAny := make([]bool, res*res)
	for ci := range cells {
		o := cellOff[ci]
		m := polyLens[ci]
		poly := px[o : o+m]
		minX, minY, maxX, maxY := poly[0][0], poly[0][1], poly[0][0], poly[0][1]
		for _, p := range poly {
			minX, maxX = minf(minX, p[0]), maxf(maxX, p[0])
			minY, maxY = minf(minY, p[1]), maxf(maxY, p[1])
		}
		x0, x1 := clampI(int(minX), 0, res), clampI(int(maxX)+1, 0, res)
		y0, y1 := clampI(int(minY), 0, res), clampI(int(maxY)+1, 0, res)
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if !coveredAny[y*res+x] && pointInPoly(poly, float64(x)+0.5, float64(y)+0.5) {
					coveredAny[y*res+x] = true
				}
			}
		}
	}
	var holeTot, holeCovered int
	for y := 0; y < res; y++ {
		for x := 0; x < res; x++ {
			i := y*res + x
			_, _, _, ua := outUnculled.At(x, y).RGBA()
			if ua > 0 && !outCulled.HasPixel[i] {
				holeTot++
				if coveredAny[i] {
					holeCovered++
				}
			}
		}
	}
	fmt.Printf("  [perslab] holes: %d total; %d (%.1f%%) covered by SOME cell footprint, %d (%.1f%%) covered by NO cell\n",
		holeTot, holeCovered, 100*float64(holeCovered)/float64(holeTot),
		holeTot-holeCovered, 100*float64(holeTot-holeCovered)/float64(holeTot))

	// Decisive Z-mismatch test: for each pixel, mark whether a cell covers it
	// whose OWN slab Z-band actually contains the source surface's Z there.
	// If hole pixels are covered by a cell but NOT by a correct-slab cell,
	// the cell was generated in the wrong slab (footprint Z-view disagrees
	// with the cut).
	zAll := map[int][2]float32{}
	for _, s := range slabRanges {
		zAll[s.Index] = [2]float32{s.ZBot, s.ZTop}
	}
	correctSlab := make([]bool, res*res)
	for ci := range cells {
		zr := zAll[cells[ci].Slab]
		o := cellOff[ci]
		m := polyLens[ci]
		poly := px[o : o+m]
		minX, minY, maxX, maxY := poly[0][0], poly[0][1], poly[0][0], poly[0][1]
		for _, p := range poly {
			minX, maxX = minf(minX, p[0]), maxf(maxX, p[0])
			minY, maxY = minf(minY, p[1]), maxf(maxY, p[1])
		}
		x0, x1 := clampI(int(minX), 0, res), clampI(int(maxX)+1, 0, res)
		y0, y1 := clampI(int(minY), 0, res), clampI(int(maxY)+1, 0, res)
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				i := y*res + x
				if correctSlab[i] || !srcCulled.HasPixel[i] {
					continue
				}
				wz := float32(-srcCulled.Depth[i])
				if wz >= zr[0] && wz <= zr[1] && pointInPoly(poly, float64(x)+0.5, float64(y)+0.5) {
					correctSlab[i] = true
				}
			}
		}
	}
	var holeWrongSlab int
	for y := 0; y < res; y++ {
		for x := 0; x < res; x++ {
			i := y*res + x
			_, _, _, ua := outUnculled.At(x, y).RGBA()
			if ua > 0 && !outCulled.HasPixel[i] && coveredAny[i] && !correctSlab[i] {
				holeWrongSlab++
			}
		}
	}
	fmt.Printf("  [perslab] holes covered by a cell but NONE whose slab Z-band contains the surface Z (WRONG-SLAB cell): %d (%.1f%% of holes)\n",
		holeWrongSlab, 100*float64(holeWrongSlab)/float64(holeTot))
	return nil
}

// writeTopLayerProfile draws a side-on profile (bed X horizontal, Z up) of
// just the cells that are topmost in the top-down view, colored by slab.
func writeTopLayerProfile(path string, cells []pipeline.CellOutline, winner []int32, res int) error {
	topSet := map[int32]bool{}
	for _, id := range winner {
		if id >= 0 {
			topSet[id] = true
		}
	}
	if len(topSet) == 0 {
		return fmt.Errorf("no top-layer cells")
	}
	// Per top cell: centroid (x,y) and Z.
	type cinfo struct {
		id      int32
		cx, cy  float32
		z       float32
	}
	var infos []cinfo
	var zs []float32
	for id := range topSet {
		c := cells[id]
		var sx, sy float32
		for _, p := range c.Pts {
			sx += p[0]
			sy += p[1]
		}
		n := float32(len(c.Pts))
		ci := cinfo{id: id, cx: sx / n, cy: sy / n, z: c.Pts[0][2]}
		infos = append(infos, ci)
		zs = append(zs, ci.z)
	}
	// Median Z of the top layer → drop posts/outliers far above it.
	medZ := medianF32(zs)
	// Median Y → take a thin slice so the X–Z staircase is crisp (no Y blur).
	ys := make([]float32, len(infos))
	for i, ci := range infos {
		ys[i] = ci.cy
	}
	medY := medianF32(ys)
	const zKeep = 0.6  // mm: flat-top band around medZ (excludes tall posts)
	const ySlice = 4.0 // mm: half-width of the Y slice

	var pts3 [][3]float32
	type cellRange struct {
		off, n, slab int
	}
	var ranges []cellRange
	for _, ci := range infos {
		if absf32(ci.z-medZ) > zKeep || absf32(ci.cy-medY) > ySlice {
			continue
		}
		c := cells[ci.id]
		ranges = append(ranges, cellRange{off: len(pts3), n: len(c.Pts), slab: c.Slab})
		// Exaggerate Z about the median so the 0.08mm slab steps are visible
		// against the ~250mm length (uniform projection scale otherwise
		// squashes them to a couple of pixels).
		const zExag = 150.0
		for _, p := range c.Pts {
			pts3 = append(pts3, [3]float32{p[0], p[1], medZ + (p[2]-medZ)*zExag})
		}
	}
	if len(pts3) == 0 {
		return fmt.Errorf("no flat-top cells in slice")
	}

	// Factual stats: which slabs the flat top spans, and their Z.
	slabZ := map[int]float32{}
	for _, ci := range infos {
		if absf32(ci.z-medZ) <= zKeep {
			c := cells[ci.id]
			slabZ[c.Slab] = ci.z
		}
	}
	slabList := make([]int, 0, len(slabZ))
	for s := range slabZ {
		slabList = append(slabList, s)
	}
	sort.Ints(slabList)
	fmt.Printf("  [profile] flat top (±%.2gmm of Z=%.3f) spans %d distinct slabs: ", zKeep, medZ, len(slabList))
	for _, s := range slabList {
		fmt.Printf("%d(z=%.3f) ", s, slabZ[s])
	}
	fmt.Println()
	v := debugrender.View{Name: "side", Azimuth: 90, Elev: 1}
	bounds := render.ProjectedBounds(pts3, v.Azimuth, v.Elev)
	px := render.ProjectToPixels(pts3, v.Azimuth, v.Elev, res, bounds)
	img := image.NewRGBA(image.Rect(0, 0, res, res))
	white := color.RGBA{255, 255, 255, 255}
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	_ = white
	for _, r := range ranges {
		col := slabColor(r.slab)
		poly := px[r.off : r.off+r.n]
		for i := 0; i < r.n; i++ {
			a := poly[i]
			b := poly[(i+1)%r.n]
			drawLine(img, int(a[0]), int(a[1]), int(b[0]), int(b[1]), col)
		}
	}
	return debugrender.WritePNG(path, img)
}

// slabColor maps a slab index to a repeating, high-contrast color so
// adjacent slabs are visually distinct (cycle length 6, prime-ish step).
func slabColor(slab int) color.RGBA {
	palette := []color.RGBA{
		{220, 60, 60, 255},
		{60, 160, 60, 255},
		{70, 110, 230, 255},
		{230, 160, 40, 255},
		{150, 80, 200, 255},
		{40, 180, 200, 255},
	}
	return palette[((slab%6)+6)%6]
}

// topLayerCellIDs depth-buffers every cell footprint by its bed-Z and
// returns, per pixel, the index of the topmost (highest-Z) cell covering
// it (-1 = none). px holds the projected pixel coords of all cell points
// concatenated; polyLens gives each cell's vertex count in order.
func topLayerCellIDs(cells []pipeline.CellOutline, px [][2]float64, polyLens []int, res int) []int32 {
	winner := make([]int32, res*res)
	zbuf := make([]float32, res*res)
	for i := range winner {
		winner[i] = -1
		zbuf[i] = float32(-1e30)
	}
	off := 0
	for ci, n := range polyLens {
		poly := px[off : off+n]
		off += n
		if n < 3 {
			continue
		}
		z := cells[ci].Pts[0][2] // slab mid-Z (same for all of the cell's points)
		minX, minY := poly[0][0], poly[0][1]
		maxX, maxY := poly[0][0], poly[0][1]
		for _, p := range poly {
			minX = minf(minX, p[0])
			maxX = maxf(maxX, p[0])
			minY = minf(minY, p[1])
			maxY = maxf(maxY, p[1])
		}
		x0, x1 := int(minX), int(maxX)+1
		y0, y1 := int(minY), int(maxY)+1
		if x0 < 0 {
			x0 = 0
		}
		if y0 < 0 {
			y0 = 0
		}
		if x1 > res {
			x1 = res
		}
		if y1 > res {
			y1 = res
		}
		for y := y0; y < y1; y++ {
			fy := float64(y) + 0.5
			for x := x0; x < x1; x++ {
				if !pointInPoly(poly, float64(x)+0.5, fy) {
					continue
				}
				idx := y*res + x
				if z > zbuf[idx] {
					zbuf[idx] = z
					winner[idx] = int32(ci)
				}
			}
		}
	}
	return winner
}

// pointInLoopsEvenOdd returns true when (x,y) is inside the even-odd fill
// of all loops combined (outer loops add, hole loops subtract).
func pointInLoopsEvenOdd(loops [][][2]float64, x, y float64) bool {
	inside := false
	for _, poly := range loops {
		n := len(poly)
		for i, j := 0, n-1; i < n; j, i = i, i+1 {
			if (poly[i][1] > y) != (poly[j][1] > y) {
				xi := (poly[j][0]-poly[i][0])*(y-poly[i][1])/(poly[j][1]-poly[i][1]) + poly[i][0]
				if x < xi {
					inside = !inside
				}
			}
		}
	}
	return inside
}

// blend alpha-composites color c over the existing pixel at (x,y).
func blend(img *image.RGBA, x, y int, c color.RGBA, a float64) {
	o := img.RGBAAt(x, y)
	img.SetRGBA(x, y, color.RGBA{
		uint8(float64(o.R)*(1-a) + float64(c.R)*a),
		uint8(float64(o.G)*(1-a) + float64(c.G)*a),
		uint8(float64(o.B)*(1-a) + float64(c.B)*a),
		255,
	})
}

func pointInPoly(poly [][2]float64, x, y float64) bool {
	inside := false
	n := len(poly)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		if (poly[i][1] > y) != (poly[j][1] > y) {
			xi := (poly[j][0]-poly[i][0])*(y-poly[i][1])/(poly[j][1]-poly[i][1]) + poly[i][0]
			if x < xi {
				inside = !inside
			}
		}
	}
	return inside
}

func absf32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

func medianF32(v []float32) float32 {
	if len(v) == 0 {
		return 0
	}
	s := make([]float32, len(v))
	copy(s, v)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

func clampI(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func flattenCells(cells []pipeline.CellOutline) ([][3]float32, []int) {
	var pts [][3]float32
	lens := make([]int, 0, len(cells))
	for _, c := range cells {
		lens = append(lens, len(c.Pts))
		pts = append(pts, c.Pts...)
	}
	return pts, lens
}

// drawLine is a plain integer Bresenham line into the RGBA image.
func drawLine(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	dx := abs(x1 - x0)
	dy := -abs(y1 - y0)
	sx := 1
	if x0 > x1 {
		sx = -1
	}
	sy := 1
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	for {
		if x0 >= 0 && x0 < w && y0 >= 0 && y0 < h {
			img.SetRGBA(x0, y0, c)
		}
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
