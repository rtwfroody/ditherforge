// Package palette handles color palette parsing, assignment, and computation.
package palette

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"

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

// WeightedLabSample is a color in Lab space with a count (weight).
type WeightedLabSample struct {
	Lab   [3]float64
	Count int
}

// CellColorHistogram builds a deduplicated, weighted Lab sample set from cell
// RGB colors. Many cells share the same color, so this is much smaller than
// the full cell list.
func CellColorHistogram(colors [][3]uint8) []WeightedLabSample {
	hist := make(map[[3]uint8]int, len(colors)/10)
	for _, c := range colors {
		// Quantize to 7 bits per channel to merge near-identical colors.
		q := [3]uint8{c[0] &^ 1, c[1] &^ 1, c[2] &^ 1}
		hist[q]++
	}
	samples := make([]WeightedLabSample, 0, len(hist))
	for rgb, count := range hist {
		cf := colorful.Color{
			R: float64(rgb[0]) / 255.0,
			G: float64(rgb[1]) / 255.0,
			B: float64(rgb[2]) / 255.0,
		}
		var s WeightedLabSample
		s.Lab[0], s.Lab[1], s.Lab[2] = cf.Lab()
		s.Count = count
		samples = append(samples, s)
	}
	return samples
}

// topSamples returns at most maxN samples, keeping those with the highest
// count. Weights are preserved so scoring remains representative.
func topSamples(samples []WeightedLabSample, maxN int) []WeightedLabSample {
	if len(samples) <= maxN {
		return samples
	}
	// Sort by count descending.
	sorted := make([]WeightedLabSample, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Count > sorted[j].Count
	})
	return sorted[:maxN]
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

// ComputePalette finds n dominant colors using k-means in Lab space.
// Takes cell colors (from voxelized mesh surface).
// Returns palette sorted by CIELAB lightness descending (lightest first).
func ComputePalette(cellColors [][3]uint8, n int) [][3]uint8 {
	// Build weighted samples from histogram, then expand for k-means.
	// k-means needs repeated points to weight properly.
	hist := CellColorHistogram(cellColors)

	// Build observations, repeating each color proportionally.
	// Cap total observations at 20k.
	totalCount := 0
	for _, s := range hist {
		totalCount += s.Count
	}
	maxObs := 20_000
	scale := 1.0
	if totalCount > maxObs {
		scale = float64(maxObs) / float64(totalCount)
	}

	observations := make(clusters.Observations, 0, maxObs)
	for _, s := range hist {
		reps := int(math.Max(1, math.Round(float64(s.Count)*scale)))
		for r := 0; r < reps; r++ {
			observations = append(observations, labPoint{s.Lab[0], s.Lab[1], s.Lab[2]})
		}
	}

	// Run k-means.
	km := kmeans.New()

	result, err := km.Partition(observations, n)
	if err != nil {
		return fallbackPalette(cellColors, n)
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
// cell colors. Uses hull-based scoring that measures distance to the convex
// hull of each palette subset, properly accounting for dithering's ability
// to mix colors.
func SelectFromInventory(cellColors [][3]uint8, inventory []InventoryEntry, n int) []InventoryEntry {
	if n >= len(inventory) {
		return inventory
	}

	samples := CellColorHistogram(cellColors)
	samples = topSamples(samples, 500)

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

	scorer := weightedHullScore

	var bestSubset []int
	if combinationsCount(len(inventory), n) <= 50000 {
		bestSubset = exhaustiveSearch(invLab, samples, n, scorer)
	} else {
		bestSubset = randomSearch(invLab, samples, n, 50000, scorer)
	}

	result := make([]InventoryEntry, n)
	for i, idx := range bestSubset {
		result[i] = inventory[idx]
	}
	return result
}

// scoreFunc is the signature for palette subset scoring functions.
type scoreFunc func(indices []int, invLab [][3]float64, samples []WeightedLabSample) float64

// weightedHullScore computes total weighted squared distance from each sample
// to the convex hull of the palette subset. Points inside the hull score 0.
func weightedHullScore(indices []int, invLab [][3]float64, samples []WeightedLabSample) float64 {
	verts := make([][3]float64, len(indices))
	for i, idx := range indices {
		verts[i] = invLab[idx]
	}
	total := 0.0
	for _, s := range samples {
		d := distToConvexHull(s.Lab, verts)
		total += d * d * float64(s.Count)
	}
	return total
}

// exhaustiveSearch enumerates all C(inv, n) subsets to find the one that
// minimizes the given scoring function. Uses parallel workers.
func exhaustiveSearch(invLab [][3]float64, samples []WeightedLabSample, n int, score scoreFunc) []int {
	if n < 1 {
		return nil
	}

	// Generate all subsets where the first index varies, and farm out to workers.
	type result struct {
		score  float64
		subset []int
	}

	numWorkers := runtime.NumCPU()
	jobs := make(chan []int, 64)
	results := make(chan result, numWorkers)

	var wg sync.WaitGroup
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			localBest := math.MaxFloat64
			var localSubset []int
			for subset := range jobs {
				s := score(subset, invLab, samples)
				if s < localBest {
					localBest = s
					localSubset = make([]int, len(subset))
					copy(localSubset, subset)
				}
			}
			results <- result{localBest, localSubset}
		}()
	}

	// Enumerate all C(invN, n) subsets.
	go func() {
		indices := make([]int, n)
		var enumerate func(start, depth int)
		enumerate = func(start, depth int) {
			if depth == n {
				sub := make([]int, n)
				copy(sub, indices)
				jobs <- sub
				return
			}
			for i := start; i <= len(invLab)-(n-depth); i++ {
				indices[depth] = i
				enumerate(i+1, depth+1)
			}
		}
		enumerate(0, 0)
		close(jobs)
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	bestScore := math.MaxFloat64
	var bestSubset []int
	for r := range results {
		if r.score < bestScore {
			bestScore = r.score
			bestSubset = r.subset
		}
	}
	return bestSubset
}

// randomSearch evaluates numTrials random n-color subsets and returns the best.
func randomSearch(invLab [][3]float64, samples []WeightedLabSample, n int, numTrials int, score scoreFunc) []int {
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

		s := score(indices, invLab, samples)
		if s < bestScore {
			bestScore = s
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
