package session

import "testing"

// IsAgentDone must treat a live background process like an open tool call:
// the turn's end_turn fired, but the session is not idle. See issue #445.
func TestIsAgentDone_HeldByLiveBackgroundProcess(t *testing.T) {
	cases := []struct {
		name string
		m    *SessionMetrics
		want bool
	}{
		{
			name: "turn_done with live background process → not done",
			m:    &SessionMetrics{LastEventType: "turn_done", HasLiveBackgroundProcess: true},
			want: false,
		},
		{
			name: "turn_done with no live background process → done",
			m:    &SessionMetrics{LastEventType: "turn_done", HasLiveBackgroundProcess: false},
			want: true,
		},
		{
			name: "background count without confirmed liveness does not gate",
			m:    &SessionMetrics{LastEventType: "turn_done", BackgroundProcessCount: 2, HasLiveBackgroundProcess: false},
			want: true,
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

// MergeMetrics carries the background-process fields from the freshly computed
// metrics (they are recomputed from the transcript every pass). See issue #445.
func TestMergeMetrics_BackgroundProcessFields(t *testing.T) {
	newM := &SessionMetrics{
		BackgroundProcessCount:   1,
		BackgroundProcessOutputs: []string{"/tmp/x/tasks/a.output"},
	}
	old := &SessionMetrics{BackgroundProcessCount: 0}
	merged := MergeMetrics(newM, old)
	if merged.BackgroundProcessCount != 1 {
		t.Errorf("BackgroundProcessCount = %d, want 1", merged.BackgroundProcessCount)
	}
	if len(merged.BackgroundProcessOutputs) != 1 {
		t.Errorf("BackgroundProcessOutputs = %v, want one path", merged.BackgroundProcessOutputs)
	}
}

// MergeMetrics carries the PID-keyed background-process field too (Gemini CLI
// reports a PID rather than an output file). See issue #661.
func TestMergeMetrics_BackgroundProcessPIDs(t *testing.T) {
	newM := &SessionMetrics{
		BackgroundProcessCount: 1,
		BackgroundProcessPIDs:  []string{"33701"},
	}
	old := &SessionMetrics{BackgroundProcessCount: 0}
	merged := MergeMetrics(newM, old)
	if len(merged.BackgroundProcessPIDs) != 1 || merged.BackgroundProcessPIDs[0] != "33701" {
		t.Errorf("BackgroundProcessPIDs = %v, want [33701]", merged.BackgroundProcessPIDs)
	}
}
