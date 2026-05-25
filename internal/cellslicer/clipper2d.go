package cellslicer

import (
	"math"

	clipper "github.com/ctessum/go.clipper"
)

// clipperScale converts mm (float32) to Clipper's integer
// coordinates. 1000 → 1 unit = 1 µm. Must match minislicer's scale so
// that points sliced there and clipped here align on the integer
// grid.
const clipperScale = 1000.0

const invClipperScale = 1.0 / clipperScale

// loopToClipperPath converts a Loop into a Clipper path,
// reorienting so outer loops are CCW and holes are CW (Clipper's
// PtSubject convention with PftNonZero fill).
func loopToClipperPath(loop Loop) clipper.Path {
	pts := loop.Points
	isCCW := loop.SignedArea > 0
	wantCCW := !loop.IsHole
	return point2sToClipperPathOriented(pts, isCCW == wantCCW)
}

// pointsToClipperPath converts a closed XY polyline (no closing
// duplicate) into a Clipper path with the orientation of the input.
func pointsToClipperPath(pts []Point2) clipper.Path {
	path := make(clipper.Path, len(pts))
	for i, p := range pts {
		path[i] = &clipper.IntPoint{
			X: clipper.CInt(math.Round(float64(p[0]) * clipperScale)),
			Y: clipper.CInt(math.Round(float64(p[1]) * clipperScale)),
		}
	}
	return path
}

// point2sToClipperPathOriented converts pts to a Clipper path; if
// keepDir is false it reverses the order while building.
func point2sToClipperPathOriented(pts []Point2, keepDir bool) clipper.Path {
	n := len(pts)
	path := make(clipper.Path, 0, n)
	if keepDir {
		for _, p := range pts {
			path = append(path, &clipper.IntPoint{
				X: clipper.CInt(math.Round(float64(p[0]) * clipperScale)),
				Y: clipper.CInt(math.Round(float64(p[1]) * clipperScale)),
			})
		}
	} else {
		for i := n - 1; i >= 0; i-- {
			p := pts[i]
			path = append(path, &clipper.IntPoint{
				X: clipper.CInt(math.Round(float64(p[0]) * clipperScale)),
				Y: clipper.CInt(math.Round(float64(p[1]) * clipperScale)),
			})
		}
	}
	return path
}

// clipperPathToPoints converts a Clipper path back to mm-coordinate
// XY points, dropping any closing duplicate.
func clipperPathToPoints(path clipper.Path) []Point2 {
	out := make([]Point2, 0, len(path))
	for _, ip := range path {
		p := Point2{
			float32(float64(ip.X) * invClipperScale),
			float32(float64(ip.Y) * invClipperScale),
		}
		if n := len(out); n > 0 && out[n-1] == p {
			continue
		}
		out = append(out, p)
	}
	if n := len(out); n > 1 && out[0] == out[n-1] {
		out = out[:n-1]
	}
	return out
}

// int2D is a Clipper-integer 2D point — mirror of int3D in
// clip2d_subdivide.go but for XY-only use. Two independently rounded
// copies of the same float32 XY coordinate bucket to the same int2D
// (1µm grid), so an edge map keyed by int2D pairs reliably identifies
// shared cell-Outer edges across cells without float comparison
// pitfalls.
type int2D struct {
	X, Y int64
}

func int2DOf(p Point2) int2D {
	return int2D{
		X: int64(math.Round(float64(p[0]) * clipperScale)),
		Y: int64(math.Round(float64(p[1]) * clipperScale)),
	}
}

// clipPolygonToFootprint intersects a single polygon with the
// footprint via Clipper non-zero fill. Returns the (one or more)
// component polygons of the result. Used by both ring-cell trapezoid
// clipping and hex-cell tile clipping.
func clipPolygonToFootprint(poly []Point2, fp *Footprint) [][]Point2 {
	c := clipper.NewClipper(clipper.IoNone)
	c.AddPaths(clipper.Paths{pointsToClipperPath(poly)}, clipper.PtSubject, true)
	c.AddPaths(footprintToClipperPaths(fp), clipper.PtClip, true)
	result, ok := c.Execute1(clipper.CtIntersection, clipper.PftNonZero, clipper.PftNonZero)
	if !ok || len(result) == 0 {
		return nil
	}
	out := make([][]Point2, 0, len(result))
	for _, path := range result {
		pts := clipperPathToPoints(path)
		if len(pts) >= 3 {
			out = append(out, pts)
		}
	}
	return out
}
