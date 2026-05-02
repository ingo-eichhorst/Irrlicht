package tailer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/pkg/capacity"
)

// idleFakeParser is a raw-line parser that implements idleFlusher. Used to
// pin the contract between TranscriptTailer.TailAndProcess and the
// idleFlusher hook without going through aider's real parser.
type idleFakeParser struct {
	openOnLine     string                          // line content that flips turnOpen=true
	turnOpen       bool
	idleCalls      []time.Duration                 // every IdleFlush(idleFor) the tailer makes
	flushAtOrAbove time.Duration                   // synthesize turn_done once idleFor crosses this
	flushedEvent   func() *ParsedEvent             // shape of the synthesized event (defaults to bare turn_done)
}

func (p *idleFakeParser) ParseLine(_ map[string]interface{}) *ParsedEvent {
	return nil
}

func (p *idleFakeParser) ParseLineRaw(line string) *ParsedEvent {
	if line == p.openOnLine {
		p.turnOpen = true
		return &ParsedEvent{EventType: "user_message", AssistantText: line}
	}
	return nil
}

func (p *idleFakeParser) IdleFlush(idleFor time.Duration) *ParsedEvent {
	p.idleCalls = append(p.idleCalls, idleFor)
	if !p.turnOpen {
		return nil
	}
	if idleFor < p.flushAtOrAbove {
		return nil
	}
	p.turnOpen = false
	if p.flushedEvent != nil {
		return p.flushedEvent()
	}
	return &ParsedEvent{EventType: "turn_done"}
}

func writeRawLines(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.md")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func newIdleFakeTailer(path string, parser *idleFakeParser) *TranscriptTailer {
	tl := NewTranscriptTailer(path, parser, "test-idle")
	tl.capacityMgr = capacity.NewForTest(testCapacityFixture)
	return tl
}

// TestTailer_IdleFlush_CalledAfterScanWithElapsedTime pins that the tailer
// calls IdleFlush once per TailAndProcess pass after the scan loop, and that
// the duration argument reflects time.Since(lastLineSeenAt).
func TestTailer_IdleFlush_CalledAfterScanWithElapsedTime(t *testing.T) {
	path := writeRawLines(t, []string{"open line"})
	parser := &idleFakeParser{openOnLine: "open line", flushAtOrAbove: 24 * time.Hour}
	tl := newIdleFakeTailer(path, parser)

	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if len(parser.idleCalls) != 1 {
		t.Fatalf("expected 1 IdleFlush call after scan, got %d", len(parser.idleCalls))
	}
	if parser.idleCalls[0] < 0 || parser.idleCalls[0] > time.Second {
		t.Errorf("first idle window should be near zero (line was just read), got %v", parser.idleCalls[0])
	}

	// Second pass with no new lines: idle window should grow.
	time.Sleep(20 * time.Millisecond)
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if len(parser.idleCalls) != 2 {
		t.Fatalf("expected a second IdleFlush call on the no-new-lines pass, got %d", len(parser.idleCalls))
	}
	if parser.idleCalls[1] < 20*time.Millisecond {
		t.Errorf("second idle window should reflect ~20ms gap, got %v", parser.idleCalls[1])
	}
}

// TestTailer_IdleFlush_NotCalledWhenNoLineEverSeen pins that the hook is
// gated on lastLineSeenAt being non-zero. A tailer pointed at an empty file
// must not invoke the parser's idle hook (the elapsed time would be
// meaningless — there's no anchor).
func TestTailer_IdleFlush_NotCalledWhenNoLineEverSeen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	parser := &idleFakeParser{openOnLine: "<unreachable>", flushAtOrAbove: 24 * time.Hour}
	tl := newIdleFakeTailer(path, parser)

	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if len(parser.idleCalls) != 0 {
		t.Errorf("expected 0 IdleFlush calls on empty transcript, got %d", len(parser.idleCalls))
	}
}

// TestTailer_IdleFlush_SynthesizedEventRunsThroughPipeline pins that when
// the parser's IdleFlush returns an event, the tailer routes it through
// processParsedEvent: LastEventType updates, openToolCalls drains via the
// turn_done sweep, and downstream metrics (HasOpenToolCall) reflect the
// post-flush state.
func TestTailer_IdleFlush_SynthesizedEventRunsThroughPipeline(t *testing.T) {
	path := writeRawLines(t, []string{"open line"})
	parser := &idleFakeParser{openOnLine: "open line", flushAtOrAbove: 0}
	tl := newIdleFakeTailer(path, parser)

	// Pre-seed an open tool call that should be swept by the synthesized
	// turn_done. (Not a tool that surviveTurnDone — a regular Bash entry.)
	tl.openToolCalls["t-1"] = "Bash"

	metrics, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if metrics.LastEventType != "turn_done" {
		t.Errorf("expected LastEventType=turn_done after idle flush, got %q", metrics.LastEventType)
	}
	if metrics.HasOpenToolCall {
		t.Errorf("turn_done sweep must clear non-surviving tools; got HasOpenToolCall=true (open=%v)", tl.openToolCalls)
	}
}

// TestTailer_IdleFlush_LastLineSeenAtClearedOnRotation pins that file
// rotation drops the pre-rotation idle anchor. Otherwise the next pass
// would compute time.Since(stale) and synthesize a phantom turn_done
// against state that no longer exists.
func TestTailer_IdleFlush_LastLineSeenAtClearedOnRotation(t *testing.T) {
	path := writeRawLines(t, []string{"open line"})
	parser := &idleFakeParser{openOnLine: "open line", flushAtOrAbove: 24 * time.Hour}
	tl := newIdleFakeTailer(path, parser)

	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("first TailAndProcess: %v", err)
	}
	if tl.lastLineSeenAt.IsZero() {
		t.Fatal("lastLineSeenAt should be set after reading a line")
	}

	// Truncate to simulate rotation: write a smaller file with no content
	// the parser cares about.
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reset idle-call tracking so the assertion below isolates the
	// post-rotation pass. The rotation pass itself will still record an
	// IdleFlush call (gated on !lastLineSeenAt.IsZero before reset, but
	// the rotation block runs before the scan loop and zeros the anchor).
	parser.idleCalls = nil

	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("post-rotation TailAndProcess: %v", err)
	}
	if !tl.lastLineSeenAt.IsZero() {
		t.Errorf("rotation must reset lastLineSeenAt to zero, got %v", tl.lastLineSeenAt)
	}
	if len(parser.idleCalls) != 0 {
		t.Errorf("post-rotation pass with no new lines must not call IdleFlush (anchor is zero), got %d calls", len(parser.idleCalls))
	}
}

// TestTailer_FlushIdle_NoOpWithoutIdleFlusher pins that calling FlushIdle on
// a tailer whose parser doesn't implement idleFlusher returns flushed=false
// and leaves metrics unchanged. Other adapters (claudecode/codex/pi) rely
// on this no-op behavior.
func TestTailer_FlushIdle_NoOpWithoutIdleFlusher(t *testing.T) {
	tl := newTestTailer(filepath.Join(t.TempDir(), "transcript.jsonl"))
	if err := os.WriteFile(tl.path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	_, flushed := tl.FlushIdle()
	if flushed {
		t.Error("FlushIdle on a parser without idleFlusher should return flushed=false")
	}
}
