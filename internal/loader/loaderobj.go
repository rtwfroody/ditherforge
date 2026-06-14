package loader

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/udhos/gwob"
)

// LoadOBJ loads a Wavefront .obj file and returns a LoadedModel. The companion
// material library (mtllib) and any diffuse textures (map_Kd) are resolved
// relative to the .obj file's directory.
//
// objectIndex is ignored: an OBJ file is treated as a single object (its "o"/"g"
// groups only carry material assignments, not separately selectable objects).
func LoadOBJ(path string, objectIndex int) (*LoadedModel, error) {
	objBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading OBJ: %w", err)
	}
	dir := filepath.Dir(path)
	open := func(name string) ([]byte, error) {
		ref := cleanOBJRef(name)
		p := filepath.Join(dir, filepath.FromSlash(ref))
		if b, err := os.ReadFile(p); err == nil {
			return b, nil
		}
		// Fall back to the bare filename in the .obj's directory; some
		// exporters embed absolute or foreign paths in mtllib/map_Kd.
		return os.ReadFile(filepath.Join(dir, pathBase(ref)))
	}
	return loadOBJCore(filepath.Base(path), objBytes, open)
}

// LoadOBJZip loads a Wavefront OBJ packaged in a .zip archive (the .obj plus its
// .mtl and texture images, as exported by many tools). The first *.obj entry is
// used; companion files are resolved by matching the basename of each
// mtllib/map_Kd reference against the archive entries.
//
// objectIndex is ignored (see LoadOBJ).
func LoadOBJZip(zipPath string, objectIndex int) (*LoadedModel, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("opening OBJ zip: %w", err)
	}
	defer zr.Close()

	// Index every file entry by lowercased basename for companion lookup, and
	// locate the first .obj entry.
	byBase := make(map[string]*zip.File, len(zr.File))
	var objFile *zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		byBase[strings.ToLower(pathBase(f.Name))] = f
		if objFile == nil && strings.HasSuffix(strings.ToLower(f.Name), ".obj") {
			objFile = f
		}
	}
	if objFile == nil {
		return nil, fmt.Errorf("zip %q contains no .obj file", zipPath)
	}

	readEntry := func(f *zip.File) ([]byte, error) {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}

	objBytes, err := readEntry(objFile)
	if err != nil {
		return nil, fmt.Errorf("reading %q from zip: %w", objFile.Name, err)
	}
	open := func(name string) ([]byte, error) {
		base := strings.ToLower(pathBase(cleanOBJRef(name)))
		if f, ok := byBase[base]; ok {
			return readEntry(f)
		}
		return nil, fmt.Errorf("entry %q not found in zip", name)
	}
	return loadOBJCore(pathBase(objFile.Name), objBytes, open)
}

