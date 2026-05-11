package main

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexflint/go-arg"

	"github.com/rtwfroody/ditherforge/internal/diskcache"
	"github.com/rtwfroody/ditherforge/internal/export3mf"
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/render"
)

// expandColors splits comma-separated --color values into individual color strings.
func expandColors(colors []string) []string {
	var result []string
	for _, c := range colors {
		for _, part := range strings.Split(c, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				result = append(result, part)
			}
		}
	}
	return result
}

// Args defines the CLI arguments.
type Args struct {
	Input                           string   `arg:"positional,required" help:"Input .glb, .3mf, or .stl file"`
	NumColors                       int      `arg:"-n" default:"4" help:"Number of palette colors"`
	Color                           []string `arg:"--color,separate" help:"Lock a color (CSS name or hex, repeatable, comma-separated)"`
	Inventory                       string   `arg:"--inventory" help:"Inventory file for remaining colors"`
	Scale                           float32  `arg:"--scale" default:"1.0" help:"Additional scale multiplier"`
	Output                          string   `arg:"--output" help:"Output .3mf file (default: <input>.3mf)"`
	BaseColor                       string   `arg:"--base-color" help:"Hex color for untextured faces (e.g. #FF0000)"`
	BaseMaterialX                   string   `arg:"--base-materialx" help:"Path to a .mtlx file or .zip archive containing one (with adjacent textures) applied as the base color of untextured faces (overrides --base-color)"`
	BaseMaterialXTileMM             float64  `arg:"--base-materialx-tile-mm" default:"10" help:"Object-space scale (mm per shading-unit cycle) for the MaterialX procedural"`
	BaseMaterialXTriplanarSharpness float64  `arg:"--base-materialx-triplanar-sharpness" default:"4" help:"Triplanar projection sharpness for image-backed MaterialX (higher = sharper axis transitions; ignored by procedural .mtlx)"`
	NozzleDiameter                  float32  `arg:"--nozzle-diameter" default:"0.4" help:"Nozzle diameter in mm"`
	LayerHeight                     float32  `arg:"--layer-height" default:"0.2" help:"Layer height in mm"`
	Printer                         string   `arg:"--printer" help:"Target printer profile id (e.g. snapmaker_u1, snapmaker_j1, prusa_xl, prusa_xl_5t, bambu_h2d, bambu_h2d_pro); defaults to snapmaker_u1"`
	Brightness                      float32  `arg:"--brightness" default:"0" help:"Brightness adjustment (-100 to +100)"`
	Contrast                        float32  `arg:"--contrast" default:"0" help:"Contrast adjustment (-100 to +100)"`
	Saturation                      float32  `arg:"--saturation" default:"0" help:"Saturation adjustment (-100 to +100)"`
	Dither                          string   `arg:"--dither" default:"riemersma" help:"Dithering mode: riemersma, riemersma-pair, blue-noise, dizzy-corrected, floyd-steinberg, none (also accepted: dizzy-2hop, dizzy-recover)"`
	RiemersmaInputBias              float64  `arg:"--riemersma-bias" default:"0.85" help:"Riemersma input-bias maximum (0..1). 0 = pure dither; higher pulls toward nearest-input palette in near-palette regions"`
	BlueNoiseTolerance              float64  `arg:"--blue-noise-tol" help:"Blue-noise dither per-cell projection-error tolerance (RGB units). Smaller = lower wander but more drift; larger = more wander but less drift. 0 = use built-in default (currently 20)"`
	NoMerge                         bool     `arg:"--no-merge" help:"Skip coplanar triangle merging"`
	NoSimplify                      bool     `arg:"--no-simplify" help:"Skip QEM mesh decimation before clipping"`
	Size                            *float32 `arg:"--size" help:"Scale model so largest extent equals this value in mm"`
	Force                           bool     `arg:"--force" help:"Bypass extent size check"`
	Stats                           bool     `arg:"--stats" help:"Print face counts per material"`
	ColorSnap                       float64  `arg:"--color-snap" default:"5" help:"Shift cell colors toward nearest palette color by this many delta E units (0 to disable)"`
	AlphaWrap                       bool     `arg:"--alpha-wrap" help:"Clean up the loaded mesh with CGAL Alpha_wrap_3 (requires uv on PATH)"`
	AlphaWrapAlpha                  float32  `arg:"--alpha-wrap-alpha" help:"Alpha-wrap probe radius in mm (default: nozzle diameter)"`
	AlphaWrapOffset                 float32  `arg:"--alpha-wrap-offset" help:"Alpha-wrap offset distance in mm (default: alpha/30)"`
	Layer0AdhesionXYScale           float32  `arg:"--layer0-adhesion-xy-scale" default:"2" help:"Multiplier on layer-0 voxel cell XY size for bed adhesion (1 = no enlargement, higher = bigger first-layer color blobs)"`
	UpperLayerXYScale               float32  `arg:"--upper-layer-xy-scale" default:"1.25" help:"Multiplier on upper-layer voxel cell XY size relative to the slicer line width (1 = at line width, higher = coarser color detail with fewer primitives)"`
	DebugRender                     string   `arg:"--debug-render" help:"After running the pipeline, write PNG renders (input + dithered + sampled, four views each) into this directory. Useful for headless debugging."`
	DebugRenderRes                  int      `arg:"--debug-render-res" default:"800" help:"PNG resolution (square) for --debug-render output"`
}

