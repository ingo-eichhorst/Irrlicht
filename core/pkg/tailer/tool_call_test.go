package tailer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTranscriptLines writes JSONL entries to a temp file and returns the path.
func writeTranscriptLines(t *testing.T, lines []map[string]interface{}) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, line := range lines {
		if err := enc.Encode(line); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func ts(offset int) string {
	return time.Now().Add(time.Duration(offset) * time.Second).Format(time.RFC3339)
}

func TestHasOpenToolCall_NoToolEvents(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1)},
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false with no tool events")
	}
	if m.OpenToolCallCount != 0 {
		t.Errorf("expected OpenToolCallCount=0, got %d", m.OpenToolCallCount)
	}
}

func TestHasOpenToolCall_SinglePairedToolCall(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "tool_use", "timestamp": ts(1)},
		{"type": "tool_result", "timestamp": ts(2)},
		{"type": "assistant", "timestamp": ts(3)},
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false when tool_use is paired with tool_result")
	}
	if m.OpenToolCallCount != 0 {
		t.Errorf("expected OpenToolCallCount=0, got %d", m.OpenToolCallCount)
	}
}

func TestHasOpenToolCall_OneOpenToolCall(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "tool_use", "timestamp": ts(1)},
		// No matching tool_result
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true with unmatched tool_use")
	}
	if m.OpenToolCallCount != 1 {
		t.Errorf("expected OpenToolCallCount=1, got %d", m.OpenToolCallCount)
	}
}

func TestHasOpenToolCall_ParallelToolCalls(t *testing.T) {
	// Simulate 3 parallel tool_use events, only 1 tool_result so far
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "tool_use", "timestamp": ts(0)},
		{"type": "tool_use", "timestamp": ts(1)},
		{"type": "tool_use", "timestamp": ts(2)},
		{"type": "tool_result", "timestamp": ts(3)},
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true with 2 unmatched tool_use events")
	}
	if m.OpenToolCallCount != 2 {
		t.Errorf("expected OpenToolCallCount=2, got %d", m.OpenToolCallCount)
	}
}

func TestHasOpenToolCall_ToolCallEventType(t *testing.T) {
	// The "tool_call" event type (legacy format) should also be counted
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"tool_call": map[string]interface{}{"name": "Bash"}, "timestamp": ts(1)},
		// No matching tool_result
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true with unmatched tool_call")
	}
	if m.OpenToolCallCount != 1 {
		t.Errorf("expected OpenToolCallCount=1, got %d", m.OpenToolCallCount)
	}
}

func TestHasOpenToolCall_ExtraResultsClamped(t *testing.T) {
	// If we start reading mid-stream, we might see more tool_results than
	// tool_use events. The count should be clamped to 0.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "tool_result", "timestamp": ts(0)},
		{"type": "tool_result", "timestamp": ts(1)},
		{"type": "assistant", "timestamp": ts(2)},
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false when results exceed uses")
	}
	if m.OpenToolCallCount != 0 {
		t.Errorf("expected OpenToolCallCount=0, got %d", m.OpenToolCallCount)
	}
}

func TestHasOpenToolCall_MultipleRoundsAllClosed(t *testing.T) {
	// Multiple tool use/result rounds, all closed
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "tool_use", "timestamp": ts(0)},
		{"type": "tool_result", "timestamp": ts(1)},
		{"type": "tool_use", "timestamp": ts(2)},
		{"type": "tool_result", "timestamp": ts(3)},
		{"type": "tool_use", "timestamp": ts(4)},
		{"type": "tool_result", "timestamp": ts(5)},
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false when all tool calls are paired")
	}
	if m.OpenToolCallCount != 0 {
		t.Errorf("expected OpenToolCallCount=0, got %d", m.OpenToolCallCount)
	}
}
