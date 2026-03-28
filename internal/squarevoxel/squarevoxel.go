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
	NoMerge        bool
}

// Remesh generates a square voxel shell of the input model using marching cubes.
func Remesh(model *loader.LoadedModel, pal [][3]uint8, cfg Config, dither bool) (*loader.LoadedModel, []int32, error) {
	if len(model.Vertices) == 0 || len(model.Faces) == 0 {
		return nil, nil, fmt.Errorf("empty model")
	}

	cellSize := cfg.NozzleDiameter
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
				clr := voxel.SampleNearestColor(
					[3]float32{cx, cy, cz},
					model, si, colorRadius, colorBuf)
				cells = append(cells, voxel.ActiveCell{
					Col: col, Row: row, Layer: layer,
					Cx: cx, Cy: cy, Cz: cz,
					Color: clr,
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
			clr := voxel.SampleNearestColor(
				[3]float32{cx, cy, cz},
				model, si, colorRadius, colorBuf)
			cells = append(cells, voxel.ActiveCell{
				Col: k.Col, Row: k.Row, Layer: k.Layer,
				Cx: cx, Cy: cy, Cz: cz,
				Color: clr,
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

	// Expand active set by 2 rings of neighbors.
	lateralOffsets := [4][2]int{{+1, 0}, {-1, 0}, {0, +1}, {0, -1}}
	expandedSet := make(map[voxel.CellKey]struct{}, len(cells)*2)
	for k := range activeSet {
		expandedSet[k] = struct{}{}
	}
	for ring := 0; ring < 2; ring++ {
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
	shellThickness := layerH
	boundaryEdges := voxel.BuildBoundaryEdges(model)
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

	uniqueSet := make(map[[3]float32]struct{})
	for k := range expandedSet {
		for corner := 0; corner < 8; corner++ {
			uniqueSet[voxel.SnapPos(vertPos(k.Col, k.Row, k.Layer, corner))] = struct{}{}
		}
	}
	uniqueVerts := make([][3]float32, 0, len(uniqueSet))
	for pos := range uniqueSet {
		uniqueVerts = append(uniqueVerts, pos)
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
				sdfValues[i] = voxel.ComputeSDF(uniqueVerts[i], model, si, searchRadius, shellThickness, boundaryEdges, modelMin, modelMax, buf)
			}
		}(start, end)
	}
	wg.Wait()

	sdfMap := make(map[[3]float32]float32, len(uniqueVerts))
	for i, pos := range uniqueVerts {
		sdfMap[pos] = sdfValues[i]
	}
	fmt.Printf("  %d unique SDF vertices computed\n", len(sdfMap))

	// 5. Palette assignment / dithering.
	var assignments []int32
	if dither {
		assignments = voxel.DitherCells(cells, pal)
	} else {
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
			cornerSDF[c] = sdfMap[cornerPos[c]]
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

	// Build output model.
	uvs := make([][2]float32, len(vd.Verts))
	faceTex := make([]int32, len(outFaces))

	placeholder := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	placeholder.SetNRGBA(0, 0, color.NRGBA{128, 128, 128, 255})
	var textures []image.Image
	if len(model.Textures) > 0 {
		textures = model.Textures[:1]
	} else {
		textures = []image.Image{placeholder}
	}

	return &loader.LoadedModel{
		Vertices:       vd.Verts,
		Faces:          outFaces,
		UVs:            uvs,
		Textures:       textures,
		FaceTextureIdx: faceTex,
	}, outAssignments, nil
}
