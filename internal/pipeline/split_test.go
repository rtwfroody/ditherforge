package pipeline

import (
	"testing"
)

// TestSplitDisabled_NoCacheKeyChange — when Split.Enabled is false,
// changing other Split fields should not affect any stage's cache
// key. This preserves cache-hit equivalence with the pre-Split path
// — anyone toggling Split sliders while Split is off must not
// invalidate downstream caches.
func TestSplitDisabled_NoCacheKeyChange(t *testing.T) {
	c := NewStageCache()
	path := makeFakeInput(t)
	base := Options{Input: path, Scale: 1, NozzleDiameter: 0.4, LayerHeight: 0.2, Dither: "none"}
	// Split off. Toggling other fields should be invisible.
	tweaked := base
	tweaked.Split.Axis = 1
	tweaked.Split.Offset = 5.0
	tweaked.Split.ConnectorStyle = "pegs"
	tweaked.Split.ConnectorCount = 2
	tweaked.Split.ConnectorDiamMM = 5
	tweaked.Split.ConnectorDepthMM = 6
	tweaked.Split.ClearanceMM = 0.15
	tweaked.Split.GapMM = 5
	for s := StageLoad; s < numStages; s++ {
		if c.stageKey(s, base) != c.stageKey(s, tweaked) {
			t.Errorf("stage %d key changed when Split is off but other Split fields changed", s)
		}
	}
}

// TestSplitEnabled_CacheKeyCascade — flipping Split.Enabled changes
// StageSplit's key and every downstream stage's key, but not
// StageLoad or StageParse (Split is downstream of Load).
func TestSplitEnabled_CacheKeyCascade(t *testing.T) {
	c := NewStageCache()
	path := makeFakeInput(t)
	off := Options{Input: path, Scale: 1, NozzleDiameter: 0.4, LayerHeight: 0.2, Dither: "none"}
	on := off
	on.Split.Enabled = true
	on.Split.Axis = 2
	on.Split.Offset = 5
	on.Split.ConnectorStyle = "dowels"
	on.Split.ConnectorDiamMM = 4
	on.Split.ConnectorDepthMM = 5
	on.Split.ClearanceMM = 0.15
	on.Split.GapMM = 5

	// Parse and Load should NOT change.
	if c.stageKey(StageParse, off) != c.stageKey(StageParse, on) {
		t.Error("StageParse key changed when Split toggled — cascade leaked upward")
	}
	if c.stageKey(StageLoad, off) != c.stageKey(StageLoad, on) {
		t.Error("StageLoad key changed when Split toggled — cascade leaked upward")
	}
	// Split through Merge SHOULD change.
	for s := StageSplit; s < numStages; s++ {
		if c.stageKey(s, off) == c.stageKey(s, on) {
			t.Errorf("stage %d key did not change when Split toggled (cascade broken)", s)
		}
	}
}

// TestSplitEnabled_FieldCascade — when Split is enabled, changing
// each Split field individually changes downstream cache keys. Maps
// to "any settings change rebuilds the appropriate caches."
func TestSplitEnabled_FieldCascade(t *testing.T) {
	c := NewStageCache()
	path := makeFakeInput(t)
	base := Options{Input: path, Scale: 1, NozzleDiameter: 0.4, LayerHeight: 0.2, Dither: "none"}
	base.Split.Enabled = true
	base.Split.Axis = 2
	base.Split.Offset = 5
	base.Split.ConnectorStyle = "dowels"
	base.Split.GapMM = 5
	cases := []struct {
		name string
		mut  func(o *Options)
	}{
		{"Axis", func(o *Options) { o.Split.Axis = 0 }},
		{"Offset", func(o *Options) { o.Split.Offset = 6 }},
		{"ConnectorStyle", func(o *Options) { o.Split.ConnectorStyle = "pegs" }},
		{"ConnectorCount", func(o *Options) { o.Split.ConnectorCount = 2 }},
		{"ConnectorDiamMM", func(o *Options) { o.Split.ConnectorDiamMM = 5 }},
		{"ConnectorDepthMM", func(o *Options) { o.Split.ConnectorDepthMM = 6 }},
		{"ClearanceMM", func(o *Options) { o.Split.ClearanceMM = 0.2 }},
		{"GapMM", func(o *Options) { o.Split.GapMM = 8 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			alt := base
			tc.mut(&alt)
			if c.stageKey(StageSplit, base) == c.stageKey(StageSplit, alt) {
				t.Errorf("StageSplit key did not change when %s changed", tc.name)
			}
		})
	}
}

// TestStageSplitDescription — the eviction-log description includes
// the connector style and offset so operators can identify entries.
func TestStageSplitDescription(t *testing.T) {
	off := Options{Input: "/tmp/foo.glb"}
	if got := stageDescription(StageSplit, off); got != "Split: foo.glb (off)" {
		t.Errorf("disabled description = %q, want 'Split: foo.glb (off)'", got)
	}
	on := off
	on.Split.Enabled = true
	on.Split.Axis = 2
	on.Split.Offset = 5
	on.Split.ConnectorStyle = "pegs"
	on.Split.ConnectorCount = 2
	got := stageDescription(StageSplit, on)
	want := "Split: foo.glb (Z@5.0mm, pegs ×2)"
	if got != want {
		t.Errorf("enabled description = %q, want %q", got, want)
	}
}
