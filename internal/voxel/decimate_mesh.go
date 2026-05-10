package voxel

import (
	"context"
	"fmt"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/plog"
	"github.com/rtwfroody/ditherforge/internal/progress"
)

// DecimateMesh runs the QEM decimator on a single LoadedModel,
// returning a new model with simplified geometry. Wrapper around
// Decimate that handles progress tracking, the noSimplify shortcut,
// and the alpha-aware face filter (transparent faces are not
// considered for collapse).
//
// Moved from internal/squarevoxel during the minislicer rearchitecture
// (it's a general-purpose decimator, not voxel-grid-specific).
func DecimateMesh(ctx context.Context, model *loader.LoadedModel, targetCells int, cellSize float32, errorBudget float64, noSimplify bool, tracker progress.Tracker) (*loader.LoadedModel, error) {
	if noSimplify {
		tracker.StageStart("Decimating", false, 0)
		tracker.StageDone("Decimating")
		return model, nil
	}

	var opaqueFaces [][3]uint32
	for fi := range model.Faces {
		if FaceAlpha(fi, model) >= 128 {
			opaqueFaces = append(opaqueFaces, model.Faces[fi])
		}
	}
	if len(opaqueFaces) <= targetCells && errorBudget <= 0 {
		tracker.StageStart("Decimating", false, 0)
		tracker.StageDone("Decimating")
		return model, nil
	}
	tracker.StageStart("Decimating", true, len(opaqueFaces)-targetCells)
	defer tracker.StageDone("Decimating")

	decVerts, decFaces, err := Decimate(ctx, model.Vertices, opaqueFaces, targetCells, float64(cellSize), errorBudget, tracker)
	if err != nil {
		return nil, err
	}
	wr := CheckWatertight(decFaces)
	plog.Printf("  Decimated mesh: %s", wr)
	return &loader.LoadedModel{
		Vertices: decVerts,
		Faces:    decFaces,
	}, nil
}

// DecimateHalves runs DecimateMesh once per Split half, splitting the
// total target cell count between halves proportional to each half's
// face count.
func DecimateHalves(ctx context.Context, halves [2]*loader.LoadedModel, totalTargetCells int, cellSize float32, errorBudget float64, noSimplify bool, tracker progress.Tracker) ([2]*loader.LoadedModel, error) {
	totalFaces := len(halves[0].Faces) + len(halves[1].Faces)
	var out [2]*loader.LoadedModel
	for i, h := range halves {
		perHalfTarget := totalTargetCells * len(h.Faces) / totalFaces
		if perHalfTarget < 1 {
			perHalfTarget = 1
		}
		decimated, err := DecimateMesh(ctx, h, perHalfTarget, cellSize, errorBudget, noSimplify, tracker)
		if err != nil {
			return out, fmt.Errorf("decimate half %d: %w", i, err)
		}
		out[i] = decimated
	}
	return out, nil
}
