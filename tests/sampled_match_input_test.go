package tests

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/debugrender"
	"github.com/rtwfroody/ditherforge/internal/pipeline"
)

// TestSampledMatchesInput runs the pipeline in ShowSampledColors
// mode and asserts that the rendered output is visually close to
// the rendered input mesh. "Close" is per-pixel mean absolute
// error in RGB over the overlapping silhouette.
//
// The ShowSampledColors mode skips the dither step and paints
// each visible face with the raw RGB sampled from the model at
// that face's section midpoint, so a faithful pipeline should
// reproduce the input model's color distribution closely.
// Differences mostly come from:
//   - slicer step-quantization in Z (small)
//   - cap sampling picking the wrong nearby triangle (the bug
//     this test exists to catch — visible as wrong colors on
//     wide bands of geometry)
//
// Resolution and size are kept small so the test runs without
// eating gigabytes of RAM.
func TestSampledMatchesInput(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (-short)")
	}

	cases := []struct {
		name         string
		path         string
		maxAvgErr    float64 // global MAE, 0..255 per channel
		maxTileErr   float64 // per-tile MAE limit
	}{
		// MAE caps were loose enough that visibly-broken sampled
		// outputs (texture blurred into mush, wrong fine detail)
		// still passed. Tightened so a regression in section-UV
		// sampling fails the test. The per-tile structural check
		// runs on an 8×8 grid of tile mean colors so a feature
		// landing in the wrong place fails even if the global
		// average diff stays low.
		{"earth", filepath.Join("objects", "earth.glb"), 20, 25},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			size := float32(50)
			opts := pipeline.Options{
				Input:             tc.path,
				NumColors:         6,
				InventoryColors:   inventoryRGB(),
				InventoryLabels:   inventoryLabels(),
				NozzleDiameter:    0.4,
				LayerHeight:       0.2,
				Dither:            "riemersma",
				ColorSnap:         5,
				Size:              &size,
				Force:             true,
				ShowSampledColors: true,
				Scale:             1,
			}
			cache := pipeline.NewStageCache()
			pr, err := pipeline.RunCached(context.Background(), cache, opts, nil)
			if err != nil {
				t.Fatalf("RunCached: %v", err)
			}
			if pr.OutputMesh == nil {
				t.Fatalf("OutputMesh is nil")
			}

			inputMesh, err := debugrender.LoadInputMesh(tc.path, &size)
			if err != nil {
				t.Fatalf("LoadInputMesh: %v", err)
			}

			// Log input/sampled mesh bboxes so an orientation
			// mismatch shows up in the test log. The two meshes
			// should sit in the same world frame; if they don't,
			// the rendered comparison is meaningless.
			t.Logf("input bbox: %s", meshBBox(inputMesh.Vertices))
			t.Logf("sampled bbox: %s", meshDataBBox(pr.OutputMesh))

			// Sample dominant face color at +X and -X bbox extremes
			// for both meshes. If one mesh has Africa at +X and the
			// other at -X, the two extreme-side colors swap.
			ixp, ixn := inputExtremeColors(inputMesh)
			sxp, sxn := sampledExtremeColors(pr.OutputMesh)
			t.Logf("input  +X side mean RGB: (%3d,%3d,%3d)  -X side mean RGB: (%3d,%3d,%3d)", ixp[0], ixp[1], ixp[2], ixn[0], ixn[1], ixn[2])
			t.Logf("sampled +X side mean RGB: (%3d,%3d,%3d)  -X side mean RGB: (%3d,%3d,%3d)", sxp[0], sxp[1], sxp[2], sxn[0], sxn[1], sxn[2])
			iyp, iyn := inputExtremeYColors(inputMesh)
			syp, syn := sampledExtremeYColors(pr.OutputMesh)
			t.Logf("input  +Y side mean RGB: (%3d,%3d,%3d)  -Y side mean RGB: (%3d,%3d,%3d)", iyp[0], iyp[1], iyp[2], iyn[0], iyn[1], iyn[2])
			t.Logf("sampled +Y side mean RGB: (%3d,%3d,%3d)  -Y side mean RGB: (%3d,%3d,%3d)", syp[0], syp[1], syp[2], syn[0], syn[1], syn[2])

			const res = 256
			// When DF_TEST_DUMP_DIR is set, also dump PNGs there
			// for visual inspection (t.TempDir() is cleaned up
			// even on failure). The CI / normal `go test` run
			// produces only the in-test failure messages; a
			// developer hunting a regression runs:
			//   DF_TEST_DUMP_DIR=/tmp/foo go test -run TestSampled...
			dumpDir := t.TempDir()
			if extra := os.Getenv("DF_TEST_DUMP_DIR"); extra != "" {
				_ = os.MkdirAll(extra, 0o755)
				dumpDir = extra
			}
			for _, v := range debugrender.DefaultViews {
				inputImg := debugrender.RenderInput(inputMesh, v, res)
				sampledImg := debugrender.RenderPipelineMesh(pr.OutputMesh, v, res)
				_ = debugrender.WritePNG(filepath.Join(dumpDir, fmt.Sprintf("input_%s.png", v.Name)), inputImg)
				_ = debugrender.WritePNG(filepath.Join(dumpDir, fmt.Sprintf("sampled_%s.png", v.Name)), sampledImg)
				mae, overlap := meanAbsoluteRGBError(inputImg, sampledImg)
				maxTileMAE, tileGrid := tileMeanMAE(inputImg, sampledImg, 8)
				t.Logf("%s/%s: overlap=%d px, mae=%.2f (limit %.1f), worst-tile mae=%.2f (limit %.1f, %dx%d grid)",
					tc.name, v.Name, overlap, mae, tc.maxAvgErr, maxTileMAE, tc.maxTileErr, tileGrid, tileGrid)
				if overlap < 100 {
					t.Errorf("%s/%s: too few overlapping pixels (%d) for a meaningful comparison",
						tc.name, v.Name, overlap)
					continue
				}
				if mae > tc.maxAvgErr {
					t.Errorf("%s/%s: sampled output diverges from input (mae=%.2f > %.1f); PNGs in %s",
						tc.name, v.Name, mae, tc.maxAvgErr, dumpDir)
				}
				if maxTileMAE > tc.maxTileErr {
					t.Errorf("%s/%s: worst-tile mean color diverges (tile mae=%.2f > %.1f); features in wrong screen positions; PNGs in %s",
						tc.name, v.Name, maxTileMAE, tc.maxTileErr, dumpDir)
				}
			}
		})
	}
}

