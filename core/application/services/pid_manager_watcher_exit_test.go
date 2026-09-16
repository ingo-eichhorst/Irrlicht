package services_test

import (
	"os"
	"testing"

	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// Issue #1962: a muse parent session and its ParentSessionID-linked
// goal-reminder / verify-reminder subagents run in-process and all report
// the same OS PID. The process-watcher's exit callback can only ever name
// ONE session per PID — its internal map is keyed by pid alone, last-write-
// wins (see the doc comments on the `watched` field in monitor_darwin.go /
// monitor_linux.go) — so when N sessions share a PID, N-1 of them never see
// process_exited at that edge. The cited live recording shows exactly this:
// the parent session received no process_exited at all; a finished subagent
// got it instead.
//
// The fix fans the exit edge out from the session repository (every session
// currently bound to the PID), not from the watcher's own map. These tests
// exercise the fan-out through SessionDetector.HandleProcessExit — the same
// method core/cmd/irrlichd/startup.go's watcher callback calls — because
// that is the real, unchanged entry point the watcher edge uses in
// production; only its internal routing changes.

// TestSessionDetector_HandleProcessExit_FansOutToParent is red-first
// evidence A: a parent plus two ParentSessionID-linked subagents are all
// bound to the same PID, and the watcher edge fires ONCE carrying the id of
// whichever session most recently registered Watch() for that PID — a
// subagent, matching the shared-PID scenario the issue reports. Before the
// fix, only that one subagent is deleted and only its own id gets a
// KindProcessExited event; the parent survives with no event at all — see
// red.txt for the captured failure.
func TestSessionDetector_HandleProcessExit_FansOutToParent(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const pid = 47792
	repo.states["parent"] = &session.SessionState{
		SessionID: "parent",
		Adapter:   "muse",
		State:     session.StateWorking,
		PID:       pid,
	}
	repo.states["sub-goal"] = &session.SessionState{
		SessionID:       "sub-goal",
		Adapter:         "muse",
		State:           session.StateReady,
		PID:             pid,
		ParentSessionID: "parent",
	}
	repo.states["sub-verify"] = &session.SessionState{
		SessionID:       "sub-verify",
		Adapter:         "muse",
		State:           session.StateReady,
		PID:             pid,
		ParentSessionID: "parent",
	}

	det := newDetector(tw, pw, repo)
	rec := &mockRecorder{}
	det.SetRecorder(rec)

	// The watcher edge fires exactly once, carrying the id of the LAST
	// session to register Watch(pid, ...) — a subagent in this scenario.
	det.HandleProcessExit(pid, "sub-verify", "process watcher: pid exited (NOTE_EXIT)")

	if state, _ := repo.Load("parent"); state != nil {
		t.Errorf("parent session should be deleted on shared-PID exit, but still exists (state=%s)", state.State)
	}
	if state, _ := repo.Load("sub-goal"); state != nil {
		t.Errorf("sibling subagent sub-goal should be deleted too, but still exists (state=%s)", state.State)
	}

	found := false
	for _, ev := range rec.snapshot() {
		if ev.Kind == lifecycle.KindProcessExited && ev.SessionID == "parent" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a KindProcessExited event for the parent session, got: %+v", rec.snapshot())
	}
}

// TestSessionDetector_HandleProcessExit_FanOutIsSynchronous is red-first
// evidence B: the parent must be reaped by the watcher edge ITSELF, not by
// the periodic liveness sweep's ESRCH probe (reapDeadOrInfraPID) coming
// along later. Every session here is bound to the test process's OWN pid
// (os.Getpid(), alive for the whole test), so syscall.Kill(pid, 0) would
// never report ESRCH — a stray sweep tick could not explain a parent
// deletion. det.Run is never called in this test, so no sweep goroutine
// exists at all: the assertion runs synchronously, immediately after
// HandleProcessExit returns, with no sleep or poll in between. A pass can
// only come from the watcher-edge fan-out itself.
func TestSessionDetector_HandleProcessExit_FanOutIsSynchronous(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	pid := os.Getpid() // alive for the test's duration — rules out the ESRCH sweep path
	repo.states["parent"] = &session.SessionState{
		SessionID: "parent",
		Adapter:   "muse",
		State:     session.StateWorking,
		PID:       pid,
	}
	repo.states["sub-goal"] = &session.SessionState{
		SessionID:       "sub-goal",
		Adapter:         "muse",
		State:           session.StateReady,
		PID:             pid,
		ParentSessionID: "parent",
	}

	det := newDetector(tw, pw, repo)

	det.HandleProcessExit(pid, "sub-goal", "process watcher: pid exited (NOTE_EXIT)")

	if state, _ := repo.Load("parent"); state != nil {
		t.Errorf("parent session should be deleted by the watcher edge itself, but still exists (state=%s)", state.State)
	}
}

// TestSessionDetector_HandleProcessExit_NoEventForAlreadyGoneSession is a
// LOCK, not red-first proof — it passes by construction both before and
// after the fix, because the fan-out re-queries the session repository at
// call time: a session that is no longer there (superseded by
// cleanupStalePIDHolders' /clear handling, or deleted by any other path
// first) can never be found as a current holder of the PID and so can never
// receive process_exited. "old-superseded" is deliberately absent from the
// repo below — exactly the shape of a real superseded session by the time
// the exit edge fires.
func TestSessionDetector_HandleProcessExit_NoEventForAlreadyGoneSession(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const pid = 9090
	repo.states["current"] = &session.SessionState{
		SessionID: "current",
		Adapter:   "claude-code",
		State:     session.StateReady,
		PID:       pid,
	}

	det := newDetector(tw, pw, repo)
	rec := &mockRecorder{}
	det.SetRecorder(rec)

	det.HandleProcessExit(pid, "current", "test: pid exited")

	for _, ev := range rec.snapshot() {
		if ev.Kind == lifecycle.KindProcessExited && ev.SessionID == "old-superseded" {
			t.Fatalf("a superseded/deleted session must never receive process_exited, got: %+v", ev)
		}
	}
}
