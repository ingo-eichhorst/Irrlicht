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

// TestSessionDetector_HandleProcessExit_StaleWatcherHintIsNotAttributed is
// red-first evidence C (QA finding #2 during #1962 review — the original
// version of this test named "old-superseded" only in its own comment and
// assertion, never in the setup, so it passed against ANY implementation,
// including a no-op HandleWatcherExit).
//
// This version actually exercises the stale-hint branch: the repo holds
// only "current" bound to pid, but the watcher edge fires carrying
// "old-superseded" — the id of a session that once held pid before
// "current" claimed it (the /clear pattern) and was already deleted by
// cleanupStalePIDHolders, exactly the shape a monitor's last-write-wins
// `watched` map can still be holding as its trigger hint. RED on
// origin/main: the unfixed SessionDetector.HandleProcessExit calls straight
// through to the single-session primitive with whatever id it's handed, so
// it records process_exited for "old-superseded" (not "current") and never
// touches "current" at all — see red.txt.
func TestSessionDetector_HandleProcessExit_StaleWatcherHintIsNotAttributed(t *testing.T) {
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
	// "old-superseded" is deliberately absent from the repo.

	det := newDetector(tw, pw, repo)
	rec := &mockRecorder{}
	det.SetRecorder(rec)

	// The watcher edge fires carrying the STALE hint, not "current".
	det.HandleProcessExit(pid, "old-superseded", "test: pid exited")

	var currentExited, oldExited int
	for _, ev := range rec.snapshot() {
		if ev.Kind != lifecycle.KindProcessExited {
			continue
		}
		switch ev.SessionID {
		case "current":
			currentExited++
		case "old-superseded":
			oldExited++
		}
	}
	if currentExited != 1 {
		t.Errorf("expected exactly one KindProcessExited for the current PID holder, got %d (events: %+v)", currentExited, rec.snapshot())
	}
	if oldExited != 0 {
		t.Errorf("the stale watcher hint must never receive process_exited, got %d event(s) for old-superseded", oldExited)
	}
	if state, _ := repo.Load("current"); state != nil {
		t.Errorf("current session should be deleted on pid exit, but still exists (state=%s)", state.State)
	}
}

// TestSessionDetector_HandleProcessExit_DeletesSession (in
// session_detector_lifecycle_test.go) is the LOCK for the ordinary,
// single-session-per-PID case: it binds exactly one session to a PID and
// asserts that session is deleted on exit. The fan-out reduces to the same
// single call in that shape, and that test's continued green (unchanged by
// this fix) is what pins it.

// TestSessionDetector_HandleProcessExit_PIDZeroDoesNotMassDelete is red-first
// evidence D (QA finding #1 during #1962 review): HandleWatcherExit's
// pidHolders(pid) scan had no `pid <= 0` guard, unlike essentially every
// sibling PID check in pid_manager.go. pidHolders(0) matches every session
// whose PID is still 0 — the PID=0 ghosts the readyTTL sweep (not the
// watcher) is supposed to own — and fans process_exited out to all of them.
//
// This is defence in depth, not a reachable production bug: both monitors'
// Watch() reject pid <= 0 outright (see monitor_darwin.go / monitor_linux.go),
// and Run's exit path only ever derives pid from a live kevent/pidfd, so the
// watcher callback itself can never carry pid <= 0. The guard exists so a
// hypothetical or future caller of HandleWatcherExit can't turn "watcher
// fired with an invalid pid" into "delete every unbound session".
func TestSessionDetector_HandleProcessExit_PIDZeroDoesNotMassDelete(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	repo.states["unbound-a"] = &session.SessionState{
		SessionID: "unbound-a",
		Adapter:   "claude-code",
		State:     session.StateWorking,
		PID:       0,
	}
	repo.states["unbound-b"] = &session.SessionState{
		SessionID: "unbound-b",
		Adapter:   "claude-code",
		State:     session.StateWorking,
		PID:       0,
	}

	det := newDetector(tw, pw, repo)

	det.HandleProcessExit(0, "whatever", "qa probe: pid<=0 guard")

	if state, _ := repo.Load("unbound-a"); state == nil {
		t.Error("unbound session unbound-a should NOT be deleted by a pid=0 watcher exit")
	}
	if state, _ := repo.Load("unbound-b"); state == nil {
		t.Error("unbound session unbound-b should NOT be deleted by a pid=0 watcher exit")
	}
}
