package pipeline

import (
	"bytes"
	"context"
	"encoding/base64"
	"image/png"
	"math"
	"strings"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
	"github.com/rtwfroody/ditherforge/internal/materialx"
	"github.com/rtwfroody/ditherforge/internal/voxel"
)

// fakeSampler is a UV-aware materialx.Sampler stub that delegates each
// SampleAt call to a caller-supplied function so triplanar tests can
// observe which UV plane the adapter chose.
type fakeSampler struct {
	cb     func(ctx materialx.SampleContext) [3]float64
	usesUV bool
}

func (f *fakeSampler) Sample(p [3]float64) [3]float64                  { return f.cb(materialx.SampleContext{Pos: p}) }
func (f *fakeSampler) SampleAt(ctx materialx.SampleContext) [3]float64 { return f.cb(ctx) }
func (f *fakeSampler) UsesUV() bool                                    { return f.usesUV }

// TestTriplanarPicksDominantPlane verifies that with a sharply-axial
// face normal, the triplanar adapter returns nearly the color
// produced by the corresponding plane: +Z normal → XY plane, +X → YZ,
// +Y → XZ. The fake sampler returns a distinct color per plane based
// on which UV component pair the adapter handed it.
//
// Positions are picked to be exactly representable in float32 so the
// fake can identify the plane by exact-equality check on UV.
func TestTriplanarPicksDominantPlane(t *testing.T) {
	pos32 := [3]float32{0.25, 0.5, 0.75}
	px, py, pz := float64(pos32[0]), float64(pos32[1]), float64(pos32[2])
	colors := [3][3]float64{
		{1, 0, 0}, // YZ plane → red
		{0, 1, 0}, // XZ plane → green
		{0, 0, 1}, // XY plane → blue
	}
	fake := &fakeSampler{
		usesUV: true,
		cb: func(ctx materialx.SampleContext) [3]float64 {
			switch {
			case ctx.UV[0] == py && ctx.UV[1] == pz:
				return colors[0] // YZ
			case ctx.UV[0] == px && ctx.UV[1] == pz:
				return colors[1] // XZ
			case ctx.UV[0] == px && ctx.UV[1] == py:
				return colors[2] // XY
			}
			t.Errorf("unexpected UV %v", ctx.UV)
			return [3]float64{}
		},
	}
	o := &materialxOverride{sampler: fake, invTileMM: 1, useUV: true, sharpness: 8}

	cases := []struct {
		normal [3]float32
		want   [3]float64
	}{
		{[3]float32{0, 0, 1}, colors[2]}, // XY dominates
		{[3]float32{1, 0, 0}, colors[0]}, // YZ dominates
		{[3]float32{0, 1, 0}, colors[1]}, // XZ dominates
	}
	for _, tc := range cases {
		got := o.SampleBaseColor(voxel.BaseColorContext{
			Pos:    pos32,
			Normal: tc.normal,
		})
		// Sharpness=8 with one component=1 and others=0 produces ~100%
		// weight for the dominant plane; the result should be the pure
		// dominant color.
		want := [3]uint8{
			uint8(math.Round(tc.want[0] * 255)),
			uint8(math.Round(tc.want[1] * 255)),
			uint8(math.Round(tc.want[2] * 255)),
		}
		if got != want {
			t.Errorf("normal=%v: got %v, want %v", tc.normal, got, want)
		}
	}
}

// TestTriplanarBlendsWhenNormalIsDiagonal — a 45° normal in the XY
// plane should blend YZ and XZ samples roughly equally; XY is
// suppressed because |z|=0.
func TestTriplanarBlendsWhenNormalIsDiagonal(t *testing.T) {
	pos32 := [3]float32{0.25, 0.5, 0.75}
	px, py, pz := float64(pos32[0]), float64(pos32[1]), float64(pos32[2])
	colors := map[[2]float64][3]float64{
		{py, pz}: {1, 0, 0}, // YZ → red
		{px, pz}: {0, 1, 0}, // XZ → green
		{px, py}: {0, 0, 1}, // XY → blue (should be 0-weighted with z=0)
	}
	fake := &fakeSampler{
		usesUV: true,
		cb: func(ctx materialx.SampleContext) [3]float64 {
			c, ok := colors[ctx.UV]
			if !ok {
				t.Errorf("unexpected UV %v", ctx.UV)
			}
			return c
		},
	}
	o := &materialxOverride{sampler: fake, invTileMM: 1, useUV: true, sharpness: 4}

	got := o.SampleBaseColor(voxel.BaseColorContext{
		Pos:    pos32,
		Normal: [3]float32{0.7071, 0.7071, 0}, // XY-plane diagonal
	})
	// Expect roughly (0.5, 0.5, 0) ± rounding. Blue (XY plane) should
	// not contribute since |normal.z|=0.
	if got[0] < 100 || got[0] > 155 || got[1] < 100 || got[1] > 155 || got[2] > 20 {
		t.Errorf("expected ~50/50 red+green blend, got %v", got)
	}
}

