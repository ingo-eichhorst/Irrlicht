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

// --- LastAssistantText extraction tests ---

func TestLastAssistantText_ClaudeCode(t *testing.T) {
	// Claude Code format: type="assistant", message.content[].type="text"
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Should I proceed with the migration?"},
			},
		}},
		{"type": "system", "subtype": "stop_hook_summary", "timestamp": ts(2)},
	})
	m, err := NewTranscriptTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastAssistantText != "Should I proceed with the migration?" {
		t.Errorf("LastAssistantText = %q, want question text", m.LastAssistantText)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("LastEventType = %q, want turn_done", m.LastEventType)
	}
}

func TestLastAssistantText_Codex(t *testing.T) {
	// Codex format: type="message" role="assistant", content[].type="output_text"
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "message", "role": "user", "timestamp": ts(0),
			"content": []interface{}{
				map[string]interface{}{"type": "input_text", "text": "hello"},
			}},
		{"type": "message", "role": "assistant", "timestamp": ts(1),
			"content": []interface{}{
				map[string]interface{}{"type": "output_text", "text": "What would you like to do with these files?"},
			}},
	})
	m, err := NewTranscriptTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastAssistantText != "What would you like to do with these files?" {
		t.Errorf("LastAssistantText = %q, want question text", m.LastAssistantText)
	}
}

func TestLastAssistantText_ClearedOnUserMessage(t *testing.T) {
	// Assistant text should be cleared when a new user message arrives.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "assistant", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Should I continue?"},
			},
		}},
		{"type": "user", "timestamp": ts(1)},
		{"type": "assistant", "timestamp": ts(2), "message": map[string]interface{}{
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Done."},
			},
		}},
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(3)},
	})
	m, err := NewTranscriptTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastAssistantText != "Done." {
		t.Errorf("LastAssistantText = %q, want 'Done.' (previous question should be cleared)", m.LastAssistantText)
	}
}

// --- Local command skip tests ---

func TestLocalCommandsSkipped(t *testing.T) {
	// Local commands (shell escapes, /context) should not affect LastEventType.
	// After turn_done, local command events should leave LastEventType as "turn_done".
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "assistant", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "assistant", "content": []interface{}{
				map[string]interface{}{"type": "text", "text": "done"},
			},
		}},
		{"type": "system", "subtype": "stop_hook_summary", "timestamp": ts(1)},
		// Local command: isMeta caveat
		{"type": "user", "isMeta": true, "timestamp": ts(2), "message": map[string]interface{}{
			"role": "user", "content": "<local-command-caveat>The messages below were generated by the user while running local commands.</local-command-caveat>",
		}},
		// Local command: command name
		{"type": "user", "timestamp": ts(3), "message": map[string]interface{}{
			"role": "user", "content": "<command-name>/context</command-name>",
		}},
		// Local command: stdout
		{"type": "user", "timestamp": ts(4), "message": map[string]interface{}{
			"role": "user", "content": "<local-command-stdout>Context Usage\n</local-command-stdout>",
		}},
		// Shell escape: bash-input
		{"type": "user", "timestamp": ts(5), "message": map[string]interface{}{
			"role": "user", "content": "<bash-input>ls</bash-input>",
		}},
		// Shell escape: bash-stdout
		{"type": "user", "timestamp": ts(6), "message": map[string]interface{}{
			"role": "user", "content": "<bash-stdout>file1\nfile2\n</bash-stdout>",
		}},
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("expected LastEventType=%q after local commands, got %q", "turn_done", m.LastEventType)
	}
}

func TestPiBashExecutionSkipped(t *testing.T) {
	// Pi bashExecution events (! shell escapes) should be skipped.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "session", "version": float64(3), "cwd": "/tmp"},
		{"type": "message", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "assistant", "stopReason": "stop",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Hi!"},
			},
		}},
		// Pi shell escape
		{"type": "message", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "bashExecution",
		}},
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "assistant_message" {
		t.Errorf("expected LastEventType=%q after Pi bashExecution, got %q", "assistant_message", m.LastEventType)
	}
}

func TestLocalCommandsDoNotAffectNormalUserMessage(t *testing.T) {
	// A normal user message after local commands should still set LastEventType.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(0)},
		// Local command
		{"type": "user", "isMeta": true, "timestamp": ts(1), "message": map[string]interface{}{
			"role": "user", "content": "<local-command-caveat>caveat</local-command-caveat>",
		}},
		// Normal user message (should NOT be skipped)
		{"type": "user", "timestamp": ts(2), "message": map[string]interface{}{
			"role": "user", "content": "hello",
		}},
	})

	tailer := NewTranscriptTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "user" {
		t.Errorf("expected LastEventType=%q for normal user message, got %q", "user", m.LastEventType)
	}
}
