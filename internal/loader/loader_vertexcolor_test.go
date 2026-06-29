package loader

import (
	"testing"

	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"
)

// TestLinearToSRGB pins the endpoints and a midpoint of the linear→sRGB curve.
func TestLinearToSRGB(t *testing.T) {
	cases := []struct {
		lin  float64
		want uint8
	}{
		{0, 0},
		{1, 255},
		{0.5, 188},       // mid linear is much brighter once gamma-encoded
		{32767.0 / 65535, 188}, // Bowl1.glb blue channel
	}
	for _, c := range cases {
		if got := linearToSRGB(c.lin); got != c.want {
			t.Errorf("linearToSRGB(%v) = %d, want %d", c.lin, got, c.want)
		}
	}
}

// TestReadVertexColorsSRGB guards two bugs that made vertex-colored GLBs
// (e.g. Bowl1.glb) render with wrong colors:
//   - modeler.ReadColor narrowed a normalized UNSIGNED_SHORT accessor to its
//     low byte (3483=0x0D9B → 0x9B) instead of scaling, turning blue into a
//     muddy purple and inventing thousands of bogus colors.
//   - glTF vertex colors are linear; the pipeline works in sRGB, so they must
//     be gamma-encoded to match on-screen appearance and the filament palette.
func TestReadVertexColorsSRGB(t *testing.T) {
	// Linear RGBA as normalized uint16 — the Bowl1.glb storage format.
	in := [][4]uint16{
		{3483, 5676, 32767, 65535}, // the dominant "blue" vertex color
		{0, 0, 0, 65535},
		{65535, 65535, 65535, 32767}, // white with half alpha
	}

	doc := gltf.NewDocument()
	accIdx := modeler.WriteColor(doc, in)
	acc := doc.Accessors[accIdx]
	if acc.ComponentType != gltf.ComponentUshort || !acc.Normalized {
		t.Fatalf("test setup: want normalized ushort accessor, got compType=%v normalized=%v",
			acc.ComponentType, acc.Normalized)
	}

	got, err := readVertexColorsSRGB(doc, acc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(in) {
		t.Fatalf("len = %d, want %d", len(got), len(in))
	}

	// The blue color must be gamma-encoded, never the #9b2cff low-byte garbage.
	if got[0] == ([4]uint8{0x9b, 0x2c, 0xff, 0xff}) {
		t.Fatalf("low-byte truncation regression: got #9b2cffff")
	}
	wantBlue := [4]uint8{
		linearToSRGB(3483.0 / 65535),
		linearToSRGB(5676.0 / 65535),
		linearToSRGB(32767.0 / 65535),
		255,
	}
	if got[0] != wantBlue {
		t.Errorf("blue: got #%02x%02x%02x a=%d, want #%02x%02x%02x a=%d",
			got[0][0], got[0][1], got[0][2], got[0][3],
			wantBlue[0], wantBlue[1], wantBlue[2], wantBlue[3])
	}
	if got[1] != ([4]uint8{0, 0, 0, 255}) {
		t.Errorf("black: got %v, want {0,0,0,255}", got[1])
	}
	// Alpha is scaled linearly (not gamma-encoded): 32767/65535 → ~128.
	if got[2][0] != 255 || got[2][1] != 255 || got[2][2] != 255 {
		t.Errorf("white RGB: got %v", got[2])
	}
	if got[2][3] < 127 || got[2][3] > 128 {
		t.Errorf("half alpha: got %d, want ~128", got[2][3])
	}
}