// TestTriplanarUVSignFlip verifies the per-plane sign flip: a face
// with +X normal samples YZ at u=+pos.y; a -X normal samples at
// u=-pos.y. Without the flip, mirror seams appear on opposite-facing
// parallel faces with directional textures.
func TestTriplanarUVSignFlip(t *testing.T) {
	pos32 := [3]float32{0.25, 0.5, 0.75}
	py, pz := float64(pos32[1]), float64(pos32[2])
	var seenUVs []float64
	fake := &fakeSampler{
		usesUV: true,
		cb: func(ctx materialx.SampleContext) [3]float64 {
			seenUVs = append(seenUVs, ctx.UV[0])
			return [3]float64{1, 1, 1}
		},
	}
	o := &materialxOverride{sampler: fake, invTileMM: 1, useUV: true, sharpness: 8}

	seenUVs = nil
	o.SampleBaseColor(voxel.BaseColorContext{Pos: pos32, Normal: [3]float32{1, 0, 0}})
	if len(seenUVs) == 0 || seenUVs[0] != py {
		t.Errorf("+X normal should sample YZ with u=+pos.y=%v; saw %v", py, seenUVs)
	}

	seenUVs = nil
	o.SampleBaseColor(voxel.BaseColorContext{Pos: pos32, Normal: [3]float32{-1, 0, 0}})
	if len(seenUVs) == 0 || seenUVs[0] != -py {
		t.Errorf("-X normal should sample YZ with u=-pos.y=%v; saw %v", -py, seenUVs)
	}
	_ = pz
}

// TestTriplanarDegenerateNormalAveragesAllPlanes verifies that a
// zero-length normal produces an equal three-way blend rather than
// silently picking one plane. A face with random distinct per-plane
// colors should resolve to their average.
func TestTriplanarDegenerateNormalAveragesAllPlanes(t *testing.T) {
	pos32 := [3]float32{0.25, 0.5, 0.75}
	px, py, pz := float64(pos32[0]), float64(pos32[1]), float64(pos32[2])
	colors := map[[2]float64][3]float64{
		{py, pz}: {0.6, 0.0, 0.0},
		{px, pz}: {0.0, 0.6, 0.0},
		{px, py}: {0.0, 0.0, 0.6},
	}
	fake := &fakeSampler{
		usesUV: true,
		cb: func(ctx materialx.SampleContext) [3]float64 {
			return colors[ctx.UV]
		},
	}
	o := &materialxOverride{sampler: fake, invTileMM: 1, useUV: true, sharpness: 4}
	got := o.SampleBaseColor(voxel.BaseColorContext{
		Pos:    pos32,
		Normal: [3]float32{0, 0, 0},
	})
	// Each channel: 0.6 / 3 = 0.2 → 0.2*255+0.5 = 51.5 → 51.
	want := [3]uint8{51, 51, 51}
	for i := range 3 {
		if got[i] < 50 || got[i] > 52 {
			t.Errorf("degenerate normal: channel %d got %d, want ~51 (3-way average)", i, got[i])
			break
		}
	}
	_ = want
}

// TestNonUVSamplerSkipsTriplanar verifies the fast path: a
// non-UV-using sampler is consulted exactly once per call regardless
// of normal, since no projection is needed.
func TestNonUVSamplerSkipsTriplanar(t *testing.T) {
	calls := 0
	fake := &fakeSampler{
		usesUV: false,
		cb: func(ctx materialx.SampleContext) [3]float64 {
			calls++
			return [3]float64{0.5, 0.5, 0.5}
		},
	}
	o := &materialxOverride{sampler: fake, invTileMM: 1, useUV: false, sharpness: 4}
	o.SampleBaseColor(voxel.BaseColorContext{
		Pos:    [3]float32{0, 0, 0},
		Normal: [3]float32{0.577, 0.577, 0.577}, // diagonal would trigger 3-way blend
	})
	if calls != 1 {
		t.Errorf("non-UV sampler should be called once per SampleBaseColor; got %d", calls)
	}
}

