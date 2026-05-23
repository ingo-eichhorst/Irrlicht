package services_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// readyTransitions counts recorded working/waiting→ready transitions for sid.
// Asserting on the thread-safe recorder (rather than reading the repo's shared
// SessionState pointer) avoids racing with the off-loop liveness probe, which
// nudges processActivity to mutate state concurrently.
func readyTransitions(rec *mockRecorder, sid string) int {
	n := 0
	for _, ev := range rec.snapshot() {
		if ev.Kind == lifecycle.KindStateTransition && ev.SessionID == sid && ev.NewState == session.StateReady {
			n++
		}
	}
	return n
}

// waitForReadyTransition polls the recorder until a →ready transition for sid
// is observed, or fails at the deadline.
func waitForReadyTransition(t *testing.T, rec *mockRecorder, sid string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if readyTransitions(rec, sid) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %s: no →ready transition recorded within deadline", sid)
}

// ClassifyState must hold a session `working` when the liveness probe has
// confirmed a background process is still alive, even though the turn ended.
// See issue #445.
func TestClassifyState_HeldByLiveBackgroundProcess(t *testing.T) {
	live := &session.SessionMetrics{LastEventType: "turn_done", HasLiveBackgroundProcess: true}
	if got, _ := services.ClassifyState(session.StateWorking, live); got != session.StateWorking {
		t.Errorf("with live background process: got %q, want working", got)
	}
	dead := &session.SessionMetrics{LastEventType: "turn_done", HasLiveBackgroundProcess: false}
	if got, _ := services.ClassifyState(session.StateWorking, dead); got != session.StateReady {
		t.Errorf("with no live background process: got %q, want ready", got)
	}
}

// End-to-end through the detector: a working session whose transcript shows an
// open background process stays working while the probe reports it alive, and
// flips to ready once the probe reports it gone — the path the 5s
// refreshStaleSessions ticker exercises in production. See issue #445.
func TestSessionDetector_BackgroundProcess_HoldsWorkingThenReady(t *testing.T) {
	const sid = "bg1"
	const path = "/home/.claude/projects/-Users-test/bg1.jsonl"

	// Metrics always show a finished turn with one open background process.
	metrics := &funcMetrics{fn: func(_, _ string) (*session.SessionMetrics, error) {
		return &session.SessionMetrics{
			LastEventType:            "turn_done",
			BackgroundProcessCount:   1,
			BackgroundProcessOutputs: []string{"/tmp/x/tasks/bc1h56v8v.output"},
		}, nil
	}}

	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	repo.states[sid] = &session.SessionState{
		SessionID:      sid,
		State:          session.StateWorking,
		TranscriptPath: path,
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
	}

	det := newDetectorWithMetrics(tw, pw, repo, metrics)
	rec := &mockRecorder{}
	det.SetRecorder(rec)

	// probeLive is read from the probe goroutine, so guard it with atomic.
	var probeLive atomic.Bool
	probeLive.Store(true)
	det.SetBackgroundProbeForTest(func(paths []string) bool {
		return probeLive.Load()
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()

	// activity drives one re-evaluation. The first event for a session fires
	// processActivity immediately; later events within the 2s debounce window
	// coalesce, so we mark the follow-up Terminal to short-circuit the
	// debounce (production's periodic re-probe bypasses debounce the same way
	// via processActivityWithoutIdentity).
	activity := func(terminal bool) {
		tw.ch <- agent.Event{
			Type:           agent.EventActivity,
			SessionID:      sid,
			ProjectDir:     "-Users-test",
			TranscriptPath: path,
			Terminal:       terminal,
		}
	}

	// Probe reports the process alive → session stays working: no →ready
	// transition should be recorded even though the turn ended (turn_done).
	activity(false)
	time.Sleep(250 * time.Millisecond) // allow the async probe + any self-trigger to settle
	if n := readyTransitions(rec, sid); n != 0 {
		t.Fatalf("session flipped to ready %d time(s) while background process is alive", n)
	}

	// Background process exits — probe now reports it gone → session goes ready.
	probeLive.Store(false)
	activity(true)
	waitForReadyTransition(t, rec, sid)

	cancel()
	<-done
}
