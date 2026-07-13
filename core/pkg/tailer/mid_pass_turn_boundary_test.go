package tailer

import (
	"irrlicht/core/pkg/capacity"
	"testing"
)

// queuedTurnSplittingTestParser wraps testParser to opt into
// queuedTurnSplitter — mirroring vibe's Parser (the only production adapter
// that implements it) so these tests can exercise the tailer's detection
// mechanism without depending on the vibe package. Every other tailer test
// in this package uses plain testParser via newTestTailer, which does NOT
// implement queuedTurnSplitter, proving the flag stays off for adapters
// that don't opt in (issue #988's whole point — see pi's
// 2-10_mid-turn-message-queued fixture, which intentionally wants a single
// contiguous working span).
type queuedTurnSplittingTestParser struct{ testParser }

func (queuedTurnSplittingTestParser) SplitsQueuedFollowUpTurns() bool { return true }

func newQueuedTurnSplittingTestTailer(path string) *TranscriptTailer {
	t := NewTranscriptTailer(path, &queuedTurnSplittingTestParser{}, "mistral-vibe")
	t.capacityMgr = capacity.NewForTest(testCapacityFixture)
	return t
}

// TestSawMidPassTurnBoundary_QueuedFollowUpInSamePass is the issue #988
// regression: when a queued follow-up turn drains synchronously with no
// observable ready gap (e.g. mistral-vibe's in-memory message queue), a
// single TailAndProcess pass sees turn_done, then a fresh user/assistant
// exchange, then turn_done again. The tailer must flag
// SawMidPassTurnBoundary so the detector can synthesize the missing
// ready→working step instead of collapsing both turns into one span.
func TestSawMidPassTurnBoundary_QueuedFollowUpInSamePass(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1)},
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(2)}, // turn 1 done
		{"type": "user", "timestamp": ts(3)},                               // queued follow-up
		{"type": "assistant", "timestamp": ts(4)},
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(5)}, // turn 2 done
	})

	m, err := newQueuedTurnSplittingTestTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.SawMidPassTurnBoundary {
		t.Error("SawMidPassTurnBoundary: got false, want true (a turn_done was followed by more activity in the same pass)")
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("LastEventType: got %q, want turn_done", m.LastEventType)
	}
}

// TestSawMidPassTurnBoundary_FalseWhenParserDoesNotOptIn is the pi
// regression guard: the exact same collapsed-turn transcript shape must NOT
// flag SawMidPassTurnBoundary for a parser that doesn't implement
// queuedTurnSplitter — e.g. pi's queued follow-up is intentionally the same
// turn (steering input), and its 2-10_mid-turn-message-queued fixture
// asserts a single contiguous working span with no synthetic split.
func TestSawMidPassTurnBoundary_FalseWhenParserDoesNotOptIn(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1)},
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(2)},
		{"type": "user", "timestamp": ts(3)},
		{"type": "assistant", "timestamp": ts(4)},
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(5)},
	})

	m, err := newTestTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.SawMidPassTurnBoundary {
		t.Error("SawMidPassTurnBoundary: got true, want false (testParser doesn't implement queuedTurnSplitter)")
	}
}

// TestSawMidPassTurnBoundary_FalseForSingleTurn is the negative case: even
// for a parser that opts into queuedTurnSplitter, a pass with exactly one
// turn (turn_done as the final event) must not flag a mid-pass boundary —
// this is the ordinary single-turn shape every adapter produces on every
// normal turn.
func TestSawMidPassTurnBoundary_FalseForSingleTurn(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1)},
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(2)},
	})

	m, err := newQueuedTurnSplittingTestTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.SawMidPassTurnBoundary {
		t.Error("SawMidPassTurnBoundary: got true, want false (only one turn_done, and it's the last event)")
	}
}

// TestSawMidPassTurnBoundary_ResetsBetweenPasses verifies the flag is
// per-pass: a boundary flagged in one pass must not leak into the next
// pass's metrics when that next pass has no boundary of its own.
func TestSawMidPassTurnBoundary_ResetsBetweenPasses(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1)},
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(2)},
		{"type": "user", "timestamp": ts(3)},
		{"type": "assistant", "timestamp": ts(4)},
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(5)},
	})

	tailer := newQueuedTurnSplittingTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.SawMidPassTurnBoundary {
		t.Fatal("pass 1: expected SawMidPassTurnBoundary=true")
	}

	appendTranscriptLine(t, path, map[string]interface{}{"type": "assistant", "timestamp": ts(6)})

	m, err = tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.SawMidPassTurnBoundary {
		t.Error("pass 2: expected SawMidPassTurnBoundary=false (this pass has no turn_done at all)")
	}
}
