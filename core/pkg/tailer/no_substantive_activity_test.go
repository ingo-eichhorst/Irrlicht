package tailer

import (
	"testing"
)

// TestNoSubstantiveActivity_AwaySummaryAfterTurnDone is the issue #329
// regression: when Claude Code writes a post-turn `system/away_summary`
// recap, the tailer must surface NoSubstantiveActivity=true on the second
// pass and leave LastEventType pointing at the prior turn_done. Without
// this, the detector's force-bounce would flip the ready session back to
// working.
func TestNoSubstantiveActivity_AwaySummaryAfterTurnDone(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1)},
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(2)},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "turn_done" {
		t.Fatalf("pass 1 LastEventType: got %q, want turn_done", m.LastEventType)
	}
	if m.NoSubstantiveActivity {
		t.Fatalf("pass 1 NoSubstantiveActivity: got true, want false (turn_done is substantive)")
	}

	appendTranscriptLine(t, path, map[string]interface{}{
		"type":      "system",
		"subtype":   "away_summary",
		"timestamp": ts(180),
	})

	m, err = tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.NoSubstantiveActivity {
		t.Errorf("pass 2 NoSubstantiveActivity: got false, want true (away_summary is skipped)")
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("pass 2 LastEventType: got %q, want turn_done (must not be overwritten by skipped event)", m.LastEventType)
	}
}

// TestNoSubstantiveActivity_ResetsBetweenPasses verifies the flag is
// per-pass: a second pass that processes a real event must clear the
// flag set by a prior skip-only pass.
func TestNoSubstantiveActivity_ResetsBetweenPasses(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "system", "subtype": "away_summary", "timestamp": ts(0)},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.NoSubstantiveActivity {
		t.Fatalf("pass 1: expected NoSubstantiveActivity=true, got false")
	}

	appendTranscriptLine(t, path, map[string]interface{}{
		"type":      "assistant",
		"timestamp": ts(1),
	})

	m, err = tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.NoSubstantiveActivity {
		t.Errorf("pass 2: expected NoSubstantiveActivity=false after assistant event, got true")
	}
}
