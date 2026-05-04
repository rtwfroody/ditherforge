package materialx_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
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
		{0.5, 0.5, 0.5, -0.2455},
		{0.25, 0.6, 0.1, -0.10025211645084006},
		{1.5, 2.5, 3.5, 0.12275},
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

// stripePNG returns a 4×1 PNG with horizontal stripes red, green,
// blue, white. Used by image-graph tests.
func stripePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	cells := []color.NRGBA{
		{R: 255, A: 255},
		{G: 255, A: 255},
		{B: 255, A: 255},
		{R: 255, G: 255, B: 255, A: 255},
	}
	for i, c := range cells {
		img.Set(i, 0, c)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode stripe png: %v", err)
	}
	return buf.Bytes()
}

// imageGraphMtlx is a minimal .mtlx that wires image → texcoord
// directly (no UV multiplier) so test UVs land at known pixel centers.
const imageGraphMtlx = `<?xml version="1.0"?>
<materialx version="1.39">
  <nodegraph name="ng">
    <texcoord name="uv" type="vector2">
      <input name="index" type="integer" value="0"/>
    </texcoord>
    <image name="img" type="color3">
      <input name="texcoord" type="vector2" nodename="uv"/>
      <input name="file" type="filename" value="stripe.png"/>
      <input name="uaddressmode" type="string" value="periodic"/>
      <input name="vaddressmode" type="string" value="periodic"/>
      <input name="filtertype" type="string" value="closest"/>
    </image>
    <output name="out" type="color3" nodename="img"/>
  </nodegraph>
  <standard_surface name="ss" type="surfaceshader">
    <input name="base_color" type="color3" nodegraph="ng" output="out"/>
  </standard_surface>
  <surfacematerial name="m" type="material">
    <input name="surfaceshader" type="surfaceshader" nodename="ss"/>
  </surfacematerial>
</materialx>
`

func TestImageGraphSamplesExactPixelCenters(t *testing.T) {
	doc, err := materialx.ParseBytes([]byte(imageGraphMtlx))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	doc.Resolver = materialx.NewMapResolver(map[string][]byte{
		"stripe.png": stripePNG(t),
	})
	s, err := doc.DefaultBaseColorSampler()
	if err != nil {
		t.Fatalf("sampler: %v", err)
	}
	// Pixel centers for a 4-wide image are at u = 0.125, 0.375, 0.625, 0.875.
	// V is irrelevant for a 1-tall image. Address mode is periodic but
	// we stay inside [0, 1] here so it doesn't matter.
	cases := []struct {
		u    float64
		want [3]float64
	}{
		{0.125, [3]float64{1, 0, 0}}, // red
		{0.375, [3]float64{0, 1, 0}}, // green
		{0.625, [3]float64{0, 0, 1}}, // blue
		{0.875, [3]float64{1, 1, 1}}, // white
	}
	for _, tc := range cases {
		got := s.SampleAt(materialx.SampleContext{UV: [2]float64{tc.u, 0.5}})
		for i := range 3 {
			if math.Abs(got[i]-tc.want[i]) > 1e-9 {
				t.Errorf("Sample(u=%v): got %v, want %v", tc.u, got, tc.want)
				break
			}
		}
	}
}

func TestImageAddressingModes(t *testing.T) {
	// Same fixture as TestImageGraphSamples but with each addressmode
	// substituted in. We test by sampling outside [0, 1] and checking
	// where the lookup lands.
	pngBytes := stripePNG(t)
	for _, mode := range []string{"periodic", "clamp", "mirror"} {
		t.Run(mode, func(t *testing.T) {
			mtlx := strings.ReplaceAll(imageGraphMtlx, `value="periodic"`, `value="`+mode+`"`)
			doc, err := materialx.ParseBytes([]byte(mtlx))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			doc.Resolver = materialx.NewMapResolver(map[string][]byte{"stripe.png": pngBytes})
			s, err := doc.DefaultBaseColorSampler()
			if err != nil {
				t.Fatalf("sampler: %v", err)
			}

			// Sample at u = -0.125 (one pixel left of u=0).
			got := s.SampleAt(materialx.SampleContext{UV: [2]float64{-0.125, 0.5}})
			var want [3]float64
			switch mode {
			case "periodic":
				// -0.125 wraps to 0.875 → white pixel.
				want = [3]float64{1, 1, 1}
			case "clamp":
				// Clamps to 0 → red pixel.
				want = [3]float64{1, 0, 0}
			case "mirror":
				// Mirrors at 0 → 0.125 → red pixel.
				want = [3]float64{1, 0, 0}
			}
			for i := range 3 {
				if math.Abs(got[i]-want[i]) > 1e-9 {
					t.Errorf("%s mode at u=-0.125: got %v, want %v", mode, got, want)
					break
				}
			}
		})
	}
}

