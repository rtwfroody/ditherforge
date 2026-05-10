// Package minislicer is a prototype color-by-layer pipeline that
// slices a model into per-layer 2D contours, partitions each contour
// into sections of bounded arc length, and dithers across the section
// graph. It exists as a sibling to the voxel-based remesher.
package minislicer

// Point2 is an XY point in mesh units.
type Point2 [2]float32

// Loop is a closed 2D polygon at a single Z height. Points are NOT
// duplicated at end-of-loop. SignedArea > 0 means CCW (outer
// boundary); < 0 means CW (a hole).
type Loop struct {
	Points     []Point2
	Z          float32
	SignedArea float32
}

// Layer is the cross-section of the model at a single Z height.
type Layer struct {
	Z         float32
	LayerIdx  int
	Loops     []Loop
}

// Section is one piece of a Loop, defined by an arc-length range.
// Arc parameter is cumulative perimeter from the loop's first point.
//
// LayerIdx + LoopIdx + Index uniquely identify the section. Mid is
// the XY point at the midpoint of the arc range (used for color
// sampling and adjacency).
type Section struct {
	LayerIdx int
	LoopIdx  int
	Index    int     // index within the loop's section list
	StartArc float32 // arc-length [m] from loop's point[0] to section start
	EndArc   float32 // arc-length to section end (cyclic; EndArc may wrap)
	Length   float32 // EndArc - StartArc, accounting for wrap
	Mid      Point2  // XY at section midpoint
	Z        float32 // copied from layer
}
