// Package debugrender renders the pipeline's input and output
// meshes from a fixed set of orthographic viewpoints (front, side,
// top, perspective) to RGBA images. It's used by the
// --debug-render CLI flag for headless visual inspection, and by
// tests that assert the sampled-mode output is close to the input.
//
// "Input" here means the original mesh as loaded by the loader,
// post unit-scale and size-normalization (so it sits in the same
// world frame as the pipeline output). Each face is colored by a
// quick texture / vertex-color / base-color centroid sample so we
// have a ground-truth image to compare pipeline results against.
package debugrender

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
	"github.com/rtwfroody/ditherforge/internal/render"
)

// View is one orthographic camera angle.
type View struct {
	Name     string
	Azimuth  float64
	Elev     float64
}

// DefaultViews lists the four views the debug renderer writes when
// no explicit set is given. Front / side / top are axis-aligned;
// persp is a moderate-elevation 3/4 view.
var DefaultViews = []View{
	{"front", 0, 0},
	{"side", 90, 0},
	{"top", 0, 90},
	{"persp", 45, 25},
}

// InputMesh wraps the loaded model and exposes a per-pixel ColorAt
// hook the renderer calls during rasterization. We keep the full
// loader.LoadedModel reference so ColorAt can interpolate UVs /
// vertex colors per pixel instead of flattening to one color per
// triangle (which made low-poly textured meshes — earth.glb in
// particular — render as obviously-faceted flat patches).
//
// Colors[fi] is the centroid color of face fi, retained for tests
// that want a single representative RGB per triangle (e.g. the
// extreme-axis side-color comparisons in sampled_match_input_test).
type InputMesh struct {
	Model    *loader.LoadedModel
	Vertices [][3]float32
	Faces    [][3]uint32
	Colors   [][3]uint8
}

// ColorAt returns the per-pixel color the renderer wants at
// barycentric (u, v) within face fi. (u, v) follow render's
// convention: vertex 0 at (0,0), vertex 1 at (1,0), vertex 2 at
// (0,1); the third weight is (1 - u - v).
func (m *InputMesh) ColorAt(fi int, u, v float64) [3]uint8 {
	if m.Model == nil || fi < 0 || fi >= len(m.Faces) {
		if fi >= 0 && fi < len(m.Colors) {
			return m.Colors[fi]
		}
		return [3]uint8{128, 128, 128}
	}
	w0 := 1 - u - v
	w1 := u
	w2 := v
	f := m.Faces[fi]
	mod := m.Model

	if mod.VertexColors != nil {
		c0 := mod.VertexColors[f[0]]
		c1 := mod.VertexColors[f[1]]
		c2 := mod.VertexColors[f[2]]
		return [3]uint8{
			uint8(clamp255(w0*float64(c0[0]) + w1*float64(c1[0]) + w2*float64(c2[0]))),
			uint8(clamp255(w0*float64(c0[1]) + w1*float64(c1[1]) + w2*float64(c2[1]))),
			uint8(clamp255(w0*float64(c0[2]) + w1*float64(c1[2]) + w2*float64(c2[2]))),
		}
	}
	if mod.UVs != nil && mod.FaceTextureIdx != nil && fi < len(mod.FaceTextureIdx) &&
		mod.FaceTextureIdx[fi] >= 0 && int(mod.FaceTextureIdx[fi]) < len(mod.Textures) {
		texIdx := int(mod.FaceTextureIdx[fi])
		uv0 := mod.UVs[f[0]]
		uv1 := mod.UVs[f[1]]
		uv2 := mod.UVs[f[2]]
		uu := w0*float64(uv0[0]) + w1*float64(uv1[0]) + w2*float64(uv2[0])
		vv := w0*float64(uv0[1]) + w1*float64(uv1[1]) + w2*float64(uv2[1])
		img := mod.Textures[texIdx]
		b := img.Bounds()
		uu = uu - float64(int(uu))
		if uu < 0 {
			uu += 1
		}
		vv = vv - float64(int(vv))
		if vv < 0 {
			vv += 1
		}
		px := int(uu*float64(b.Dx()-1)) + b.Min.X
		py := int(vv*float64(b.Dy()-1)) + b.Min.Y
		r, g, bl, _ := img.At(px, py).RGBA()
		return [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8)}
	}
	if mod.FaceBaseColor != nil && fi < len(mod.FaceBaseColor) {
		c := mod.FaceBaseColor[fi]
		return [3]uint8{c[0], c[1], c[2]}
	}
	return [3]uint8{128, 128, 128}
}

