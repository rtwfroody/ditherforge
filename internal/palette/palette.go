// Package palette handles color palette parsing, assignment, and computation.
package palette

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"image"
	"math"
	"math/rand"
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
// InventoryEntry holds a color from an inventory file with an optional label.
type InventoryEntry struct {
	Color [3]uint8
	Label string // user comment after the color, empty if none
}

func ParseInventoryFile(path string) ([]InventoryEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []InventoryEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Lines starting with "# " are comments; bare "#" followed by
		// hex digits are color values like #FF0000.
		if strings.HasPrefix(line, "#") && (len(line) < 2 || (line[1] < '0' || line[1] > '9') && (line[1] < 'a' || line[1] > 'f') && (line[1] < 'A' || line[1] > 'F')) {
			continue
		}
		// Split into color token and optional label.
		colorStr := line
		label := ""
		if idx := strings.IndexAny(line, " \t"); idx >= 0 {
			colorStr = line[:idx]
			label = strings.TrimSpace(line[idx+1:])
		}
		rgb, err := parseColor(colorStr)
		if err != nil {
			return nil, fmt.Errorf("in %s: %w", path, err)
		}
		entries = append(entries, InventoryEntry{Color: rgb, Label: label})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("inventory file %s contains no colors", path)
	}
	return entries, nil
}

// SelectFromInventory picks the best n colors from inventory for the given
// textures, minimizing the total nearest-color distance across texture samples
// in CIELAB space. This favors palettes where dominant texture colors land
// close to a palette color (producing solid areas), rather than maximizing
// gamut span (which causes everything to be dithered).
func SelectFromInventory(textures []image.Image, inventory []InventoryEntry, n int) []InventoryEntry {
	if n >= len(inventory) {
		return inventory
	}

	// Sample texture pixels into Lab space.
	samples := sampleTextureLab(textures, 500)

	// Convert inventory to Lab.
	invLab := make([][3]float64, len(inventory))
	for i, e := range inventory {
		cf := colorful.Color{
			R: float64(e.Color[0]) / 255.0,
			G: float64(e.Color[1]) / 255.0,
			B: float64(e.Color[2]) / 255.0,
		}
		invLab[i][0], invLab[i][1], invLab[i][2] = cf.Lab()
	}

	var bestSubset []int
	if combinationsCount(len(inventory), n) <= 50000 {
		bestSubset = exhaustiveNearestSearch(invLab, samples, n)
	} else {
		bestSubset = randomNearestSearch(invLab, samples, n, 50000)
	}

	result := make([]InventoryEntry, n)
	for i, idx := range bestSubset {
		result[i] = inventory[idx]
	}
	return result
}

// sampleTextureLab collects up to maxSamples texture pixels in CIELAB space.
func sampleTextureLab(textures []image.Image, maxSamples int) [][3]float64 {
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

	if len(allPixels) > maxSamples {
		step := len(allPixels) / maxSamples
		sampled := make([][3]uint8, 0, maxSamples)
		for i := 0; i < len(allPixels); i += step {
			sampled = append(sampled, allPixels[i])
		}
		allPixels = sampled
	}

	result := make([][3]float64, len(allPixels))
	for i, p := range allPixels {
		c := colorful.Color{
			R: float64(p[0]) / 255.0,
			G: float64(p[1]) / 255.0,
			B: float64(p[2]) / 255.0,
		}
		result[i][0], result[i][1], result[i][2] = c.Lab()
	}
	return result
}

// nearestScore computes total squared distance from each sample to its nearest
// palette color. Lower means less dithering noise and more solid areas.
func nearestScore(indices []int, invLab [][3]float64, samples [][3]float64) float64 {
	total := 0.0
	for _, s := range samples {
		best := math.MaxFloat64
		for _, idx := range indices {
			c := invLab[idx]
			d0 := s[0] - c[0]
			d1 := s[1] - c[1]
			d2 := s[2] - c[2]
			d := d0*d0 + d1*d1 + d2*d2
			if d < best {
				best = d
			}
		}
		total += best
	}
	return total
}

// exhaustiveNearestSearch enumerates all C(inv, n) subsets to find the one
// that minimizes total nearest-color distance from texture samples.
func exhaustiveNearestSearch(invLab [][3]float64, samples [][3]float64, n int) []int {
	bestScore := math.MaxFloat64
	var bestSubset []int

	indices := make([]int, n)
	var enumerate func(start, depth int)
	enumerate = func(start, depth int) {
		if depth == n {
			score := nearestScore(indices, invLab, samples)
			if score < bestScore {
				bestScore = score
				bestSubset = make([]int, n)
				copy(bestSubset, indices)
			}
			return
		}
		for i := start; i <= len(invLab)-(n-depth); i++ {
			indices[depth] = i
			enumerate(i+1, depth+1)
		}
	}
	enumerate(0, 0)
	return bestSubset
}

// randomNearestSearch evaluates numTrials random n-color subsets and returns the best.
func randomNearestSearch(invLab [][3]float64, samples [][3]float64, n int, numTrials int) []int {
	rng := rand.New(rand.NewSource(42))
	invN := len(invLab)

	bestScore := math.MaxFloat64
	var bestSubset []int
	indices := make([]int, n)

	for trial := 0; trial < numTrials; trial++ {
		for i := 0; i < n; i++ {
			for {
				indices[i] = rng.Intn(invN)
				dup := false
				for j := 0; j < i; j++ {
					if indices[j] == indices[i] {
						dup = true
						break
					}
				}
				if !dup {
					break
				}
			}
		}

		score := nearestScore(indices, invLab, samples)
		if score < bestScore {
			bestScore = score
			bestSubset = make([]int, n)
			copy(bestSubset, indices)
		}
	}
	return bestSubset
}

func combinationsCount(n, k int) int {
	if k > n {
		return 0
	}
	if k == 0 || k == n {
		return 1
	}
	if k > n-k {
		k = n - k
	}
	result := 1
	for i := 0; i < k; i++ {
		result = result * (n - i) / (i + 1)
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