func (Args) Description() string {
	return "Convert a textured GLB model to a multi-material 3MF file."
}

func (Args) Version() string {
	return pipeline.Version
}

func main() {
	var args Args
	arg.MustParse(&args)

	if args.Output == "" {
		base := filepath.Base(args.Input)
		ext := filepath.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		if strings.EqualFold(ext, ".3mf") {
			args.Output = stem + "-df.3mf"
		} else {
			args.Output = stem + ".3mf"
		}
	}

	opts := pipeline.Options{
		Input:                                args.Input,
		NumColors:                            args.NumColors,
		LockedColors:                         expandColors(args.Color),
		InventoryFile:                        args.Inventory,
		Scale:                                args.Scale,
		Output:                               args.Output,
		BaseColor:                            args.BaseColor,
		BaseColorMaterialX:                   args.BaseMaterialX,
		BaseColorMaterialXTileMM:             args.BaseMaterialXTileMM,
		BaseColorMaterialXTriplanarSharpness: args.BaseMaterialXTriplanarSharpness,
		NozzleDiameter:                       args.NozzleDiameter,
		LayerHeight:                          args.LayerHeight,
		Printer:                              args.Printer,
		Brightness:                           args.Brightness,
		Contrast:                             args.Contrast,
		Saturation:                           args.Saturation,
		Dither:                               args.Dither,
		RiemersmaInputBias:                   args.RiemersmaInputBias,
		BlueNoiseTolerance:                   args.BlueNoiseTolerance,
		NoMerge:                              args.NoMerge,
		NoSimplify:                           args.NoSimplify,
		Size:                                 args.Size,
		Force:                                args.Force,
		Stats:                                args.Stats,
		ColorSnap:                            args.ColorSnap,
		ObjectIndex:                          -1, // load all objects (no CLI flag yet; GUI has a picker dialog)
		AlphaWrap:                            args.AlphaWrap,
		AlphaWrapAlpha:                       args.AlphaWrapAlpha,
		AlphaWrapOffset:                      args.AlphaWrapOffset,
		Layer0AdhesionXYScale:                args.Layer0AdhesionXYScale,
		UpperLayerXYScale:                    args.UpperLayerXYScale,
	}

	ctx := context.Background()
	cache := pipeline.NewStageCache()
	// Attach the same on-disk cache the GUI uses. ExportFile reads
	// stage outputs back from disk, so a CLI run without disk
	// attached can't export — the GUI configures this automatically
	// in NewApp.
	if dir, err := diskcache.DefaultDir(); err == nil {
		if d, derr := diskcache.Open(dir); derr == nil {
			cache.SetDisk(d)
		} else {
			fmt.Fprintf(os.Stderr, "disk cache disabled: %v\n", derr)
		}
	}
	cb := &pipeline.Callbacks{Progress: progress.NewCLITracker()}

	pr, err := pipeline.RunCached(ctx, cache, opts, cb)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if pr.NeedsForce {
		fmt.Fprintf(os.Stderr, "Error: model extent %.0f mm exceeds 300 mm; use --scale or --size to reduce size (or --force to bypass)\n", pr.ModelExtentMM)
		os.Exit(1)
	}

	if _, err := pipeline.ExportFile(cache, opts, opts.Output, export3mf.Options{
		PrinterID:      opts.Printer,
		NozzleDiameter: opts.NozzleDiameter,
		LayerHeight:    opts.LayerHeight,
		AppVersion:     pipeline.VersionSemver,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if args.DebugRender != "" {
		if err := writeDebugRenders(ctx, cache, opts, args.Input, args.DebugRender, args.DebugRenderRes, pr.OutputMesh); err != nil {
			fmt.Fprintf(os.Stderr, "debug-render: %v\n", err)
			os.Exit(1)
		}
	}
}

// debugView is one orthographic camera angle in the --debug-render
// strip.
type debugView struct {
	name          string
	azimuth, elev float64
}

var debugViews = []debugView{
	{"front", 0, 0},
	{"side", 90, 0},
	{"top", 0, 90},
	{"persp", 45, 25},
}

// writeDebugRenders writes PNG renders of three things to dir:
//   - input_<view>.png: the raw model with per-face sampled color
//   - dithered_<view>.png: the pipeline output (palette-quantized)
//   - sampled_<view>.png: the pipeline output with ShowSampledColors
//     (per-section raw sampled RGB before dithering)
//
// The dithered mesh comes from the caller's existing pipeline run;
// the sampled mesh is produced by re-running RunCached with
// ShowSampledColors=true, which reuses everything up to the dither
// stage from `cache` and only redoes the post-merge bypass.
func writeDebugRenders(ctx context.Context, cache *pipeline.StageCache, opts pipeline.Options, inputPath, dir string, res int, ditheredMesh *pipeline.MeshData) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	if inputMesh, err := loadInputRenderMesh(inputPath, opts.Size); err != nil {
		fmt.Fprintf(os.Stderr, "debug-render: skipping input reference (%v)\n", err)
	} else {
		for _, v := range debugViews {
			p := filepath.Join(dir, fmt.Sprintf("input_%s.png", v.name))
			if err := writeMeshPNG(inputMesh.vertices, inputMesh.faces, inputMesh.colorAt, v.azimuth, v.elev, res, p); err != nil {
				return err
			}
		}
	}

	if ditheredMesh != nil {
		for _, v := range debugViews {
			p := filepath.Join(dir, fmt.Sprintf("dithered_%s.png", v.name))
			if err := writeMeshDataPNG(ditheredMesh, v.azimuth, v.elev, res, p); err != nil {
				return err
			}
		}
	}

	sampledOpts := opts
	sampledOpts.ShowSampledColors = true
	sampledPr, err := pipeline.RunCached(ctx, cache, sampledOpts, &pipeline.Callbacks{Progress: progress.NewCLITracker()})
	if err != nil {
		return fmt.Errorf("sampled re-run: %w", err)
	}
	if sampledPr.OutputMesh != nil {
		for _, v := range debugViews {
			p := filepath.Join(dir, fmt.Sprintf("sampled_%s.png", v.name))
			if err := writeMeshDataPNG(sampledPr.OutputMesh, v.azimuth, v.elev, res, p); err != nil {
				return err
			}
		}
	}
	return nil
}

type inputRenderMesh struct {
	vertices [][3]float32
	faces    [][3]uint32
	colors   [][3]uint8
}

func (m *inputRenderMesh) colorAt(fi int, u, v float64) [3]uint8 {
	if fi < 0 || fi >= len(m.colors) {
		return [3]uint8{128, 128, 128}
	}
	return m.colors[fi]
}

// loadInputRenderMesh loads the model the same way the pipeline does
// (unit conversion + optional size normalization) and produces a
// per-face color sampled at the face centroid — used as the
// reference image to compare pipeline output against.
func loadInputRenderMesh(path string, sizePtr *float32) (*inputRenderMesh, error) {
	model, err := loadAnyModel(path)
	if err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".glb" {
		loader.ScaleModel(model, 1000)
	}
	if sizePtr != nil {
		e := maxExtent(model)
		if e > 0 {
			loader.ScaleModel(model, *sizePtr/e)
		}
	}
	colors := make([][3]uint8, len(model.Faces))
	for fi := range model.Faces {
		colors[fi] = faceCentroidColor(model, fi)
	}
	return &inputRenderMesh{
		vertices: model.Vertices,
		faces:    model.Faces,
		colors:   colors,
	}, nil
}

func loadAnyModel(path string) (*loader.LoadedModel, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".glb":
		return loader.LoadGLB(path, 0)
	case ".3mf":
		return loader.Load3MF(path, 0)
	case ".stl":
		return loader.LoadSTL(path, 0)
	}
	return nil, fmt.Errorf("unsupported format")
}

