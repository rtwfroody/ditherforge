package manifoldbool

import (
	"math"
	"testing"
)

// boxMesh returns a closed [0,w]×[0,w]×[0,h] box as XYZ triangle soup
// with CCW outward winding (same index/winding layout as cubeMesh, just
// not centred). Top face (z=h) is triangles {4,5,6},{4,6,7}.
func boxMesh(w, h float32) ([][3]float32, [][3]uint32) {
	v := [][3]float32{
		{0, 0, 0}, {w, 0, 0}, {w, w, 0}, {0, w, 0},
		{0, 0, h}, {w, 0, h}, {w, w, h}, {0, w, h},
	}
	f := [][3]uint32{
		{0, 2, 1}, {0, 3, 2},
		{4, 5, 6}, {4, 6, 7},
		{0, 1, 5}, {0, 5, 4},
		{2, 3, 7}, {2, 7, 6},
		{0, 4, 7}, {0, 7, 3},
		{1, 2, 6}, {1, 6, 5},
	}
	return v, f
}

// countTopFaces returns how many triangles in (v,f) have all three
// vertices within tol(mm) of z=h — i.e. lie on the model's flat top —
// and the maximum z seen across all returned vertices.
func countTopFaces(v [][3]float32, f [][3]uint32, h, tol float32) (nTop int, maxZ float32) {
	maxZ = float32(math.Inf(-1))
	for _, p := range v {
		if p[2] > maxZ {
			maxZ = p[2]
		}
	}
	for _, tri := range f {
		onTop := true
		for _, idx := range tri {
			if absf(v[idx][2]-h) > tol {
				onTop = false
				break
			}
		}
		if onTop {
			nTop++
		}
	}
	return nTop, maxZ
}