// uvScaleMtlx multiplies texcoord by 2 before sampling — a single tile
// of the texture covers UV [0, 0.5]; UV [0.5, 1] also tiles the same
// content (with periodic addressing).
const uvScaleMtlx = `<?xml version="1.0"?>
<materialx version="1.39">
  <nodegraph name="ng">
    <texcoord name="uv" type="vector2"><input name="index" type="integer" value="0"/></texcoord>
    <constant name="scale" type="float"><input name="value" type="float" value="2.0"/></constant>
    <multiply name="suv" type="vector2">
      <input name="in1" type="vector2" nodename="uv"/>
      <input name="in2" type="float" nodename="scale"/>
    </multiply>
    <image name="img" type="color3">
      <input name="texcoord" type="vector2" nodename="suv"/>
      <input name="file" type="filename" value="stripe.png"/>
      <input name="filtertype" type="string" value="closest"/>
    </image>
    <output name="out" type="color3" nodename="img"/>
  </nodegraph>
  <standard_surface name="ss" type="surfaceshader">
    <input name="base_color" type="color3" nodegraph="ng" output="out"/>
  </standard_surface>
  <surfacematerial name="m" type="material">
    <input name="surfaceshader" type="surfaceshader" nodename="ss"/>
  </surfacematerial>
</materialx>
`

func TestUVScalingTilesTexture(t *testing.T) {
	doc, err := materialx.ParseBytes([]byte(uvScaleMtlx))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	doc.Resolver = materialx.NewMapResolver(map[string][]byte{"stripe.png": stripePNG(t)})
	s, err := doc.DefaultBaseColorSampler()
	if err != nil {
		t.Fatalf("sampler: %v", err)
	}
	// UV 0.0625 * 2 = 0.125 → red. UV 0.5625 * 2 = 1.125 wraps → 0.125 → red.
	for _, u := range []float64{0.0625, 0.5625} {
		got := s.SampleAt(materialx.SampleContext{UV: [2]float64{u, 0.5}})
		want := [3]float64{1, 0, 0}
		for i := range 3 {
			if math.Abs(got[i]-want[i]) > 1e-9 {
				t.Errorf("u=%v: got %v, want %v (UV scaling/wrap broken)", u, got, want)
				break
			}
		}
	}
}

func TestParsePackageZip(t *testing.T) {
	// Build an in-memory zip containing the .mtlx + the PNG, write it
	// to a temp file, and load via ParsePackage.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	addZip := func(name string, data []byte) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	addZip("pack.mtlx", []byte(imageGraphMtlx))
	addZip("stripe.png", stripePNG(t))
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}

	tmp := filepath.Join(t.TempDir(), "pack.zip")
	if err := os.WriteFile(tmp, buf.Bytes(), 0644); err != nil {
		t.Fatalf("write zip: %v", err)
	}

	doc, err := materialx.ParsePackage(tmp)
	if err != nil {
		t.Fatalf("ParsePackage: %v", err)
	}
	s, err := doc.DefaultBaseColorSampler()
	if err != nil {
		t.Fatalf("sampler: %v", err)
	}
	got := s.SampleAt(materialx.SampleContext{UV: [2]float64{0.125, 0.5}})
	want := [3]float64{1, 0, 0}
	for i := range 3 {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Errorf("zip-loaded sampler at u=0.125: got %v, want %v", got, want)
			break
		}
	}
}

