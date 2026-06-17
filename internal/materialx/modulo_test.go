package materialx

import (
	"math"
	"strings"
	"testing"
)

func sampleColorGraph(t *testing.T, doc string) Sampler {
	t.Helper()
	d, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s, err := d.DefaultBaseColorSampler()
	if err != nil {
		t.Fatalf("sampler: %v", err)
	}
	return s
}

func nearVec3(a, b [3]float64) bool {
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-9 {
			return false
		}
	}
	return true
}

// modulo wraps height into a repeating stripe phase. Floor-based so a
// negative input (below the origin) still wraps into [0, in2). The
// modulo node here outputs color3, broadcasting the scalar z across all
// channels.
func TestModuloTilesAndWrapsNegative(t *testing.T) {
	const doc = `<?xml version="1.0"?>
<materialx version="1.38">
  <nodegraph name="ng">
    <position name="p" type="vector3"/>
    <separate3 name="xyz" type="multioutput">
      <input name="in" type="vector3" nodename="p"/>
    </separate3>
    <modulo name="m" type="color3">
      <input name="in1" type="float" nodename="xyz" output="outz"/>
      <input name="in2" type="float" value="1.0"/>
    </modulo>
    <output name="out" type="color3" nodename="m"/>
  </nodegraph>
  <standard_surface name="s" type="surfaceshader">
    <input name="base_color" type="color3" nodegraph="ng" output="out"/>
  </standard_surface>
  <surfacematerial name="M" type="material">
    <input name="surfaceshader" type="surfaceshader" nodename="s"/>
  </surfacematerial>
</materialx>`
	s := sampleColorGraph(t, doc)
	cases := []struct {
		z, want float64
	}{
		{0.25, 0.25},
		{1.25, 0.25}, // tiles
		{2.75, 0.75},
		{-0.25, 0.75}, // floor-based wrap of a negative input
		{-1.25, 0.75},
	}
	for _, c := range cases {
		got := s.Sample([3]float64{0, 0, c.z})
		if !nearVec3(got, [3]float64{c.want, c.want, c.want}) {
			t.Errorf("mod(%v,1): got %v want %v", c.z, got[0], c.want)
		}
	}
}

// divide implements the standard arithmetic node and guards a zero
// divisor instead of emitting Inf/NaN.
func TestDivideAndSafeZero(t *testing.T) {
	const doc = `<?xml version="1.0"?>
<materialx version="1.38">
  <nodegraph name="ng">
    <divide name="d" type="color3">
      <input name="in1" type="color3" value="0.6, 0.9, 0.3"/>
      <input name="in2" type="float" value="3.0"/>
    </divide>
    <output name="out" type="color3" nodename="d"/>
  </nodegraph>
  <standard_surface name="s" type="surfaceshader">
    <input name="base_color" type="color3" nodegraph="ng" output="out"/>
  </standard_surface>
  <surfacematerial name="M" type="material">
    <input name="surfaceshader" type="surfaceshader" nodename="s"/>
  </surfacematerial>
</materialx>`
	s := sampleColorGraph(t, doc)
	got := s.Sample([3]float64{0, 0, 0})
	if !nearVec3(got, [3]float64{0.2, 0.3, 0.1}) {
		t.Errorf("divide: got %v want {0.2 0.3 0.1}", got)
	}

	if v := safeDiv(1, 0); v != 0 {
		t.Errorf("safeDiv by zero: got %v want 0", v)
	}
	if v := floorMod(1, 0); v != 0 {
		t.Errorf("floorMod by zero: got %v want 0", v)
	}
}
