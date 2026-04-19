package tests

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	colorful "github.com/lucasb-eyer/go-colorful"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/render"
	"github.com/rtwfroody/ditherforge/internal/squarevoxel"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

const (
	defaultNozzle      = float32(0.4)
	defaultLayerHeight = float32(0.2)
	maxExtentMM        = float32(50)
	testResolution     = 512
	marginFrac         = 0.05
	// Only ever tighten this requirement.
	minCoverage        = 0.98
	maxDepthDiff       = 20 // p95 gray level difference (0-255)
)

var views = []struct {
	name      string
	azimuth   float64
	elevation float64
}{
	{"front", 90, 20},
	{"left", 0, 20},
	{"top", 0, 90},
}

func defaultPaletteConfig() voxel.PaletteConfig {
	defaultColors := []string{"cyan", "magenta", "yellow", "black", "white", "red", "green", "blue"}
	var pcfg voxel.PaletteConfig
	pcfg.NumColors = 4
	for _, name := range defaultColors {
		rgb, _ := palette.ParsePalette([]string{name})
		pcfg.Inventory = append(pcfg.Inventory, palette.InventoryEntry{Color: rgb[0], Label: name})
	}
	return pcfg
}

type remeshResult struct {
	model       *loader.LoadedModel
	outModel    *loader.LoadedModel
	assignments []int32
	paletteRGB  [][3]uint8
	err         error
}

type remeshEntry struct {
	once   sync.Once
	result *remeshResult
}

var (
	remeshCache   = map[string]*remeshEntry{}
	remeshCacheMu sync.Mutex
)

// getRemeshResult loads the model and returns a shared remesh result for the
// given model path. The first caller runs the load+remesh; concurrent and
// subsequent callers block on sync.Once and reuse the result.
func getRemeshResult(t *testing.T, modelPath string) *remeshResult {
	t.Helper()

	remeshCacheMu.Lock()
	entry, ok := remeshCache[modelPath]
	if !ok {
		entry = &remeshEntry{}
		remeshCache[modelPath] = entry
	}
	remeshCacheMu.Unlock()

	entry.once.Do(func() {
		model := loadTestModel(t, modelPath)
		ctx := context.Background()
		cellSize := defaultNozzle * 1.275
		layerH := defaultLayerHeight

		t.Log("Running pipeline stages...")
		cells, _, minV, err := squarevoxel.Voxelize(ctx, model, model, cellSize, layerH, progress.NullTracker{}, nil)
		if err != nil {
			entry.result = &remeshResult{err: err}
			return
		}

		decimModel, err := squarevoxel.DecimateMesh(ctx, model, len(cells), cellSize, false, progress.NullTracker{})
		if err != nil {
			entry.result = &remeshResult{err: err}
			return
		}

		pal, _, _, err := voxel.ResolvePalette(context.Background(), cells, defaultPaletteConfig(), true, progress.NullTracker{})
		if err != nil {
			entry.result = &remeshResult{err: err}
			return
		}
		assignments, err := voxel.DitherCellsDizzy(ctx, cells, pal)
		if err != nil {
			entry.result = &remeshResult{err: err}
			return
		}

		var ffCounter atomic.Int64
		patchMap, numPatches, err := voxel.FloodFillPatches(ctx, cells, assignments, progress.NullTracker{}, &ffCounter)
		if err != nil {
			entry.result = &remeshResult{err: err}
			return
		}
		patchAssignment := make([]int32, numPatches)
		for i, c := range cells {
			k := voxel.CellKey{Grid: c.Grid, Col: c.Col, Row: c.Row, Layer: c.Layer}
			patchAssignment[patchMap[k]] = assignments[i]
		}

		shellVerts, shellFaces, shellAssignments, err := voxel.ClipMeshByPatches(
			ctx, decimModel, patchMap, patchAssignment, minV, cellSize, layerH)
		if err != nil {
			entry.result = &remeshResult{err: err}
			return
		}

		shellFaces, shellAssignments, err = voxel.MergeCoplanarTriangles(ctx, shellVerts, shellFaces, shellAssignments, nil)
		if err != nil {
			entry.result = &remeshResult{err: err}
			return
		}

		outModel := &loader.LoadedModel{
			Vertices: shellVerts,
			Faces:    shellFaces,
		}

		entry.result = &remeshResult{model, outModel, shellAssignments, pal, nil}
	})

	if entry.result.err != nil {
		t.Fatalf("Remesh: %v", entry.result.err)
	}
	return entry.result
}

