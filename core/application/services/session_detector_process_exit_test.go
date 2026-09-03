package services_test

import (
	"testing"
	"time"

	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// midTurnWorkingSession is the shape a session has when its process goes away
// with a turn still open: actively working, with a tool call the agent will
// never come back to close.
//
// The open tool call is what makes this genuinely mid-turn and is not
// incidental. `LastEventType: "assistant"` alone is NOT mid-turn — IsAgentDone
// treats a bare assistant tail as a completed turn (the pre-Stop-hook
// fallback), so a fixture without the open call describes a session that had
// finished. "Bash" specifically, because it is not a user-blocking tool name:
// NeedsUserAttention stays false, so this fixture classifies to `working` on
// its own and the assertion below is about the process evidence rather than
// about a waiting rule.
func midTurnWorkingSession(sid string, pid int) *session.SessionState {
	return &session.SessionState{
		SessionID:      sid,
		State:          session.StateWorking,
		PID:            pid,
		Adapter:        "geminicli",
		TranscriptPath: "/tmp/" + sid + ".jsonl",
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		Metrics: &session.SessionMetrics{
			LastEventType:     "assistant",
			HasOpenToolCall:   true,
			LastOpenToolNames: []string{"Bash"},
		},
	}
}

// TestHandleProcessExit_MidTurnExitDeletesTheRow is the defect test for #1860.
//
// RED BEFORE THE FIX, against the tree that still carried #1800: the row was
// converted to `error` and KEPT for twelve hours, so this assertion failed with
// "session survived the process exit ... state=error". #1800 guessed a crash
// from the session's shape, and the guess is wrong for the two commonest ways a
// working session ends — an ESC interrupt and a denied tool prompt — because
// the classifier's activity debounce means the exit edge reads a row that has
// not yet seen the interrupt (#1860's reproduction).
//
// THE RULE THIS PINS: irrlicht is not the agent's parent, so it gets no exit
// status on any platform. A segfault, an OOM kill, a `kill -9` and a clean
// `exit(0)` are one observation, and a state that is wrong is worse than a
// state that is absent. A process exit therefore deletes the row, whatever
// state the row was in.
//
// NO det.Run, DELIBERATELY, matching the two HandleProcessExit tests in
// session_detector_lifecycle_test.go. The whole path is synchronous, so the
// event loop buys this test nothing — and starting it introduces a real race
// against seedFromDisk, whose SeedPIDs pass re-Saves a row with a live PID and
// so resurrects the very row the assertion is about. Measured at 17 failures
// in 30 runs under -race before the goroutine was dropped, and the resurrected
// row reads `working`, which is a different failure from the one this test
// exists to catch. In production the two cannot overlap: SeedPIDs runs
// synchronously inside Run before the event loop's select and before the sweep
// goroutine exists (see its doc), so neither real caller of HandleProcessExit
// can race it.
func TestHandleProcessExit_MidTurnExitDeletesTheRow(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const sid = "midturn-exit-1"
	const pid = 30535
	repo.states[sid] = midTurnWorkingSession(sid, pid)

	det := newDetector(tw, pw, repo)

	det.HandleProcessExit(pid, sid, "test: pid exited (ESRCH)")

	if state, err := repo.Load(sid); err == nil && state != nil {
		t.Fatalf("session survived the process exit — state=%q; a process exit must delete the row, "+
			"because the daemon cannot tell a crash from a deliberate quit (#1860)", state.State)
	}
}

// TestHandleProcessExit_RecordsProcessExitedForAMidTurnDeath pins the recorded
// trace as well as the row, because the two came apart under #1800: the retain
// branch deliberately suppressed KindProcessExited and recorded a
// process_died_midturn event in its place. With the retain branch gone there is
// one exit edge and one Kind again, and a replayed recording sees the deletion.
//
// RED BEFORE THE FIX: no KindProcessExited was recorded for a mid-turn exit at
// all.
func TestHandleProcessExit_RecordsProcessExitedForAMidTurnDeath(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()
	rec := &mockRecorder{}

	const sid = "midturn-exit-2"
	const pid = 30536
	repo.states[sid] = midTurnWorkingSession(sid, pid)

	det := newDetector(tw, pw, repo)
	det.SetRecorder(rec)

	det.HandleProcessExit(pid, sid, "test: pid exited (ESRCH)")

	var exited int
	for _, ev := range rec.snapshot() {
		if ev.Kind == lifecycle.KindProcessExited && ev.SessionID == sid {
			exited++
		}
	}
	if exited != 1 {
		t.Fatalf("KindProcessExited recorded %d times, want exactly 1 — a mid-turn exit is an "+
			"ordinary exit again (#1860)", exited)
	}
}
