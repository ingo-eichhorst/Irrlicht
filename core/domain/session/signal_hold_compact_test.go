package session

import (
	"testing"
	"time"
)

// TestSignalHolds_CompactInProgress covers the PreCompact force-working hold
// (#657) as a signalPolicies row — the migration #1297 performed. It is the
// same three-way lifecycle the detector's own applyCompactHold carried before:
// the hold forces working and keeps doing so across the silent window, the
// manual compact_boundary releases it (#656), and an interrupted /compact that
// never writes a boundary is dropped once compactHoldTimeout elapses instead of
// stranding the session in working forever.
//
// These cases are LOCKS on behaviour that must not change across the
// migration: the pre-#1297 code satisfied every one of them through a
// different mechanism, so they pass by construction and none of them is
// evidence of a fixed defect. Their value is that they can fail, which was
// demonstrated by mutating the policy three ways:
//
//   - >= relaxed to >: "timeout drops an orphaned hold" fails — the hold
//     survives exactly at the deadline;
//   - the clock term deleted (the predicate this row could express before
//     holdContext existed): the same case fails, an orphaned hold now living
//     forever;
//   - compactHoldTimeout shortened to 1m: "persists across repeated passes"
//     fails at pass 2 — a live compaction dropped mid-window.
//
// The first two catch a hold that outlives its window, the third one that dies
// inside it. "just inside the window" deliberately measures against the
// constant rather than a literal, so it pins the comparison and not the value.
func TestSignalHolds_CompactInProgress(t *testing.T) {
	t.Run("persists across repeated passes in the silent window", func(t *testing.T) {
		h := NewSignalHolds()
		h.Hold(holdSID, SignalCompactInProgress, SignalPayload{}, holdT0)

		// A manual /compact writes nothing for tens of seconds to minutes;
		// every re-evaluation in that window must re-apply the hold, or the
		// stale pre-compact turn_done leaks the session back to ready.
		for pass := 1; pass <= 3; pass++ {
			m := &SessionMetrics{LastEventType: "turn_done"}
			h.Overlay(holdSID, m, holdT0.Add(time.Duration(pass)*30*time.Second))
			if !m.CompactInProgress {
				t.Fatalf("pass %d: the hold must re-apply — it is persistent, not consume-once", pass)
			}
			if !h.Held(holdSID, SignalCompactInProgress) {
				t.Fatalf("pass %d: the hold must survive until boundary or timeout", pass)
			}
		}
	})

	t.Run("boundary clears the hold without forcing working", func(t *testing.T) {
		h := NewSignalHolds()
		h.Hold(holdSID, SignalCompactInProgress, SignalPayload{}, holdT0)

		m := &SessionMetrics{LastEventType: "turn_done", SawManualCompactBoundary: true}
		h.Overlay(holdSID, m, holdT0.Add(time.Second))

		if m.CompactInProgress {
			t.Error("CompactInProgress must NOT be set the pass the boundary lands (release → ready)")
		}
		if h.Held(holdSID, SignalCompactInProgress) {
			t.Error("the boundary must clear the hold")
		}
	})

	t.Run("just inside the window still holds", func(t *testing.T) {
		h := NewSignalHolds()
		h.Hold(holdSID, SignalCompactInProgress, SignalPayload{}, holdT0)

		m := &SessionMetrics{LastEventType: "turn_done"}
		h.Overlay(holdSID, m, holdT0.Add(compactHoldTimeout-time.Nanosecond))

		if !m.CompactInProgress {
			t.Error("a hold one tick short of the timeout must still force working")
		}
	})

	t.Run("timeout drops an orphaned hold (interrupted /compact, no boundary)", func(t *testing.T) {
		h := NewSignalHolds()
		h.Hold(holdSID, SignalCompactInProgress, SignalPayload{}, holdT0)

		m := &SessionMetrics{LastEventType: "turn_done"} // no boundary ever arrived
		h.Overlay(holdSID, m, holdT0.Add(compactHoldTimeout))

		if m.CompactInProgress {
			t.Error("CompactInProgress must NOT be set after the timeout — the session must re-classify, not stay held")
		}
		if h.Held(holdSID, SignalCompactInProgress) {
			t.Error("the timeout must drop the orphaned hold so it can't be re-armed on every refreshStaleSessions tick")
		}
	})

	t.Run("nil metrics leaves the hold intact", func(t *testing.T) {
		h := NewSignalHolds()
		h.Hold(holdSID, SignalCompactInProgress, SignalPayload{}, holdT0)

		h.Overlay(holdSID, nil, holdT0.Add(2*compactHoldTimeout)) // must not panic

		if !h.Held(holdSID, SignalCompactInProgress) {
			t.Error("a pass with no metrics must leave the hold for a later one, not silently expire it")
		}
	})
}

// TestSignalHolds_ReHoldRestartsTheTimeout pins the consequence of Hold
// replacing the arrival time along with the payload: a second PreCompact hook —
// a user running /compact again after an interrupted one — restarts its own
// safety-net window rather than inheriting the first hook's, which would expire
// the fresh compaction early.
//
// This is the one behaviour that only becomes expressible once heldAt exists,
// so it is not a lock: before #1297 the detector's map stored an int64 that the
// same re-arm overwrote, and no test looked.
func TestSignalHolds_ReHoldRestartsTheTimeout(t *testing.T) {
	h := NewSignalHolds()
	h.Hold(holdSID, SignalCompactInProgress, SignalPayload{}, holdT0)

	// Four minutes in — inside the first window — the user compacts again.
	reheld := holdT0.Add(4 * time.Minute)
	h.Hold(holdSID, SignalCompactInProgress, SignalPayload{}, reheld)

	// A pass that would have timed out the ORIGINAL hold must still hold,
	// because the clock now runs from the re-arm.
	m := &SessionMetrics{LastEventType: "turn_done"}
	h.Overlay(holdSID, m, holdT0.Add(compactHoldTimeout+time.Second))
	if !m.CompactInProgress {
		t.Error("re-holding must restart the timeout, not inherit the first hook's deadline")
	}

	// It still expires on its own schedule.
	expired := &SessionMetrics{LastEventType: "turn_done"}
	h.Overlay(holdSID, expired, reheld.Add(compactHoldTimeout))
	if expired.CompactInProgress {
		t.Error("the re-armed hold must still expire once its own window elapses")
	}
}