// modelExtent returns the max bounding box extent in mm.
func modelExtent(model *loader.LoadedModel) float32 {
	minV, maxV := model.Vertices[0], model.Vertices[0]
	for _, v := range model.Vertices[1:] {
		for i := 0; i < 3; i++ {
			if v[i] < minV[i] {
				minV[i] = v[i]
			}
			if v[i] > maxV[i] {
				maxV[i] = v[i]
			}
		}
	}
	ext := float32(0)
	for i := 0; i < 3; i++ {
		d := maxV[i] - minV[i]
		if d > ext {
			ext = d
		}
	}
	return ext
}

func computeDilatePx(nozzleMM, layerHeightMM, modelExtentMM float64) int {
	hexFlat := nozzleMM * 1.5
	cellDiag := math.Sqrt(hexFlat*hexFlat + math.Max(hexFlat, layerHeightMM)*math.Max(hexFlat, layerHeightMM))
	cellNormalized := cellDiag / modelExtentMM
	pixelsPerUnit := float64(testResolution) * (1 - 2*marginFrac)
	dilatePx := int(math.Ceil(1.5 * cellNormalized * pixelsPerUnit))
	dilatePx++ // alignment rounding
	return dilatePx
}

func centroid(mask []bool, width, height int) (float64, float64) {
	var sx, sy float64
	var n int
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if mask[y*width+x] {
				sx += float64(x)
				sy += float64(y)
				n++
			}
		}
	}
	if n == 0 {
		return -1, -1
	}
	return sx / float64(n), sy / float64(n)
}

func shiftDepth(img *render.DepthImage, dx, dy int) *render.DepthImage {
	out := &render.DepthImage{
		Width:  img.Width,
		Height: img.Height,
		Depth:  make([]float64, len(img.Depth)),
	}
	for i := range out.Depth {
		out.Depth[i] = math.NaN()
	}
	for y := 0; y < img.Height; y++ {
		ny := y + dy
		if ny < 0 || ny >= img.Height {
			continue
		}
		for x := 0; x < img.Width; x++ {
			nx := x + dx
			if nx < 0 || nx >= img.Width {
				continue
			}
			out.Depth[ny*img.Width+nx] = img.Depth[y*img.Width+x]
		}
	}
	return out
}

func percentile(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sort.Float64s(vals)
	idx := p / 100 * float64(len(vals)-1)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	if lo == hi || hi >= len(vals) {
		return vals[lo]
	}
	frac := idx - float64(lo)
	return vals[lo]*(1-frac) + vals[hi]*frac
}

// loadTestModel loads a GLB or 3MF file, scaling to maxExtentMM.
func loadTestModel(t *testing.T, path string) *loader.LoadedModel {
	t.Helper()
	ext := strings.ToLower(filepath.Ext(path))

	var model *loader.LoadedModel
	var err error
	var unitScale float32 = 1
	t.Logf("Loading %s...", path)
	switch ext {
	case ".glb":
		model, err = loader.LoadGLB(path, -1)
		unitScale = 1000 // meters → mm
	case ".3mf":
		model, err = loader.Load3MF(path, -1)
	default:
		t.Fatalf("unsupported extension %q", ext)
	}
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	loader.ScaleModel(model, unitScale)

	me := modelExtent(model)
	if me != maxExtentMM {
		scale := maxExtentMM / me
		t.Logf("  Extent %.1fmm, target %.0fmm, scaling by %.4f", me, maxExtentMM, scale)
		loader.ScaleModel(model, scale)
	}
	return model
}

// discoverTestModels finds all GLB and 3MF files in objects/.
func discoverTestModels(t *testing.T) []string {
	t.Helper()
	var paths []string
	for _, pattern := range []string{"objects/*.glb", "objects/*.3mf"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("globbing %s: %v", pattern, err)
		}
		paths = append(paths, matches...)
	}
	if len(paths) == 0 {
		t.Skip("no model files in objects/")
	}
	return paths
}

