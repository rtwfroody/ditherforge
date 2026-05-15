package cellslicer

import (
	"fmt"
	"strconv"
	"strings"
)

// DebugSVGOptions controls the per-slab cell visualization rendered
// by RenderSlabDebugSVG. Zero values pick sensible defaults.
type DebugSVGOptions struct {
	// CellSizeMM is the slab's cell pitch in mm; used to pick a
	// sensible default edge stroke width when EdgeWidthMM <= 0.
	CellSizeMM float32
	// PadMM is the bbox padding in mm. 0 → CellSizeMM (or 1 if that's 0).
	PadMM float32
	// FillBackgroundWhite renders an opaque white background rect
	// inside the viewBox. When false, the background is transparent.
	FillBackgroundWhite bool
	// DrawEdges adds a stroked path of every cell boundary on top of
	// the fills, so neighboring same-color cells remain visible.
	DrawEdges bool
	// EdgeWidthMM is the edge stroke width in mm. 0 → CellSizeMM/40
	// (≈ a thin hairline at typical viewing scale).
	EdgeWidthMM float32
	// MissingFill is the fill color for cells with no sample (or
	// Alpha=false). Default "#b4b4b4" — same grey the PNG path used.
	MissingFill string
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

	edgeW := opt.EdgeWidthMM
	if edgeW <= 0 {
		cs := opt.CellSizeMM
		if cs <= 0 {
			cs = 1
		}
		edgeW = cs / 40
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
		fmt.Fprintf(&sb,
			`<path fill="none" stroke="#000000" stroke-width="%s" vector-effect="non-scaling-stroke" d="`,
			f(edgeW),
		)
		all := make([]int, len(s.Cells))
		for i := range all {
			all[i] = i
		}
		appendCellPaths(&sb, s.Cells, all)
		sb.WriteString(`"/>`)
	}

	sb.WriteString(`</g></svg>`)
	return sb.String()
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
