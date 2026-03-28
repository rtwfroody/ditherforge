package main

import (
	"crypto/sha256"
	"encoding/gob"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/render"
	"github.com/rtwfroody/ditherforge/internal/squarevoxel"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// sourceHash is computed once in TestMain from all Go source files.
var sourceHash string

const cacheDir = "tests/cache"

const (
	defaultNozzle      = float32(0.4)
	defaultLayerHeight = float32(0.2)
	maxExtentMM        = float32(100)
	testResolution     = 512
	marginFrac         = 0.05
	minCoverage        = 0.95
	maxOvershoot       = 0.0
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

func TestMain(m *testing.M) {
	h, err := computeSourceHash()
	if err != nil {
		fmt.Fprintf(os.Stderr, "computing source hash: %v\n", err)
		os.Exit(1)
	}
	sourceHash = h
	os.MkdirAll(cacheDir, 0755)
	os.Exit(m.Run())
}

func computeSourceHash() (string, error) {
	h := sha256.New()
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if path == ".git" || path == "vendor" || path == "tests" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		h.Write([]byte(path))
		return nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16], nil
}

type cachedRemeshOutput struct {
	Vertices    [][3]float32
	Faces       [][3]uint32
	Assignments []int32
}

func getOrRunRemesh(t *testing.T, name string, model *loader.LoadedModel, pal [][3]uint8) (*loader.LoadedModel, []int32) {
	t.Helper()
	cacheFile := filepath.Join(cacheDir, fmt.Sprintf("%s_%s.gob", name, sourceHash))

	if f, err := os.Open(cacheFile); err == nil {
		defer f.Close()
		var cached cachedRemeshOutput
		if err := gob.NewDecoder(f).Decode(&cached); err == nil {
			t.Log("Using cached remesh output")
			return &loader.LoadedModel{
				Vertices: cached.Vertices,
				Faces:    cached.Faces,
			}, cached.Assignments
		}
	}

	cfg := squarevoxel.Config{
		NozzleDiameter: defaultNozzle,
		LayerHeight:    defaultLayerHeight,
	}

	t.Log("Running squarevoxel remesh...")
	pcfg := voxel.PaletteConfig{Palette: pal}
	outModel, assignments, _, err := squarevoxel.Remesh(model, pcfg, cfg, "dizzy")
	if err != nil {
		t.Fatalf("Remesh: %v", err)
	}

	if f, err := os.Create(cacheFile); err == nil {
		gob.NewEncoder(f).Encode(cachedRemeshOutput{
			Vertices:    outModel.Vertices,
			Faces:       outModel.Faces,
			Assignments: assignments,
		})
		f.Close()
	}

	// Clean stale cache for this model.
	prefix := name + "_"
	entries, _ := os.ReadDir(cacheDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && e.Name() != filepath.Base(cacheFile) {
			os.Remove(filepath.Join(cacheDir, e.Name()))
		}
	}

	return outModel, assignments
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

func TestMeshRender(t *testing.T) {
	// Auto-discover all GLB files in objects/.
	glbs, err := filepath.Glob("objects/*.glb")
	if err != nil {
		t.Fatalf("globbing objects/*.glb: %v", err)
	}
	if len(glbs) == 0 {
		t.Skip("no .glb files in objects/")
	}

	outdir := filepath.Join("tests", "output")

	for _, glbPath := range glbs {
		name := strings.TrimSuffix(filepath.Base(glbPath), ".glb")
		t.Run(name, func(t *testing.T) {
			// Load with default GLB unit (meters → mm).
			const unitScale = float32(1000)

			t.Logf("Loading %s...", glbPath)
			model, err := loader.LoadGLB(glbPath, unitScale)
			if err != nil {
				t.Fatalf("LoadGLB: %v", err)
			}

			// Auto-scale to fit within maxExtentMM.
			ext := modelExtent(model)
			scale := float32(1.0)
			if ext > maxExtentMM {
				scale = maxExtentMM / ext
				t.Logf("  Extent %.1fmm > %.0fmm, scaling by %.4f", ext, maxExtentMM, scale)
				// Reload with adjusted scale.
				model, err = loader.LoadGLB(glbPath, unitScale*scale)
				if err != nil {
					t.Fatalf("LoadGLB (rescaled): %v", err)
				}
				ext = modelExtent(model)
			}
			t.Logf("  Input: %d verts, %d faces, extent %.1fmm",
				len(model.Vertices), len(model.Faces), ext)

			pal := [][3]uint8{{0, 255, 255}, {255, 0, 255}, {255, 255, 0}, {0, 0, 0}}
			outModel, _ := getOrRunRemesh(t, name, model, pal)
			t.Logf("  Output: %d verts, %d faces", len(outModel.Vertices), len(outModel.Faces))

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

					saveImage(t, outdir, fmt.Sprintf("%s_%s_input.png", name, v.name), inpImg, bounds)
				saveImage(t, outdir, fmt.Sprintf("%s_%s_output.png", name, v.name), outImg, bounds)
				saveDiffImage(t, outdir, fmt.Sprintf("%s_%s_diff.png", name, v.name),
					inpMask, outMask, overshoot, testResolution, testResolution)

				passed := true
				var msgs []string

				if overshootFrac > maxOvershoot {
					passed = false
					msgs = append(msgs, fmt.Sprintf("overshoot %.1f%% > %.1f%% (%d px)",
						overshootFrac*100, maxOvershoot*100, overshootCount))
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