func TestMeshRender(t *testing.T) {
	modelPaths := discoverTestModels(t)

	outdir := "output"

	for _, modelPath := range modelPaths {
		modelPath := modelPath
		base := filepath.Base(modelPath)
		name := strings.TrimSuffix(base, filepath.Ext(base))
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := getRemeshResult(t, modelPath)
			model, outModel := r.model, r.outModel
			ext := modelExtent(model)
			t.Logf("  Input: %d verts, %d faces, extent %.1fmm",
				len(model.Vertices), len(model.Faces), ext)
			t.Logf("  Output: %d verts, %d faces", len(outModel.Vertices), len(outModel.Faces))

			// Check mesh quality. Boundary edges indicate a pipeline
			// bug (mismatched clip planes). Non-manifold edges come from
			// self-intersecting input geometry and aren't our fault, but
			// the pipeline shouldn't make them dramatically worse.
			inWt := voxel.CheckWatertight(model.Faces)
			outWt := voxel.CheckWatertight(outModel.Faces)
			t.Logf("  Input:  %s", inWt)
			t.Logf("  Output: %s", outWt)
			if len(inWt.BoundaryEdges) == 0 && len(outWt.BoundaryEdges) > 0 {
				t.Errorf("  pipeline introduced %d boundary edges", len(outWt.BoundaryEdges))
			} else if len(outWt.BoundaryEdges) > len(inWt.BoundaryEdges)*3 {
				t.Errorf("  boundary edges grew from %d to %d (>3x)",
					len(inWt.BoundaryEdges), len(outWt.BoundaryEdges))
			}
			// Allow non-manifold edges up to 0.5% of output faces, since
			// they reflect input self-intersections amplified by clipping.
			// Open meshes (with boundary edges) produce more, so add those.
			nmLimit := len(outModel.Faces)/200 + len(inWt.BoundaryEdges)
			if nmLimit < len(inWt.NonManifoldEdges) {
				nmLimit = len(inWt.NonManifoldEdges)
			}
			if len(outWt.NonManifoldEdges) > nmLimit {
				t.Errorf("  too many non-manifold edges: %d (limit %d)",
					len(outWt.NonManifoldEdges), nmLimit)
			}

			dilatePx := computeDilatePx(float64(defaultNozzle), float64(defaultLayerHeight), float64(ext))
			t.Logf("  Tolerance: %dpx", dilatePx)

			os.MkdirAll(outdir, 0755)

			for _, v := range views {
				inpBounds := render.ProjectedBounds(model.Vertices, v.azimuth, v.elevation)
				outBounds := render.ProjectedBounds(outModel.Vertices, v.azimuth, v.elevation)
				bounds := render.UnionBounds(inpBounds, outBounds)

				inpImg := render.Render(model.Vertices, model.Faces, v.azimuth, v.elevation, testResolution, bounds)
				outRaw := render.Render(outModel.Vertices, outModel.Faces, v.azimuth, v.elevation, testResolution, bounds)

				// Align output to input by centroid matching.
				inpMask := inpImg.Mask()
				outRawMask := outRaw.Mask()
				ix, iy := centroid(inpMask, testResolution, testResolution)
				ox, oy := centroid(outRawMask, testResolution, testResolution)
				dx, dy := 0, 0
				if ix >= 0 && ox >= 0 {
					dx = int(math.Round(ix - ox))
					dy = int(math.Round(iy - oy))
				}
				outImg := outRaw
				if dx != 0 || dy != 0 {
					outImg = shiftDepth(outRaw, dx, dy)
				}

				outMask := outImg.Mask()

				var inpCount, outCount, coveredCount int
				for i := range inpMask {
					if inpMask[i] {
						inpCount++
					}
					if outMask[i] {
						outCount++
					}
					if inpMask[i] && outMask[i] {
						coveredCount++
					}
				}

				if inpCount == 0 {
					t.Logf("  %s: no geometry in input, skipping", v.name)
					continue
				}

				dilatedInp := render.DilateMask(inpMask, testResolution, testResolution, dilatePx)

				overshoot := make([]bool, len(outMask))
				var overshootCount int
				for i := range outMask {
					if outMask[i] && !dilatedInp[i] {
						overshoot[i] = true
						overshootCount++
					}
				}
				overshootFrac := 0.0
				if outCount > 0 {
					overshootFrac = float64(overshootCount) / float64(outCount)
				}

				coverage := float64(coveredCount) / float64(inpCount)

				var depthDiffs []float64
				for i := range inpMask {
					if inpMask[i] && outMask[i] {
						ig := inpImg.GrayAt(i%testResolution, i/testResolution, bounds)
						og := outImg.GrayAt(i%testResolution, i/testResolution, bounds)
						depthDiffs = append(depthDiffs, math.Abs(float64(og-ig)))
					}
				}
				depthP95 := percentile(depthDiffs, 95)

				saveImage(t, outdir, fmt.Sprintf("mesh-%s-%s-input.png", name, v.name), inpImg, bounds)
				saveImage(t, outdir, fmt.Sprintf("mesh-%s-%s-output.png", name, v.name), outImg, bounds)
				saveDiffImage(t, outdir, fmt.Sprintf("mesh-%s-%s-diff.png", name, v.name),
					inpMask, outMask, overshoot, testResolution, testResolution)

				passed := true
				var msgs []string

				// Do not relax this requirement.
				if overshootFrac > 0.0 {
					passed = false
					msgs = append(msgs, fmt.Sprintf("overshoot %.1f%% > 0 (%d px)",
						overshootFrac*100, overshootCount))
				}
				if coverage < minCoverage {
					passed = false
					msgs = append(msgs, fmt.Sprintf("coverage %.1f%% < %.1f%%",
						coverage*100, minCoverage*100))
				}
				if depthP95 > maxDepthDiff {
					passed = false
					msgs = append(msgs, fmt.Sprintf("depth p95=%.0f > %d", depthP95, maxDepthDiff))
				}

				detail := fmt.Sprintf("coverage=%.1f%%, overshoot=%.1f%%, depth_p95=%.0f",
					coverage*100, overshootFrac*100, depthP95)
				for _, m := range msgs {
					detail += "; " + m
				}

				if passed {
					t.Logf("  %s: PASS (%dpx tolerance) — %s", v.name, dilatePx, detail)
				} else {
					t.Errorf("  %s: FAIL (%dpx tolerance) — %s", v.name, dilatePx, detail)
				}
			}
		})
	}
}

