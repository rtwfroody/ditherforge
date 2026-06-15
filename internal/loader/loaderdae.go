package loader

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LoadDAE loads a COLLADA (.dae) file and returns a LoadedModel. Texture images
// referenced by the document's <library_images> are resolved relative to the
// .dae file's directory.
//
// objectIndex is ignored: the whole visual scene is loaded as one model (its
// node hierarchy carries transforms and material bindings, not separately
// selectable objects).
func LoadDAE(path string, objectIndex int) (*LoadedModel, error) {
	daeBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading COLLADA: %w", err)
	}
	dir := filepath.Dir(path)
	open := func(name string) ([]byte, error) {
		ref := cleanOBJRef(name)
		p := filepath.Join(dir, filepath.FromSlash(ref))
		if b, err := os.ReadFile(p); err == nil {
			return b, nil
		}
		// Some exporters embed absolute or foreign paths in <init_from>; fall
		// back to the bare filename in the .dae's directory.
		return os.ReadFile(filepath.Join(dir, pathBase(ref)))
	}
	return loadDAECore(daeBytes, open)
}

// LoadDAEZip loads a COLLADA model packaged in a .zip archive (the .dae plus its
// texture images, the layout SketchUp and many other tools export). The first
// *.dae entry is used; texture images are resolved by matching the basename of
// each <init_from> reference against the archive entries.
//
// objectIndex is ignored (see LoadDAE).
func LoadDAEZip(zipPath string, objectIndex int) (*LoadedModel, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("opening COLLADA zip: %w", err)
	}
	defer zr.Close()

	byBase := make(map[string]*zip.File, len(zr.File))
	var daeFile *zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		byBase[strings.ToLower(pathBase(f.Name))] = f
		if daeFile == nil && strings.HasSuffix(strings.ToLower(f.Name), ".dae") {
			daeFile = f
		}
	}
	if daeFile == nil {
		return nil, fmt.Errorf("zip %q contains no .dae file", zipPath)
	}

	readEntry := func(f *zip.File) ([]byte, error) {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}

	daeBytes, err := readEntry(daeFile)
	if err != nil {
		return nil, fmt.Errorf("reading %q from zip: %w", daeFile.Name, err)
	}
	open := func(name string) ([]byte, error) {
		base := strings.ToLower(pathBase(cleanOBJRef(name)))
		if f, ok := byBase[base]; ok {
			return readEntry(f)
		}
		return nil, fmt.Errorf("entry %q not found in zip", name)
	}
	return loadDAECore(daeBytes, open)
}

// --- COLLADA XML schema (the subset we consume) ---

type daeCollada struct {
	XMLName    xml.Name         `xml:"COLLADA"`
	Asset      daeAsset         `xml:"asset"`
	Images     []daeImage       `xml:"library_images>image"`
	Materials  []daeMaterial    `xml:"library_materials>material"`
	Effects    []daeEffect      `xml:"library_effects>effect"`
	Geometries []daeGeometry    `xml:"library_geometries>geometry"`
	LibNodes   []daeNode        `xml:"library_nodes>node"`
	Scenes     []daeVisualScene `xml:"library_visual_scenes>visual_scene"`
}

type daeAsset struct {
	UpAxis string `xml:"up_axis"`
	Unit   struct {
		Meter float64 `xml:"meter,attr"`
		Name  string  `xml:"name,attr"`
	} `xml:"unit"`
}

type daeImage struct {
	ID       string `xml:"id,attr"`
	InitFrom string `xml:"init_from"`
}

type daeMaterial struct {
	ID             string `xml:"id,attr"`
	InstanceEffect struct {
		URL string `xml:"url,attr"`
	} `xml:"instance_effect"`
}

type daeEffect struct {
	ID      string `xml:"id,attr"`
	Profile struct {
		Newparams []daeNewparam `xml:"newparam"`
		Technique daeTechnique  `xml:"technique"`
	} `xml:"profile_COMMON"`
}

