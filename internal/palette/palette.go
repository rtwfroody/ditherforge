// Package palette handles color palette parsing, assignment, and computation.
package palette

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	colorful "github.com/lucasb-eyer/go-colorful"
	"github.com/muesli/clusters"
	"github.com/muesli/kmeans"
	"github.com/rtwfroody/ditherforge/internal/progress"
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

// topSamples returns at most maxN samples while preserving color diversity.
// It bins samples in Lab space and takes the top samples from each bin
// proportionally, so minority color regions are not drowned out by the
// majority.
func topSamples(samples []WeightedLabSample, maxN int) []WeightedLabSample {
	if len(samples) <= maxN {
		return samples
	}

	// Bin samples in coarse Lab space (L in [0,100], a/b in ~[-128,128]).
	// Use 8 bins per axis = 512 bins total.
	const binsPerAxis = 8
	type binKey [3]int
	binOf := func(lab [3]float64) binKey {
		l := int(lab[0] / (100.0 / binsPerAxis))
		a := int((lab[1] + 128) / (256.0 / binsPerAxis))
		b := int((lab[2] + 128) / (256.0 / binsPerAxis))
		if l < 0 {
			l = 0
		} else if l >= binsPerAxis {
			l = binsPerAxis - 1
		}
		if a < 0 {
			a = 0
		} else if a >= binsPerAxis {
			a = binsPerAxis - 1
		}
		if b < 0 {
			b = 0
		} else if b >= binsPerAxis {
			b = binsPerAxis - 1
		}
		return binKey{l, a, b}
	}

	// Group samples into bins, sorted by count within each bin.
	bins := make(map[binKey][]int) // bin -> indices into samples
	for i := range samples {
		k := binOf(samples[i].Lab)
		bins[k] = append(bins[k], i)
	}
	for _, idxs := range bins {
		sort.Slice(idxs, func(a, b int) bool {
			return samples[idxs[a]].Count > samples[idxs[b]].Count
		})
	}

	// Each occupied bin gets at least 1 slot, then remaining slots are
	// distributed proportionally to total weight.
	nBins := len(bins)
	base := nBins
	if base > maxN {
		base = maxN
	}
	remaining := maxN - base

	// Compute total weight per bin, sorted by weight descending for
	// deterministic allocation (map iteration order is random).
	type binInfo struct {
		key    binKey
		weight int
	}
	binList := make([]binInfo, 0, nBins)
	totalWeight := 0
	for k, idxs := range bins {
		w := 0
		for _, i := range idxs {
			w += samples[i].Count
		}
		binList = append(binList, binInfo{k, w})
		totalWeight += w
	}
	sort.Slice(binList, func(i, j int) bool {
		return binList[i].weight > binList[j].weight
	})

	// Allocate slots: 1 guaranteed per bin + proportional share of remainder.
	result := make([]WeightedLabSample, 0, maxN)
	for _, bi := range binList {
		idxs := bins[bi.key]
		slots := 1
		if totalWeight > 0 {
			slots += int(float64(remaining) * float64(bi.weight) / float64(totalWeight))
		}
		if slots > len(idxs) {
			slots = len(idxs)
		}
		for j := 0; j < slots; j++ {
			result = append(result, samples[idxs[j]])
		}
	}

	// If we ended up with more than maxN due to rounding, trim.
	if len(result) > maxN {
		sort.Slice(result, func(i, j int) bool {
			return result[i].Count > result[j].Count
		})
		result = result[:maxN]
	}
	return result
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

// ComputePaletteWithLocked computes n additional palette colors that complement
// the locked colors. Runs k-means for (n + len(locked)) clusters, then drops
// the clusters closest to locked colors, returning the remaining n.
func ComputePaletteWithLocked(cellColors [][3]uint8, n int, locked [][3]uint8) [][3]uint8 {
	if len(locked) == 0 {
		return ComputePalette(cellColors, n)
	}

	// Compute more clusters than needed, then remove ones covered by locked.
	total := n + len(locked)
	all := ComputePalette(cellColors, total)

	// Convert locked colors to Lab for distance comparison.
	lockedLab := make([][3]float64, len(locked))
	for i, c := range locked {
		cf := colorful.Color{
			R: float64(c[0]) / 255.0,
			G: float64(c[1]) / 255.0,
			B: float64(c[2]) / 255.0,
		}
		lockedLab[i][0], lockedLab[i][1], lockedLab[i][2] = cf.Lab()
	}

	// Convert computed colors to Lab.
	allLab := make([][3]float64, len(all))
	for i, c := range all {
		cf := colorful.Color{
			R: float64(c[0]) / 255.0,
			G: float64(c[1]) / 255.0,
			B: float64(c[2]) / 255.0,
		}
		allLab[i][0], allLab[i][1], allLab[i][2] = cf.Lab()
	}

	// Greedily remove len(locked) computed colors closest to any locked color.
	removed := make([]bool, len(all))
	for range locked {
		bestIdx := -1
		bestDist := math.MaxFloat64
		for i, lab := range allLab {
			if removed[i] {
				continue
			}
			for _, ll := range lockedLab {
				d0 := lab[0] - ll[0]
				d1 := lab[1] - ll[1]
				d2 := lab[2] - ll[2]
				d := d0*d0 + d1*d1 + d2*d2
				if d < bestDist {
					bestDist = d
					bestIdx = i
				}
			}
		}
		if bestIdx >= 0 {
			removed[bestIdx] = true
		}
	}

	result := make([][3]uint8, 0, n)
	for i, c := range all {
		if !removed[i] {
			result = append(result, c)
		}
		if len(result) >= n {
			break
		}
	}
	return result
}

// InventoryEntry holds a color from an inventory file with an optional label.
type InventoryEntry struct {
	Color [3]uint8
	Label string // user comment after the color, empty if none
}

// ParseInventoryData parses inventory entries from raw bytes.
// Blank lines and lines starting with # (comment) are ignored.
// Each non-comment line has a color (#RRGGBB or CSS name) and optional label.
func ParseInventoryData(data []byte) ([]InventoryEntry, error) {
	var entries []InventoryEntry
	scanner := bufio.NewScanner(bytes.NewReader(data))
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
			return nil, err
		}
		entries = append(entries, InventoryEntry{Color: rgb, Label: label})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// ParseInventoryFile reads a file with one color per line.
// Blank lines and lines starting with # are ignored.
func ParseInventoryFile(path string) ([]InventoryEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	entries, err := ParseInventoryData(data)
	if err != nil {
		return nil, fmt.Errorf("in %s: %w", path, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("inventory file %s contains no colors", path)
	}
	return entries, nil
}

// SelectFromInventory picks the best n colors from inventory that, together
// with the locked colors, best cover the cell colors. Locked colors are
// included in scoring so candidates complement them rather than duplicate.
// When dithering is true, uses hull-based scoring that accounts for
// dithering's ability to mix colors. When false, uses nearest-color scoring
// that minimizes the error when each cell gets exactly one color.
func SelectFromInventory(ctx context.Context, cellColors [][3]uint8, inventory []InventoryEntry, n int, locked [][3]uint8, dithering bool) ([]InventoryEntry, error) {
	if n >= len(inventory) {
		return inventory, nil
	}

	samples := CellColorHistogram(cellColors)
	samples = topSamples(samples, 5000)

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

	// Convert locked colors to Lab so scoring includes them.
	lockedLab := make([][3]float64, len(locked))
	for i, c := range locked {
		cf := colorful.Color{
			R: float64(c[0]) / 255.0,
			G: float64(c[1]) / 255.0,
			B: float64(c[2]) / 255.0,
		}
		lockedLab[i][0], lockedLab[i][1], lockedLab[i][2] = cf.Lab()
	}

	var scorer scoreFunc
	if dithering {
		scorer = weightedHullScore
	} else {
		scorer = weightedNearestScore
	}

	combos := combinationsCount(len(inventory), n)
	exhaustive := combos <= 2000

	start := time.Now()
	var bestSubset []int
	var evaluated int
	var err error
	if exhaustive {
		var counter atomic.Int64
		bar := progress.NewBar(combos, "  Selecting colors")
		done := make(chan struct{})
		exited := make(chan struct{})
		go func() {
			defer close(exited)
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					bar.Set64(counter.Load())
				case <-done:
					bar.Set64(counter.Load())
					return
				}
			}
		}()
		bestSubset, err = exhaustiveSearch(ctx, invLab, lockedLab, samples, n, scorer, &counter)
		close(done)
		<-exited
		evaluated = int(counter.Load())
		if err != nil {
			bar.Finish()
			return nil, err
		}
		progress.FinishBar(bar, "Selected colors", fmt.Sprintf("(%d evaluated)", evaluated), time.Since(start))
	} else {
		bestSubset, evaluated, err = greedySwapSearch(ctx, invLab, lockedLab, samples, n, scorer)
		if err != nil {
			return nil, err
		}
		fmt.Printf("  Selected colors (%d evaluated) in %.1fs\n", evaluated, time.Since(start).Seconds())
	}

	result := make([]InventoryEntry, n)
	for i, idx := range bestSubset {
		result[i] = inventory[idx]
	}
	return result, nil
}

