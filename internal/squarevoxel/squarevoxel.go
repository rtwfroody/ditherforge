// Package squarevoxel generates a square voxel shell of a textured mesh.
// Each cube cell has edge length ~1.275× the nozzle diameter.
// Isosurface extraction uses marching cubes for a smooth surface.
package squarevoxel

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/voxel"
	"github.com/schollz/progressbar/v3"
	"golang.org/x/term"
)

// Config holds parameters for square voxel remeshing.
type Config struct {
	NozzleDiameter float32
	LayerHeight    float32
	WallThickness  float32 // shell thickness in mm (default 3.0)
	NoMerge        bool
	Infill         bool // generate infill object inside the shell
	MinFeatureSize float32 // minimum feature size in mm (0 to disable)
}

var isTTY = term.IsTerminal(int(os.Stderr.Fd()))

func newBar(total int, description string) *progressbar.ProgressBar {
	if !isTTY {
		// Non-interactive: return a silent bar.
		return progressbar.NewOptions(total,
			progressbar.OptionSetVisibility(false),
		)
	}
	return progressbar.NewOptions(total,
		progressbar.OptionSetDescription(description),
		progressbar.OptionSetWidth(30),
		progressbar.OptionShowCount(),
		progressbar.OptionClearOnFinish(),
		progressbar.OptionThrottle(500*time.Millisecond),
	)
}

