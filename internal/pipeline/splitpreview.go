package pipeline

import (
	"fmt"
)

// SplitPreviewResult describes the cut plane and the model's
// projected silhouette in plane-local coordinates so the frontend
// can draw a translucent rectangle through the model. All vector
// fields are in original-mesh world coordinates (the same frame as
// the input mesh emitted via OnInputMesh) — NOT in bed coordinates.
type SplitPreviewResult struct {
	// Origin is the centre of the model's silhouette projected onto
	// the cut plane. Lies on the plane (Normal·Origin == Offset)
	// but is offset within the plane to the projected centroid so
	// the rendered quad is symmetric over the model.
	Origin [3]float32 `json:"origin"`
	// Normal is the plane's unit normal, in original-mesh coords.
	Normal [3]float32 `json:"normal"`
	// U and V are the orthonormal basis vectors that span the plane,
	// chosen with U × V = Normal so the frontend can build a
	// right-handed orientation for the quad.
	U [3]float32 `json:"u"`
	V [3]float32 `json:"v"`
	// HalfExtentU and HalfExtentV are half-side lengths of the
	// plane-local bounding rectangle that contains the model's
	// projection onto (U, V). The quad rendered by the frontend has
	// world-space corners
	//   Origin ± HalfExtentU·U ± HalfExtentV·V.
	HalfExtentU float32 `json:"halfExtentU"`
	HalfExtentV float32 `json:"halfExtentV"`
}

// ComputeSplitPreview returns the cut-plane geometry for the model
// cached under `opts`. Reads the StageLoad output from the cache;
// returns an error if it's not present (e.g., the user hasn't run
// the pipeline since startup).
//
// Goroutine-safe: only reads from the cache (which itself reads from
// disk via atomic rename) and Vertices (immutable after StageLoad
// completes). Safe to call from any goroutine, including
// concurrently with a pipeline run.
func ComputeSplitPreview(cache *StageCache, opts Options, s SplitSettings) (*SplitPreviewResult, error) {
	lo := cache.getLoad(opts)
	if lo == nil || lo.Model == nil {
		return nil, fmt.Errorf("split preview: model load output not in cache (run the pipeline first)")
	}
	return computeSplitPreviewFromVertices(lo.Model.Vertices, s)
}

// computeSplitPreviewFromVertices is the pure, cache-independent
// core of ComputeSplitPreview. Tests inject vertices directly here
// rather than go through the cache, which would require disk-backed
// scaffolding for round-tripping a synthetic loadOutput.
//
// The result is centered on the model's projected bbox along (U, V)
// so the quad is symmetric over the model — convenient for
// frontend rendering. The plane's actual world position is at
// `Offset` along the chosen `Axis`; the centering only translates
// the quad within the plane (U·Normal = V·Normal = 0), not the
// plane equation Normal·p = Offset.
//
// Mirrored client-side in frontend/src/App.svelte's
// `cutPlanePreview` $derived to avoid an RPC per slider tick. Keep
// the two implementations in sync — especially the (U, V) basis
// table and the centering math.
func computeSplitPreviewFromVertices(verts [][3]float32, s SplitSettings) (*SplitPreviewResult, error) {
	if len(verts) == 0 {
		return nil, fmt.Errorf("split preview: model has no vertices")
	}

	axis := s.Axis
	if axis < 0 || axis > 2 {
		axis = 2
	}
	var normal [3]float32
	normal[axis] = 1

	// Origin starts at offset along the chosen axis, from world
	// origin. This matches split.AxisPlane(axis, offset) which says
	// "Normal·p == D" with D = offset.
	origin := [3]float32{0, 0, 0}
	origin[axis] = float32(s.Offset)

	// Orthonormal (U, V) basis on the plane. Fixed convention per
	// axis so the basis is stable as the user toggles axes.
	// All three are right-handed: U × V = Normal.
	var u, v [3]float32
	switch axis {
	case 0: // normal = +X → U=+Y, V=+Z
		u = [3]float32{0, 1, 0}
		v = [3]float32{0, 0, 1}
	case 1: // normal = +Y → U=+Z, V=+X
		u = [3]float32{0, 0, 1}
		v = [3]float32{1, 0, 0}
	default: // axis == 2, normal = +Z → U=+X, V=+Y
		u = [3]float32{1, 0, 0}
		v = [3]float32{0, 1, 0}
	}

	// Project the model's silhouette onto (U, V); find the bbox.
	// Note: this is the projected silhouette of all vertices, not
	// the cross-section at the cut. The frontend renders this as a
	// translucent overlay, so a slightly oversized rectangle is
	// preferable to one that shrinks/grows as the cut moves through
	// the model.
	minU, maxU := projectAxis(verts[0], u), projectAxis(verts[0], u)
	minV, maxV := projectAxis(verts[0], v), projectAxis(verts[0], v)
	for _, p := range verts[1:] {
		du := projectAxis(p, u)
		dv := projectAxis(p, v)
		if du < minU {
			minU = du
		}
		if du > maxU {
			maxU = du
		}
		if dv < minV {
			minV = dv
		}
		if dv > maxV {
			maxV = dv
		}
	}
	halfU := (maxU - minU) / 2
	halfV := (maxV - minV) / 2
	originU := (minU + maxU) / 2
	originV := (minV + maxV) / 2
	for i := 0; i < 3; i++ {
		origin[i] += originU*u[i] + originV*v[i]
	}

	return &SplitPreviewResult{
		Origin:      origin,
		Normal:      normal,
		U:           u,
		V:           v,
		HalfExtentU: halfU,
		HalfExtentV: halfV,
	}, nil
}

// projectAxis returns the dot product of point p and unit-vector
// axis a — the scalar coordinate of p along a.
func projectAxis(p, a [3]float32) float32 {
	return p[0]*a[0] + p[1]*a[1] + p[2]*a[2]
}