type daeNewparam struct {
	SID     string `xml:"sid,attr"`
	Surface struct {
		InitFrom string `xml:"init_from"`
	} `xml:"surface"`
	Sampler2D struct {
		Source string `xml:"source"`
	} `xml:"sampler2D"`
}

// daeTechnique holds whichever shading model the effect declares. Only one is
// non-nil in practice; daeShading collects the slots we care about.
type daeTechnique struct {
	Lambert  *daeShading `xml:"lambert"`
	Phong    *daeShading `xml:"phong"`
	Blinn    *daeShading `xml:"blinn"`
	Constant *daeShading `xml:"constant"`
}

type daeShading struct {
	Emission    *daeColorOrTexture `xml:"emission"`
	Diffuse     *daeColorOrTexture `xml:"diffuse"`
	Transparent *daeColorOrTexture `xml:"transparent"`
}

type daeColorOrTexture struct {
	Color   string `xml:"color"` // "r g b a"
	Texture struct {
		Texture  string `xml:"texture,attr"` // sampler sid
		Texcoord string `xml:"texcoord,attr"`
	} `xml:"texture"`
}

type daeGeometry struct {
	ID   string `xml:"id,attr"`
	Mesh struct {
		Sources   []daeSource    `xml:"source"`
		Vertices  daeVertices    `xml:"vertices"`
		Triangles []daeTriangles `xml:"triangles"`
		Polylists []daePolylist  `xml:"polylist"`
	} `xml:"mesh"`
}

type daeSource struct {
	ID    string `xml:"id,attr"`
	Float struct {
		Count int    `xml:"count,attr"`
		Data  string `xml:",chardata"`
	} `xml:"float_array"`
	Accessor struct {
		Count  int `xml:"count,attr"`
		Stride int `xml:"stride,attr"`
	} `xml:"technique_common>accessor"`
}

type daeVertices struct {
	ID     string     `xml:"id,attr"`
	Inputs []daeInput `xml:"input"`
}

type daeInput struct {
	Semantic string `xml:"semantic,attr"`
	Source   string `xml:"source,attr"`
	Offset   int    `xml:"offset,attr"`
	Set      int    `xml:"set,attr"`
}

type daeTriangles struct {
	Count    int        `xml:"count,attr"`
	Material string     `xml:"material,attr"`
	Inputs   []daeInput `xml:"input"`
	P        string     `xml:"p"`
}

type daePolylist struct {
	Count    int        `xml:"count,attr"`
	Material string     `xml:"material,attr"`
	Inputs   []daeInput `xml:"input"`
	VCount   string     `xml:"vcount"`
	P        string     `xml:"p"`
}

type daeVisualScene struct {
	ID    string    `xml:"id,attr"`
	Nodes []daeNode `xml:"node"`
}

type daeNode struct {
	ID        string                `xml:"id,attr"`
	Name      string                `xml:"name,attr"`
	Matrices  []string              `xml:"matrix"`
	Translate []string              `xml:"translate"`
	Rotate    []string              `xml:"rotate"`
	Scale     []string              `xml:"scale"`
	Children  []daeNode             `xml:"node"`
	Instances []daeInstanceGeometry `xml:"instance_geometry"`
	NodeRefs  []struct {
		URL string `xml:"url,attr"`
	} `xml:"instance_node"`
}

type daeInstanceGeometry struct {
	URL       string                `xml:"url,attr"`
	Materials []daeInstanceMaterial `xml:"bind_material>technique_common>instance_material"`
}

type daeInstanceMaterial struct {
	Symbol string `xml:"symbol,attr"`
	Target string `xml:"target,attr"`
}

// resolvedMaterial is the flattened appearance for an effect.
type resolvedMaterial struct {
	hasTexture bool
	texPath    string // <init_from> path of the diffuse texture (resolved image)
	baseColor  [4]uint8
	alpha      float32
}