func finishBar(bar *progressbar.ProgressBar, description string, detail string, elapsed time.Duration) {
	bar.Finish()
	fmt.Printf("  %s %s in %.1fs\n", description, detail, elapsed.Seconds())
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

	// 3. Triangle-overlap voxelization.
	// For each triangle, mark all cells whose AABB overlaps the triangle.
	tVoxelize := time.Now()
	bar := newBar(len(model.Faces), "  Voxelizing")
	halfExtent := [3]float32{cellSize / 2, cellSize / 2, layerH / 2}
	cellSet := make(map[voxel.CellKey]struct{})
	for fi := range model.Faces {
		bar.Add(1)
		f := model.Faces[fi]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]
		// Bounding box of triangle in grid coordinates.
		tMinX := min(v0[0], min(v1[0], v2[0]))
		tMaxX := max(v0[0], max(v1[0], v2[0]))
		tMinY := min(v0[1], min(v1[1], v2[1]))
		tMaxY := max(v0[1], max(v1[1], v2[1]))
		tMinZ := min(v0[2], min(v1[2], v2[2]))
		tMaxZ := max(v0[2], max(v1[2], v2[2]))
		colMin := int(math.Floor(float64(tMinX-minV[0])/float64(cellSize) - 0.5))
		colMax := int(math.Ceil(float64(tMaxX-minV[0])/float64(cellSize) + 0.5))
		rowMin := int(math.Floor(float64(tMinY-minV[1])/float64(cellSize) - 0.5))
		rowMax := int(math.Ceil(float64(tMaxY-minV[1])/float64(cellSize) + 0.5))
		layerMin := int(math.Floor(float64(tMinZ-minV[2])/float64(layerH) - 0.5))
		layerMax := int(math.Ceil(float64(tMaxZ-minV[2])/float64(layerH) + 0.5))
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
			cx := minV[0] + float32(col)*cellSize
			for row := rowMin; row <= rowMax; row++ {
				cy := minV[1] + float32(row)*cellSize
				for layer := layerMin; layer <= layerMax; layer++ {
					cz := minV[2] + float32(layer)*layerH
					center := [3]float32{cx, cy, cz}
					if voxel.TriangleAABBOverlap(v0, v1, v2, center, halfExtent) {
						cellSet[voxel.CellKey{Col: col, Row: row, Layer: layer}] = struct{}{}
					}
				}
			}
		}
	}
	finishBar(bar, "Voxelized", fmt.Sprintf("%d cells", len(cellSet)), time.Since(tVoxelize))

	// Color each active cell.
	tColor := time.Now()
	colorBuf := voxel.NewSearchBuf(len(model.Faces))
	colorRadius := cellSize * 3
	var cells []voxel.ActiveCell
	barColor := newBar(len(cellSet), "  Coloring cells")
	for k := range cellSet {
		barColor.Add(1)
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
	}
	finishBar(barColor, "Colored cells", fmt.Sprintf("%d cells", len(cells)), time.Since(tColor))
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

		tInterior := time.Now()
		barInterior := newBar(nCols, "  Finding interior")
		interiorSet := make(map[voxel.CellKey]struct{}, len(cells)*4)
		for k := range activeSet {
			interiorSet[k] = struct{}{}
		}
		for col := 0; col < nCols; col++ {
			barInterior.Add(1)
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
		finishBar(barInterior, "Found interior", fmt.Sprintf("%d cells", len(interiorSet)-len(activeSet)), time.Since(tInterior))

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
		// No infill: expand active set by 2 rings for MC interpolation.
		expansionRings := 2
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
	tPrep := time.Now()
	barPrep := newBar(-1, "  Preparing SDF")
	searchRadius := cellSize * 3
	shellThickness := layerH
	pn := voxel.BuildPseudonormals(model)

	// Corner grid: each cell (Col, Row, Layer) has 8 corners at integer
	// offsets (Col+di, Row+dj, Layer+dk) where di, dj, dk ∈ {0, 1}.
	// The corner grid has (nCols+1) × (nRows+1) × (nLayers+1) positions.
	cornerStride := [2]int32{int32(nCols + 1), int32((nCols + 1) * (nRows + 1))}
	cornerIdx := func(ci, cj, ck int) int32 {
		return int32(ci) + int32(cj)*cornerStride[0] + int32(ck)*cornerStride[1]
	}
	cornerPos := func(ci, cj, ck int) [3]float32 {
		return [3]float32{
			minV[0] + (float32(ci)-0.5)*cellSize,
			minV[1] + (float32(cj)-0.5)*cellSize,
			minV[2] + (float32(ck)-0.5)*layerH,
		}
	}
	// Integer offsets for the 8 cube corners relative to the cell.
	// Corner numbering matches the marching cubes table:
	//   0: (0,0,0)  1: (1,0,0)  2: (1,1,0)  3: (0,1,0)
	//   4: (0,0,1)  5: (1,0,1)  6: (1,1,1)  7: (0,1,1)
	cornerOffsets := [8][3]int{{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0},
		{0, 0, 1}, {1, 0, 1}, {1, 1, 1}, {0, 1, 1}}

	// Build a sparse map from corner index to SDF array index.
	vertIndex := make(map[int32]int32, len(shellExpandedSet)*4)
	for k := range shellExpandedSet {
		for _, off := range cornerOffsets {
			ci, cj, ck := k.Col+off[0], k.Row+off[1], k.Layer+off[2]
			idx := cornerIdx(ci, cj, ck)
			if _, ok := vertIndex[idx]; !ok {
				vertIndex[idx] = int32(len(vertIndex))
			}
		}
	}
	// Build flat arrays of positions for parallel SDF evaluation.
	uniqueVerts := make([][3]float32, len(vertIndex))
	for gridIdx, sdfIdx := range vertIndex {
		ci := gridIdx % cornerStride[0]
		cj := (gridIdx / cornerStride[0]) % int32(nRows+1)
		ck := gridIdx / cornerStride[1]
		uniqueVerts[sdfIdx] = cornerPos(int(ci), int(cj), int(ck))
	}
	finishBar(barPrep, "Prepared SDF", fmt.Sprintf("%d vertices", len(uniqueVerts)), time.Since(tPrep))

	tSDF := time.Now()
	nWorkers := runtime.NumCPU()
	sdfValues := make([]float32, len(uniqueVerts))
	barSDF := newBar(len(uniqueVerts), "  Computing SDF")
	var sdfProgress atomic.Int64
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
			localCount := 0
			for i := start; i < end; i++ {
				sdfValues[i] = voxel.ComputeSDF(uniqueVerts[i], model, si, searchRadius, shellThickness, pn, modelMin, modelMax, buf)
				localCount++
				if localCount%100 == 0 {
					barSDF.Add(localCount)
					sdfProgress.Add(int64(localCount))
					localCount = 0
				}
			}
			if localCount > 0 {
				barSDF.Add(localCount)
				sdfProgress.Add(int64(localCount))
			}
		}(start, end)
	}
	wg.Wait()
	finishBar(barSDF, "Computed SDF", fmt.Sprintf("%d vertices", len(vertIndex)), time.Since(tSDF))
	uniqueVerts = nil // free; only vertIndex+sdfValues needed from here

	// 5. Resolve palette and assign / dither.
	pal, palDisplay := voxel.ResolvePalette(cells, pcfg, ditherMode != "none")
	if palDisplay != "" {
		fmt.Printf("%s\n", palDisplay)
	}
	if len(pal) == 0 {
		return nil, nil, fmt.Errorf("no palette colors")
	}
	tDither := time.Now()
	barDither := newBar(-1, fmt.Sprintf("  Dithering (%s)", ditherMode))
	var assignments []int32
	switch ditherMode {
	case "dizzy":
		assignments = voxel.DitherCellsDizzy(cells, pal)
	case "fs":
		assignments = voxel.DitherCellsFS(cells, pal)
	default:
		assignments = voxel.AssignColors(cells, pal)
	}
	finishBar(barDither, fmt.Sprintf("Dithered (%s)", ditherMode), fmt.Sprintf("%d cells", len(cells)), time.Since(tDither))

	// 5b. Enforce minimum feature thickness by clamping SDF.
	// For all interior points where |SDF| < t/2, clamp to -t/2. This pushes
	// the zero-level-set outward at thin regions while preserving thick
	// geometry exactly.
	minFeature := cfg.MinFeatureSize
	if minFeature < 0 {
		minFeature = cellSize
	}
	if minFeature > 0 {
		tThin := time.Now()
		halfT := minFeature / 2
		clamped := 0
		for i, sdf := range sdfValues {
			if sdf < 0 && sdf > -halfT {
				sdfValues[i] = -halfT
				clamped++
			}
		}
		fmt.Printf("  Min feature size %.2fmm: %d corners clamped in %.1fs\n", minFeature, clamped, time.Since(tThin).Seconds())
	}

	// 6. Marching cubes isosurface extraction for smooth outer surface.
	tMC := time.Now()
	barMC := newBar(len(shellExpandedSet), "  Marching cubes")
	vd := voxel.NewVertexDedup()
	mcFaces := make([][3]uint32, 0)
	mcAssignments := make([]int32, 0)

	for k := range shellExpandedSet {
		barMC.Add(1)
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
		var cPos [8][3]float32
		var cornerSDF [8]float32
		for c := 0; c < 8; c++ {
			off := cornerOffsets[c]
			ci, cj, ck := k.Col+off[0], k.Row+off[1], k.Layer+off[2]
			cPos[c] = cornerPos(ci, cj, ck)
			cornerSDF[c] = sdfValues[vertIndex[cornerIdx(ci, cj, ck)]]
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
				posA := cPos[ea]
				posB := cPos[eb]
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

	finishBar(barMC, "Marching cubes", fmt.Sprintf("%d faces", len(mcFaces)), time.Since(tMC))

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
			tMergeInfill := time.Now()
			barMergeInfill := newBar(-1, "  Merging infill")
			before := len(infillFaces)
			infillAssignments := make([]int32, len(infillFaces))
			infillFaces, infillAssignments = voxel.MergeCoplanarTriangles(infillVD.Verts, infillFaces, infillAssignments)
			_ = infillAssignments
			finishBar(barMergeInfill, "Merged infill", fmt.Sprintf("%d -> %d faces", before, len(infillFaces)), time.Since(tMergeInfill))
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
		tMergeShell := time.Now()
		barMergeShell := newBar(-1, "  Merging shell")
		before := len(shellFaces)
		shellFaces, shellAssignments = voxel.MergeCoplanarTriangles(vd.Verts, shellFaces, shellAssignments)
		finishBar(barMergeShell, "Merged shell", fmt.Sprintf("%d -> %d faces", before, len(shellFaces)), time.Since(tMergeShell))
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
