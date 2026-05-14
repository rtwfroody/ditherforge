package cellslicer

import (
	"github.com/rtwfroody/ditherforge/internal/loader"
)

// BuildPrismMesh extrudes outer (a CCW XY polygon, possibly non-
// convex) from zBot to zTop into a closed triangle-soup mesh
// suitable for CGAL Boolean ops. Caps are earcut-triangulated so
// non-convex cells don't produce self-intersecting fans. Returns
// nil for degenerate inputs.
func BuildPrismMesh(outer []Point2, zBot, zTop float32) *loader.LoadedModel {
	n := len(outer)
	if n < 3 || zTop <= zBot {
		return nil
	}
	// Earcut the outer once; we'll reuse the triangulation for both
	// the bottom and top cap (re-oriented as needed).
	earVerts, capTris := Earcut(outer, nil)
	if len(capTris) == 0 {
		return nil
	}
	// Earcut may inject Steiner-like vertices, but with no holes
	// passed in it should return the outer's vertices unchanged.
	// Defensive: only proceed if vertex count matches.
	if len(earVerts) != n {
		return nil
	}

	verts := make([][3]float32, 0, 2*n)
	for _, p := range outer {
		verts = append(verts, [3]float32{p[0], p[1], zBot})
	}
	for _, p := range outer {
		verts = append(verts, [3]float32{p[0], p[1], zTop})
	}

	faces := make([][3]uint32, 0, 2*len(capTris)+2*n)
	// Bottom cap: reverse winding so the outward normal points -Z.
	for _, t := range capTris {
		faces = append(faces, [3]uint32{t[0], t[2], t[1]})
	}
	// Top cap: shift to top-ring indices, keep CCW for +Z normal.
	off := uint32(n)
	for _, t := range capTris {
		faces = append(faces, [3]uint32{t[0] + off, t[1] + off, t[2] + off})
	}
	// Side walls: edge i → i+1 (mod n), connect to top ring.
	for i := uint32(0); i < uint32(n); i++ {
		ni := (i + 1) % uint32(n)
		b0 := i
		b1 := ni
		t0 := uint32(n) + i
		t1 := uint32(n) + ni
		faces = append(faces, [3]uint32{b0, b1, t1})
		faces = append(faces, [3]uint32{b0, t1, t0})
	}
	return &loader.LoadedModel{
		Vertices: verts,
		Faces:    faces,
	}
}