// loadDAECore parses the COLLADA bytes and assembles a LoadedModel. open
// resolves a texture image (named by an <image>'s <init_from>) to its raw bytes;
// its meaning differs between the on-disk and zip variants.
func loadDAECore(daeBytes []byte, open func(name string) ([]byte, error)) (*LoadedModel, error) {
	var doc daeCollada
	if err := xml.Unmarshal(daeBytes, &doc); err != nil {
		return nil, fmt.Errorf("parsing COLLADA: %w", err)
	}
	if len(doc.Geometries) == 0 {
		return nil, fmt.Errorf("COLLADA file contains no geometry")
	}

	// Index libraries by id for cross-reference resolution. COLLADA url/source
	// attributes carry a leading '#'; stripURIRef removes it.
	imageByID := make(map[string]string, len(doc.Images))
	for _, im := range doc.Images {
		imageByID[im.ID] = strings.TrimSpace(im.InitFrom)
	}
	effectByID := make(map[string]*daeEffect, len(doc.Effects))
	for i := range doc.Effects {
		effectByID[doc.Effects[i].ID] = &doc.Effects[i]
	}
	materialByID := make(map[string]*daeMaterial, len(doc.Materials))
	for i := range doc.Materials {
		materialByID[doc.Materials[i].ID] = &doc.Materials[i]
	}
	geomByID := make(map[string]*daeGeometry, len(doc.Geometries))
	for i := range doc.Geometries {
		geomByID[doc.Geometries[i].ID] = &doc.Geometries[i]
	}

	// Resolve each material id to a flattened appearance, memoized.
	matCache := make(map[string]resolvedMaterial)
	resolveMaterial := func(matID string) resolvedMaterial {
		if r, ok := matCache[matID]; ok {
			return r
		}
		r := resolveDAEMaterial(matID, materialByID, effectByID, imageByID)
		matCache[matID] = r
		return r
	}

	// Axis conversion: slicers are Z-up. Convert from the document's up axis.
	upAxis := strings.ToUpper(strings.TrimSpace(doc.Asset.UpAxis))
	// Unit conversion to millimetres. <unit meter="X"> means one file unit is X
	// metres; ×1000 gives mm (matching the GLB loader's metres→mm convention).
	unitScale := float32(1)
	if doc.Asset.Unit.Meter > 0 {
		unitScale = float32(doc.Asset.Unit.Meter * 1000)
	}

	var (
		verts         [][3]float32
		faces         [][3]uint32
		uvs           [][2]float32
		faceTexIdx    []int32
		faceBaseColor [][4]uint8
		faceAlpha     []float32
		faceMeshIdx   []int32
	)
	texList := []image.Image{}
	texByPath := make(map[string]int) // image path → index in texList (-1 = decode failed)
	hasNonOpaque := false
	hasUntextured := false
	meshCounter := 0

	// resolveTexture decodes (once) the image at path and returns its texList
	// index, or -1 if it can't be loaded.
	resolveTexture := func(path string) int {
		if ti, ok := texByPath[path]; ok {
			return ti
		}
		imgBytes, err := open(path)
		if err != nil {
			fmt.Printf("  Warning: COLLADA texture %q not found: %v\n", path, err)
			texByPath[path] = -1
			return -1
		}
		img, _, err := image.Decode(bytes.NewReader(imgBytes))
		if err != nil {
			fmt.Printf("  Warning: COLLADA texture %q failed to decode: %v\n", path, err)
			texByPath[path] = -1
			return -1
		}
		ti := len(texList)
		texList = append(texList, img)
		texByPath[path] = ti
		return ti
	}

	// parsedGeom holds a geometry's float sources tokenized once. The same
	// geometry is often instanced many times (SketchUp emits each repeated part
	// via instance_node), so parsing is memoized to avoid re-tokenizing the
	// (potentially large) float arrays on every instance.
	type parsedGeom struct {
		sources   map[string][]float32 // source id → values
		srcStride map[string]int       // source id → accessor stride
		posSrc    string               // POSITION source the <vertices> resolves to
	}
	geomCache := make(map[*daeGeometry]*parsedGeom)
	parseGeom := func(g *daeGeometry) *parsedGeom {
		if pg, ok := geomCache[g]; ok {
			return pg
		}
		pg := &parsedGeom{
			sources:   make(map[string][]float32, len(g.Mesh.Sources)),
			srcStride: make(map[string]int, len(g.Mesh.Sources)),
		}
		for i := range g.Mesh.Sources {
			s := &g.Mesh.Sources[i]
			vals, err := parseFloat32s(s.Float.Data, s.Float.Count)
			if err != nil {
				continue
			}
			pg.sources[s.ID] = vals
			st := s.Accessor.Stride
			if st <= 0 {
				st = 3
			}
			pg.srcStride[s.ID] = st
		}
		// VERTEX inputs reference the <vertices> element; map it to its POSITION
		// source so a VERTEX offset resolves to actual coordinates.
		for _, in := range g.Mesh.Vertices.Inputs {
			if in.Semantic == "POSITION" {
				pg.posSrc = stripURIRef(in.Source)
			}
		}
		geomCache[g] = pg
		return pg
	}

	// emit appends one instance of a geometry, transformed by world, with the
	// given symbol→materialID bindings.
	emit := func(g *daeGeometry, world mat4, bind map[string]string) {
		pg := parseGeom(g)
		sources := pg.sources
		srcStride := pg.srcStride
		posSourceForVertices := pg.posSrc

		// De-index cache for this instance's primitives: a unique
		// (positionIndex, uvIndex, uvSource) triple maps to one output vertex.
		// The UV source is part of the key because two primitives in one mesh
		// can index different TEXCOORD sources with the same integer indices.
		type vkey struct {
			p, t  int
			uvSrc string
		}
		vcache := make(map[vkey]uint32)

		usedMesh := false

		// addCorner resolves one (posIdx,uvIdx) corner to an output vertex index.
		addCorner := func(posSrc string, posIdx int, uvSrc string, uvIdx int) (uint32, bool) {
			key := vkey{posIdx, uvIdx, uvSrc}
			if vi, ok := vcache[key]; ok {
				return vi, true
			}
			pv := sources[posSrc]
			ps := srcStride[posSrc]
			if pv == nil || (posIdx+1)*ps > len(pv) || posIdx < 0 {
				return 0, false
			}
			p := [3]float32{pv[posIdx*ps], pv[posIdx*ps+1], pv[posIdx*ps+2]}
			p = transformPoint(world, p)
			p = convertAxis(upAxis, p)
			p[0] *= unitScale
			p[1] *= unitScale
			p[2] *= unitScale

			uv := [2]float32{0, 0}
			if uvSrc != "" && uvIdx >= 0 {
				uvv := sources[uvSrc]
				us := srcStride[uvSrc]
				if uvv != nil && (uvIdx+1)*us <= len(uvv) {
					// COLLADA V origin is bottom-left; the sampler treats V=0 as
					// the top row, so flip V.
					uv = [2]float32{uvv[uvIdx*us], 1 - uvv[uvIdx*us+1]}
				}
			}

			vi := uint32(len(verts))
			verts = append(verts, p)
			uvs = append(uvs, uv)
			vcache[key] = vi
			return vi, true
		}

		// processPrim handles one triangles/polylist primitive: it reads the
		// interleaved index stream and appends faces. corners is a flat list of
		// per-corner index slots (already fan-triangulated for polylists).
		processPrim := func(matSymbol string, inputs []daeInput, indices []int, triCorners [][3]int) {
			stride, vertOff, texOff, hasTex := 1, 0, -1, false
			for _, in := range inputs {
				if in.Offset+1 > stride {
					stride = in.Offset + 1
				}
			}
			var texSrc string
			for _, in := range inputs {
				switch in.Semantic {
				case "VERTEX":
					vertOff = in.Offset
				case "TEXCOORD":
					texOff = in.Offset
					texSrc = stripURIRef(in.Source)
					hasTex = true
				}
			}
			rm := resolvedMaterialForSymbol(matSymbol, bind, resolveMaterial)
			texIdx := -1
			if rm.hasTexture && hasTex {
				texIdx = resolveTexture(rm.texPath)
			}

			for _, tri := range triCorners {
				var face [3]uint32
				ok := true
				for c := 0; c < 3; c++ {
					corner := tri[c]
					base := corner * stride
					if base+stride > len(indices) {
						ok = false
						break
					}
					posIdx := indices[base+vertOff]
					uvIdx := -1
					if hasTex && texOff >= 0 {
						uvIdx = indices[base+texOff]
					}
					vi, vok := addCorner(posSourceForVertices, posIdx, texSrc, uvIdx)
					if !vok {
						ok = false
						break
					}
					face[c] = vi
				}
				if !ok {
					continue
				}
				faces = append(faces, face)
				if texIdx >= 0 {
					faceTexIdx = append(faceTexIdx, int32(texIdx))
				} else {
					faceTexIdx = append(faceTexIdx, -1) // patched to sentinel below
					hasUntextured = true
				}
				bc := rm.baseColor
				al := rm.alpha
				if al < 1 {
					hasNonOpaque = true
				}
				faceBaseColor = append(faceBaseColor, bc)
				faceAlpha = append(faceAlpha, al)
				faceMeshIdx = append(faceMeshIdx, int32(meshCounter))
				usedMesh = true
			}
		}

		for i := range g.Mesh.Triangles {
			t := &g.Mesh.Triangles[i]
			indices, err := parseInts(t.P, t.Count*3*4)
			if err != nil {
				continue
			}
			stride := 1
			for _, in := range t.Inputs {
				if in.Offset+1 > stride {
					stride = in.Offset + 1
				}
			}
			nCorner := len(indices) / stride
			nTri := nCorner / 3
			triCorners := make([][3]int, 0, nTri)
			for k := 0; k < nTri; k++ {
				triCorners = append(triCorners, [3]int{k * 3, k*3 + 1, k*3 + 2})
			}
			processPrim(t.Material, t.Inputs, indices, triCorners)
		}

		for i := range g.Mesh.Polylists {
			pl := &g.Mesh.Polylists[i]
			vcounts, err := parseInts(pl.VCount, pl.Count*4)
			if err != nil {
				continue
			}
			indices, err := parseInts(pl.P, 0)
			if err != nil {
				continue
			}
			// Fan-triangulate each polygon (corner offsets are sequential;
			// correct for the convex faces exporters emit).
			var triCorners [][3]int
			corner := 0
			for _, vc := range vcounts {
				for k := 1; k+1 < vc; k++ {
					triCorners = append(triCorners, [3]int{corner, corner + k, corner + k + 1})
				}
				corner += vc
			}
			processPrim(pl.Material, pl.Inputs, indices, triCorners)
		}

		if usedMesh {
			meshCounter++
		}
	}

	// Library nodes are reusable subtrees referenced by <instance_node> (how
	// SketchUp instances repeated parts, e.g. each of the 88 keys).
	nodeByID := make(map[string]*daeNode, len(doc.LibNodes))
	for i := range doc.LibNodes {
		nodeByID[doc.LibNodes[i].ID] = &doc.LibNodes[i]
	}

	// Walk the scene graph, accumulating node transforms, and emit each
	// instance_geometry it references. depth guards against instance_node cycles.
	const maxNodeDepth = 256
	var visit func(n *daeNode, parent mat4, depth int)
	visit = func(n *daeNode, parent mat4, depth int) {
		if depth > maxNodeDepth {
			return
		}
		local := nodeLocalMatrix(n)
		world := mul(parent, local)
		for i := range n.Instances {
			inst := &n.Instances[i]
			gid := stripURIRef(inst.URL)
			g, ok := geomByID[gid]
			if !ok {
				continue
			}
			bind := make(map[string]string, len(inst.Materials))
			for _, im := range inst.Materials {
				bind[im.Symbol] = stripURIRef(im.Target)
			}
			emit(g, world, bind)
		}
		for i := range n.Children {
			visit(&n.Children[i], world, depth+1)
		}
		// <instance_node> re-enters a library node at the current transform; the
		// referenced node carries its own instance_geometry + bind_material.
		for _, ref := range n.NodeRefs {
			if target, ok := nodeByID[stripURIRef(ref.URL)]; ok {
				visit(target, world, depth+1)
			}
		}
	}
	for si := range doc.Scenes {
		for i := range doc.Scenes[si].Nodes {
			visit(&doc.Scenes[si].Nodes[i], identity(), 0)
		}
	}

	if len(faces) == 0 {
		return nil, fmt.Errorf("COLLADA file produced no triangles")
	}

	nTex := len(texList) // sentinel value for untextured faces
	var noTextureMask []bool
	if hasUntextured {
		noTextureMask = make([]bool, len(faces))
	}
	for i := range faceTexIdx {
		if faceTexIdx[i] < 0 {
			faceTexIdx[i] = int32(nTex)
			if noTextureMask != nil {
				noTextureMask[i] = true
			}
		}
	}

	var alphaOut []float32
	if hasNonOpaque {
		alphaOut = faceAlpha
	}

	return &LoadedModel{
		Vertices:       verts,
		Faces:          faces,
		UVs:            uvs,
		Textures:       texList,
		FaceTextureIdx: faceTexIdx,
		FaceAlpha:      alphaOut,
		FaceBaseColor:  faceBaseColor,
		NoTextureMask:  noTextureMask,
		FaceMeshIdx:    faceMeshIdx,
		NumMeshes:      meshCounter,
	}, nil
}

