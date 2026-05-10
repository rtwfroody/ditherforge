package minislicer

import "fmt"

// PatchReport summarizes the result of VerifyPatchLengths for one
// layer.
type PatchReport struct {
	LayerIdx       int
	NumPatches     int
	NumPatchesShort int     // patches with total arc length < cellSize
	MinLen         float32 // shortest patch in this layer
	MaxLen         float32 // longest patch in this layer
}

// VerifyPatchLengths walks each layer, groups consecutive same-color
// sections within each loop into "patches," and reports any patch
// shorter than cellSize. The success criterion ("each colored patch
// is at least as long as a current XY voxel") is satisfied iff
// SumNumShort across the returned reports == 0.
//
// "Shortness" is exempted for patches that are an entire degenerate
// loop with perimeter < cellSize: a tiny island can't be partitioned
// finer than itself, and the slicer would drop it regardless. Those
// are excluded from NumPatchesShort and the boolean ok return.
func VerifyPatchLengths(sections []Section, layers []Layer, assignments []int32, cellSize float32) (reports []PatchReport, ok bool) {
	type loopKey struct{ layer, loop int }
	loopSecs := make(map[loopKey][]int)
	for i, s := range sections {
		loopSecs[loopKey{s.LayerIdx, s.LoopIdx}] = append(loopSecs[loopKey{s.LayerIdx, s.LoopIdx}], i)
	}
	for _, ids := range loopSecs {
		// Sort by Index to walk the loop in arc order.
		sortByIndex(ids, sections)
	}

	reports = make([]PatchReport, len(layers))
	for i := range reports {
		reports[i].LayerIdx = layers[i].LayerIdx
	}

	ok = true
	for li, layer := range layers {
		rep := &reports[li]
		rep.MinLen = float32(1e30)
		for lp := range layer.Loops {
			ids := loopSecs[loopKey{layer.LayerIdx, lp}]
			if len(ids) == 0 {
				continue
			}
			// Compute loop perimeter from sections (sum of lengths).
			var perim float32
			for _, id := range ids {
				perim += sections[id].Length
			}
			// Walk the cyclic sequence, grouping by color. We start
			// at a boundary between different colors so groups are
			// contiguous arcs (vs. wrapping around with the same
			// color spanning the seam). If all sections share a
			// color, the whole loop is one patch.
			startK := findColorBoundary(ids, assignments)
			n := len(ids)
			cur := startK
			for steps := 0; steps < n; {
				color := assignments[ids[cur]]
				var grpLen float32
				var grpSize int
				for steps < n && assignments[ids[(cur+grpSize)%n]] == color {
					grpLen += sections[ids[(cur+grpSize)%n]].Length
					grpSize++
					steps++
				}
				rep.NumPatches++
				if grpLen < rep.MinLen {
					rep.MinLen = grpLen
				}
				if grpLen > rep.MaxLen {
					rep.MaxLen = grpLen
				}
				if grpLen < cellSize-1e-5 && perim >= cellSize {
					rep.NumPatchesShort++
					ok = false
				}
				cur = (cur + grpSize) % n
			}
		}
		if rep.NumPatches == 0 {
			rep.MinLen = 0
		}
	}
	return reports, ok
}

// findColorBoundary returns an index into ids where the color
// transitions (i.e., assignments[ids[i-1]] != assignments[ids[i]]).
// If no such boundary exists (all same color), returns 0.
func findColorBoundary(ids []int, assignments []int32) int {
	n := len(ids)
	for i := 0; i < n; i++ {
		prev := (i - 1 + n) % n
		if assignments[ids[i]] != assignments[ids[prev]] {
			return i
		}
	}
	return 0
}

// sortByIndex sorts ids by sections[id].Index ascending. Insertion
// sort because section counts per loop are typically small.
func sortByIndex(ids []int, sections []Section) {
	for i := 1; i < len(ids); i++ {
		j := i
		for j > 0 && sections[ids[j-1]].Index > sections[ids[j]].Index {
			ids[j-1], ids[j] = ids[j], ids[j-1]
			j--
		}
	}
}

// FormatReport returns a human-readable summary of patch reports.
func FormatReport(reports []PatchReport, cellSize float32) string {
	var totalPatches, totalShort int
	var globalMin float32 = 1e30
	for _, r := range reports {
		totalPatches += r.NumPatches
		totalShort += r.NumPatchesShort
		if r.NumPatches > 0 && r.MinLen < globalMin {
			globalMin = r.MinLen
		}
	}
	if globalMin == 1e30 {
		globalMin = 0
	}
	return fmt.Sprintf(
		"%d layers, %d patches total, %d shorter than cellSize=%.3f (min patch length: %.3f)",
		len(reports), totalPatches, totalShort, cellSize, globalMin)
}
