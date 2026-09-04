package services_test

import (
	"context"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// TestSessionDetector_LateStopHookAfterProcessExit_SettlesToReady is issue
// #1772's acceptance evidence.
//
// A headless opencode run (`opencode run`) exits so quickly after the store
// write that ends its turn that the daemon's own process-exit teardown —
// HandleProcessExit records process_exited and deletes the session's repo
// row — routinely wins the race against the plugin's session.idle beacon
// POST. Measured against three real recordings while onboarding opencode's
// first hook-bearing fixture (#1770/#1771): the process exits roughly 115ms
// after the turn-ending store write, and the beacon lands only about 2ms
// after teardown has already run. hook_received is still recorded — the
// HTTP handler doesn't know the session is gone — but before this fix the
// state transition it exists to drive was silently dropped: the repo row is
// already deleted, deletedSessions tombstones the id for the next 10
// seconds, and processActivity's cooldown branch just returns. Three
// recorded attempts at the 2-1_basic-turn scenario came back
// "unsettled_session … still working" as a direct result.
//
// Reproduced here without opencode's own SQLite store or HTTP plugin, both
// incidental to the race: the daemon-side sequence that matters is
// HandleProcessExit(pid, sid, …) immediately followed by
// HandleStopHook(sid, …) for the same sid, inside the 10s cooldown — exactly
// what a same-process kqueue callback racing a slightly slower HTTP handler
// produces in production, compressed to (near) zero wall-clock gap here
// because the fix depends only on landing inside the cooldown window, not on
// the gap's exact size. Confirmed red against pre-fix behavior: reverting
// the fix (the deletedStates cache in session_detector.go/
// session_detector_helpers.go, the OnSessionRemoved wiring in
// pid_manager.go, dispatchHookActivity's Terminal flag, and processActivity's
// revive branch) while keeping this test makes it fail with "last saved
// state = "", want "ready"" — the Stop hook is dropped and repo.Save is never
// called for this session again.
func TestSessionDetector_LateStopHookAfterProcessExit_SettlesToReady(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const sid = "oc-headless-1"
	const path = "/tmp/opencode-test.db-wal?session=oc-headless-1"

	repo.states[sid] = &session.SessionState{
		SessionID:      sid,
		State:          session.StateWorking,
		PID:            54321,
		Adapter:        "opencode",
		TranscriptPath: path,
		FirstSeen:      time.Now().Unix(),
		UpdatedAt:      time.Now().Unix(),
		// LastEventType deliberately does NOT say the turn is done. Every bit
		// of "this settled to ready" below must come from the Stop hook's
		// SignalTurnDone hold — not from re-reading transcript content, which
		// wouldn't exist to re-read for a torn-down opencode DB session
		// anyway (issue #1772's whole point: the daemon's own repo row is
		// gone, there is nothing left to re-derive state from except the
		// hook's payload).
		Metrics: &session.SessionMetrics{LastEventType: "assistant_message"},
	}

	det := newDetector(tw, pw, repo)
	// Keep the default 10s cooldown: the race this reproduces falls inside a
	// single-digit-millisecond window, nowhere near the 10s boundary.

	// The daemon's own teardown wins the race: process exit is handled — and
	// the repo row deleted — before the hook lands.
	det.HandleProcessExit(54321, sid, "test: pid exited (ESRCH)")

	// The row is gone before the hook lands — a process exit deletes the
	// session whatever state it was in (#1860 restored that after #1800 briefly
	// retained a mid-turn exit as `error`), so the beacon below has to be
	// revived from deletedStates. That revive path IS what this test guards.
	if state, _ := repo.Load(sid); state != nil {
		t.Fatal("precondition: session should be deleted by HandleProcessExit")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- det.Run(ctx) }()
	defer func() { cancel(); <-done }()

	time.Sleep(20 * time.Millisecond) // let seedFromDisk finish

	// The plugin's beacon lands a couple of milliseconds too late in
	// production; here it lands right after teardown, well inside the 10s
	// cooldown — the scenario issue #1772 asks to be proven red, then green.
	det.HandleStopHook(sid, path, "Done. All tests pass.", false)

	waitForSessionState(repo, sid, session.StateReady, 2*time.Second)

	repo.mu.Lock()
	got := repo.lastSavedState[sid]
	repo.mu.Unlock()
	if got != session.StateReady {
		t.Fatalf("after a Stop hook racing process-exit teardown: last saved state = %q, want %q — "+
			"the turn-done signal was dropped against an already-torn-down session (issue #1772)",
			got, session.StateReady)
	}
}
