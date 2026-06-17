package materialx

import (
	"strings"
	"testing"
)

// smoothstep eases a float through a Hermite ramp, clamping outside
// [low, high]. Here it drives a color3 (broadcast across channels) so we
// can read the result through the base-color sampler.
func TestSmoothstepRamp(t *testing.T) {
	const doc = `<?xml version="1.0"?>
<materialx version="1.38">
  <nodegraph name="ng">
    <position name="p" type="vector3"/>
    <separate3 name="xyz" type="multioutput">
      <input name="in" type="vector3" nodename="p"/>
    </separate3>
    <smoothstep name="s" type="color3">
      <input name="in" type="float" nodename="xyz" output="outz"/>
      <input name="low" type="float" value="0.0"/>
      <input name="high" type="float" value="1.0"/>
    </smoothstep>
    <output name="out" type="color3" nodename="s"/>
  </nodegraph>
  <standard_surface name="srf" type="surfaceshader">
    <input name="base_color" type="color3" nodegraph="ng" output="out"/>
  </standard_surface>
  <surfacematerial name="M" type="material">
    <input name="surfaceshader" type="surfaceshader" nodename="srf"/>
  </surfacematerial>
</materialx>`
	s := sampleColorGraph(t, doc)
	hermite := func(x float64) float64 { return x * x * (3 - 2*x) }
	cases := []struct {
		z, want float64
	}{
		{-0.5, 0},        // below low -> clamped to 0
		{0.0, 0},         // at low
		{0.25, hermite(0.25)},
		{0.5, 0.5},       // midpoint of a symmetric ramp
		{0.75, hermite(0.75)},
		{1.0, 1},         // at high
		{1.5, 1},         // above high -> clamped to 1
	}
	for _, c := range cases {
		got := s.Sample([3]float64{0, 0, c.z})
		if !nearVec3(got, [3]float64{c.want, c.want, c.want}) {
			t.Errorf("smoothstep(0,1,%v): got %v want %v", c.z, got[0], c.want)
		}
	}
}

// A degenerate band (high <= low) must collapse to a hard step rather
// than divide by zero.
func TestSmoothstepDegenerateBand(t *testing.T) {
	if v := smoothstepF(0.3, 0.5, 0.5); v != 0 {
		t.Errorf("below collapsed band: got %v want 0", v)
	}
	if v := smoothstepF(0.7, 0.5, 0.5); v != 1 {
		t.Errorf("at/above collapsed band: got %v want 1", v)
	}
	if v := smoothstepF(0.7, 0.6, 0.4); v != 1 {
		t.Errorf("inverted band: got %v want 1", v)
	}
}

// MaterialX files commonly use "----" dividers inside comments, which is
// illegal per the strict XML spec. Parse must tolerate them.
func TestParseTolerantOfDashesInComments(t *testing.T) {
	const doc = `<?xml version="1.0"?>
<materialx version="1.38">
  <!-- ---- a section divider with -- double dashes ---- -->
  <nodegraph name="ng">
    <constant name="c" type="color3">
      <input name="value" type="color3" value="0.2, 0.4, 0.6"/>
    </constant>
    <output name="out" type="color3" nodename="c"/>
  </nodegraph>
  <standard_surface name="srf" type="surfaceshader">
    <input name="base_color" type="color3" nodegraph="ng" output="out"/>
  </standard_surface>
  <surfacematerial name="M" type="material">
    <input name="surfaceshader" type="surfaceshader" nodename="srf"/>
  </surfacematerial>
</materialx>`
	s := sampleColorGraph(t, doc)
	if got := s.Sample([3]float64{0, 0, 0}); !nearVec3(got, [3]float64{0.2, 0.4, 0.6}) {
		t.Errorf("got %v want {0.2 0.4 0.6}", got)
	}
}

// blankXMLComments must preserve newlines so post-comment parse errors
// still report the correct line number.
func TestBlankXMLCommentsPreservesLines(t *testing.T) {
	in := "a\n<!-- multi\nline -- comment -->\nb"
	out := string(blankXMLComments([]byte(in)))
	if strings.Count(out, "\n") != strings.Count(in, "\n") {
		t.Fatalf("newline count changed: %q", out)
	}
	if len(out) != len(in) {
		t.Fatalf("length changed: %d != %d", len(out), len(in))
	}
	if strings.Contains(out, "comment") {
		t.Errorf("comment body not blanked: %q", out)
	}
	if !strings.HasPrefix(out, "a\n") || !strings.HasSuffix(out, "\nb") {
		t.Errorf("non-comment text altered: %q", out)
	}
}
