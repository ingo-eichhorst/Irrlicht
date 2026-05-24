package replayengine_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/application/replayengine"
	"irrlicht/core/domain/session"
)

// repoRoot resolves the repository root from this test file's location so the
// test reads the committed fixtures regardless of the working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// core/application/replayengine/engine_test.go → repo root is three up.
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

// TestReplayTranscript_producesWaitingFromQuestion is the regression guard
// for issue #461 finding #1: a transcript whose turn ends on a question must
// route through `waiting`. This is exactly the semantics the agent-onboarding
// viewer's old fabricated ready↔working arc could not express; now that the
// viewer drives this engine, the engine owning the behaviour keeps them in
// lockstep.
func TestReplayTranscript_producesWaitingFromQuestion(t *testing.T) {
	src := filepath.Join(repoRoot(t),
		"replaydata", "agents", "claudecode", "scenarios", "user-blocking-question", "transcript.jsonl")

	res, err := replayengine.ReplayTranscript(src, replayengine.Options{
		Adapter:                    claudecode.AdapterName,
		Parser:                     &claudecode.Parser{},
		DisableModelConfigFallback: true,
	})
	if err != nil {
		t.Fatalf("ReplayTranscript: %v", err)
	}
	if res == nil || len(res.Transitions) == 0 {
		t.Fatal("expected transitions, got none")
	}

	// First transition is always the synthetic initial ready state.
	if got := res.Transitions[0]; got.Cause != replayengine.CauseInit || got.NewState != session.StateReady {
		t.Errorf("first transition = %+v; want init→ready", got)
	}

	var sawWaiting, sawWorking bool
	var prevTime = res.Transitions[0].VirtualTime
	for i, tr := range res.Transitions {
		switch tr.NewState {
		case session.StateWaiting:
			sawWaiting = true
		case session.StateWorking:
			sawWorking = true
		}
		// Monotonic non-decreasing virtual time, so the viewer's playback
		// scheduler never sees a negative inter-event delta.
		if tr.VirtualTime.Before(prevTime) {
			t.Errorf("transition %d goes back in time: %v < %v", i, tr.VirtualTime, prevTime)
		}
		prevTime = tr.VirtualTime
	}
	if !sawWorking {
		t.Error("expected a working transition")
	}
	if !sawWaiting {
		t.Error("expected a waiting transition (turn ended on a question) — the classifier semantics the naive arc lacked")
	}
}

// TestReplayTranscript_emptyTranscript returns (nil, nil) for an empty file
// so callers can treat "no usable transcript" uniformly.
func TestReplayTranscript_emptyTranscript(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := replayengine.ReplayTranscript(empty, replayengine.Options{
		Adapter: claudecode.AdapterName,
		Parser:  &claudecode.Parser{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != nil {
		t.Fatalf("expected nil result for empty transcript, got %+v", res)
	}
}