// resolvedMaterialForSymbol maps a triangle's material symbol to the resolved
// appearance via the instance bindings. An unbound or empty symbol yields a
// neutral grey untextured material.
func resolvedMaterialForSymbol(symbol string, bind map[string]string, resolve func(string) resolvedMaterial) resolvedMaterial {
	if symbol != "" {
		if matID, ok := bind[symbol]; ok {
			return resolve(matID)
		}
	}
	return resolvedMaterial{baseColor: [4]uint8{200, 200, 200, 255}, alpha: 1}
}

// resolveDAEMaterial flattens a material id to a renderable appearance by
// following material → effect → diffuse (texture or color).
func resolveDAEMaterial(matID string, mats map[string]*daeMaterial, effects map[string]*daeEffect, images map[string]string) resolvedMaterial {
	def := resolvedMaterial{baseColor: [4]uint8{200, 200, 200, 255}, alpha: 1}
	mat, ok := mats[matID]
	if !ok {
		return def
	}
	eff, ok := effects[stripURIRef(mat.InstanceEffect.URL)]
	if !ok {
		return def
	}

	shading := firstShading(&eff.Profile.Technique)
	if shading == nil {
		return def
	}
	// Prefer diffuse; fall back to emission, then to the transparent color that
	// SketchUp's "constant" effects use for solid colors.
	cot := shading.Diffuse
	fromTransparent := false
	if cot == nil || (cot.Color == "" && cot.Texture.Texture == "") {
		if shading.Emission != nil && shading.Emission.Color != "" {
			cot = shading.Emission
		} else if shading.Transparent != nil && shading.Transparent.Color != "" {
			cot = shading.Transparent
			fromTransparent = true
		}
	}
	if cot == nil {
		return def
	}

	if cot.Texture.Texture != "" {
		// Resolve sampler sid → surface sid → image init_from.
		if path := resolveSamplerImage(cot.Texture.Texture, eff, images); path != "" {
			return resolvedMaterial{hasTexture: true, texPath: path, baseColor: [4]uint8{200, 200, 200, 255}, alpha: 1}
		}
		return def
	}

	if cot.Color != "" {
		rgba, err := parseFloat32s(cot.Color, 4)
		if err == nil && len(rgba) >= 3 {
			alpha := float32(1)
			// A <transparent> color's 4th component is opacity-mask semantics
			// (opaque=A_ONE/RGB_ZERO), not a diffuse alpha — borrow only its RGB
			// so an a=0 mask can't mark the face transparent and drop it.
			if len(rgba) >= 4 && !fromTransparent {
				alpha = clampF32(rgba[3], 0, 1)
			}
			return resolvedMaterial{
				baseColor: [4]uint8{
					uint8(clampF32(rgba[0], 0, 1)*255 + 0.5),
					uint8(clampF32(rgba[1], 0, 1)*255 + 0.5),
					uint8(clampF32(rgba[2], 0, 1)*255 + 0.5),
					uint8(alpha*255 + 0.5),
				},
				alpha: alpha,
			}
		}
	}
	return def
}