func TestParsePackageZipWithSubdirectory(t *testing.T) {
	// Many real-world packs have the .mtlx + textures inside a single
	// top-level folder rather than at the archive root. Verify the
	// resolver's prefix logic handles that case (image's relative
	// "stripe.png" resolves via the .mtlx's containing directory).
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	addZip := func(name string, data []byte) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	addZip("Pack/pack.mtlx", []byte(imageGraphMtlx))
	addZip("Pack/stripe.png", stripePNG(t))
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	tmp := filepath.Join(t.TempDir(), "pack.zip")
	if err := os.WriteFile(tmp, buf.Bytes(), 0644); err != nil {
		t.Fatalf("write zip: %v", err)
	}
	doc, err := materialx.ParsePackage(tmp)
	if err != nil {
		t.Fatalf("ParsePackage: %v", err)
	}
	s, err := doc.DefaultBaseColorSampler()
	if err != nil {
		t.Fatalf("sampler: %v", err)
	}
	got := s.SampleAt(materialx.SampleContext{UV: [2]float64{0.125, 0.5}})
	want := [3]float64{1, 0, 0}
	for i := range 3 {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Errorf("subdir-zip-loaded sampler at u=0.125: got %v, want %v", got, want)
			break
		}
	}
}

func TestImageGraphRequiresResolver(t *testing.T) {
	doc, err := materialx.ParseBytes([]byte(imageGraphMtlx))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// No Resolver set — image node should fail at sampler construction.
	if _, err := doc.DefaultBaseColorSampler(); err == nil {
		t.Errorf("expected error when Resolver is nil, got success")
	}
}

// TestHSVAdjustShiftsHueAndScalesSV exercises the hsvadjust node added
// for Polyhaven-style PBR packs (Bricks_2k_8b et al.). Round-trip
// through HSV with no shift should leave color unchanged; a 1/3-cycle
// hue shift on pure red should produce green.
func TestHSVAdjustShiftsHueAndScalesSV(t *testing.T) {
	const tmpl = `<?xml version="1.0"?>
<materialx version="1.38">
  <nodegraph name="ng">
    <constant name="src" type="color3"><input name="value" type="color3" value="1.0, 0.0, 0.0"/></constant>
    <constant name="amt" type="vector3"><input name="value" type="vector3" value="AMOUNT"/></constant>
    <hsvadjust name="adj" type="color3">
      <input name="in" type="color3" nodename="src"/>
      <input name="amount" type="vector3" nodename="amt"/>
    </hsvadjust>
    <output name="out" type="color3" nodename="adj"/>
  </nodegraph>
  <standard_surface name="ss" type="surfaceshader">
    <input name="base_color" type="color3" nodegraph="ng" output="out"/>
  </standard_surface>
  <surfacematerial name="m" type="material">
    <input name="surfaceshader" type="surfaceshader" nodename="ss"/>
  </surfacematerial>
</materialx>`
	cases := []struct {
		name   string
		amount string
		want   [3]float64
	}{
		{"no-shift", "0.0, 1.0, 1.0", [3]float64{1, 0, 0}},
		{"hue+1/3 → green", "0.333333333333, 1.0, 1.0", [3]float64{0, 1, 0}},
		{"sat=0 → grayscale value", "0.0, 0.0, 1.0", [3]float64{1, 1, 1}},
		{"value=0 → black", "0.0, 1.0, 0.0", [3]float64{0, 0, 0}},
		// Polyhaven Bricks_2k_8b uses sat > 1 to push grout colors and
		// value > 1 to brighten. Sat must clamp at 1 before back-conversion
		// (otherwise the 1-s term in hsvToRGB goes negative and produces
		// garbage); value can pass through and let the byte quantizer clamp
		// per-channel. Pure red is already maximally saturated, so sat*1.2
		// stays at 1; value*2.0 exceeds 1 but clamps to 1 per channel.
		{"sat=1.2 idempotent on already-saturated", "0.0, 1.2, 1.0", [3]float64{1, 0, 0}},
		{"value=2.0 brightens past 1", "0.0, 1.0, 2.0", [3]float64{2, 0, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := materialx.ParseBytes([]byte(strings.Replace(tmpl, "AMOUNT", tc.amount, 1)))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			s, err := doc.DefaultBaseColorSampler()
			if err != nil {
				t.Fatalf("sampler: %v", err)
			}
			got := s.SampleAt(materialx.SampleContext{})
			for i := range 3 {
				if math.Abs(got[i]-tc.want[i]) > 1e-6 {
					t.Errorf("got %v, want %v", got, tc.want)
					break
				}
			}
		})
	}
}

