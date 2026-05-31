package cellslicer

import (
	"fmt"
	"strconv"
	"strings"
)

// DebugSVGOptions controls the per-slab cell visualization rendered
// by RenderSlabDebugSVG. Zero values pick sensible defaults.
type DebugSVGOptions struct {
	// CellSizeMM is the slab's cell pitch in mm; used to pick sensible
	// default bbox padding and contour stroke widths.
	CellSizeMM float32
	// PadMM is the bbox padding in mm. 0 → CellSizeMM (or 1 if that's 0).
	PadMM float32
	// FillBackgroundWhite renders an opaque white background rect
	// inside the viewBox. When false, the background is transparent.
	FillBackgroundWhite bool
	// DrawEdges adds a stroked path of every cell boundary on top of
	// the fills, so neighboring same-color cells remain visible.
	DrawEdges bool
	// EdgeWidthPx is the edge stroke width in CSS pixels. The edge path
	// uses vector-effect=non-scaling-stroke, so the width is measured in
	// the SVG element's pixel space (constant on screen at any zoom),
	// NOT in viewBox mm. 0 → 1px (a thin hairline). A mm-scale value
	// here renders sub-pixel and is invisible — see the cs/40 bug.
	EdgeWidthPx float32
	// MissingFill is the fill color for cells with no sample (or
	// Alpha=false). Default "#b4b4b4" — same grey the PNG path used.
	MissingFill string
	// DrawFootprint paints the slab's computed footprint as an opaque
	// base fill beneath the cells, then draws everything else on top in
	// a slightly-transparent group. Coverage gaps between the cells and
	// the footprint boundary show through as the pure base color.
	DrawFootprint bool
	// FootprintFill is the footprint base-fill color. Default "#ff00ff"
	// (magenta — contrasts with most sampled colors).
	FootprintFill string
	// DrawContours overlays the raw bot-Z and top-Z slice contours that
	// were unioned to make the footprint, so you can see when they are
	// nested (annular surface) vs. nearly coincident (wall slab).
	DrawContours bool
	// BotContourStroke / TopContourStroke override the contour colors.
	// Defaults: cyan ("#00b7eb") for bot, orange ("#ff7f00") for top.
	BotContourStroke string
	TopContourStroke string
	// HighlightUncovered fills the area in the footprint that no cell's
	// Outer polygon covers, so partition coverage gaps are visible.
	HighlightUncovered bool
	// UncoveredFill is the fill color for uncovered areas. Default
	// "#ff0000" (opaque red).
	UncoveredFill string
}

