package progress

import (
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
}

// NullTracker is a no-op Tracker for use when progress reporting is not needed.
type NullTracker struct{}

func (NullTracker) StageStart(string, bool, int) {}
func (NullTracker) StageProgress(string, int)    {}
func (NullTracker) StageDone(string)             {}

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
