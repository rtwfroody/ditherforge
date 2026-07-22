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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	colorful "github.com/lucasb-eyer/go-colorful"
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

// WeightedLabSample is a color in Lab space with both a raw pixel count
// (Count, used by topSamples for diversity sorting) and a perceptual
// scoring weight (Weight, used by the score functions). After
// CellColorHistogram, Weight = float64(Count). ApplyChromaWeighting
// inflates Weight on chromatic samples so the scorer treats colorful
// regions as more important than their pixel-area share.
//
// Note: topSamples still bins and ranks by Count, not Weight. That
// keeps diversity-trimming based on pixel area (so a chromatic sample
// with zero raw pixels can't sneak through), and means chroma
// emphasis only fires at the score step. Justified for now since
// real models produce many more bins than the 5000-sample cap.
type WeightedLabSample struct {
	Lab    [3]float64
	Count  int
	Weight float64
}

// CellColorHistogram builds a deduplicated, weighted Lab sample set from cell
// RGB colors. Many cells share the same color, so this is much smaller than
// the full cell list.
//
// If weights is nil, every cell contributes Weight=1 (legacy behavior:
// each voxel votes equally). If weights is non-nil it must be parallel
// to colors; per-cell area is summed into the bucket's Weight so sliver
// voxels don't outvote full-coverage voxels in palette selection. Count
// always tracks raw cell count (used by topSamples for diversity
// trimming; that is independent of physical area).
func CellColorHistogram(colors [][3]uint8, weights []float32) []WeightedLabSample {
	type bucket struct {
		count  int
		weight float64
	}
	hist := make(map[[3]uint8]bucket, len(colors)/10)
	useWeights := weights != nil
	for i, c := range colors {
		q := [3]uint8{c[0] &^ 1, c[1] &^ 1, c[2] &^ 1}
		b := hist[q]
		b.count++
		if useWeights {
			w := float64(weights[i])
			if w <= 0 {
				w = 1
			}
			b.weight += w
		} else {
			b.weight += 1
		}
		hist[q] = b
	}
	samples := make([]WeightedLabSample, 0, len(hist))
	for rgb, b := range hist {
		cf := colorful.Color{
			R: float64(rgb[0]) / 255.0,
			G: float64(rgb[1]) / 255.0,
			B: float64(rgb[2]) / 255.0,
		}
		var s WeightedLabSample
		s.Lab[0], s.Lab[1], s.Lab[2] = cf.Lab()
		s.Count = b.count
		s.Weight = b.weight
		samples = append(samples, s)
	}
	// Map iteration order is randomized, so sort into a stable order. The
	// downstream subset scorers sum a float term per sample, and float
	// addition is non-associative, so an unstable sample order makes the
	// scores — and thus the selected palette — vary run to run (the
	// cube_dither flake). Each sample is a distinct quantized colour, so
	// its Lab triple is a total order.
	sort.Slice(samples, func(i, j int) bool {
		a, b := samples[i].Lab, samples[j].Lab
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		if a[1] != b[1] {
			return a[1] < b[1]
		}
		return a[2] < b[2]
	})
	return samples
}

// chromaWeightExponent and chromaWeightScale tune the chroma-weighting
// applied by ApplyChromaWeighting. The form is:
//
//	Weight = Count * (1 + chroma/scale)^exponent
//
// where chroma = sqrt(a² + b²) measured in standard CIELAB units. The
// "1 +" floor keeps achromatic samples at their raw count weight (so
// all-grey models like the delorean still favor a grey vertex). Higher
// exponent or lower scale gives chromatic samples a louder voice in
// the score; the values below were tuned so all four
// tests/testdata/color/* fixtures pass simultaneously.
const (
	chromaWeightExponent = 1.0
	chromaWeightScale    = 50.0
)