// RenderSlabDebugSVG renders a single slab as an SVG document and
// returns the markup as a string. Returns "" when the slab has no
// footprint geometry. Cells with the same exact RGB are folded into
// one <path>, keeping the DOM size proportional to the number of
// distinct sampled colors rather than the number of cells.
func RenderSlabDebugSVG(slabs []Slab, samples []CellSample, slabIdx int, opt DebugSVGOptions) string {
	if slabIdx < 0 || slabIdx >= len(slabs) {
		return ""
	}
	s := &slabs[slabIdx]
	if s.Footprint == nil || len(s.Footprint.Loops) == 0 {
		return ""
	}
	minX, minY, maxX, maxY, ok := s.Footprint.Bounds()
	if !ok {
		return ""
	}

	pad := opt.PadMM
	if pad <= 0 {
		pad = opt.CellSizeMM
		if pad <= 0 {
			pad = 1
		}
	}
	minX -= pad
	minY -= pad
	maxX += pad
	maxY += pad
	w := maxX - minX
	h := maxY - minY
	if w <= 0 || h <= 0 {
		return ""
	}

	// Edge stroke is a non-scaling (pixel-space) width, so this is in
	// CSS px, not mm. 1px is a crisp hairline at any zoom.
	edgeW := opt.EdgeWidthPx
	if edgeW <= 0 {
		edgeW = 1
	}
	missingFill := opt.MissingFill
	if missingFill == "" {
		missingFill = "#b4b4b4"
	}

	cellColor := make(map[int][3]uint8, len(s.Cells))
	for _, sp := range samples {
		if sp.SlabIdx != slabIdx || !sp.Alpha {
			continue
		}
		cellColor[sp.CellIdx] = sp.Color
	}

	type bucket struct {
		hex     string
		cellIdx []int
	}
	byColor := make(map[[3]uint8]*bucket, 64)
	var missing []int
	order := make([][3]uint8, 0, 64)
	for idx := range s.Cells {
		rgb, hasColor := cellColor[idx]
		if !hasColor {
			missing = append(missing, idx)
			continue
		}
		b, ok := byColor[rgb]
		if !ok {
			b = &bucket{hex: fmt.Sprintf("#%02x%02x%02x", rgb[0], rgb[1], rgb[2])}
			byColor[rgb] = b
			order = append(order, rgb)
		}
		b.cellIdx = append(b.cellIdx, idx)
	}

	var sb strings.Builder
	// Conservative starting capacity: ~80 chars per cell.
	sb.Grow(len(s.Cells) * 80)

	// Y is flipped via the outer <g> so world Y-up renders north-up.
	// viewBox is in world coords (mm); the consumer sizes the <svg>
	// element with CSS, so we leave width/height off for crisp scaling.
	fmt.Fprintf(&sb,
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="%s %s %s %s" preserveAspectRatio="xMidYMid meet">`,
		f(minX), f(-maxY), f(w), f(h),
	)
	if opt.FillBackgroundWhite {
		fmt.Fprintf(&sb, `<rect x="%s" y="%s" width="%s" height="%s" fill="#ffffff"/>`,
			f(minX), f(-maxY), f(w), f(h))
	}
	sb.WriteString(`<g transform="scale(1,-1)">`)

	// Footprint base fill: the entire footprint is painted in a single
	// distinct color first, then everything else is drawn on top inside
	// a slightly-transparent group. Coverage gaps show through as the
	// pure base color, and covered cells pick up a subtle wash that
	// marks them as lying inside the footprint. even-odd respects holes.
	dimmed := opt.DrawFootprint && s.Footprint != nil && len(s.Footprint.Loops) > 0
	if dimmed {
		fpFill := opt.FootprintFill
		if fpFill == "" {
			fpFill = "#ff00ff"
		}
		fmt.Fprintf(&sb,
			`<path fill="%s" fill-rule="evenodd" shape-rendering="crispEdges" d="`,
			fpFill,
		)
		appendFootprintPaths(&sb, s.Footprint)
		sb.WriteString(`"/>`)
		// Everything below renders slightly transparent so the base
		// footprint color tints through.
		sb.WriteString(`<g opacity="0.85">`)
	}

	// Uncovered region: footprint minus union of cell.Outer polygons,
	// using SVG's even-odd fill rule. With all polygons CCW the rule
	// XORs them, so points inside fp but inside zero cells stay
	// "odd" (filled), everything else becomes "even" (transparent).
	if opt.HighlightUncovered {
		fill := opt.UncoveredFill
		if fill == "" {
			fill = "#ff0000"
		}
		fmt.Fprintf(&sb,
			`<path fill="%s" fill-rule="evenodd" shape-rendering="crispEdges" d="`,
			fill,
		)
		appendFootprintPaths(&sb, s.Footprint)
		all := make([]int, len(s.Cells))
		for i := range all {
			all[i] = i
		}
		if len(s.Footprint.Loops) > 0 && len(s.Cells) > 0 {
			sb.WriteByte(' ')
		}
		appendCellPaths(&sb, s.Cells, all)
		sb.WriteString(`"/>`)
	}

	// Filled paths, one per color, with shape-rendering hint to keep
	// adjacent cells visually flush (no antialias gaps).
	if len(missing) > 0 {
		sb.WriteString(`<path shape-rendering="crispEdges" fill="`)
		sb.WriteString(missingFill)
		sb.WriteString(`" d="`)
		appendCellPaths(&sb, s.Cells, missing)
		sb.WriteString(`"/>`)
	}
	for _, rgb := range order {
		b := byColor[rgb]
		sb.WriteString(`<path shape-rendering="crispEdges" fill="`)
		sb.WriteString(b.hex)
		sb.WriteString(`" d="`)
		appendCellPaths(&sb, s.Cells, b.cellIdx)
		sb.WriteString(`"/>`)
	}

	if opt.DrawEdges {
		// Single stroked path over every cell, fill=none. Shared edges
		// get double-stroked, which at typical edge widths is invisible.
		// Thin and translucent so the cell grid is legible without
		// burying the sampled fills underneath.
		fmt.Fprintf(&sb,
			`<path fill="none" stroke="#000000" stroke-opacity="0.5" stroke-width="%s" vector-effect="non-scaling-stroke" d="`,
			f(edgeW),
		)
		all := make([]int, len(s.Cells))
		for i := range all {
			all[i] = i
		}
		appendCellPaths(&sb, s.Cells, all)
		sb.WriteString(`"/>`)

		// Overlay: cells' outer-boundary edges (open-ended at clip
		// time — see Cell.OuterEdgeOpen). Rendered in red, 2×
		// the base edge width so they're visibly thicker than the
		// black underlay. Lets a reader of the layer dump see at a
		// glance which edges absorb source geometry that nudges past
		// the partition outline. No-op when no cell has the field
		// populated (legacy PartitionSlab path).
		if anyBoundaryEdgeTagged(s.Cells) {
			fmt.Fprintf(&sb,
				`<path fill="none" stroke="#e02020" stroke-width="%s" stroke-linecap="round" vector-effect="non-scaling-stroke" d="`,
				f(edgeW*2),
			)
			appendOuterBoundaryEdgePaths(&sb, s.Cells)
			sb.WriteString(`"/>`)
		}
	}

	if opt.DrawContours {
		botStroke := opt.BotContourStroke
		if botStroke == "" {
			botStroke = "#00b7eb"
		}
		topStroke := opt.TopContourStroke
		if topStroke == "" {
			topStroke = "#ff7f00"
		}
		cs := opt.CellSizeMM
		if cs <= 0 {
			cs = 1
		}
		cw := cs / 6
		if s.BotLayer != nil {
			fmt.Fprintf(&sb,
				`<path fill="none" stroke="%s" stroke-width="%s" d="`,
				botStroke, f(cw),
			)
			appendLoopPaths(&sb, s.BotLayer.Loops)
			sb.WriteString(`"/>`)
		}
		if s.TopLayer != nil {
			fmt.Fprintf(&sb,
				`<path fill="none" stroke="%s" stroke-width="%s" d="`,
				topStroke, f(cw),
			)
			appendLoopPaths(&sb, s.TopLayer.Loops)
			sb.WriteString(`"/>`)
		}
	}

	if dimmed {
		// Close the slightly-transparent overlay group opened above the
		// footprint base fill.
		sb.WriteString(`</g>`)
	}

	sb.WriteString(`</g></svg>`)
	return sb.String()
}