func absf(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

// TestOnPlaneFaceSurvival measures, as a function of the gap between a
// flat top face (at an integer-µm Z, simulating post-DedupVertsByPosition
// quantization) and the SplitByPlane cut plane, whether the top face
// survives ToMeshFiltered(srcID) in the "above" piece. This pins down the
// effective on-plane distance that turns a source surface into a discarded
// cut cap — the root-cause mechanism for the flat-top white holes.
func TestOnPlaneFaceSurvival(t *testing.T) {
	const (
		w      = 10.0   // mm footprint
		topUM  = 4043.0 // mm×1000 — flat top at an integer-µm grid line
		tolMM  = 1e-4   // 0.1µm: "is this vertex on the top plane?"
		nextUM = 4123.0 // next slab boundary above (one 80µm layer up)
	)
	h := float32(topUM / 1000.0)

	v, f := boxMesh(w, h)
	m, err := FromMesh(v, f)
	if err != nil {
		t.Fatalf("FromMesh: %v", err)
	}
	defer m.Close()
	srcID := m.OriginalID()
	t.Logf("box top z=%.6f mm (%.0f µm), srcID=%d, tris=%d", h, topUM, srcID, m.NumTri())

	// gaps in µm between the top face and the cut plane below it.
	gapsUM := []float64{0.05, 0.1, 0.2, 0.35, 0.5, 0.65, 0.85, 1.0, 1.5, 2.0, 5.0}

	t.Logf("%-8s %-10s | split-above ToMeshFiltered(srcID) | split-above raw  | end-to-end (∩ prism)",
		"gap_µm", "plane_µm")
	t.Logf("%-8s %-10s | %-7s %-7s %-7s | %-7s %-7s | %-7s %-7s",
		"", "", "topF", "allF", "maxZ", "topF", "allF", "topF", "allF")

	for _, gUM := range gapsUM {
		planeUM := topUM - gUM
		zPlane := planeUM / 1000.0

		above, below, err := SplitByPlane(m, 0, 0, 1, zPlane)
		if err != nil {
			t.Errorf("gap=%.2fµm SplitByPlane: %v", gUM, err)
			continue
		}

		// (1) above piece, srcID-filtered (what the pipeline keeps).
		fv, ff := above.ToMeshFiltered(srcID)
		fTop, fMaxZ := countTopFaces(fv, ff, h, tolMM)

		// (2) above piece, raw (is the geometry there at all, regardless
		//     of srcID label?).
		rv, rf := above.ToMesh()
		rTop, _ := countTopFaces(rv, rf, h, tolMM)

		// (3) end-to-end: clip the above piece by a full-footprint prism
		//     spanning [zPlane, nextUM], mirroring runClip, then filter.
		var eTop, eAllF int
		prism, perr := ExtrudePolygons([][][2]float32{{
			{0, 0}, {w, 0}, {w, w}, {0, w},
		}}, float32(zPlane), float32(nextUM/1000.0))
		if perr != nil {
			t.Errorf("gap=%.2fµm ExtrudePolygons: %v", gUM, perr)
		} else {
			out, ierr := Intersection(above, prism)
			if ierr != nil {
				t.Errorf("gap=%.2fµm Intersection: %v", gUM, ierr)
			} else {
				ev, ef := out.ToMeshFiltered(srcID)
				eTop, _ = countTopFaces(ev, ef, h, tolMM)
				eAllF = len(ef)
				out.Close()
			}
			prism.Close()
		}

		t.Logf("%-8.2f %-10.2f | %-7d %-7d %-7.4f | %-7d %-7d | %-7d %-7d",
			gUM, planeUM, fTop, len(ff), fMaxZ, rTop, len(rf), eTop, eAllF)

		// Regression guard: the whole point of this repro is that a flat top
		// above an off-grid cut plane is NOT discarded. The two top triangles
		// must survive both the split (srcID-filtered above piece) and the
		// end-to-end prism intersection, at every gap. If a future change made
		// SplitByPlane drop a near-coincident face as a cut cap, fTop/eTop go
		// to 0 here and this fails.
		if fTop < 2 {
			t.Errorf("gap=%.2fµm: top face dropped from split-above srcID mesh (fTop=%d, want >=2)", gUM, fTop)
		}
		if eTop < 2 {
			t.Errorf("gap=%.2fµm: top face dropped end-to-end through prism (eTop=%d, want >=2)", gUM, eTop)
		}

		above.Close()
		below.Close()
	}
}

// tentBox returns a closed box whose top is a shallow "tent": four corner
// vertices at z=hi and a centre vertex at z=lo, so the top straddles any
// cut plane between lo and hi. This mirrors a nominally-flat but
// quantization-noisy real surface whose vertices land on both sides of an
// off-grid slab boundary.
func tentBox(w, lo, hi float32) ([][3]float32, [][3]uint32) {
	v := [][3]float32{
		{0, 0, 0}, {w, 0, 0}, {w, w, 0}, {0, w, 0}, // 0-3 bottom
		{0, 0, hi}, {w, 0, hi}, {w, w, hi}, {0, w, hi}, // 4-7 top corners
		{w / 2, w / 2, lo}, // 8 top centre (dipped)
	}
	f := [][3]uint32{
		{0, 2, 1}, {0, 3, 2}, // bottom
		{4, 5, 8}, {5, 6, 8}, {6, 7, 8}, {7, 4, 8}, // tent top (CCW from +Z)
		{0, 1, 5}, {0, 5, 4}, // -Y wall
		{2, 3, 7}, {2, 7, 6}, // +Y wall
		{0, 4, 7}, {0, 7, 3}, // -X wall
		{1, 2, 6}, {1, 6, 5}, // +X wall
	}
	return v, f
}

func triArea(a, b, c [3]float32) (area, nz float32) {
	ux, uy, uz := b[0]-a[0], b[1]-a[1], b[2]-a[2]
	vx, vy, vz := c[0]-a[0], c[1]-a[1], c[2]-a[2]
	cx := uy*vz - uz*vy
	cy := uz*vx - ux*vz
	cz := ux*vy - uy*vx
	mag := float32(math.Sqrt(float64(cx*cx + cy*cy + cz*cz)))
	area = mag / 2
	if mag > 0 {
		nz = cz / mag
	}
	return area, nz
}

// upwardArea sums the area of faces whose unit normal points up (nz>0.5):
// the top surface, isolating it from vertical walls and the bottom.
func upwardArea(v [][3]float32, f [][3]uint32) float32 {
	var sum float32
	for _, t := range f {
		a, nz := triArea(v[t[0]], v[t[1]], v[t[2]])
		if nz > 0.5 {
			sum += a
		}
	}
	return sum
}

// TestStraddlingTopAreaConservation cuts a tent-topped box (top straddling
// the plane) and checks whether the up-facing srcID surface area is
// conserved across the above+below pieces. A shortfall = surface lost into
// the cut = a hole.
func TestStraddlingTopAreaConservation(t *testing.T) {
	const w = 10.0
	lo := float32(4042.0 / 1000.0) // centre dips to 4042µm
	hi := float32(4043.0 / 1000.0) // corners at 4043µm

	v, f := tentBox(w, lo, hi)
	m, err := FromMesh(v, f)
	if err != nil {
		t.Fatalf("FromMesh: %v", err)
	}
	defer m.Close()
	srcID := m.OriginalID()
	origTop := upwardArea(v, f)
	t.Logf("tent top: lo=%.0fµm hi=%.0fµm, original up-area=%.5f mm² (≈%.1f expected), srcID=%d",
		lo*1000, hi*1000, origTop, float32(w*w), srcID)

	planesUM := []float64{4041.5, 4042.0, 4042.35, 4042.5, 4042.65, 4043.0, 4043.5}
	t.Logf("%-10s | %-9s %-9s %-9s | %s", "plane_µm", "above_up", "below_up", "sum_up", "loss_µm²")
	for _, pUM := range planesUM {
		zPlane := pUM / 1000.0
		above, below, err := SplitByPlane(m, 0, 0, 1, zPlane)
		if err != nil {
			t.Errorf("plane=%.2f SplitByPlane: %v", pUM, err)
			continue
		}
		av, af := above.ToMeshFiltered(srcID)
		bv, bf := below.ToMeshFiltered(srcID)
		aUp := upwardArea(av, af)
		bUp := upwardArea(bv, bf)
		sum := aUp + bUp
		lossUM2 := (origTop - sum) * 1e6 // mm²→µm²
		t.Logf("%-10.2f | %-9.5f %-9.5f %-9.5f | %.3f", pUM, aUp, bUp, sum, lossUM2)
		// Regression guard: the split must conserve the straddling top surface
		// across both slab pieces. Observed loss is float noise (≤~8 µm² of a
		// 100 mm² top); a real regression that dropped a straddle fragment into
		// the cut would lose thousands of µm². 200 µm² cleanly separates the two.
		if lossUM2 > 200 {
			t.Errorf("plane=%.2fµm: up-facing top area not conserved across split (loss=%.1f µm², want <200) — SplitByPlane dropped straddling surface", pUM, lossUM2)
		}
		above.Close()
		below.Close()
	}
}
