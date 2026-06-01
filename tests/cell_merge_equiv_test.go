package tests

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/debugrender"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
)

// triArea returns the area of mesh face f.
func triArea(m *pipeline.MeshData, f int) float64 {
	i0, i1, i2 := m.Faces[3*f], m.Faces[3*f+1], m.Faces[3*f+2]
	ax, ay, az := m.Vertices[3*i0], m.Vertices[3*i0+1], m.Vertices[3*i0+2]
	bx, by, bz := m.Vertices[3*i1], m.Vertices[3*i1+1], m.Vertices[3*i1+2]
	cx, cy, cz := m.Vertices[3*i2], m.Vertices[3*i2+1], m.Vertices[3*i2+2]
	ux, uy, uz := bx-ax, by-ay, bz-az
	vx, vy, vz := cx-ax, cy-ay, cz-az
	nx, ny, nz := uy*vz-uz*vy, uz*vx-ux*vz, ux*vy-uy*vx
	return 0.5 * math.Sqrt(float64(nx*nx+ny*ny+nz*nz))
}

// meshSurfaceArea returns the total triangle surface area of the mesh.
// Re-triangulating a surface (what merging does) leaves total area
// unchanged, so merged and per-cell clips of the same model must report
// the same total. A hole — surface the merge failed to emit — shows up
// here as missing area, which the normalized colorAreaFractions metric
// cannot see (it divides it back out).
func meshSurfaceArea(m *pipeline.MeshData) float64 {
	var total float64
	for f := 0; f < len(m.Faces)/3; f++ {
		total += triArea(m, f)
	}
	return total
}

// colorAreaFractions returns, per face color (RGB triple), that color's
// fraction of the mesh's total triangle surface area. Triangulation-
// independent: merging same-color faces leaves each color's total area
// unchanged, so this is invariant between the merged and per-cell clips.
func colorAreaFractions(m *pipeline.MeshData) map[[3]uint16]float64 {
	area := map[[3]uint16]float64{}
	var total float64
	for f := 0; f < len(m.Faces)/3; f++ {
		a := triArea(m, f)
		col := [3]uint16{m.FaceColors[3*f], m.FaceColors[3*f+1], m.FaceColors[3*f+2]}
		area[col] += a
		total += a
	}
	if total == 0 {
		return area
	}
	for c := range area {
		area[c] /= total
	}
	return area
}