// TestImageVector4ReadsAlpha ensures vector4-typed image nodes pull the
// actual alpha channel out of the PNG instead of pinning it to 1. PBR
// packs use 4-channel mask textures where alpha encodes data (leak
// presence, etc.) — pinning alpha breaks the masking math. The graph
// extracts each channel of the image node directly so the assertion
// pins the buildImage vector4 path with no intermediate ops to mask
// regressions.
func TestImageVector4ReadsAlpha(t *testing.T) {
	// 1×1 RGBA = (10, 20, 30, 200).
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 200})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Build a graph with one extract per channel, wired into a
	// constant-from-floats color3 reconstruction (R from the alpha
	// extract, G/B from the R/G extracts) so we can read all four
	// channels of the source image through one base_color call.
	const mtlx = `<?xml version="1.0"?>
<materialx version="1.38">
  <nodegraph name="ng">
    <texcoord name="uv" type="vector2"><input name="index" type="integer" value="0"/></texcoord>
    <image name="img" type="vector4">
      <input name="texcoord" type="vector2" nodename="uv"/>
      <input name="file" type="filename" value="px.png"/>
      <input name="uaddressmode" type="string" value="clamp"/>
      <input name="vaddressmode" type="string" value="clamp"/>
      <input name="filtertype" type="string" value="closest"/>
    </image>
    <extract name="ch_INDEX" type="float">
      <input name="in" type="vector4" nodename="img"/>
      <input name="index" type="integer" value="INDEX"/>
    </extract>
    <output name="out" type="float" nodename="ch_INDEX"/>
  </nodegraph>
  <standard_surface name="ss" type="surfaceshader">
    <input name="base_color" type="color3" nodegraph="ng" output="out"/>
  </standard_surface>
  <surfacematerial name="m" type="material">
    <input name="surfaceshader" type="surfaceshader" nodename="ss"/>
  </surfacematerial>
</materialx>`
	cases := []struct {
		index int
		want  float64
	}{
		{0, 10.0 / 255},
		{1, 20.0 / 255},
		{2, 30.0 / 255},
		{3, 200.0 / 255},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("index=%d", tc.index), func(t *testing.T) {
			src := strings.ReplaceAll(mtlx, "INDEX", fmt.Sprintf("%d", tc.index))
			doc, err := materialx.ParseBytes([]byte(src))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			doc.Resolver = materialx.NewMapResolver(map[string][]byte{"px.png": buf.Bytes()})
			s, err := doc.DefaultBaseColorSampler()
			if err != nil {
				t.Fatalf("sampler: %v", err)
			}
			// Float-typed output broadcasts across the color3 sample;
			// sampling all three channels and checking one is enough.
			got := s.SampleAt(materialx.SampleContext{UV: [2]float64{0.5, 0.5}})
			if math.Abs(got[0]-tc.want) > 1e-6 {
				t.Errorf("got %v, want %v", got[0], tc.want)
			}
		})
	}
}

