package progress

import (
	"log"
	"sync/atomic"
	"time"

	"github.com/schollz/progressbar/v3"
)

// Tracker receives progress updates from pipeline stages.
type Tracker interface {
	// StageStart signals that a named stage has begun. If hasProgress is true,
	// total indicates the expected number of work units and StageProgress
	// calls will follow.
	StageStart(stage string, hasProgress bool, total int)

	// StageProgress reports incremental progress within a stage.
	StageProgress(stage string, current int)

	// StageDone signals that a stage has completed.
	StageDone(stage string)

	// Warn surfaces a non-fatal user-facing notice (e.g. malformed
	// MaterialX file, missing inventory entry). The pipeline continues
	// after the warning is logged. Implementations route this to
	// stderr (CLI), the GUI's toast/notification panel, or — when the
	// kind matches a known input — a persistent banner adjacent to
	// the offending control.
	//
	// kind is a stable identifier like "materialx-base-color" that
	// lets UI consumers route the message structurally (no
	// substring-matching the message body, which is fragile to
	// rewording). Pass "" for a generic status-bar warning with no
	// specific home — string-matching the message is then explicitly
	// not supported, callers that need to be routed should declare a
	// kind constant.
	Warn(kind, message string)
}

// Common Tracker.Warn kinds. Defined here so producers and consumers
// (Go-side pipeline + Wails event listeners + the frontend banner)
// agree on the same vocabulary without literal-string drift.
const (
	// WarnKindGeneric is the empty kind — surfaced as a generic
	// status-bar warning with no specific UI home. Used for things
	// like "missing inventory entry" that don't tie back to a single
	// input control. Prefer this constant over a literal "" at call
	// sites so a grep for "WarnKind" finds every routing decision.
	WarnKindGeneric = ""

	// WarnKindMaterialXBaseColor signals a failure compiling or
	// applying the MaterialX file selected for the base color of
	// untextured faces. UI surfaces this on the persistent banner
	// adjacent to the texture-file picker.
	WarnKindMaterialXBaseColor = "materialx-base-color"
)

// NullTracker is a no-op Tracker for use when progress reporting is not needed.
type NullTracker struct{}

func (NullTracker) StageStart(string, bool, int) {}
func (NullTracker) StageProgress(string, int)    {}
func (NullTracker) StageDone(string)             {}
func (NullTracker) Warn(string, string)          {}

// Stage is a handle returned by BeginStage. Its Done method ends the stage
// and is idempotent — safe to call from defer plus explicitly when you want
// to end the stage early (e.g. before starting another stage in the same
// function). A nil *Stage is also safe to call Done on.
type Stage struct {
	tracker Tracker
	name    string
	done    bool
	// last is the highest progress value forwarded so far; Progress
	// drops anything lower so the bar is strictly non-decreasing even
	// when concurrent reporters deliver ticks out of order (see Span).
	last atomic.Int64
}

// BeginStage emits StageStart and returns a handle whose Done method emits
// StageDone exactly once. The intended pattern is:
//
//	stage := progress.BeginStage(tracker, "Loading", false, 0)
//	defer stage.Done()
//	... work ...
//	stage.Done() // optional explicit end before starting a sibling stage
func BeginStage(t Tracker, name string, hasProgress bool, total int) *Stage {
	t.StageStart(name, hasProgress, total)
	return &Stage{tracker: t, name: name}
}

// CacheAware is an optional Tracker extension. StageCached marks an
// already-started stage as a cache replay (blob decode in progress /
// completed) so a UI can label it distinctly from a recompute.
// Trackers that don't implement it just never see the marker.
type CacheAware interface {
	StageCached(stage string)
}

// BeginStageCached is BeginStage for a stage being replayed from the
// disk cache: spinner-only (no determinate progress), plus the
// CacheAware marker when the tracker supports it.
func BeginStageCached(t Tracker, name string) *Stage {
	s := BeginStage(t, name, false, 0)
	if ca, ok := t.(CacheAware); ok {
		ca.StageCached(name)
	}
	return s
}

// Done ends the stage. Idempotent.
func (s *Stage) Done() {
	if s == nil || s.done {
		return
	}
	s.done = true
	s.tracker.StageDone(s.name)
}

// Progress reports incremental progress within the stage. Ticks that
// don't advance the bar are dropped: concurrent reporters (Span) can
// deliver a stale lower value after a higher one, and forwarding it
// would move the bar backwards — worse in the GUI, whose 100ms event
// throttle could keep the dip on screen.
func (s *Stage) Progress(current int) {
	if s == nil {
		return
	}
	for {
		prev := s.last.Load()
		if int64(current) <= prev {
			return
		}
		if s.last.CompareAndSwap(prev, int64(current)) {
			break
		}
	}
	s.tracker.StageProgress(s.name, current)
}

// ScaleTotal is the stage total used by stages whose true work-unit
// count is not known when the stage starts (Voxelize, Clip: slab and
// clip-job counts only emerge partway in). The stage begins with
// total=ScaleTotal and each sub-phase maps its own (done, total) onto
// a fixed window of the bar via Span, weighted by rough wall-clock
// share — a smooth normalized bar rather than an exact unit count.
const ScaleTotal = 1000

// Span returns a reporter that maps a sub-phase's (done, total) onto
// the [lo, hi] window of the stage bar. A nil *Stage yields a no-op
// reporter. Safe to call from concurrent workers when done values come
// from a shared atomic counter; ticks may land out of order, but
// Progress drops any that don't advance the bar.
func (s *Stage) Span(lo, hi int) func(done, total int) {
	return func(done, total int) {
		if s == nil || total <= 0 {
			return
		}
		s.Progress(lo + (hi-lo)*done/total)
	}
}

// CLITracker wraps schollz/progressbar for terminal output.
type CLITracker struct {
	bars map[string]*cliStage
}

type cliStage struct {
	bar   *progressbar.ProgressBar
	start time.Time
}

// NewCLITracker returns a CLITracker ready for use.
func NewCLITracker() *CLITracker {
	return &CLITracker{
		bars: make(map[string]*cliStage),
	}
}

func (t *CLITracker) StageStart(stage string, hasProgress bool, total int) {
	if hasProgress {
		t.bars[stage] = &cliStage{
			bar:   NewBar(total, "  "+stage),
			start: time.Now(),
		}
	}
}

func (t *CLITracker) StageProgress(stage string, current int) {
	if s, ok := t.bars[stage]; ok {
		s.bar.Set(current)
	}
}

func (t *CLITracker) StageDone(stage string) {
	if s, ok := t.bars[stage]; ok {
		FinishBar(s.bar, stage, "done", time.Since(s.start))
		delete(t.bars, stage)
	}
}

func (t *CLITracker) Warn(kind, message string) {
	if kind != "" {
		log.Printf("Warning [%s]: %s", kind, message)
	} else {
		log.Printf("Warning: %s", message)
	}
}
