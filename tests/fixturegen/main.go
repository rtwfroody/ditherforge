// Command fixturegen renders model files into multi-view PNG strips
// used as input to the color-selection regression tests in tests/.
// See tests/testdata/color/README.md for the regeneration procedure.
//
// Each fixture's input model lives outside the repo (too large to
// check in); the resulting PNG strip captures the per-pixel surface
// color and is small enough to commit. The tests load these PNGs and
// run voxel.ResolvePalette on the opaque pixel histogram, so the
// regression suite stays hermetic and reproducible.
package main

import (
	"flag"
	"fmt"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
	"github.com/rtwfroody/ditherforge/internal/render"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// fixtureCase describes one regression fixture to render. Mirrors the
// shape of colorTestCase in tests/color_test.go (same `name` is used
// to look up the produced PNG file there).
type fixtureCase struct {
	name           string
	modelPath      string
	materialXPath  string  // empty = render the model's own UV texture
	tileMM         float64 // only used when materialXPath != ""
	triplanarSharp float64 // only used when materialXPath != ""
}

// expandHome resolves a leading "~/" against $HOME. Test data lives in
// each developer's local directory and the fixture cases below carry
// "~/Documents/3d_print/..." paths to keep the source readable.
func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return home + p[1:]
	}
	return p
}

// stripViews are six axis-aligned ortho cameras (4 sides + top + bottom).
// Each surface point is captured by whichever view has the closest-
// matching surface normal; corner-facing surfaces appear in two views
// (slight double-count, acceptable for a regression histogram).
var stripViews = []render.StripView{
	{Name: "front", Azimuth: 90, Elevation: 0},
	{Name: "right", Azimuth: 180, Elevation: 0},
	{Name: "back", Azimuth: 270, Elevation: 0},
	{Name: "left", Azimuth: 0, Elevation: 0},
	{Name: "top", Azimuth: 0, Elevation: 90},
	{Name: "bottom", Azimuth: 0, Elevation: -90},
}

// Per-view long-axis pixel budget. The model's longest 3D dimension
// maps to this many pixels; per-view canvas sizes follow.
const longestPixels = 512

// Inter-view gap in the strip (transparent columns).
const stripGapPx = 8

// Default cases mirror tests/color_test.go's colorTests by name.
var fixtureCases = []fixtureCase{
	{
		name:      "delorean",
		modelPath: "~/Documents/3d_print/objects/1985_delorean_dmc-12_time_machine_bttf.glb",
	},
	{
		name:      "golden_pheasant",
		modelPath: "~/Documents/3d_print/objects/golden_pheasant.glb",
	},
	{
		name:      "earth",
		modelPath: "objects/earth.glb",
	},
	{
		name:           "bricks_benchy",
		modelPath:      "~/Documents/3d_print/objects/3DBenchy.stl",
		materialXPath:  "~/Downloads/Bricks_2k_8b.zip",
		tileMM:         200,
		triplanarSharp: 4,
	},
}

func main() {
	outDir := flag.String("out", "testdata/color", "output directory for fixture PNGs (relative to working dir)")
	only := flag.String("only", "", "regenerate only the fixture with this name (comma-separated for multiple); default is all")
	flag.Parse()

	wanted := map[string]bool{}
	if *only != "" {
		for _, n := range strings.Split(*only, ",") {
			wanted[strings.TrimSpace(n)] = true
		}
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("mkdir %s: %v", *outDir, err)
	}

	for _, fc := range fixtureCases {
		if len(wanted) > 0 && !wanted[fc.name] {
			continue
		}
		modelPath := expandHome(fc.modelPath)
		if _, err := os.Stat(modelPath); os.IsNotExist(err) {
			log.Printf("[%s] skipped: model not found at %s", fc.name, modelPath)
			continue
		}
		outPath := filepath.Join(*outDir, fc.name+".png")
		if err := generate(fc, modelPath, outPath); err != nil {
			log.Printf("[%s] FAILED: %v", fc.name, err)
			continue
		}
		log.Printf("[%s] wrote %s", fc.name, outPath)
	}
}

func generate(fc fixtureCase, modelPath, outPath string) error {
	model, err := loadAny(modelPath)
	if err != nil {
		return fmt.Errorf("load %s: %w", modelPath, err)
	}

	// Match the test pipeline's unit handling: GLBs are in meters,
	// STLs/3MFs are already in mm. Without the *1000 the GLB's
	// triangles project at micro-mm pixel scale and the renderer
	// produces a 1×1 image.
	ext := strings.ToLower(filepath.Ext(modelPath))
	if ext == ".glb" {
		loader.ScaleModel(model, 1000)
	}
	// Normalize to a 100mm longest extent so all fixtures share a
	// consistent size in the saved PNGs (~512px per view).
	const targetExtent = float32(100)
	maxExt := modelMaxExtent(model)
	if maxExt > 0 && maxExt != targetExtent {
		loader.ScaleModel(model, targetExtent/maxExt)
	}

	colorFn, err := buildColorFn(fc, model)
	if err != nil {
		return fmt.Errorf("build colorFn: %w", err)
	}

	img := render.RenderColorStrip(model.Vertices, model.Faces, stripViews, longestPixels, stripGapPx, colorFn)

	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		return fmt.Errorf("encode %s: %w", outPath, err)
	}
	return nil
}