func maxExtent(m *loader.LoadedModel) float32 {
	if len(m.Vertices) == 0 {
		return 0
	}
	mn, mx := m.Vertices[0], m.Vertices[0]
	for _, v := range m.Vertices {
		for k := 0; k < 3; k++ {
			if v[k] < mn[k] {
				mn[k] = v[k]
			}
			if v[k] > mx[k] {
				mx[k] = v[k]
			}
		}
	}
	var e float32
	for k := 0; k < 3; k++ {
		if d := mx[k] - mn[k]; d > e {
			e = d
		}
	}
	return e
}

// faceCentroidColor samples a face's representative color via vertex
// colors → UV texture → base color, in that order. Cheap, visual,
// used for the reference render only.
func faceCentroidColor(m *loader.LoadedModel, fi int) [3]uint8 {
	if fi < 0 || fi >= len(m.Faces) {
		return [3]uint8{128, 128, 128}
	}
	f := m.Faces[fi]
	if m.VertexColors != nil {
		c0 := m.VertexColors[f[0]]
		c1 := m.VertexColors[f[1]]
		c2 := m.VertexColors[f[2]]
		return [3]uint8{
			uint8((int(c0[0]) + int(c1[0]) + int(c2[0])) / 3),
			uint8((int(c0[1]) + int(c1[1]) + int(c2[1])) / 3),
			uint8((int(c0[2]) + int(c1[2]) + int(c2[2])) / 3),
		}
	}
	if m.UVs != nil && m.FaceTextureIdx != nil && m.FaceTextureIdx[fi] >= 0 &&
		int(m.FaceTextureIdx[fi]) < len(m.Textures) {
		texIdx := int(m.FaceTextureIdx[fi])
		uv0 := m.UVs[f[0]]
		uv1 := m.UVs[f[1]]
		uv2 := m.UVs[f[2]]
		u := (uv0[0] + uv1[0] + uv2[0]) / 3
		v := (uv0[1] + uv1[1] + uv2[1]) / 3
		img := m.Textures[texIdx]
		b := img.Bounds()
		u = u - float32(int(u))
		if u < 0 {
			u += 1
		}
		v = v - float32(int(v))
		if v < 0 {
			v += 1
		}
		px := int(u*float32(b.Dx()-1)) + b.Min.X
		py := int(v*float32(b.Dy()-1)) + b.Min.Y
		r, g, bl, _ := img.At(px, py).RGBA()
		return [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8)}
	}
	if m.FaceBaseColor != nil && fi < len(m.FaceBaseColor) {
		c := m.FaceBaseColor[fi]
		return [3]uint8{c[0], c[1], c[2]}
	}
	return [3]uint8{128, 128, 128}
}

