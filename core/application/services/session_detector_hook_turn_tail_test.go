package services_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
)

// hookTurnTailLogLine is the line the detector logs when it skips the force
// for the tail of a hook-ended turn (#2109 P5-0). It is spelled out here
// rather than read from the services constant because the orchestrator's live
// checker matches this exact text in daemon.log; a reworded constant would
// leave the checker reading a fixed daemon as broken.
const hookTurnTailLogLine = "turn-end tail after a hook turn end — not new activity"

// hookTailPath is shaped like opencode's activity path: a WAL file plus a
// session query, which os.Stat cannot find, so observeTranscriptGrowth leaves
// transcriptGrew true. That is why the #905 exemption never fires for the
// store pass this file is about. A claudecode or codex transcript that grew
// reaches the same place.
const hookTailPath = "/nonexistent/opencode/opencode.db-wal?session=hooktail"

const hookTailSID = "hooktail"

// hookTailFixture drives one session through a Stop-hook turn end and the
// passes that follow it. Each pass is awaited on the repo's save counter —
// finalizeActivityPass saves once per pass — so an assertion never reads a
// pass that has not run.
type hookTailFixture struct {
	t    *testing.T
	det  *services.SessionDetector
	tw   *mockAgentWatcher
	repo *mockRepo
	rec  *mockRecorder
	log  *mockLogger

	probes atomic.Int32

	mu   sync.Mutex
	next session.SessionMetrics
}