func clamp255(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 255 {
		return 255
	}
	return x
}

// LoadInputMesh loads the model at path and applies the same unit
// scale (GLB → mm) and optional size normalization the pipeline
// uses, so the resulting mesh shares a world frame with the
// pipeline's output mesh. Colors[fi] is the centroid color of face
// fi (used by tests that want a per-triangle representative color);
// per-pixel rendering goes through ColorAt, which interpolates UVs
// or vertex colors on the underlying loader.LoadedModel.
func LoadInputMesh(path string, sizePtr *float32) (*InputMesh, error) {
	model, err := LoadAnyModel(path)
	if err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".glb" {
		loader.ScaleModel(model, 1000)
	}
	if sizePtr != nil {
		e := MaxExtent(model)
		if e > 0 {
			loader.ScaleModel(model, *sizePtr/e)
		}
	}
	colors := make([][3]uint8, len(model.Faces))
	for fi := range model.Faces {
		colors[fi] = FaceCentroidColor(model, fi)
	}
	return &InputMesh{
		Model:    model,
		Vertices: model.Vertices,
		Faces:    model.Faces,
		Colors:   colors,
	}, nil
}

// LoadAnyModel dispatches to the right loader for a given file
// extension. Returns an error for unsupported formats.
//
// Uses ObjectIndex = -1 ("all objects") so multi-primitive scenes
// (e.g. a building with floor + walls + windows + roof as
// separate glTF meshes) render in full instead of showing only
// the first primitive. Matches the pipeline's default — the CLI
// passes -1 for the same reason.
func LoadAnyModel(path string) (*loader.LoadedModel, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".glb":
		return loader.LoadGLB(path, -1)
	case ".3mf":
		return loader.Load3MF(path, -1)
	case ".stl":
		return loader.LoadSTL(path, -1)
	}
	return nil, fmt.Errorf("unsupported format: %s", filepath.Ext(path))
}

// MaxExtent returns the largest XYZ axis range of the model.
func MaxExtent(m *loader.LoadedModel) float32 {
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

// FaceCentroidColor samples a representative RGB for face fi:
// vertex colors (averaged) when present, else a UV-centroid
// texture lookup, else the face's base color, else mid-grey.
func FaceCentroidColor(m *loader.LoadedModel, fi int) [3]uint8 {
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

// RenderInput renders an InputMesh as an RGBA image with a
// transparent background, using the provided view.
func RenderInput(m *InputMesh, v View, res int) *image.RGBA {
	bounds := render.ProjectedBounds(m.Vertices, v.Azimuth, v.Elev)
	ci := render.RenderColor(m.Vertices, m.Faces, v.Azimuth, v.Elev, res, bounds, m.ColorAt)
	return ci.ToRGBA()
}

// RenderPipelineMesh renders a pipeline output MeshData using its
// per-face FaceColors (which the ShowSampledColors override
// rewrites in sampled mode).
func RenderPipelineMesh(mesh *pipeline.MeshData, v View, res int) *image.RGBA {
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
	bounds := render.ProjectedBounds(verts, v.Azimuth, v.Elev)
	ci := render.RenderColor(verts, faces, v.Azimuth, v.Elev, res, bounds,
		func(fi int, u, vv float64) [3]uint8 {
			if fi < 0 || fi*3+2 >= len(mesh.FaceColors) {
				return [3]uint8{128, 128, 128}
			}
			return [3]uint8{
				uint8(mesh.FaceColors[3*fi+0]),
				uint8(mesh.FaceColors[3*fi+1]),
				uint8(mesh.FaceColors[3*fi+2]),
			}
		})
	return ci.ToRGBA()
}

// WritePNG saves an RGBA image to disk.
func WritePNG(path string, img *image.RGBA) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