// scoreFunc is the signature for palette subset scoring functions.
// fixedLab contains locked colors that are always part of the palette.
type scoreFunc func(indices []int, invLab [][3]float64, fixedLab [][3]float64, samples []WeightedLabSample) float64

// ditherSpreadFactor controls how much the nearest-vertex distance penalizes
// colors that are inside the hull but far from any palette color. This
// accounts for the perceptual cost of dithering distant colors: e.g. gray
// reproduced by alternating black and white cells looks noisy even though
// gray is inside the {black,white,...} hull.
const ditherSpreadFactor = 0.3

// nearestVertexDist returns the Euclidean distance from p to the closest vertex.
func nearestVertexDist(p [3]float64, verts [][3]float64) float64 {
	best := math.MaxFloat64
	for _, v := range verts {
		d0 := p[0] - v[0]
		d1 := p[1] - v[1]
		d2 := p[2] - v[2]
		d := d0*d0 + d1*d1 + d2*d2
		if d < best {
			best = d
		}
	}
	return math.Sqrt(best)
}

// buildVerts combines candidate inventory colors with fixed (locked) colors
// into a single vertex slice for scoring.
func buildVerts(indices []int, invLab [][3]float64, fixedLab [][3]float64) [][3]float64 {
	verts := make([][3]float64, len(fixedLab)+len(indices))
	copy(verts, fixedLab)
	for i, idx := range indices {
		verts[len(fixedLab)+i] = invLab[idx]
	}
	return verts
}

