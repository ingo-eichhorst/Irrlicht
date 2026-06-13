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

// AppliedTaskDeltas is per-pass transient: the merge takes newM's value with no
// old-value fallback, so a pass that surfaced no deltas does not resurrect a
// prior pass's — that is what keeps task_delta lifecycle events from being
// re-recorded every refresh (#662).
func TestMergeMetrics_AppliedTaskDeltasNoFallback(t *testing.T) {
	newM := &SessionMetrics{AppliedTaskDeltas: []AppliedTaskDelta{{Op: "create", ID: "1", Subject: "build"}}}
	old := &SessionMetrics{AppliedTaskDeltas: []AppliedTaskDelta{{Op: "update", ID: "9"}}}
	if merged := MergeMetrics(newM, old); len(merged.AppliedTaskDeltas) != 1 || merged.AppliedTaskDeltas[0].ID != "1" {
		t.Fatalf("AppliedTaskDeltas = %+v, want newM's single create", merged.AppliedTaskDeltas)
	}
	// A nil-delta pass must NOT carry over the old deltas.
	if merged := MergeMetrics(&SessionMetrics{}, old); len(merged.AppliedTaskDeltas) != 0 {
		t.Errorf("AppliedTaskDeltas = %+v, want empty (no old-value fallback)", merged.AppliedTaskDeltas)
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
