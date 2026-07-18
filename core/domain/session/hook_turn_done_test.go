package session

import "testing"

// IsAgentDone must treat the Claude Code Stop hook (HookTurnDone) as an
// authoritative turn-done signal — true even when the transcript-tail
// LastEventType hasn't landed a done signal — while still yielding to the
// open-tool and live-background-process guards, which outlive the turn. See
// issue #1161 (and #445 for the background-process guard).
func TestIsAgentDone_HookTurnDone(t *testing.T) {
	cases := []struct {
		name string
		m    *SessionMetrics
		want bool
	}{
		{
			name: "hook Stop, no transcript done signal → done",
			m:    &SessionMetrics{HookTurnDone: true, LastEventType: "assistant_streaming"},
			want: true,
		},
		{
			name: "hook Stop with open tool call → not done",
			m:    &SessionMetrics{HookTurnDone: true, HasOpenToolCall: true},
			want: false,
		},
		{
			name: "hook Stop with live background process → not done",
			m:    &SessionMetrics{HookTurnDone: true, HasLiveBackgroundProcess: true},
			want: false,
		},
		{
			name: "no hook, no transcript done signal → not done",
			m:    &SessionMetrics{LastEventType: "assistant_streaming"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.IsAgentDone(); got != tc.want {
				t.Errorf("IsAgentDone() = %v, want %v", got, tc.want)
			}
		})
	}
}

// HookTurnDone is a per-pass overlay the detector re-sets fresh each pass, so
// MergeMetrics must not carry it forward from the prior pass — otherwise a
// single Stop hook would strand the session as "done" across the next turn.
func TestMergeMetrics_HookTurnDoneNotCarriedForward(t *testing.T) {
	old := &SessionMetrics{HookTurnDone: true}
	merged := MergeMetrics(&SessionMetrics{LastEventType: "assistant_streaming"}, old)
	if merged.HookTurnDone {
		t.Errorf("HookTurnDone carried forward from prior pass; want reset to false")
	}
}
