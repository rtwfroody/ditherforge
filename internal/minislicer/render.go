package minislicer

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// RenderConfig controls per-layer SVG output.
type RenderConfig struct {
	// OutputDir is a directory; one SVG per layer is written into it.
	OutputDir string
	// PixelsPerUnit scales mesh-unit coordinates to SVG pixels.
	PixelsPerUnit float32
	// StrokeWidth (in SVG units) for the section arcs.
	StrokeWidth float32
	// PadFraction extends the viewBox by this fraction of model size.
	PadFraction float32
}

// DefaultRenderConfig returns a RenderConfig sized for ~10mm models.
func DefaultRenderConfig(outDir string) RenderConfig {
	return RenderConfig{
		OutputDir:     outDir,
		PixelsPerUnit: 50,
		StrokeWidth:   3,
		PadFraction:   0.05,
	}
}

// RenderLayers writes one SVG file per non-empty layer to cfg.OutputDir
// showing each section as a colored arc of the layer contour.
//
// `palette` and `assignments` are the outputs of DitherSections.
// `assignments[i] == -1` means the section is hidden (alpha=false)
// and is rendered in light gray.
func RenderLayers(layers []Layer, sections []Section, palette [][3]uint8, assignments []int32, cfg RenderConfig) error {
	return renderLayersWithFn(layers, sections, cfg, func(sid int) string {
		if sid < 0 || sid >= len(assignments) || assignments[sid] < 0 {
			return "#888888"
		}
		rgb := palette[assignments[sid]]
		return fmt.Sprintf("#%02x%02x%02x", rgb[0], rgb[1], rgb[2])
	})
}

// RenderLayersSampled writes SVGs colored by each section's raw
// pre-dither sampled RGB. Use this alongside RenderLayers to
// distinguish sampling bugs (visible in both) from dither bugs
// (visible only in dithered output).
func RenderLayersSampled(layers []Layer, sections []Section, sampleColors [][3]uint8, cfg RenderConfig) error {
	return renderLayersWithFn(layers, sections, cfg, func(sid int) string {
		if sid < 0 || sid >= len(sampleColors) {
			return "#888888"
		}
		c := sampleColors[sid]
		return fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2])
	})
}

