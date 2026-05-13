package rectclip

import (
	"math"
	"testing"
)

func TestRectClipSquare(t *testing.T) {
	s := New()
	defer s.Free()
	s.SetPaths([][][2]int64{
		{{0, 0}, {10, 0}, {10, 10}, {0, 10}},
	})
	out := s.Clip(2, 2, 8, 8)
	if len(out) != 1 {
		t.Fatalf("want 1 path, got %d", len(out))
	}
	if len(out[0]) != 4 {
		t.Fatalf("want 4 verts, got %d: %v", len(out[0]), out[0])
	}
	for _, p := range out[0] {
		if p[0] < 2 || p[0] > 8 || p[1] < 2 || p[1] > 8 {
			t.Errorf("vertex %v outside clip rect", p)
		}
	}
}

func BenchmarkRectClipCircle(b *testing.B) {
	const n = 50
	circle := make([][2]int64, n)
	for i := 0; i < n; i++ {
		a := float64(i) / float64(n) * 6.28319
		circle[i] = [2]int64{int64(5000 + 4000*math.Cos(a)), int64(5000 + 4000*math.Sin(a))}
	}
	s := New()
	defer s.Free()
	s.SetPaths([][][2]int64{circle})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Clip(4000, 4000, 4400, 4400)
	}
}
