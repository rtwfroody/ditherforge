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
// boundary vertex falls inside an odd number of other loops in the
// same layer is a hole. The slicer's segment-chaining doesn't
// enforce a canonical winding, so SignedArea alone can't tell outer
// from hole — IsHole is the load-bearing classifier downstream
// stages branch on.
//
// HasHoleChild applies to outer loops only: true when at least one
// hole loop in the same layer is contained in this outer. The mesh
// emitter uses it to skip the simple fan-triangulated cap on outers
// that need a polygon-with-holes triangulation; outers without any
// hole children still get the fan and stay watertight.
type Loop struct {
	Points []Point2
	// EdgeTris[i] is the index of the source triangle in the model
	// for the edge Points[i] → Points[(i+1) % len(Points)] —
	// i.e. the triangle whose intersection with the slicing plane
	// produced that edge. -1 means unknown (e.g. after a merge
	// that lost provenance). Used by partitionLoop to tag each
	// section with the triangle it really came from, so
	// downstream color sampling can call voxel.SampleByTriangle
	// instead of nearest-tri (which picks unrelated triangles from
	// adjacent objects).
	EdgeTris     []int32
	Z            float32
	SignedArea   float32
	IsHole       bool
	HasHoleChild bool
	// XY bbox of Points, populated by RefreshDerived. The cap
	// partition's per-cell 5-point exposure test and the cap
	// emitter's per-corner exposure test both call Contains
	// across thousands of points per layer; the bbox lets the
	// common "point nowhere near this loop" case bail out in O(1)
	// before the O(N) ray cast.
	MinX, MinY, MaxX, MaxY float32
}

// RefreshDerived recomputes SignedArea and the XY bbox from
// Points. Call after constructing a Loop literal or mutating
// Points in place.
func (l *Loop) RefreshDerived() {
	l.SignedArea = signedArea(l.Points)
	if len(l.Points) == 0 {
		l.MinX, l.MinY, l.MaxX, l.MaxY = 0, 0, 0, 0
		return
	}
	l.MinX, l.MaxX = l.Points[0][0], l.Points[0][0]
	l.MinY, l.MaxY = l.Points[0][1], l.Points[0][1]
	for _, p := range l.Points[1:] {
		if p[0] < l.MinX {
			l.MinX = p[0]
		}
		if p[0] > l.MaxX {
			l.MaxX = p[0]
		}
		if p[1] < l.MinY {
			l.MinY = p[1]
		}
		if p[1] > l.MaxY {
			l.MaxY = p[1]
		}
	}
}

// Contains returns true when (x, y) is inside the closed polygon
// l.Points, using even-odd ray casting along +X. Bbox-rejects far
// points in O(1). RefreshDerived must have populated the bbox.
func (l *Loop) Contains(x, y float32) bool {
	if x < l.MinX || x > l.MaxX || y < l.MinY || y > l.MaxY {
		return false
	}
	return pointInPolygon(l.Points, x, y)
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

	// SrcTriIdx is the model triangle that produced the slicer
	// segment containing this ribbon section's midpoint, or -1
	// for cap tiles / sections with no recoverable source. Used
	// by SampleSectionColors to bypass nearest-tri lookup.
	SrcTriIdx int32

	// SrcTriNormalZ is the Z component of the source triangle's
	// unit normal (in mesh coords). Used by the earcut cap
	// colorer to prefer ribbons whose source triangle faces in
	// the same direction as the cap (upward for top caps,
	// downward for bottom caps): the cap material is bounded
	// above (or below) by a roughly upward-facing (or
	// downward-facing) surface, so picking a ribbon whose
	// source triangle matches that orientation gives a color
	// from the right surface region.
	//
	// In particular, near a vertical cut surface inside a
	// solid (e.g. the salmon-colored interior of a sliced
	// fish), the cut surface's triangles have ~zero normal_z;
	// without this filter the cap's nearest-XY ribbon search
	// can pick a cut-surface ribbon and paint a dome cap
	// salmon — visible as horizontal stripes in front/side
	// renderings.
	//
	// 0 (the zero value) is a valid value for a vertical-wall
	// triangle; we don't treat it as "missing." For sections
	// without a recoverable source (SrcTriIdx<0) leave at 0.
	SrcTriNormalZ float32
}