// ApplyChromaWeighting reweights samples in-place so that high-chroma
// (vivid, attention-grabbing) cells contribute more to the scoring
// objective than their pixel-area share would suggest. This addresses
// a perceptual bias in the count-only scorer: the eye over-attends to
// saturated colors relative to greys, so a brick texture that's
// 70% mortar by pixel area still reads as "red brick" to a viewer
// and the palette should be picked accordingly.
//
// Composes multiplicatively against the existing Weight (so a future
// step that adds another perceptual factor can stack on top), rather
// than overwriting from Count. Callers that don't want chroma
// weighting can skip this call entirely.
func ApplyChromaWeighting(samples []WeightedLabSample) {
	for i := range samples {
		a := samples[i].Lab[1]
		b := samples[i].Lab[2]
		// go-colorful Lab values are scaled by 1/100 vs. standard
		// CIELAB; multiply by 100 so the constants above are in
		// familiar Lab units (chroma=20 = "modest," chroma=60 = "very vivid").
		chroma := math.Sqrt(a*a+b*b) * 100
		factor := math.Pow(1+chroma/chromaWeightScale, chromaWeightExponent)
		samples[i].Weight *= factor
	}
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
			if samples[idxs[a]].Count != samples[idxs[b]].Count {
				return samples[idxs[a]].Count > samples[idxs[b]].Count
			}
			// samples is in stable Lab order, so the smaller index is the
			// deterministic tie-break (sort.Slice is not stable).
			return idxs[a] < idxs[b]
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
		if binList[i].weight != binList[j].weight {
			return binList[i].weight > binList[j].weight
		}
		// Equal-weight bins must order deterministically (sort.Slice is
		// not stable and binList was built from random map order); the
		// binKey is a total order.
		ki, kj := binList[i].key, binList[j].key
		if ki[0] != kj[0] {
			return ki[0] < kj[0]
		}
		if ki[1] != kj[1] {
			return ki[1] < kj[1]
		}
		return ki[2] < kj[2]
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
			if result[i].Count != result[j].Count {
				return result[i].Count > result[j].Count
			}
			// Deterministic tie-break by Lab so the trim keeps a stable
			// subset (sort.Slice is not stable).
			a, b := result[i].Lab, result[j].Lab
			if a[0] != b[0] {
				return a[0] < b[0]
			}
			if a[1] != b[1] {
				return a[1] < b[1]
			}
			return a[2] < b[2]
		})
		result = result[:maxN]
	}
	return result
}

// DefaultTD is the transmission distance (mm) assumed for a filament whose
// TD is not specified. It is the opaque end of the scale: at the dither's
// nominal optical thickness this yields opacity ~1, so untagged palettes
// behave exactly as they did before TD existed.
const DefaultTD = 1.0

// InventoryEntry holds a color from an inventory file with an optional label.
type InventoryEntry struct {
	Color [3]uint8
	Label string  // user comment after the color, empty if none
	TD    float32 // transmission distance in mm; higher = more translucent
}

