// Package fwnrepair rebuilds a triangle mesh as the 0.5 iso-surface of
// its generalized winding number field (Barill et al. 2018, "Fast
// Winding Numbers for Soups and Clouds"), sampled on a uniform grid and
// contoured with marching cubes. Output is watertight; input may be
// non-manifold, self-intersecting, open, or unoriented soup.
//
// It is an alternative to the CGAL alpha wrap (internal/alphawrap) for
// producing the ε-valid 2-manifold meshes that the Manifold-library
// booleans (internal/manifoldbool) require.
package fwnrepair

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"sync"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// iso is the winding-number level set that defines the surface. Inside
// the solid |w| ≈ 1, outside ≈ 0, so 0.5 sits on the boundary.
const iso = 0.5

// maxDim caps the grid resolution on every axis. 384 samples per axis is
// the working ceiling; if a requested pitch would exceed it that axis's
// pitch is raised (see Repair's return of the effective pitches). X and Y
// share the XY pitch, so they are capped jointly; Z is capped on its own.
const maxDim = 384

// gridPad is the number of empty cells added on every side of the mesh
// bbox so the isosurface always closes inside the grid.
const gridPad = 2

// Repair returns a geometry-only LoadedModel whose surface is the 0.5
// iso-surface of the input mesh's generalized winding number field. The
// grid is anisotropic: pitchXY is the cell size along X and Y, pitchZ
// along Z (both in model units — mm after pipeline scaling), letting a
// caller resolve XY with the nozzle diameter and Z with the layer
// height. Only Vertices and Faces are populated on the result.
//
// The second and third return values are the *effective* XY and Z
// pitches actually used: if a requested pitch would have produced a grid
// larger than maxDim on its axis it is raised and the coarser value
// reported here (so a caller/log line can note the downscale). X and Y
// share pitchXY, so they are capped jointly (whichever of the two grids
// is larger drives the raise); Z is capped independently. Each returned
// pitch equals the requested one when its axis was not capped.
//
// The context is checked once per Z-slice; a cancelled context makes
// Repair return promptly with ctx.Err().
func Repair(ctx context.Context, model *loader.LoadedModel, pitchXY, pitchZ float32) (*loader.LoadedModel, float32, float32, error) {
	if pitchXY <= 0 {
		return nil, 0, 0, fmt.Errorf("fwnrepair: pitchXY must be positive (got %g)", pitchXY)
	}
	if pitchZ <= 0 {
		return nil, 0, 0, fmt.Errorf("fwnrepair: pitchZ must be positive (got %g)", pitchZ)
	}
	if model == nil || len(model.Faces) == 0 {
		return nil, 0, 0, fmt.Errorf("fwnrepair: input mesh has no faces")
	}

	tris := buildTris(model)
	tree := buildBVH(tris)

	g := newGrid(model.Vertices, float64(pitchXY), float64(pitchZ))
	verts, faces, err := contour(ctx, tree, g)
	if err != nil {
		return nil, 0, 0, err
	}

	orientOutward(verts, faces)

	return &loader.LoadedModel{
		Vertices: verts,
		Faces:    faces,
	}, float32(g.dx), float32(g.dz), nil
}

// buildTris widens the model geometry to float64 and precomputes the
// per-triangle winding-number quantities.
func buildTris(model *loader.LoadedModel) []tri {
	tris := make([]tri, 0, len(model.Faces))
	for _, f := range model.Faces {
		a := model.Vertices[f[0]]
		b := model.Vertices[f[1]]
		c := model.Vertices[f[2]]
		tris = append(tris, newTri(
			vec3{float64(a[0]), float64(a[1]), float64(a[2])},
			vec3{float64(b[0]), float64(b[1]), float64(b[2])},
			vec3{float64(c[0]), float64(c[1]), float64(c[2])},
		))
	}
	return tris
}

// grid is the sampling lattice: nx×ny×nz sample points at per-axis
// spacing (dx,dy,dz) starting from origin. dx and dy are the XY pitch,
// dz the Z pitch. Cell (i,j,k) spans samples [i,i+1]×… so there are
// (nx-1)×(ny-1)×(nz-1) rectangular-box marching-cubes cells.
type grid struct {
	origin     vec3
	dx, dy, dz float64
	nx, ny, nz int
}

