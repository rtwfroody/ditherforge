package fwnrepair

import (
	"context"
	"math"
	"testing"

	"github.com/rtwfroody/ditherforge/internal/loader"
)

// cubeMesh returns an axis-aligned cube of the given edge length,
// centered at the origin, with outward-facing CCW triangles.
func cubeMesh(edge float32) ([][3]float32, [][3]uint32) {
	h := edge / 2
	v := [][3]float32{
		{-h, -h, -h}, {h, -h, -h}, {h, h, -h}, {-h, h, -h},
		{-h, -h, h}, {h, -h, h}, {h, h, h}, {-h, h, h},
	}
	// Six faces, each two CCW-outward triangles.
	f := [][3]uint32{
		{0, 3, 2}, {0, 2, 1}, // -Z
		{4, 5, 6}, {4, 6, 7}, // +Z
		{0, 1, 5}, {0, 5, 4}, // -Y
		{2, 3, 7}, {2, 7, 6}, // +Y
		{1, 2, 6}, {1, 6, 5}, // +X
		{0, 4, 7}, {0, 7, 3}, // -X
	}
	return v, f
}

func cubeTris(edge float32) []tri {
	v, f := cubeMesh(edge)
	m := &loader.LoadedModel{Vertices: v, Faces: f}
	return buildTris(m)
}

// TestWindingInsideOutside checks that both evaluators report w≈1
// inside a closed oriented cube and w≈0 outside, and agree closely.
func TestWindingInsideOutside(t *testing.T) {
	tris := cubeTris(2) // cube spans [-1,1]³
	tree := buildBVH(tris)

	inside := []vec3{{0, 0, 0}, {0.5, -0.3, 0.2}, {-0.7, 0.6, -0.4}}
	outside := []vec3{{3, 0, 0}, {0, -5, 2}, {2, 2, 2}}

	for _, q := range inside {
		we := windingExact(tris, q)
		wf := tree.winding(q)
		if math.Abs(we-1) > 1e-3 {
			t.Errorf("exact w%v = %g, want ≈1", q, we)
		}
		if math.Abs(wf-1) > 1e-3 {
			t.Errorf("fast w%v = %g, want ≈1", q, wf)
		}
		if math.Abs(we-wf) > 1e-3 {
			t.Errorf("exact/fast disagree at %v: %g vs %g", q, we, wf)
		}
	}
	for _, q := range outside {
		we := windingExact(tris, q)
		wf := tree.winding(q)
		if math.Abs(we) > 1e-3 {
			t.Errorf("exact w%v = %g, want ≈0", q, we)
		}
		if math.Abs(wf) > 1e-3 {
			t.Errorf("fast w%v = %g, want ≈0", q, wf)
		}
		if math.Abs(we-wf) > 1e-3 {
			t.Errorf("exact/fast disagree at %v: %g vs %g", q, we, wf)
		}
	}
}

// meshStats reports boundary edges (edges used by !=2 faces),
// non-manifold edges (used by >2 faces), and the signed volume.
func meshStats(t *testing.T, verts [][3]float32, faces [][3]uint32) (boundary, nonManifold int, volume float64) {
	t.Helper()
	type edge struct{ a, b uint32 }
	count := map[edge]int{}
	add := func(x, y uint32) {
		if x > y {
			x, y = y, x
		}
		count[edge{x, y}]++
	}
	for _, f := range faces {
		add(f[0], f[1])
		add(f[1], f[2])
		add(f[2], f[0])
	}
	for _, c := range count {
		switch {
		case c != 2:
			boundary++
		}
		if c > 2 {
			nonManifold++
		}
	}
	for _, f := range faces {
		a := verts[f[0]]
		b := verts[f[1]]
		c := verts[f[2]]
		av := vec3{float64(a[0]), float64(a[1]), float64(a[2])}
		bv := vec3{float64(b[0]), float64(b[1]), float64(b[2])}
		cv := vec3{float64(c[0]), float64(c[1]), float64(c[2])}
		volume += dot(av, cross(bv, cv))
	}
	volume /= 6
	return
}

func TestRepairClosedCube(t *testing.T) {
	v, f := cubeMesh(10)
	out, eff, err := Repair(context.Background(), &loader.LoadedModel{Vertices: v, Faces: f}, 0.5)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if eff != 0.5 {
		t.Errorf("effective pitch = %g, want 0.5 (no capping expected)", eff)
	}
	boundary, nonManifold, vol := meshStats(t, out.Vertices, out.Faces)
	if boundary != 0 {
		t.Errorf("output has %d boundary edges, want 0 (watertight)", boundary)
	}
	if nonManifold != 0 {
		t.Errorf("output has %d non-manifold edges, want 0 (2-manifold)", nonManifold)
	}
	if vol <= 0 {
		t.Errorf("signed volume = %g, want positive (outward orientation)", vol)
	}
	// True cube volume is 1000; a 0.5 iso of a smoothed field is close.
	if rel := math.Abs(vol-1000) / 1000; rel > 0.10 {
		t.Errorf("volume = %g, want within 10%% of 1000 (rel err %.3f)", vol, rel)
	}
}