func saveImage(t *testing.T, outdir, name string, img *render.DepthImage, bounds render.Bounds) {
	path := filepath.Join(outdir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Logf("failed to save %s: %v", path, err)
		return
	}
	defer f.Close()
	png.Encode(f, img.ToRGBA(bounds))
}

func saveDiffImage(t *testing.T, outdir, name string, inpMask, outMask, overshoot []bool, width, height int) {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < width*height; i++ {
		var r, g, b uint8
		switch {
		case overshoot[i]:
			r, g, b = 255, 0, 0
		case inpMask[i] && outMask[i]:
			r, g, b = 0, 180, 0
		case inpMask[i] && !outMask[i]:
			r, g, b = 0, 0, 180
		default:
			r, g, b = 255, 255, 255
		}
		img.Pix[i*4+0] = r
		img.Pix[i*4+1] = g
		img.Pix[i*4+2] = b
		img.Pix[i*4+3] = 255
	}
	path := filepath.Join(outdir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Logf("failed to save %s: %v", path, err)
		return
	}
	defer f.Close()
	png.Encode(f, img)
}

// --- Texture render test ---

const (
	colorBlockSize       = 16
	colorMinBlockCov     = 0.25
	colorMaxMedianDeltaE = 5.0
	colorMaxP95DeltaE    = 15.0
)

func inputColorFn(model *loader.LoadedModel) func(faceIdx int, baryU, baryV float64) [3]uint8 {
	return func(faceIdx int, baryU, baryV float64) [3]uint8 {
		bc := model.FaceBaseColor[faceIdx]
		f := model.Faces[faceIdx]
		texIdx := int(model.FaceTextureIdx[faceIdx])
		if texIdx >= len(model.Textures) {
			return [3]uint8{bc[0], bc[1], bc[2]}
		}
		uv0 := model.UVs[f[0]]
		uv1 := model.UVs[f[1]]
		uv2 := model.UVs[f[2]]
		w := float32(1.0 - baryU - baryV)
		u := w*uv0[0] + float32(baryU)*uv1[0] + float32(baryV)*uv2[0]
		v := w*uv0[1] + float32(baryU)*uv1[1] + float32(baryV)*uv2[1]
		rgba := voxel.BilinearSample(model.Textures[texIdx], u, v)
		// Alpha-blend texture sample over the material base color.
		a := float32(rgba[3]) / 255
		blend := func(tex, base uint8) uint8 {
			return uint8(float32(tex)*a + float32(base)*(1-a))
		}
		return [3]uint8{blend(rgba[0], bc[0]), blend(rgba[1], bc[1]), blend(rgba[2], bc[2])}
	}
}

