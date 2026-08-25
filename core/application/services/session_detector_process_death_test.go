package services_test

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// workingMidTurn is the shape a session has when its process dies with a turn
// still open: actively working, with a tool call the agent will never come
// back to close.
//
// The open tool call is what makes this genuinely mid-turn and is not
// incidental. `LastEventType: "assistant"` alone is NOT mid-turn —
// IsAgentDone treats a bare assistant tail as a completed turn (the pre-Stop-
// hook fallback), so a fixture without the open call describes a session that
// had finished. "Bash" specifically, because it is not a user-blocking tool
// name: NeedsUserAttention stays false, so this fixture classifies to
// `working` on its own and the assertions below are about the process
// evidence rather than about a waiting rule.
// livePID is this test process, used so the detector's startup seed does not
// delete the fixture before the test runs: seedAlivePIDs reaps any persisted
// session whose PID is already dead (ESRCH), by a direct delete that never
// reaches HandleProcessExit. A live PID keeps the row until the test drives
// the exit itself, which is also the real sequence — the kqueue callback fires
// for a process the daemon was watching.
var livePID = os.Getpid()

func workingMidTurn(sid string) *session.SessionState {
	return &session.SessionState{
		SessionID:      sid,
		State:          session.StateWorking,
		PID:            livePID,
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

// TestHandleProcessExit_MidTurnDeathRetainsSessionAsError is the defect test
// for #1800's daemon-side half.
//
// RED BEFORE THE FIX: HandleProcessExit deleted every session whose process
// exited, whatever state it was in, so the single most useful thing irrlicht
// could say about an agent left running — that it stopped without finishing —
// was the one outcome it could not report. The row simply vanished, which is
// indistinguishable from the session never having mattered.
func TestHandleProcessExit_MidTurnDeathRetainsSessionAsError(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const sid = "crash-1"
	repo.states[sid] = workingMidTurn(sid)

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	// Two defers, LIFO: cancel runs first, then the join. Spelled this way
	// rather than as one closure so `cancel` is a plain deferred call.
	defer func() { <-done }()
	defer cancel()

	det.HandleProcessExit(livePID, sid, "test: pid exited (ESRCH)")

	waitForSessionState(repo, sid, session.StateError, 2*time.Second)

	state, _ := repo.Load(sid)
	if state == nil {
		t.Fatal("session was deleted — a mid-turn process death must be reported, not erased")
	}
	if state.State != session.StateError {
		t.Fatalf("state = %q, want %q", state.State, session.StateError)
	}
	if state.Metrics == nil || state.Metrics.SessionError == nil {
		t.Fatal("no SessionError recorded — the red state carries no explanation")
	}
	if got := state.Metrics.SessionError.Class; got != session.ErrorClassProcessDeath {
		t.Errorf("Class = %q, want %q", got, session.ErrorClassProcessDeath)
	}
	if state.Metrics.SessionError.Phase != session.ErrorPhaseTerminal {
		t.Errorf("Phase = %q, want terminal — an exited process will not retry",
			state.Metrics.SessionError.Phase)
	}
}

// TestHandleProcessExit_CleanShapesAreStillDeleted is diedMidTurn's fail-closed
// side, one row per clause.
//
// Every one of these passed before #1800 too — the old code deleted
// unconditionally — so they are LOCKS, not red-first evidence. Their value is
// the mutation in the PR body: drop any single clause from diedMidTurn and the
// matching row here turns red, which is the only way to show that clause
// reaches anything.
func TestHandleProcessExit_CleanShapesAreStillDeleted(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*session.SessionState)
		because string
	}{
		{"ready", func(s *session.SessionState) {
			s.State = session.StateReady
		}, "a finished session had nothing in flight to lose"},

		{"waiting", func(s *session.SessionState) {
			s.State = session.StateWaiting
		}, "a session blocked on the user was not mid-turn"},

		{"turn-already-done", func(s *session.SessionState) {
			s.Metrics.LastEventType = "turn_done"
			s.Metrics.HasOpenToolCall = false
			s.Metrics.LastOpenToolNames = nil
		}, "the turn had ended and the daemon had merely not caught up — the ordinary " +
			"shape of every headless run"},

		{"user-esc-interrupt", func(s *session.SessionState) {
			s.Metrics.LastWasUserInterrupt = true
		}, "the USER stopped it; 2-20_user-esc-interrupt must keep its arc"},

		{"tool-denial", func(s *session.SessionState) {
			s.Metrics.LastWasToolDenial = true
		}, "the user denied a tool and the agent stopped"},

		{"child-session", func(s *session.SessionState) {
			s.ParentSessionID = "parent-1"
		}, "a subagent has no process of its own; reapStaleChild owns its teardown"},

		{"no-metrics", func(s *session.SessionState) {
			s.Metrics = nil
		}, "nothing is known, so nothing is claimed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tw := newMockAgentWatcher()
			pw := newMockProcessWatcher()
			repo := newMockRepo()

			const sid = "clean-1"
			st := workingMidTurn(sid)
			tc.mutate(st)
			repo.states[sid] = st

			det := newDetector(tw, pw, repo)
			det.HandleProcessExit(livePID, sid, "test: pid exited (ESRCH)")

			if got, _ := repo.Load(sid); got != nil {
				t.Fatalf("session was retained as %q, want deleted — %s", got.State, tc.because)
			}
		})
	}
}

