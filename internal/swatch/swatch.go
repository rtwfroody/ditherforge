// Package swatch synthesizes physical filament-calibration plates. For every
// pair {A,B} of a chosen filament palette it lays out a 30x30x2mm plate,
// printed standing vertically, whose face is a 3x3 grid of 10x10mm sections
// mixing A and B at coverage p = 0, 1/8, ..., 8/8 (reading order). The mixture
// is cell-scale speckle (an ordered Bayer threshold on the block grid) with
// exact, known coverage, so a photograph of the printed plate yields the real
// physical color-mixing curve between the two filaments.
//
// The geometry is emitted as an OBJ+MTL pair (per-material Kd = filament sRGB),
// which the loader's per-material FaceBaseColor path turns into sampleable
// color for the dithering pipeline. See BuildPlan and WriteOBJ.
package swatch

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Physical dimensions of a plate, in millimeters. These are fixed by the
// calibration protocol and are not user-tunable.
const (
	PlateMM   = 30.0 // face width (X) and height (Z)
	ThickMM   = 2.0  // slab thickness (Y)
	PitchMM   = 12.0 // center-to-center spacing of adjacent plates along Y
	SectionMM = 10.0 // side of each of the 9 sections (3x3 grid over the 30mm face)
	Sections  = 9    // sections per face
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
type Plan struct {
	Palette []Filament
	BlockMM float64 // realized block size (PlateMM/N), snapped to an integer block count
	N       int     // blocks per 30mm axis
	Plates  []Plate
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

// SectionIndex returns which of the 9 sections the point (cx, cz) on the FRONT
// face falls in, in reading order (top-left = 0). The face is viewed from
// outside (from -Y): +X is to the viewer's right, +Z is up, so the top row is
// the highest Z. Coordinates outside [0,PlateMM) are clamped into the grid.
func SectionIndex(cx, cz float64) int {
	col := clampInt(int(cx/SectionMM), 0, 2)
	rowFromBottom := clampInt(int(cz/SectionMM), 0, 2)
	row := 2 - rowFromBottom // top row (highest Z) is reading-order row 0
	return row*3 + col
}

// blockIsB reports whether the block at grid index (i,j) whose center falls in
// the given section is filament B under the ordered Bayer threshold.
func blockIsB(i, j, section int) bool {
	p := Coverage(section)
	return float64(Bayer8[i%8][j%8]) < p*64.0
}

// BuildPlan lays out one plate per unordered pair of distinct palette entries
// (all C(n,2) pairs, in ascending (a,b) order), and resolves the block grid so
// an integer number of blocks spans the 30mm face at ~blockMM each.
func BuildPlan(palette []Filament, blockMM float64) Plan {
	n := int(PlateMM/blockMM + 0.5)
	if n < 1 {
		n = 1
	}
	plan := Plan{
		Palette: palette,
		BlockMM: PlateMM / float64(n),
		N:       n,
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

// Tri is a triangle referencing three vertices in a mesh's vertex slice, tagged
// with the palette index of its filament.
type Tri struct {
	A, B, C int
	Mat     int // palette index
}

// BuildMesh tessellates every plate into a single watertight triangle mesh in
// FINAL Z-up millimeter coordinates: X in [0,30], Z in [0,30], the slab's two
// 30x30 faces at Y = YOffsetMM (front, facing -Y) and Y = YOffsetMM+2 (back).
// Plates are disjoint closed box components (they do not share vertices). Each
// front/back block and each rim triangle carries its filament's palette index.
//
// Triangle winding is oriented outward per-triangle against the plate center
// (valid because each plate is a convex box), so callers get consistent
// outward normals without depending on emission order.
func BuildMesh(plan Plan) (verts [][3]float64, tris []Tri) {
	n := plan.N
	block := plan.BlockMM
	for _, plate := range plan.Plates {
		y0 := plate.YOffsetMM
		y1 := y0 + ThickMM
		center := [3]float64{PlateMM / 2, y0 + ThickMM/2, PlateMM / 2}

		// Two (n+1)x(n+1) vertex grids, front (Y=y0) and back (Y=y1), indexed
		// [i][j] with i along X, j along Z. Shared by the block faces and the
		// rim strips so every boundary edge is used by exactly two triangles.
		front := make([][]int, n+1)
		back := make([][]int, n+1)
		for i := 0; i <= n; i++ {
			front[i] = make([]int, n+1)
			back[i] = make([]int, n+1)
			x := float64(i) * block
			for j := 0; j <= n; j++ {
				z := float64(j) * block
				front[i][j] = len(verts)
				verts = append(verts, [3]float64{x, y0, z})
				back[i][j] = len(verts)
				verts = append(verts, [3]float64{x, y1, z})
			}
		}

		addTri := func(a, b, c, mat int) {
			t := Tri{A: a, B: b, C: c, Mat: mat}
			if !outwardOK(verts, t, center) {
				t.B, t.C = t.C, t.B
			}
			tris = append(tris, t)
		}
		addQuad := func(a, b, c, d, mat int) {
			addTri(a, b, c, mat)
			addTri(a, c, d, mat)
		}

		// Block faces (front + back share each block's material).
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				cx := (float64(i) + 0.5) * block
				cz := (float64(j) + 0.5) * block
				s := SectionIndex(cx, cz)
				mat := plate.A
				if blockIsB(i, j, s) {
					mat = plate.B
				}
				addQuad(front[i][j], front[i+1][j], front[i+1][j+1], front[i][j+1], mat)
				addQuad(back[i][j], back[i+1][j], back[i+1][j+1], back[i][j+1], mat)
			}
		}

		// Rim strips: solid filament A, subdivided to match the grid boundary
		// vertices so front/back boundary edges are shared, not duplicated.
		rimMat := plate.A
		for k := 0; k < n; k++ {
			// z=0 edge (j=0) and z=max edge (j=n).
			addQuad(front[k][0], front[k+1][0], back[k+1][0], back[k][0], rimMat)
			addQuad(front[k][n], front[k+1][n], back[k+1][n], back[k][n], rimMat)
			// x=0 edge (i=0) and x=max edge (i=n).
			addQuad(front[0][k], front[0][k+1], back[0][k+1], back[0][k], rimMat)
			addQuad(front[n][k], front[n][k+1], back[n][k+1], back[n][k], rimMat)
		}
	}
	return verts, tris
}

// outwardOK reports whether triangle t's winding normal points away from the
// plate center c (i.e. outward for a convex box).
func outwardOK(verts [][3]float64, t Tri, c [3]float64) bool {
	a := verts[t.A]
	b := verts[t.B]
	d := verts[t.C]
	e1 := [3]float64{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
	e2 := [3]float64{d[0] - a[0], d[1] - a[1], d[2] - a[2]}
	nx := e1[1]*e2[2] - e1[2]*e2[1]
	ny := e1[2]*e2[0] - e1[0]*e2[2]
	nz := e1[0]*e2[1] - e1[1]*e2[0]
	// Vector from center to the triangle centroid.
	ox := (a[0]+b[0]+d[0])/3 - c[0]
	oy := (a[1]+b[1]+d[1])/3 - c[1]
	oz := (a[2]+b[2]+d[2])/3 - c[2]
	return nx*ox+ny*oy+nz*oz > 0
}

// WriteOBJ writes the plan as swatch.obj + swatch.mtl into dir and returns the
// OBJ path. The mesh is emitted in the loader's Y-up convention (final X,Y,Z ->
// OBJ X,Z,-Y) so it converts back to the intended Z-up layout on load. Faces
// are grouped by material to keep the file compact; each material's Kd is the
// filament's exact sRGB triple.
func WriteOBJ(plan Plan, dir string) (objPath string, err error) {
	verts, tris := BuildMesh(plan)

	objPath = filepath.Join(dir, "swatch.obj")
	mtlPath := filepath.Join(dir, "swatch.mtl")

	if err := writeMTL(mtlPath, plan.Palette); err != nil {
		return "", err
	}

	f, err := os.Create(objPath)
	if err != nil {
		return "", fmt.Errorf("creating swatch OBJ: %w", err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)

	fmt.Fprintf(w, "# DitherForge swatch plates\n")
	fmt.Fprintf(w, "mtllib swatch.mtl\n")
	fmt.Fprintf(w, "o swatch\n")

	// Vertices: final (X,Y,Z) -> OBJ (X, Z, -Y).
	for _, v := range verts {
		fmt.Fprintf(w, "v %s %s %s\n",
			ftoa(v[0]), ftoa(v[2]), ftoa(-v[1]))
	}

	// Group faces by material (palette index) so the file has one usemtl
	// switch per filament rather than one per triangle.
	byMat := make(map[int][]Tri)
	order := make([]int, 0)
	for _, t := range tris {
		if _, ok := byMat[t.Mat]; !ok {
			order = append(order, t.Mat)
		}
		byMat[t.Mat] = append(byMat[t.Mat], t)
	}
	for _, mat := range order {
		fmt.Fprintf(w, "usemtl %s\n", matName(mat))
		for _, t := range byMat[mat] {
			// OBJ faces are 1-indexed.
			fmt.Fprintf(w, "f %d %d %d\n", t.A+1, t.B+1, t.C+1)
		}
	}

	if err := w.Flush(); err != nil {
		return "", fmt.Errorf("writing swatch OBJ: %w", err)
	}
	return objPath, nil
}

// writeMTL writes one material per palette entry, Kd = filament sRGB / 255.
func writeMTL(path string, palette []Filament) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating swatch MTL: %w", err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	fmt.Fprintf(w, "# DitherForge swatch materials\n")
	for i, fil := range palette {
		r, g, b := hexToUnit(fil.Hex)
		fmt.Fprintf(w, "newmtl %s\n", matName(i))
		fmt.Fprintf(w, "Kd %s %s %s\n", ftoa(r), ftoa(g), ftoa(b))
		fmt.Fprintf(w, "d 1.0\n")
	}
	return w.Flush()
}

func matName(i int) string { return "mat" + strconv.Itoa(i) }

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
