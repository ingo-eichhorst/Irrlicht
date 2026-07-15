package tailer

import (
	"os"
	"testing"
)

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