// newHookTailFixture starts a detector over one working session whose metrics
// source returns start until set is called.
func newHookTailFixture(t *testing.T, start session.SessionMetrics) *hookTailFixture {
	t.Helper()
	f := &hookTailFixture{t: t, tw: newMockAgentWatcher(), repo: newMockRepo(), rec: &mockRecorder{}, log: &mockLogger{}, next: start}
	initial := start
	f.repo.states[hookTailSID] = &session.SessionState{
		SessionID:      hookTailSID,
		State:          session.StateWorking,
		TranscriptPath: hookTailPath,
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		Metrics:        &initial,
	}
	metrics := &funcMetrics{fn: func(_, _ string) (*session.SessionMetrics, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		m := f.next
		return &m, nil
	}}
	deps := defaultSessionDetectorDeps(newMockProcessWatcher(), f.repo, nil)
	deps.Log = f.log
	deps.Metrics = metrics
	f.det = services.NewSessionDetector([]inbound.Watcher{f.tw}, deps)
	f.det.SetRecorder(f.rec)
	// A live background process, as the #937 lock needs. Counted so that lock
	// can require the probe to have run.
	f.det.SetBackgroundProbeForTest(func([]string) (bool, bool) {
		f.probes.Add(1)
		return true, true
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.det.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	// seedFromDisk re-persists the session once. Waiting for that save keeps
	// it out of the first pass's count.
	f.awaitSaves(1, "the startup seed")
	return f
}

func (f *hookTailFixture) saves() int {
	f.repo.mu.Lock()
	defer f.repo.mu.Unlock()
	return f.repo.saves
}

// awaitSaves polls until the repo has seen at least n saves, failing with the
// elapsed time if it never does.
func (f *hookTailFixture) awaitSaves(n int, what string) {
	f.t.Helper()
	start := time.Now()
	for f.saves() < n {
		if time.Since(start) > 3*time.Second {
			f.t.Fatalf("%s never ran: %d save(s) after %v, want %d", what, f.saves(), time.Since(start), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// set chooses the metrics the next pass reads.
func (f *hookTailFixture) set(m session.SessionMetrics) {
	f.mu.Lock()
	f.next = m
	f.mu.Unlock()
}

// pass runs send and waits for the pass it triggers to be saved.
func (f *hookTailFixture) pass(what string, send func()) {
	f.t.Helper()
	before := f.saves()
	send()
	f.awaitSaves(before+1, what)
}

// stopHook delivers a Stop hook and requires the hook to end the turn.
func (f *hookTailFixture) stopHook() {
	f.t.Helper()
	f.pass("the Stop hook's pass", func() {
		f.det.HandleStopHook(hookTailSID, hookTailPath, "Done.", false)
	})
	f.requireState(session.StateReady, "after the Stop hook")
}

// activity sends one watcher pass. terminal is set the way opencode's store
// watcher sets it; it also lets a second pass inside the 2s debounce window
// run at once instead of being coalesced.
func (f *hookTailFixture) activity(what string, synthetic, terminal bool) {
	f.t.Helper()
	f.pass(what, func() {
		f.tw.ch <- agent.Event{
			Type:           agent.EventActivity,
			SessionID:      hookTailSID,
			ProjectDir:     "-Users-test",
			TranscriptPath: hookTailPath,
			Synthetic:      synthetic,
			Terminal:       terminal,
		}
	})
}

// transitions returns the recorded state transitions for the session.
func (f *hookTailFixture) transitions() []lifecycle.Event {
	var out []lifecycle.Event
	for _, ev := range f.rec.snapshot() {
		if ev.SessionID == hookTailSID && ev.Kind == lifecycle.KindStateTransition {
			out = append(out, ev)
		}
	}
	return out
}

// forcesSince counts the force-bounces recorded after the first mark transitions.
func (f *hookTailFixture) forcesSince(mark int) int {
	n := 0
	for _, ev := range f.transitions()[mark:] {
		if ev.Reason == services.ForceReadyToWorkingReason {
			n++
		}
	}
	return n
}

func (f *hookTailFixture) state() string {
	f.repo.mu.Lock()
	defer f.repo.mu.Unlock()
	return f.repo.lastSavedState[hookTailSID]
}

func (f *hookTailFixture) requireState(want, when string) {
	f.t.Helper()
	if got := f.state(); got != want {
		f.t.Fatalf("%s: state = %q, want %q; transitions: %s", when, got, want, describeTransitions(f.transitions()))
	}
}

func (f *hookTailFixture) skipLines() int {
	n := 0
	for _, line := range f.log.infoSnapshot() {
		if line == hookTurnTailLogLine {
			n++
		}
	}
	return n
}

func describeTransitions(evs []lifecycle.Event) string {
	parts := make([]string, 0, len(evs))
	for _, ev := range evs {
		parts = append(parts, fmt.Sprintf("%s→%s (%s)", ev.PrevState, ev.NewState, ev.Reason))
	}
	return "[" + strings.Join(parts, "; ") + "]"
}

// TestSessionDetector_HookTurnTail_DoesNotBounce is the #2109 P5-0 defect. A
// hook ends the turn, and the store or transcript pass that follows carries
// only that turn's own tail: the turn still reads done and no user turn has
// started. Before the fix that pass forced ready → working and the classifier
// put it straight back, so every hook-ended turn recorded a bounce (10 of 10
// in the sandbox runs the triage measured, and claudecode 2-8 and codex 2-13
// among the committed recordings).
//
// Two arms: Terminal is how opencode's store watcher marks the pass, and a
// claudecode or codex transcript pass arrives without it, so the rule must not
// lean on it.
func TestSessionDetector_HookTurnTail_DoesNotBounce(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal bool
	}{
		{"store pass marked Terminal (opencode)", true},
		{"transcript pass without Terminal (claudecode, codex)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Not done on the transcript side ("assistant_message" is outside
			// IsAgentDone's fallback), so the ready below is the hook's.
			f := newHookTailFixture(t, session.SessionMetrics{LastEventType: "assistant_message", UserTurns: 1})
			f.stopHook()
			mark := len(f.transitions())

			f.set(session.SessionMetrics{LastEventType: "turn_done", UserTurns: 1})
			f.activity("the tail pass", false, tc.terminal)

			if got := f.transitions()[mark:]; len(got) != 0 {
				t.Errorf("the tail of a hook-ended turn moved the session: %s", describeTransitions(got))
			}
			f.requireState(session.StateReady, "after the tail pass")
			if n := f.skipLines(); n != 1 {
				t.Errorf("logged %q %d time(s), want 1 — the live checker needs it to see the tail was judged", hookTurnTailLogLine, n)
			}
		})
	}
}

// The locks below pass before the fix by construction: the base forces on
// every pass these tests send. Each one exists for the mutation fixture
// (tools/lib/hook-turn-tail-mutations_test.sh), which breaks one condition of
// the rule and requires the matching lock to go red.

// TestSessionDetector_HookTurnTail_NewUserTurnStillBounces is the claudecode
// 5-6 shape: a prompt and its end land in one coalesced pass after a
// hook-ended ready. That is a real new turn, so its bounce stays.
func TestSessionDetector_HookTurnTail_NewUserTurnStillBounces(t *testing.T) {
	f := newHookTailFixture(t, session.SessionMetrics{LastEventType: "assistant_message", UserTurns: 1})
	f.stopHook()
	mark := len(f.transitions())

	f.set(session.SessionMetrics{LastEventType: "turn_done", UserTurns: 2})
	f.activity("a collapsed new turn", false, true)

	if n := f.forcesSince(mark); n != 1 {
		t.Fatalf("a collapsed new user turn forced %d time(s), want 1; transitions: %s", n, describeTransitions(f.transitions()[mark:]))
	}
	f.requireState(session.StateReady, "after the collapsed turn")
}

// TestSessionDetector_HookTurnTail_NewUserTurnGoesWorking: a new prompt that
// has not finished is plain new activity.
func TestSessionDetector_HookTurnTail_NewUserTurnGoesWorking(t *testing.T) {
	f := newHookTailFixture(t, session.SessionMetrics{LastEventType: "assistant_message", UserTurns: 1})
	f.stopHook()

	f.set(session.SessionMetrics{LastEventType: "user", UserTurns: 2})
	f.activity("a new prompt", false, true)

	f.requireState(session.StateWorking, "after a new prompt")
}

// TestSessionDetector_HookTurnTail_SyntheticHookStillForces: every hook pass
// keeps the force (#307/#657), however the metrics read.
func TestSessionDetector_HookTurnTail_SyntheticHookStillForces(t *testing.T) {
	f := newHookTailFixture(t, session.SessionMetrics{LastEventType: "assistant_message", UserTurns: 1})
	f.stopHook()
	mark := len(f.transitions())

	f.set(session.SessionMetrics{LastEventType: "turn_done", UserTurns: 1})
	f.pass("the PreToolUse hook's pass", func() {
		f.det.HandlePermissionHook(hookTailSID, hookTailPath, session.HookPreToolUse)
	})

	if n := f.forcesSince(mark); n != 1 {
		t.Fatalf("a PreToolUse hook after a hook stop forced %d time(s), want 1; transitions: %s", n, describeTransitions(f.transitions()[mark:]))
	}
}

// TestSessionDetector_HookTurnTail_ZeroCountForcesAsToday: a metrics source
// that counts no user turns (hermes) gets today's behaviour.
func TestSessionDetector_HookTurnTail_ZeroCountForcesAsToday(t *testing.T) {
	f := newHookTailFixture(t, session.SessionMetrics{LastEventType: "assistant_message"})
	f.stopHook()
	mark := len(f.transitions())

	f.set(session.SessionMetrics{LastEventType: "turn_done"})
	f.activity("a tail with no user-turn count", false, true)

	if n := f.forcesSince(mark); n != 1 {
		t.Fatalf("with UserTurns 0 the tail forced %d time(s), want 1 (today's behaviour); transitions: %s", n, describeTransitions(f.transitions()[mark:]))
	}
}

// TestSessionDetector_HookTurnTail_LiveBackgroundProcessStaysWorking: the
// force's other job is to move the session to working before the
// background-liveness probe, which only runs for working sessions (#937). A
// done pass that shows a background process must keep it.
func TestSessionDetector_HookTurnTail_LiveBackgroundProcessStaysWorking(t *testing.T) {
	f := newHookTailFixture(t, session.SessionMetrics{LastEventType: "assistant_message", UserTurns: 1})
	f.stopHook()

	f.set(session.SessionMetrics{
		LastEventType:            "turn_done",
		UserTurns:                1,
		BackgroundProcessCount:   1,
		BackgroundProcessOutputs: []string{"/tmp/x/tasks/hooktail.output"},
	})
	f.activity("a done pass with a background process", false, true)

	f.requireState(session.StateWorking, "with a live background process")
	start := time.Now()
	for f.probes.Load() == 0 {
		if time.Since(start) > 2*time.Second {
			t.Fatalf("the background probe never ran after %v — the session never reached working with its process", time.Since(start))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestSessionDetector_HookTurnTail_StaleMarkerForces: the marker describes the
// ready a hook produced and nothing after it. Once the session has left that
// ready and come back by the transcript alone, a done pass with the same count
// is ordinary activity again and forces.
func TestSessionDetector_HookTurnTail_StaleMarkerForces(t *testing.T) {
	f := newHookTailFixture(t, session.SessionMetrics{LastEventType: "assistant_message", UserTurns: 1})
	f.stopHook()

	// An agent-written turn opener (claudecode's isMeta, #1858) leaves the
	// count where it was and reads not done, so it goes working.
	f.set(session.SessionMetrics{LastEventType: "assistant_message", UserTurns: 1})
	f.activity("an agent-written turn", false, true)
	f.requireState(session.StateWorking, "after the agent-written turn")

	f.set(session.SessionMetrics{LastEventType: "turn_done", UserTurns: 1})
	f.activity("its transcript turn end", false, true)
	f.requireState(session.StateReady, "after the transcript turn end")
	mark := len(f.transitions())

	f.activity("a later done pass", false, true)
	if n := f.forcesSince(mark); n != 1 {
		t.Fatalf("after a transcript-only ready the pass forced %d time(s), want 1 — a marker from the earlier hook ready was still applied; transitions: %s", n, describeTransitions(f.transitions()[mark:]))
	}
}

// TestSessionDetector_HookTurnTail_TranscriptReadyKeepsTheForce: only a ready
// a hook decided is exempt. The same done pass after a ready the transcript
// decided forces as it always has.
func TestSessionDetector_HookTurnTail_TranscriptReadyKeepsTheForce(t *testing.T) {
	f := newHookTailFixture(t, session.SessionMetrics{LastEventType: "assistant_message", UserTurns: 1})

	f.set(session.SessionMetrics{LastEventType: "turn_done", UserTurns: 1})
	f.activity("a transcript turn end", false, true)
	f.requireState(session.StateReady, "after the transcript turn end")
	mark := len(f.transitions())

	f.activity("a later done pass", false, true)
	if n := f.forcesSince(mark); n != 1 {
		t.Fatalf("after a transcript-decided ready the pass forced %d time(s), want 1; transitions: %s", n, describeTransitions(f.transitions()[mark:]))
	}
}