// TestHandleProcessExit_IsIdempotentAcrossSweeps pins the property both exit
// paths depend on. The periodic liveness sweep re-reaps a dead PID every few
// seconds for as long as the row exists, so this is called again and again
// after the conversion; it must keep answering "keep" without re-converting.
func TestHandleProcessExit_IsIdempotentAcrossSweeps(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const sid = "crash-idem"
	repo.states[sid] = workingMidTurn(sid)

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	// Two defers, LIFO: cancel runs first, then the join. Spelled this way
	// rather than as one closure so `cancel` is a plain deferred call.
	defer func() { <-done }()
	defer cancel()

	det.HandleProcessExit(livePID, sid, "kqueue: pid exited (NOTE_EXIT)")
	waitForSessionState(repo, sid, session.StateError, 2*time.Second)

	// Now the sweep, several times over, exactly as reapDeadOrInfraPID does.
	for i := 0; i < 3; i++ {
		det.HandleProcessExit(livePID, sid, "pid exited (ESRCH)")
	}

	state, _ := repo.Load(sid)
	if state == nil {
		t.Fatal("a later sweep deleted the errored session — the verdict must survive " +
			"repeated reaps of the same dead PID")
	}
	if state.State != session.StateError {
		t.Errorf("state = %q, want %q after repeated sweeps", state.State, session.StateError)
	}
}

// TestProcessDeath_YieldsToAFinishedTurnOnTheNextPass is the false-positive
// guard, and it is the reason the verdict is not decided on the exit edge.
//
// A headless agent writes its turn-ending line and exits microseconds later, so
// at the instant the process dies the daemon has usually not read that line
// yet: `working` with an open-looking tail is what EVERY clean `claude -p`,
// `codex exec` and `opencode run` looks like from the exit edge. The hold is
// therefore placed provisionally, and SignalProcessDeath's staleness rule
// decides once the metrics have been rebuilt from the finished transcript.
//
// Here the recomputed metrics say the turn ended, so the session must NOT stay
// red.
func TestProcessDeath_YieldsToAFinishedTurnOnTheNextPass(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const sid = "headless-1"
	repo.states[sid] = workingMidTurn(sid)

	// The transcript is only re-read as finished AFTER the exit, which is the
	// whole ordering under test: on the exit edge the daemon still believes
	// the turn is open.
	var finished atomic.Bool
	metrics := &funcMetrics{fn: func(string, string) (*session.SessionMetrics, error) {
		if !finished.Load() {
			return nil, nil
		}
		return &session.SessionMetrics{LastEventType: "turn_done"}, nil
	}}

	det := newDetectorWithMetrics(tw, pw, repo, metrics)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	// Two defers, LIFO: cancel runs first, then the join. Spelled this way
	// rather than as one closure so `cancel` is a plain deferred call.
	defer func() { <-done }()
	defer cancel()

	det.HandleProcessExit(livePID, sid, "test: headless run exited")
	waitForSessionState(repo, sid, session.StateError, 2*time.Second)
	if st, _ := repo.Load(sid); st == nil || st.State != session.StateError {
		t.Fatalf("precondition: the exit edge must reach the provisional verdict, got %+v", st)
	}

	// The tailer catches up: the transcript did end on a completed turn.
	finished.Store(true)
	tw.ch <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      sid,
		TranscriptPath: "/tmp/" + sid + ".jsonl",
	}

	waitForSessionState(repo, sid, session.StateReady, 2*time.Second)
	st, _ := repo.Load(sid)
	if st == nil {
		t.Fatal("session disappeared")
	}
	if st.State != session.StateReady {
		t.Fatalf("state = %q, want %q — a turn that had in fact completed must not be "+
			"reported as a crash once the transcript is re-read", st.State, session.StateReady)
	}
}

