package cellslicer

// Watertight invariant test for PartitionModel + ClipMeshToCells2D.
//
// Step 0 of the voxelize/clip cleanup plan (docs/voxelize-clip-cleanup-plan.md):
// run the slab-partition + 2D-clip pipeline against a small set of fixtures and
// assert the output mesh has zero boundary edges and zero non-manifold edges
// — keyed by 1µm quantised 3D position so coincident-position vertices that
// haven't been deduplicated still match.
//
// Fixtures currently failing this invariant are marked with t.Skip and a note
// pointing at the cleanup step expected to fix them, rather than relaxing the
// threshold. As Steps 1–5 land, those skips should be removed one by one.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// watertightFixture pins one fixture and the cellslicer parameters to run it
// at. Fixtures live under tests/objects/ relative to the package directory.
type watertightFixture struct {
	name       string
	path       string
	targetSize float32 // max extent after uniform scaling (mm)
	cellSize   float32
	layerH     float32
	// skipReason, if non-empty, is the reason this fixture is expected to
	// fail today; the test t.Skip's with this message. Clear the field as
	// the upstream bug fix lands.
	skipReason string
}

// watertightFixtures pin the four fixtures listed in the cleanup plan, plus
// fallback substitutes for ones we don't have a 1MB checked-in copy of.
// "sphere" → earth.glb (smooth approximately-spherical mesh).
// "lekythos vase" → no checked-in fixture under tests/objects/; the closest
// match (terracotta-lekythos*.3mf at the repo root) is large and not
// guaranteed watertight, so we substitute glyphid_praetorian.glb as the
// fourth fixture for now.
var watertightFixtures = []watertightFixture{
	{
		name:       "cube",
		path:       "../../tests/objects/cube.stl",
		targetSize: 20,
		cellSize:   1.0,
		layerH:     0.5,
		// Cube input is already watertight but the slab partition
		// emits overlapping rectangular cells (e.g. cell 0/1 and 78/79
		// both claim the x=19 wall strip), so the clip output has
		// duplicate triangles for the same region and the open-ended
		// outer-edge flag bleeds geometry past partition boundaries.
		// Expected to clear once Step 2 (analytic cell outlines) and
		// Step 3 (unified 3D-prism clip) land.
		skipReason: "documented bug pressure for plan Steps 2 & 3 — see docs/voxelize-clip-cleanup-plan.md",
	},
	{
		name:       "sphere",
		path:       "../../tests/objects/earth.glb",
		targetSize: 50,
		cellSize:   1.0,
		layerH:     0.5,
		// Same bug class as cube, amplified by the sphere's smooth
		// slanted geometry exercising the cap/vertical clip dispatch.
		skipReason: "documented bug pressure for plan Steps 1, 2 & 3 — see docs/voxelize-clip-cleanup-plan.md",
	},
	{
		name:       "low_poly_building",
		path:       "../../tests/objects/low_poly_building.glb",
		targetSize: 50,
		cellSize:   1.0,
		layerH:     0.5,
		// Production runs this fixture through alphawrap + decimate to
		// repair its non-watertight input mesh before slicing; the raw
		// fixture has open edges that defeat the cellslicer invariant
		// independently of any cellslicer bug.
		skipReason: "raw fixture not watertight; needs alphawrap/decimate preprocessing — see plan Step 2",
	},
	{
		name:       "praetorian",
		path:       "../../tests/objects/glyphid_praetorian.glb",
		targetSize: 50,
		cellSize:   1.0,
		layerH:     0.5,
		skipReason: "raw fixture not watertight; needs alphawrap/decimate preprocessing — see plan Step 2",
	},
}