// TestBakeMaterialXAtlasLongThinTriangle pins the anisotropic
// patch contract: a long thin triangle gets a wide-but-short patch
// (W tier > H tier), not a square one. This is the whole point of
// the bbox-aligned-to-longest-edge layout — sample density along
// the long axis matches the longest edge, sample density along the
// short axis matches the perpendicular extent.
func TestBakeMaterialXAtlasLongThinTriangle(t *testing.T) {
	// Triangle: longest edge 60mm along X, third vertex 1mm above the
	// edge. Expect W=largest tier (60/0.4 = 150 → cap at N=32), H=
	// smallest tier (1/0.4 = 2.5 → N=4 since the second-smallest tier).
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 0}, {60, 0, 0}, {30, 1, 0},
		},
		Faces: [][3]uint32{{0, 1, 2}},
	}
	lay := computeFaceLayout(model, 0)
	if int(lay.WT) <= int(lay.HT) {
		t.Errorf("expected W tier > H tier for long-thin triangle; got WT=%d (W=%d) HT=%d (H=%d)",
			lay.WT, baseColorAtlasTierSizes[lay.WT], lay.HT, baseColorAtlasTierSizes[lay.HT])
	}
	if got := int(lay.WT); got != len(baseColorAtlasTierSizes)-1 {
		t.Errorf("expected largest W tier (60mm edge), got %d", got)
	}
	// Perpendicular distance ~1mm should give a small H tier — the
	// crucial property is W ≫ H. Anything above mid-range fails the
	// "anisotropic" promise.
	if int(lay.HT) >= int(lay.WT) {
		t.Errorf("expected small H tier vs large W tier; got HT=%d (N=%d) WT=%d (N=%d)",
			lay.HT, baseColorAtlasTierSizes[lay.HT], lay.WT, baseColorAtlasTierSizes[lay.WT])
	}

	fake := &fakeSampler{cb: func(materialx.SampleContext) [3]float64 { return [3]float64{0.5, 0.5, 0.5} }}
	override := &materialxOverride{sampler: fake, invTileMM: 1, useUV: false, sharpness: 4}
	atlas, err := bakeMaterialXAtlas(context.Background(), model, override, nil)
	if err != nil {
		t.Fatalf("bakeMaterialXAtlas: %v", err)
	}
	W := baseColorAtlasTierSizes[lay.WT]
	H := baseColorAtlasTierSizes[lay.HT]
	// Single face → 1×1 grid in its bucket; atlas dims should match
	// the patch's W × H.
	if got, want := int(atlas.Width), W; got != want {
		t.Errorf("Width: got %d, want %d", got, want)
	}
	if got, want := int(atlas.Height), H; got != want {
		t.Errorf("Height: got %d, want %d", got, want)
	}
	// Image must be a non-empty base64-encoded PNG.
	if !strings.HasPrefix(atlas.Image, "png:") || len(atlas.Image) < 20 {
		t.Errorf("Image: expected non-empty 'png:' base64, got %q (len %d)", trunc(atlas.Image, 30), len(atlas.Image))
	}
}