// TestPolyhavenStyleGraphCompiles end-to-end-parses a graph that
// mirrors the Polyhaven Bricks_2k_8b structure (image → extract on a
// vector4 mask, hsvadjust on the base color, floor on the leak gate,
// mix to combine). Guards against a regression where any one of those
// node types stops compiling and the override silently falls through
// to gray.
func TestPolyhavenStyleGraphCompiles(t *testing.T) {
	// 1×1 mask image; alpha < 1 so floor(alpha) = 0, exercising the
	// branch that motivated the bug fix.
	maskImg := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	maskImg.Set(0, 0, color.NRGBA{R: 50, G: 100, B: 150, A: 200})
	var maskBuf bytes.Buffer
	if err := png.Encode(&maskBuf, maskImg); err != nil {
		t.Fatalf("encode mask: %v", err)
	}
	colorImg := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	colorImg.Set(0, 0, color.NRGBA{R: 180, G: 80, B: 40, A: 255})
	var colorBuf bytes.Buffer
	if err := png.Encode(&colorBuf, colorImg); err != nil {
		t.Fatalf("encode color: %v", err)
	}
	const mtlx = `<?xml version="1.0"?>
<materialx version="1.38">
  <nodegraph name="ng">
    <texcoord name="uv" type="vector2"><input name="index" type="integer" value="0"/></texcoord>
    <image name="basecol" type="color3">
      <input name="texcoord" type="vector2" nodename="uv"/>
      <input name="file" type="filename" value="color.png"/>
      <input name="uaddressmode" type="string" value="periodic"/>
      <input name="vaddressmode" type="string" value="periodic"/>
    </image>
    <image name="mask" type="vector4">
      <input name="texcoord" type="vector2" nodename="uv"/>
      <input name="file" type="filename" value="mask.png"/>
      <input name="uaddressmode" type="string" value="periodic"/>
      <input name="vaddressmode" type="string" value="periodic"/>
    </image>
    <extract name="alpha" type="float">
      <input name="in" type="vector4" nodename="mask"/>
      <input name="index" type="integer" value="3"/>
    </extract>
    <floor name="gate" type="float"><input name="in" type="float" nodename="alpha"/></floor>
    <constant name="shift" type="vector3"><input name="value" type="vector3" value="0.0, 1.2, 0.9"/></constant>
    <hsvadjust name="adj" type="color3">
      <input name="in" type="color3" nodename="basecol"/>
      <input name="amount" type="vector3" nodename="shift"/>
    </hsvadjust>
    <constant name="leak" type="color3"><input name="value" type="color3" value="0.04, 0.05, 0.11"/></constant>
    <mix name="combine" type="color3">
      <input name="bg" type="color3" nodename="adj"/>
      <input name="fg" type="color3" nodename="leak"/>
      <input name="mix" type="float" nodename="gate"/>
    </mix>
    <output name="out" type="color3" nodename="combine"/>
  </nodegraph>
  <standard_surface name="ss" type="surfaceshader">
    <input name="base_color" type="color3" nodegraph="ng" output="out"/>
  </standard_surface>
  <surfacematerial name="m" type="material">
    <input name="surfaceshader" type="surfaceshader" nodename="ss"/>
  </surfacematerial>
</materialx>`
	doc, err := materialx.ParseBytes([]byte(mtlx))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	doc.Resolver = materialx.NewMapResolver(map[string][]byte{
		"color.png": colorBuf.Bytes(),
		"mask.png":  maskBuf.Bytes(),
	})
	s, err := doc.DefaultBaseColorSampler()
	if err != nil {
		t.Fatalf("sampler (regression: did a node type stop compiling?): %v", err)
	}
	got := s.SampleAt(materialx.SampleContext{UV: [2]float64{0.5, 0.5}})
	// Mask alpha = 200/255 < 1, floor = 0, mix selects bg (the hsvadjust
	// result). Expect non-zero RGB derived from (180, 80, 40) shifted in
	// HSV space — the exact value depends on the round-trip but it must
	// not match the leak constant and must be finite/non-negative.
	leak := [3]float64{0.04, 0.05, 0.11}
	allLeak := true
	for i := range 3 {
		if math.Abs(got[i]-leak[i]) > 1e-3 {
			allLeak = false
		}
		if got[i] < 0 || math.IsNaN(got[i]) || math.IsInf(got[i], 0) {
			t.Errorf("channel %d went out of bounds: %v", i, got[i])
		}
	}
	if allLeak {
		t.Errorf("got leak color %v, expected hsvadjust output (mask gate should be 0)", got)
	}
}
