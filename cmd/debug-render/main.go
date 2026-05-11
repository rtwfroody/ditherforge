// debug-render runs the full pipeline (same code path as the GUI)
// on a model and writes PNG renders of the output mesh from
// multiple viewpoints, in both normal (dithered) and
// ShowSampledColors (raw pre-dither RGB) modes. Used for
// autonomous debugging without a human in the loop: I run this,
// read the PNGs, and iterate on fixes.
//
//	go run ./cmd/debug-render \
//	    --input /home/tnewsome/Documents/3d_print/objects/cut_fish.glb \
//	    --out /tmp/df_debug
//
// PNGs are written to <out>/<mode>_<view>.png where mode is
// "dithered" or "sampled" and view is one of "front", "side",
// "top", "persp". <out>/input_<view>.png shows the original
// model for reference comparison.
package main

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexflint/go-arg"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/palette"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
	"github.com/rtwfroody/ditherforge/internal/render"
)

type args struct {
	Input      string  `arg:"--input,required" help:"model file (.stl/.glb/.3mf)"`
	Out        string  `arg:"--out" default:"/tmp/df_debug" help:"output directory for PNGs"`
	Inventory  string  `arg:"--inventory" default:"/home/tnewsome/.config/ditherforge/collections/Inventory.txt" help:"inventory file"`
	NumColors  int     `arg:"--colors" default:"6"`
	Size       float32 `arg:"--size" default:"100" help:"target longest dimension in mm; 0 = no rescale"`
	LayerH     float32 `arg:"--layer" default:"0.2"`
	Nozzle     float32 `arg:"--nozzle" default:"0.4"`
	Dither     string  `arg:"--dither" default:"riemersma"`
	Resolution int     `arg:"--res" default:"800"`
	NoSimplify bool    `arg:"--no-simplify"`
	NoInput    bool    `arg:"--no-input" help:"skip rendering the input reference"`
	NoDither   bool    `arg:"--no-dither" help:"skip the dithered-output render"`
}

func main() {
	var a args
	arg.MustParse(&a)

	if err := os.MkdirAll(a.Out, 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}

	inv, err := palette.ParseInventoryFile(a.Inventory)
	if err != nil {
		log.Fatalf("inventory: %v", err)
	}
	invColors := make([][3]uint8, len(inv))
	invLabels := make([]string, len(inv))
	for i, e := range inv {
		invColors[i] = e.Color
		invLabels[i] = e.Label
	}

	sizePtr := &a.Size
	if a.Size == 0 {
		sizePtr = nil
	}
	opts := pipeline.Options{
		Input:           a.Input,
		Scale:           1, // pipeline multiplies by Scale; 0 would zero the model
		NumColors:       a.NumColors,
		InventoryColors: invColors,
		InventoryLabels: invLabels,
		NozzleDiameter:  a.Nozzle,
		LayerHeight:     a.LayerH,
		Dither:          a.Dither,
		ColorSnap:       5,
		Size:            sizePtr,
		Force:           true, // skip the 300mm-extent guard
		NoSimplify:      a.NoSimplify,
	}

	cache := pipeline.NewStageCache() // in-memory only; we don't need disk

	views := []struct {
		name          string
		azimuth, elev float64
	}{
		{"front", 0, 0},
		{"side", 90, 0},
		{"top", 0, 90},
		{"persp", 45, 25},
	}

	// Reference: render the *input* mesh (before any of our pipeline
	// runs). Gives us ground truth for color comparison.
	if !a.NoInput {
		inputMesh, err := loadInputMesh(a.Input, sizePtr)
		if err != nil {
			log.Printf("load input for reference render failed: %v", err)
		} else {
			for _, v := range views {
				path := filepath.Join(a.Out, fmt.Sprintf("input_%s.png", v.name))
				if err := renderAndWrite(inputMesh, v.azimuth, v.elev, a.Resolution, path); err != nil {
					log.Fatalf("write %s: %v", path, err)
				}
				log.Printf("wrote %s", path)
			}
		}
	}

	modes := []struct {
		name string
		ssc  bool
	}{}
	if !a.NoDither {
		modes = append(modes, struct {
			name string
			ssc  bool
		}{"dithered", false})
	}
	modes = append(modes, struct {
		name string
		ssc  bool
	}{"sampled", true})

	for _, mode := range modes {
		opts.ShowSampledColors = mode.ssc
		log.Printf("running pipeline (%s)...", mode.name)
		result, err := pipeline.RunCached(context.Background(), cache, opts, nil)
		if err != nil {
			log.Fatalf("pipeline %s: %v", mode.name, err)
		}
		if result.OutputMesh == nil {
			log.Fatalf("pipeline %s returned no OutputMesh", mode.name)
		}
		mesh := result.OutputMesh
		log.Printf("  %s: %d verts, %d faces", mode.name,
			len(mesh.Vertices)/3, len(mesh.Faces)/3)

		for _, v := range views {
			path := filepath.Join(a.Out, fmt.Sprintf("%s_%s.png", mode.name, v.name))
			if err := renderMeshDataToPNG(mesh, v.azimuth, v.elev, a.Resolution, path); err != nil {
				log.Fatalf("write %s: %v", path, err)
			}
			log.Printf("wrote %s", path)
		}
	}
}