func renderLayersWithFn(layers []Layer, sections []Section, cfg RenderConfig, colorFor func(int) string) error {
	if cfg.OutputDir == "" {
		return fmt.Errorf("RenderConfig.OutputDir is required")
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	// Compute global XY bbox so all SVGs share a viewBox (so layers
	// register when stacked / animated).
	minX, minY := float32(math.Inf(1)), float32(math.Inf(1))
	maxX, maxY := float32(math.Inf(-1)), float32(math.Inf(-1))
	hasAny := false
	for _, l := range layers {
		for _, lp := range l.Loops {
			for _, p := range lp.Points {
				if p[0] < minX {
					minX = p[0]
				}
				if p[1] < minY {
					minY = p[1]
				}
				if p[0] > maxX {
					maxX = p[0]
				}
				if p[1] > maxY {
					maxY = p[1]
				}
				hasAny = true
			}
		}
	}
	if !hasAny {
		return nil
	}
	pad := cfg.PadFraction * float32(math.Max(float64(maxX-minX), float64(maxY-minY)))
	minX -= pad
	minY -= pad
	maxX += pad
	maxY += pad
	w := (maxX - minX) * cfg.PixelsPerUnit
	h := (maxY - minY) * cfg.PixelsPerUnit

	// Group sections by (LayerIdx, LoopIdx) for efficient per-layer
	// rendering.
	type loopKey struct{ layer, loop int }
	loopSecs := make(map[loopKey][]int)
	for i, s := range sections {
		loopSecs[loopKey{s.LayerIdx, s.LoopIdx}] = append(loopSecs[loopKey{s.LayerIdx, s.LoopIdx}], i)
	}

	for li, layer := range layers {
		// Skip empty layers.
		if len(layer.Loops) == 0 {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b,
			`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %.2f %.2f" width="%.2f" height="%.2f">`,
			w, h, w, h)
		fmt.Fprintf(&b, "\n  <rect width=\"100%%\" height=\"100%%\" fill=\"#202020\"/>\n")
		fmt.Fprintf(&b, "  <g transform=\"translate(%.2f %.2f) scale(%.4f -%.4f)\">\n",
			-minX*cfg.PixelsPerUnit, h+minY*cfg.PixelsPerUnit, cfg.PixelsPerUnit, cfg.PixelsPerUnit)

		// Light outline of every loop first (so cross-loop seams are
		// visible even before colored sections paint over them).
		for _, lp := range layer.Loops {
			fmt.Fprintf(&b, "    <polygon points=\"%s\" fill=\"#383838\" stroke=\"#505050\" stroke-width=\"%.4f\"/>\n",
				polygonPointsAttr(lp.Points), float32(cfg.StrokeWidth)/cfg.PixelsPerUnit*0.4)
		}

		// Colored section arcs.
		for lpi, lp := range layer.Loops {
			ids := loopSecs[loopKey{li, lpi}]
			cumLen := loopCumLen(lp.Points)
			perim := cumLen[len(lp.Points)]
			_ = perim
			for _, secID := range ids {
				s := sections[secID]
				if s.Kind != KindRibbon {
					continue
				}
				poly := pathBetweenArcs(lp.Points, cumLen, s.StartArc, s.EndArc)
				color := colorFor(secID)
				fmt.Fprintf(&b,
					"    <polyline points=\"%s\" fill=\"none\" stroke=\"%s\" stroke-width=\"%.4f\" stroke-linecap=\"round\"/>\n",
					polylinePointsAttr(poly), color,
					float32(cfg.StrokeWidth)/cfg.PixelsPerUnit)
			}
		}

		// Cap tile rectangles, colored from the same colorFor()
		// callback so the diagnostic shows both ribbons and caps.
		for sid, s := range sections {
			if s.LayerIdx != layer.LayerIdx {
				continue
			}
			if s.Kind != KindCapTop && s.Kind != KindCapBottom {
				continue
			}
			x0, y0, x1, y1 := s.CapBoundsXY[0], s.CapBoundsXY[1], s.CapBoundsXY[2], s.CapBoundsXY[3]
			color := colorFor(sid)
			fmt.Fprintf(&b,
				"    <rect x=\"%.4f\" y=\"%.4f\" width=\"%.4f\" height=\"%.4f\" fill=\"%s\" stroke=\"none\" opacity=\"0.7\"/>\n",
				x0, y0, x1-x0, y1-y0, color)
		}

		fmt.Fprintf(&b, "  </g>\n</svg>\n")

		path := filepath.Join(cfg.OutputDir, fmt.Sprintf("layer_%04d_z%.3f.svg", layer.LayerIdx, layer.Z))
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

func loopCumLen(points []Point2) []float32 {
	n := len(points)
	cum := make([]float32, n+1)
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		dx := float64(points[j][0] - points[i][0])
		dy := float64(points[j][1] - points[i][1])
		cum[i+1] = cum[i] + float32(math.Sqrt(dx*dx+dy*dy))
	}
	return cum
}

// pathBetweenArcs returns the polyline along the closed loop from
// arc parameter startArc to endArc (cyclic). startArc < endArc
// always; if endArc > perimeter, wraps.
func pathBetweenArcs(points []Point2, cumLen []float32, startArc, endArc float32) []Point2 {
	n := len(points)
	perim := cumLen[n]
	out := []Point2{pointAtArc(points, cumLen, startArc)}
	// Walk forward through vertices that fall in (startArc, endArc).
	cur := startArc
	for cur < endArc {
		// Find next vertex strictly after cur.
		var nextArc float32 = perim + endArc + 1 // sentinel
		var nextVert Point2
		for i := 0; i <= n; i++ {
			a := cumLen[i%n]
			if i == n {
				a = perim
			}
			if a > cur && a < nextArc && a <= endArc {
				nextArc = a
				nextVert = points[i%n]
			}
		}
		if nextArc > endArc {
			break
		}
		out = append(out, nextVert)
		cur = nextArc
	}
	out = append(out, pointAtArc(points, cumLen, endArc))
	return out
}

func polylinePointsAttr(points []Point2) string {
	var b strings.Builder
	for i, p := range points {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%.4f,%.4f", p[0], p[1])
	}
	return b.String()
}

func polygonPointsAttr(points []Point2) string { return polylinePointsAttr(points) }