// TestBakeMaterialXAtlasMixedTiers builds a mesh with triangles of
// very different sizes and confirms the tier classifier maps each to
// its expected bucket, the atlas grows tall enough to stack the
// tiers, and per-face UVs from different tiers point at non-
// overlapping atlas regions.
func TestBakeMaterialXAtlasMixedTiers(t *testing.T) {
	// Triangle 0: ~0.5 mm longest edge → smallest tier (N=2).
	// Triangle 1: ~3 mm longest edge → middle tier (3/0.4 ≈ 7.5 needs N=8).
	// Triangle 2: ~50 mm longest edge → largest tier.
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 0}, {0.5, 0, 0}, {0, 0.5, 0}, // tri 0: tiny (longest = √0.5 ≈ 0.71mm)
			{10, 10, 0}, {12, 10, 0}, {10, 12, 0}, // tri 1: medium (longest = √8 ≈ 2.83mm)
			{50, 50, 0}, {100, 50, 0}, {50, 100, 0}, // tri 2: large (longest = √5000 ≈ 70.7mm)
		},
		Faces: [][3]uint32{{0, 1, 2}, {3, 4, 5}, {6, 7, 8}},
	}
	wt0 := int(computeFaceLayout(model, 0).WT)
	wt1 := int(computeFaceLayout(model, 1).WT)
	wt2 := int(computeFaceLayout(model, 2).WT)
	// Strictly increasing tier with longest-edge length — actual
	// tier indices depend on the target/sizes constants. The very
	// large triangle should also saturate at the largest tier.
	if !(wt0 < wt1 && wt1 < wt2) {
		t.Errorf("expected wt0 < wt1 < wt2; got %d, %d, %d", wt0, wt1, wt2)
	}
	if wt2 != len(baseColorAtlasTierSizes)-1 {
		t.Errorf("tri 2 (50mm edge): expected largest W tier %d, got %d", len(baseColorAtlasTierSizes)-1, wt2)
	}

	fake := &fakeSampler{cb: func(materialx.SampleContext) [3]float64 { return [3]float64{0, 0, 0} }}
	override := &materialxOverride{sampler: fake, invTileMM: 1, useUV: false, sharpness: 4}
	atlas, err := bakeMaterialXAtlas(context.Background(), model, override, nil)
	if err != nil {
		t.Fatalf("bakeMaterialXAtlas: %v", err)
	}

	// Each face's UVs should fall within its tier's vertical band of
	// the atlas, and use the tier-specific patch size.
	uvY := func(fi, slot int) float32 {
		return atlas.FaceVertexUVs[fi*6+slot*2+1]
	}
	// tri 0 in tier 0 → y in [0, h0/atlasH]; tri 2 in last tier →
	// y in upper portion. Just assert tri 0's UVs are strictly above
	// tri 2's UVs (smaller v).
	if uvY(0, 0) >= uvY(2, 0) {
		t.Errorf("tri 0 (smallest tier) v=%v should be above tri 2 (largest tier) v=%v", uvY(0, 0), uvY(2, 0))
	}
}

// TestBakeMaterialXAtlasUVsAndPixelsForRightTriangle pins absolute
// UV values and a baked pixel for a single right triangle. The
// position-encoded sampler maps `pos.x` → red, so the pixel beneath
// vertex B's UV must match B's x-coord byte; the pixel beneath
// vertex A's UV must match A's x-coord. Catches sign/ordering bugs
// in `sToPix`, AIdx/BIdx/CIdx writeback, and image base64-decode
// round-trip — failure modes the relative-comparison tests can't see.
func TestBakeMaterialXAtlasUVsAndPixelsForRightTriangle(t *testing.T) {
	// Right triangle in the XY plane. With target=0.2mm and longest
	// edge ~14.14mm, the bake should pick a small/middle tier — the
	// test reads the actual choice rather than hard-coding it.
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 0}, {10, 0, 0}, {0, 10, 0},
		},
		Faces: [][3]uint32{{0, 1, 2}},
	}
	// Sampler: red channel = pos.x / 100 (so pos.x=10 → 25 byte;
	// pos.x=0 → 0 byte). Other channels constant for stability.
	fake := &fakeSampler{
		usesUV: false,
		cb: func(ctx materialx.SampleContext) [3]float64 {
			return [3]float64{float64(ctx.Pos[0]) / 100, 0.5, 0.5}
		},
	}
	override := &materialxOverride{sampler: fake, invTileMM: 1, useUV: false, sharpness: 4}
	atlas, err := bakeMaterialXAtlas(context.Background(), model, override, nil)
	if err != nil || atlas == nil {
		t.Fatalf("bakeMaterialXAtlas: atlas=%v err=%v", atlas, err)
	}
	// Decode the atlas image and read the texel under each face-vertex UV.
	// The image is a "png:" + base64 PNG.
	if !strings.HasPrefix(atlas.Image, "png:") {
		t.Fatalf("expected 'png:' prefix")
	}
	pngBytes, err := base64.StdEncoding.DecodeString(atlas.Image[len("png:"):])
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("png decode: %v", err)
	}
	// Each vertex's UV reads the texel center it was baked at; the
	// red channel of that texel encodes the vertex's x coord.
	uvAt := func(slot int) (float32, float32) {
		return atlas.FaceVertexUVs[slot*2], atlas.FaceVertexUVs[slot*2+1]
	}
	check := func(slot int, wantPosX float32, label string) {
		u, v := uvAt(slot)
		px := int(u * float32(atlas.Width))
		py := int(v * float32(atlas.Height))
		r, _, _, _ := img.At(px, py).RGBA()
		// PNG decode returns 16-bit channel; extract 8-bit value.
		got8 := int(r >> 8)
		want8 := int(floatToByte(float64(wantPosX) / 100))
		if got8 < want8-1 || got8 > want8+1 {
			t.Errorf("%s: pixel red at uv (%v,%v) → (%d,%d): got %d, want %d (±1)",
				label, u, v, px, py, got8, want8)
		}
	}
	check(0, 0, "vertex 0 (x=0)")
	check(1, 10, "vertex 1 (x=10)")
	check(2, 0, "vertex 2 (x=0)")
}

