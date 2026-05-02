package materialx_test

import (
	"math"
	"os"
	"strings"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/materialx"
)

func TestParseMarbleStructure(t *testing.T) {
	doc := loadMarble(t)
	if got, want := len(doc.NodeGraphs), 1; got != want {
		t.Errorf("nodegraphs: got %d, want %d", got, want)
	}
	if got, want := len(doc.Surfaces), 1; got != want {
		t.Errorf("surfaces: got %d, want %d", got, want)
	}
	if got, want := len(doc.Materials), 1; got != want {
		t.Errorf("materials: got %d, want %d", got, want)
	}
	names := doc.MaterialNames()
	if len(names) != 1 || names[0] != "Marble_3D" {
		t.Errorf("material names: got %v, want [Marble_3D]", names)
	}
}

func TestSampleMarbleDeterministic(t *testing.T) {
	doc := loadMarble(t)
	s, err := doc.DefaultBaseColorSampler()
	if err != nil {
		t.Fatal(err)
	}
	p := [3]float64{0.31, -0.42, 0.7}
	c1 := s.Sample(p)
	c2 := s.Sample(p)
	if c1 != c2 {
		t.Errorf("non-deterministic: %v vs %v", c1, c2)
	}
}

func TestSampleMarbleVariesAndStaysInRange(t *testing.T) {
	doc := loadMarble(t)
	s, err := doc.DefaultBaseColorSampler()
	if err != nil {
		t.Fatal(err)
	}

	// Marble graph mixes between (0.8, 0.8, 0.8) and (0.1, 0.1, 0.3)
	// using a mix factor in [0, 1]. Output channels must lie within
	// the per-channel min/max of those two endpoints.
	c1 := [3]float64{0.8, 0.8, 0.8}
	c2 := [3]float64{0.1, 0.1, 0.3}
	lo := [3]float64{}
	hi := [3]float64{}
	for i := range 3 {
		lo[i] = math.Min(c1[i], c2[i])
		hi[i] = math.Max(c1[i], c2[i])
	}

	var minV, maxV [3]float64
	for i := range minV {
		minV[i] = math.Inf(1)
		maxV[i] = math.Inf(-1)
	}
	const eps = 1e-9
	const samples = 8
	const span = 0.5
	for ix := range samples {
		for iy := range samples {
			for iz := range samples {
				p := [3]float64{
					-span + 2*span*float64(ix)/float64(samples-1),
					-span + 2*span*float64(iy)/float64(samples-1),
					-span + 2*span*float64(iz)/float64(samples-1),
				}
				c := s.Sample(p)
				for i := range 3 {
					if c[i] < lo[i]-eps || c[i] > hi[i]+eps {
						t.Fatalf("color out of range at %v: %v (allowed [%v, %v])", p, c, lo, hi)
					}
					if c[i] < minV[i] {
						minV[i] = c[i]
					}
					if c[i] > maxV[i] {
						maxV[i] = c[i]
					}
				}
			}
		}
	}

	// Variation: at least one channel must span >10% of its allowable
	// range across the sample grid. Without this the sampler could be
	// stuck on a single mix endpoint and the test would still pass.
	varied := false
	for i := range 3 {
		if maxV[i]-minV[i] > 0.1*(hi[i]-lo[i]) {
			varied = true
			break
		}
	}
	if !varied {
		t.Errorf("output insufficiently varied across grid: min=%v max=%v", minV, maxV)
	}
}

func TestParseUnknownNodeFailsConstruction(t *testing.T) {
	// The marble file contains only known nodes; a doc that references
	// an unknown node type should fail at sampler construction (not
	// silently at sample time).
	bad := strings.NewReader(`<?xml version="1.0"?>
<materialx version="1.39">
  <nodegraph name="ng">
    <bogusnode name="b" type="float"/>
    <output name="out" type="color3" nodename="b"/>
  </nodegraph>
  <standard_surface name="ss" type="surfaceshader">
    <input name="base_color" type="color3" nodegraph="ng" output="out"/>
  </standard_surface>
  <surfacematerial name="m" type="material">
    <input name="surfaceshader" type="surfaceshader" nodename="ss"/>
  </surfacematerial>
</materialx>
`)
	doc, err := materialx.Parse(bad)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := doc.DefaultBaseColorSampler(); err == nil {
		t.Fatalf("expected error from unsupported node, got nil")
	}
}