// weightedHullScore computes total weighted distance from each sample to the
// full palette (locked + candidate). Uses hull distance plus a dithering
// spread penalty based on nearest-vertex distance, so colors that require
// mixing distant palette entries are penalized even when geometrically
// inside the hull.
func weightedHullScore(indices []int, invLab [][3]float64, fixedLab [][3]float64, samples []WeightedLabSample) float64 {
	verts := buildVerts(indices, invLab, fixedLab)
	total := 0.0
	for _, s := range samples {
		hullDist := distToConvexHull(s.Lab, verts)
		nearDist := nearestVertexDist(s.Lab, verts)
		d := hullDist + ditherSpreadFactor*nearDist
		total += d * d * float64(s.Count)
	}
	return total
}

// weightedNearestScore computes total weighted squared distance from each
// sample to the nearest color in the full palette (locked + candidate).
// Used when dithering is disabled, since each cell gets exactly one color.
func weightedNearestScore(indices []int, invLab [][3]float64, fixedLab [][3]float64, samples []WeightedLabSample) float64 {
	verts := buildVerts(indices, invLab, fixedLab)
	total := 0.0
	for _, s := range samples {
		d := nearestVertexDist(s.Lab, verts)
		total += d * d * float64(s.Count)
	}
	return total
}

// exhaustiveSearch enumerates all C(inv, n) subsets to find the one that
// minimizes the given scoring function. Uses parallel workers.
func exhaustiveSearch(ctx context.Context, invLab [][3]float64, lockedLab [][3]float64, samples []WeightedLabSample, n int, score scoreFunc, progress *atomic.Int64) ([]int, error) {
	if n < 1 {
		return nil, nil
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
				if ctx.Err() != nil {
					break
				}
				progress.Add(1)
				s := score(subset, invLab, lockedLab, samples)
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
			if ctx.Err() != nil {
				return
			}
			if depth == n {
				sub := make([]int, n)
				copy(sub, indices)
				select {
				case jobs <- sub:
				case <-ctx.Done():
					return
				}
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return bestSubset, nil
}

// greedySwapSearch builds an initial subset greedily, then refines with
// pairwise swaps (the PAM k-medoids algorithm). Returns the best subset
// found and the total number of scoring calls made.
func greedySwapSearch(ctx context.Context, invLab [][3]float64, lockedLab [][3]float64, samples []WeightedLabSample, n int, score scoreFunc) ([]int, int, error) {
	invN := len(invLab)
	evaluated := 0

	// Greedy construction: add one color at a time, picking the candidate
	// that most reduces the score.
	selected := make([]int, 0, n)
	inSubset := make([]bool, invN)
	for len(selected) < n {
		if ctx.Err() != nil {
			return nil, evaluated, ctx.Err()
		}
		bestIdx := -1
		bestScore := math.MaxFloat64
		selected = append(selected, 0) // extend by one slot
		for ci := 0; ci < invN; ci++ {
			if inSubset[ci] {
				continue
			}
			selected[len(selected)-1] = ci
			evaluated++
			s := score(selected, invLab, lockedLab, samples)
			if s < bestScore {
				bestScore = s
				bestIdx = ci
			}
		}
		selected[len(selected)-1] = bestIdx
		inSubset[bestIdx] = true
	}

	// Swap refinement: try replacing each selected color with each
	// unselected color. Repeat until no improvement found.
	currentScore := score(selected, invLab, lockedLab, samples)
	evaluated++
	for {
		improved := false
		for si := 0; si < n; si++ {
			if ctx.Err() != nil {
				return nil, evaluated, ctx.Err()
			}
			old := selected[si]
			for ci := 0; ci < invN; ci++ {
				if inSubset[ci] {
					continue
				}
				selected[si] = ci
				evaluated++
				s := score(selected, invLab, lockedLab, samples)
				if s < currentScore {
					// Accept the swap.
					inSubset[old] = false
					inSubset[ci] = true
					currentScore = s
					old = ci
					improved = true
				} else {
					selected[si] = old
				}
			}
		}
		if !improved {
			break
		}
	}

	return selected, evaluated, nil
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
