package cellslicer

import "math"

// Canonical 1µm-bucket quantisation for cellslicer geometry.
//
// Every vertex that the cellslicer emits — slab clips, prism clips,
// cell-piece splices, footprint round-trips — must pass through this
// API so it sits exactly on the 1µm grid (clipperScale = 1000, defined
// in clipper2d.go). With on-grid vertices, equality on the int3D
// bucket is exact: the splice machinery in clip2d_subdivide.go uses
// strict int64 collinearity instead of a float-tolerance bucket
// fudge, and the cross-piece dedup in clip2d.go matches coincident-
// position vertices across cells without ambiguity.
//
// Background. Before this API existed, two paths produced the same
// logical point at slightly different float coordinates:
//   - Clipper's PtIntersection rounded each XY component to 1µm and
//     re-emitted a float Point2; the resulting polygon vertices were
//     on the grid but the Z was re-lifted from the source-plane
//     equation, amplifying error.
//   - lerpAtZ wrote z = slabPlane verbatim but lerp'd XY in float;
//     two source triangles sharing an edge produced XY values that
//     differed in the last bit.
// Splice's float tolerance existed only to bridge that gap. Snapping
// every emitted vertex to grid makes the gap disappear.

// int3D is a 1µm-bucket integer 3D position. X, Y, and Z are model
// millimetres × clipperScale (1000) rounded to int64; two
// independently rounded float copies of the same logical position
// bucket to the same int3D, so an edge / vertex map keyed by int3D
// pairs reliably identifies coincident vertices across paths without
// float comparison pitfalls.
type int3D struct {
	X, Y, Z int64
}

// Quantize returns the 1µm-bucket int3D identifying the canonical
// position of p.
func Quantize(p [3]float32) int3D {
	return int3D{
		X: int64(math.Round(float64(p[0]) * clipperScale)),
		Y: int64(math.Round(float64(p[1]) * clipperScale)),
		Z: int64(math.Round(float64(p[2]) * clipperScale)),
	}
}

// Dequantize returns the canonical float32 position at the centre of
// bucket q.
//
// Precision envelopes (different limits, both should be respected):
//
//   - float32 round-trip lossless when |coord_mm| × clipperScale fits
//     in float32's 24-bit mantissa, i.e. ~16 m. Beyond that, individual
//     positions can drift by a bucket on Dequantize.
//   - int64 cross/dot in splicePoly3DEdges stays overflow-free while
//     coord_mm × clipperScale ≲ 0.85 × 10⁹, i.e. model extents up to
//     ~850 m. This is the tighter of the two limits and the one that
//     bounds practical cellslicer use; print volumes (cm–m) are deep
//     inside the envelope.
func Dequantize(q int3D) [3]float32 {
	return [3]float32{
		float32(float64(q.X) * invClipperScale),
		float32(float64(q.Y) * invClipperScale),
		float32(float64(q.Z) * invClipperScale),
	}
}

// Snap is Dequantize(Quantize(p)). Use it at every site that emits a
// vertex into the cellslicer pipeline so two paths computing the same
// logical point land on bit-identical floats — no tolerance, no
// downstream splice gymnastics.
//
// math.Round inside Quantize rounds half-away-from-zero, so values at
// exact half-bucket boundaries (e.g. +0.0005 mm and −0.0005 mm) round
// to opposite buckets — the function is symmetric across the origin
// but not strictly monotone near a tie. In practice the cellslicer's
// inputs (slab plane Z values, source-tri vertices, intersection
// lerps) never sit exactly on a half-bucket boundary, but if a future
// caller hand-constructs such values it should expect to see them
// snap to two distinct buckets across zero.
func Snap(p [3]float32) [3]float32 {
	return Dequantize(Quantize(p))
}

// DedupVertsByPosition returns a mesh whose vertices are collapsed to
// their canonical 1µm bucket. Duplicate-position vertices that the
// loader kept separate because they had different UVs / normals /
// other per-vertex attributes (typical of textured GLB) all map to
// the same output vertex, and faces are re-indexed.
//
// Degenerate faces (two or more vertices sharing the same bucket
// after merging) are dropped — these are usually no-area triangles
// the loader emitted as filler, not load-bearing topology.
//
// Manifold's C ABI requires watertight 2-manifold input; the
// cellslicer pipeline's source meshes (alphawrap output, untextured
// GLB, simple STL) are mostly already that shape after position dedup.
// Run this once before constructing the source Manifold.
func DedupVertsByPosition(verts [][3]float32, faces [][3]uint32) ([][3]float32, [][3]uint32) {
	if len(verts) == 0 {
		return nil, nil
	}
	bucketIdx := make(map[int3D]uint32, len(verts))
	remap := make([]uint32, len(verts))
	outV := make([][3]float32, 0, len(verts))
	for i, v := range verts {
		q := Quantize(v)
		if j, ok := bucketIdx[q]; ok {
			remap[i] = j
			continue
		}
		j := uint32(len(outV))
		bucketIdx[q] = j
		remap[i] = j
		outV = append(outV, Dequantize(q))
	}
	outF := make([][3]uint32, 0, len(faces))
	for _, f := range faces {
		a, b, c := remap[f[0]], remap[f[1]], remap[f[2]]
		if a == b || b == c || a == c {
			continue
		}
		outF = append(outF, [3]uint32{a, b, c})
	}
	return outV, outF
}
