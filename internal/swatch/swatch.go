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
	"math"
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

// patternImage renders one plate's block pattern into an NRGBA image, upsampled
// by patternUpsample. Column c is block column c/K, so texture U = x/PlateWidthMM
// puts section 0 at the left.
//
// The vertical axis is mapped to MATCH the pipeline's non-uniform slab rows: the
// texture V axis is linear in z (v = z/PlateHeightMM), but the physical rows are
// not uniform (the first slab is the taller initial-layer height). So each texel
// row is colored by the PHYSICAL row that contains its z, giving each row a texel
// height proportional to its real height. Without this, a uniform texel-per-row
// layout misaligns the taller first row from the linear UV and cells sample the
// wrong row / bilinear-blend two rows into a third color (the White+Orange->Beige
// contamination) — badly at 0.08mm where the first row is 2.5x the others. Row
// up/down orientation is otherwise irrelevant (a section's B-fraction is flip-
// invariant and both faces share the image).
func (plan Plan) patternImage(plate Plate) *image.NRGBA {
	k := patternUpsample
	nx := plan.Nx
	// Texel height fine enough that the thinnest slab row still spans ~K texels.
	texelH := plan.UpperZMM / float64(k)
	if texelH <= 0 {
		texelH = PlateHeightMM / float64(plan.Nrows()*k)
	}
	htex := int(math.Ceil(PlateHeightMM / texelH))
	if htex < 1 {
		htex = 1
	}
	img := image.NewNRGBA(image.Rect(0, 0, nx*k, htex))
	row := 0
	for ty := 0; ty < htex; ty++ {
		z := (float64(ty) + 0.5) / float64(htex) * PlateHeightMM
		for row+1 < plan.Nrows() && z >= plan.RowEdges[row+1] {
			row++
		}
		for col := 0; col < nx; col++ {
			r, g, b := hexToBytes(plan.Palette[plan.blockColor(plate, col, row)].Hex)
			c := color.NRGBA{R: r, G: g, B: b, A: 255}
			for dx := 0; dx < k; dx++ {
				img.SetNRGBA(col*k+dx, ty, c)
			}
		}
	}
	return img
}

// ChamferMM is the TARGET 45° chamfer width taken off every edge of a plate (all
// 12 box edges). It softens sharp corners for handling and mitigates
// elephant-foot on the 2mm-thick bottom edge. Each of an edge's two adjacent
// faces recedes this far from the edge, and the removed wedge becomes a 45° flat
// band. The realized chamfer (plateChamferMM) snaps this to a whole number of
// pattern blocks — see plateChamferMM for why.
const ChamferMM = 0.5

// plateChamferMM returns the realized chamfer width, ChamferMM capped so the two
// receding faces never cross (the binding dimension is the ThickMM thickness, so
// the cap keeps a positive side-wall width). blockWidthMM is unused today but
// kept in the signature so the chamfer can be re-derived from cell geometry if
// the cap ever needs to grow smarter.
func plateChamferMM(blockWidthMM float64) float64 {
	maxC := 0.45 * ThickMM
	return math.Min(ChamferMM, maxC)
}

// chamferVertIdx returns the local index (0..23) of a chamfered-plate vertex.
// Each of i,j,k is 0/1 selecting the low/high end of X,Y,Z (a box corner); pin
// (0=X, 1=Y, 2=Z) selects which of the three faces meeting at that corner this
// vertex lies on. Truncating each of the 8 corners into 3 vertices (one per
// incident face) is the standard chamfered-box topology.
func chamferVertIdx(i, j, k, pin int) int { return ((i*2+j)*2+k)*3 + pin }

// plateCenter returns the centroid of the plate whose front face is at y0.
func plateCenter(y0 float64) [3]float64 {
	return [3]float64{PlateWidthMM / 2, y0 + ThickMM/2, PlateHeightMM / 2}
}

