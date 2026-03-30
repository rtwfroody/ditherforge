// Package squarevoxel generates a square voxel shell of a textured mesh.
// Each cube cell has edge length ~1.275× the nozzle diameter.
// Isosurface extraction uses marching cubes for a smooth surface.
package squarevoxel

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"runtime"
	"sync"
	"time"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// Config holds parameters for square voxel remeshing.
type Config struct {
	NozzleDiameter float32
	LayerHeight    float32
	WallThickness  float32 // shell thickness in mm (default 3.0)
	NoMerge        bool
	Infill         bool // generate infill object inside the shell
}

// Remesh generates a square voxel shell of the input model using marching cubes.
// Returns mesh parts (shell + optional infill), the palette, and an error.
func Remesh(model *loader.LoadedModel, pcfg voxel.PaletteConfig, cfg Config, ditherMode string) ([]voxel.MeshPart, [][3]uint8, error) {
	if len(model.Vertices) == 0 || len(model.Faces) == 0 {
		return nil, nil, fmt.Errorf("empty model")
	}

	// Cell edge length. At 1.0× nozzle diameter the slicer can't fill the
	// bottom layer reliably. Empirically 1.275× (0.51mm for a 0.4mm nozzle)
	// is the minimum that produces solid first layers.
	cellSize := cfg.NozzleDiameter * 1.275
	layerH := cfg.LayerHeight

	// 1. Bounding box.
	minV, maxV := voxel.ComputeBounds(model.Vertices)
	modelMin, modelMax := minV, maxV
	xyPad := cellSize * 2
	zPad := layerH * 2
	minV[0] -= xyPad
	minV[1] -= xyPad
	minV[2] -= zPad
	maxV[0] += xyPad
	maxV[1] += xyPad
	maxV[2] += zPad

	nCols := int(math.Ceil(float64(maxV[0]-minV[0])/float64(cellSize))) + 1
	nRows := int(math.Ceil(float64(maxV[1]-minV[1])/float64(cellSize))) + 1
	nLayers := int(math.Ceil(float64(maxV[2]-minV[2])/float64(layerH))) + 1

	// 2. Spatial index.
	si := voxel.NewSpatialIndex(model, cellSize*2)

	// 3. Z-ray voxelization.
	fmt.Printf("  Voxelizing...")
	tVoxelize := time.Now()
	var cells []voxel.ActiveCell
	colorBuf := voxel.NewSearchBuf(len(model.Faces))
	colorRadius := cellSize * 3

	for col := 0; col < nCols; col++ {
		cx := minV[0] + float32(col)*cellSize
		for row := 0; row < nRows; row++ {
			cy := minV[1] + float32(row)*cellSize

			activeLayers := voxel.VoxelizeColumn(cx, cy, model, si, layerH, minV[2], nLayers)
			for layer := range activeLayers {
				cz := minV[2] + float32(layer)*layerH
				rgba := voxel.SampleNearestColor(
					[3]float32{cx, cy, cz},
					model, si, colorRadius, colorBuf)
				if rgba[3] < 128 {
					continue // skip translucent voxels
				}
				cells = append(cells, voxel.ActiveCell{
					Col: col, Row: row, Layer: layer,
					Cx: cx, Cy: cy, Cz: cz,
					Color: [3]uint8{rgba[0], rgba[1], rgba[2]},
				})
			}
		}
	}

	// Proximity-based supplementary voxelization.
	proximitySet := make(map[voxel.CellKey]struct{})
	proximityRadius := cellSize
	for fi := range model.Faces {
		f := model.Faces[fi]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]
		tMinX := min(v0[0], min(v1[0], v2[0]))
		tMaxX := max(v0[0], max(v1[0], v2[0]))
		tMinY := min(v0[1], min(v1[1], v2[1]))
		tMaxY := max(v0[1], max(v1[1], v2[1]))
		tMinZ := min(v0[2], min(v1[2], v2[2]))
		tMaxZ := max(v0[2], max(v1[2], v2[2]))
		colMin := int(math.Floor(float64(tMinX-minV[0])/float64(cellSize))) - 1
		colMax := int(math.Ceil(float64(tMaxX-minV[0])/float64(cellSize))) + 1
		rowMin := int(math.Floor(float64(tMinY-minV[1])/float64(cellSize))) - 1
		rowMax := int(math.Ceil(float64(tMaxY-minV[1])/float64(cellSize))) + 1
		layerMin := int(math.Floor(float64(tMinZ-minV[2])/float64(layerH))) - 1
		layerMax := int(math.Ceil(float64(tMaxZ-minV[2])/float64(layerH))) + 1
		if colMin < 0 {
			colMin = 0
		}
		if colMax >= nCols {
			colMax = nCols - 1
		}
		if rowMin < 0 {
			rowMin = 0
		}
		if rowMax >= nRows {
			rowMax = nRows - 1
		}
		if layerMin < 0 {
			layerMin = 0
		}
		if layerMax >= nLayers {
			layerMax = nLayers - 1
		}
		for col := colMin; col <= colMax; col++ {
			hcx := minV[0] + float32(col)*cellSize
			for row := rowMin; row <= rowMax; row++ {
				hcy := minV[1] + float32(row)*cellSize
				for layer := layerMin; layer <= layerMax; layer++ {
					hcz := minV[2] + float32(layer)*layerH
					_, dSq := voxel.ClosestPointOnTriangle3D(
						[3]float32{hcx, hcy, hcz}, v0, v1, v2)
					if float32(math.Sqrt(float64(dSq))) <= proximityRadius {
						proximitySet[voxel.CellKey{Col: col, Row: row, Layer: layer}] = struct{}{}
					}
				}
			}
		}
	}
	existingCells := make(map[voxel.CellKey]struct{}, len(cells))
	for _, c := range cells {
		existingCells[voxel.CellKey{Col: c.Col, Row: c.Row, Layer: c.Layer}] = struct{}{}
	}
	proximityAdded := 0
	for k := range proximitySet {
		if _, exists := existingCells[k]; !exists {
			cx := minV[0] + float32(k.Col)*cellSize
			cy := minV[1] + float32(k.Row)*cellSize
			cz := minV[2] + float32(k.Layer)*layerH
			rgba := voxel.SampleNearestColor(
				[3]float32{cx, cy, cz},
				model, si, colorRadius, colorBuf)
			if rgba[3] < 128 {
				continue // skip translucent voxels
			}
			cells = append(cells, voxel.ActiveCell{
				Col: k.Col, Row: k.Row, Layer: k.Layer,
				Cx: cx, Cy: cy, Cz: cz,
				Color: [3]uint8{rgba[0], rgba[1], rgba[2]},
			})
			proximityAdded++
		}
	}

	cells = voxel.DeduplicateCells(cells)
	fmt.Printf(" %d cells in %.1fs\n", len(cells), time.Since(tVoxelize).Seconds())
	if len(cells) == 0 {
		return nil, nil, fmt.Errorf("no active cells found")
	}

	// Build cell lookup maps.
	cellAssignMap := make(map[voxel.CellKey]int, len(cells))
	activeSet := make(map[voxel.CellKey]struct{}, len(cells))
	for i, c := range cells {
		k := voxel.CellKey{Col: c.Col, Row: c.Row, Layer: c.Layer}
		cellAssignMap[k] = i
		activeSet[k] = struct{}{}
	}

	wallThickness := cfg.WallThickness
	if wallThickness <= 0 {
		wallThickness = 3.0
	}
	lateralOffsets := [4][2]int{{+1, 0}, {-1, 0}, {0, +1}, {0, -1}}

	// Build the expanded cell set for marching cubes, and optionally the
	// infill cell set for a separate infill object.
	var shellExpandedSet map[voxel.CellKey]struct{}
	var infillSet map[voxel.CellKey]struct{}

	if cfg.Infill {
		// Find all interior cells using Z-ray parity per column.
		shellRings := int(math.Ceil(float64(wallThickness) / float64(cellSize)))
		if shellRings < 1 {
			shellRings = 1
		}

		fmt.Printf("  Finding interior cells...")
		tInterior := time.Now()
		interiorSet := make(map[voxel.CellKey]struct{}, len(cells)*4)
		for k := range activeSet {
			interiorSet[k] = struct{}{}
		}
		for col := 0; col < nCols; col++ {
			cx := minV[0] + float32(col)*cellSize
			for row := 0; row < nRows; row++ {
				cy := minV[1] + float32(row)*cellSize
				interior := voxel.InteriorLayers(cx, cy, model, si, layerH, minV[2], nLayers)
				for layer := range interior {
					k := voxel.CellKey{Col: col, Row: row, Layer: layer}
					interiorSet[k] = struct{}{}
				}
			}
		}
		fmt.Printf(" %d cells in %.1fs\n", len(interiorSet)-len(activeSet), time.Since(tInterior).Seconds())

		// BFS from active cells through interior cells to compute distance.
		distMap := make(map[voxel.CellKey]int, len(interiorSet))
		queue := make([]voxel.CellKey, 0, len(cells))
		for k := range activeSet {
			distMap[k] = 0
			queue = append(queue, k)
		}
		for len(queue) > 0 {
			k := queue[0]
			queue = queue[1:]
			nd := distMap[k] + 1
			for _, nk := range [6]voxel.CellKey{
				{Col: k.Col + 1, Row: k.Row, Layer: k.Layer},
				{Col: k.Col - 1, Row: k.Row, Layer: k.Layer},
				{Col: k.Col, Row: k.Row + 1, Layer: k.Layer},
				{Col: k.Col, Row: k.Row - 1, Layer: k.Layer},
				{Col: k.Col, Row: k.Row, Layer: k.Layer + 1},
				{Col: k.Col, Row: k.Row, Layer: k.Layer - 1},
			} {
				if _, visited := distMap[nk]; visited {
					continue
				}
				if _, isInterior := interiorSet[nk]; !isInterior {
					continue
				}
				distMap[nk] = nd
				queue = append(queue, nk)
			}
		}

		// Split into shell and infill cell sets.
		shellSet := make(map[voxel.CellKey]struct{}, len(cells)*2)
		infillSet = make(map[voxel.CellKey]struct{})
		for k, d := range distMap {
			if d < shellRings {
				shellSet[k] = struct{}{}
			} else {
				infillSet[k] = struct{}{}
			}
		}
		fmt.Printf("  Shell/infill split: %d shell, %d infill (shellRings=%d)\n",
			len(shellSet), len(infillSet), shellRings)

		// Exterior padding: expand 2 rings outward from activeSet for MC.
		exteriorPadding := make(map[voxel.CellKey]struct{})
		padSource := make(map[voxel.CellKey]struct{}, len(activeSet))
		for k := range activeSet {
			padSource[k] = struct{}{}
		}
		for ring := 0; ring < 2; ring++ {
			snapshot := make([]voxel.CellKey, 0, len(padSource))
			for k := range padSource {
				snapshot = append(snapshot, k)
			}
			newPad := make(map[voxel.CellKey]struct{})
			for _, k := range snapshot {
				for _, nk := range [6]voxel.CellKey{
					{Col: k.Col + 1, Row: k.Row, Layer: k.Layer},
					{Col: k.Col - 1, Row: k.Row, Layer: k.Layer},
					{Col: k.Col, Row: k.Row + 1, Layer: k.Layer},
					{Col: k.Col, Row: k.Row - 1, Layer: k.Layer},
					{Col: k.Col, Row: k.Row, Layer: k.Layer + 1},
					{Col: k.Col, Row: k.Row, Layer: k.Layer - 1},
				} {
					if _, inShell := shellSet[nk]; inShell {
						continue
					}
					if _, inInfill := infillSet[nk]; inInfill {
						continue
					}
					if _, already := exteriorPadding[nk]; already {
						continue
					}
					exteriorPadding[nk] = struct{}{}
					newPad[nk] = struct{}{}
				}
			}
			padSource = newPad
		}

		shellExpandedSet = make(map[voxel.CellKey]struct{}, len(shellSet)+len(exteriorPadding))
		for k := range shellSet {
			shellExpandedSet[k] = struct{}{}
		}
		for k := range exteriorPadding {
			shellExpandedSet[k] = struct{}{}
		}
		fmt.Printf("  Expanded set: %d cells\n", len(shellExpandedSet))
	} else {
		// No infill: expand active set by enough rings for wall thickness
		// + marching cubes margin.
		expansionRings := int(math.Ceil(float64(wallThickness)/float64(cellSize))) + 1
		shellExpandedSet = make(map[voxel.CellKey]struct{}, len(cells)*2)
		for k := range activeSet {
			shellExpandedSet[k] = struct{}{}
		}
		for ring := 0; ring < expansionRings; ring++ {
			snapshot := make([]voxel.CellKey, 0, len(shellExpandedSet))
			for k := range shellExpandedSet {
				snapshot = append(snapshot, k)
			}
			for _, k := range snapshot {
				shellExpandedSet[voxel.CellKey{Col: k.Col, Row: k.Row, Layer: k.Layer - 1}] = struct{}{}
				shellExpandedSet[voxel.CellKey{Col: k.Col, Row: k.Row, Layer: k.Layer + 1}] = struct{}{}
				for _, off := range lateralOffsets {
					shellExpandedSet[voxel.CellKey{Col: k.Col + off[0], Row: k.Row + off[1], Layer: k.Layer}] = struct{}{}
				}
			}
		}
		fmt.Printf("  Expanded set: %d cells (from %d active)\n", len(shellExpandedSet), len(cells))
	}

	// 4. Compute SDF at cube vertices.
	fmt.Printf("  Computing SDF...")
	tSDF := time.Now()
	// Search radius must reach the farthest expanded cell from the surface.
	var searchRadius float32
	if cfg.Infill {
		// Infill path: only 2 rings of exterior padding.
		searchRadius = cellSize * 3
	} else {
		// Non-infill path: expansionRings = ceil(wallThickness/cellSize) + 1.
		searchRadius = wallThickness + cellSize*3
	}
	shellThickness := layerH
	pn := voxel.BuildPseudonormals(model)
	halfCell := cellSize / 2

	// Cube corner offsets: 8 corners of a cube centered at (cx, cy, cz).
	// Bottom layer (z - layerH/2): corners 0-3
	// Top layer (z + layerH/2): corners 4-7
	// Corner numbering:
	//   0: (-half, -half, bot)  1: (+half, -half, bot)
	//   2: (+half, +half, bot)  3: (-half, +half, bot)
	//   4: (-half, -half, top)  5: (+half, -half, top)
	//   6: (+half, +half, top)  7: (-half, +half, top)
	halfH := layerH / 2
	cubeOffsets := [8][3]float32{
		{-halfCell, -halfCell, -halfH}, {+halfCell, -halfCell, -halfH},
		{+halfCell, +halfCell, -halfH}, {-halfCell, +halfCell, -halfH},
		{-halfCell, -halfCell, +halfH}, {+halfCell, -halfCell, +halfH},
		{+halfCell, +halfCell, +halfH}, {-halfCell, +halfCell, +halfH},
	}

	vertPos := func(col, row, layer, corner int) [3]float32 {
		cx := minV[0] + float32(col)*cellSize
		cy := minV[1] + float32(row)*cellSize
		cz := minV[2] + float32(layer)*layerH
		return [3]float32{
			cx + cubeOffsets[corner][0],
			cy + cubeOffsets[corner][1],
			cz + cubeOffsets[corner][2],
		}
	}

	// Build a map from snapped vertex position to index, and a flat array of
	// positions for parallel SDF evaluation. This avoids storing SDF values
	// in a second map (which would double memory usage).
	vertIndex := make(map[[3]float32]int32)
	for k := range shellExpandedSet {
		for corner := 0; corner < 8; corner++ {
			pos := voxel.SnapPos(vertPos(k.Col, k.Row, k.Layer, corner))
			if _, ok := vertIndex[pos]; !ok {
				vertIndex[pos] = int32(len(vertIndex))
			}
		}
	}
	uniqueVerts := make([][3]float32, len(vertIndex))
	for pos, idx := range vertIndex {
		uniqueVerts[idx] = pos
	}

	nWorkers := runtime.NumCPU()
	sdfValues := make([]float32, len(uniqueVerts))
	var wg sync.WaitGroup
	chunkSize := (len(uniqueVerts) + nWorkers - 1) / nWorkers
	for w := 0; w < nWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if end > len(uniqueVerts) {
			end = len(uniqueVerts)
		}
		if start >= end {
			break
		}
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			buf := voxel.NewSearchBuf(len(model.Faces))
			for i := start; i < end; i++ {
				sdfValues[i] = voxel.ComputeSDF(uniqueVerts[i], model, si, searchRadius, shellThickness, pn, modelMin, modelMax, buf)
			}
		}(start, end)
	}
	wg.Wait()
	fmt.Printf(" %d vertices in %.1fs\n", len(vertIndex), time.Since(tSDF).Seconds())
	uniqueVerts = nil // free; only vertIndex+sdfValues needed from here

	// 5. Resolve palette and assign / dither.
	pal, palDisplay := voxel.ResolvePalette(cells, pcfg)
	if palDisplay != "" {
		fmt.Printf("%s\n", palDisplay)
	}
	if len(pal) == 0 {
		return nil, nil, fmt.Errorf("no palette colors")
	}
	fmt.Printf("  Dithering (%s)...", ditherMode)
	tDither := time.Now()
	var assignments []int32
	switch ditherMode {
	case "dizzy":
		assignments = voxel.DitherCellsDizzy(cells, pal)
	case "fs":
		assignments = voxel.DitherCellsFS(cells, pal)
	default:
		assignments = voxel.AssignColors(cells, pal)
	}
	fmt.Printf(" %d cells in %.1fs\n", len(cells), time.Since(tDither).Seconds())

	// 6. Marching cubes isosurface extraction for smooth outer surface.
	fmt.Printf("  Marching cubes...")
	tMC := time.Now()
	vd := voxel.NewVertexDedup()
	mcFaces := make([][3]uint32, 0)
	mcAssignments := make([]int32, 0)

	for k := range shellExpandedSet {
		// Determine color assignment.
		assignment := int32(0)
		if hi, ok := cellAssignMap[k]; ok {
			assignment = assignments[hi]
		} else {
			found := false
			for _, dl := range []int{-1, 1} {
				nk := voxel.CellKey{Col: k.Col, Row: k.Row, Layer: k.Layer + dl}
				if hi, ok := cellAssignMap[nk]; ok {
					assignment = assignments[hi]
					found = true
					break
				}
			}
			if !found {
				for _, off := range lateralOffsets {
					nk := voxel.CellKey{Col: k.Col + off[0], Row: k.Row + off[1], Layer: k.Layer}
					if hi, ok := cellAssignMap[nk]; ok {
						assignment = assignments[hi]
						break
					}
				}
			}
		}

		// Get SDF at 8 cube corners.
		var cornerPos [8][3]float32
		var cornerSDF [8]float32
		for c := 0; c < 8; c++ {
			cornerPos[c] = voxel.SnapPos(vertPos(k.Col, k.Row, k.Layer, c))
			cornerSDF[c] = sdfValues[vertIndex[cornerPos[c]]]
		}

		// Build 8-bit case index.
		caseIdx := 0
		for i := 0; i < 8; i++ {
			if cornerSDF[i] < 0 {
				caseIdx |= 1 << i
			}
		}

		triEdges := mcTriTable[caseIdx]
		if len(triEdges) == 0 {
			continue
		}

		for t := 0; t+2 < len(triEdges); t += 3 {
			var triVerts [3]uint32
			for ki := 0; ki < 3; ki++ {
				edge := triEdges[t+ki]
				ea := mcEdges[edge][0]
				eb := mcEdges[edge][1]
				posA := cornerPos[ea]
				posB := cornerPos[eb]
				sdfA := cornerSDF[ea]
				sdfB := cornerSDF[eb]

				// Canonicalize edge direction for consistent interpolation.
				if posA[0] > posB[0] || (posA[0] == posB[0] && posA[1] > posB[1]) ||
					(posA[0] == posB[0] && posA[1] == posB[1] && posA[2] > posB[2]) {
					posA, posB = posB, posA
					sdfA, sdfB = sdfB, sdfA
				}

				var interp [3]float32
				denom := sdfB - sdfA
				if denom == 0 {
					interp = [3]float32{
						(posA[0] + posB[0]) / 2,
						(posA[1] + posB[1]) / 2,
						(posA[2] + posB[2]) / 2,
					}
				} else {
					frac := -sdfA / denom
					frac = voxel.ClampF(frac, 0, 1)
					interp = [3]float32{
						posA[0] + frac*(posB[0]-posA[0]),
						posA[1] + frac*(posB[1]-posA[1]),
						posA[2] + frac*(posB[2]-posA[2]),
					}
				}
				triVerts[ki] = vd.GetVertex(interp)
			}
			if triVerts[0] == triVerts[1] || triVerts[1] == triVerts[2] || triVerts[0] == triVerts[2] {
				continue
			}
			mcFaces = append(mcFaces, [3]uint32{triVerts[0], triVerts[1], triVerts[2]})
			mcAssignments = append(mcAssignments, assignment)
		}
	}

	fmt.Printf(" %d faces in %.1fs\n", len(mcFaces), time.Since(tMC).Seconds())

	// Build output models.
	placeholder := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	placeholder.SetNRGBA(0, 0, color.NRGBA{128, 128, 128, 255})
	var textures []image.Image
	if len(model.Textures) > 0 {
		textures = model.Textures[:1]
	} else {
		textures = []image.Image{placeholder}
	}

	buildModel := func(verts [][3]float32, faces [][3]uint32) *loader.LoadedModel {
		return &loader.LoadedModel{
			Vertices:       verts,
			Faces:          faces,
			UVs:            make([][2]float32, len(verts)),
			Textures:       textures,
			FaceTextureIdx: make([]int32, len(faces)),
		}
	}

	// Infill: voxel boundary faces of infillSet (watertight by construction).
	// Generate infill first because the shell reuses its faces (flipped) as
	// the inner boundary.
	var infillFaces [][3]uint32
	var infillVD *voxel.VertexDedup
	if len(infillSet) > 0 {
		infillVD = voxel.NewVertexDedup()
		infillFaces, _ = voxel.GenerateBoundaryFaces(infillSet, nil, minV, cellSize, layerH, infillVD)

		if !cfg.NoMerge {
			fmt.Printf("  Merging infill faces...")
			tMergeInfill := time.Now()
			before := len(infillFaces)
			infillAssignments := make([]int32, len(infillFaces))
			infillFaces, infillAssignments = voxel.MergeCoplanarTriangles(infillVD.Verts, infillFaces, infillAssignments)
			_ = infillAssignments
			fmt.Printf(" %d -> %d faces in %.1fs\n", before, len(infillFaces), time.Since(tMergeInfill).Seconds())
		}
	}

	// Shell inner boundary: reuse the infill's outer surface with flipped
	// winding order. This guarantees the shell's inner surface is exactly
	// the infill's outer surface — no gaps possible.
	if len(infillFaces) > 0 {
		for _, f := range infillFaces {
			// Flip winding: swap vertices 1 and 2 to reverse normal direction.
			flipped := [3]uint32{
				vd.GetVertex(infillVD.Verts[f[0]]),
				vd.GetVertex(infillVD.Verts[f[2]]),
				vd.GetVertex(infillVD.Verts[f[1]]),
			}
			mcFaces = append(mcFaces, flipped)
			mcAssignments = append(mcAssignments, 0) // palette index 0 (interior)
		}
	}

	shellFaces := mcFaces
	shellAssignments := mcAssignments

	if !cfg.NoMerge {
		fmt.Printf("  Merging shell faces...")
		tMergeShell := time.Now()
		before := len(shellFaces)
		shellFaces, shellAssignments = voxel.MergeCoplanarTriangles(vd.Verts, shellFaces, shellAssignments)
		fmt.Printf(" %d -> %d faces in %.1fs\n", before, len(shellFaces), time.Since(tMergeShell).Seconds())
	}

	parts := []voxel.MeshPart{{
		Model:       buildModel(vd.Verts, shellFaces),
		Assignments: shellAssignments,
	}}

	if len(infillFaces) > 0 {
		infillAssignments := make([]int32, len(infillFaces))
		parts = append(parts, voxel.MeshPart{
			Model:       buildModel(infillVD.Verts, infillFaces),
			Assignments: infillAssignments,
		})
	}

	return parts, pal, nil
}
