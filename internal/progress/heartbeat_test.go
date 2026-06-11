package progress

import (
	"sync"
	"testing"
	"time"
)

// beatRecorder is a Tracker+Heartbeater that records heartbeats.
type beatRecorder struct {
	NullTracker
	mu    sync.Mutex
	beats []string
}

func (b *beatRecorder) StageHeartbeat(stage string, _ time.Duration) {
	b.mu.Lock()
	b.beats = append(b.beats, stage)
	b.mu.Unlock()
}

// TestMonitorStopIdempotent: Stop must be safe to call more than once
// (a future caller that defers Stop and also calls it on an early-exit
// path must not panic on a double channel close).
func TestMonitorStopIdempotent(t *testing.T) {
	m := NewMonitor(&beatRecorder{})
	m.StageStart("x", false, 0)
	m.StageDone("x")
	m.Stop()
	m.Stop() // must not panic
}

// TestMonitorEmitsHeartbeatForSilentStage: a running stage that emits
// no progress gets a StageHeartbeat within a couple of intervals.
func TestMonitorEmitsHeartbeatForSilentStage(t *testing.T) {
	rec := &beatRecorder{}
	m := NewMonitor(rec)
	defer m.Stop()
	m.StageStart("quiet", false, 0)
	deadline := time.After(4 * HeartbeatInterval)
	for {
		rec.mu.Lock()
		n := len(rec.beats)
		rec.mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no heartbeat for a silent running stage within 4 intervals")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