// firstShading returns the first non-nil shading model in a technique.
func firstShading(t *daeTechnique) *daeShading {
	switch {
	case t.Lambert != nil:
		return t.Lambert
	case t.Phong != nil:
		return t.Phong
	case t.Blinn != nil:
		return t.Blinn
	case t.Constant != nil:
		return t.Constant
	}
	return nil
}

// resolveSamplerImage follows sampler sid → surface sid → image to a texture
// file path. The surface's <init_from> is usually an <image> id, but some
// exporters put a file path there directly.
func resolveSamplerImage(samplerSID string, eff *daeEffect, images map[string]string) string {
	var surfaceSID string
	for i := range eff.Profile.Newparams {
		np := &eff.Profile.Newparams[i]
		if np.SID == samplerSID && np.Sampler2D.Source != "" {
			surfaceSID = strings.TrimSpace(np.Sampler2D.Source)
		}
	}
	if surfaceSID == "" {
		// Some files reference the image id directly as the sampler target.
		if p, ok := images[samplerSID]; ok {
			return p
		}
		return ""
	}
	for i := range eff.Profile.Newparams {
		np := &eff.Profile.Newparams[i]
		if np.SID == surfaceSID && np.Surface.InitFrom != "" {
			ref := strings.TrimSpace(np.Surface.InitFrom)
			if p, ok := images[ref]; ok {
				return p
			}
			return ref // already a file path
		}
	}
	return ""
}

