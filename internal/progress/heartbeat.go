package progress

import (
	"sync"
	"time"
)

// Heartbeater is an optional Tracker extension. Trackers that
// implement it receive periodic liveness ticks for stages that are
// running but have not reported progress recently, so a UI can
// distinguish "still computing" from "hung" — the no-silent-dead-air
// guarantee. Trackers that don't implement it (CLI, tests) simply
// never see heartbeats; the Monitor checks at runtime.
type Heartbeater interface {
	// StageHeartbeat fires for a running stage that has emitted no
	// StageStart/StageProgress for at least the monitor interval.
	// elapsed is the time since the stage started.
	StageHeartbeat(stage string, elapsed time.Duration)
}

// HeartbeatInterval is the maximum silence the Monitor allows before
// emitting a StageHeartbeat for a running stage. Half the GUI's 1s
// "stalled" threshold so a single delayed tick never trips it.
const HeartbeatInterval = 500 * time.Millisecond

// Monitor wraps a Tracker and guarantees liveness reporting: any
// stage that is running but silent for HeartbeatInterval gets a
// StageHeartbeat on the wrapped tracker (when it implements
// Heartbeater). All Tracker calls are forwarded unchanged.
//
// Usage:
//
//	mon := progress.NewMonitor(tracker)
//	defer mon.Stop()
//	... use mon as the run's Tracker ...
//
// The watchdog goroutine starts lazily on the first StageStart and
// exits when Stop is called (or idles when no stage is running).
type Monitor struct {
	inner Tracker
	hb    Heartbeater // inner as Heartbeater, nil if not implemented

	mu       sync.Mutex
	active   map[string]*stageActivity
	stopCh   chan struct{}
	stopOnce sync.Once
	runner   bool // watchdog goroutine live
}

type stageActivity struct {
	started  time.Time
	lastSeen time.Time
}

// NewMonitor wraps tracker with a heartbeat watchdog. If tracker does
// not implement Heartbeater the wrapper is still safe — it forwards
// everything and the watchdog never starts.
func NewMonitor(tracker Tracker) *Monitor {
	m := &Monitor{
		inner:  tracker,
		active: make(map[string]*stageActivity),
		stopCh: make(chan struct{}),
	}
	m.hb, _ = tracker.(Heartbeater)
	return m
}

// Stop terminates the watchdog. Idempotent — Monitor is exported, so
// a future caller that both defers Stop and calls it on an early-exit
// path must not panic on the second close.
func (m *Monitor) Stop() {
	m.stopOnce.Do(func() { close(m.stopCh) })
}

func (m *Monitor) StageStart(stage string, hasProgress bool, total int) {
	now := time.Now()
	m.mu.Lock()
	m.active[stage] = &stageActivity{started: now, lastSeen: now}
	if m.hb != nil && !m.runner {
		m.runner = true
		go m.watch()
	}
	m.mu.Unlock()
	m.inner.StageStart(stage, hasProgress, total)
}

func (m *Monitor) StageProgress(stage string, current int) {
	m.mu.Lock()
	if a, ok := m.active[stage]; ok {
		a.lastSeen = time.Now()
	}
	m.mu.Unlock()
	m.inner.StageProgress(stage, current)
}

func (m *Monitor) StageDone(stage string) {
	m.mu.Lock()
	delete(m.active, stage)
	m.mu.Unlock()
	m.inner.StageDone(stage)
}

func (m *Monitor) Warn(kind, message string) {
	m.inner.Warn(kind, message)
}

// watch scans active stages at half the heartbeat interval and emits
// a heartbeat for any that have been silent for a full interval.
// Heartbeats refresh lastSeen so a stage beats at most once per
// interval.
func (m *Monitor) watch() {
	ticker := time.NewTicker(HeartbeatInterval / 2)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
		}
		now := time.Now()
		// Collect beats under the lock, emit outside it: the inner
		// tracker may do I/O (Wails event emit) and must not hold up
		// concurrent StageProgress calls from pipeline workers.
		type beat struct {
			stage   string
			elapsed time.Duration
		}
		var beats []beat
		m.mu.Lock()
		for name, a := range m.active {
			if now.Sub(a.lastSeen) >= HeartbeatInterval {
				a.lastSeen = now
				beats = append(beats, beat{name, now.Sub(a.started)})
			}
		}
		m.mu.Unlock()
		for _, b := range beats {
			m.hb.StageHeartbeat(b.stage, b.elapsed)
		}
	}
}

// Compile-time check that Monitor implements Tracker.
var _ Tracker = (*Monitor)(nil)
