package split

import (
	"math"
)

// Transform maps original-mesh coordinates to bed coordinates:
//
//	bed_pos = Rotation · orig_pos + Translation
//
// where Rotation is a 3×3 rotation matrix stored row-major. The inverse
// (used by Voxelize for color sampling on the unmoved ColorModel /
// SampleModel / sticker meshes) is the transpose of Rotation:
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
// coord where ColorModel / SampleModel / sticker decals live.
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
	plane := result.Plane
	var xforms [2]Transform

	// Step 1: per-half rotation chosen by Orientation.
	capNormals := [2][3]float64{
		plane.Normal,
		{-plane.Normal[0], -plane.Normal[1], -plane.Normal[2]},
	}
	for h := 0; h < 2; h++ {
		R := orientationRotation(result.Orientation[h], capNormals[h])
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

// orientationRotation returns the row-major 3×3 rotation that orients
// `capNormal` according to the requested Orientation. For OrientOriginal
// the identity is returned (the half stays in its authored frame). For
// the four Seam* orientations, capNormal is rotated to the corresponding
// world axis (+Z, −Z, −X, +X). Z-rotation freedom is resolved by
// `rotateVecToTarget`'s arbitrary-but-stable axis choice.
func orientationRotation(o Orientation, capNormal [3]float64) [9]float64 {
	switch o {
	case OrientSeamUp:
		return rotateVecToTarget(capNormal, [3]float64{0, 0, 1})
	case OrientSeamDown:
		return rotateVecToTarget(capNormal, [3]float64{0, 0, -1})
	case OrientSeamLeft:
		return rotateVecToTarget(capNormal, [3]float64{-1, 0, 0})
	case OrientSeamRight:
		return rotateVecToTarget(capNormal, [3]float64{1, 0, 0})
	}
	return [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}
}

// rotateVecToTarget returns the row-major 3×3 rotation that maps unit
// vector `a` onto unit vector `target`. The antipodal case picks an
// arbitrary perpendicular axis for the 180° rotation; the choice is
// stable for reproducibility but not otherwise meaningful.
func rotateVecToTarget(a, target [3]float64) [9]float64 {
	dot := a[0]*target[0] + a[1]*target[1] + a[2]*target[2]
	const aligned = 1 - 1e-9
	if dot > aligned {
		return [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}
	}
	if dot < -aligned {
		// Pick any axis perpendicular to target. Use the world-axis
		// least-aligned with target so the cross product is well-conditioned.
		var perp [3]float64
		switch {
		case math.Abs(target[0]) <= math.Abs(target[1]) && math.Abs(target[0]) <= math.Abs(target[2]):
			perp = [3]float64{1, 0, 0}
		case math.Abs(target[1]) <= math.Abs(target[2]):
			perp = [3]float64{0, 1, 0}
		default:
			perp = [3]float64{0, 0, 1}
		}
		// Project perp onto the plane orthogonal to target, then normalize.
		d := perp[0]*target[0] + perp[1]*target[1] + perp[2]*target[2]
		ax := perp[0] - d*target[0]
		ay := perp[1] - d*target[1]
		az := perp[2] - d*target[2]
		l := math.Sqrt(ax*ax + ay*ay + az*az)
		ax /= l
		ay /= l
		az /= l
		// 180° rotation around (ax, ay, az).
		return [9]float64{
			2*ax*ax - 1, 2 * ax * ay, 2 * ax * az,
			2 * ay * ax, 2*ay*ay - 1, 2 * ay * az,
			2 * az * ax, 2 * az * ay, 2*az*az - 1,
		}
	}
	// Rodrigues' formula: axis = a × target (normalised), angle = acos(a · target).
	ax := a[1]*target[2] - a[2]*target[1]
	ay := a[2]*target[0] - a[0]*target[2]
	az := a[0]*target[1] - a[1]*target[0]
	axisLen := math.Sqrt(ax*ax + ay*ay + az*az)
	ax /= axisLen
	ay /= axisLen
	az /= axisLen
	angle := math.Acos(dot)
	c := math.Cos(angle)
	s := math.Sin(angle)
	omc := 1 - c
	return [9]float64{
		c + ax*ax*omc, ax*ay*omc - az*s, ax*az*omc + ay*s,
		ay*ax*omc + az*s, c + ay*ay*omc, ay*az*omc - ax*s,
		az*ax*omc - ay*s, az*ay*omc + ax*s, c + az*az*omc,
	}
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
