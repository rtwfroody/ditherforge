// Package palette handles color palette parsing, assignment, and computation.
package palette

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"image"
	"math"
	"os"
	"sort"
	"strings"

	colorful "github.com/lucasb-eyer/go-colorful"
	"github.com/muesli/clusters"
	"github.com/muesli/kmeans"
	"golang.org/x/image/colornames"
)

// ParsePalette parses a list of color strings into RGB triplets.
// Accepts CSS named colors and hex strings like #RRGGBB.
func ParsePalette(colors []string) ([][3]uint8, error) {
	result := make([][3]uint8, 0, len(colors))
	for _, c := range colors {
		c = strings.TrimSpace(c)
		rgb, err := parseColor(c)
		if err != nil {
			return nil, err
		}
		result = append(result, rgb)
	}
	return result, nil
}

func parseColor(c string) ([3]uint8, error) {
	if strings.HasPrefix(c, "#") {
		// Parse hex: #RGB, #RRGGBB, #RRGGBBAA
		s := strings.TrimPrefix(c, "#")
		if len(s) == 3 {
			// Expand #RGB → #RRGGBB
			s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
		}
		if len(s) == 8 {
			// Drop alpha
			s = s[:6]
		}
		if len(s) != 6 {
			return [3]uint8{}, fmt.Errorf("unknown color %q — use a CSS name (e.g. 'red') or hex (e.g. '#FF0000')", c)
		}
		b, err := hex.DecodeString(s)
		if err != nil {
			return [3]uint8{}, fmt.Errorf("unknown color %q — use a CSS name (e.g. 'red') or hex (e.g. '#FF0000')", c)
		}
		return [3]uint8{b[0], b[1], b[2]}, nil
	}

	// Try CSS named color lookup.
	if rgba, ok := colornames.Map[strings.ToLower(c)]; ok {
		return [3]uint8{rgba.R, rgba.G, rgba.B}, nil
	}

	return [3]uint8{}, fmt.Errorf("unknown color %q — use a CSS name (e.g. 'red') or hex (e.g. '#FF0000')", c)
}

// AssignPalette assigns each face color to the nearest palette color using
// CIELAB distance. Returns a slice of palette indices per face.
func AssignPalette(faceColors [][3]uint8, palette [][3]uint8) []int32 {
	// Convert palette to Lab.
	palLab := make([][3]float64, len(palette))
	for i, p := range palette {
		c := colorful.Color{
			R: float64(p[0]) / 255.0,
			G: float64(p[1]) / 255.0,
			B: float64(p[2]) / 255.0,
		}
		palLab[i][0], palLab[i][1], palLab[i][2] = c.Lab()
	}

	assignments := make([]int32, len(faceColors))
	for fi, fc := range faceColors {
		c := colorful.Color{
			R: float64(fc[0]) / 255.0,
			G: float64(fc[1]) / 255.0,
			B: float64(fc[2]) / 255.0,
		}
		fL, fA, fB := c.Lab()

		bestIdx := 0
		bestDist := math.MaxFloat64
		for pi, pl := range palLab {
			dL := fL - pl[0]
			dA := fA - pl[1]
			dB := fB - pl[2]
			d := dL*dL + dA*dA + dB*dB
			if d < bestDist {
				bestDist = d
				bestIdx = pi
			}
		}
		assignments[fi] = int32(bestIdx)
	}
	return assignments
}

// labPoint implements clusters.Observation for k-means clustering in Lab space.
type labPoint struct {
	L, A, B float64
}

func (p labPoint) Coordinates() clusters.Coordinates {
	return clusters.Coordinates{p.L, p.A, p.B}
}

func (p labPoint) Distance(point clusters.Coordinates) float64 {
	dL := p.L - point[0]
	dA := p.A - point[1]
	dB := p.B - point[2]
	return math.Sqrt(dL*dL + dA*dA + dB*dB)
}