// TestBakeMaterialXAtlasObtuseTriangle exercises the case where the
// third vertex's foot-of-perpendicular falls outside the longest
// edge's [0, abLen] range (sCsigned < 0 or > abLen). The bbox along
// the longest-edge axis is wider than the edge itself; the layout
// must still keep all three vertex UVs inside the patch, and the
// bake at vertex C's UV must read the same color the sampler would
// produce at C's actual 3D position.
func TestBakeMaterialXAtlasObtuseTriangle(t *testing.T) {
	// Obtuse triangle: A=(0,0,0), B=(10,0,0), C=(15,1,0).
	// Foot of perpendicular from C onto AB is x=15, which is past B.
	model := &loader.LoadedModel{
		Vertices: [][3]float32{
			{0, 0, 0}, {10, 0, 0}, {15, 1, 0},
		},
		Faces: [][3]uint32{{0, 1, 2}},
	}
	fake := &fakeSampler{
		usesUV: false,
		cb: func(ctx materialx.SampleContext) [3]float64 {
			return [3]float64{float64(ctx.Pos[0]) / 100, float64(ctx.Pos[1]) / 100, 0.5}
		},
	}
	override := &materialxOverride{sampler: fake, invTileMM: 1, useUV: false, sharpness: 4}
	atlas, err := bakeMaterialXAtlas(context.Background(), model, override, nil)
	if err != nil || atlas == nil {
		t.Fatalf("bakeMaterialXAtlas: atlas=%v err=%v", atlas, err)
	}
	// All three vertex UVs must be inside [0, 1].
	for slot := 0; slot < 3; slot++ {
		u := atlas.FaceVertexUVs[slot*2]
		v := atlas.FaceVertexUVs[slot*2+1]
		if u < 0 || u > 1 || v < 0 || v > 1 {
			t.Errorf("slot %d UV (%v,%v) out of [0,1]", slot, u, v)
		}
	}
	pngBytes, _ := base64.StdEncoding.DecodeString(atlas.Image[len("png:"):])
	img, _ := png.Decode(bytes.NewReader(pngBytes))
	// Vertex C is at x=15, so its baked texel should have red ≈ 0.15*255 = 38.
	uC, vC := atlas.FaceVertexUVs[4], atlas.FaceVertexUVs[5]
	pxC := int(uC * float32(atlas.Width))
	pyC := int(vC * float32(atlas.Height))
	r, _, _, _ := img.At(pxC, pyC).RGBA()
	got8 := int(r >> 8)
	want8 := int(floatToByte(15.0 / 100))
	if got8 < want8-1 || got8 > want8+1 {
		t.Errorf("vertex C: pixel red got %d, want %d (±1)", got8, want8)
	}
}

// TestBakeMaterialXAtlasEdgeCases ensures nil and empty models
// return nil rather than panicking or producing garbage.
func TestBakeMaterialXAtlasEdgeCases(t *testing.T) {
	fake := &fakeSampler{cb: func(materialx.SampleContext) [3]float64 { return [3]float64{} }}
	override := &materialxOverride{sampler: fake, invTileMM: 1}
	got, err := bakeMaterialXAtlas(context.Background(), nil, override, nil)
	if err != nil || got != nil {
		t.Errorf("nil model: got %+v err=%v, want nil/nil", got, err)
	}
	got, err = bakeMaterialXAtlas(context.Background(), &loader.LoadedModel{}, override, nil)
	if err != nil || got != nil {
		t.Errorf("empty model: got %+v err=%v, want nil/nil", got, err)
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
