package split

import (
	"fmt"
	"math"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// buildCylinder returns a closed triangle-mesh cylinder centered at
// the origin, with axis pointing along `axis` (must be unit-length),
// the given radius, total height 2*halfHeight, and `segments`
// circumferential subdivisions. The cylinder is geometry-only — the
// returned LoadedModel populates only Vertices and Faces.
//
// Tessellation: 2*segments triangles for the side wall, segments-2
// for each cap (fan from the first ring vertex), so 4*segments-4
// triangles total. Faces wind so that outward normals point away
// from the cylinder axis (and from the caps).
func buildCylinder(axis [3]float64, radius, halfHeight float64, segments int) (*loader.LoadedModel, error) {
	if segments < 3 {
		return nil, fmt.Errorf("buildCylinder: segments must be ≥ 3, got %d", segments)
	}
	if radius <= 0 || halfHeight <= 0 {
		return nil, fmt.Errorf("buildCylinder: radius and halfHeight must be positive (got %v, %v)", radius, halfHeight)
	}

	u, v := perpBasis(axis)

	// Vertices: top ring (at +halfHeight) followed by bottom ring (at
	// -halfHeight). 2*segments vertices total.
	verts := make([][3]float32, 0, 2*segments)
	for s := 0; s < 2; s++ {
		h := halfHeight
		if s == 1 {
			h = -halfHeight
		}
		for i := 0; i < segments; i++ {
			theta := 2 * math.Pi * float64(i) / float64(segments)
			c := math.Cos(theta)
			si := math.Sin(theta)
			p := [3]float64{
				radius*(c*u[0]+si*v[0]) + h*axis[0],
				radius*(c*u[1]+si*v[1]) + h*axis[1],
				radius*(c*u[2]+si*v[2]) + h*axis[2],
			}
			verts = append(verts, [3]float32{float32(p[0]), float32(p[1]), float32(p[2])})
		}
	}

	faces := make([][3]uint32, 0, 4*segments-4)

	// Side walls: each segment i contributes two triangles.
	// top[i], bot[i], bot[(i+1)%segments] and top[i], bot[(i+1)%segments], top[(i+1)%segments]
	// Wind outward (right-hand rule with outward normal away from axis).
	for i := 0; i < segments; i++ {
		t0 := uint32(i)
		t1 := uint32((i + 1) % segments)
		b0 := uint32(segments + i)
		b1 := uint32(segments + (i+1)%segments)
		faces = append(faces,
			[3]uint32{t0, b0, b1},
			[3]uint32{t0, b1, t1},
		)
	}

	// Top cap: fan from vertex 0, normal == +axis. Triangle (0, i, i+1)
	// where i runs from 1 to segments-2.
	for i := uint32(1); i < uint32(segments-1); i++ {
		faces = append(faces, [3]uint32{0, i, i + 1})
	}

	// Bottom cap: fan from segments (first bottom vertex), normal == -axis.
	// Wind in reverse order so the outward normal points opposite to axis.
	base := uint32(segments)
	for i := uint32(1); i < uint32(segments-1); i++ {
		faces = append(faces, [3]uint32{base, base + i + 1, base + i})
	}

	return &loader.LoadedModel{
		Vertices: verts,
		Faces:    faces,
	}, nil
}

// translateMesh returns a new LoadedModel whose vertices are shifted
// by `offset`. Geometry-only.
func translateMesh(m *loader.LoadedModel, offset [3]float64) *loader.LoadedModel {
	out := &loader.LoadedModel{
		Vertices: make([][3]float32, len(m.Vertices)),
		Faces:    make([][3]uint32, len(m.Faces)),
	}
	copy(out.Faces, m.Faces)
	for i, v := range m.Vertices {
		out.Vertices[i] = [3]float32{
			v[0] + float32(offset[0]),
			v[1] + float32(offset[1]),
			v[2] + float32(offset[2]),
		}
	}
	return out
}

// perpBasis returns two unit vectors u, v that together with `n`
// form a right-handed orthonormal basis (u × v == n). `n` must be
// unit-length.
func perpBasis(n [3]float64) (u, v [3]float64) {
	// Pick the basis vector least aligned with n to avoid degenerate
	// cross products.
	var ref [3]float64
	if math.Abs(n[0]) <= math.Abs(n[1]) && math.Abs(n[0]) <= math.Abs(n[2]) {
		ref = [3]float64{1, 0, 0}
	} else if math.Abs(n[1]) <= math.Abs(n[2]) {
		ref = [3]float64{0, 1, 0}
	} else {
		ref = [3]float64{0, 0, 1}
	}
	u = cross3(ref, n)
	u = normalize3(u)
	v = cross3(n, u)
	v = normalize3(v)
	return u, v
}

func cross3(a, b [3]float64) [3]float64 {
	return [3]float64{
		a[1]*b[2] - a[2]*b[1],
		a[2]*b[0] - a[0]*b[2],
		a[0]*b[1] - a[1]*b[0],
	}
}

func normalize3(a [3]float64) [3]float64 {
	l := math.Sqrt(a[0]*a[0] + a[1]*a[1] + a[2]*a[2])
	if l == 0 {
		return a
	}
	return [3]float64{a[0] / l, a[1] / l, a[2] / l}
}
