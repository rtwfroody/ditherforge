package split

import (
	"math"
	"testing"
)

// TestPlacePegs_UnitSquareSpread verifies that 4 pegs in a unit
// square aren't clustered — pairwise distances should be reasonably
// spread.
func TestPlacePegs_UnitSquareSpread(t *testing.T) {
	square := capPolygon{
		outer: [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}},
	}
	pegs, err := placePegs(square, 4, 0, 0)
	if err != nil {
		t.Fatalf("placePegs: %v", err)
	}
	if len(pegs) != 4 {
		t.Fatalf("got %d pegs, want 4", len(pegs))
	}
	// All inside the square.
	for i, p := range pegs {
		if p[0] < 0 || p[0] > 1 || p[1] < 0 || p[1] > 1 {
			t.Errorf("peg %d at %v outside unit square", i, p)
		}
	}
	// Min pairwise distance >= 0.4. A truly clustered placement (all
	// near centroid) would yield distances ~0.05; this threshold
	// flags clustering without demanding optimal packing.
	minD := math.Inf(1)
	for i := 0; i < len(pegs); i++ {
		for j := i + 1; j < len(pegs); j++ {
			dx := pegs[i][0] - pegs[j][0]
			dy := pegs[i][1] - pegs[j][1]
			d := math.Sqrt(dx*dx + dy*dy)
			if d < minD {
				minD = d
			}
		}
	}
	if minD < 0.4 {
		t.Errorf("min pairwise distance = %g, want >= 0.4 (pegs are clustered)", minD)
	}
}

// TestPlacePegs_LShapeSpread — for an L-shaped polygon (a non-convex
// shape) and N=2 pegs, verify the pegs are reasonably spread. The
// L-shape has a longest internal distance ≈ 2.83 (corner-to-corner),
// so a sane greedy placement should yield pegs at least 1.0 apart.
func TestPlacePegs_LShapeSpread(t *testing.T) {
	// L-shape (CCW): (0,0) -> (2,0) -> (2,1) -> (1,1) -> (1,2) -> (0,2) -> (0,0)
	lshape := capPolygon{
		outer: [][2]float64{
			{0, 0}, {2, 0}, {2, 1}, {1, 1}, {1, 2}, {0, 2},
		},
	}
	pegs, err := placePegs(lshape, 2, 0, 0)
	if err != nil {
		t.Fatalf("placePegs: %v", err)
	}
	if len(pegs) != 2 {
		t.Fatalf("got %d pegs, want 2", len(pegs))
	}
	dx := pegs[0][0] - pegs[1][0]
	dy := pegs[0][1] - pegs[1][1]
	d := math.Sqrt(dx*dx + dy*dy)
	if d < 1.0 {
		t.Errorf("L-shape pegs at %v %v, distance %g; want >= 1.0", pegs[0], pegs[1], d)
	}
}

// TestPlacePegs_HoleAvoided checks that a peg isn't placed inside a
// polygon hole.
func TestPlacePegs_HoleAvoided(t *testing.T) {
	// Square outer 4×4, hole 1.5×1.5 in the center.
	poly := capPolygon{
		outer: [][2]float64{{0, 0}, {4, 0}, {4, 4}, {0, 4}},
		holes: [][][2]float64{
			{{1.25, 2.75}, {2.75, 2.75}, {2.75, 1.25}, {1.25, 1.25}}, // CW
		},
	}
	pegs, err := placePegs(poly, 1, 0, 0)
	if err != nil {
		t.Fatalf("placePegs: %v", err)
	}
	if len(pegs) != 1 {
		t.Fatalf("got %d pegs, want 1", len(pegs))
	}
	p := pegs[0]
	// Peg shouldn't be in the hole.
	if p[0] >= 1.25 && p[0] <= 2.75 && p[1] >= 1.25 && p[1] <= 2.75 {
		t.Errorf("peg at %v lies inside the hole", p)
	}
	// Peg should be inside the outer square.
	if p[0] < 0 || p[0] > 4 || p[1] < 0 || p[1] > 4 {
		t.Errorf("peg at %v outside outer square", p)
	}
}

// TestPlacePegs_BoundaryClearance verifies pegs sit at least
// boundaryClearance from every edge of the polygon. With a 10×10
// square and clearance 2, every peg must lie within [2, 8] × [2, 8].
func TestPlacePegs_BoundaryClearance(t *testing.T) {
	square := capPolygon{
		outer: [][2]float64{{0, 0}, {10, 0}, {10, 10}, {0, 10}},
	}
	pegs, err := placePegs(square, 4, 0, 2.0)
	if err != nil {
		t.Fatalf("placePegs: %v", err)
	}
	if len(pegs) != 4 {
		t.Fatalf("got %d pegs, want 4", len(pegs))
	}
	// Allow one-pixel slack for rasterization (10mm / 200px = 0.05mm).
	const slack = 0.1
	for i, p := range pegs {
		if p[0] < 2.0-slack || p[0] > 8.0+slack || p[1] < 2.0-slack || p[1] > 8.0+slack {
			t.Errorf("peg %d at %v violates boundary clearance 2.0 (must be in [2,8]×[2,8])", i, p)
		}
	}
}

// TestPlacePegs_SinglePeg with count=1 places near centroid.
func TestPlacePegs_SinglePeg(t *testing.T) {
	square := capPolygon{
		outer: [][2]float64{{0, 0}, {2, 0}, {2, 2}, {0, 2}},
	}
	pegs, err := placePegs(square, 1, 0, 0)
	if err != nil {
		t.Fatalf("placePegs: %v", err)
	}
	if len(pegs) != 1 {
		t.Fatalf("got %d pegs, want 1", len(pegs))
	}
	p := pegs[0]
	// Centroid is (1, 1). Single-peg placement should be near it.
	if math.Abs(p[0]-1) > 0.2 || math.Abs(p[1]-1) > 0.2 {
		t.Errorf("single peg at %v, want near (1, 1)", p)
	}
}