func TestRepairOpenBox(t *testing.T) {
	v, f := cubeMesh(10)
	// Drop the +Z face (its two triangles are indices 2 and 3).
	open := append([][3]uint32{}, f[:2]...)
	open = append(open, f[4:]...)

	out, _, err := Repair(context.Background(), &loader.LoadedModel{Vertices: v, Faces: open}, 0.5)
	if err != nil {
		t.Fatalf("Repair open box: %v", err)
	}
	boundary, nonManifold, vol := meshStats(t, out.Vertices, out.Faces)
	if boundary != 0 {
		t.Errorf("open-box output has %d boundary edges, want 0 (watertight)", boundary)
	}
	if nonManifold != 0 {
		t.Errorf("open-box output has %d non-manifold edges, want 0", nonManifold)
	}
	if vol <= 0 {
		t.Errorf("open-box signed volume = %g, want positive", vol)
	}
	// The winding field still closes the box; volume near the full cube.
	if rel := math.Abs(vol-1000) / 1000; rel > 0.15 {
		t.Errorf("open-box volume = %g, want within 15%% of 1000 (rel err %.3f)", vol, rel)
	}
}

func TestRepairInvertedCube(t *testing.T) {
	v, f := cubeMesh(10)
	// Flip every triangle's winding: w becomes ≈ −1 inside, |w| handles it.
	inv := make([][3]uint32, len(f))
	for i, tri := range f {
		inv[i] = [3]uint32{tri[0], tri[2], tri[1]}
	}
	out, _, err := Repair(context.Background(), &loader.LoadedModel{Vertices: v, Faces: inv}, 0.5)
	if err != nil {
		t.Fatalf("Repair inverted cube: %v", err)
	}
	boundary, nonManifold, vol := meshStats(t, out.Vertices, out.Faces)
	if boundary != 0 {
		t.Errorf("inverted-cube output has %d boundary edges, want 0", boundary)
	}
	if nonManifold != 0 {
		t.Errorf("inverted-cube output has %d non-manifold edges, want 0", nonManifold)
	}
	if vol <= 0 {
		t.Errorf("inverted-cube signed volume = %g, want positive", vol)
	}
	if rel := math.Abs(vol-1000) / 1000; rel > 0.10 {
		t.Errorf("inverted-cube volume = %g, want within 10%% of 1000", vol)
	}
}

func TestRepairPitchCapRaisesPitch(t *testing.T) {
	// A large cube at a fine pitch would blow past maxDim; the effective
	// pitch must come back coarser and the grid must stay within bounds.
	v, f := cubeMesh(1000)
	out, eff, err := Repair(context.Background(), &loader.LoadedModel{Vertices: v, Faces: f}, 0.5)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if eff <= 0.5 {
		t.Errorf("effective pitch = %g, want > 0.5 (should have been raised)", eff)
	}
	if boundary, _, _ := meshStats(t, out.Vertices, out.Faces); boundary != 0 {
		t.Errorf("capped-grid output has %d boundary edges, want 0", boundary)
	}
}

func TestRepairCancellation(t *testing.T) {
	v, f := cubeMesh(10)
	model := &loader.LoadedModel{Vertices: v, Faces: f}

	// Cancelled before the call: must return ctx.Err() promptly.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Repair(ctx, model, 0.5); err != context.Canceled {
		t.Errorf("pre-cancelled Repair returned %v, want context.Canceled", err)
	}

	// Cancelled mid-call: use a fine pitch so several slices run, and
	// cancel from a watcher goroutine. Must still surface ctx.Err().
	ctx2, cancel2 := context.WithCancel(context.Background())
	go cancel2()
	big := &loader.LoadedModel{Vertices: mustCube(100), Faces: cubeFaces()}
	_, _, err := Repair(ctx2, big, 0.4)
	// Either it finished before the cancel landed (nil) or was cancelled.
	if err != nil && err != context.Canceled {
		t.Errorf("mid-cancel Repair returned %v, want nil or context.Canceled", err)
	}
}

func mustCube(edge float32) [][3]float32 { v, _ := cubeMesh(edge); return v }
func cubeFaces() [][3]uint32             { _, f := cubeMesh(1); return f }
