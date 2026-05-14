package cellslicer

import (
	"math"

	clipper "github.com/ctessum/go.clipper"
)

// FootprintLoop is one outer or hole loop inside a Footprint. Points
// are CCW for outers / CW for holes after Clipper non-zero union,
// no closing duplicate.
type FootprintLoop struct {
	Points []Point2
	IsHole bool
	// MinX..MaxY is the XY bounding box of Points. Set by
	// computeBbox; consulted by Contains for an O(1) reject before
	// the O(N) ray cast.
	MinX, MinY, MaxX, MaxY float32
}

// Footprint is the XY region of a slab: the Clipper non-zero union
// of the bot-Z and top-Z contour loops. May be empty (no geometry in
// the slab), single component, or multiple disjoint components with
// hole loops nested inside outers.
type Footprint struct {
	Loops []FootprintLoop
}

// ComputeFootprint returns the union of the bot and top contour
// loops as a Footprint. Loops with fewer than 3 points are dropped.
func ComputeFootprint(bot, top []Loop) *Footprint {
	paths := make(clipper.Paths, 0, len(bot)+len(top))
	for _, l := range bot {
		if len(l.Points) >= 3 {
			paths = append(paths, loopToClipperPath(l))
		}
	}
	for _, l := range top {
		if len(l.Points) >= 3 {
			paths = append(paths, loopToClipperPath(l))
		}
	}
	if len(paths) == 0 {
		return &Footprint{}
	}
	c := clipper.NewClipper(clipper.IoNone)
	c.AddPaths(paths, clipper.PtSubject, true)
	tree, ok := c.Execute2(clipper.CtUnion, clipper.PftNonZero, clipper.PftNonZero)
	if !ok || tree == nil {
		return &Footprint{}
	}
	fp := &Footprint{}
	for _, child := range tree.Childs() {
		collectFootprintLoops(child, fp)
	}
	return fp
}

func collectFootprintLoops(node *clipper.PolyNode, fp *Footprint) {
	if node == nil {
		return
	}
	pts := clipperPathToPoints(node.Contour())
	if len(pts) >= 3 {
		loop := FootprintLoop{Points: pts, IsHole: node.IsHole()}
		loop.computeBbox()
		fp.Loops = append(fp.Loops, loop)
	}
	for _, child := range node.Childs() {
		collectFootprintLoops(child, fp)
	}
}

// OffsetFootprint shrinks (or grows) fp by distance mm. Negative for
// inward (outers shrink, holes grow). The resulting paths are re-
// unioned into a polytree so the hole/outer nesting stays consistent
// with ComputeFootprint's output.
func OffsetFootprint(fp *Footprint, distance float32) *Footprint {
	if len(fp.Loops) == 0 {
		return &Footprint{}
	}
	co := clipper.NewClipperOffset()
	co.AddPaths(footprintToClipperPaths(fp), clipper.JtMiter, clipper.EtClosedPolygon)
	deltaScaled := float64(distance) * clipperScale
	out := co.Execute(deltaScaled)
	if len(out) == 0 {
		return &Footprint{}
	}
	c := clipper.NewClipper(clipper.IoNone)
	c.AddPaths(out, clipper.PtSubject, true)
	tree, ok := c.Execute2(clipper.CtUnion, clipper.PftNonZero, clipper.PftNonZero)
	if !ok || tree == nil {
		return &Footprint{}
	}
	off := &Footprint{}
	for _, child := range tree.Childs() {
		collectFootprintLoops(child, off)
	}
	return off
}

// footprintToClipperPaths re-emits fp's loops as Clipper paths with
// canonical orientation (outers CCW, holes CW).
func footprintToClipperPaths(fp *Footprint) clipper.Paths {
	paths := make(clipper.Paths, 0, len(fp.Loops))
	for _, lp := range fp.Loops {
		isCCW := signedArea(lp.Points) > 0
		wantCCW := !lp.IsHole
		paths = append(paths, point2sToClipperPathOriented(lp.Points, isCCW == wantCCW))
	}
	return paths
}

func (fl *FootprintLoop) computeBbox() {
	if len(fl.Points) == 0 {
		return
	}
	fl.MinX, fl.MaxX = fl.Points[0][0], fl.Points[0][0]
	fl.MinY, fl.MaxY = fl.Points[0][1], fl.Points[0][1]
	for _, p := range fl.Points[1:] {
		if p[0] < fl.MinX {
			fl.MinX = p[0]
		}
		if p[0] > fl.MaxX {
			fl.MaxX = p[0]
		}
		if p[1] < fl.MinY {
			fl.MinY = p[1]
		}
		if p[1] > fl.MaxY {
			fl.MaxY = p[1]
		}
	}
}

// Contains returns true if (x, y) is inside this footprint loop's
// polygon, using a bbox reject then even-odd ray cast.
func (fl *FootprintLoop) Contains(x, y float32) bool {
	if x < fl.MinX || x > fl.MaxX || y < fl.MinY || y > fl.MaxY {
		return false
	}
	return pointInPolygon(fl.Points, x, y)
}

// Contains returns true if (x, y) is inside fp (odd number of
// containing loops).
func (fp *Footprint) Contains(x, y float32) bool {
	n := 0
	for i := range fp.Loops {
		if fp.Loops[i].Contains(x, y) {
			n++
		}
	}
	return n%2 == 1
}

// Bounds returns the XY bounding box of all loops, ok=false if empty.
func (fp *Footprint) Bounds() (minX, minY, maxX, maxY float32, ok bool) {
	if len(fp.Loops) == 0 {
		return 0, 0, 0, 0, false
	}
	minX, minY = fp.Loops[0].MinX, fp.Loops[0].MinY
	maxX, maxY = fp.Loops[0].MaxX, fp.Loops[0].MaxY
	for i := 1; i < len(fp.Loops); i++ {
		l := &fp.Loops[i]
		if l.MinX < minX {
			minX = l.MinX
		}
		if l.MinY < minY {
			minY = l.MinY
		}
		if l.MaxX > maxX {
			maxX = l.MaxX
		}
		if l.MaxY > maxY {
			maxY = l.MaxY
		}
	}
	return minX, minY, maxX, maxY, true
}

// pointInPolygon is even-odd ray cast along +X.
func pointInPolygon(pts []Point2, x, y float32) bool {
	inside := false
	n := len(pts)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		if (pts[i][1] > y) != (pts[j][1] > y) {
			xIntersect := (pts[j][0]-pts[i][0])*(y-pts[i][1])/(pts[j][1]-pts[i][1]) + pts[i][0]
			if x < xIntersect {
				inside = !inside
			}
		}
	}
	return inside
}

// polyBounds returns the XY bounding box of pts. Caller must ensure
// pts is non-empty.
func polyBounds(pts []Point2) (minX, minY, maxX, maxY float32) {
	minX, minY = pts[0][0], pts[0][1]
	maxX, maxY = pts[0][0], pts[0][1]
	for _, p := range pts[1:] {
		if p[0] < minX {
			minX = p[0]
		}
		if p[0] > maxX {
			maxX = p[0]
		}
		if p[1] < minY {
			minY = p[1]
		}
		if p[1] > maxY {
			maxY = p[1]
		}
	}
	return
}

// hypot is a float32 convenience wrapper. Stays close to the
// prototype's math.Hypot usage so floating-point behavior matches.
func hypot(dx, dy float32) float32 {
	return float32(math.Hypot(float64(dx), float64(dy)))
}
