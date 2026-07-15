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
// hook without depending on any real adapter's parsing logic. parsedLines
// counts ParseLine calls, which lets a test distinguish a full re-read from
// byte 0 from a resume at a stale offset.
type rotationResetParser struct {
	resetCalls  int
	parsedLines int
}

func (p *rotationResetParser) ParseLine(raw map[string]interface{}) *ParsedEvent {
	p.parsedLines++
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

// TestTailAndProcess_InPlaceRewriteWithGrowthIsRotation is the regression test
// for issue #1104. The tailer used to detect rotation ONLY by the file
// shrinking (fileSize < lastOffset). An in-place rewrite whose new size is
// >= lastOffset slipped through: the tailer seeked to a stale byte offset
// inside content that had since changed, resumed mid-record, and kept
// accumulating on top of pre-rewrite state — silently wrong tokens/cost and
// missed messages, the same failure class #1063/#1078 fixed for the shrink
// path.
//
// The rewrite here both changes the already-consumed bytes AND grows the file,
// which is the exact shape that escaped detection (vibe's fingerprint-mismatch
// rewrite; a truncate-then-append coalesced into one debounced pass).
func TestTailAndProcess_InPlaceRewriteWithGrowthIsRotation(t *testing.T) {
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
	consumed := tl.lastOffset
	p.parsedLines = 0

	// Rewrite in place: different content in the already-consumed region, and
	// a net-larger file, so the historical fileSize < lastOffset test cannot
	// see it.
	rewritten := []byte(`{"line":11}` + "\n" + `{"line":22}` + "\n" + `{"line":33}` + "\n" + `{"line":44}` + "\n")
	if err := os.WriteFile(path, rewritten, 0o644); err != nil {
		t.Fatal(err)
	}
	if int64(len(rewritten)) < consumed {
		t.Fatalf("test setup is wrong: rewritten file (%d bytes) is smaller than the consumed offset (%d) — "+
			"the shrink check would catch it and the #1104 gap wouldn't be exercised", len(rewritten), consumed)
	}
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("second (rewritten) TailAndProcess: %v", err)
	}

	if p.resetCalls != 1 {
		t.Errorf("resetCalls = %d after an in-place rewrite that grew the file, want 1: "+
			"the rewrite went undetected and stale accumulators carried over", p.resetCalls)
	}
	if p.parsedLines != 4 {
		t.Errorf("parsedLines = %d on the pass after the rewrite, want 4 (a full re-read from byte 0): "+
			"the tailer resumed at the stale offset %d and read only part of the rewritten file", p.parsedLines, consumed)
	}
}

// TestTailAndProcess_AppendIsNotRotation pins the other side of issue #1104's
// resume check: an ordinary append must stay on the cheap incremental path.
// Detecting a rewrite by re-hashing the consumed bytes is only sound because an
// append never rewrites bytes below lastOffset — a false positive here would
// re-read and re-reset the world on every single transcript write.
func TestTailAndProcess_AppendIsNotRotation(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"line": 1},
		{"line": 2},
	})
	p := &rotationResetParser{}
	tl := NewTranscriptTailer(path, p, "test-adapter")

	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("first TailAndProcess: %v", err)
	}
	p.parsedLines = 0

	appendTranscriptLine(t, path, map[string]interface{}{"line": 3})
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("second (appended) TailAndProcess: %v", err)
	}

	if p.resetCalls != 0 {
		t.Errorf("resetCalls = %d after a plain append, want 0 (an append is not a rotation)", p.resetCalls)
	}
	if p.parsedLines != 1 {
		t.Errorf("parsedLines = %d after a plain append, want 1 (only the appended line): "+
			"the tailer re-read content it had already consumed", p.parsedLines)
	}
}

