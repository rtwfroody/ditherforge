package pipeline

import (
	"fmt"
	"math"

	"github.com/rtwfroody/ditherforge/internal/split"
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

	// The plane's tilted unit normal and right-handed in-plane basis.
	// tiltA = tiltB = 0 reproduces the axis-aligned frame exactly.
	normal, u, v := split.TiltedFrame(s.Axis, s.TiltADeg, s.TiltBDeg)

	// Pivot: the point the plane passes through and rotates about —
	// the projected centroid of the model's silhouette, at Offset
	// along the chosen axis. Shares splitPlanePivot with runSplit so the
	// preview quad registers on the real cut — but only when s.Offset is
	// in the SAME units runSplit sees (resolved millimetres). Callers
	// holding a still-fractional Offset (the live GUI SplitSettings) must
	// resolve it first or the previewed plane lands at the wrong depth.
	pivot := splitPlanePivot(verts, s.Axis, s.Offset)

	// Half-side lengths of the plane-local rectangle that contains the
	// model's projection onto the tilted (U, V), measured symmetrically
	// about the pivot so the rendered quad is centred there. With
	// tilt=0 this equals the old (max-min)/2 since the pivot is the
	// projection centre.
	var halfU, halfV float64
	for _, p := range verts {
		d := [3]float64{float64(p[0]) - pivot[0], float64(p[1]) - pivot[1], float64(p[2]) - pivot[2]}
		if au := math.Abs(dot3f(d, u)); au > halfU {
			halfU = au
		}
		if av := math.Abs(dot3f(d, v)); av > halfV {
			halfV = av
		}
	}

	return &SplitPreviewResult{
		Origin:      [3]float32{float32(pivot[0]), float32(pivot[1]), float32(pivot[2])},
		Normal:      [3]float32{float32(normal[0]), float32(normal[1]), float32(normal[2])},
		U:           [3]float32{float32(u[0]), float32(u[1]), float32(u[2])},
		V:           [3]float32{float32(v[0]), float32(v[1]), float32(v[2])},
		HalfExtentU: float32(halfU),
		HalfExtentV: float32(halfV),
	}, nil
}

// splitPlanePivot returns the point the cut plane passes through: the
// centre of the model's silhouette projected onto the plane's base
// (axis-aligned) in-plane basis, placed at offsetMM along the cut axis.
// This is the rotation pivot shared by the preview quad and the real
// cut in runSplit, so the two stay registered under tilt.
//
// offsetMM must be in millimetres along the axis — runSplit passes the
// fraction-resolved Split.Offset (see applyFractionalOptions). On an
// empty mesh it returns the origin, leaving split.Cut to surface the
// clean "empty model" error rather than panicking on verts[0].
func splitPlanePivot(verts [][3]float32, axis int, offsetMM float64) [3]float64 {
	if axis < 0 || axis > 2 {
		axis = 2
	}
	if len(verts) == 0 {
		return [3]float64{}
	}
	u, v := split.AxisBasis(axis)
	var n [3]float64
	n[axis] = 1

	p0 := [3]float64{float64(verts[0][0]), float64(verts[0][1]), float64(verts[0][2])}
	minU, maxU := dot3f(p0, u), dot3f(p0, u)
	minV, maxV := dot3f(p0, v), dot3f(p0, v)
	for _, p := range verts[1:] {
		pf := [3]float64{float64(p[0]), float64(p[1]), float64(p[2])}
		du, dv := dot3f(pf, u), dot3f(pf, v)
		minU, maxU = math.Min(minU, du), math.Max(maxU, du)
		minV, maxV = math.Min(minV, dv), math.Max(maxV, dv)
	}
	cu, cv := (minU+maxU)/2, (minV+maxV)/2
	return [3]float64{
		offsetMM*n[0] + cu*u[0] + cv*v[0],
		offsetMM*n[1] + cu*u[1] + cv*v[1],
		offsetMM*n[2] + cu*u[2] + cv*v[2],
	}
}

// dot3f is the dot product of two float64 3-vectors.
func dot3f(a, b [3]float64) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