// inputExtremeYColors / sampledExtremeYColors: same as the X
// versions but split by Y instead.
func inputExtremeYColors(m *debugrender.InputMesh) (yp, yn [3]int) {
	return inputExtremeAxis(m, 1)
}
func sampledExtremeYColors(md *pipeline.MeshData) (yp, yn [3]int) {
	return sampledExtremeAxis(md, 1)
}

// inputExtremeColors returns the mean face color of faces whose
// centroid sits in the +X 20% slab vs the -X 20% slab of the mesh.
func inputExtremeColors(m *debugrender.InputMesh) (xp, xn [3]int) {
	return inputExtremeAxis(m, 0)
}

func sampledExtremeColors(md *pipeline.MeshData) (xp, xn [3]int) {
	return sampledExtremeAxis(md, 0)
}

func inputExtremeAxis(m *debugrender.InputMesh, axis int) (vp, vn [3]int) {
	var mn, mx float32 = m.Vertices[0][axis], m.Vertices[0][axis]
	for _, v := range m.Vertices {
		if v[axis] < mn {
			mn = v[axis]
		}
		if v[axis] > mx {
			mx = v[axis]
		}
	}
	w := mx - mn
	thrPos := mx - w*0.2
	thrNeg := mn + w*0.2
	var sumPos, sumNeg [3]int
	var nPos, nNeg int
	for i, f := range m.Faces {
		c := (m.Vertices[f[0]][axis] + m.Vertices[f[1]][axis] + m.Vertices[f[2]][axis]) / 3
		col := m.Colors[i]
		if c >= thrPos {
			sumPos[0] += int(col[0])
			sumPos[1] += int(col[1])
			sumPos[2] += int(col[2])
			nPos++
		} else if c <= thrNeg {
			sumNeg[0] += int(col[0])
			sumNeg[1] += int(col[1])
			sumNeg[2] += int(col[2])
			nNeg++
		}
	}
	if nPos > 0 {
		for k := 0; k < 3; k++ {
			vp[k] = sumPos[k] / nPos
		}
	}
	if nNeg > 0 {
		for k := 0; k < 3; k++ {
			vn[k] = sumNeg[k] / nNeg
		}
	}
	return
}