// TestHandlePIDAssigned_RetiresAProcessDeathVerdict covers the other
// end-of-life notice: a session that has a live PID again is not a session
// whose process is gone. Nothing about a resumed session's METRICS says the
// process came back, so the staleness rule cannot see it and the release has
// to be explicit.
func TestHandlePIDAssigned_RetiresAProcessDeathVerdict(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const sid = "resumed-1"
	repo.states[sid] = workingMidTurn(sid)

	det := newDetector(tw, pw, repo)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	// Two defers, LIFO: cancel runs first, then the join. Spelled this way
	// rather than as one closure so `cancel` is a plain deferred call.
	defer func() { <-done }()
	defer cancel()

	det.HandleProcessExit(livePID, sid, "test: pid exited")
	waitForSessionState(repo, sid, session.StateError, 2*time.Second)

	// The session is resumed under the same id and binds a new PID.
	det.HandlePIDAssigned(livePID, sid)

	// With the hold released, the row is an ordinary one again: the next
	// process exit for it takes the delete path, because it is no longer being
	// retained by a standing verdict.
	det.HandleProcessExit(livePID, sid, "test: pid exited again")
	if got, _ := repo.Load(sid); got != nil {
		t.Fatalf("session retained as %q — HandlePIDAssigned must retire the standing "+
			"process-death hold, or a resumed session can never be reaped again", got.State)
	}
}

// TestProcessDeath_RetentionIsTerminal is the regression test for the
// immortality loop the first cut of this feature shipped.
//
// RED BEFORE THE FIX: retention was owned by the SignalProcessDeath hold's
// ceiling, and "keep the row" meant "the hold still stands". Once the ceiling
// dropped the hold, the next classify pass re-derived `working` from the frozen
// transcript, the next sweep called retainAsProcessDeath again, diedMidTurn said
// yes a second time, and the session was re-converted — forever, emitting a
// spurious "the agent is working" transition every twelve hours for a process
// that had been dead for days. The doc comment claimed there was "no way for a
// retained session to become immortal".
//
// The window is compressed via SetProcessDeathRetention; a twelve-hour test is
// not a test.
func TestProcessDeath_RetentionIsTerminal(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const sid = "crash-expiry"
	repo.states[sid] = workingMidTurn(sid)

	det := newDetector(tw, pw, repo)
	det.SetProcessDeathRetention(150 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	defer func() { <-done }()
	defer cancel()
	go func() { done <- det.Run(ctx) }()

	det.HandleProcessExit(livePID, sid, "test: pid exited")
	waitForSessionState(repo, sid, session.StateError, 2*time.Second)
	if st, _ := repo.Load(sid); st == nil || st.State != session.StateError {
		t.Fatalf("precondition: the session must first be retained as error, got %+v", st)
	}

	// Poll the sweep past the retention deadline. Polling rather than one
	// sleep-then-assert so the failure says how long it actually took.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		det.HandleProcessExit(livePID, sid, "pid exited (ESRCH)")
		if st, _ := repo.Load(sid); st == nil {
			return // reaped — the verdict was terminal
		}
		time.Sleep(20 * time.Millisecond)
	}

	st, _ := repo.Load(sid)
	t.Fatalf("session still present as %q after %v of sweeps past a %v retention window — "+
		"the process-death verdict is being re-applied instead of expiring, so this row "+
		"can never be reaped", st.State, 3*time.Second, 150*time.Millisecond)
}

// Once the window has closed the session must be judged on its own merits
// again, NOT immediately re-converted. This is the specific step the loop took.
func TestProcessDeath_DoesNotReconvertAfterExpiry(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const sid = "crash-noreconvert"
	repo.states[sid] = workingMidTurn(sid)

	det := newDetector(tw, pw, repo)
	det.SetProcessDeathRetention(1 * time.Nanosecond)

	// No Run loop: this is purely about the decision, and without a classify
	// pass the row stays exactly as seeded — `working`, mid-turn — which is the
	// state the loop kept re-converting.
	if !det.HandleProcessExitRetainedForTest(livePID, sid, "first exit") {
		t.Fatal("precondition: the first mid-turn exit must retain the session")
	}
	if det.HandleProcessExitRetainedForTest(livePID, sid, "sweep after expiry") {
		t.Fatal("the session was re-converted after its retention window closed — " +
			"diedMidTurn still says yes for a frozen mid-turn transcript, so the " +
			"verdict registry is the only thing that can make retention terminal")
	}
	if st, _ := repo.Load(sid); st != nil {
		t.Errorf("session should have been reaped once the verdict expired, got %q", st.State)
	}
}
