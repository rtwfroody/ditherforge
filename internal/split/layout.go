package split

import (
	"math"
)

// Transform maps original-mesh coordinates to bed coordinates:
//
//	bed_pos = Rotation · orig_pos + Translation
//
// where Rotation is a 3×3 rotation matrix stored row-major. The inverse
// (used by Voxelize for color sampling on the unmoved ColorModel and
// sticker meshes) is the transpose of Rotation:
//
//	orig_pos = Rotationᵀ · (bed_pos − Translation)
type Transform struct {
	Rotation    [9]float64 // 3×3, row-major
	Translation [3]float64
}

// IdentityTransform is the trivial (no-op) transform.
var IdentityTransform = Transform{
	Rotation: [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1},
}

// Apply maps p from original-mesh coords to bed coords.
func (t Transform) Apply(p [3]float32) [3]float32 {
	px, py, pz := float64(p[0]), float64(p[1]), float64(p[2])
	return [3]float32{
		float32(t.Rotation[0]*px + t.Rotation[1]*py + t.Rotation[2]*pz + t.Translation[0]),
		float32(t.Rotation[3]*px + t.Rotation[4]*py + t.Rotation[5]*pz + t.Translation[1]),
		float32(t.Rotation[6]*px + t.Rotation[7]*py + t.Rotation[8]*pz + t.Translation[2]),
	}
}

// ApplyInverse maps p from bed coords back to original-mesh coords.
// Phase-6 voxelize uses this for color sampling: the cell centroid
// arrives in bed coords, this returns the corresponding original-mesh
// coord where ColorModel and sticker decals live.
func (t Transform) ApplyInverse(p [3]float32) [3]float32 {
	px := float64(p[0]) - t.Translation[0]
	py := float64(p[1]) - t.Translation[1]
	pz := float64(p[2]) - t.Translation[2]
	return [3]float32{
		float32(t.Rotation[0]*px + t.Rotation[3]*py + t.Rotation[6]*pz),
		float32(t.Rotation[1]*px + t.Rotation[4]*py + t.Rotation[7]*pz),
		float32(t.Rotation[2]*px + t.Rotation[5]*py + t.Rotation[8]*pz),
	}
}

