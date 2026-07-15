package tailer

import (
	"os"
	"sort"
	"testing"
)

// openToolParser emits a tool_use for {"tool_use":"<id>","name":"<n>"} lines,
// so a test can leave tool calls open across a rotation without depending on
// any real adapter's parsing logic. No line ever closes a call: the point is
// what survives a rotation, not pair matching.
type openToolParser struct{}

func (p *openToolParser) ParseLine(raw map[string]interface{}) *ParsedEvent {
	if id, ok := raw["tool_use"].(string); ok {
		name, _ := raw["name"].(string)
		return &ParsedEvent{EventType: "assistant", ToolUses: []ToolUse{{ID: id, Name: name}}}
	}
	return &ParsedEvent{Skip: true}
}

// TestResetAccumulatorsForRotation_ClearsOpenToolCalls is the regression test
// for issue #1088's fourth edge: openToolCalls was missing from
// resetAccumulatorsForRotation's reset list.
//
// Re-reading a rotated file from byte 0 re-inserts every surviving tool_use by
// ID, so the id-keyed map self-corrects for anything the new file still
// contains. What it cannot correct is a call left open in the *pre*-rotation
// content: no surviving line re-inserts or closes it. The pre-fix behaviour
// leaked all three ids below into the post-rotation count (4 instead of 1).
//
// sweepOpenToolCallsOnTurnDone was assumed to self-heal this at the next turn
// end, but it only half-does: it deliberately preserves the surviveTurnDone
// names, which are exactly the user-blocking ones NeedsUserAttention() reads —
// so a stale AskUserQuestion survived every subsequent sweep and pinned the
// session to `waiting` forever. That is why two of the three ids below use
// surviveTurnDone names: no later sweep could have rescued them, so the reset
// is the only thing that can.
func TestResetAccumulatorsForRotation_ClearsOpenToolCalls(t *testing.T) {
	// Pre-rotation: three calls left open — one ordinary, two that survive a
	// turn_done sweep (so the sweep can't be what rescues us).
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"tool_use": "T1", "name": "Bash"},
		{"tool_use": "T2", "name": "AskUserQuestion"},
		{"tool_use": "T3", "name": "Agent"},
	})
	tl := NewTranscriptTailer(path, &openToolParser{}, "test-adapter")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("first TailAndProcess: %v", err)
	}
	if m.OpenToolCallCount != 3 {
		t.Fatalf("pre-rotation OpenToolCallCount = %d, want 3 (test setup)", m.OpenToolCallCount)
	}

	// Rotate: a strictly smaller file whose content shares no tool id with the
	// previous one — a fresh session, exactly what the prior file's open calls
	// must not survive into.
	if err := os.WriteFile(path, []byte(`{"tool_use":"NEW1","name":"Read"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err = tl.TailAndProcess()
	if err != nil {
		t.Fatalf("second (rotated) TailAndProcess: %v", err)
	}
	if m.OpenToolCallCount != 1 {
		t.Errorf("post-rotation OpenToolCallCount = %d, want 1 (only the rotated file's NEW1); pre-rotation calls leaked", m.OpenToolCallCount)
	}
	// The user-visible consequence lives in the domain layer:
	// SessionMetrics.NeedsUserAttention reads LastOpenToolNames and pins the
	// session to `waiting` when a user-blocking tool is open. A leaked
	// AskUserQuestion would therefore strand the session forever, so assert
	// the exact surviving name set, not just the count.
	got := append([]string(nil), m.LastOpenToolNames...)
	sort.Strings(got)
	if len(got) != 1 || got[0] != "Read" {
		t.Errorf("post-rotation LastOpenToolNames = %v, want [Read] — a pre-rotation tool call leaked (AskUserQuestion/Agent survive the turn_done sweep, so nothing else would ever clear them)", got)
	}
}

// rotationResetParser is a minimal TranscriptParser that also implements
// rotationResetter, so tests can observe whether/when the tailer invokes the
// hook without depending on any real adapter's parsing logic.
type rotationResetParser struct {
	resetCalls int
}

func (p *rotationResetParser) ParseLine(raw map[string]interface{}) *ParsedEvent {
	return &ParsedEvent{Skip: true}
}

func (p *rotationResetParser) ResetForRotation() {
	p.resetCalls++
}

// TestResetAccumulatorsForRotation_InvokesParserHook pins the shared seam
// added for issue #1063: when the tailer detects a rotated/truncated
// transcript (fileSize < lastOffset), it must call ResetForRotation on any
// parser that implements the optional rotationResetter interface, in
// addition to resetting its own cumulative accumulators. A normal
// (non-rotated) pass must NOT call it.
func TestResetAccumulatorsForRotation_InvokesParserHook(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"line": 1},
		{"line": 2},
		{"line": 3},
	})
	p := &rotationResetParser{}
	tl := NewTranscriptTailer(path, p, "test-adapter")

	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("first TailAndProcess: %v", err)
	}
	if p.resetCalls != 0 {
		t.Fatalf("resetCalls = %d after a normal pass, want 0 (no rotation occurred)", p.resetCalls)
	}

	// Simulate rotation: replace the transcript with a strictly smaller file
	// so fileSize < tl.lastOffset on the next pass.
	if err := os.WriteFile(path, []byte(`{"line":1}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("second (rotated) TailAndProcess: %v", err)
	}
	if p.resetCalls != 1 {
		t.Fatalf("resetCalls = %d after a rotated pass, want 1", p.resetCalls)
	}
}

// TestResetAccumulatorsForRotation_NoOpForNonImplementingParser pins that the
// rotationResetter type-assertion is a harmless no-op (no panic, no special
// handling required) for the common case of a parser that doesn't implement
// it — every adapter except mistral-vibe as of issue #1063.
func TestResetAccumulatorsForRotation_NoOpForNonImplementingParser(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1)},
	})
	tl := newTestTailer(path)
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("first TailAndProcess: %v", err)
	}

	// Rotate to a smaller file; must not panic even though testParser
	// implements no reset hook.
	if err := os.WriteFile(path, []byte(`{"type":"user","timestamp":"`+ts(0)+`"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("second (rotated) TailAndProcess: %v", err)
	}
}