func sampledExtremeAxis(md *pipeline.MeshData, axis int) (vp, vn [3]int) {
	if md == nil || len(md.Vertices) < 3 {
		return
	}
	nV := len(md.Vertices) / 3
	verts := make([][3]float32, nV)
	for i := 0; i < nV; i++ {
		verts[i] = [3]float32{md.Vertices[3*i], md.Vertices[3*i+1], md.Vertices[3*i+2]}
	}
	var mn, mx float32 = verts[0][axis], verts[0][axis]
	for _, v := range verts {
		if v[axis] < mn {
			mn = v[axis]
		}
		if v[axis] > mx {
			mx = v[axis]
		}
	}
	w := mx - mn
	thrPos := mx - w*0.2
	thrNeg := mn + w*0.2
	var sumPos, sumNeg [3]int
	var nPos, nNeg int
	nF := len(md.Faces) / 3
	for fi := 0; fi < nF; fi++ {
		a := md.Faces[3*fi]
		b := md.Faces[3*fi+1]
		c := md.Faces[3*fi+2]
		cax := (verts[a][axis] + verts[b][axis] + verts[c][axis]) / 3
		if 3*fi+2 >= len(md.FaceColors) {
			continue
		}
		r := int(md.FaceColors[3*fi])
		g := int(md.FaceColors[3*fi+1])
		bl := int(md.FaceColors[3*fi+2])
		if cax >= thrPos {
			sumPos[0] += r
			sumPos[1] += g
			sumPos[2] += bl
			nPos++
		} else if cax <= thrNeg {
			sumNeg[0] += r
			sumNeg[1] += g
			sumNeg[2] += bl
			nNeg++
		}
	}
	if nPos > 0 {
		for k := 0; k < 3; k++ {
			vp[k] = sumPos[k] / nPos
		}
	}
	if nNeg > 0 {
		for k := 0; k < 3; k++ {
			vn[k] = sumNeg[k] / nNeg
		}
	}
	return
}

// Old sampledExtremeColors block — leftover duplication removed.

// meshBBox formats the XYZ bounding box of a vertex slice.
func meshBBox(verts [][3]float32) string {
	if len(verts) == 0 {
		return "(empty)"
	}
	mn := verts[0]
	mx := verts[0]
	for _, v := range verts {
		for k := 0; k < 3; k++ {
			if v[k] < mn[k] {
				mn[k] = v[k]
			}
			if v[k] > mx[k] {
				mx[k] = v[k]
			}
		}
	}
	return fmt.Sprintf("X[%.2f..%.2f] Y[%.2f..%.2f] Z[%.2f..%.2f]",
		mn[0], mx[0], mn[1], mx[1], mn[2], mx[2])
}