func TestConstantBaseColor(t *testing.T) {
	// Surface shader with a literal base_color should produce a constant
	// sampler that ignores position.
	src := strings.NewReader(`<?xml version="1.0"?>
<materialx version="1.39">
  <standard_surface name="ss" type="surfaceshader">
    <input name="base_color" type="color3" value="0.25, 0.5, 0.75"/>
  </standard_surface>
  <surfacematerial name="m" type="material">
    <input name="surfaceshader" type="surfaceshader" nodename="ss"/>
  </surfacematerial>
</materialx>
`)
	doc, err := materialx.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	s, err := doc.DefaultBaseColorSampler()
	if err != nil {
		t.Fatal(err)
	}
	want := [3]float64{0.25, 0.5, 0.75}
	for _, p := range [][3]float64{{0, 0, 0}, {1, 2, 3}, {-5, 100, 0.1}} {
		if got := s.Sample(p); got != want {
			t.Errorf("Sample(%v) = %v, want %v", p, got, want)
		}
	}
}

// TestAttributeOrderRobustness exercises the parser on input elements
// whose `value` attribute appears before `type`. XML attributes are
// unordered, so a parser that consumed them in iteration order would
// try to parse the value as TypeUnknown and fail.
func TestAttributeOrderRobustness(t *testing.T) {
	src := strings.NewReader(`<?xml version="1.0"?>
<materialx version="1.39">
  <nodegraph name="ng">
    <input value="0.4, 0.6, 0.8" type="color3" name="bg"/>
    <input value="0.0, 0.2, 0.4" type="color3" name="fg"/>
    <mix name="m" type="color3">
      <input nodename="" value="0.5" type="float" name="mix"/>
      <input interfacename="bg" type="color3" name="bg"/>
      <input interfacename="fg" type="color3" name="fg"/>
    </mix>
    <output type="color3" nodename="m" name="out"/>
  </nodegraph>
  <standard_surface name="ss" type="surfaceshader">
    <input name="base_color" type="color3" nodegraph="ng" output="out"/>
  </standard_surface>
  <surfacematerial name="m" type="material">
    <input name="surfaceshader" type="surfaceshader" nodename="ss"/>
  </surfacematerial>
</materialx>
`)
	doc, err := materialx.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s, err := doc.DefaultBaseColorSampler()
	if err != nil {
		t.Fatalf("sampler: %v", err)
	}
	got := s.Sample([3]float64{0, 0, 0})
	want := [3]float64{0.2, 0.4, 0.6} // mix(bg, fg, 0.5) = 0.5*bg + 0.5*fg
	for i := range 3 {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Errorf("channel %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

// TestMixScalarBroadcast checks that a scalar fed into a color3 mix is
// broadcast across all channels (per MaterialX implicit-conversion
// rules) rather than producing zeros in components 1-2.
func TestMixScalarBroadcast(t *testing.T) {
	src := strings.NewReader(`<?xml version="1.0"?>
<materialx version="1.39">
  <nodegraph name="ng">
    <mix name="m" type="color3">
      <input name="bg" type="float" value="0.2"/>
      <input name="fg" type="float" value="0.8"/>
      <input name="mix" type="float" value="0.25"/>
    </mix>
    <output name="out" type="color3" nodename="m"/>
  </nodegraph>
  <standard_surface name="ss" type="surfaceshader">
    <input name="base_color" type="color3" nodegraph="ng" output="out"/>
  </standard_surface>
  <surfacematerial name="m" type="material">
    <input name="surfaceshader" type="surfaceshader" nodename="ss"/>
  </surfacematerial>
</materialx>
`)
	doc, err := materialx.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	s, err := doc.DefaultBaseColorSampler()
	if err != nil {
		t.Fatal(err)
	}
	got := s.Sample([3]float64{0, 0, 0})
	want := 0.2*0.75 + 0.8*0.25 // 0.35
	for i := range 3 {
		if math.Abs(got[i]-want) > 1e-9 {
			t.Errorf("channel %d: got %v, want %v (scalar should broadcast)", i, got[i], want)
		}
	}
}

// TestArithmeticTypeCoercion checks that vector op scalar broadcasts
// the scalar across the vector's components — e.g. multiply(vec3, float).
func TestArithmeticTypeCoercion(t *testing.T) {
	src := strings.NewReader(`<?xml version="1.0"?>
<materialx version="1.39">
  <nodegraph name="ng">
    <multiply name="m" type="color3">
      <input name="in1" type="color3" value="0.1, 0.2, 0.3"/>
      <input name="in2" type="float" value="2.0"/>
    </multiply>
    <output name="out" type="color3" nodename="m"/>
  </nodegraph>
  <standard_surface name="ss" type="surfaceshader">
    <input name="base_color" type="color3" nodegraph="ng" output="out"/>
  </standard_surface>
  <surfacematerial name="m" type="material">
    <input name="surfaceshader" type="surfaceshader" nodename="ss"/>
  </surfacematerial>
</materialx>
`)
	doc, err := materialx.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	s, err := doc.DefaultBaseColorSampler()
	if err != nil {
		t.Fatal(err)
	}
	got := s.Sample([3]float64{0, 0, 0})
	want := [3]float64{0.2, 0.4, 0.6}
	for i := range 3 {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Errorf("channel %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

// TestInterfaceFallthroughNoDefault verifies that referencing a graph
// input that has no value attribute uses the type's zero value (rather
// than crashing or producing an error).
func TestInterfaceFallthroughNoDefault(t *testing.T) {
	src := strings.NewReader(`<?xml version="1.0"?>
<materialx version="1.39">
  <nodegraph name="ng">
    <input name="amount" type="float"/>
    <multiply name="m" type="color3">
      <input name="in1" type="color3" value="0.5, 0.5, 0.5"/>
      <input name="in2" type="float" interfacename="amount"/>
    </multiply>
    <output name="out" type="color3" nodename="m"/>
  </nodegraph>
  <standard_surface name="ss" type="surfaceshader">
    <input name="base_color" type="color3" nodegraph="ng" output="out"/>
  </standard_surface>
  <surfacematerial name="m" type="material">
    <input name="surfaceshader" type="surfaceshader" nodename="ss"/>
  </surfacematerial>
</materialx>
`)
	doc, err := materialx.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	s, err := doc.DefaultBaseColorSampler()
	if err != nil {
		t.Fatal(err)
	}
	got := s.Sample([3]float64{0, 0, 0})
	want := [3]float64{0, 0, 0}
	for i := range 3 {
		if got[i] != want[i] {
			t.Errorf("channel %d: got %v, want %v (missing default → zero)", i, got[i], want[i])
		}
	}
}

// TestPositionSpaceUnsupported verifies that a position node with a
// non-default space attribute fails at construction rather than
// silently producing wrong coordinates.
func TestPositionSpaceUnsupported(t *testing.T) {
	src := strings.NewReader(`<?xml version="1.0"?>
<materialx version="1.39">
  <nodegraph name="ng">
    <position name="p" type="vector3">
      <input name="space" type="string" value="world"/>
    </position>
    <output name="out" type="color3" nodename="p"/>
  </nodegraph>
  <standard_surface name="ss" type="surfaceshader">
    <input name="base_color" type="color3" nodegraph="ng" output="out"/>
  </standard_surface>
  <surfacematerial name="m" type="material">
    <input name="surfaceshader" type="surfaceshader" nodename="ss"/>
  </surfacematerial>
</materialx>
`)
	doc, err := materialx.Parse(src)
	if err != nil {
		// Parse may fail because string isn't a known type — also acceptable.
		return
	}
	if _, err := doc.DefaultBaseColorSampler(); err == nil {
		t.Errorf("expected error for unsupported position space, got nil")
	}
}

// TestPerlinGoldenValues pins a handful of perlin3D outputs at known
// inputs. The Perlin permutation table is the reference Ken Perlin
// 2002 table; if any of these golden values change, the noise
// implementation has drifted from the standard.
func TestPerlinGoldenValues(t *testing.T) {
	// Computed once from this implementation; serves as a regression
	// guard. Any future reshuffle of the permutation table or edit to
	// fade/grad/lerp must be reflected here intentionally.
	cases := []struct {
		x, y, z float64
		want    float64
	}{
		{0, 0, 0, 0},
		{0.5, 0.5, 0.5, -0.25},
		{0.25, 0.6, 0.1, -0.10208973162000007},
		{1.5, 2.5, 3.5, 0.125},
	}
	for _, tc := range cases {
		got := materialx.PerlinForTest(tc.x, tc.y, tc.z)
		if math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("perlin3D(%v,%v,%v) = %v, want %v", tc.x, tc.y, tc.z, got, tc.want)
		}
	}
}

// BenchmarkSampleMarble measures per-Sample cost on the hot path. The
// closure-tree compiler should produce zero allocations per call so
// the voxelizer can call this millions of times per print without GC
// pressure.
func BenchmarkSampleMarble(b *testing.B) {
	f, err := os.Open("testdata/standard_surface_marble_solid.mtlx")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	doc, err := materialx.Parse(f)
	if err != nil {
		b.Fatal(err)
	}
	s, err := doc.DefaultBaseColorSampler()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	var sink [3]float64
	i := 0
	for b.Loop() {
		sink = s.Sample([3]float64{float64(i) * 0.001, 0.4, -0.7})
		i++
	}
	_ = sink
}

func loadMarble(t *testing.T) *materialx.Document {
	t.Helper()
	f, err := os.Open("testdata/standard_surface_marble_solid.mtlx")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	doc, err := materialx.Parse(f)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}