// loadOBJCore parses the OBJ bytes and assembles a LoadedModel. open resolves a
// companion file (the .mtl named by mtllib, or a texture named by map_Kd) to its
// raw bytes; its meaning differs between the on-disk and zip variants.
func loadOBJCore(objName string, objBytes []byte, open func(name string) ([]byte, error)) (*LoadedModel, error) {
	// Logger is nil → gwob stays silent; we surface our own warnings. Normals
	// are unused downstream, so skip storing them to save memory.
	opts := &gwob.ObjParserOptions{IgnoreNormals: true}
	o, err := gwob.NewObjFromBuf(objName, objBytes, opts)
	if err != nil {
		return nil, fmt.Errorf("parsing OBJ: %w", err)
	}
	if len(o.Indices) < 3 {
		return nil, fmt.Errorf("OBJ contains no triangles")
	}

	// gwob only triangulates triangles and quads; faces with 5+ vertices are
	// dropped during parsing. Warn rather than silently lose that geometry.
	if n := countNgonFaces(objBytes); n > 0 {
		fmt.Printf("  Warning: %d OBJ face(s) with 5+ vertices were skipped (only tris/quads are supported)\n", n)
	}

	// Load the material library, if referenced. A missing or malformed .mtl is
	// non-fatal: faces fall back to a neutral base color.
	lib := gwob.NewMaterialLib()
	if o.Mtllib != "" {
		if mtlBytes, err := open(o.Mtllib); err != nil {
			fmt.Printf("  Warning: OBJ material library %q not found: %v\n", o.Mtllib, err)
		} else if parsed, err := gwob.ReadMaterialLibFromBuf(mtlBytes, opts); err != nil {
			fmt.Printf("  Warning: OBJ material library %q failed to parse: %v\n", o.Mtllib, err)
		} else {
			lib = parsed
		}
	}

	// Decode each material's diffuse texture (map_Kd), deduplicated by reference.
	// Skip entirely when the mesh has no texture coordinates — there is nothing
	// to sample against, and the (possibly huge) images would be dead weight.
	var texList []image.Image
	matTexIdx := make(map[string]int, len(lib.Lib)) // material name → texList index (-1 = none)
	if o.TextCoordFound {
		refToTex := map[string]int{}
		for name, mat := range lib.Lib {
			matTexIdx[name] = -1
			if mat.MapKd == "" {
				continue
			}
			ref := cleanOBJRef(mat.MapKd)
			if ti, ok := refToTex[ref]; ok {
				matTexIdx[name] = ti
				continue
			}
			imgBytes, err := open(ref)
			if err != nil {
				fmt.Printf("  Warning: OBJ texture %q for material %q not found: %v\n", ref, name, err)
				continue
			}
			img, _, err := image.Decode(bytes.NewReader(imgBytes))
			if err != nil {
				fmt.Printf("  Warning: OBJ texture %q failed to decode: %v\n", ref, err)
				continue
			}
			ti := len(texList)
			texList = append(texList, img)
			refToTex[ref] = ti
			matTexIdx[name] = ti
		}
	}
	nTex := len(texList) // sentinel value in FaceTextureIdx for untextured faces

	// Extract per-vertex positions and UVs from gwob's interleaved Coord buffer.
	// gwob has already merged each unique v/vt/vn combination into one vertex.
	//
	// gwob uses a single global stride but only appends UV floats for the
	// face-vertices that actually carried a vt index. An OBJ that mixes
	// "f a/at …" and "f a …" faces therefore yields a ragged Coord buffer the
	// uniform-stride math below would misread (garbage vertices, and indices
	// pointing past the buffer). Detect that and fail clearly instead of
	// returning corrupt geometry or panicking.
	floatsPerStride := o.StrideSize / 4
	if floatsPerStride == 0 || len(o.Coord)%floatsPerStride != 0 {
		return nil, fmt.Errorf("OBJ mixes faces with and without texture coordinates, which is not supported")
	}
	nVert := len(o.Coord) / floatsPerStride
	maxIdx := 0
	for _, idx := range o.Indices {
		if idx > maxIdx {
			maxIdx = idx
		}
	}
	if maxIdx >= nVert {
		return nil, fmt.Errorf("OBJ mixes faces with and without texture coordinates, which is not supported")
	}

	verts := make([][3]float32, nVert)
	uvs := make([][2]float32, nVert)
	texOff := o.StrideOffsetTexture / 4
	for s := 0; s < nVert; s++ {
		base := s * floatsPerStride
		x, y, z := o.Coord[base], o.Coord[base+1], o.Coord[base+2]
		// OBJ is Y-up; slicers are Z-up (same convention as the GLB loader).
		verts[s] = [3]float32{x, -z, y}
		if o.TextCoordFound {
			u, v := o.Coord[base+texOff], o.Coord[base+texOff+1]
			// OBJ's V origin is bottom-left; the texture sampler treats V=0 as
			// the top row, so flip V to match.
			uvs[s] = [2]float32{u, 1 - v}
		}
	}

	// Map each index slot to the material of the group that owns it. Slots not
	// covered by any group keep "" (the default neutral material).
	idxMat := make([]string, len(o.Indices))
	for _, g := range o.Groups {
		end := g.IndexBegin + g.IndexCount
		if end > len(o.Indices) {
			end = len(o.Indices)
		}
		for i := g.IndexBegin; i < end && i >= 0; i++ {
			idxMat[i] = g.Usemtl
		}
	}

	nFace := len(o.Indices) / 3
	faces := make([][3]uint32, nFace)
	faceTexIdx := make([]int32, nFace)
	faceBaseColor := make([][4]uint8, nFace)
	faceAlpha := make([]float32, nFace)
	faceMeshIdx := make([]int32, nFace)
	hasNonOpaque := false
	hasUntextured := false

	for t := 0; t < nFace; t++ {
		faces[t] = [3]uint32{
			uint32(o.Indices[t*3]),
			uint32(o.Indices[t*3+1]),
			uint32(o.Indices[t*3+2]),
		}

		mat := lib.Lib[idxMat[t*3]] // nil if the material is absent
		bc := [4]uint8{200, 200, 200, 255}
		alpha := float32(1)
		if mat != nil {
			alpha = mat.D
			// gwob leaves D=0 when no `d` line is present; treat that as opaque.
			// Clamp out-of-range dissolve values too, so the alpha byte below
			// can't overflow uint8 and FaceAlpha keeps its [0,1] contract.
			if alpha <= 0 || alpha > 1 {
				alpha = 1
			}
			bc = [4]uint8{
				uint8(clampF32(mat.Kd[0], 0, 1)*255 + 0.5),
				uint8(clampF32(mat.Kd[1], 0, 1)*255 + 0.5),
				uint8(clampF32(mat.Kd[2], 0, 1)*255 + 0.5),
				uint8(alpha*255 + 0.5),
			}
		}

		texIdx := -1
		if ti, ok := matTexIdx[idxMat[t*3]]; ok {
			texIdx = ti
		}
		if texIdx >= 0 {
			faceTexIdx[t] = int32(texIdx)
		} else {
			faceTexIdx[t] = int32(nTex) // sentinel: use base color
			hasUntextured = true
		}

		faceBaseColor[t] = bc
		faceAlpha[t] = alpha
		faceMeshIdx[t] = 0
		if alpha < 1 {
			hasNonOpaque = true
		}
	}

	var noTextureMask []bool
	if hasUntextured {
		noTextureMask = make([]bool, nFace)
		for i, ti := range faceTexIdx {
			if ti >= int32(nTex) {
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
		NumMeshes:      1,
	}, nil
}

// cleanOBJRef normalizes a file reference taken from an OBJ/MTL directive: it
// trims whitespace and surrounding quotes and converts Windows-style backslash
// separators to forward slashes.
func cleanOBJRef(ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.Trim(ref, "\"")
	ref = strings.ReplaceAll(ref, "\\", "/")
	return ref
}

// pathBase returns the final path element of p, treating both '/' and '\' as
// separators (OBJ/MTL references and zip entries may use either).
func pathBase(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// countNgonFaces counts OBJ face lines ("f ...") with 5 or more vertices, which
// gwob cannot triangulate and silently drops. It iterates lines and tokens
// without allocating, since the OBJ buffer can be hundreds of MB.
func countNgonFaces(objBytes []byte) int {
	n := 0
	for len(objBytes) > 0 {
		line := objBytes
		if i := bytes.IndexByte(objBytes, '\n'); i >= 0 {
			line = objBytes[:i]
			objBytes = objBytes[i+1:]
		} else {
			objBytes = nil
		}
		line = bytes.TrimSpace(line)
		if len(line) < 2 || line[0] != 'f' || line[1] != ' ' {
			continue
		}
		// Count whitespace-separated tokens in the vertex list; stop at 5.
		tokens, inTok := 0, false
		for _, c := range line[2:] {
			if c == ' ' || c == '\t' {
				inTok = false
			} else if !inTok {
				inTok = true
				tokens++
				if tokens >= 5 {
					break
				}
			}
		}
		if tokens >= 5 {
			n++
		}
	}
	return n
}
