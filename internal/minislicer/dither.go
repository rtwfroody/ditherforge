package minislicer

import (
	"context"

	"github.com/rtwfroody/ditherforge/internal/progress"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// DitherSections runs palette resolution and error-diffusion dither
// across the section graph. Returns the palette and a per-section
// palette index aligned to `sections`.
//
// `alpha[i] == false` excludes section i from both palette selection
// and dithering (its assignment is set to -1). neighbors must be the
// adjacency produced by BuildSectionGraph.
//
// layerH is the layer height; it scales the per-section weight in
// palette voting and dither error mass to "ribbon area"
// (length * layerH) so longer / thicker sections dominate
// proportionally — same idea as the area-weighted voxel pipeline.
func DitherSections(
	ctx context.Context,
	sections []Section,
	colors [][3]uint8,
	alpha []bool,
	neighbors [][]voxel.Neighbor,
	pcfg voxel.PaletteConfig,
	layerH float32,
	tracker progress.Tracker,
) (pal [][3]uint8, palLabels []string, paletteSourceLabel string, assignments []int32, err error) {
	if tracker == nil {
		tracker = progress.NullTracker{}
	}

	// Build ActiveCells for the visible (alpha) sections. Keep a
	// remap so we can scatter dither results back to the full
	// `sections` array.
	cells := make([]voxel.ActiveCell, 0, len(sections))
	visibleToFull := make([]int, 0, len(sections))
	fullToVisible := make([]int, len(sections))
	for i := range sections {
		fullToVisible[i] = -1
	}
	for i, s := range sections {
		if !alpha[i] {
			continue
		}
		fullToVisible[i] = len(cells)
		visibleToFull = append(visibleToFull, i)
		cells = append(cells, voxel.ActiveCell{
			Cx:    s.Mid[0],
			Cy:    s.Mid[1],
			Cz:    s.Z,
			Color: colors[i],
			Area:  s.Length * layerH,
		})
	}

	if len(cells) == 0 {
		return nil, nil, "", make([]int32, len(sections)), nil
	}

	pal, palLabels, paletteSourceLabel, err = voxel.ResolvePalette(ctx, cells, pcfg, true, tracker)
	if err != nil {
		return nil, nil, "", nil, err
	}

	// Reindex neighbors from full-section indices to visible-cell
	// indices, dropping any neighbor whose section is alpha=false.
	visNeigh := make([][]voxel.Neighbor, len(cells))
	for fi, ns := range neighbors {
		vi := fullToVisible[fi]
		if vi < 0 {
			continue
		}
		var out []voxel.Neighbor
		for _, n := range ns {
			vj := fullToVisible[n.Idx]
			if vj < 0 {
				continue
			}
			out = append(out, voxel.Neighbor{Idx: vj, Weight: n.Weight})
		}
		visNeigh[vi] = out
	}

	visAssign, err := voxel.DitherWithNeighbors(ctx, cells, pal, visNeigh, tracker)
	if err != nil {
		return nil, nil, "", nil, err
	}

	assignments = make([]int32, len(sections))
	for i := range assignments {
		assignments[i] = -1
	}
	for vi, fi := range visibleToFull {
		assignments[fi] = visAssign[vi]
	}
	return pal, palLabels, paletteSourceLabel, assignments, nil
}
