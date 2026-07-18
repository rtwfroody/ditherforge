// Package swatch synthesizes physical filament-calibration plates. For every
// pair {A,B} of a chosen filament palette it lays out a 90x10x2mm plate, printed
// standing vertically, whose face is a single row of nine 10x10mm sections
// mixing A and B at coverage p = 0, 1/8, ..., 8/8 (left to right). The mixture is
// cell-scale speckle (an ordered Bayer threshold on the block grid: blocks are
// one voxel cell wide and one print layer tall) with exact, known coverage, so a
// photograph of the printed plate yields the real physical color-mixing curve
// between the two filaments.
//
// To keep the pipeline fast (its cost scales with input triangles, and the block
// grid is very fine), the geometry emitted is only a coarse box per plate; the
// A/B pattern is baked into a per-plate TEXTURE the pipeline samples per cell.
// See BuildPlan, patternImage, and WriteOBJ.
package swatch

import (
	"bufio"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// patternUpsample is how many texture texels span one pattern block edge. It is
// >1 so a per-cell sample taken near a block's centre lands well inside that
// block's uniform texel neighbourhood, and the sampler's bilinear filtering
// never blends two different block colours together (which would misassign the
// cell to a third palette entry).
const patternUpsample = 4

// Physical dimensions of a plate, in millimeters. These are fixed by the
// calibration protocol and are not user-tunable. The face is a single row of
// 9 sections: 90mm wide (X), 10mm tall (Z), so the plate is short when standing
// and needs few print layers (fewer tool changes).
const (
	PlateWidthMM  = 90.0 // face width (X): Sections * SectionMM
	PlateHeightMM = 10.0 // face height (Z): one section tall
	ThickMM       = 2.0  // slab thickness (Y)
	PitchMM       = 12.0 // center-to-center spacing of adjacent plates along Y
	SectionMM     = 10.0 // side of each of the 9 square sections
	Sections      = 9    // sections per face (single row, left -> right)
)

// Filament is one palette entry: its sRGB hex ("#RRGGBB"), transmission
// distance (mm), and human-readable label.
type Filament struct {
	Hex   string
	TD    float32
	Label string
}

// Section describes one 10x10mm cell of a plate's 3x3 grid.
type Section struct {
	Index           int     // 0..8 in reading order (top-left -> bottom-right)
	NominalCoverage float64 // fraction of filament B, = Index/8
}

// Plate is a single A/B calibration plate.
type Plate struct {
	A         int     // palette index of filament A (rim + p=0 sections)
	B         int     // palette index of filament B (p=1 sections)
	YOffsetMM float64 // front face position along Y (back face at YOffsetMM+ThickMM)
}

// Plan is the full set of plates for a palette, plus the resolved block grid.
//
// Blocks are RECTANGULAR: their width is the voxel cell width (snapped so an
// integer count spans each 10mm section), and their height is one PRINT LAYER —
// the pattern rows are aligned to the pipeline's slab grid (first row spans the
// initial-layer height Layer0ZMM, every row above spans UpperZMM) so each
// pipeline cell samples exactly one row's color. This gives the pattern
// layer-height granularity in Z, so a p=1/8 section places isolated
// single-layer-tall B cells sandwiched between A cells.
type Plan struct {
	Palette      []Filament
	BlockWidthMM float64   // block width (= SectionMM/blocksPerSection), aligned to sections
	Nx           int       // blocks along X (face width), = Sections * blocksPerSection
	RowEdges     []float64 // Z boundaries of the Nrows pattern rows: len Nrows+1, [0]=0, [last]=PlateHeightMM
	Layer0ZMM    float64   // first-row (slab 0) height
	UpperZMM     float64   // height of every row above the first
	Plates       []Plate
}

// Nrows returns the number of pattern rows (slab-tall bands) in the plan.
func (p Plan) Nrows() int {
	if len(p.RowEdges) < 2 {
		return 0
	}
	return len(p.RowEdges) - 1
}

// Bayer8 is the standard 8x8 ordered-dither threshold matrix, holding the
// values 0..63 each exactly once. A block at grid index (i,j) is filament B
// iff Bayer8[i%8][j%8] < coverage*64, which places isolated B blocks at
// evenly spaced positions as coverage rises.
var Bayer8 = [8][8]int{
	{0, 32, 8, 40, 2, 34, 10, 42},
	{48, 16, 56, 24, 50, 18, 58, 26},
	{12, 44, 4, 36, 14, 46, 6, 38},
	{60, 28, 52, 20, 62, 30, 54, 22},
	{3, 35, 11, 43, 1, 33, 9, 41},
	{51, 19, 59, 27, 49, 17, 57, 25},
	{15, 47, 7, 39, 13, 45, 5, 37},
	{63, 31, 55, 23, 61, 29, 53, 21},
}

// Coverage returns the nominal fraction of filament B for section index s
// (0..8): 0, 1/8, ..., 1.
func Coverage(section int) float64 { return float64(section) / 8.0 }

// SectionIndex returns which of the 9 sections the X coordinate cx on the FRONT
// face falls in, left -> right (0..8). The face is viewed from outside (from
// -Y), where +X is to the viewer's right, so section 0 is the leftmost 10mm and
// section 8 the rightmost. cx outside [0,PlateWidthMM) is clamped into range.
func SectionIndex(cx float64) int {
	return clampInt(int(cx/SectionMM), 0, Sections-1)
}

// blockIsB reports whether the block at grid column i (X) and row j (Z, the
// slab-row index) whose center falls in the given section is filament B under
// the ordered Bayer threshold.
func blockIsB(i, j, section int) bool {
	p := Coverage(section)
	return float64(Bayer8[i%8][j%8]) < p*64.0
}

// slabRowEdges returns the Z boundaries partitioning [0, PlateHeightMM] into
// slab-tall rows: [0, layer0Z, layer0Z+upperZ, …], with the final boundary
// clamped to PlateHeightMM (so the top row may be shorter than upperZ). This
// mirrors cellslicer.SlabBoundaryPlanesFirst(0, PlateHeightMM, layer0Z, upperZ)
// up to the sub-µm per-plane nudges, which are negligible at ~0.2mm rows.
func slabRowEdges(layer0Z, upperZ float64) []float64 {
	if layer0Z <= 0 {
		layer0Z = upperZ
	}
	if upperZ <= 0 {
		upperZ = 0.2
	}
	const eps = 1e-6
	edges := []float64{0}
	z := layer0Z
	for z < PlateHeightMM-eps {
		edges = append(edges, z)
		z += upperZ
	}
	edges = append(edges, PlateHeightMM)
	return edges
}

// BuildPlan lays out one plate per unordered pair of distinct palette entries
// (all C(n,2) pairs, in ascending (a,b) order) and resolves the rectangular
// block grid. blockWidthMM is snapped so an integer number of blocks spans each
// 10mm section (block boundaries land on section boundaries); rows are one slab
// tall each, aligned to the pipeline's slab grid via (layer0ZMM, upperZMM).
func BuildPlan(palette []Filament, blockWidthMM, layer0ZMM, upperZMM float64) Plan {
	perSection := int(SectionMM/blockWidthMM + 0.5)
	if perSection < 1 {
		perSection = 1
	}
	plan := Plan{
		Palette:      palette,
		BlockWidthMM: SectionMM / float64(perSection),
		Nx:           Sections * perSection,
		RowEdges:     slabRowEdges(layer0ZMM, upperZMM),
		Layer0ZMM:    layer0ZMM,
		UpperZMM:     upperZMM,
	}
	idx := 0
	for a := 0; a < len(palette); a++ {
		for b := a + 1; b < len(palette); b++ {
			plan.Plates = append(plan.Plates, Plate{
				A:         a,
				B:         b,
				YOffsetMM: float64(idx) * PitchMM,
			})
			idx++
		}
	}
	return plan
}

// PlateSections returns the 9 sections (index + nominal coverage) shared by
// every plate.
func PlateSections() []Section {
	out := make([]Section, Sections)
	for s := 0; s < Sections; s++ {
		out[s] = Section{Index: s, NominalCoverage: Coverage(s)}
	}
	return out
}

// Tri references three vertices of a mesh's vertex slice (one triangle). The
// pattern color is carried by the texture, not per triangle, so Tri holds no
// material.
type Tri struct {
	A, B, C int
}

// blockColor returns the palette index painted on block (column i, row j) of the
// given plate: column i selects the section (hence coverage), and the Bayer
// threshold at (i, row j) decides A vs. B. This is the pattern's color field;
// it is baked into a texture (see patternImage) rather than into geometry, so
// the mesh stays trivial and the pipeline's cost no longer scales with the
// (very fine) block count.
func (plan Plan) blockColor(plate Plate, i, j int) int {
	cx := (float64(i) + 0.5) * plan.BlockWidthMM
	if blockIsB(i, j, SectionIndex(cx)) {
		return plate.B
	}
	return plate.A
}

// patternImage renders one plate's Nx×Nrows block pattern into an NRGBA image,
// upsampled by patternUpsample so each block is a K×K run of identical texels.
// Column c of the image is block column c/K (so texture U = x/PlateWidthMM lands
// section 0 at the left); the row axis is the pattern's Z rows — its up/down
// orientation is irrelevant because a section's B-fraction is invariant to
// vertical flip and both faces sample the same image.
func (plan Plan) patternImage(plate Plate) *image.NRGBA {
	k := patternUpsample
	nx, nz := plan.Nx, plan.Nrows()
	img := image.NewNRGBA(image.Rect(0, 0, nx*k, nz*k))
	for col := 0; col < nx; col++ {
		for row := 0; row < nz; row++ {
			r, g, b := hexToBytes(plan.Palette[plan.blockColor(plate, col, row)].Hex)
			c := color.NRGBA{R: r, G: g, B: b, A: 255}
			for dy := 0; dy < k; dy++ {
				for dx := 0; dx < k; dx++ {
					img.SetNRGBA(col*k+dx, row*k+dy, c)
				}
			}
		}
	}
	return img
}

// BuildMesh tessellates every plate into a coarse, watertight box in FINAL Z-up
// millimeter coordinates: X in [0,PlateWidthMM], Z in [0,PlateHeightMM], the two
// 90×10 faces at Y = YOffsetMM (front, facing -Y) and Y = YOffsetMM+ThickMM
// (back), closed by four rim quads. Each plate is a disjoint 8-vertex,
// 12-triangle box (shared vertices, every edge used by exactly two triangles).
//
// The A/B pattern is NOT in this geometry — it is carried by a per-plate texture
// (see patternImage / WriteOBJ), sampled per cell by the pipeline. Keeping the
// geometry trivial is what stops the load-time decimation and the clip stage
// from scaling with the (hundreds of thousands of) pattern blocks.
//
// Triangle winding is oriented outward per-triangle against the plate center
// (valid because each plate is a convex box).
func BuildMesh(plan Plan) (verts [][3]float64, tris []Tri) {
	W, H := PlateWidthMM, PlateHeightMM
	for _, plate := range plan.Plates {
		y0 := plate.YOffsetMM
		y1 := y0 + ThickMM
		center := [3]float64{W / 2, y0 + ThickMM/2, H / 2}
		base := len(verts)
		verts = append(verts,
			[3]float64{0, y0, 0}, [3]float64{W, y0, 0}, [3]float64{W, y0, H}, [3]float64{0, y0, H}, // front 0..3
			[3]float64{0, y1, 0}, [3]float64{W, y1, 0}, [3]float64{W, y1, H}, [3]float64{0, y1, H}, // back 4..7
		)
		quad := func(a, b, c, d int) {
			for _, t := range [2]Tri{{A: base + a, B: base + b, C: base + c}, {A: base + a, B: base + c, C: base + d}} {
				if !outwardOK(verts, t, center) {
					t.B, t.C = t.C, t.B
				}
				tris = append(tris, t)
			}
		}
		quad(0, 1, 2, 3) // front (-Y)
		quad(4, 5, 6, 7) // back  (+Y)
		quad(0, 1, 5, 4) // z=0
		quad(3, 2, 6, 7) // z=H
		quad(0, 3, 7, 4) // x=0
		quad(1, 2, 6, 5) // x=W
	}
	return verts, tris
}

// outwardOK reports whether triangle t's winding normal points away from the
// plate center c (i.e. outward for a convex box).
func outwardOK(verts [][3]float64, t Tri, c [3]float64) bool {
	return outwardTri(verts[t.A], verts[t.B], verts[t.C], c)
}

// WriteOBJ writes the plan as swatch.obj + swatch.mtl plus one pattern PNG per
// plate into dir, and returns the OBJ path. Geometry is the coarse per-plate box
// (see BuildMesh) emitted in the loader's Y-up convention (final X,Y,Z -> OBJ
// X,Z,-Y). Every plate's front and back faces are UV-mapped onto that plate's
// pattern texture (map_Kd); the rim faces sample a fixed section-0 (color-A)
// texel of the same texture, so no face lacks texture coordinates (the OBJ
// loader rejects meshes that mix textured and untextured faces).
func WriteOBJ(plan Plan, dir string) (objPath string, err error) {
	objPath = filepath.Join(dir, "swatch.obj")
	mtlPath := filepath.Join(dir, "swatch.mtl")

	// One texture + material per plate.
	mtl, err := os.Create(mtlPath)
	if err != nil {
		return "", fmt.Errorf("creating swatch MTL: %w", err)
	}
	mw := bufio.NewWriter(mtl)
	fmt.Fprintf(mw, "# DitherForge swatch materials\n")
	for p, plate := range plan.Plates {
		pngName := fmt.Sprintf("swatch_%d.png", p)
		if err := writePNG(filepath.Join(dir, pngName), plan.patternImage(plate)); err != nil {
			mtl.Close()
			return "", err
		}
		fmt.Fprintf(mw, "newmtl %s\n", plateMatName(p))
		fmt.Fprintf(mw, "Kd 1 1 1\n")
		fmt.Fprintf(mw, "map_Kd %s\n", pngName)
	}
	if err := mw.Flush(); err != nil {
		mtl.Close()
		return "", fmt.Errorf("writing swatch MTL: %w", err)
	}
	if err := mtl.Close(); err != nil {
		return "", err
	}

	verts, _ := BuildMesh(plan)

	f, err := os.Create(objPath)
	if err != nil {
		return "", fmt.Errorf("creating swatch OBJ: %w", err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	fmt.Fprintf(w, "# DitherForge swatch plates\n")
	fmt.Fprintf(w, "mtllib swatch.mtl\n")
	fmt.Fprintf(w, "o swatch\n")

	// Positions: final (X,Y,Z) -> OBJ (X, Z, -Y).
	for _, v := range verts {
		fmt.Fprintf(w, "v %s %s %s\n", ftoa(v[0]), ftoa(v[2]), ftoa(-v[1]))
	}

	// Per-plate texture coordinates: the four face corners (u = x/width,
	// v = z/height; section 0 sits at u=0) plus one rim texel deep inside
	// section 0 (color A). 5 vt per plate.
	uRim := 0.5 / float64(plan.Nx*patternUpsample) // centre of texel column 0
	for range plan.Plates {
		fmt.Fprintf(w, "vt 0 0\nvt 1 0\nvt 1 1\nvt 0 1\n")
		fmt.Fprintf(w, "vt %s 0.5\n", ftoa(uRim))
	}

	center := func(base int) [3]float64 {
		return [3]float64{PlateWidthMM / 2, verts[base][1] + ThickMM/2, PlateHeightMM / 2}
	}
	// Emit a quad (position + vt indices, 0-based within the plate) as two
	// outward-oriented triangles.
	for p := range plan.Plates {
		vBase := p*8 + 1 // 1-based OBJ vertex index of this plate's corner 0
		tBase := p*5 + 1 // 1-based vt index of this plate's corner 0
		c := center(p * 8)
		fmt.Fprintf(w, "usemtl %s\n", plateMatName(p))
		emitQuad := func(pos, uv [4]int) {
			order := [4]int{0, 1, 2, 3}
			a, b, d := verts[p*8+pos[0]], verts[p*8+pos[1]], verts[p*8+pos[2]]
			if !outwardTri(a, b, d, c) {
				order = [4]int{0, 3, 2, 1}
			}
			pv := [4]int{pos[order[0]], pos[order[1]], pos[order[2]], pos[order[3]]}
			uvv := [4]int{uv[order[0]], uv[order[1]], uv[order[2]], uv[order[3]]}
			tri := func(i, j, k int) {
				fmt.Fprintf(w, "f %d/%d %d/%d %d/%d\n",
					vBase+pv[i], tBase+uvv[i], vBase+pv[j], tBase+uvv[j], vBase+pv[k], tBase+uvv[k])
			}
			tri(0, 1, 2)
			tri(0, 2, 3)
		}
		rim := [4]int{4, 4, 4, 4}                        // all rim corners -> the section-0 rim texel (vt index 4)
		emitQuad([4]int{0, 1, 2, 3}, [4]int{0, 1, 2, 3}) // front (-Y)
		emitQuad([4]int{4, 5, 6, 7}, [4]int{0, 1, 2, 3}) // back  (+Y)
		emitQuad([4]int{0, 1, 5, 4}, rim)                // z=0
		emitQuad([4]int{3, 2, 6, 7}, rim)                // z=H
		emitQuad([4]int{0, 3, 7, 4}, rim)                // x=0
		emitQuad([4]int{1, 2, 6, 5}, rim)                // x=W
	}

	if err := w.Flush(); err != nil {
		return "", fmt.Errorf("writing swatch OBJ: %w", err)
	}
	return objPath, nil
}

// outwardTri reports whether triangle (a,b,d) winds outward relative to center c.
func outwardTri(a, b, d, c [3]float64) bool {
	e1 := [3]float64{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
	e2 := [3]float64{d[0] - a[0], d[1] - a[1], d[2] - a[2]}
	nx := e1[1]*e2[2] - e1[2]*e2[1]
	ny := e1[2]*e2[0] - e1[0]*e2[2]
	nz := e1[0]*e2[1] - e1[1]*e2[0]
	ox := (a[0]+b[0]+d[0])/3 - c[0]
	oy := (a[1]+b[1]+d[1])/3 - c[1]
	oz := (a[2]+b[2]+d[2])/3 - c[2]
	return nx*ox+ny*oy+nz*oz > 0
}

// writePNG encodes img to path.
func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating swatch texture: %w", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		return fmt.Errorf("encoding swatch texture: %w", err)
	}
	return nil
}

func plateMatName(p int) string { return "pattern" + strconv.Itoa(p) }

// hexToBytes parses "#RRGGBB" to sRGB bytes; malformed input yields mid gray.
func hexToBytes(hex string) (r, g, b uint8) {
	rf, gf, bf := hexToUnit(hex)
	return uint8(rf*255 + 0.5), uint8(gf*255 + 0.5), uint8(bf*255 + 0.5)
}

// hexToUnit parses "#RRGGBB" to unit-range floats. Malformed input yields mid
// gray so a bad hex is visible rather than silently black.
func hexToUnit(hex string) (r, g, b float64) {
	hex = strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(hex) != 6 {
		return 0.5, 0.5, 0.5
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return 0.5, 0.5, 0.5
	}
	return float64((v>>16)&0xFF) / 255.0,
		float64((v>>8)&0xFF) / 255.0,
		float64(v&0xFF) / 255.0
}

// ftoa formats a float compactly (no exponent, trimmed) for OBJ/MTL output.
func ftoa(x float64) string {
	return strconv.FormatFloat(x, 'f', -1, 64)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