// meshDataBBox formats the XYZ bounding box of a flat vertex array.
func meshDataBBox(m *pipeline.MeshData) string {
	if m == nil || len(m.Vertices) < 3 {
		return "(empty)"
	}
	n := len(m.Vertices) / 3
	verts := make([][3]float32, n)
	for i := 0; i < n; i++ {
		verts[i] = [3]float32{m.Vertices[3*i], m.Vertices[3*i+1], m.Vertices[3*i+2]}
	}
	return meshBBox(verts)
}

// tileMeanMAE splits both images into an N×N grid of tiles,
// computes each tile's mean RGB over its non-transparent pixels,
// and returns the WORST per-tile MAE between matching tiles
// (input vs sampled). Catches cases where global MAE is low but
// features have shifted: e.g. Africa rendered where the Indian
// Ocean should be produces a low global mean diff but a huge
// per-tile mean diff on those tiles.
//
// Returns (0, n) if either image is empty.
func tileMeanMAE(a, b *image.RGBA, n int) (float64, int) {
	if a.Bounds() != b.Bounds() || n < 1 {
		return 0, n
	}
	bounds := a.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	var worst float64
	for ty := 0; ty < n; ty++ {
		for tx := 0; tx < n; tx++ {
			x0 := tx * w / n
			x1 := (tx + 1) * w / n
			y0 := ty * h / n
			y1 := (ty + 1) * h / n
			var aR, aG, aB, bR, bG, bB int
			var nA, nB int
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					ai := (y-bounds.Min.Y)*a.Stride + (x-bounds.Min.X)*4
					bi := (y-bounds.Min.Y)*b.Stride + (x-bounds.Min.X)*4
					if a.Pix[ai+3] != 0 {
						aR += int(a.Pix[ai])
						aG += int(a.Pix[ai+1])
						aB += int(a.Pix[ai+2])
						nA++
					}
					if b.Pix[bi+3] != 0 {
						bR += int(b.Pix[bi])
						bG += int(b.Pix[bi+1])
						bB += int(b.Pix[bi+2])
						nB++
					}
				}
			}
			// Skip tiles that are mostly background in either
			// image — they don't usefully constrain alignment.
			minPx := (x1 - x0) * (y1 - y0) / 4
			if nA < minPx || nB < minPx {
				continue
			}
			diff := absDiff(aR/nA, bR/nB) + absDiff(aG/nA, bG/nB) + absDiff(aB/nA, bB/nB)
			tileMAE := float64(diff) / 3
			if tileMAE > worst {
				worst = tileMAE
			}
		}
	}
	return worst, n
}

func absDiff(a, b int) int {
	if a < b {
		return b - a
	}
	return a - b
}

// meanAbsoluteRGBError walks two same-sized RGBA images and
// averages |Δr|+|Δg|+|Δb|, divided by 3, over the set of pixels
// that are non-transparent in BOTH images. Returns the MAE and
// the overlap pixel count.
func meanAbsoluteRGBError(a, b *image.RGBA) (mae float64, overlap int) {
	if a.Bounds() != b.Bounds() {
		return 0, 0
	}
	bounds := a.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	var total int
	var sum int
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			ai := (y-bounds.Min.Y)*a.Stride + (x-bounds.Min.X)*4
			bi := (y-bounds.Min.Y)*b.Stride + (x-bounds.Min.X)*4
			if a.Pix[ai+3] == 0 || b.Pix[bi+3] == 0 {
				continue
			}
			total++
			for k := 0; k < 3; k++ {
				d := int(a.Pix[ai+k]) - int(b.Pix[bi+k])
				if d < 0 {
					d = -d
				}
				sum += d
			}
		}
	}
	if total == 0 {
		return 0, 0
	}
	return float64(sum) / (3 * float64(total)), total
}

func inventoryRGB() [][3]uint8 {
	out := make([][3]uint8, len(testInventory))
	for i, e := range testInventory {
		out[i] = e.Color
	}
	return out
}

func inventoryLabels() []string {
	out := make([]string, len(testInventory))
	for i, e := range testInventory {
		out[i] = e.Label
	}
	return out
}

