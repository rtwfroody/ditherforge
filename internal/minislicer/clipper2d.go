package minislicer

import (
	"math"

	clipper "github.com/ctessum/go.clipper"
)

// clipperScale converts mm (float32) to Clipper's int coordinates.
// 1000 → 1 unit = 1µm, comfortably below any feature size we care
// about and well inside Clipper1's int64 safe range for any sane
// model dimension.
const clipperScale = 1000.0

// CapRegion is one polygon-with-holes piece of a cap surface.
// Outer + Holes are ready to feed directly to Earcut.
type CapRegion struct {
	Outer []Point2
	Holes [][]Point2
}

// pointSetsToClipperPaths converts a list of polygon vertex
// sequences to Clipper paths in int coords. Polygons with fewer
// than 3 points are skipped. Used by the cap emitter to feed
// already-wall-conforming subdivided loops to Clipper, so
// downstream cap geometry shares vertex sets with the wall on
// their common boundary.
func pointSetsToClipperPaths(loops [][]Point2) clipper.Paths {
	if len(loops) == 0 {
		return nil
	}
	out := make(clipper.Paths, 0, len(loops))
	for _, pts := range loops {
		if len(pts) < 3 {
			continue
		}
		path := make(clipper.Path, 0, len(pts))
		for _, p := range pts {
			x := clipper.CInt(math.Round(float64(p[0]) * clipperScale))
			y := clipper.CInt(math.Round(float64(p[1]) * clipperScale))
			path = append(path, &clipper.IntPoint{X: x, Y: y})
		}
		out = append(out, path)
	}
	return out
}

// loopsToClipperPaths is the convenience pointSetsToClipperPaths
// wrapper for the raw Loop slice.
func loopsToClipperPaths(loops []Loop) clipper.Paths {
	if len(loops) == 0 {
		return nil
	}
	pts := make([][]Point2, 0, len(loops))
	for i := range loops {
		pts = append(pts, loops[i].Points)
	}
	return pointSetsToClipperPaths(pts)
}

// clipperOp runs a single Boolean op on two Clipper Paths sets with
// even-odd fill. Returns the result paths, or nil on failure / empty
// inputs.
//
// IoPreserveCollinear keeps wall-subdivision vertices that lie
// mid-edge from being silently dropped. Without it the cap's outer
// boundary would have fewer vertices than the wall's top edge at
// the same slab Z, recreating T-junctions and the camera-rotates-
// then-shimmers artifact this rewrite is meant to eliminate.
func clipperOp(subj, clip clipper.Paths, op clipper.ClipType) clipper.Paths {
	if len(subj) == 0 {
		return nil
	}
	c := clipper.NewClipper(clipper.IoPreserveCollinear)
	c.AddPaths(subj, clipper.PtSubject, true)
	if len(clip) > 0 {
		c.AddPaths(clip, clipper.PtClip, true)
	}
	out, ok := c.Execute1(op, clipper.PftEvenOdd, clipper.PftEvenOdd)
	if !ok {
		return nil
	}
	return out
}

// clipperPathsToRegions runs the given paths through a Clipper
// union (even-odd) to build a proper PolyTree, then walks it to
// extract one CapRegion per non-hole node with its direct hole
// children attached.
func clipperPathsToRegions(paths clipper.Paths) []CapRegion {
	if len(paths) == 0 {
		return nil
	}
	c := clipper.NewClipper(clipper.IoPreserveCollinear)
	c.AddPaths(paths, clipper.PtSubject, true)
	tree, ok := c.Execute2(clipper.CtUnion, clipper.PftEvenOdd, clipper.PftEvenOdd)
	if !ok || tree == nil {
		return nil
	}
	var regions []CapRegion
	for _, child := range tree.Childs() {
		collectCapRegions(child, &regions)
	}
	return regions
}

// collectCapRegions walks the Clipper PolyTree starting at a
// non-hole node. The non-hole node's contour becomes the region's
// Outer; its direct hole children's contours become Holes.
// Grandchildren (non-hole again — a nested island inside a hole)
// are themselves new top-level regions: we recurse into them.
func collectCapRegions(node *clipper.PolyNode, out *[]CapRegion) {
	if node == nil {
		return
	}
	if node.IsHole() {
		for _, gc := range node.Childs() {
			collectCapRegions(gc, out)
		}
		return
	}
	region := CapRegion{Outer: clipperPathToPoints(node.Contour())}
	if len(region.Outer) < 3 {
		return
	}
	for _, child := range node.Childs() {
		if child.IsHole() {
			h := clipperPathToPoints(child.Contour())
			if len(h) >= 3 {
				region.Holes = append(region.Holes, h)
			}
			for _, gc := range child.Childs() {
				collectCapRegions(gc, out)
			}
		} else {
			collectCapRegions(child, out)
		}
	}
	*out = append(*out, region)
}

// clipperPathToPoints converts a Clipper Path back to Point2 in mm.
// Drops consecutive duplicates (Clipper can emit them at edge
// boundaries) since earcut treats coincident vertices as
// degenerate.
func clipperPathToPoints(p clipper.Path) []Point2 {
	if len(p) == 0 {
		return nil
	}
	out := make([]Point2, 0, len(p))
	const inv = 1.0 / clipperScale
	for _, ip := range p {
		pt := Point2{float32(float64(ip.X) * inv), float32(float64(ip.Y) * inv)}
		if n := len(out); n > 0 && out[n-1] == pt {
			continue
		}
		out = append(out, pt)
	}
	if n := len(out); n > 1 && out[0] == out[n-1] {
		out = out[:n-1]
	}
	return out
}

// exposedCapRegions returns the polygon-with-holes pieces of the
// layer's slab face that are NOT covered by the neighbor. The
// caller supplies subdivided loop point-sets for both the layer
// and the neighbor; passing the wall-emitter's exact vertex
// sequence here makes cap and wall share vertex sets along their
// common boundary, eliminating T-junction cracks the camera can
// see background through. neighbor == nil → full footprint exposed
// (topmost or bottommost layer).
func exposedCapRegions(layerLoops [][]Point2, neighborLoops [][]Point2) []CapRegion {
	subj := pointSetsToClipperPaths(layerLoops)
	if len(subj) == 0 {
		return nil
	}
	if len(neighborLoops) == 0 {
		return clipperPathsToRegions(subj)
	}
	nbr := pointSetsToClipperPaths(neighborLoops)
	if len(nbr) == 0 {
		return clipperPathsToRegions(subj)
	}
	diff := clipperOp(subj, nbr, clipper.CtDifference)
	if len(diff) == 0 {
		return nil
	}
	return clipperPathsToRegions(diff)
}