// loadInputMesh loads a model file and applies the same scale
// normalization the pipeline does, returning a MeshData with
// FaceColors driven by the model's per-face texture/material
// (sampled at face centroid via voxel.SampleNearestColor when
// requested). For the reference render we use the model's vertex
// colors / texture average per face — cheap and visual.
func loadInputMesh(path string, sizePtr *float32) (*meshForRender, error) {
	model, err := loadModel(path)
	if err != nil {
		return nil, err
	}
	scale := unitScale(filepath.Ext(path))
	if scale != 1 {
		loader.ScaleModel(model, scale)
	}
	if sizePtr != nil {
		ext := maxExtent(model)
		if ext > 0 {
			loader.ScaleModel(model, *sizePtr/ext)
		}
	}

	// Build a faceColor per face by sampling the texture at the
	// face's barycentric center.
	faceColors := make([][3]uint8, len(model.Faces))
	for fi := range model.Faces {
		c := faceCentroidColor(model, fi)
		faceColors[fi] = c
	}
	return &meshForRender{
		vertices:   model.Vertices,
		faces:      model.Faces,
		faceColors: faceColors,
	}, nil
}

type meshForRender struct {
	vertices   [][3]float32
	faces      [][3]uint32
	faceColors [][3]uint8
}

func renderAndWrite(m *meshForRender, az, el float64, res int, path string) error {
	bounds := render.ProjectedBounds(m.vertices, az, el)
	ci := render.RenderColor(m.vertices, m.faces, az, el, res, bounds,
		func(fi int, u, v float64) [3]uint8 {
			if fi < 0 || fi >= len(m.faceColors) {
				return [3]uint8{128, 128, 128}
			}
			return m.faceColors[fi]
		})
	return writePNG(path, ci.ToRGBA())
}

func renderMeshDataToPNG(mesh *pipeline.MeshData, az, el float64, res int, path string) error {
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
	bounds := render.ProjectedBounds(verts, az, el)
	ci := render.RenderColor(verts, faces, az, el, res, bounds,
		func(fi int, u, v float64) [3]uint8 {
			if fi < 0 || fi*3+2 >= len(mesh.FaceColors) {
				return [3]uint8{128, 128, 128}
			}
			return [3]uint8{
				uint8(mesh.FaceColors[3*fi+0]),
				uint8(mesh.FaceColors[3*fi+1]),
				uint8(mesh.FaceColors[3*fi+2]),
			}
		})
	return writePNG(path, ci.ToRGBA())
}

func writePNG(path string, img *image.RGBA) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func loadModel(path string) (*loader.LoadedModel, error) {
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
func unitScale(ext string) float32 {
	if strings.ToLower(ext) == ".glb" {
		return 1000
	}
	return 1
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

// faceCentroidColor samples the model's texture / vertex colors /
// base color at the centroid of face fi. Used for the reference
// "input" render so the user has a known-correct image to compare
// the pipeline output against.
func faceCentroidColor(m *loader.LoadedModel, fi int) [3]uint8 {
	if fi < 0 || fi >= len(m.Faces) {
		return [3]uint8{128, 128, 128}
	}
	f := m.Faces[fi]
	// Try vertex colors first (cheap).
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
	// Texture lookup via UV centroid.
	if m.UVs != nil && m.FaceTextureIdx != nil && m.FaceTextureIdx[fi] >= 0 &&
		int(m.FaceTextureIdx[fi]) < len(m.Textures) {
		texIdx := int(m.FaceTextureIdx[fi])
		uv0 := m.UVs[f[0]]
		uv1 := m.UVs[f[1]]
		uv2 := m.UVs[f[2]]
		u := (uv0[0] + uv1[0] + uv2[0]) / 3
		v := (uv0[1] + uv1[1] + uv2[1]) / 3
		img := m.Textures[texIdx]
		// Defer to voxel.BilinearSample if we want; for simplicity
		// just nearest-pixel here.
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
	// Base color fallback.
	if m.FaceBaseColor != nil && fi < len(m.FaceBaseColor) {
		c := m.FaceBaseColor[fi]
		return [3]uint8{c[0], c[1], c[2]}
	}
	return [3]uint8{128, 128, 128}
}
