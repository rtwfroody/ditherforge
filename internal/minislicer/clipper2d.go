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

// loopsToClipperPaths converts a layer's loops to Clipper paths in
// int coords. Loops with fewer than 3 points are skipped. The
// orientation of each loop is preserved; downstream Boolean ops use
// even-odd fill, so winding doesn't affect inside/outside.
func loopsToClipperPaths(loops []Loop) clipper.Paths {
	if len(loops) == 0 {
		return nil
	}
	out := make(clipper.Paths, 0, len(loops))
	for i := range loops {
		l := &loops[i]
		if len(l.Points) < 3 {
			continue
		}
		path := make(clipper.Path, 0, len(l.Points))
		for _, p := range l.Points {
			x := clipper.CInt(math.Round(float64(p[0]) * clipperScale))
			y := clipper.CInt(math.Round(float64(p[1]) * clipperScale))
			path = append(path, &clipper.IntPoint{X: x, Y: y})
		}
		out = append(out, path)
	}
	return out
}

// clipperOp runs a single Boolean op on two Clipper Paths sets with
// even-odd fill. Returns the result paths, or nil on failure / empty
// inputs.
func clipperOp(subj, clip clipper.Paths, op clipper.ClipType) clipper.Paths {
	if len(subj) == 0 {
		return nil
	}
	c := clipper.NewClipper(clipper.IoNone)
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
	c := clipper.NewClipper(clipper.IoNone)
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
// layer's slab face that are NOT covered by neighbor — i.e. air-
// facing at that Z. Pass neighbor == nil for the topmost/bottommost
// layer (no neighbor → the whole layer footprint is exposed).
func exposedCapRegions(layer *Layer, neighbor *Layer) []CapRegion {
	subj := loopsToClipperPaths(layer.Loops)
	if len(subj) == 0 {
		return nil
	}
	if neighbor == nil {
		return clipperPathsToRegions(subj)
	}
	nbr := loopsToClipperPaths(neighbor.Loops)
	if len(nbr) == 0 {
		return clipperPathsToRegions(subj)
	}
	diff := clipperOp(subj, nbr, clipper.CtDifference)
	if len(diff) == 0 {
		return nil
	}
	return clipperPathsToRegions(diff)
}
