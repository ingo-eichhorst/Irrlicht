package metrics

import (
	"os"
	"path/filepath"
	"testing"
)

// scenarioTranscript resolves a committed claudecode scenario recording. It
// fails rather than skips: these recordings are deletion-guarded (#268), so a
// missing one is a broken checkout, and a skip here would report the same green
// as a passing plumbing check.
func scenarioTranscript(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "replaydata", "agents",
		"claudecode", "scenarios", filepath.FromSlash(rel))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("committed fixture missing at %s: %v", path, err)
	}
	return path
}

const (
	terminalErrorRecording = "2-14_turn-aborted-by-error/recordings/" +
		"2026-05-22-15-49-22_irrlichd-unknown/transcript.jsonl"
	retryingErrorRecording = "2-9_token-quota-exhausted/recordings/" +
		"2026-05-18-23-08-01_irrlichd-0.4.5+6898561/transcript.jsonl"
)

// TestIngestTurnBoundary_ReachesTheSessionsTailer is the plumbing test for
// #1799's hook path. It runs end-to-end through the port implementation —
// ComputeMetrics to create the tailer, IngestTurnBoundary to deliver the Stop
// hook's boundary, ComputeMetrics again to read the verdict — because the
// defect this guards against is a call that silently reaches nothing.
//
// The phase rule itself is `core/pkg/tailer`'s; what is asserted here is that
// the adapter finds the right tailer and that the two outcomes actually differ,
// so a no-op implementation (or a lookup that misses) cannot pass.
func TestIngestTurnBoundary_ReachesTheSessionsTailer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture string
		wantNil bool
		why     string
	}{
		{
			name:    "terminal error stands",
			fixture: terminalErrorRecording,
			wantNil: false,
			why: "the Stop hook fires for the very turn that failed — claudecode " +
				"writes its API-error message and then a turn_duration epilogue",
		},
		{
			name:    "retrying error is retired",
			fixture: retryingErrorRecording,
			wantNil: true,
			why:     "the agent said another attempt was coming and the turn then completed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := scenarioTranscript(t, tc.fixture)
			a := newClaudeCodeAdapter(t)

			m, err := a.ComputeMetrics(path, "claude-code")
			if err != nil || m == nil {
				t.Fatalf("ComputeMetrics: m=%v err=%v", m, err)
			}
			if m.SessionError == nil {
				t.Fatal("precondition failed: this recording produced no session error, " +
					"so the call under test has nothing to act on")
			}

			a.IngestTurnBoundary(path)

			m, err = a.ComputeMetrics(path, "claude-code")
			if err != nil || m == nil {
				t.Fatalf("ComputeMetrics after the boundary: m=%v err=%v", m, err)
			}
			if got := m.SessionError == nil; got != tc.wantNil {
				t.Errorf("after IngestTurnBoundary, SessionError == nil is %v, want %v (%+v) — %s",
					got, tc.wantNil, m.SessionError, tc.why)
			}
		})
	}
}

// TestIngestTurnBoundary_UnknownPathIsASilentNoOp matches IngestRateLimit's
// contract: a hook can arrive before the session has ever been tailed, and that
// must not panic or create state.
func TestIngestTurnBoundary_UnknownPathIsASilentNoOp(t *testing.T) {
	a := newClaudeCodeAdapter(t)
	a.IngestTurnBoundary("/nonexistent.jsonl")
	a.IngestTurnBoundary("")
}