// newGrid builds a grid covering the vertex bbox padded by gridPad cells
// on every side, capping each dimension at maxDim by raising the
// relevant pitch if necessary. X and Y share pitchXY and are capped
// jointly (whichever axis first exceeds maxDim raises the shared pitch);
// Z uses pitchZ and is capped on its own.
func newGrid(verts [][3]float32, pitchXY, pitchZ float64) grid {
	var lo, hi vec3
	for d := 0; d < 3; d++ {
		lo[d] = math.Inf(1)
		hi[d] = math.Inf(-1)
	}
	for _, v := range verts {
		for d := 0; d < 3; d++ {
			x := float64(v[d])
			lo[d] = math.Min(lo[d], x)
			hi[d] = math.Max(hi[d], x)
		}
	}

	// samples returns the sample count along axis a at pitch p.
	samples := func(a int, p float64) int {
		ext := hi[a] - lo[a]
		cells := int(math.Ceil(ext/p)) + 2*gridPad
		return cells + 1 // sample count = cells + 1
	}

	// Raise pitchXY until both X and Y fit under maxDim, then pitchZ
	// until Z fits. A direct closed-form solve is possible but the ceil()
	// coupling makes the short multiplicative loop simpler and just as
	// robust.
	for samples(0, pitchXY) > maxDim || samples(1, pitchXY) > maxDim {
		pitchXY *= 1.05
	}
	for samples(2, pitchZ) > maxDim {
		pitchZ *= 1.05
	}

	nx := samples(0, pitchXY)
	ny := samples(1, pitchXY)
	nz := samples(2, pitchZ)

	origin := vec3{
		lo[0] - gridPad*pitchXY,
		lo[1] - gridPad*pitchXY,
		lo[2] - gridPad*pitchZ,
	}
	return grid{origin: origin, dx: pitchXY, dy: pitchXY, dz: pitchZ, nx: nx, ny: ny, nz: nz}
}

// samplePos returns the world position of sample (i,j,k).
func (g grid) samplePos(i, j, k int) vec3 {
	return vec3{
		g.origin[0] + float64(i)*g.dx,
		g.origin[1] + float64(j)*g.dy,
		g.origin[2] + float64(k)*g.dz,
	}
}

// evalSlice fills dst (length nx*ny) with the field value f = |w| at
// every sample of Z-slice k, evaluated in parallel. Samples landing
// exactly on the iso level are nudged off it so no output vertex can
// coincide with a grid node (which would risk degenerate MC cases).
//
// The outermost sample shell of the grid (the six boundary faces) is
// clamped to 0, i.e. strictly outside, rather than being evaluated. This
// forces the isosurface to close inside the grid: a marching-cubes
// boundary edge (a triangle edge used by a single face — a crack) can
// only appear on a cube face lying on the grid's outer boundary, and
// clamping every corner on that boundary below the iso level removes any
// sign change there, so no such edge is ever generated. Without it, a
// winding field that has not decayed below iso within the padding — as
// happens for open-bottomed models whose interior field extends past the
// padded bbox — gets clipped by the grid boundary and opens the mesh.
func evalSlice(tree *bvh, g grid, k int, dst []float64) {
	z := g.origin[2] + float64(k)*g.dz
	zBoundary := k == 0 || k == g.nz-1
	workers := runtime.NumCPU()
	if workers > g.ny {
		workers = g.ny
	}
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	rows := (g.ny + workers - 1) / workers
	for w := 0; w < workers; w++ {
		j0 := w * rows
		if j0 >= g.ny {
			break
		}
		j1 := j0 + rows
		if j1 > g.ny {
			j1 = g.ny
		}
		wg.Add(1)
		go func(j0, j1 int) {
			defer wg.Done()
			for j := j0; j < j1; j++ {
				rowBoundary := zBoundary || j == 0 || j == g.ny-1
				y := g.origin[1] + float64(j)*g.dy
				base := j * g.nx
				for i := 0; i < g.nx; i++ {
					if rowBoundary || i == 0 || i == g.nx-1 {
						dst[base+i] = 0 // outer shell: force outside so the surface closes inside the grid
						continue
					}
					x := g.origin[0] + float64(i)*g.dx
					f := math.Abs(tree.winding(vec3{x, y, z}))
					if f == iso {
						f = iso + 1e-6
					}
					dst[base+i] = f
				}
			}
		}(j0, j1)
	}
	wg.Wait()
}

// edgeCorners maps each of the 12 cube edges to its two corner indices.
var edgeCorners = [12][2]int{
	{0, 1}, {1, 2}, {2, 3}, {3, 0},
	{4, 5}, {5, 6}, {6, 7}, {7, 4},
	{0, 4}, {1, 5}, {2, 6}, {3, 7},
}

// cornerOffset maps each of the 8 cube corners to its (dx,dy,dz) grid
// offset from the cell's base sample (i,j,k).
var cornerOffset = [8][3]int{
	{0, 0, 0}, {1, 0, 0}, {1, 1, 0}, {0, 1, 0},
	{0, 0, 1}, {1, 0, 1}, {1, 1, 1}, {0, 1, 1},
}

// edgeKeyOffset gives, per edge, the (dx,dy,dz) offset of the edge's
// lower corner plus its axis (0=x,1=y,2=z). Two cells sharing an edge
// resolve to the same key, so the interpolated vertex is welded exactly
// — the guarantee that makes the output watertight.
var edgeKeyOffset = [12][4]int{
	{0, 0, 0, 0}, {1, 0, 0, 1}, {0, 1, 0, 0}, {0, 0, 0, 1},
	{0, 0, 1, 0}, {1, 0, 1, 1}, {0, 1, 1, 0}, {0, 0, 1, 1},
	{0, 0, 0, 2}, {1, 0, 0, 2}, {1, 1, 0, 2}, {0, 1, 0, 2},
}

