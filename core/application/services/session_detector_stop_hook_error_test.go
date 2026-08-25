package services_test

import (
	"testing"

	"irrlicht/core/domain/session"
)

// TestHandleStopHook_RetiresTheStickySessionError pins the wiring half of
// #1799's blip fix: the Stop hook must reach the metrics collector, which is
// the only route to the tailer's sticky session error.
//
// WHY A WIRING TEST AND NOT A STATE TEST. The flicker itself needs a real
// tailer — the collector doubles here hold none — because the two passes that
// disagree are two tailer passes. What CAN be pinned from this layer is the one
// thing whose absence silently restores the bug: HandleStopHook holds a
// consume-once SignalTurnDone, which suppresses the classifier's session_error
// rule for exactly one pass, and if the call below goes missing the very next
// pass reads the untouched error again. `core/pkg/tailer`'s
// TestClearSessionError_RetiresAStandingError covers the other half — that the
// call, once it arrives, actually ends the error.
//
// It is an ADDED check, so it has no red-first "before". Verified by mutation:
// deleting the d.metrics.ClearSessionError call from HandleStopHook fails it.
func TestHandleStopHook_RetiresTheStickySessionError(t *testing.T) {
	tw := newMockAgentWatcher()
	pw := newMockProcessWatcher()
	repo := newMockRepo()

	const sid = "stoperr1"
	const path = "/home/.claude/projects/-Users-test/stoperr1.jsonl"

	metrics := &funcMetrics{fn: func(_, _ string) (*session.SessionMetrics, error) {
		return &session.SessionMetrics{LastEventType: "assistant_message"}, nil
	}}
	det := newDetectorWithMetrics(tw, pw, repo, metrics)

	// No Run goroutine: HandleStopHook places the hold, retires the error and
	// enqueues the synthetic activity event synchronously on the caller's
	// goroutine, so the effect under test is observable without racing the
	// event loop.
	det.HandleStopHook(sid, path, "Done.", false)

	got := metrics.errClearedSnapshot()
	if len(got) != 1 || got[0] != path {
		t.Fatalf("ClearSessionError calls = %v, want exactly [%q] — without it a "+
			"Stop hook only SUPPRESSES the session_error rule for the single pass "+
			"that consumed the hold, and the next pass reads the still-sticky "+
			"error: error → ready → error", got, path)
	}
}
