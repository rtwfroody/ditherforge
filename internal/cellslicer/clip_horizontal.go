// Diagnostic / experimental clip path: slice the model at slab Z
// boundaries only, with no per-cell XY intersection. Each output
// face is tagged with the slab-local cell whose Outer polygon
// contains its XY centroid (nearest-bbox-centroid fallback for
// points that land on a cell edge).
//
// The result is a topologically simple mesh — basically the input
// mesh with horizontal seams at every slab plane — at the cost of
// losing per-cell color resolution within a slab. Used to test
// whether the per-cell XY clip is the source of slicer-rejected
// topology defects.

package cellslicer

import (
	"github.com/rtwfroody/ditherforge/internal/loader"
)

// ClipMeshHorizontally clips model only at slab Z boundaries — no
// per-cell XY clip. See file header for caveats.
func ClipMeshHorizontally(model *loader.LoadedModel, slabs []Slab) (ClipResult, error) {
	offsets := make([]int, len(slabs)+1)
	for si := range slabs {
		offsets[si+1] = offsets[si] + len(slabs[si].Cells)
	}

	cellIndices := make([]*slabCellIndex, len(slabs))
	for si := range slabs {
		if len(slabs[si].Cells) > 0 {
			cellIndices[si] = buildSlabCellIndex(&slabs[si])
		}
	}

	var (
		verts       [][3]float32
		faces       [][3]uint32
		faceCellIdx []int32
		candBuf     []int
	)

	pickCell := func(s *Slab, idx *slabCellIndex, cx, cy float32) int32 {
		if idx == nil {
			return -1
		}
		candBuf = idx.candidates(cx, cy, cx, cy, candBuf)
		for _, ci := range candBuf {
			if pointInPolygon(s.Cells[ci].Outer, cx, cy) {
				return int32(ci)
			}
		}
		if len(candBuf) == 0 {
			// Re-scan with a tiny epsilon: cube-wall centroids land
			// exactly on the footprint boundary and the strict bbox
			// test misses them.
			eps := float32(1e-3)
			candBuf = idx.candidates(cx-eps, cy-eps, cx+eps, cy+eps, candBuf)
		}
		best := int32(-1)
		bestD2 := float32(0)
		for _, ci := range candBuf {
			b := idx.bbox[ci]
			bx := (b.minX + b.maxX) * 0.5
			by := (b.minY + b.maxY) * 0.5
			d2 := (bx-cx)*(bx-cx) + (by-cy)*(by-cy)
			if best < 0 || d2 < bestD2 {
				best = int32(ci)
				bestD2 = d2
			}
		}
		return best
	}

	for ti := range model.Faces {
		f := model.Faces[ti]
		a := model.Vertices[f[0]]
		b := model.Vertices[f[1]]
		c := model.Vertices[f[2]]
		zMin := minf3(a[2], b[2], c[2])
		zMax := maxf3(a[2], b[2], c[2])
		siLo, siHi := 0, len(slabs)-1
		for siLo <= siHi && slabs[siLo].ZTop < zMin {
			siLo++
		}
		for siHi >= siLo && slabs[siHi].ZBot > zMax {
			siHi--
		}
		for si := siLo; si <= siHi; si++ {
			if si < 0 || si >= len(slabs) {
				continue
			}
			s := &slabs[si]
			poly := sliceTriangleToSlab(a, b, c, s.ZBot, s.ZTop)
			if poly == nil || len(poly.Pts) < 3 {
				continue
			}
			idx := cellIndices[si]
			base := uint32(len(verts))
			verts = append(verts, poly.Pts...)
			// Fan-triangulate the convex slab-clipped sub-polygon,
			// matching each fan triangle's winding to the source
			// triangle's normal so outward facing is preserved.
			for i := 1; i < len(poly.Pts)-1; i++ {
				triN := triangleNormal(poly.Pts[0], poly.Pts[i], poly.Pts[i+1])
				dot := triN[0]*poly.Normal[0] + triN[1]*poly.Normal[1] + triN[2]*poly.Normal[2]
				cx := (poly.Pts[0][0] + poly.Pts[i][0] + poly.Pts[i+1][0]) / 3
				cy := (poly.Pts[0][1] + poly.Pts[i][1] + poly.Pts[i+1][1]) / 3
				ci := pickCell(s, idx, cx, cy)
				if dot >= 0 {
					faces = append(faces, [3]uint32{base, base + uint32(i), base + uint32(i+1)})
				} else {
					faces = append(faces, [3]uint32{base, base + uint32(i+1), base + uint32(i)})
				}
				faceCellIdx = appendCell(faceCellIdx, ci, offsets[si])
			}
		}
	}

	return ClipResult{Verts: verts, Faces: faces, FaceCellIdx: faceCellIdx}, nil
}

func appendCell(dst []int32, ci int32, slabOffset int) []int32 {
	if ci < 0 {
		return append(dst, -1)
	}
	return append(dst, int32(slabOffset)+ci)
}

