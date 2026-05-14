package cellslicer

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
type Loop struct {
	Points     []Point2
	Z          float32
	SignedArea float32
	IsHole     bool
	// XY bbox of Points, populated by RefreshDerived. Lets
	// Contains bail out in O(1) before the O(N) ray cast for
	// points outside the loop's bbox.
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
	Z        float32
	LayerIdx int
	Loops    []Loop
}