// ComputePalette finds n dominant colors in the textures using k-means in Lab
// space. Returns palette sorted by CIELAB lightness descending (lightest first).
func ComputePalette(textures []image.Image, n int) [][3]uint8 {
	// Collect all pixels.
	var allPixels [][3]uint8
	for _, tex := range textures {
		bounds := tex.Bounds()
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				r, g, b, _ := tex.At(x, y).RGBA()
				allPixels = append(allPixels, [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)})
			}
		}
	}

	// Subsample to 20k pixels if needed.
	const maxPixels = 20_000
	if len(allPixels) > maxPixels {
		step := len(allPixels) / maxPixels
		sampled := make([][3]uint8, 0, maxPixels)
		for i := 0; i < len(allPixels); i += step {
			sampled = append(sampled, allPixels[i])
		}
		allPixels = sampled
	}

	// Convert pixels to Lab observations.
	observations := make(clusters.Observations, len(allPixels))
	for i, p := range allPixels {
		c := colorful.Color{
			R: float64(p[0]) / 255.0,
			G: float64(p[1]) / 255.0,
			B: float64(p[2]) / 255.0,
		}
		L, A, B := c.Lab()
		observations[i] = labPoint{L, A, B}
	}

	// Run k-means.
	km := kmeans.New()

	result, err := km.Partition(observations, n)
	if err != nil {
		return fallbackPalette(allPixels, n)
	}

	// Convert centroids back to RGB.
	type entry struct {
		rgb [3]uint8
		L   float64
	}
	entries := make([]entry, 0, len(result))
	for _, cluster := range result {
		center := cluster.Center
		if len(center) < 3 {
			continue
		}
		c := colorful.Lab(center[0], center[1], center[2])
		r := math.Round(c.R * 255)
		g := math.Round(c.G * 255)
		b := math.Round(c.B * 255)
		if r < 0 {
			r = 0
		}
		if r > 255 {
			r = 255
		}
		if g < 0 {
			g = 0
		}
		if g > 255 {
			g = 255
		}
		if b < 0 {
			b = 0
		}
		if b > 255 {
			b = 255
		}
		entries = append(entries, entry{
			rgb: [3]uint8{uint8(r), uint8(g), uint8(b)},
			L:   center[0],
		})
	}

	// Sort by lightness descending.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].L > entries[j].L
	})

	result2 := make([][3]uint8, len(entries))
	for i, e := range entries {
		result2[i] = e.rgb
	}
	return result2
}

// ParseInventoryFile reads a file with one color per line.
// Blank lines and lines starting with # are ignored.
func ParseInventoryFile(path string) ([][3]uint8, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var colors [][3]uint8
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rgb, err := parseColor(line)
		if err != nil {
			return nil, fmt.Errorf("in %s: %w", path, err)
		}
		colors = append(colors, rgb)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(colors) == 0 {
		return nil, fmt.Errorf("inventory file %s contains no colors", path)
	}
	return colors, nil
}

// SelectFromInventory picks the best n colors from inventory for the given
// textures. It samples the textures, clusters into n groups, then for each
// cluster picks the inventory color closest to the centroid.
func SelectFromInventory(textures []image.Image, inventory [][3]uint8, n int) [][3]uint8 {
	if n >= len(inventory) {
		return inventory
	}

	// Find n ideal colors using k-means on the texture.
	ideal := ComputePalette(textures, n)

	// For each ideal color, find the closest unused inventory color.
	used := make([]bool, len(inventory))
	invLab := make([][3]float64, len(inventory))
	for i, c := range inventory {
		cf := colorful.Color{
			R: float64(c[0]) / 255.0,
			G: float64(c[1]) / 255.0,
			B: float64(c[2]) / 255.0,
		}
		invLab[i][0], invLab[i][1], invLab[i][2] = cf.Lab()
	}

	result := make([][3]uint8, n)
	for i, id := range ideal {
		cf := colorful.Color{
			R: float64(id[0]) / 255.0,
			G: float64(id[1]) / 255.0,
			B: float64(id[2]) / 255.0,
		}
		iL, iA, iB := cf.Lab()

		bestIdx := -1
		bestDist := math.MaxFloat64
		for j := range inventory {
			if used[j] {
				continue
			}
			dL := iL - invLab[j][0]
			dA := iA - invLab[j][1]
			dB := iB - invLab[j][2]
			d := dL*dL + dA*dA + dB*dB
			if d < bestDist {
				bestDist = d
				bestIdx = j
			}
		}
		used[bestIdx] = true
		result[i] = inventory[bestIdx]
	}
	return result
}

func fallbackPalette(pixels [][3]uint8, n int) [][3]uint8 {
	seen := map[[3]uint8]bool{}
	var result [][3]uint8
	for _, p := range pixels {
		if !seen[p] {
			seen[p] = true
			result = append(result, p)
			if len(result) >= n {
				break
			}
		}
	}
	return result
}
