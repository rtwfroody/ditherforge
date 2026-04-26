package voxel

import (
	"math"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// makeFlatGrid builds a regular tessellated XY plane: (n+1)×(n+1) verts,
// 2n² triangles with one-direction diagonals.
func makeFlatGrid(size float32, n int) *loader.LoadedModel {
	nv := n + 1
	verts := make([][3]float32, 0, nv*nv)
	cell := size / float32(n)
	for j := 0; j <= n; j++ {
		for i := 0; i <= n; i++ {
			verts = append(verts, [3]float32{float32(i) * cell, float32(j) * cell, 0})
		}
	}
	var faces [][3]uint32
	for j := 0; j < n; j++ {
		for i := 0; i < n; i++ {
			v0 := uint32(j*nv + i)
			v1 := v0 + 1
			v2 := v0 + uint32(nv)
			v3 := v2 + 1
			faces = append(faces, [3]uint32{v0, v1, v3}, [3]uint32{v0, v3, v2})
		}
	}
	return &loader.LoadedModel{Vertices: verts, Faces: faces}
}

// makeClosedCylinder builds a CLOSED cylindrical shell (no caps) of radius
// r, height h, with `segs` segments around. Vertices on z=-h/2 first
// (indices 0..segs-1), then z=+h/2 (indices segs..2*segs-1).
func makeClosedCylinder(r, h float32, segs int) *loader.LoadedModel {
	var verts [][3]float32
	for i := 0; i < segs; i++ {
		theta := 2 * math.Pi * float64(i) / float64(segs)
		x := r * float32(math.Cos(theta))
		y := r * float32(math.Sin(theta))
		verts = append(verts, [3]float32{x, y, -h / 2})
	}
	for i := 0; i < segs; i++ {
		theta := 2 * math.Pi * float64(i) / float64(segs)
		x := r * float32(math.Cos(theta))
		y := r * float32(math.Sin(theta))
		verts = append(verts, [3]float32{x, y, h / 2})
	}
	var faces [][3]uint32
	for i := 0; i < segs; i++ {
		a := uint32(i)
		b := uint32((i + 1) % segs)
		c := uint32(segs + i)
		d := uint32(segs + (i+1)%segs)
		faces = append(faces, [3]uint32{a, b, d}, [3]uint32{a, d, c})
	}
	return &loader.LoadedModel{Vertices: verts, Faces: faces}
}

// dirtifyMesh injects decimation-style artifacts into a clean mesh, in
// place: zero-area "slivers" (collinear vertices), oversized triangles
// formed by collapsing adjacent face pairs, and a handful of literal
// duplicate faces. This reproduces the failure profile observed in
// real-world heavily-decimated meshes (top.stl / base.stl in the user's
// test set) without depending on any external fixture files.
//
// Counts are chosen to roughly match the ~2-4% degenerate / 50:1 area
// ratio seen on those meshes. A fixed seed keeps tests deterministic.
func dirtifyMesh(model *loader.LoadedModel, seed int64, frac float32) {
	rng := newDeterministicRNG(seed)
	nFaces := len(model.Faces)
	if nFaces == 0 {
		return
	}

	// Mark ~frac of faces as zero-area degenerates: collapse vertex 2
	// onto vertex 0 by appending a duplicate vertex at v0's position.
	// Caught by the BFS-time badTriangle filter (twiceArea < minTwiceArea).
	nDegen := int(float32(nFaces) * frac)
	for i := 0; i < nDegen; i++ {
		fi := rng.intn(nFaces)
		f := model.Faces[fi]
		v0 := model.Vertices[f[0]]
		// Append a vertex coincident with v0 (snapped match → same
		// position, but a fresh index so we don't disturb other faces
		// referencing f[2]).
		model.Vertices = append(model.Vertices, v0)
		newIdx := uint32(len(model.Vertices) - 1)
		f[2] = newIdx
		model.Faces[fi] = f
	}

	// Append ~frac of high-aspect-ratio slivers: long, thin triangles
	// with non-zero area. Caught by the post-BFS sliver filter
	// (twiceArea/maxEdge² < 0.01). Distinct from the zero-area degenerates
	// above — those test the BFS filter, these test the LSCM-input filter.
	// Each sliver is created by taking a random face, stretching v2 along
	// the v0-v1 edge so the third vertex lies just barely off the line.
	nSlivers := nDegen
	for i := 0; i < nSlivers; i++ {
		fi := rng.intn(nFaces)
		f := model.Faces[fi]
		v0 := model.Vertices[f[0]]
		v1 := model.Vertices[f[1]]
		// Place v2 far along the v0→v1 direction, with a tiny
		// perpendicular offset so the area is nonzero but the aspect
		// ratio is ~1:1000. Perpendicular = an arbitrary axis, picking
		// world Z component scaled tiny.
		dx := v1[0] - v0[0]
		dy := v1[1] - v0[1]
		dz := v1[2] - v0[2]
		long := [3]float32{
			v0[0] + dx*5.0,
			v0[1] + dy*5.0,
			v0[2] + dz*5.0 + 0.001, // tiny offset → non-collinear, very thin
		}
		model.Vertices = append(model.Vertices, long)
		newIdx := uint32(len(model.Vertices) - 1)
		newFace := [3]uint32{f[0], f[1], newIdx}
		model.Faces = append(model.Faces, newFace)
		if model.FaceBaseColor != nil {
			model.FaceBaseColor = append(model.FaceBaseColor, [4]uint8{128, 128, 128, 255})
		}
	}

	// Append ~frac/4 huge faces by stretching a random face's vertex 2
	// far away — produces a tri spanning a large fraction of the model.
	nHuge := nDegen / 4
	bbMin, bbMax := computeMeshBounds(model)
	diag := edgeLen3D(bbMin, bbMax)
	for i := 0; i < nHuge; i++ {
		fi := rng.intn(nFaces)
		f := model.Faces[fi]
		v0 := model.Vertices[f[0]]
		stretched := [3]float32{
			v0[0] + diag*0.6,
			v0[1] + diag*0.6,
			v0[2] + diag*0.6,
		}
		model.Vertices = append(model.Vertices, stretched)
		newIdx := uint32(len(model.Vertices) - 1)
		// Append a NEW face rather than rewriting (so we keep the original
		// connected mesh intact and just add a free-floating huge tri).
		newFace := [3]uint32{f[0], f[1], newIdx}
		model.Faces = append(model.Faces, newFace)
		if model.FaceBaseColor != nil {
			model.FaceBaseColor = append(model.FaceBaseColor, [4]uint8{128, 128, 128, 255})
		}
	}

	// Append a few literal duplicate faces (matches "duplicate faces
	// (snapped): 15-28" in real meshes).
	nDups := nDegen / 100
	if nDups < 5 {
		nDups = 5
	}
	for i := 0; i < nDups; i++ {
		fi := rng.intn(nFaces)
		model.Faces = append(model.Faces, model.Faces[fi])
		if model.FaceBaseColor != nil {
			model.FaceBaseColor = append(model.FaceBaseColor, model.FaceBaseColor[fi])
		}
	}
}

func computeMeshBounds(model *loader.LoadedModel) (minP, maxP [3]float32) {
	minP = [3]float32{math.MaxFloat32, math.MaxFloat32, math.MaxFloat32}
	maxP = [3]float32{-math.MaxFloat32, -math.MaxFloat32, -math.MaxFloat32}
	for _, v := range model.Vertices {
		for d := 0; d < 3; d++ {
			if v[d] < minP[d] {
				minP[d] = v[d]
			}
			if v[d] > maxP[d] {
				maxP[d] = v[d]
			}
		}
	}
	return
}

// Tiny self-contained PRNG so test fixtures don't pull in math/rand
// state from the wider package.
type deterministicRNG struct{ state uint64 }

func newDeterministicRNG(seed int64) *deterministicRNG {
	return &deterministicRNG{state: uint64(seed) | 1}
}

func (r *deterministicRNG) intn(n int) int {
	// xorshift64
	r.state ^= r.state << 13
	r.state ^= r.state >> 7
	r.state ^= r.state << 17
	return int(r.state % uint64(n))
}

// makeCylinderArc builds an open cylindrical patch spanning angles
// [-arcDeg/2, +arcDeg/2] around the +X axis at radius r and height h, with
// segs segments along the arc. Open at the seams — disk topology.
func makeCylinderArc(r, h, arcDeg float32, segs int) *loader.LoadedModel {
	arc := float64(arcDeg) * math.Pi / 180
	var verts [][3]float32
	nv := segs + 1
	for i := 0; i <= segs; i++ {
		theta := -arc/2 + arc*float64(i)/float64(segs)
		x := r * float32(math.Cos(theta))
		y := r * float32(math.Sin(theta))
		verts = append(verts, [3]float32{x, y, -h / 2})
	}
	for i := 0; i <= segs; i++ {
		theta := -arc/2 + arc*float64(i)/float64(segs)
		x := r * float32(math.Cos(theta))
		y := r * float32(math.Sin(theta))
		verts = append(verts, [3]float32{x, y, h / 2})
	}
	var faces [][3]uint32
	for i := 0; i < segs; i++ {
		a := uint32(i)
		b := uint32(i + 1)
		c := uint32(nv + i)
		d := uint32(nv + i + 1)
		faces = append(faces, [3]uint32{a, b, d}, [3]uint32{a, d, c})
	}
	return &loader.LoadedModel{Vertices: verts, Faces: faces}
}

