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
)

// sourceHash is computed once in TestMain from all Go source files.
var sourceHash string

// cacheDir holds cached remesh outputs.
const cacheDir = "tests/cache"

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

// computeSourceHash hashes all .go files in the repo to detect code changes.
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
		// Include the path so file renames are detected.
		h.Write([]byte(path))
		return nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16], nil
}

// cachedRemeshOutput holds the data we cache from a remesh run.
type cachedRemeshOutput struct {
	Vertices    [][3]float32
	Faces       [][3]uint32
	Assignments []int32
}

// getOrRunRemesh returns cached remesh output if available, otherwise runs
// the remesh and caches the result.
func getOrRunRemesh(t *testing.T, vec testVector, model *loader.LoadedModel, pal [][3]uint8) (*loader.LoadedModel, []int32) {
	t.Helper()
	cacheFile := filepath.Join(cacheDir, fmt.Sprintf("%s_%s.gob", vec.name, sourceHash))

	// Try loading from cache.
	if f, err := os.Open(cacheFile); err == nil {
		defer f.Close()
		var cached cachedRemeshOutput
		if err := gob.NewDecoder(f).Decode(&cached); err == nil {
			t.Log("Using cached remesh output")
			outModel := &loader.LoadedModel{
				Vertices: cached.Vertices,
				Faces:    cached.Faces,
			}
			return outModel, cached.Assignments
		}
	}

	// Cache miss — run remesh.
	cfg := squarevoxel.Config{
		NozzleDiameter: vec.nozzle,
		LayerHeight:    vec.layerHeight,
	}

	t.Log("Running squarevoxel remesh...")
	outModel, assignments, err := squarevoxel.Remesh(model, pal, cfg, true)
	if err != nil {
		t.Fatalf("Remesh: %v", err)
	}

	// Save to cache (best-effort).
	if f, err := os.Create(cacheFile); err == nil {
		gob.NewEncoder(f).Encode(cachedRemeshOutput{
			Vertices:    outModel.Vertices,
			Faces:       outModel.Faces,
			Assignments: assignments,
		})
		f.Close()
	}

	// Clean up stale cache files for this vector.
	prefix := vec.name + "_"
	entries, _ := os.ReadDir(cacheDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && e.Name() != filepath.Base(cacheFile) {
			os.Remove(filepath.Join(cacheDir, e.Name()))
		}
	}

	return outModel, assignments
}

type testVector struct {
	name          string
	input         string
	scale         float32
	glbUnit       string
	nozzle        float32
	layerHeight   float32
	modelExtentMM float64
}

type view struct {
	name      string
	azimuth   float64
	elevation float64
}

var testVectors = []testVector{
	{
		name:          "praetorian",
		input:         "objects/glyphid_praetorian.glb",
		scale:         0.05,
		glbUnit:       "m",
		nozzle:        0.4,
		layerHeight:   0.2,
		modelExtentMM: 117.5,
	},
}

var views = []view{
	{"front", 90, 20},
	{"left", 0, 20},
	{"top", 0, 90},
}

const (
	testResolution = 512
	marginFrac     = 0.05
	minCoverage    = 0.95
	maxOvershoot   = 0.0
	maxDepthDiff   = 20 // p95 gray level difference (0-255)
)

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

// loadInput loads and caches the input model for a test vector.
func loadInput(t *testing.T, vec testVector) *loader.LoadedModel {
	t.Helper()
	unitScales := map[string]float32{"m": 1000, "dm": 100, "cm": 10, "mm": 1}
	scale := unitScales[vec.glbUnit] * vec.scale

	t.Logf("Loading %s (scale=%.4f)...", vec.input, scale)
	model, err := loader.LoadGLB(vec.input, scale)
	if err != nil {
		t.Fatalf("LoadGLB: %v", err)
	}
	t.Logf("  Input: %d verts, %d faces", len(model.Vertices), len(model.Faces))
	return model
}

func TestMeshRender(t *testing.T) {
	outdir := filepath.Join("tests", "output")
	keepOutput := os.Getenv("KEEP_OUTPUT") != ""

	for _, vec := range testVectors {
		t.Run(vec.name, func(t *testing.T) {
			if _, err := os.Stat(vec.input); os.IsNotExist(err) {
				t.Skipf("input file not found: %s", vec.input)
			}

			model := loadInput(t, vec)
			pal := [][3]uint8{{0, 255, 255}, {255, 0, 255}, {255, 255, 0}, {0, 0, 0}}
			outModel, _ := getOrRunRemesh(t, vec, model, pal)
			t.Logf("  Output: %d verts, %d faces", len(outModel.Vertices), len(outModel.Faces))

			dilatePx := computeDilatePx(float64(vec.nozzle), float64(vec.layerHeight), vec.modelExtentMM)
			t.Logf("  Tolerance: %dpx", dilatePx)

			if keepOutput {
				os.MkdirAll(outdir, 0755)
			}

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

				if keepOutput {
					saveImage(t, outdir, fmt.Sprintf("%s_%s_input.png", vec.name, v.name), inpImg, bounds)
					saveImage(t, outdir, fmt.Sprintf("%s_%s_output.png", vec.name, v.name), outImg, bounds)
					saveDiffImage(t, outdir, fmt.Sprintf("%s_%s_diff.png", vec.name, v.name),
						inpMask, outMask, overshoot, testResolution, testResolution)
				}

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