// appendLoopPaths writes each Loop's points as a closed SVG subpath.
// Used by the contour overlays to draw the raw bot/top slicer output.
func appendLoopPaths(sb *strings.Builder, loops []Loop) {
	for i, lp := range loops {
		pts := lp.Points
		if len(pts) < 3 {
			continue
		}
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteByte('M')
		sb.WriteString(f(pts[0][0]))
		sb.WriteByte(',')
		sb.WriteString(f(pts[0][1]))
		for j := 1; j < len(pts); j++ {
			sb.WriteByte('L')
			sb.WriteString(f(pts[j][0]))
			sb.WriteByte(',')
			sb.WriteString(f(pts[j][1]))
		}
		sb.WriteByte('Z')
	}
}

// appendFootprintPaths writes each footprint loop (outer or hole) as a
// closed subpath. Holes are drawn the same as outers — for a stroked
// overlay we only care about the boundary; fill-rule is irrelevant.
func appendFootprintPaths(sb *strings.Builder, fp *Footprint) {
	if fp == nil {
		return
	}
	for i, lp := range fp.Loops {
		pts := lp.Points
		if len(pts) < 3 {
			continue
		}
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteByte('M')
		sb.WriteString(f(pts[0][0]))
		sb.WriteByte(',')
		sb.WriteString(f(pts[0][1]))
		for j := 1; j < len(pts); j++ {
			sb.WriteByte('L')
			sb.WriteString(f(pts[j][0]))
			sb.WriteByte(',')
			sb.WriteString(f(pts[j][1]))
		}
		sb.WriteByte('Z')
	}
}

