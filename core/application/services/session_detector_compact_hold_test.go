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
		assertCompactPending(t, d, sid, true, "hold must persist until boundary or timeout")
	})

	t.Run("boundary clears the hold without forcing working", func(t *testing.T) {
		d := &SessionDetector{compactPending: map[string]int64{sid: 1000}}
		m := &session.SessionMetrics{LastEventType: "turn_done", SawManualCompactBoundary: true}
		d.applyCompactHold(sid, m, 1000+1)
		assertCompactInProgress(t, m, false, "CompactInProgress must NOT be set the pass the boundary lands (release → ready)")
		assertCompactPending(t, d, sid, false, "boundary must clear the hold")
	})

	t.Run("timeout drops an orphaned hold (interrupted /compact, no boundary)", func(t *testing.T) {
		d := &SessionDetector{compactPending: map[string]int64{sid: 1000}}
		m := &session.SessionMetrics{LastEventType: "turn_done"} // no boundary ever arrived
		d.applyCompactHold(sid, m, 1000+timeout)
		assertCompactInProgress(t, m, false, "CompactInProgress must NOT be set after timeout — the session must re-classify, not stay held")
		assertCompactPending(t, d, sid, false, "timeout must drop the orphaned hold so it can't be re-armed every tick")
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
		assertCompactPending(t, d, sid, true, "nil metrics must leave the hold untouched")
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

// assertCompactPending fails the test if the presence of d.compactPending[sid]
// doesn't match wantPresent.
func assertCompactPending(t *testing.T, d *SessionDetector, sid string, wantPresent bool, msg string) {
	t.Helper()
	if _, ok := d.compactPending[sid]; ok != wantPresent {
		t.Fatal(msg)
	}
}