// nodeLocalMatrix composes a node's local transform. <matrix> elements are
// applied in document order; any translate/rotate/scale follow. The common case
// (a single <matrix>, as SketchUp exports) is exact; mixed transform orderings
// are rare and approximated as M·T·R·S.
func nodeLocalMatrix(n *daeNode) mat4 {
	m := identity()
	for _, ms := range n.Matrices {
		vals, err := parseFloat64s(ms, 16)
		if err != nil || len(vals) < 16 {
			continue
		}
		m = mul(m, colladaMatrix(vals))
	}
	for _, ts := range n.Translate {
		v, err := parseFloat64s(ts, 3)
		if err != nil || len(v) < 3 {
			continue
		}
		t := identity()
		t[12], t[13], t[14] = v[0], v[1], v[2]
		m = mul(m, t)
	}
	for _, rs := range n.Rotate {
		v, err := parseFloat64s(rs, 4)
		if err != nil || len(v) < 4 {
			continue
		}
		m = mul(m, axisAngleMatrix(v[0], v[1], v[2], v[3]))
	}
	for _, ss := range n.Scale {
		v, err := parseFloat64s(ss, 3)
		if err != nil || len(v) < 3 {
			continue
		}
		s := identity()
		s[0], s[5], s[10] = v[0], v[1], v[2]
		m = mul(m, s)
	}
	return m
}

