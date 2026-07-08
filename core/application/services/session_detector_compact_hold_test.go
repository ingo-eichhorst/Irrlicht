package services

import (
	"testing"

	"irrlicht/core/domain/session"
)

// TestApplyCompactHold covers the PreCompact force-working hold (#657): a fresh
// hold overlays CompactInProgress, the manual compact_boundary clears it
// (normal release, #656), and — the safety net this test pins — an interrupted
// /compact that never writes a boundary is dropped once compactHoldTimeout
// elapses instead of stranding the session in working forever. White-box so it
// can drive the unexported method + compactPending map directly, injecting
// `now` rather than sleeping out the real multi-minute timeout.
func TestApplyCompactHold(t *testing.T) {
	const sid = "s"
	timeout := int64(compactHoldTimeout.Seconds())

	t.Run("fresh hold forces working", func(t *testing.T) {
		d := &SessionDetector{compactPending: map[string]int64{sid: 1000}}
		m := &session.SessionMetrics{LastEventType: "turn_done"}
		d.applyCompactHold(sid, m, 1000+timeout-1) // just inside the window
		assertCompactInProgress(t, m, true, "CompactInProgress must be set while the hold is live")
		assertCompactPending(t, d, compactPendingWant{SessionID: sid, Present: true, Msg: "hold must persist until boundary or timeout"})
	})

	t.Run("boundary clears the hold without forcing working", func(t *testing.T) {
		d := &SessionDetector{compactPending: map[string]int64{sid: 1000}}
		m := &session.SessionMetrics{LastEventType: "turn_done", SawManualCompactBoundary: true}
		d.applyCompactHold(sid, m, 1000+1)
		assertCompactInProgress(t, m, false, "CompactInProgress must NOT be set the pass the boundary lands (release → ready)")
		assertCompactPending(t, d, compactPendingWant{SessionID: sid, Present: false, Msg: "boundary must clear the hold"})
	})

	t.Run("timeout drops an orphaned hold (interrupted /compact, no boundary)", func(t *testing.T) {
		d := &SessionDetector{compactPending: map[string]int64{sid: 1000}}
		m := &session.SessionMetrics{LastEventType: "turn_done"} // no boundary ever arrived
		d.applyCompactHold(sid, m, 1000+timeout)
		assertCompactInProgress(t, m, false, "CompactInProgress must NOT be set after timeout — the session must re-classify, not stay held")
		assertCompactPending(t, d, compactPendingWant{SessionID: sid, Present: false, Msg: "timeout must drop the orphaned hold so it can't be re-armed every tick"})
	})

	t.Run("no pending hold is a no-op", func(t *testing.T) {
		d := &SessionDetector{compactPending: map[string]int64{}}
		m := &session.SessionMetrics{LastEventType: "turn_done", SawManualCompactBoundary: true}
		d.applyCompactHold(sid, m, 5000)
		assertCompactInProgress(t, m, false, "a session with no recorded hold must not be forced working")
	})

	t.Run("nil metrics is a no-op", func(t *testing.T) {
		d := &SessionDetector{compactPending: map[string]int64{sid: 1000}}
		d.applyCompactHold(sid, nil, 9999) // must not panic
		assertCompactPending(t, d, compactPendingWant{SessionID: sid, Present: true, Msg: "nil metrics must leave the hold untouched"})
	})
}

// assertCompactInProgress fails the test if m.CompactInProgress doesn't
// match want.
func assertCompactInProgress(t *testing.T, m *session.SessionMetrics, want bool, msg string) {
	t.Helper()
	if m.CompactInProgress != want {
		t.Fatal(msg)
	}
}

// compactPendingWant bundles assertCompactPending's expected-presence and
// failure-message fields, keeping its parameter list within CodeScene's
// argument-count limit instead of threading each value through individually.
type compactPendingWant struct {
	SessionID string
	Present   bool
	Msg       string
}

// assertCompactPending fails the test if the presence of
// d.compactPending[want.SessionID] doesn't match want.Present.
func assertCompactPending(t *testing.T, d *SessionDetector, want compactPendingWant) {
	t.Helper()
	if _, ok := d.compactPending[want.SessionID]; ok != want.Present {
		t.Fatal(want.Msg)
	}
}
