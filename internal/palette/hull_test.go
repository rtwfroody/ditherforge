package palette

import (
	"math"
	"math/rand"
	"testing"
)

// hullDistance is a distance-only twin of closestHullFeature, used by the
// per-sample scorer to avoid a second attribution allocation per sample. If the
// two ever disagree the scorer's bulk-reach term is measuring something other
// than a hull distance, so pin them together over random configurations —
// including the degenerate ones (coincident, collinear, coplanar vertices) the
// scorer sees when a palette contains near-duplicate or neutral filaments.
func TestHullDistanceMatchesFeature(t *testing.T) {
	rng := rand.New(rand.NewSource(20260724))

	randPt := func() [3]float64 {
		return [3]float64{rng.Float64(), rng.Float64()*2 - 1, rng.Float64()*2 - 1}
	}

	for _, n := range []int{1, 2, 3, 4, 5, 6} {
		for trial := 0; trial < 400; trial++ {
			verts := make([][3]float64, n)
			for i := range verts {
				verts[i] = randPt()
			}
			switch trial % 4 {
			case 1: // collinear
				if n >= 2 {
					a, d := verts[0], randPt()
					for i := range verts {
						t := float64(i)
						verts[i] = [3]float64{a[0] + t*d[0], a[1] + t*d[1], a[2] + t*d[2]}
					}
				}
			case 2: // coplanar (flat in L)
				for i := range verts {
					verts[i][0] = 0.5
				}
			case 3: // duplicate vertices
				for i := 1; i < n; i++ {
					verts[i] = verts[0]
				}
			}

			p := randPt()
			want, _, _ := closestHullFeature(p, verts)
			got := hullDistance(p, verts)
			if math.Abs(got-want) > 1e-12 {
				t.Fatalf("n=%d trial=%d: hullDistance=%g closestHullFeature=%g (verts %v, p %v)",
					n, trial, got, want, verts, p)
			}
		}
	}

	if d := hullDistance([3]float64{0, 0, 0}, nil); d != math.MaxFloat64 {
		t.Errorf("empty hull: got %g, want MaxFloat64", d)
	}
}

// An opaque palette has eff == nominal, so the supporting vertices' nominal hull
// IS the closest hull feature and the bulk-reach shortfall must vanish. This is
// what keeps the forceOpaque path bit-identical to the nominal scorer.
func TestBulkReachShortfallZeroWhenOpaque(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 500; trial++ {
		verts := make([][3]float64, 4)
		for i := range verts {
			verts[i] = [3]float64{rng.Float64(), rng.Float64()*2 - 1, rng.Float64()*2 - 1}
		}
		p := [3]float64{rng.Float64(), rng.Float64()*2 - 1, rng.Float64()*2 - 1}

		hullDist, feat, _ := closestHullFeature(p, verts)
		// Opaque: nominal colors are the eff vertices themselves.
		sup := make([][3]float64, len(feat))
		for m, vi := range feat {
			sup[m] = verts[vi]
		}
		if bulk := hullDistance(p, sup); bulk-hullDist > 1e-12 {
			t.Fatalf("trial %d: opaque shortfall %g (bulk %g, hull %g)",
				trial, bulk-hullDist, bulk, hullDist)
		}
	}
}