// colladaMatrix converts 16 row-major COLLADA floats into the internal
// column-major mat4.
func colladaMatrix(v []float64) mat4 {
	var m mat4
	for row := 0; row < 4; row++ {
		for col := 0; col < 4; col++ {
			m[col*4+row] = v[row*4+col]
		}
	}
	return m
}

// axisAngleMatrix builds a column-major rotation matrix for a COLLADA <rotate>
// (axis x,y,z and angle in degrees).
func axisAngleMatrix(x, y, z, angleDeg float64) mat4 {
	const deg2rad = 3.141592653589793 / 180
	a := angleDeg * deg2rad
	s, c := math.Sin(a), math.Cos(a)
	t := 1 - c
	// Normalize axis.
	l := math.Sqrt(x*x + y*y + z*z)
	if l == 0 {
		return identity()
	}
	x, y, z = x/l, y/l, z/l
	return mat4{
		t*x*x + c, t*x*y + s*z, t*x*z - s*y, 0,
		t*x*y - s*z, t*y*y + c, t*y*z + s*x, 0,
		t*x*z + s*y, t*y*z - s*x, t*z*z + c, 0,
		0, 0, 0, 1,
	}
}

// convertAxis rotates a point from the document's up axis into Z-up (the slicer
// convention). Y_UP matches the OBJ/GLB Y-up→Z-up transform; Z_UP is identity.
func convertAxis(upAxis string, p [3]float32) [3]float32 {
	switch upAxis {
	case "Y_UP":
		return [3]float32{p[0], -p[2], p[1]}
	case "X_UP":
		return [3]float32{-p[1], p[0], p[2]}
	default: // Z_UP (or unspecified)
		return p
	}
}