func outputColorFn(assignments []int32, paletteRGB [][3]uint8) func(faceIdx int, baryU, baryV float64) [3]uint8 {
	return func(faceIdx int, baryU, baryV float64) [3]uint8 {
		return paletteRGB[assignments[faceIdx]]
	}
}

func shiftColor(img *render.ColorImage, dx, dy int) *render.ColorImage {
	out := &render.ColorImage{
		Width:    img.Width,
		Height:   img.Height,
		R:        make([]uint8, len(img.R)),
		G:        make([]uint8, len(img.G)),
		B:        make([]uint8, len(img.B)),
		HasPixel: make([]bool, len(img.HasPixel)),
	}
	for y := 0; y < img.Height; y++ {
		ny := y + dy
		if ny < 0 || ny >= img.Height {
			continue
		}
		for x := 0; x < img.Width; x++ {
			nx := x + dx
			if nx < 0 || nx >= img.Width {
				continue
			}
			si := y*img.Width + x
			di := ny*img.Width + nx
			out.R[di] = img.R[si]
			out.G[di] = img.G[si]
			out.B[di] = img.B[si]
			out.HasPixel[di] = img.HasPixel[si]
		}
	}
	return out
}

func rgbToLab(r, g, b uint8) (float64, float64, float64) {
	c := colorful.Color{R: float64(r) / 255, G: float64(g) / 255, B: float64(b) / 255}
	return c.Lab()
}

func deltaE(l1, a1, b1, l2, a2, b2 float64) float64 {
	dl := l1 - l2
	da := a1 - a2
	db := b1 - b2
	return math.Sqrt(dl*dl + da*da + db*db)
}

func saveColorImage(t *testing.T, outdir, name string, img *render.ColorImage) {
	path := filepath.Join(outdir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Logf("failed to save %s: %v", path, err)
		return
	}
	defer f.Close()
	png.Encode(f, img.ToRGBA())
}

func saveDeltaEImage(t *testing.T, outdir, name string, blockDeltaEs [][]float64, blocksX, blocksY, blockSize int) {
	img := image.NewRGBA(image.Rect(0, 0, blocksX*blockSize, blocksY*blockSize))
	for by := 0; by < blocksY; by++ {
		for bx := 0; bx < blocksX; bx++ {
			de := blockDeltaEs[by][bx]
			var r, g, b uint8
			if de < 0 {
				// No data — dark gray.
				r, g, b = 40, 40, 40
			} else {
				// Scale so deltaE=20 maps to white.
				v := uint8(math.Min(255, de/20*255))
				r, g, b = v, v, v
			}
			for py := by * blockSize; py < (by+1)*blockSize; py++ {
				for px := bx * blockSize; px < (bx+1)*blockSize; px++ {
					i := py*img.Stride + px*4
					img.Pix[i+0] = r
					img.Pix[i+1] = g
					img.Pix[i+2] = b
					img.Pix[i+3] = 255
				}
			}
		}
	}
	path := filepath.Join(outdir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Logf("failed to save %s: %v", path, err)
		return
	}
	defer f.Close()
	png.Encode(f, img)
}