// plateVertices returns the 24 vertices of one chamfered plate in FINAL Z-up mm
// coordinates, with the front face at Y=y0 (thickness ThickMM in +Y) and chamfer
// width c. The chamfer is applied on the Z (top/bottom) and Y (thickness) axes
// only; the X axis is deliberately NOT inset (the textured faces keep their full
// [0,PlateWidthMM] X extent).
//
// Why X is left un-chamfered: the pipeline seeds the front/back faces' surface
// cells by walking each face's in-plane boundary at cell-size spacing. Insetting
// the textured face in X moves that boundary and shifts every front-face cell
// column off the fixed, position-mapped pattern-block grid; the cells then land
// on block-column boundaries and the sampler bilinear-blends two adjacent blocks
// into a third palette color (the Black+Orange -> Beige contamination). No X
// inset value re-aligns them (cell size, block width, and multiples were all
// measured to fail), so the four vertical side edges stay square. The Z inset is
// harmless (the pattern's row mapping is v = z/height, unaffected by trimming the
// top/bottom), so the elephant-foot-prone bottom edge and the top edge — the two
// long horizontal edges of each face — do get a true 45° chamfer.
func plateVertices(y0, c float64) [][3]float64 {
	cx, cy, cz := 0.0, c, c
	xEnd := [2]float64{0, PlateWidthMM}
	yEnd := [2]float64{y0, y0 + ThickMM}
	zEnd := [2]float64{0, PlateHeightMM}
	xIn := [2]float64{cx, PlateWidthMM - cx}
	yIn := [2]float64{y0 + cy, y0 + ThickMM - cy}
	zIn := [2]float64{cz, PlateHeightMM - cz}
	verts := make([][3]float64, 24)
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			for k := 0; k < 2; k++ {
				verts[chamferVertIdx(i, j, k, 0)] = [3]float64{xEnd[i], yIn[j], zIn[k]} // X-face
				verts[chamferVertIdx(i, j, k, 1)] = [3]float64{xIn[i], yEnd[j], zIn[k]} // Y-face
				verts[chamferVertIdx(i, j, k, 2)] = [3]float64{xIn[i], yIn[j], zEnd[k]} // Z-face
			}
		}
	}
	return verts
}

// plateFace is one polygon (3 or 4 local vertex indices) of a chamfered plate.
// textured marks the two big front/back faces that carry the A/B pattern
// texture; every other face (the four thin side rims, the twelve chamfer bands,
// the eight corner triangles) is solid color A.
type plateFace struct {
	idx      []int
	textured bool
}

// plateFaceTopology returns the constant 26-face topology of a chamfered plate:
// 6 inset rectangular faces, 12 chamfer bands, and 8 corner triangles. Vertex
// indices are local (0..23); winding is fixed up per plate by orientedFace. The
// front (Y=y0) and back (Y=y1) inset faces are textured; all others are rim.
func plateFaceTopology() []plateFace {
	v := chamferVertIdx
	faces := make([]plateFace, 0, 26)

	// 6 inset faces (each original box face, shrunk by the chamfer on all sides).
	// Front (j=0) and back (j=1): the four Y-pinned corner vertices; textured.
	for j := 0; j < 2; j++ {
		faces = append(faces, plateFace{
			idx:      []int{v(0, j, 0, 1), v(1, j, 0, 1), v(1, j, 1, 1), v(0, j, 1, 1)},
			textured: true,
		})
	}
	// Left/right walls (fixed i): the X-pinned vertices.
	for i := 0; i < 2; i++ {
		faces = append(faces, plateFace{idx: []int{v(i, 0, 0, 0), v(i, 1, 0, 0), v(i, 1, 1, 0), v(i, 0, 1, 0)}})
	}
	// Bottom/top walls (fixed k): the Z-pinned vertices.
	for k := 0; k < 2; k++ {
		faces = append(faces, plateFace{idx: []int{v(0, 0, k, 2), v(1, 0, k, 2), v(1, 1, k, 2), v(0, 1, k, 2)}})
	}

	// 12 chamfer bands, one per box edge, each joining the two faces' inset
	// vertices along that edge.
	// Edges along X (fixed j,k): join the Y-face and Z-face vertices.
	for j := 0; j < 2; j++ {
		for k := 0; k < 2; k++ {
			faces = append(faces, plateFace{idx: []int{v(0, j, k, 1), v(1, j, k, 1), v(1, j, k, 2), v(0, j, k, 2)}})
		}
	}
	// Edges along Y (fixed i,k): join the X-face and Z-face vertices.
	for i := 0; i < 2; i++ {
		for k := 0; k < 2; k++ {
			faces = append(faces, plateFace{idx: []int{v(i, 0, k, 0), v(i, 1, k, 0), v(i, 1, k, 2), v(i, 0, k, 2)}})
		}
	}
	// Edges along Z (fixed i,j): join the X-face and Y-face vertices.
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			faces = append(faces, plateFace{idx: []int{v(i, j, 0, 0), v(i, j, 1, 0), v(i, j, 1, 1), v(i, j, 0, 1)}})
		}
	}

	// 8 corner triangles: the three inset vertices at each box corner.
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			for k := 0; k < 2; k++ {
				faces = append(faces, plateFace{idx: []int{v(i, j, k, 0), v(i, j, k, 1), v(i, j, k, 2)}})
			}
		}
	}
	return faces
}

// orientedFace reorders a face's local vertex indices so a triangle fan from
// order[0] winds outward (its normal points away from center). A chamfered box
// is convex, so per-face outward orientation yields a consistently-wound,
// watertight mesh (shared edges end up anti-parallel between neighbours).
func orientedFace(verts [][3]float64, idx []int, center [3]float64) []int {
	order := append([]int(nil), idx...)
	if !outwardTri(verts[idx[0]], verts[idx[1]], verts[idx[2]], center) {
		for a, b := 0, len(order)-1; a < b; a, b = a+1, b-1 {
			order[a], order[b] = order[b], order[a]
		}
	}
	return order
}