// stripURIRef removes a leading '#' from a COLLADA url/source reference.
func stripURIRef(s string) string {
	s = strings.TrimSpace(s)
	return strings.TrimPrefix(s, "#")
}

// parseFloat32s parses whitespace-separated floats. hint preallocates capacity.
func parseFloat32s(s string, hint int) ([]float32, error) {
	out := make([]float32, 0, hint)
	n := len(s)
	i := 0
	for i < n {
		for i < n && isASCIISpace(s[i]) {
			i++
		}
		if i >= n {
			break
		}
		j := i
		for j < n && !isASCIISpace(s[j]) {
			j++
		}
		f, err := strconv.ParseFloat(s[i:j], 32)
		if err != nil {
			return nil, err
		}
		out = append(out, float32(f))
		i = j
	}
	return out, nil
}

// parseFloat64s parses whitespace-separated float64s.
func parseFloat64s(s string, hint int) ([]float64, error) {
	out := make([]float64, 0, hint)
	n := len(s)
	i := 0
	for i < n {
		for i < n && isASCIISpace(s[i]) {
			i++
		}
		if i >= n {
			break
		}
		j := i
		for j < n && !isASCIISpace(s[j]) {
			j++
		}
		f, err := strconv.ParseFloat(s[i:j], 64)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
		i = j
	}
	return out, nil
}

// parseInts parses whitespace-separated ints. hint preallocates capacity.
func parseInts(s string, hint int) ([]int, error) {
	out := make([]int, 0, hint)
	n := len(s)
	i := 0
	for i < n {
		for i < n && isASCIISpace(s[i]) {
			i++
		}
		if i >= n {
			break
		}
		j := i
		for j < n && !isASCIISpace(s[j]) {
			j++
		}
		v, err := strconv.Atoi(s[i:j])
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		i = j
	}
	return out, nil
}

func isASCIISpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == '\v'
}

// LoadZip loads a 3D model packaged in a .zip archive, dispatching by the model
// file it contains: an OBJ archive (.obj + .mtl + textures) goes to LoadOBJZip,
// a COLLADA archive (.dae + textures) to LoadDAEZip. An .obj takes precedence if
// both are present.
func LoadZip(path string, objectIndex int) (*LoadedModel, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("opening zip: %w", err)
	}
	hasOBJ, hasDAE := false, false
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := strings.ToLower(f.Name)
		if strings.HasSuffix(name, ".obj") {
			hasOBJ = true
		} else if strings.HasSuffix(name, ".dae") {
			hasDAE = true
		}
	}
	zr.Close()

	switch {
	case hasOBJ:
		return LoadOBJZip(path, objectIndex)
	case hasDAE:
		return LoadDAEZip(path, objectIndex)
	default:
		return nil, fmt.Errorf("zip %q contains no .obj or .dae model file", path)
	}
}
