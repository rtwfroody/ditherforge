package voxel

import (
	"context"
	"math"
	"testing"
)

func TestRgbLabRoundTrip(t *testing.T) {
	colors := [][3]uint8{
		{0, 0, 0},
		{255, 255, 255},
		{255, 0, 0},
		{0, 255, 0},
		{0, 0, 255},
		{128, 64, 200},
	}
	for _, c := range colors {
		L, a, b := rgbToLab(c)
		got := labToRGB(L, a, b)
		for ch := range 3 {
			diff := int(got[ch]) - int(c[ch])
			if diff < -1 || diff > 1 {
				t.Errorf("round trip %v: got %v (diff %d in channel %d)", c, got, diff, ch)
			}
		}
	}
}

func TestWarpNoPins(t *testing.T) {
	cells := []ActiveCell{
		{Color: [3]uint8{100, 150, 200}},
		{Color: [3]uint8{50, 50, 50}},
	}
	out, err := WarpCellColors(context.Background(), cells, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(cells) {
		t.Fatalf("expected %d cells, got %d", len(cells), len(out))
	}
	for i := range cells {
		if out[i].Color != cells[i].Color {
			t.Errorf("cell %d: expected %v, got %v", i, cells[i].Color, out[i].Color)
		}
	}
	// Verify it's a copy, not aliased.
	out[0].Color = [3]uint8{0, 0, 0}
	if cells[0].Color == out[0].Color {
		t.Error("output aliases input")
	}
}

func TestWarpSinglePin(t *testing.T) {
	// A single pin: red → blue. A red cell should become blue.
	red := [3]uint8{255, 0, 0}
	blue := [3]uint8{0, 0, 255}
	pins := []ColorWarpPin{{Source: red, Target: blue}}

	cells := []ActiveCell{
		{Color: red},
	}
	out, err := WarpCellColors(context.Background(), cells, pins)
	if err != nil {
		t.Fatal(err)
	}

	// The source color should map very close to the target.
	got := out[0].Color
	dist := colorDist(got, blue)
	if dist > 5 {
		t.Errorf("red→blue warp: expected close to %v, got %v (dist %.1f)", blue, got, dist)
	}
}

func TestWarpDistantColorUnchanged(t *testing.T) {
	// Pin red → blue. A distant color (green) should be mostly unchanged.
	red := [3]uint8{255, 0, 0}
	blue := [3]uint8{0, 0, 255}
	green := [3]uint8{0, 255, 0}
	// Use a tight sigma so the effect is very local.
	pins := []ColorWarpPin{{Source: red, Target: blue, Sigma: 10}}

	cells := []ActiveCell{
		{Color: green},
	}
	out, err := WarpCellColors(context.Background(), cells, pins)
	if err != nil {
		t.Fatal(err)
	}

	got := out[0].Color
	dist := colorDist(got, green)
	if dist > 10 {
		t.Errorf("distant color should be mostly unchanged: expected ~%v, got %v (dist %.1f)", green, got, dist)
	}
}

func TestWarpTwoPins(t *testing.T) {
	// Two pins: red → blue and green → yellow. Both should land on their targets.
	red := [3]uint8{255, 0, 0}
	blue := [3]uint8{0, 0, 255}
	green := [3]uint8{0, 255, 0}
	yellow := [3]uint8{255, 255, 0}

	pins := []ColorWarpPin{
		{Source: red, Target: blue},
		{Source: green, Target: yellow},
	}

	cells := []ActiveCell{
		{Color: red},
		{Color: green},
	}
	out, err := WarpCellColors(context.Background(), cells, pins)
	if err != nil {
		t.Fatal(err)
	}

	distRedBlue := colorDist(out[0].Color, blue)
	if distRedBlue > 5 {
		t.Errorf("red→blue: got %v, dist %.1f from target", out[0].Color, distRedBlue)
	}
	distGreenYellow := colorDist(out[1].Color, yellow)
	if distGreenYellow > 5 {
		t.Errorf("green→yellow: got %v, dist %.1f from target", out[1].Color, distGreenYellow)
	}
}

func TestWarpHeterogeneousSigmas(t *testing.T) {
	// Two pins with different sigmas. Pin 0 (tight) should have local effect;
	// pin 1 (wide) should affect a broader range.
	red := [3]uint8{255, 0, 0}
	blue := [3]uint8{0, 0, 255}
	green := [3]uint8{0, 200, 0}
	yellow := [3]uint8{255, 255, 0}
	// A color between red and green.
	olive := [3]uint8{128, 128, 0}

	pins := []ColorWarpPin{
		{Source: red, Target: blue, Sigma: 5},    // very tight
		{Source: green, Target: yellow, Sigma: 80}, // very wide
	}

	cells := []ActiveCell{
		{Color: red},
		{Color: green},
		{Color: olive},
	}
	out, err := WarpCellColors(context.Background(), cells, pins)
	if err != nil {
		t.Fatal(err)
	}

	// Both pin sources should still land on their targets.
	distRedBlue := colorDist(out[0].Color, blue)
	if distRedBlue > 5 {
		t.Errorf("red→blue: got %v, dist %.1f from target", out[0].Color, distRedBlue)
	}
	distGreenYellow := colorDist(out[1].Color, yellow)
	if distGreenYellow > 5 {
		t.Errorf("green→yellow: got %v, dist %.1f from target", out[1].Color, distGreenYellow)
	}

	// Olive is between red and green. The wide green pin (sigma=80) should
	// affect it more than the tight red pin (sigma=5). So olive should shift
	// noticeably toward yellow.
	oliveDistToOriginal := colorDist(out[2].Color, olive)
	if oliveDistToOriginal < 5 {
		t.Errorf("olive should be shifted by wide green pin, but dist from original is only %.1f", oliveDistToOriginal)
	}
}

func TestWarpContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	cells := make([]ActiveCell, 100000)
	for i := range cells {
		cells[i].Color = [3]uint8{128, 128, 128}
	}
	pins := []ColorWarpPin{{Source: [3]uint8{128, 128, 128}, Target: [3]uint8{0, 0, 0}}}

	_, err := WarpCellColors(ctx, cells, pins)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

func TestGaussElimIdentity(t *testing.T) {
	// 3x3 identity matrix.
	A := [][]float64{
		{1, 0, 0},
		{0, 1, 0},
		{0, 0, 1},
	}
	b := []float64{3, 5, 7}
	x, err := gaussElim(A, b)
	if err != nil {
		t.Fatal(err)
	}
	for i := range b {
		if math.Abs(x[i]-b[i]) > 1e-10 {
			t.Errorf("x[%d] = %f, want %f", i, x[i], b[i])
		}
	}
}

func TestGaussElimSingular(t *testing.T) {
	A := [][]float64{
		{1, 2},
		{2, 4}, // linearly dependent
	}
	b := []float64{1, 2}
	_, err := gaussElim(A, b)
	if err == nil {
		t.Error("expected singular matrix error")
	}
}

// colorDist returns Euclidean distance in RGB space.
func colorDist(a, b [3]uint8) float64 {
	dr := float64(a[0]) - float64(b[0])
	dg := float64(a[1]) - float64(b[1])
	db := float64(a[2]) - float64(b[2])
	return math.Sqrt(dr*dr + dg*dg + db*db)
}