// Layout rotates each half according to result.Orientation[h], then
// places the two halves side by side along +X with `gapMM` between
// them, centred on Y = 0 and resting on Z = 0. Vertex positions in
// result.Halves are rewritten in place to bed coordinates. Returns
// the per-half Transform that took original-mesh coords to those bed
// coords.
//
// Half 0's outward cap normal is +result.Plane.Normal; half 1's is
// −result.Plane.Normal. Half 0 ends up to the −X side, half 1 to the
// +X side. The bbox-min-z=0 shift always applies, so each half rests
// with its lowest point on the bed regardless of orientation.
func Layout(result *CutResult, gapMM float64) [2]Transform {
	var xforms [2]Transform

	// Step 1: per-half rotation chosen by Orientation.
	//
	// A "cut face up/down" orientation (one whose up-axis equals the cut
	// axis) seats the half on its cut face. If the cut was tilted, the
	// cap is off-axis, so we first apply CapAlign — which rotates the
	// tilted cut frame back to the axis frame — and then the orientation
	// lands the now-axis-aligned cap flat on the bed. The model body
	// picks up the tilt instead. Orientations pointing a non-cut axis up
	// (e.g. +X up on an XY cut) rest on a model face and must NOT be
	// re-tilted, so they skip CapAlign. CapAlign is the identity for an
	// un-tilted cut, and result.Axis is -1 when the cut frame is unknown,
	// so both leave the legacy layout untouched.
	for h := 0; h < 2; h++ {
		R := orientationRotation(result.Orientation[h])
		if result.Axis >= 0 && orientationAxis(result.Orientation[h]) == result.Axis {
			R = matMul3(R, result.CapAlign)
		}
		for i, v := range result.Halves[h].Vertices {
			result.Halves[h].Vertices[i] = applyRotation(R, v)
		}
		xforms[h].Rotation = R
	}

	// Step 2: compute post-rotation bboxes; we need them for both the
	// z-zero shift and the side-by-side xy placement.
	bboxes := make([]struct {
		minX, maxX float64
		minY, maxY float64
		minZ       float64
	}, 2)
	for h := 0; h < 2; h++ {
		bboxes[h].minX = math.Inf(1)
		bboxes[h].maxX = math.Inf(-1)
		bboxes[h].minY = math.Inf(1)
		bboxes[h].maxY = math.Inf(-1)
		bboxes[h].minZ = math.Inf(1)
		for _, v := range result.Halves[h].Vertices {
			x, y, z := float64(v[0]), float64(v[1]), float64(v[2])
			if x < bboxes[h].minX {
				bboxes[h].minX = x
			}
			if x > bboxes[h].maxX {
				bboxes[h].maxX = x
			}
			if y < bboxes[h].minY {
				bboxes[h].minY = y
			}
			if y > bboxes[h].maxY {
				bboxes[h].maxY = y
			}
			if z < bboxes[h].minZ {
				bboxes[h].minZ = z
			}
		}
	}

	// Step 3: composed translation per half.
	//   - z: shift so bbox.min.z = 0.
	//   - y: shift so y-centroid = 0.
	//   - x: half 0 has min.x = 0; half 1 has min.x = halfA.x_extent + gap.
	halfAExtentX := bboxes[0].maxX - bboxes[0].minX
	translations := [2][3]float64{
		{
			-bboxes[0].minX,
			-(bboxes[0].minY + bboxes[0].maxY) / 2,
			-bboxes[0].minZ,
		},
		{
			-bboxes[1].minX + halfAExtentX + gapMM,
			-(bboxes[1].minY + bboxes[1].maxY) / 2,
			-bboxes[1].minZ,
		},
	}

	for h := 0; h < 2; h++ {
		for i, v := range result.Halves[h].Vertices {
			result.Halves[h].Vertices[i] = [3]float32{
				v[0] + float32(translations[h][0]),
				v[1] + float32(translations[h][1]),
				v[2] + float32(translations[h][2]),
			}
		}
		xforms[h].Translation = translations[h]
	}

	return xforms
}

// orientationRotation returns the row-major 3×3 rotation that points the
// requested model-space axis "up" (+Z on the bed). Each is a fixed
// axis-permutation rotation (a proper rotation, det +1) that leaves one
// of the other two authored axes unchanged, so the half keeps its
// authored yaw as closely as the choice allows. OrientZUp is the
// identity.
func orientationRotation(o Orientation) [9]float64 {
	switch o {
	case OrientZDown:
		// −Z up: 180° about X. +Z→−Z, +X→+X, +Y→−Y.
		return [9]float64{1, 0, 0, 0, -1, 0, 0, 0, -1}
	case OrientXUp:
		// +X up: +X→+Z, +Y→+Y, +Z→−X.
		return [9]float64{0, 0, -1, 0, 1, 0, 1, 0, 0}
	case OrientXDown:
		// −X up: +X→−Z, +Y→+Y, +Z→+X.
		return [9]float64{0, 0, 1, 0, 1, 0, -1, 0, 0}
	case OrientYUp:
		// +Y up: +Y→+Z, +X→+X, +Z→−Y.
		return [9]float64{1, 0, 0, 0, 0, -1, 0, 1, 0}
	case OrientYDown:
		// −Y up: +Y→−Z, +X→+X, +Z→+Y.
		return [9]float64{1, 0, 0, 0, 0, 1, 0, -1, 0}
	}
	// OrientZUp (default): identity.
	return [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}
}

// applyRotation returns R · v for a row-major 3×3 rotation matrix R.
func applyRotation(R [9]float64, v [3]float32) [3]float32 {
	px, py, pz := float64(v[0]), float64(v[1]), float64(v[2])
	return [3]float32{
		float32(R[0]*px + R[1]*py + R[2]*pz),
		float32(R[3]*px + R[4]*py + R[5]*pz),
		float32(R[6]*px + R[7]*py + R[8]*pz),
	}
}