func TestTextureRender(t *testing.T) {
	modelPaths := discoverTestModels(t)

	outdir := "output"

	for _, modelPath := range modelPaths {
		modelPath := modelPath
		base := filepath.Base(modelPath)
		name := strings.TrimSuffix(base, filepath.Ext(base))
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := getRemeshResult(t, modelPath)
			model, outModel := r.model, r.outModel
			assignments, paletteRGB := r.assignments, r.paletteRGB
			ext := modelExtent(model)

			dilatePx := computeDilatePx(float64(defaultNozzle), float64(defaultLayerHeight), float64(ext))

			os.MkdirAll(outdir, 0755)

			inpColorFn := inputColorFn(model)
			outColorFn := outputColorFn(assignments, paletteRGB)

			for _, v := range views {
				inpBounds := render.ProjectedBounds(model.Vertices, v.azimuth, v.elevation)
				outBounds := render.ProjectedBounds(outModel.Vertices, v.azimuth, v.elevation)
				bounds := render.UnionBounds(inpBounds, outBounds)

				inpImg := render.RenderColor(model.Vertices, model.Faces, v.azimuth, v.elevation, testResolution, bounds, inpColorFn)
				outRaw := render.RenderColor(outModel.Vertices, outModel.Faces, v.azimuth, v.elevation, testResolution, bounds, outColorFn)

				// Centroid-align output to input (reuse depth mask approach).
				inpDepth := render.Render(model.Vertices, model.Faces, v.azimuth, v.elevation, testResolution, bounds)
				outDepth := render.Render(outModel.Vertices, outModel.Faces, v.azimuth, v.elevation, testResolution, bounds)
				inpMask := inpDepth.Mask()
				outRawMask := outDepth.Mask()
				ix, iy := centroid(inpMask, testResolution, testResolution)
				ox, oy := centroid(outRawMask, testResolution, testResolution)
				dx, dy := 0, 0
				if ix >= 0 && ox >= 0 {
					dx = int(math.Round(ix - ox))
					dy = int(math.Round(iy - oy))
				}
				_ = dilatePx // used by mesh test; alignment uses same centroid logic

				outImg := outRaw
				if dx != 0 || dy != 0 {
					outImg = shiftColor(outRaw, dx, dy)
				}

				// Block-averaged deltaE comparison.
				blocksX := testResolution / colorBlockSize
				blocksY := testResolution / colorBlockSize
				blockDeltaEs := make([][]float64, blocksY)
				var validDeltaEs []float64

				for by := 0; by < blocksY; by++ {
					blockDeltaEs[by] = make([]float64, blocksX)
					for bx := 0; bx < blocksX; bx++ {
						var sumIR, sumIG, sumIB float64
						var sumOR, sumOG, sumOB float64
						var overlap int
						blockPixels := colorBlockSize * colorBlockSize

						for py := by * colorBlockSize; py < (by+1)*colorBlockSize; py++ {
							for px := bx * colorBlockSize; px < (bx+1)*colorBlockSize; px++ {
								i := py*testResolution + px
								if inpImg.HasPixel[i] && outImg.HasPixel[i] {
									sumIR += float64(inpImg.R[i])
									sumIG += float64(inpImg.G[i])
									sumIB += float64(inpImg.B[i])
									sumOR += float64(outImg.R[i])
									sumOG += float64(outImg.G[i])
									sumOB += float64(outImg.B[i])
									overlap++
								}
							}
						}

						if float64(overlap)/float64(blockPixels) < colorMinBlockCov {
							blockDeltaEs[by][bx] = -1 // no data
							continue
						}

						n := float64(overlap)
						il, ia, ib := rgbToLab(
							uint8(sumIR/n), uint8(sumIG/n), uint8(sumIB/n))
						ol, oa, ob := rgbToLab(
							uint8(sumOR/n), uint8(sumOG/n), uint8(sumOB/n))
						de := deltaE(il, ia, ib, ol, oa, ob)
						blockDeltaEs[by][bx] = de
						validDeltaEs = append(validDeltaEs, de)
					}
				}

				saveColorImage(t, outdir, fmt.Sprintf("texture-%s-%s-input.png", name, v.name), inpImg)
				saveColorImage(t, outdir, fmt.Sprintf("texture-%s-%s-output.png", name, v.name), outImg)
				saveDeltaEImage(t, outdir, fmt.Sprintf("texture-%s-%s-diff.png", name, v.name),
					blockDeltaEs, blocksX, blocksY, colorBlockSize)

				if len(validDeltaEs) == 0 {
					t.Logf("  %s: no overlapping blocks, skipping color check", v.name)
					continue
				}

				medianDE := percentile(validDeltaEs, 50)
				p95DE := percentile(validDeltaEs, 95)

				passed := true
				var msgs []string
				if medianDE > colorMaxMedianDeltaE {
					passed = false
					msgs = append(msgs, fmt.Sprintf("median deltaE %.1f > %.1f", medianDE, colorMaxMedianDeltaE))
				}
				if p95DE > colorMaxP95DeltaE {
					passed = false
					msgs = append(msgs, fmt.Sprintf("p95 deltaE %.1f > %.1f", p95DE, colorMaxP95DeltaE))
				}

				detail := fmt.Sprintf("median_dE=%.1f, p95_dE=%.1f, blocks=%d",
					medianDE, p95DE, len(validDeltaEs))
				for _, m := range msgs {
					detail += "; " + m
				}

				if passed {
					t.Logf("  %s: PASS — %s", v.name, detail)
				} else {
					t.Errorf("  %s: FAIL — %s", v.name, detail)
				}
			}
		})
	}
}