// contour runs marching cubes over the whole grid, streaming Z-slices
// so at most two field slices are resident at once (grids can reach
// maxDim³). Vertices are welded by canonical grid-edge key.
func contour(ctx context.Context, tree *bvh, g grid) ([][3]float32, [][3]uint32, error) {
	if g.nx < 2 || g.ny < 2 || g.nz < 2 {
		return nil, nil, fmt.Errorf("fwnrepair: grid too small (%dx%dx%d)", g.nx, g.ny, g.nz)
	}

	sliceLen := g.nx * g.ny
	fieldA := make([]float64, sliceLen) // Z-slice k
	fieldB := make([]float64, sliceLen) // Z-slice k+1

	var verts [][3]float32
	var faces [][3]uint32
	vertIndex := make(map[uint64]uint32)

	// getVert returns the welded output-vertex index for the crossing on
	// edge e of cell (i,j,k), interpolating on first encounter.
	getVert := func(i, j, k, e int, corners *[8]float64) uint32 {
		off := edgeKeyOffset[e]
		ci, cj, ck, axis := i+off[0], j+off[1], k+off[2], off[3]
		key := (((uint64(ck)*uint64(g.ny)+uint64(cj))*uint64(g.nx)+uint64(ci))<<2 | uint64(axis))
		if idx, ok := vertIndex[key]; ok {
			return idx
		}
		c0, c1 := edgeCorners[e][0], edgeCorners[e][1]
		v0, v1 := corners[c0], corners[c1]
		p0 := g.samplePos(i+cornerOffset[c0][0], j+cornerOffset[c0][1], k+cornerOffset[c0][2])
		p1 := g.samplePos(i+cornerOffset[c1][0], j+cornerOffset[c1][1], k+cornerOffset[c1][2])
		t := (iso - v0) / (v1 - v0)
		p := vec3{
			p0[0] + t*(p1[0]-p0[0]),
			p0[1] + t*(p1[1]-p0[1]),
			p0[2] + t*(p1[2]-p0[2]),
		}
		idx := uint32(len(verts))
		verts = append(verts, [3]float32{float32(p[0]), float32(p[1]), float32(p[2])})
		vertIndex[key] = idx
		return idx
	}

	evalSlice(tree, g, 0, fieldA)
	for k := 0; k < g.nz-1; k++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		evalSlice(tree, g, k+1, fieldB)

		for j := 0; j < g.ny-1; j++ {
			for i := 0; i < g.nx-1; i++ {
				var corners [8]float64
				for c := 0; c < 8; c++ {
					o := cornerOffset[c]
					f := fieldA
					if o[2] == 1 {
						f = fieldB
					}
					corners[c] = f[(j+o[1])*g.nx+(i+o[0])]
				}
				// Bourke convention: mark corners below the iso level.
				cube := 0
				for c := 0; c < 8; c++ {
					if corners[c] < iso {
						cube |= 1 << uint(c)
					}
				}
				if edgeTable[cube] == 0 {
					continue
				}
				tri := triTable[cube]
				for t := 0; tri[t] >= 0; t += 3 {
					a := getVert(i, j, k, tri[t], &corners)
					b := getVert(i, j, k, tri[t+1], &corners)
					c := getVert(i, j, k, tri[t+2], &corners)
					if a == b || b == c || a == c {
						continue // guard against pinched (degenerate) triangles
					}
					faces = append(faces, [3]uint32{a, b, c})
				}
			}
		}

		fieldA, fieldB = fieldB, fieldA
	}

	if len(faces) == 0 {
		return nil, nil, fmt.Errorf("fwnrepair: isosurface is empty (field never crossed %g)", iso)
	}
	return verts, faces, nil
}

// orientOutward flips every triangle if the mesh's signed volume is
// negative, so output normals point outward (toward decreasing field —
// away from the |w|>0.5 interior). For a closed manifold the sign of
// the total signed volume is a single global orientation flag.
func orientOutward(verts [][3]float32, faces [][3]uint32) {
	var vol float64
	for _, f := range faces {
		a := verts[f[0]]
		b := verts[f[1]]
		c := verts[f[2]]
		av := vec3{float64(a[0]), float64(a[1]), float64(a[2])}
		bv := vec3{float64(b[0]), float64(b[1]), float64(b[2])}
		cv := vec3{float64(c[0]), float64(c[1]), float64(c[2])}
		vol += dot(av, cross(bv, cv))
	}
	if vol < 0 {
		for i := range faces {
			faces[i][1], faces[i][2] = faces[i][2], faces[i][1]
		}
	}
}
