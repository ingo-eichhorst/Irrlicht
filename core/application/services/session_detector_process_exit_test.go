package services_test

import (
	"context"
	"os"
	"testing"
	"time"

	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// selfPID is this test process, used so the detector's startup seed does not
// delete a fixture before the test drives the exit itself: seedAlivePIDs reaps
// any persisted session whose PID is already dead (ESRCH), by a direct delete
// that never reaches HandleProcessExit. A live PID keeps the row until the test
// drives the exit, which is also the real sequence — the kqueue callback fires
// for a process the daemon was watching.
var selfPID = os.Getpid()

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
func midTurnWorkingSession(sid string) *session.SessionState {
	return &session.SessionState{
		SessionID:      sid,
		State:          session.StateWorking,
		PID:            selfPID,
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
func TestHandleProcessExit_MidTurnExitDeletesTheRow(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const sid = "midturn-exit-1"
	repo.states[sid] = midTurnWorkingSession(sid)

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	// Two defers, LIFO: cancel runs first, then the join.
	defer func() { <-done }()
	defer cancel()

	det.HandleProcessExit(selfPID, sid, "test: pid exited (ESRCH)")

	waitForSessionDeleted(repo, sid, 2*time.Second)

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
	repo.states[sid] = midTurnWorkingSession(sid)

	det := newDetector(tw, pw, repo)
	det.SetRecorder(rec)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	defer func() { <-done }()
	defer cancel()

	det.HandleProcessExit(selfPID, sid, "test: pid exited (ESRCH)")

	waitForSessionDeleted(repo, sid, 2*time.Second)

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