// TestCellMergeMatchesPerCell validates the same-color cell merge in the
// Clip stage (ClipMeshToMergedCellsManifold, opt-in via CellMerge)
// against the default per-cell clip. Merging groups same-palette cells
// per slab and clips them as one prism; because the grouping key is the
// dithered palette index, the real palette-coloured output must be
// visually identical to the per-cell clip — only the triangulation
// differs (fewer, larger faces, no internal same-color seams).
//
// This is the merged path's correctness guard: TestSampledMatchesInput
// runs in ShowSampledColors mode, which forces the per-cell clip (the
// sampled-colour diagnostic needs per-cell face provenance that merging
// coarsens), so it never exercises merging. Here ShowSampledColors is
// off, so the genuine merged output is rendered and compared.
func TestCellMergeMatchesPerCell(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (-short)")
	}

	cases := []struct {
		name      string
		path      string
		alphaWrap bool // building.glb is non-watertight; needs the wrap
	}{
		{"cube", filepath.Join("objects", "cube.stl"), false},
		{"earth", filepath.Join("objects", "earth.glb"), false},
		{"building", filepath.Join("objects", "low_poly_building.glb"), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			size := float32(50)
			// Lock the full palette so selection is deterministic.
			// ResolvePalette returns locked colors verbatim when
			// NumColors == len(LockedColors) (no inventory search), so both
			// clip runs dither against the identical palette. Without this,
			// palette selection is non-deterministic and the two runs can
			// pick different colors, making the area-fraction comparison
			// flaky (the test has no disk cache to share the palette between
			// runs). The inventory is supplied but unused at this lock count.
			lockedPalette := lockedTestPalette()
			base := pipeline.Options{
				Input:           tc.path,
				ObjectIndex:     -1,
				NumColors:       len(lockedPalette),
				LockedColors:    lockedPalette,
				InventoryColors: inventoryRGB(),
				InventoryLabels: inventoryLabels(),
				NozzleDiameter:  0.4,
				LayerHeight:     0.2,
				Dither:          "riemersma",
				ColorSnap:       5,
				Force:           true,
				Scale:           1,
				Size:            &size,
				AlphaWrap:       tc.alphaWrap,
				// ShowSampledColors stays false: we want the real
				// palette-coloured output, with merging actually active.
			}

			// NewStageCache has no disk tier, so nothing is actually
			// reused between the two runs — each recomputes the whole
			// pipeline. Equivalence holds because LockedColors pins the
			// palette (see base), not because upstream is shared.
			cache := pipeline.NewStageCache()
			run := func(cellMerge bool) *pipeline.MeshData {
				opts := base
				opts.CellMerge = cellMerge
				pr, err := pipeline.RunCached(context.Background(), cache, opts, nil)
				if err != nil {
					t.Fatalf("%s cellMerge=%v: RunCached: %v", tc.name, cellMerge, err)
				}
				if pr.OutputMesh == nil {
					t.Fatalf("%s cellMerge=%v: OutputMesh is nil", tc.name, cellMerge)
				}
				return pr.OutputMesh
			}

			merged := run(true)
			perCell := run(false)

			mergedTris := len(merged.Faces) / 3
			perCellTris := len(perCell.Faces) / 3
			// Merging must reduce triangle count (its whole point);
			// otherwise the optimisation isn't doing anything.
			if mergedTris >= perCellTris {
				t.Errorf("%s: merged output has %d triangles, not fewer than per-cell %d",
					tc.name, mergedTris, perCellTris)
			}
			t.Logf("%s: triangles merged=%d per-cell=%d (%.1f%% of per-cell)",
				tc.name, mergedTris, perCellTris,
				100*float64(mergedTris)/float64(perCellTris))

			// Total surface area must match: merging only re-triangulates a
			// surface, so it can't change how much surface there is. This is
			// the hole guard — a hole (surface the merge dropped) removes
			// area here, which the normalized per-color metric below divides
			// out and cannot detect. Both clips nudge open edges outward by
			// the same fixed OpenEdgeBloatMM margin, so the silhouettes land
			// in the same place; the only residual is floating-point noise in
			// the Manifold booleans (measured ≤14 ppm across cube/earth/
			// building). The 0.05% limit is well above that FP floor and far
			// below a real hole (which ran ~3%). An earlier cell-scale bloat
			// made the merged silhouette diverge ~3% here; that's gone.
			mArea := meshSurfaceArea(merged)
			pArea := meshSurfaceArea(perCell)
			relArea := math.Abs(mArea-pArea) / pArea
			t.Logf("%s: total surface area merged=%.2f per-cell=%.2f (rel diff %.5f)",
				tc.name, mArea, pArea, relArea)
			if relArea > 0.0005 {
				t.Errorf("%s: total surface area diverges by %.5f (>0.05%%): merged=%.2f per-cell=%.2f — merge dropped or added surface (hole?)",
					tc.name, relArea, mArea, pArea)
			}

			// Semantic equivalence: merging re-triangulates within a
			// color but must not move surface to a different color, so the
			// area-weighted palette distribution is invariant. This is the
			// right metric — a per-pixel render diff is fooled by the
			// high-frequency dither speckle, whose edges rasterise to
			// different pixels under the two (different) triangulations
			// even when the printed result is identical. A real
			// color-scrambling regression (the bug this guards against)
			// moved large area between colors; this catches that.
			ma := colorAreaFractions(merged)
			pa := colorAreaFractions(perCell)
			colors := map[[3]uint16]struct{}{}
			for c := range ma {
				colors[c] = struct{}{}
			}
			for c := range pa {
				colors[c] = struct{}{}
			}
			var maxDiff float64
			var worst [3]uint16
			for c := range colors {
				d := ma[c] - pa[c]
				if d < 0 {
					d = -d
				}
				if d > maxDiff {
					maxDiff, worst = d, c
				}
			}
			t.Logf("%s: max per-color area-fraction diff = %.5f at RGB(%d,%d,%d)",
				tc.name, maxDiff, worst[0], worst[1], worst[2])
			// 0.05% absolute. With both clips bloating open edges by the same
			// fixed margin, the per-color distribution is invariant to
			// floating-point noise (measured 0.0000 across all three models).
			// A real color-scrambling regression — the bug this guards
			// against — moved large area between colors (~10%); this catches
			// that with a wide margin.
			if maxDiff > 0.0005 {
				t.Errorf("%s: per-color area fraction diverges by %.5f (>0.05%%) at RGB(%d,%d,%d) — merging moved surface between colors",
					tc.name, maxDiff, worst[0], worst[1], worst[2])
			}

			// Optional visual dump for a human cross-check.
			if dumpDir := os.Getenv("DF_TEST_DUMP_DIR"); dumpDir != "" {
				_ = os.MkdirAll(dumpDir, 0o755)
				for _, v := range debugrender.DefaultViews {
					mImg := debugrender.RenderPipelineMeshCulled(merged, v, 256).ToRGBA()
					pImg := debugrender.RenderPipelineMeshCulled(perCell, v, 256).ToRGBA()
					_ = debugrender.WritePNG(filepath.Join(dumpDir, tc.name+"_merged_"+v.Name+".png"), mImg)
					_ = debugrender.WritePNG(filepath.Join(dumpDir, tc.name+"_percell_"+v.Name+".png"), pImg)
				}
			}
		})
	}
}

// lockedTestPalette is a fixed, well-spread 6-colour palette used to make
// pipeline tests deterministic. Passed as opts.LockedColors with
// NumColors == len, it forces ResolvePalette to return these colours
// verbatim (no inventory selection), so palette choice — which is
// otherwise non-deterministic run-to-run — can't make output comparisons
// flaky. The spread (neutrals + R/G/B + yellow) keeps every test model
// dithering across several colours.
func lockedTestPalette() []string {
	return []string{
		"#FFFFFF", // white
		"#7F7F7F", // grey
		"#000000", // black
		"#C03020", // red
		"#2080C0", // blue
		"#E0C020", // yellow
	}
}