func loadAny(path string) (*loader.LoadedModel, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".glb":
		return loader.LoadGLB(path, -1)
	case ".3mf":
		return loader.Load3MF(path, -1)
	case ".stl":
		return loader.LoadSTL(path, -1)
	case ".obj":
		return loader.LoadOBJ(path, -1)
	case ".zip":
		return loader.LoadOBJZip(path, -1)
	default:
		return nil, fmt.Errorf("unsupported extension %q", filepath.Ext(path))
	}
}

// buildColorFn returns the per-pixel color resolver. UV-textured models
// sample the texture at the per-pixel barycentric UV. MaterialX-driven
// models compute the per-pixel 3D position and ask the triplanar
// sampler. Both produce the same kind of pixel as the live pipeline
// gives the dither stage, so the histogram is faithful.
func buildColorFn(fc fixtureCase, model *loader.LoadedModel) (func(faceIdx int, baryU, baryV float64) [3]uint8, error) {
	if fc.materialXPath != "" {
		mtlxPath := expandHome(fc.materialXPath)
		override, err := pipeline.NewMaterialXOverride(mtlxPath, fc.tileMM, fc.triplanarSharp)
		if err != nil {
			return nil, err
		}
		if override == nil {
			return nil, fmt.Errorf("MaterialX override returned nil sampler for %q", mtlxPath)
		}
		return materialxColorFn(model, override), nil
	}
	return uvColorFn(model), nil
}

// uvColorFn samples the model's first face texture per pixel. Faces
// with no texture fall back to the per-face base color. Mirrors the
// inputColorFn in mesh_render_test.go so the pixel histogram matches
// what the live pipeline would derive from the same model.
func uvColorFn(model *loader.LoadedModel) func(int, float64, float64) [3]uint8 {
	return func(faceIdx int, baryU, baryV float64) [3]uint8 {
		bc := model.FaceBaseColor[faceIdx]
		f := model.Faces[faceIdx]
		texIdx := int(model.FaceTextureIdx[faceIdx])
		if texIdx < 0 || texIdx >= len(model.Textures) {
			return [3]uint8{bc[0], bc[1], bc[2]}
		}
		uv0 := model.UVs[f[0]]
		uv1 := model.UVs[f[1]]
		uv2 := model.UVs[f[2]]
		w := float32(1.0 - baryU - baryV)
		u := w*uv0[0] + float32(baryU)*uv1[0] + float32(baryV)*uv2[0]
		v := w*uv0[1] + float32(baryU)*uv1[1] + float32(baryV)*uv2[1]
		rgba := voxel.BilinearSample(model.Textures[texIdx], u, v)
		a := float32(rgba[3]) / 255
		blend := func(tex, base uint8) uint8 {
			return uint8(float32(tex)*a + float32(base)*(1-a))
		}
		return [3]uint8{
			blend(rgba[0], bc[0]),
			blend(rgba[1], bc[1]),
			blend(rgba[2], bc[2]),
		}
	}
}

// materialxColorFn computes the per-pixel 3D world position from the
// face vertices and barycentric coords, then evaluates the triplanar
// MaterialX sampler at that point. This is the same code path the
// dither pipeline takes via voxel.SampleNearestColor + override.
func materialxColorFn(model *loader.LoadedModel, override voxel.BaseColorOverride) func(int, float64, float64) [3]uint8 {
	return func(faceIdx int, baryU, baryV float64) [3]uint8 {
		f := model.Faces[faceIdx]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		v2 := model.Vertices[f[2]]
		w := float32(1.0 - baryU - baryV)
		pos := [3]float32{
			w*v0[0] + float32(baryU)*v1[0] + float32(baryV)*v2[0],
			w*v0[1] + float32(baryU)*v1[1] + float32(baryV)*v2[1],
			w*v0[2] + float32(baryU)*v1[2] + float32(baryV)*v2[2],
		}
		normal := voxel.FaceNormal(faceIdx, model)
		return override.SampleBaseColor(voxel.BaseColorContext{Pos: pos, Normal: normal})
	}
}

func modelMaxExtent(m *loader.LoadedModel) float32 {
	if len(m.Vertices) == 0 {
		return 0
	}
	var lo, hi [3]float32
	for i := 0; i < 3; i++ {
		lo[i] = m.Vertices[0][i]
		hi[i] = m.Vertices[0][i]
	}
	for _, v := range m.Vertices {
		for i := 0; i < 3; i++ {
			if v[i] < lo[i] {
				lo[i] = v[i]
			}
			if v[i] > hi[i] {
				hi[i] = v[i]
			}
		}
	}
	maxE := float32(0)
	for i := 0; i < 3; i++ {
		e := hi[i] - lo[i]
		if e > maxE {
			maxE = e
		}
	}
	return maxE
}