// BuildMesh tessellates every plate into a watertight, 2-manifold chamfered box
// in FINAL Z-up millimeter coordinates: X in [0,PlateWidthMM], Z in
// [0,PlateHeightMM], the two 90×10 faces near Y = YOffsetMM (front, facing -Y)
// and Y = YOffsetMM+ThickMM (back). Each plate is a disjoint 24-vertex,
// 44-triangle chamfered-box solid (6 inset faces + 12 edge bands + 8 corner
// triangles; see plateFaceTopology). The chamfer is applied on the Z and Y axes
// only (see plateVertices for why X is left un-inset), so the four long
// horizontal edges — front/back top and bottom, including the elephant-foot-prone
// bottom edge — become true ChamferMM-wide 45° bands, while the eight edges
// touching X=0/PlateWidthMM stay square (their "bands" collapse to flat, coplanar
// wall strips that keep the mesh watertight).
//
// The A/B pattern is NOT in this geometry — it is carried by a per-plate texture
// (see patternImage / WriteOBJ), sampled per cell by the pipeline. Keeping the
// geometry trivial is what stops the load-time decimation and the clip stage
// from scaling with the (hundreds of thousands of) pattern blocks.
//
// Triangle winding is oriented outward per-face against the plate center (valid
// because each chamfered plate is convex).
func BuildMesh(plan Plan) (verts [][3]float64, tris []Tri) {
	topo := plateFaceTopology()
	c := plateChamferMM(plan.BlockWidthMM)
	for _, plate := range plan.Plates {
		base := len(verts)
		pv := plateVertices(plate.YOffsetMM, c)
		verts = append(verts, pv...)
		center := plateCenter(plate.YOffsetMM)
		for _, f := range topo {
			order := orientedFace(pv, f.idx, center)
			for m := 1; m+1 < len(order); m++ {
				tris = append(tris, Tri{A: base + order[0], B: base + order[m], C: base + order[m+1]})
			}
		}
	}
	return verts, tris
}

// WriteOBJ writes the plan as swatch.obj + swatch.mtl plus one pattern PNG per
// plate into dir, and returns the OBJ path. Geometry is the per-plate chamfered
// box (see BuildMesh) emitted in the loader's Y-up convention (final X,Y,Z ->
// OBJ X,Z,-Y). Every plate's front and back inset faces are UV-mapped onto that
// plate's pattern texture (map_Kd) with a position-based mapping (u=x/W, v=z/H);
// the rim, chamfer-band, and corner faces sample a fixed section-0 (color-A)
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

	// Per-plate texture coordinates: one position-based UV (u = x/width,
	// v = z/height; section 0 at u=0) for each of the 24 vertices — used by the
	// textured front/back faces — plus one rim texel deep inside section 0
	// (color A) used by every rim/chamfer/corner face. 25 vt per plate.
	c := plateChamferMM(plan.BlockWidthMM)
	uRim := 0.5 / float64(plan.Nx*patternUpsample) // centre of texel column 0
	texVerts := plateVertices(0, c)                // x,z are y-independent
	for range plan.Plates {
		for _, vtx := range texVerts {
			fmt.Fprintf(w, "vt %s %s\n", ftoa(vtx[0]/PlateWidthMM), ftoa(vtx[2]/PlateHeightMM))
		}
		fmt.Fprintf(w, "vt %s 0.5\n", ftoa(uRim))
	}

	topo := plateFaceTopology()
	for p := range plan.Plates {
		vBase := p * 24    // 0-based OBJ vertex offset of this plate
		uvBase := p * 25   // 0-based vt offset of this plate
		rimVt := uvBase + 25 // 1-based rim vt (color A) of this plate
		center := plateCenter(plan.Plates[p].YOffsetMM)
		pv := plateVertices(plan.Plates[p].YOffsetMM, c)
		fmt.Fprintf(w, "usemtl %s\n", plateMatName(p))
		for _, face := range topo {
			order := orientedFace(pv, face.idx, center)
			vt := func(local int) int {
				if face.textured {
					return uvBase + local + 1 // this vertex's position-based UV
				}
				return rimVt
			}
			// Fan-triangulate the outward-oriented polygon.
			for m := 1; m+1 < len(order); m++ {
				a, b, c := order[0], order[m], order[m+1]
				fmt.Fprintf(w, "f %d/%d %d/%d %d/%d\n",
					vBase+a+1, vt(a), vBase+b+1, vt(b), vBase+c+1, vt(c))
			}
		}
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