// anyBoundaryEdgeTagged reports whether at least one cell has a
// populated OuterEdgeOpen slice with a true entry. Lets the
// SVG renderer skip emitting an empty outer-edge overlay (and the
// associated path tag) when nothing is tagged — keeps legacy
// PartitionSlab debug output free of an empty <path>.
func anyBoundaryEdgeTagged(cells []Cell) bool {
	for ci := range cells {
		flags := cells[ci].OuterEdgeOpen
		for _, f := range flags {
			if f {
				return true
			}
		}
	}
	return false
}

// appendOuterBoundaryEdgePaths emits one "M a L b" subpath per cell
// edge where OuterEdgeOpen[k] is true. Caller wraps in a single
// stroked <path> element. Each edge becomes its own M/L pair (rather
// than joining into long polylines) because adjacent outer-boundary
// edges in CCW order are not necessarily contiguous on the page —
// stair-step polyomino cells often have non-adjacent runs of outer
// edges.
func appendOuterBoundaryEdgePaths(sb *strings.Builder, cells []Cell) {
	first := true
	for ci := range cells {
		pts := cells[ci].Outer
		flags := cells[ci].OuterEdgeOpen
		if len(flags) != len(pts) {
			continue
		}
		n := len(pts)
		for k := 0; k < n; k++ {
			if !flags[k] {
				continue
			}
			a := pts[k]
			b := pts[(k+1)%n]
			if !first {
				sb.WriteByte(' ')
			}
			first = false
			sb.WriteByte('M')
			sb.WriteString(f(a[0]))
			sb.WriteByte(',')
			sb.WriteString(f(a[1]))
			sb.WriteByte('L')
			sb.WriteString(f(b[0]))
			sb.WriteByte(',')
			sb.WriteString(f(b[1]))
		}
	}
}

// appendCellPaths writes each cell's outer polygon as a closed
// subpath ("M x,y L x,y L x,y Z ...") into sb. Coordinates are emitted
// in world space; the caller-provided <g transform="scale(1,-1)">
// handles Y inversion.
func appendCellPaths(sb *strings.Builder, cells []Cell, idxs []int) {
	for i, ci := range idxs {
		if ci < 0 || ci >= len(cells) {
			continue
		}
		pts := cells[ci].Outer
		if len(pts) < 3 {
			continue
		}
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteByte('M')
		sb.WriteString(f(pts[0][0]))
		sb.WriteByte(',')
		sb.WriteString(f(pts[0][1]))
		for j := 1; j < len(pts); j++ {
			sb.WriteByte('L')
			sb.WriteString(f(pts[j][0]))
			sb.WriteByte(',')
			sb.WriteString(f(pts[j][1]))
		}
		sb.WriteByte('Z')
	}
}

// f formats a float32 coordinate at 2-decimal precision, stripping
// trailing zeros and an orphan decimal point to keep the SVG compact.
func f(v float32) string {
	s := strconv.FormatFloat(float64(v), 'f', 2, 32)
	if !strings.Contains(s, ".") {
		return s
	}
	// Trim trailing zeros, then a trailing dot if exposed.
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}