// parseTDAndLabel splits the part of an inventory line after the color into
// an optional transmission distance and a label. The primary form is
// positional — "#RRGGBB <td> <label>", e.g. "#FFE800 4.3 Yellow" — matching
// the Panchroma/HueForge convention used by the built-in collections: a
// leading bare number is the TD and the rest is the label. An explicit
// "td=<float>" token is also accepted anywhere as a fallback. When neither is
// present (e.g. "#A79E82 Tan") DefaultTD is returned and the whole remainder
// is the label.
func parseTDAndLabel(rest string) (float32, string) {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return DefaultTD, ""
	}
	// Positional: a leading bare number is the TD.
	if f, err := strconv.ParseFloat(fields[0], 32); err == nil {
		return float32(f), strings.Join(fields[1:], " ")
	}
	// Fallback: an explicit td=<float> token anywhere; other tokens are label.
	td := float32(DefaultTD)
	var labelTokens []string
	for _, tok := range fields {
		if v, ok := strings.CutPrefix(tok, "td="); ok {
			if f, err := strconv.ParseFloat(v, 32); err == nil {
				td = float32(f)
				continue
			}
		}
		labelTokens = append(labelTokens, tok)
	}
	return td, strings.Join(labelTokens, " ")
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
		// Split into color token and optional remainder (TD + label).
		colorStr := line
		rest := ""
		if idx := strings.IndexAny(line, " \t"); idx >= 0 {
			colorStr = line[:idx]
			rest = strings.TrimSpace(line[idx+1:])
		}
		rgb, err := parseColor(colorStr)
		if err != nil {
			return nil, err
		}
		td, label := parseTDAndLabel(rest)
		entries = append(entries, InventoryEntry{Color: rgb, Label: label, TD: td})
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
//
// cellWeights, if non-nil, must be parallel to cellColors and gives each
// cell's voting weight (typically the cell's clipped triangle surface
// area). When nil, every cell votes equally.
//
// tdp carries the transmission-distance context. When tdp.Enabled and the
// palette's TDs are non-uniform, candidate subsets are scored on their
// TD-effective colors (each filament composited toward the subset's infill
// through the printed shell) rather than nominal filament colors — otherwise a
// translucent filament gets picked as a chroma carrier it can't deliver. When
// tdp is not Enabled, or the effective TDs turn out uniform / all-opaque,
// scoring is bit-identical to the nominal path. Locked entries carry their own
// TDs so they participate in both the infill designation and the effective
// mix.
func SelectFromInventory(ctx context.Context, cellColors [][3]uint8, cellWeights []float32, inventory []InventoryEntry, n int, locked []InventoryEntry, dithering bool, tdp TDParams, tracker progress.Tracker) ([]InventoryEntry, error) {
	if n >= len(inventory) {
		return inventory, nil
	}

	samples := CellColorHistogram(cellColors, cellWeights)
	ApplyChromaWeighting(samples)
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
	for i, e := range locked {
		cf := colorful.Color{
			R: float64(e.Color[0]) / 255.0,
			G: float64(e.Color[1]) / 255.0,
			B: float64(e.Color[2]) / 255.0,
		}
		lockedLab[i][0], lockedLab[i][1], lockedLab[i][2] = cf.Lab()
	}

	var scorer scoreFunc
	if dithering {
		scorer = weightedHullScore
	} else {
		scorer = weightedNearestScore
	}

	// TD-aware scoring: only when requested AND the palette's normalized TDs
	// are non-uniform with at least one filament genuinely translucent. When
	// active, each candidate subset is scored on its PER-SAMPLE effective colors:
	// for every target sample s, filament c contributes eff(c, s) — c composited
	// toward THAT sample's color by its lateral leak β and hue filter T (the
	// neighbor model; see TDParams and voxel.EffectiveCellColors) — so the vertex
	// set varies per sample exactly as the dither's per-cell rule does. A
	// saturated translucent filament therefore can't fake-enclose interior body
	// colors it physically can't render per cell (defect 2). A uniform shift (or
	// no shift, when every filament is effectively opaque) can't reorder the
	// subsets being compared, so those cases keep the bit-identical nominal path
	// above. See tdSelectState.
	var tdState *tdSelectState
	if tdp.Enabled {
		neighborPath := float64(tdp.NeighborPathMM)
		if neighborPath <= 0 {
			neighborPath = DefaultNeighborPathMM
		}
		uniform := true
		anyLeak := false
		var first float64
		seen := false
		checkTD := func(td float32) {
			nt := normSelTD(td)
			if !seen {
				first = nt
				seen = true
			} else if nt != first {
				uniform = false
			}
			if NeighborLeak(nt, neighborPath) > 0 {
				anyLeak = true
			}
		}
		for _, e := range inventory {
			checkTD(e.TD)
		}
		for _, e := range locked {
			checkTD(e.TD)
		}
		if !uniform && anyLeak {
			kappa := float64(tdp.Kappa)
			tdState = newTDSelectState(inventory, locked, invLab, lockedLab, samples, neighborPath, kappa, dithering)
			// The search functions call scorer(indices, invLab, lockedLab,
			// samples); the TD scorer captures its own precomputed state and
			// ignores those args (invLab is still passed so the enumerators size
			// off len(inventory)).
			scorer = func(indices []int, _ [][3]float64, _ [][3]float64, _ []WeightedLabSample) float64 {
				return tdState.score(indices)
			}
			fmt.Printf("  TD-aware selection (per-sample eff): neighbor path %.2f mm, κ %.2f\n", neighborPath, kappa)
		}
	}

	combos := combinationsCount(len(inventory), n)
	// The budgeted exhaustive + multi-start VND search is enabled only for the
	// TD-aware scorer, which the hull-membership wash penalty hardened against
	// hull over-optimism (see tdSelectState.score). The plain nominal hull
	// scorer is NOT so hardened: aggressive global search there surfaces its
	// additive-mixing over-optimism — it prefers a big enclosing hull of
	// saturated extremes it cannot physically dither to (e.g. a landless
	// {Black, Yellow, Azure, Aqua} over {Green, Brown, White, Blue} for an earth
	// globe, ~20% lower score yet perceptually wrong) — which the greedy local
	// search fortunately steps around. Until the nominal scorer gets a matching
	// guard, keep it on the legacy greedy swap search (bit-identical to before).
	newSearch := tdState != nil
	exhaustiveThreshold := 2000
	if newSearch {
		exhaustiveThreshold = selectEvalBudget
	}
	exhaustive := combos <= exhaustiveThreshold

	start := time.Now()
	var bestSubset []int
	var evaluated int
	var err error
	if exhaustive {
		var counter atomic.Int64
		tracker.StageStart("Selecting colors", true, combos)
		done := make(chan struct{})
		exited := make(chan struct{})
		go func() {
			defer close(exited)
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					tracker.StageProgress("Selecting colors", int(counter.Load()))
				case <-done:
					tracker.StageProgress("Selecting colors", int(counter.Load()))
					return
				}
			}
		}()
		bestSubset, err = exhaustiveSearch(ctx, invLab, lockedLab, samples, n, scorer, &counter)
		close(done)
		<-exited
		evaluated = int(counter.Load())
		if err != nil {
			return nil, err
		}
		tracker.StageDone("Selecting colors")
		fmt.Printf("  Selected colors (%d evaluated) in %.1fs\n", evaluated, time.Since(start).Seconds())
	} else if newSearch {
		var hitCap bool
		bestSubset, evaluated, hitCap, err = multiStartVND(ctx, invLab, lockedLab, samples, n, scorer)
		if err != nil {
			return nil, err
		}
		capNote := ""
		if hitCap {
			capNote = " (eval cap hit; best-so-far)"
		}
		fmt.Printf("  Selected colors (%d evaluated, multi-start VND)%s in %.1fs\n", evaluated, capNote, time.Since(start).Seconds())
	} else {
		bestSubset, evaluated, err = greedySwapSearch(ctx, invLab, lockedLab, samples, n, scorer)
		if err != nil {
			return nil, err
		}
		fmt.Printf("  Selected colors (%d evaluated) in %.1fs\n", evaluated, time.Since(start).Seconds())
	}

	// Predicted-usage safety net: replace any free pick the dither would barely
	// place with a genuinely-used alternative of near-equal score (TD-aware path
	// only; see tdSelectState.refineUsage).
	if tdState != nil {
		bestSubset = tdState.refineUsage(bestSubset)
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
// gray is inside the {black,white,...} hull. It is modulated per-sample by
// chromaSpreadFalloff below — see weightedHullScore.
const ditherSpreadFactor = 0.3

// chromaSpreadFalloff is the Lab-chroma constant in the per-sample
// spread-penalty modulation:
//
//	effectiveSpread = ditherSpreadFactor * exp(-chroma/falloff)
//
// Low-chroma samples (chroma ≪ falloff) keep the full spread penalty,
// so grey cells anchor a grey palette vertex. High-chroma samples
// (chroma ≫ falloff) lose the spread penalty, so they happily vote
// for vivid palette vertices in their hue direction even when those
// vertices sit further out than any sample. This was the missing
// signal for the brick benchy: cells max out around chroma 30 and
// can't directly justify a chroma-70 Red, but they shouldn't be
// penalized for the palette having one — Red is exactly what their
// hue points at.
const chromaSpreadFalloff = 30.0

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

// nearestVertexDistChromaWeighted is nearestVertexDist with a per-sample
// discount on the L (lightness) axis. lightnessWeight is precomputed by
// the caller (it shares the same exp(-chroma/falloff) value used to
// modulate the spread magnitude — see weightedHullScore — and they're
// the same number expressing the same claim, so we don't recompute).
//
// Full lightness weight for achromatic samples means a grey body of a
// car needs a grey-axis palette vertex at matching lightness. Rapidly
// diminishing weight for chromatic samples means a brick at chroma 30
// doesn't care that Red sits at a different lightness, since dither
// can mix Red with neighboring colors to recover any lightness in its
// hue's column.
//
// The (a, b) terms are always full-weight: hue and chroma matching
// matter regardless of sample chroma — even a near-grey sample with
// a slight warm tint should anchor on a warm vertex over a cool one
// at the same lightness.
func nearestVertexDistChromaWeighted(p [3]float64, verts [][3]float64, lightnessWeight float64) float64 {
	best := math.MaxFloat64
	for _, v := range verts {
		d0 := (p[0] - v[0]) * lightnessWeight
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
	return hullScoreVerts(buildVerts(indices, invLab, fixedLab), samples)
}

// hullScoreVerts is weightedHullScore's geometric core over already-assembled
// Lab vertices.
func hullScoreVerts(verts [][3]float64, samples []WeightedLabSample) float64 {
	total := 0.0
	for _, s := range samples {
		hullDist := distToConvexHull(s.Lab, verts)
		// Standard CIELAB chroma; multiply by 100 to undo go-colorful's
		// 1/100 scaling so chromaSpreadFalloff is in familiar units.
		chroma := math.Sqrt(s.Lab[1]*s.Lab[1]+s.Lab[2]*s.Lab[2]) * 100
		// chromaKnee is the single perceptual claim: chromatic samples
		// don't constrain lightness. It's used twice — to suppress the
		// lightness component of the per-vertex distance, and to suppress
		// the spread magnitude itself — because both encode the same
		// fact about how dither can substitute for missing palette
		// vertices in the lightness direction.
		chromaKnee := math.Exp(-chroma / chromaSpreadFalloff)
		nearDist := nearestVertexDistChromaWeighted(s.Lab, verts, chromaKnee)
		spread := ditherSpreadFactor * chromaKnee
		d := hullDist + spread*nearDist
		total += d * d * s.Weight
	}
	return total
}

// weightedNearestScore computes total weighted squared distance from each
// sample to the nearest color in the full palette (locked + candidate).
// Used when dithering is disabled, since each cell gets exactly one color.
func weightedNearestScore(indices []int, invLab [][3]float64, fixedLab [][3]float64, samples []WeightedLabSample) float64 {
	return nearestScoreVerts(buildVerts(indices, invLab, fixedLab), samples)
}

// nearestScoreVerts is weightedNearestScore's geometric core over
// already-assembled Lab vertices.
func nearestScoreVerts(verts [][3]float64, samples []WeightedLabSample) float64 {
	total := 0.0
	for _, s := range samples {
		d := nearestVertexDist(s.Lab, verts)
		total += d * d * s.Weight
	}
	return total
}

// exhaustiveSearch enumerates all C(inv, n) subsets to find the one that
// subsetLess reports whether subset a sorts before b lexicographically.
// Used as a deterministic tie-break when two palette subsets score
// equally, so the parallel search result doesn't depend on goroutine
// scheduling. b == nil (no candidate yet) sorts after any real subset.
func subsetLess(a, b []int) bool {
	if b == nil {
		return true
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

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
				// On an exact score tie, keep the lexicographically smaller
				// subset so the result is independent of which worker
				// happened to evaluate it (otherwise equal-scoring palettes
				// — common for near-achromatic inputs — are picked by
				// goroutine scheduling, varying run to run).
				if s < localBest || (s == localBest && subsetLess(subset, localSubset)) {
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
		if r.subset == nil {
			continue
		}
		if r.score < bestScore || (r.score == bestScore && subsetLess(r.subset, bestSubset)) {
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
// pairwise swaps (the PAM k-medoids algorithm). Returns the best subset found
// and the total number of scoring calls made. This is the legacy search kept for
// the nominal (non-TD) scorer, whose over-optimism the more aggressive
// budgeted-exhaustive/VND search would surface (see SelectFromInventory).
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

// selectEvalBudget is the subset-evaluation budget for palette selection. When
// the number of candidate subsets C(inventory, n) fits within it, selection is
// exhaustive — the global optimum. Above it, exhaustive enumeration is
// infeasible (e.g. C(80, 6) ≈ 3e8) and selection falls back to the deterministic
// multi-start VND below. ~50k keeps a cache-missed selection to a few seconds
// (C(28, 4) = 20475 scores in ~10s parallelized) while covering typical
// collections exhaustively.
const selectEvalBudget = 50000

// numVNDAnchorStarts is how many farthest-point anchor starts multiStartVND runs
// in addition to the plain greedy start — enough diverse basins to escape the
// single-greedy local minima (the Magenta trap) without a large fixed cost.
const numVNDAnchorStarts = 10

// vndEvalCap hard-bounds the multi-start VND fallback's total scoring calls so a
// pathological inventory can't blow up runtime. On reaching it the search
// returns the best subset found so far.
const vndEvalCap = 500000

// greedyComplete extends the partial subset `seed` (distinct inventory indices,
// len ≤ n) to size n by repeatedly adding the not-yet-chosen candidate that most
// reduces the score. Deterministic: it scans candidates in index order and keeps
// the strictly-best, so the lowest index wins a score tie. Returns the completed
// subset (seed first, greedily-added members appended) and the scoring calls made.
func greedyComplete(ctx context.Context, invLab [][3]float64, lockedLab [][3]float64, samples []WeightedLabSample, n int, score scoreFunc, seed []int) ([]int, int, error) {
	invN := len(invLab)
	evaluated := 0
	selected := make([]int, len(seed), n)
	copy(selected, seed)
	inSubset := make([]bool, invN)
	for _, idx := range selected {
		inSubset[idx] = true
	}
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
	return selected, evaluated, nil
}

// farthestPointOrder returns all inventory indices in farthest-point-sampling
// order over their Lab colors: the first is the entry farthest from the Lab
// centroid (the most extreme color), then each subsequent entry maximizes its
// minimum distance to those already chosen. Deterministic — the lowest index
// breaks a distance tie. Used to seed diverse VND starts on distinct extremes.
func farthestPointOrder(invLab [][3]float64) []int {
	m := len(invLab)
	order := make([]int, 0, m)
	if m == 0 {
		return order
	}
	// First anchor: farthest from the Lab centroid (deterministic, extreme).
	var cen [3]float64
	for _, v := range invLab {
		cen[0] += v[0]
		cen[1] += v[1]
		cen[2] += v[2]
	}
	inv := 1.0 / float64(m)
	cen[0] *= inv
	cen[1] *= inv
	cen[2] *= inv
	first, bestD := 0, -1.0
	for i, v := range invLab {
		if d := dist3(v, cen); d > bestD {
			bestD, first = d, i
		}
	}
	used := make([]bool, m)
	order = append(order, first)
	used[first] = true
	minDist := make([]float64, m)
	for i := range minDist {
		minDist[i] = dist3(invLab[i], invLab[first])
	}
	for len(order) < m {
		next, nd := -1, -1.0
		for i := 0; i < m; i++ {
			if used[i] {
				continue
			}
			if minDist[i] > nd {
				nd, next = minDist[i], i
			}
		}
		order = append(order, next)
		used[next] = true
		for i := 0; i < m; i++ {
			if used[i] {
				continue
			}
			if d := dist3(invLab[i], invLab[next]); d < minDist[i] {
				minDist[i] = d
			}
		}
	}
	return order
}

// multiStartVND is the large-inventory fallback (C(inventory, n) > selectEvalBudget)
// where exhaustive enumeration is infeasible. It runs a deterministic Variable
// Neighborhood Descent (steepest 1-swap to a local optimum, then a first-improving
// 2-swap kick that returns to 1-swap) from several diverse starts — the plain
// greedy construction plus farthest-point anchor starts, each forcing a distinct
// extreme filament into the palette — and returns the best subset found. No RNG:
// identical inputs give identical output. Total scoring calls are bounded by
// vndEvalCap; hitCap reports whether that cap was reached (best-so-far returned).
func multiStartVND(ctx context.Context, invLab [][3]float64, lockedLab [][3]float64, samples []WeightedLabSample, n int, score scoreFunc) (best []int, evaluated int, hitCap bool, err error) {
	starts := make([][]int, 0, numVNDAnchorStarts+1)

	// Plain greedy construction.
	g, ev, gerr := greedyComplete(ctx, invLab, lockedLab, samples, n, score, nil)
	if gerr != nil {
		return nil, evaluated, false, gerr
	}
	evaluated += ev
	starts = append(starts, g)

	// Farthest-point anchor starts: force each distinct extreme in, complete greedily.
	order := farthestPointOrder(invLab)
	limit := numVNDAnchorStarts
	if limit > len(order) {
		limit = len(order)
	}
	for a := 0; a < limit && evaluated < vndEvalCap; a++ {
		s, ev, aerr := greedyComplete(ctx, invLab, lockedLab, samples, n, score, []int{order[a]})
		if aerr != nil {
			return nil, evaluated, hitCap, aerr
		}
		evaluated += ev
		starts = append(starts, s)
	}

	bestScore := math.MaxFloat64
	for _, st := range starts {
		if evaluated >= vndEvalCap {
			hitCap = true
			break
		}
		sub, sc, cap2, derr := vndDescent(ctx, invLab, lockedLab, samples, n, score, st, &evaluated)
		if derr != nil {
			return nil, evaluated, hitCap, derr
		}
		if cap2 {
			hitCap = true
		}
		// sub is sorted ascending; subsetLess gives a deterministic tie-break.
		if sc < bestScore || (sc == bestScore && subsetLess(sub, best)) {
			bestScore = sc
			best = sub
		}
	}
	return best, evaluated, hitCap, nil
}

// vndDescent runs Variable Neighborhood Descent from `start`: steepest
// (best-improvement) 1-swaps until no single swap improves, then a first-improving
// 2-swap (eject 2 members, insert 2 candidates) that, on finding an improvement,
// returns to 1-swap descent; it stops when a full 2-swap sweep finds nothing.
// Every scan is in fixed index order so the result is deterministic. It shares
// the *evaluated counter and returns the best subset so far once vndEvalCap is
// reached. Returns the local-optimum subset (sorted ascending), its score,
// whether the cap was hit, and any ctx error.
func vndDescent(ctx context.Context, invLab [][3]float64, lockedLab [][3]float64, samples []WeightedLabSample, n int, score scoreFunc, start []int, evaluated *int) ([]int, float64, bool, error) {
	invN := len(invLab)
	cur := make([]int, len(start))
	copy(cur, start)
	inSubset := make([]bool, invN)
	for _, idx := range cur {
		inSubset[idx] = true
	}
	doScore := func(sub []int) float64 {
		*evaluated++
		return score(sub, invLab, lockedLab, samples)
	}
	curScore := doScore(cur)

	for {
		if ctx.Err() != nil {
			return nil, 0, false, ctx.Err()
		}
		// Steepest 1-swap descent to a local optimum.
		for {
			if ctx.Err() != nil {
				return nil, 0, false, ctx.Err()
			}
			bestScore := curScore
			bestSi, bestCi := -1, -1
			for si := 0; si < n; si++ {
				old := cur[si]
				for ci := 0; ci < invN; ci++ {
					if inSubset[ci] {
						continue
					}
					cur[si] = ci
					if s := doScore(cur); s < bestScore {
						bestScore, bestSi, bestCi = s, si, ci
					}
				}
				cur[si] = old
			}
			if bestSi < 0 || *evaluated >= vndEvalCap {
				if bestSi >= 0 { // apply the found improvement before capping out
					inSubset[cur[bestSi]] = false
					cur[bestSi] = bestCi
					inSubset[bestCi] = true
					curScore = bestScore
				}
				break
			}
			inSubset[cur[bestSi]] = false
			cur[bestSi] = bestCi
			inSubset[bestCi] = true
			curScore = bestScore
		}
		if *evaluated >= vndEvalCap {
			sort.Ints(cur)
			return cur, curScore, true, nil
		}

		// First-improving 2-swap; on success return to 1-swap descent.
		improved, capHit, cerr := vndTwoSwap(ctx, invLab, lockedLab, samples, n, score, cur, inSubset, &curScore, evaluated)
		if cerr != nil {
			return nil, 0, false, cerr
		}
		if capHit {
			sort.Ints(cur)
			return cur, curScore, true, nil
		}
		if !improved {
			break
		}
	}
	sort.Ints(cur)
	return cur, curScore, false, nil
}

// vndTwoSwap performs one first-improving 2-swap sweep on cur: eject two members
// and insert two non-members. Ejection pairs are tried worst-marginal-contribution
// first (each member's marginal = the score increase when it alone is removed, so
// the least-useful pair is tried first and an improving move is usually found
// early); insert pairs are scanned in index order. The first trial that strictly
// beats *curScore is applied in place (cur, inSubset, *curScore updated) and the
// function returns improved=true. All ordering is deterministic. It shares
// *evaluated and reports capHit when vndEvalCap is reached mid-sweep.
func vndTwoSwap(ctx context.Context, invLab [][3]float64, lockedLab [][3]float64, samples []WeightedLabSample, n int, score scoreFunc, cur []int, inSubset []bool, curScore *float64, evaluated *int) (bool, bool, error) {
	invN := len(invLab)
	doScore := func(sub []int) float64 {
		*evaluated++
		return score(sub, invLab, lockedLab, samples)
	}

	// Marginal contribution of each member = score increase when it is removed.
	contrib := make([]float64, n)
	reduced := make([]int, 0, n-1)
	for si := 0; si < n; si++ {
		reduced = reduced[:0]
		for k, idx := range cur {
			if k != si {
				reduced = append(reduced, idx)
			}
		}
		contrib[si] = doScore(reduced) - *curScore
		if *evaluated >= vndEvalCap {
			return false, true, nil
		}
	}

	// Ejection pairs (si<sj), least-useful (smallest summed contribution) first;
	// deterministic tie-break by (si, sj).
	type ejectPair struct {
		i, j int
		key  float64
	}
	pairs := make([]ejectPair, 0, n*(n-1)/2)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			pairs = append(pairs, ejectPair{i, j, contrib[i] + contrib[j]})
		}
	}
	sort.Slice(pairs, func(a, b int) bool {
		if pairs[a].key != pairs[b].key {
			return pairs[a].key < pairs[b].key
		}
		if pairs[a].i != pairs[b].i {
			return pairs[a].i < pairs[b].i
		}
		return pairs[a].j < pairs[b].j
	})

	cand := make([]int, 0, invN-n)
	for ci := 0; ci < invN; ci++ {
		if !inSubset[ci] {
			cand = append(cand, ci)
		}
	}

	trial := make([]int, n)
	for _, pr := range pairs {
		if ctx.Err() != nil {
			return false, false, ctx.Err()
		}
		oldI, oldJ := cur[pr.i], cur[pr.j]
		for a := 0; a < len(cand); a++ {
			for b := a + 1; b < len(cand); b++ {
				// Score is set-based, so ci→slot i / cj→slot j yields the same
				// palette as the reverse — one assignment per candidate pair.
				copy(trial, cur)
				trial[pr.i], trial[pr.j] = cand[a], cand[b]
				if s := doScore(trial); s < *curScore {
					inSubset[oldI] = false
					inSubset[oldJ] = false
					cur[pr.i], cur[pr.j] = cand[a], cand[b]
					inSubset[cand[a]] = true
					inSubset[cand[b]] = true
					*curScore = s
					return true, false, nil
				}
				if *evaluated >= vndEvalCap {
					return false, true, nil
				}
			}
		}
	}
	return false, false, nil
}

// combinationsCount returns C(n, k). To stay overflow-safe for large
// inventories it saturates at math.MaxInt32 (any value well above
// selectEvalBudget serves the exhaustive-vs-VND decision identically), so a
// huge C(n, k) can never wrap negative and masquerade as "small".
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
	const saturate = math.MaxInt32
	result := 1
	for i := 0; i < k; i++ {
		result = result * (n - i) / (i + 1)
		if result > saturate || result < 0 {
			return saturate
		}
	}
	return result
}
