package session

import "testing"

// TestMergeMetrics_CarriesTranscriptPermissionPending pins a defect found by
// recording GitHub Copilot's tool-gate-permission-prompt cell (#1256).
//
// newMergedMetrics is an explicit field allowlist, so a newly-added
// tailer-derived field is silently DROPPED on the live path unless it is
// listed. TranscriptPermissionPending was not, so the transcript-tier
// permission rule could never fire live: the detector classifies against the
// post-merge metrics, which always read false.
//
// The asymmetry is what made this so quiet — replay carries the field through
// its own converter, so the parser unit tests, the classifier tests and
// replay-fixtures.sh were ALL green while the live behaviour was broken. Only
// driving a real blocked session exposed it.
func TestMergeMetrics_CarriesTranscriptPermissionPending(t *testing.T) {
	fresh := &SessionMetrics{TranscriptPermissionPending: true}
	old := &SessionMetrics{}

	merged := MergeMetrics(fresh, old)

	if !merged.TranscriptPermissionPending {
		t.Error("TranscriptPermissionPending = false after merge, want true — the field is " +
			"dropped by newMergedMetrics' allowlist, so the transcript-tier permission " +
			"rule can never fire on the live path")
	}
}

// TestMergeMetrics_ClearsTranscriptPermissionWhenResolved guards the other
// direction: once the prompt is answered the fresh pass reports false, and the
// merge must NOT carry the stale true forward or the session would stick in
// waiting for the rest of its life.
func TestMergeMetrics_ClearsTranscriptPermissionWhenResolved(t *testing.T) {
	fresh := &SessionMetrics{TranscriptPermissionPending: false}
	old := &SessionMetrics{TranscriptPermissionPending: true}

	merged := MergeMetrics(fresh, old)

	if merged.TranscriptPermissionPending {
		t.Error("TranscriptPermissionPending = true after the prompt was answered — a stale " +
			"true would pin the session in waiting permanently")
	}
}
