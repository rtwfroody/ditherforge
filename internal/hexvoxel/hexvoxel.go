// Package hexvoxel generates a hexagonal voxel shell of a textured mesh.
// Each hexagonal prism in the output corresponds to one column (1.5× nozzle width)
// at one layer height, matching what a slicer actually deposits.
// Isosurface extraction uses marching prisms (wedge decomposition) to produce
// a smooth surface that follows the original model shape.
package hexvoxel

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

// Config holds parameters for hexagonal voxel remeshing.
type Config struct {
	NozzleDiameter float32 // flat-to-flat hex width in mm
	LayerHeight    float32 // Z extrusion per layer in mm
	NoMerge        bool    // skip coplanar triangle merging
}

// Remesh generates a hexagonal voxel shell of the input model using marching
// prisms for smooth isosurface extraction.
func Remesh(model *loader.LoadedModel, pal [][3]uint8, cfg Config, dither bool) (*loader.LoadedModel, []int32, error) {
	if len(model.Vertices) == 0 || len(model.Faces) == 0 {
		return nil, nil, fmt.Errorf("empty model")
	}

	nozzle := cfg.NozzleDiameter
	layerH := cfg.LayerHeight
	// Hex flat-to-flat spacing. Using 1.5× nozzle diameter because at 1.0×
	// the hexes are too small for the slicer to fill reliably — bottom/top
	// surfaces don't become solid. Tested with a 0.4mm nozzle: 1.5× (0.6mm)
	// gives solid first layers while preserving good color resolution.
	hexFlat := nozzle * 1.5
	size := hexFlat / float32(math.Sqrt(3)) // center-to-vertex

	// 1. Bounding box.
	minV, maxV := voxel.ComputeBounds(model.Vertices)
	modelMin, modelMax := minV, maxV
	xyPad := hexFlat * 2
	zPad := layerH * 2
	minV[0] -= xyPad
	minV[1] -= xyPad
	minV[2] -= zPad
	maxV[0] += xyPad
	maxV[1] += xyPad
	maxV[2] += zPad

	// Grid dimensions.
	colStep := 1.5 * size   // horizontal spacing
	rowStep := hexFlat       // vertical spacing (flat-to-flat)
	nCols := int(math.Ceil(float64(maxV[0]-minV[0])/float64(colStep))) + 1
	nRows := int(math.Ceil(float64(maxV[1]-minV[1])/float64(rowStep))) + 1
	nLayers := int(math.Ceil(float64(maxV[2]-minV[2])/float64(layerH))) + 1

	fmt.Printf("  Hex grid: %d cols x %d rows x %d layers\n", nCols, nRows, nLayers)

	// 2. Spatial index.
	si := voxel.NewSpatialIndex(model, hexFlat*2)

	// Precompute hex vertex offsets (flat-top hexagon).
	var hexOffsetsX, hexOffsetsY [6]float32
	for i := 0; i < 6; i++ {
		angle := float64(i) * math.Pi / 3.0
		hexOffsetsX[i] = size * float32(math.Cos(angle))
		hexOffsetsY[i] = size * float32(math.Sin(angle))
	}

	// Helper to compute vertex position.
	vertPos := func(col, row, vertLayer, corner int) [3]float32 {
		cx := minV[0] + float32(col)*colStep
		cy := minV[1] + float32(row)*rowStep
		if col%2 == 1 {
			cy += rowStep / 2
		}
		z := minV[2] + float32(vertLayer)*layerH - layerH/2
		if corner < 6 {
			return [3]float32{cx + hexOffsetsX[corner], cy + hexOffsetsY[corner], z}
		}
		return [3]float32{cx, cy, z}
	}

	// 3. Ray-parity voxelization to determine active hexes and their colors.
	var cells []voxel.ActiveCell
	colorBuf := voxel.NewSearchBuf(len(model.Faces))
	colorRadius := hexFlat * 3

	for col := 0; col < nCols; col++ {
		cx := minV[0] + float32(col)*colStep
		for row := 0; row < nRows; row++ {
			cy := minV[1] + float32(row)*rowStep
			if col%2 == 1 {
				cy += rowStep / 2
			}

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
	proximityRadius := hexFlat
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
		colMin := int(math.Floor(float64(tMinX-minV[0])/float64(colStep))) - 1
		colMax := int(math.Ceil(float64(tMaxX-minV[0])/float64(colStep))) + 1
		layerMin := int(math.Floor(float64(tMinZ-minV[2])/float64(layerH))) - 1
		layerMax := int(math.Ceil(float64(tMaxZ-minV[2])/float64(layerH))) + 1
		if colMin < 0 {
			colMin = 0
		}
		if colMax >= nCols {
			colMax = nCols - 1
		}
		if layerMin < 0 {
			layerMin = 0
		}
		if layerMax >= nLayers {
			layerMax = nLayers - 1
		}
		for col := colMin; col <= colMax; col++ {
			rowOff := float32(0)
			if col%2 == 1 {
				rowOff = rowStep / 2
			}
			rMin := int(math.Floor(float64(tMinY-minV[1]-rowOff)/float64(rowStep))) - 1
			rMax := int(math.Ceil(float64(tMaxY-minV[1]-rowOff)/float64(rowStep))) + 1
			if rMin < 0 {
				rMin = 0
			}
			if rMax >= nRows {
				rMax = nRows - 1
			}
			for row := rMin; row <= rMax; row++ {
				hcx := minV[0] + float32(col)*colStep
				hcy := minV[1] + float32(row)*rowStep + rowOff
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
			cx := minV[0] + float32(k.Col)*colStep
			cy := minV[1] + float32(k.Row)*rowStep
			if k.Col%2 == 1 {
				cy += rowStep / 2
			}
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
		fmt.Printf("  %d active hex prisms (%d added by surface proximity)\n", len(cells), proximityAdded)
	} else {
		fmt.Printf("  %d active hex prisms\n", len(cells))
	}
	if len(cells) == 0 {
		return nil, nil, fmt.Errorf("no active hex prisms found")
	}

	// Build cell assignment map for color lookup during marching prisms.
	cellAssignMap := make(map[voxel.CellKey]int, len(cells))
	activeSet := make(map[voxel.CellKey]struct{}, len(cells))
	for i, c := range cells {
		k := voxel.CellKey{Col: c.Col, Row: c.Row, Layer: c.Layer}
		cellAssignMap[k] = i
		activeSet[k] = struct{}{}
	}

	// Expand active set by neighbor rings.
	lateralOffsets := [2][6][2]int{
		{{+1, 0}, {0, +1}, {-1, 0}, {-1, -1}, {0, -1}, {+1, -1}},
		{{+1, +1}, {0, +1}, {-1, +1}, {-1, 0}, {0, -1}, {+1, 0}},
	}
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
			parity := k.Col & 1
			for _, off := range lateralOffsets[parity] {
				expandedSet[voxel.CellKey{Col: k.Col + off[0], Row: k.Row + off[1], Layer: k.Layer}] = struct{}{}
			}
		}
	}
	fmt.Printf("  %d cells in expanded set (from %d active)\n", len(expandedSet), len(cells))

	// 4. Compute SDF at vertices of expanded cell set.
	fmt.Println("  Computing SDF at cell vertices...")
	searchRadius := hexFlat * 3
	shellThickness := layerH
	boundaryEdges := voxel.BuildBoundaryEdges(model)

	uniqueSet := make(map[[3]float32]struct{})
	for k := range expandedSet {
		for _, vl := range [2]int{k.Layer, k.Layer + 1} {
			for corner := 0; corner <= 6; corner++ {
				uniqueSet[voxel.SnapPos(vertPos(k.Col, k.Row, vl, corner))] = struct{}{}
			}
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

	// 6. Marching prisms isosurface extraction.
	fmt.Println("  Extracting isosurface with marching prisms...")
	vd := voxel.NewVertexDedup()
	outFaces := make([][3]uint32, 0)
	outAssignments := make([]int32, 0)

	for k := range expandedSet {
		// Determine color assignment: use this cell if active, else nearest active neighbor.
		assignment := int32(0)
		if hi, ok := cellAssignMap[k]; ok {
			assignment = assignments[hi]
		} else {
			parity := k.Col & 1
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
				for _, off := range lateralOffsets[parity] {
					nk := voxel.CellKey{Col: k.Col + off[0], Row: k.Row + off[1], Layer: k.Layer}
					if hi, ok := cellAssignMap[nk]; ok {
						assignment = assignments[hi]
						break
					}
				}
			}
		}

		botLayer := k.Layer
		topLayer := k.Layer + 1

		var sdfCornerBot, sdfCornerTop [6]float32
		var posCornerBot, posCornerTop [6][3]float32
		for c := 0; c < 6; c++ {
			posCornerBot[c] = voxel.SnapPos(vertPos(k.Col, k.Row, botLayer, c))
			posCornerTop[c] = voxel.SnapPos(vertPos(k.Col, k.Row, topLayer, c))
			sdfCornerBot[c] = sdfMap[posCornerBot[c]]
			sdfCornerTop[c] = sdfMap[posCornerTop[c]]
		}
		posCenterBot := voxel.SnapPos(vertPos(k.Col, k.Row, botLayer, 6))
		posCenterTop := voxel.SnapPos(vertPos(k.Col, k.Row, topLayer, 6))
		sdfCenterBot := sdfMap[posCenterBot]
		sdfCenterTop := sdfMap[posCenterTop]

		// Process 6 wedges.
		for w := 0; w < 6; w++ {
			next := (w + 1) % 6

			wedgePos := [6][3]float32{
				posCenterBot, posCornerBot[w], posCornerBot[next],
				posCenterTop, posCornerTop[w], posCornerTop[next],
			}
			wedgeSDF := [6]float32{
				sdfCenterBot, sdfCornerBot[w], sdfCornerBot[next],
				sdfCenterTop, sdfCornerTop[w], sdfCornerTop[next],
			}

			caseIdx := 0
			for i := 0; i < 6; i++ {
				if wedgeSDF[i] < 0 {
					caseIdx |= 1 << i
				}
			}

			triEdges := wedgeTriTable[caseIdx]
			if len(triEdges) == 0 {
				continue
			}

			for t := 0; t+2 < len(triEdges); t += 3 {
				var triVerts [3]uint32
				for ki := 0; ki < 3; ki++ {
					edge := triEdges[t+ki]
					ea := wedgeEdges[edge][0]
					eb := wedgeEdges[edge][1]
					posA := wedgePos[ea]
					posB := wedgePos[eb]
					sdfA := wedgeSDF[ea]
					sdfB := wedgeSDF[eb]

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
	}

	fmt.Printf("  %d vertices, %d faces after marching prisms\n", len(vd.Verts), len(outFaces))

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

// --- Marching Prisms (Wedge) Lookup Tables ---

var wedgeEdges = [9][2]int{
	{0, 1}, {1, 2}, {2, 0},
	{3, 4}, {4, 5}, {5, 3},
	{0, 3}, {1, 4}, {2, 5},
}

var wedgeTriTable = [64][]int{
	/*  0 */ {},
	/*  1 */ {0, 6, 2},
	/*  2 */ {0, 1, 7},
	/*  3 */ {6, 1, 7, 6, 2, 1},
	/*  4 */ {1, 2, 8},
	/*  5 */ {6, 1, 0, 6, 8, 1},
	/*  6 */ {0, 2, 8, 7, 0, 8},
	/*  7 */ {7, 6, 8},
	/*  8 */ {3, 5, 6},
	/*  9 */ {3, 5, 0, 5, 2, 0},
	/* 10 */ {0, 1, 7, 6, 3, 5},
	/* 11 */ {1, 7, 3, 1, 3, 5, 1, 5, 2},
	/* 12 */ {2, 8, 1, 6, 3, 5},
	/* 13 */ {0, 3, 1, 1, 3, 5, 1, 5, 8},
	/* 14 */ {6, 3, 5, 0, 8, 7, 0, 2, 8},
	/* 15 */ {7, 3, 5, 7, 5, 8},
	/* 16 */ {7, 4, 3},
	/* 17 */ {7, 4, 3, 0, 6, 2},
	/* 18 */ {0, 1, 3, 1, 4, 3},
	/* 19 */ {1, 4, 3, 1, 3, 6, 1, 6, 2},
	/* 20 */ {7, 4, 3, 2, 8, 1},
	/* 21 */ {7, 4, 3, 6, 1, 0, 6, 8, 1},
	/* 22 */ {0, 4, 3, 0, 8, 4, 0, 2, 8},
	/* 23 */ {6, 8, 3, 3, 8, 4},
	/* 24 */ {6, 7, 4, 6, 4, 5},
	/* 25 */ {0, 7, 5, 7, 4, 5, 2, 0, 5},
	/* 26 */ {1, 6, 0, 1, 5, 6, 1, 4, 5},
	/* 27 */ {2, 1, 5, 5, 1, 4},
	/* 28 */ {2, 8, 1, 6, 7, 5, 7, 4, 5},
	/* 29 */ {0, 7, 5, 7, 4, 5, 0, 5, 1, 1, 5, 8},
	/* 30 */ {0, 2, 8, 0, 8, 4, 0, 4, 5, 0, 5, 6},
	/* 31 */ {8, 4, 5},
	/* 32 */ {4, 8, 5},
	/* 33 */ {4, 8, 5, 0, 6, 2},
	/* 34 */ {4, 8, 5, 0, 1, 7},
	/* 35 */ {4, 8, 5, 6, 1, 7, 6, 2, 1},
	/* 36 */ {1, 5, 4, 2, 5, 1},
	/* 37 */ {1, 5, 4, 1, 6, 5, 1, 0, 6},
	/* 38 */ {5, 4, 7, 5, 7, 0, 5, 0, 2},
	/* 39 */ {6, 4, 7, 6, 5, 4},
	/* 40 */ {6, 3, 8, 3, 4, 8},
	/* 41 */ {0, 3, 4, 0, 4, 8, 0, 8, 2},
	/* 42 */ {7, 0, 1, 6, 3, 4, 6, 4, 8},
	/* 43 */ {1, 7, 3, 1, 3, 2, 2, 3, 8, 8, 3, 4},
	/* 44 */ {2, 6, 1, 6, 3, 1, 3, 4, 1},
	/* 45 */ {0, 3, 1, 1, 3, 4},
	/* 46 */ {7, 0, 4, 4, 0, 2, 4, 2, 3, 3, 2, 6},
	/* 47 */ {7, 3, 4},
	/* 48 */ {7, 8, 5, 7, 5, 3},
	/* 49 */ {0, 6, 2, 7, 8, 5, 7, 5, 3},
	/* 50 */ {0, 1, 3, 1, 5, 3, 1, 8, 5},
	/* 51 */ {2, 1, 6, 6, 1, 3, 5, 1, 8, 3, 1, 5},
	/* 52 */ {1, 3, 7, 1, 5, 3, 1, 2, 5},
	/* 53 */ {1, 0, 6, 1, 6, 5, 1, 5, 7, 7, 5, 3},
	/* 54 */ {0, 2, 5, 0, 5, 3},
	/* 55 */ {3, 6, 5},
	/* 56 */ {7, 8, 6},
	/* 57 */ {0, 7, 8, 0, 8, 2},
	/* 58 */ {0, 1, 6, 1, 8, 6},
	/* 59 */ {2, 1, 8},
	/* 60 */ {6, 7, 1, 6, 1, 2},
	/* 61 */ {0, 7, 1},
	/* 62 */ {0, 2, 6},
	/* 63 */ {},
}