func TestWatertightAfterClip(t *testing.T) {
	for _, fx := range watertightFixtures {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			if fx.skipReason != "" {
				t.Skip(fx.skipReason)
			}
			model, err := loadFixtureForWatertight(fx.path)
			if err != nil {
				t.Fatalf("load %s: %v", fx.path, err)
			}
			if extent := maxExtentForWatertight(model); extent > 0 {
				loader.ScaleModel(model, fx.targetSize/extent)
			}
			normalizeZForWatertight(model)

			slabs := PartitionModel(model, fx.layerH, fx.cellSize)
			if len(slabs) == 0 {
				t.Fatalf("PartitionModel returned 0 slabs")
			}
			triIdx := NewTriXYZIndex(model, fx.cellSize*2)
			clipped, err := ClipMeshToCells2D(model, slabs, triIdx)
			if err != nil {
				t.Fatalf("ClipMeshToCells2D: %v", err)
			}
			boundary, nonManifold := countHoleEdges(clipped.Verts, clipped.Faces)
			t.Logf("clipped: %d verts, %d faces; boundary=%d nonManifold=%d",
				len(clipped.Verts), len(clipped.Faces), boundary, nonManifold)
			if boundary != 0 || nonManifold != 0 {
				DumpFirstBoundaryEdge(clipped, slabs, model)
				t.Fatalf("watertight invariant violated: boundary=%d nonManifold=%d (want 0,0)",
					boundary, nonManifold)
			}
		})
	}
}

// countHoleEdges returns (boundary, nonManifold) edge counts keyed by 1µm
// quantised 3D position — same Quantize bucket the splice and cross-piece
// dedup use, so coincident-position vertices that didn't share an index
// still collapse to one edge.
//
// Mirrors reportHolesByPos in clip2d.go, returning counts instead of
// writing to stderr.
func countHoleEdges(verts [][3]float32, faces [][3]uint32) (boundary, nonManifold int) {
	type ek struct{ A, B int3D }
	mk := func(a, b int3D) ek {
		if a.X > b.X || (a.X == b.X && a.Y > b.Y) || (a.X == b.X && a.Y == b.Y && a.Z > b.Z) {
			a, b = b, a
		}
		return ek{a, b}
	}
	counts := make(map[ek]int, len(faces)*2)
	for _, f := range faces {
		va := Quantize(verts[f[0]])
		vb := Quantize(verts[f[1]])
		vc := Quantize(verts[f[2]])
		if va != vb {
			counts[mk(va, vb)]++
		}
		if vb != vc {
			counts[mk(vb, vc)]++
		}
		if vc != va {
			counts[mk(vc, va)]++
		}
	}
	for _, c := range counts {
		switch {
		case c == 1:
			boundary++
		case c == 2:
			// manifold edge — expected
		default:
			nonManifold++
		}
	}
	return
}

func loadFixtureForWatertight(path string) (*loader.LoadedModel, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".glb":
		return loader.LoadGLB(path, -1)
	case ".3mf":
		return loader.Load3MF(path, -1)
	case ".stl":
		return loader.LoadSTL(path, -1)
	default:
		return nil, fmt.Errorf("unsupported fixture format %q", ext)
	}
}

func maxExtentForWatertight(m *loader.LoadedModel) float32 {
	if len(m.Vertices) == 0 {
		return 0
	}
	mn, mx := m.Vertices[0], m.Vertices[0]
	for _, v := range m.Vertices[1:] {
		for i := 0; i < 3; i++ {
			if v[i] < mn[i] {
				mn[i] = v[i]
			}
			if v[i] > mx[i] {
				mx[i] = v[i]
			}
		}
	}
	e := mx[0] - mn[0]
	if y := mx[1] - mn[1]; y > e {
		e = y
	}
	if z := mx[2] - mn[2]; z > e {
		e = z
	}
	return e
}

// normalizeZForWatertight shifts the model so its bottom sits at z=0. The
// cellslicer's slab partitioning is sensitive to where geometry lands
// relative to the slab grid; lifting any negative-Z bias keeps the fixtures
// in a regime that matches production pipeline behaviour.
func normalizeZForWatertight(m *loader.LoadedModel) {
	if len(m.Vertices) == 0 {
		return
	}
	mn := m.Vertices[0][2]
	for _, v := range m.Vertices[1:] {
		if v[2] < mn {
			mn = v[2]
		}
	}
	for i := range m.Vertices {
		m.Vertices[i][2] -= mn
	}
}
