// Package squarevoxel generates a square voxel shell of a textured mesh.
// Each cube cell has edge length equal to the nozzle diameter, giving finer
// resolution than hexvoxel mode (which uses 1.5× nozzle).
// Isosurface extraction uses marching cubes for a smooth surface.
package squarevoxel

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"runtime"
	"sync"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// Config holds parameters for square voxel remeshing.
type Config struct {
	NozzleDiameter float32
	LayerHeight    float32
	WallThickness  float32 // shell thickness in mm (default 3.0)
	NoMerge        bool
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

	fmt.Printf("  Square grid: %d cols x %d rows x %d layers\n", nCols, nRows, nLayers)

	// 2. Spatial index.
	si := voxel.NewSpatialIndex(model, cellSize*2)

	// 3. Z-ray voxelization.
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
	if proximityAdded > 0 {
		fmt.Printf("  %d active cells (%d added by surface proximity)\n", len(cells), proximityAdded)
	} else {
		fmt.Printf("  %d active cells\n", len(cells))
	}
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

	// Expand active set by enough rings for wall thickness + marching cubes margin.
	wallThickness := cfg.WallThickness
	if wallThickness <= 0 {
		wallThickness = 3.0
	}
	expansionRings := int(math.Ceil(float64(wallThickness)/float64(cellSize))) + 1
	lateralOffsets := [4][2]int{{+1, 0}, {-1, 0}, {0, +1}, {0, -1}}
	expandedSet := make(map[voxel.CellKey]struct{}, len(cells)*2)
	for k := range activeSet {
		expandedSet[k] = struct{}{}
	}
	for ring := 0; ring < expansionRings; ring++ {
		snapshot := make([]voxel.CellKey, 0, len(expandedSet))
		for k := range expandedSet {
			snapshot = append(snapshot, k)
		}
		for _, k := range snapshot {
			expandedSet[voxel.CellKey{Col: k.Col, Row: k.Row, Layer: k.Layer - 1}] = struct{}{}
			expandedSet[voxel.CellKey{Col: k.Col, Row: k.Row, Layer: k.Layer + 1}] = struct{}{}
			for _, off := range lateralOffsets {
				expandedSet[voxel.CellKey{Col: k.Col + off[0], Row: k.Row + off[1], Layer: k.Layer}] = struct{}{}
			}
		}
	}
	fmt.Printf("  %d cells in expanded set (from %d active)\n", len(expandedSet), len(cells))

	// 4. Compute SDF at cube vertices.
	fmt.Println("  Computing SDF at cube vertices...")
	searchRadius := cellSize * 3
	if wallThickness*2 > searchRadius {
		searchRadius = wallThickness * 2
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
	for k := range expandedSet {
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
	fmt.Printf("  %d unique SDF vertices computed\n", len(vertIndex))
	uniqueVerts = nil // free; only vertIndex+sdfValues needed from here

	// 5. Resolve palette and assign / dither.
	pal, palDisplay := voxel.ResolvePalette(cells, pcfg)
	if palDisplay != "" {
		fmt.Println(palDisplay)
	}
	if len(pal) == 0 {
		return nil, nil, fmt.Errorf("no palette colors")
	}
	var assignments []int32
	switch ditherMode {
	case "dizzy":
		assignments = voxel.DitherCellsDizzy(cells, pal)
	case "fs":
		assignments = voxel.DitherCellsFS(cells, pal)
	default:
		assignments = voxel.AssignColors(cells, pal)
	}

	// 6. Marching cubes isosurface extraction.
	fmt.Println("  Extracting isosurface with marching cubes...")
	vd := voxel.NewVertexDedup()
	outFaces := make([][3]uint32, 0)
	outAssignments := make([]int32, 0)

	for k := range expandedSet {
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
			outFaces = append(outFaces, [3]uint32{triVerts[0], triVerts[1], triVerts[2]})
			outAssignments = append(outAssignments, assignment)
		}
	}

	fmt.Printf("  %d vertices, %d faces after marching cubes\n", len(vd.Verts), len(outFaces))

	if !cfg.NoMerge {
		before := len(outFaces)
		outFaces, outAssignments = voxel.MergeCoplanarTriangles(vd.Verts, outFaces, outAssignments)
		fmt.Printf("  %d faces after coplanar merge (%.0f%% reduction)\n",
			len(outFaces), 100*float64(before-len(outFaces))/float64(before))
	}

	// Build shell output model.
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

	parts := []voxel.MeshPart{{
		Model:       buildModel(vd.Verts, outFaces),
		Assignments: outAssignments,
	}}

	// Generate infill mesh: a second marching cubes pass with SDF offset
	// inward by wallThickness. This creates a solid interior object that
	// the slicer can infill with just the first filament.
	fmt.Println("  Generating infill mesh...")
	infillVD := voxel.NewVertexDedup()
	var infillFaces [][3]uint32

	for k := range expandedSet {
		var cornerPos [8][3]float32
		var cornerSDF [8]float32
		for c := 0; c < 8; c++ {
			cornerPos[c] = voxel.SnapPos(vertPos(k.Col, k.Row, k.Layer, c))
			// Offset SDF inward: points within wallThickness of surface become positive (outside).
			cornerSDF[c] = sdfValues[vertIndex[cornerPos[c]]] + wallThickness
		}

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

				if posA[0] > posB[0] || (posA[0] == posB[0] && posA[1] > posB[1]) ||
					(posA[0] == posB[0] && posA[1] == posB[1] && posA[2] > posB[2]) {
					posA, posB = posB, posA
					sdfA, sdfB = sdfB, sdfA
				}

				var interp [3]float32
				denom := sdfA - sdfB
				if denom == 0 {
					interp = [3]float32{
						(posA[0] + posB[0]) / 2,
						(posA[1] + posB[1]) / 2,
						(posA[2] + posB[2]) / 2,
					}
				} else {
					frac := sdfA / denom
					frac = voxel.ClampF(frac, 0, 1)
					interp = [3]float32{
						posA[0] + frac*(posB[0]-posA[0]),
						posA[1] + frac*(posB[1]-posA[1]),
						posA[2] + frac*(posB[2]-posA[2]),
					}
				}
				triVerts[ki] = infillVD.GetVertex(interp)
			}
			if triVerts[0] == triVerts[1] || triVerts[1] == triVerts[2] || triVerts[0] == triVerts[2] {
				continue
			}
			infillFaces = append(infillFaces, [3]uint32{triVerts[0], triVerts[1], triVerts[2]})
		}
	}

	if len(infillFaces) > 0 {
		fmt.Printf("  %d vertices, %d faces in infill mesh\n", len(infillVD.Verts), len(infillFaces))
		infillAssign := make([]int32, len(infillFaces)) // all zero = palette[0]
		parts = append(parts, voxel.MeshPart{
			Model:       buildModel(infillVD.Verts, infillFaces),
			Assignments: infillAssign,
		})
	}

	return parts, pal, nil
}
