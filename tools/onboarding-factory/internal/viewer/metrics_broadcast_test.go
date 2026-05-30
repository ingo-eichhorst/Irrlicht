package viewer

import (
	"testing"

	"irrlicht/core/domain/session"
)

// TestSelectAtPlayhead pins the playhead → snapshot selection that animates
// cost/tokens during recording playback: return the last snapshot at or before
// the playhead, and nil while the playhead precedes the first snapshot (so the
// dashboard shows "no metrics yet" instead of a final-ish total at t=0).
func TestSelectAtPlayhead(t *testing.T) {
	tl := []timelinePoint{
		{offsetMs: 0, metrics: &session.SessionMetrics{EstimatedCostUSD: 0.01, TotalTokens: 100}},
		{offsetMs: 1000, metrics: &session.SessionMetrics{EstimatedCostUSD: 0.05, TotalTokens: 500}},
		{offsetMs: 2000, metrics: &session.SessionMetrics{EstimatedCostUSD: 0.12, TotalTokens: 1200}},
	}

	cases := []struct {
		name     string
		pos      int64
		wantNil  bool
		wantCost float64
	}{
		{"before first", -1, true, 0},
		{"at first", 0, false, 0.01},
		{"between first and second", 500, false, 0.01},
		{"at second", 1000, false, 0.05},
		{"between second and third", 1500, false, 0.05},
		{"past last clamps to last", 5000, false, 0.12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := selectAtPlayhead(tl, tc.pos)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("pos=%d: want nil, got %+v", tc.pos, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("pos=%d: want cost %v, got nil", tc.pos, tc.wantCost)
			}
			if got.EstimatedCostUSD != tc.wantCost {
				t.Errorf("pos=%d: cost=%v, want %v", tc.pos, got.EstimatedCostUSD, tc.wantCost)
			}
		})
	}
}

// TestSelectAtPlayhead_returnsCopy verifies each selection is a fresh pointer,
// so a downstream broadcast can't mutate a shared timeline snapshot.
func TestSelectAtPlayhead_returnsCopy(t *testing.T) {
	orig := &session.SessionMetrics{EstimatedCostUSD: 0.07}
	tl := []timelinePoint{{offsetMs: 0, metrics: orig}}
	got := selectAtPlayhead(tl, 100)
	if got == nil {
		t.Fatal("expected a snapshot")
	}
	if got == orig {
		t.Fatal("selectAtPlayhead must return a copy, not the shared timeline pointer")
	}
	got.EstimatedCostUSD = 99
	if orig.EstimatedCostUSD != 0.07 {
		t.Errorf("mutating the returned copy leaked into the timeline: %v", orig.EstimatedCostUSD)
	}
}
