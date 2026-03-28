package main

import (
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/render"
	"github.com/rtwfroody/ditherforge/internal/squarevoxel"
)

type testVector struct {
	name           string
	input          string
	scale          float32
	glbUnit        string
	nozzle         float32
	layerHeight    float32
	modelExtentMM  float64
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
	resolution  = 512
	marginFrac  = 0.05
	minCoverage = 0.95
	maxOvershoot = 0.0
	maxDepthDiff = 20 // p95 gray level difference (0-255)
)

func computeDilatePx(nozzleMM, layerHeightMM, modelExtentMM float64) int {
	hexFlat := nozzleMM * 1.5
	cellDiag := math.Sqrt(hexFlat*hexFlat + math.Max(hexFlat, layerHeightMM)*math.Max(hexFlat, layerHeightMM))
	cellNormalized := cellDiag / modelExtentMM
	pixelsPerUnit := float64(resolution) * (1 - 2*marginFrac)
	dilatePx := int(math.Ceil(1.5 * cellNormalized * pixelsPerUnit))
	dilatePx++ // alignment rounding
	return dilatePx
}

// centroid returns the centroid of object pixels, or (-1,-1) if none.
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

// shiftDepth returns a new DepthImage shifted by (dx, dy) pixels.
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
	// Create output dir for debug images.
	outdir := filepath.Join("tests", "output")
	keepOutput := os.Getenv("KEEP_OUTPUT") != ""

	for _, vec := range testVectors {
		t.Run(vec.name, func(t *testing.T) {
			if _, err := os.Stat(vec.input); os.IsNotExist(err) {
				t.Skipf("input file not found: %s", vec.input)
			}

			unitScales := map[string]float32{"m": 1000, "dm": 100, "cm": 10, "mm": 1}
			scale := unitScales[vec.glbUnit] * vec.scale

			t.Logf("Loading %s (scale=%.4f)...", vec.input, scale)
			model, err := loader.LoadGLB(vec.input, scale)
			if err != nil {
				t.Fatalf("LoadGLB: %v", err)
			}
			t.Logf("  Input: %d verts, %d faces", len(model.Vertices), len(model.Faces))

			// Build default palette.
			pal := [][3]uint8{{0, 255, 255}, {255, 0, 255}, {255, 255, 0}, {0, 0, 0}}

			cfg := squarevoxel.Config{
				NozzleDiameter: vec.nozzle,
				LayerHeight:    vec.layerHeight,
			}

			t.Log("Running squarevoxel remesh...")
			outModel, _, err := squarevoxel.Remesh(model, pal, cfg, true)
			if err != nil {
				t.Fatalf("Remesh: %v", err)
			}
			t.Logf("  Output: %d verts, %d faces", len(outModel.Vertices), len(outModel.Faces))

			dilatePx := computeDilatePx(float64(vec.nozzle), float64(vec.layerHeight), vec.modelExtentMM)
			t.Logf("  Tolerance: %dpx", dilatePx)

			if keepOutput {
				os.MkdirAll(outdir, 0755)
			}

			for _, v := range views {
				// Shared bounds for comparable depth values.
				inpBounds := render.ProjectedBounds(model.Vertices, v.azimuth, v.elevation)
				outBounds := render.ProjectedBounds(outModel.Vertices, v.azimuth, v.elevation)
				bounds := render.UnionBounds(inpBounds, outBounds)

				inpImg := render.Render(model.Vertices, model.Faces, v.azimuth, v.elevation, resolution, bounds)
				outRaw := render.Render(outModel.Vertices, outModel.Faces, v.azimuth, v.elevation, resolution, bounds)

				// Align output to input by centroid matching.
				inpMask := inpImg.Mask()
				outRawMask := outRaw.Mask()
				ix, iy := centroid(inpMask, resolution, resolution)
				ox, oy := centroid(outRawMask, resolution, resolution)
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

				// Count pixels.
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

				// Dilate input mask.
				dilatedInp := render.DilateMask(inpMask, resolution, resolution, dilatePx)

				// Overshoot: output pixels outside dilated input.
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

				// Coverage.
				coverage := float64(coveredCount) / float64(inpCount)

				// Depth comparison where both have geometry.
				var depthDiffs []float64
				for i := range inpMask {
					if inpMask[i] && outMask[i] {
						ig := inpImg.GrayAt(i%resolution, i/resolution, bounds)
						og := outImg.GrayAt(i%resolution, i/resolution, bounds)
						depthDiffs = append(depthDiffs, math.Abs(float64(og-ig)))
					}
				}
				depthP95 := percentile(depthDiffs, 95)

				// Save debug images if keeping output.
				if keepOutput {
					saveImage(t, outdir, fmt.Sprintf("%s_%s_input.png", vec.name, v.name), inpImg, bounds)
					saveImage(t, outdir, fmt.Sprintf("%s_%s_output.png", vec.name, v.name), outImg, bounds)
					saveDiffImage(t, outdir, fmt.Sprintf("%s_%s_diff.png", vec.name, v.name),
						inpMask, outMask, overshoot, resolution, resolution)
				}

				// Check thresholds.
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
				if len(msgs) > 0 {
					for _, m := range msgs {
						detail += "; " + m
					}
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
