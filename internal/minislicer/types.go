// Package minislicer is a prototype color-by-layer pipeline that
// slices a model into per-layer 2D contours, partitions each contour
// into sections of bounded arc length, and dithers across the section
// graph. It exists as a sibling to the voxel-based remesher.
package minislicer

// Point2 is an XY point in mesh units.
type Point2 [2]float32

// Loop is a closed 2D polygon at a single Z height. Points are NOT
// duplicated at end-of-loop. SignedArea > 0 means CCW; < 0 means CW.
//
// IsHole is set after slicing by a nesting-depth pass: a loop whose
// centroid falls inside an odd number of other loops in the same
// layer is a hole. The slicer's segment-chaining doesn't enforce a
// canonical winding, so SignedArea alone can't tell outer from hole
// — IsHole is the load-bearing classifier downstream stages branch
// on.
type Loop struct {
	Points     []Point2
	Z          float32
	SignedArea float32
	IsHole     bool
}

// Layer is the cross-section of the model at a single Z height.
type Layer struct {
	Z         float32
	LayerIdx  int
	Loops     []Loop
}

// SectionKind tags a Section as a perimeter-wall ribbon or as a
// top / bottom cap tile. Cap tiles cover horizontal exposed surfaces
// (the topmost layer's top, the bottommost layer's bottom — and, in
// the future, "step" exposures between layers of different
// footprints). The default zero value is KindRibbon so existing
// constructors stay valid.
type SectionKind uint8

const (
	KindRibbon SectionKind = iota
	KindCapTop
	KindCapBottom
)

// Section is one piece of a Loop's perimeter (Kind == KindRibbon)
// or one tile of a layer's exposed top/bottom cap
// (Kind == KindCapTop / KindCapBottom).
//
// LayerIdx + LoopIdx + Index uniquely identify the section. Mid is
// the XY point at the section's midpoint — for ribbon sections, the
// arc midpoint; for cap tiles, the tile's center. Z is the 3D Z
// coordinate where color is sampled.
type Section struct {
	LayerIdx int
	LoopIdx  int
	Index    int // index within the loop's section list
	Kind     SectionKind

	Mid Point2  // XY: arc midpoint (ribbon) or tile center (cap)
	Z   float32 // 3D Z for color sampling

	// Ribbon-only.
	StartArc float32
	EndArc   float32
	Length   float32

	// Cap-only. CapBoundsXY = (minX, minY, maxX, maxY). TileCol/Row
	// are the tile's grid coordinates within the cap, used by
	// BuildSectionGraph for 4-neighbor adjacency.
	CapBoundsXY [4]float32
	TileCol     int
	TileRow     int
}
