package cellslicer

import (
	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/minislicer"
)

// BuildPrismMesh extrudes outer (CCW XY polygon) from zBot to zTop
// into a closed triangle-soup mesh suitable for CGAL Boolean ops.
// Top and bottom caps are fan-triangulated from the first vertex
// (fine for convex hex / ring cells; for general non-convex cells
// caller should pre-triangulate). Side walls are two triangles per
// edge. The output has 2N vertices and 4N - 4 triangles for an
// N-vertex outer (2N-4 cap + 2N wall).
func BuildPrismMesh(outer []minislicer.Point2, zBot, zTop float32) *loader.LoadedModel {
	n := len(outer)
	if n < 3 || zTop <= zBot {
		return nil
	}
	verts := make([][3]float32, 0, 2*n)
	// Bottom ring (indices 0..n-1)
	for _, p := range outer {
		verts = append(verts, [3]float32{p[0], p[1], zBot})
	}
	// Top ring (indices n..2n-1)
	for _, p := range outer {
		verts = append(verts, [3]float32{p[0], p[1], zTop})
	}

	faces := make([][3]uint32, 0, 4*n-4)
	// Bottom cap (CW from below = CCW normal points -Z). outer is
	// CCW from above, so for a -Z outward normal, fan in reverse.
	for i := uint32(1); i < uint32(n-1); i++ {
		faces = append(faces, [3]uint32{0, i + 1, i})
	}
	// Top cap (CCW from above = +Z outward normal).
	for i := uint32(1); i < uint32(n-1); i++ {
		faces = append(faces, [3]uint32{uint32(n), uint32(n) + i, uint32(n) + i + 1})
	}
	// Side walls: edge i → i+1 (mod n), connect to top ring.
	for i := uint32(0); i < uint32(n); i++ {
		ni := (i + 1) % uint32(n)
		b0 := i
		b1 := ni
		t0 := uint32(n) + i
		t1 := uint32(n) + ni
		// Two triangles per quad, CCW outward (since outer is CCW
		// from above, the side normal points outward).
		faces = append(faces, [3]uint32{b0, b1, t1})
		faces = append(faces, [3]uint32{b0, t1, t0})
	}
	return &loader.LoadedModel{
		Vertices: verts,
		Faces:    faces,
	}
}