func writeMeshPNG(verts [][3]float32, faces [][3]uint32, colorFn func(fi int, u, v float64) [3]uint8, az, el float64, res int, path string) error {
	bounds := render.ProjectedBounds(verts, az, el)
	ci := render.RenderColor(verts, faces, az, el, res, bounds, colorFn)
	return writePNG(path, ci.ToRGBA())
}

func writeMeshDataPNG(mesh *pipeline.MeshData, az, el float64, res int, path string) error {
	nVerts := len(mesh.Vertices) / 3
	verts := make([][3]float32, nVerts)
	for i := 0; i < nVerts; i++ {
		verts[i] = [3]float32{
			mesh.Vertices[3*i],
			mesh.Vertices[3*i+1],
			mesh.Vertices[3*i+2],
		}
	}
	nFaces := len(mesh.Faces) / 3
	faces := make([][3]uint32, nFaces)
	for i := 0; i < nFaces; i++ {
		faces[i] = [3]uint32{
			mesh.Faces[3*i],
			mesh.Faces[3*i+1],
			mesh.Faces[3*i+2],
		}
	}
	return writeMeshPNG(verts, faces, func(fi int, u, v float64) [3]uint8 {
		if fi < 0 || fi*3+2 >= len(mesh.FaceColors) {
			return [3]uint8{128, 128, 128}
		}
		return [3]uint8{
			uint8(mesh.FaceColors[3*fi+0]),
			uint8(mesh.FaceColors[3*fi+1]),
			uint8(mesh.FaceColors[3*fi+2]),
		}
	}, az, el, res, path)
}

func writePNG(path string, img *image.RGBA) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