// TestTailAndProcess_IdenticalRewriteIsNotRotation pins the benign case issue
// #1104 called out explicitly: a rewrite that reproduces the same bytes (a
// legacy no-fingerprint full rewrite of unchanged content) leaves the consumed
// prefix intact, so there is nothing to re-read and no reason to reset.
func TestTailAndProcess_IdenticalRewriteIsNotRotation(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"line": 1},
		{"line": 2},
	})
	p := &rotationResetParser{}
	tl := NewTranscriptTailer(path, p, "test-adapter")

	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("first TailAndProcess: %v", err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p.parsedLines = 0

	// Same path, same bytes, new write.
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("second (identical rewrite) TailAndProcess: %v", err)
	}

	if p.resetCalls != 0 {
		t.Errorf("resetCalls = %d after a byte-identical rewrite, want 0 (content never changed)", p.resetCalls)
	}
	if p.parsedLines != 0 {
		t.Errorf("parsedLines = %d after a byte-identical rewrite, want 0 (offset already at EOF)", p.parsedLines)
	}
}

// TestSetLedgerState_DetectsRewriteAcrossRestart pins that the resume
// fingerprint survives a daemon restart (issue #1104). The ledger persists
// lastOffset, so without a persisted fingerprint a rewrite landing while the
// daemon is DOWN would be trusted blindly on the next boot — the restart is
// precisely when the tailer has no in-memory memory of what it consumed.
func TestSetLedgerState_DetectsRewriteAcrossRestart(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"line": 1},
		{"line": 2},
		{"line": 3},
	})
	before := NewTranscriptTailer(path, &rotationResetParser{}, "test-adapter")
	if _, err := before.TailAndProcess(); err != nil {
		t.Fatalf("pre-restart TailAndProcess: %v", err)
	}
	ledger := before.GetLedgerState()
	if ledger.ResumeFingerprint == 0 {
		t.Fatalf("ledger carries no ResumeFingerprint after a pass that consumed %d bytes; "+
			"a restart would have nothing to validate the offset against", ledger.LastOffset)
	}

	// The transcript is rewritten in place (bigger, different) while "down".
	if err := os.WriteFile(path, []byte(`{"line":11}`+"\n"+`{"line":22}`+"\n"+`{"line":33}`+"\n"+`{"line":44}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &rotationResetParser{}
	after := NewTranscriptTailer(path, p, "test-adapter")
	after.SetLedgerState(ledger)
	if _, err := after.TailAndProcess(); err != nil {
		t.Fatalf("post-restart TailAndProcess: %v", err)
	}

	if p.resetCalls != 1 {
		t.Errorf("resetCalls = %d on the first post-restart pass, want 1: the rehydrated offset was "+
			"trusted even though the file had been rewritten underneath it", p.resetCalls)
	}
	if p.parsedLines != 4 {
		t.Errorf("parsedLines = %d on the first post-restart pass, want 4 (a full re-read from byte 0)", p.parsedLines)
	}
}

// TestSetLedgerState_PreFingerprintLedgerResumesAtOffset pins the backward
// compatibility that let #1104's fingerprint land WITHOUT a LedgerSchemaVersion
// bump: a ledger written before the field existed carries an offset and no
// fingerprint, and must resume exactly as it always did rather than being
// treated as a rewrite. Bumping the schema instead would discard every live
// session's accumulated cost to re-scan for a latent gap.
func TestSetLedgerState_PreFingerprintLedgerResumesAtOffset(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"line": 1},
		{"line": 2},
	})
	seed := NewTranscriptTailer(path, &rotationResetParser{}, "test-adapter")
	if _, err := seed.TailAndProcess(); err != nil {
		t.Fatalf("seed TailAndProcess: %v", err)
	}

	// A pre-#1104 ledger: offset only, no fingerprint.
	legacy := LedgerState{SchemaVersion: LedgerSchemaVersion, LastOffset: seed.lastOffset}

	p := &rotationResetParser{}
	tl := NewTranscriptTailer(path, p, "test-adapter")
	tl.SetLedgerState(legacy)
	appendTranscriptLine(t, path, map[string]interface{}{"line": 3})
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("TailAndProcess on a legacy ledger: %v", err)
	}

	if p.resetCalls != 0 {
		t.Errorf("resetCalls = %d resuming a pre-#1104 ledger, want 0: a missing fingerprint means "+
			"'can't tell', which must fall back to trusting the offset, not to a spurious rotation", p.resetCalls)
	}
	if p.parsedLines != 1 {
		t.Errorf("parsedLines = %d resuming a pre-#1104 ledger, want 1 (only the newly appended line)", p.parsedLines)
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
