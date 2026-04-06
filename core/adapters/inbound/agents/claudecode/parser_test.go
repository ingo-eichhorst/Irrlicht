package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/pkg/tailer"
)

func TestParser_SystemEvent_TurnDone(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "system",
		"subtype":   "turn_duration",
		"timestamp": "2026-04-05T22:00:00Z",
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want turn_done", ev.EventType)
	}
	if ev.Skip {
		t.Error("turn_done should not be skipped — tailer needs it for LastEventType")
	}
}

func TestParser_SystemEvent_StopHookSummary(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "system",
		"subtype":   "stop_hook_summary",
		"timestamp": "2026-04-05T22:00:00Z",
	})
	if ev == nil || ev.EventType != "turn_done" {
		t.Errorf("expected turn_done for stop_hook_summary, got %+v", ev)
	}
}

func TestParser_IsMeta_Skip(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "user",
		"isMeta":    true,
		"timestamp": "2026-04-05T22:00:00Z",
	})
	if ev == nil || !ev.Skip {
		t.Error("expected isMeta user event to be skipped")
	}
}

func TestParser_LocalCommand_Skip(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "user",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"content": "<local-command-caveat>text</local-command-caveat>",
		},
	})
	if ev == nil || !ev.Skip {
		t.Error("expected local command to be skipped")
	}
}

func TestParser_PermissionMode(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":           "permission-mode",
		"permissionMode": "bypassPermissions",
		"timestamp":      "2026-04-05T22:00:00Z",
	})
	if ev == nil || !ev.Skip {
		t.Error("expected permission-mode to be skipped")
	}
	if ev.PermissionMode != "bypassPermissions" {
		t.Errorf("PermissionMode = %q, want bypassPermissions", ev.PermissionMode)
	}
}

func TestParser_ToolUseInContent(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role":        "assistant",
			"stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Let me check."},
				map[string]interface{}{"type": "tool_use", "name": "Bash", "id": "tu_1"},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if len(ev.ToolUseNames) != 1 || ev.ToolUseNames[0] != "Bash" {
		t.Errorf("ToolUseNames = %v, want [Bash]", ev.ToolUseNames)
	}
}

func TestParser_ToolResultInContent(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "user",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_1", "is_error": false},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.ToolResultCount != 1 {
		t.Errorf("ToolResultCount = %d, want 1", ev.ToolResultCount)
	}
}

func TestParser_AssistantText(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role":        "assistant",
			"stop_reason": "end_turn",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Should I proceed?"},
			},
		},
	})
	if ev.AssistantText != "Should I proceed?" {
		t.Errorf("AssistantText = %q, want question", ev.AssistantText)
	}
}

func TestParser_AssistantStreaming_ThinkingBlock(t *testing.T) {
	// Thinking blocks are always intermediate regardless of ID.
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"id": "msg_new", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "Let me think..."},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "assistant_streaming" {
		t.Errorf("EventType = %q, want assistant_streaming (thinking block)", ev.EventType)
	}
}

func TestParser_AssistantStreaming_SameID(t *testing.T) {
	// Consecutive messages with the same ID are part of one streaming sequence.
	p := &Parser{}

	// First message sets the ID.
	p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"id": "msg_abc", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "analyzing..."},
			},
		},
	})

	// Second message with same ID + null stop_reason → streaming.
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:01Z",
		"message": map[string]interface{}{
			"id": "msg_abc", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Let me check the file..."},
			},
		},
	})
	if ev.EventType != "assistant_streaming" {
		t.Errorf("EventType = %q, want assistant_streaming (same ID)", ev.EventType)
	}
	if ev.AssistantText != "Let me check the file..." {
		t.Errorf("AssistantText = %q, want text (should still extract)", ev.AssistantText)
	}
}

func TestParser_AssistantFinal_NullStopReason_DifferentID(t *testing.T) {
	// A standalone text response with null stop_reason and a new message ID
	// should keep "assistant" (potentially final — like session 891f03e9 L8).
	p := &Parser{}

	// Set a previous ID.
	p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"id": "msg_old", "role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Bash", "id": "tu_1"},
			},
		},
	})

	// New ID + null stop_reason + text → assistant (not streaming).
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:01Z",
		"message": map[string]interface{}{
			"id": "msg_new", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Here are the files."},
			},
		},
	})
	if ev.EventType != "assistant" {
		t.Errorf("EventType = %q, want assistant (different ID, potentially final)", ev.EventType)
	}
}

func TestParser_AssistantFinal_EndTurn(t *testing.T) {
	// Final response with stop_reason=end_turn keeps EventType="assistant".
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role":        "assistant",
			"stop_reason": "end_turn",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Done. All tests pass."},
			},
		},
	})
	if ev.EventType != "assistant" {
		t.Errorf("EventType = %q, want assistant (stop_reason=end_turn)", ev.EventType)
	}
}

func TestParser_AssistantFinal_ToolUse(t *testing.T) {
	// Tool call with stop_reason=tool_use keeps EventType="assistant".
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role":        "assistant",
			"stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Read", "id": "tu_1"},
			},
		},
	})
	if ev.EventType != "assistant" {
		t.Errorf("EventType = %q, want assistant (stop_reason=tool_use)", ev.EventType)
	}
	if len(ev.ToolUseNames) != 1 || ev.ToolUseNames[0] != "Read" {
		t.Errorf("ToolUseNames = %v, want [Read]", ev.ToolUseNames)
	}
}

func TestParser_AssistantStreaming_NullStopReason_SameID(t *testing.T) {
	// stop_reason: null + same message ID as previous = streaming sequence.
	p := &Parser{}

	// First message in sequence
	p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"id": "msg_x", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "hmm"},
			},
		},
	})

	// Same ID, text content → streaming
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:01Z",
		"message": map[string]interface{}{
			"id": "msg_x", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Here are the files."},
			},
		},
	})
	if ev.EventType != "assistant_streaming" {
		t.Errorf("EventType = %q, want assistant_streaming (null + same ID)", ev.EventType)
	}
}

func TestParser_UserInterrupted_SetsIsError(t *testing.T) {
	// ESC during text generation writes "[Request interrupted by user]"
	// as a user text message. Parser should set IsError=true.
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "user",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "[Request interrupted by user]"},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if !ev.IsError {
		t.Error("expected IsError=true for user interruption message")
	}
	if ev.EventType != "user" {
		t.Errorf("EventType = %q, want user", ev.EventType)
	}
}

func TestParser_UserMessage_ClearsToolNames(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "user",
		"timestamp": "2026-04-05T22:00:00Z",
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if !ev.ClearToolNames {
		t.Error("expected ClearToolNames=true for user event")
	}
}

// --- Integration tests: full transcript sequences through tailer ---
//
// These tests simulate real Claude Code transcript patterns observed in
// sessions 8b00212a and a27640c8 that exposed bugs #84.

func ts(offset int) string {
	return time.Now().Add(time.Duration(offset) * time.Second).Format(time.RFC3339)
}

func writeTranscript(t *testing.T, lines []map[string]interface{}) string {
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

func newCCTailer(path string) *tailer.TranscriptTailer {
	return tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
}

// Regression: consecutive assistant messages (thinking + text + tool_use)
// caused false ready because intermediate messages set LastEventType="assistant".
// With stop_reason + message ID check, intermediates use "assistant_streaming".
func TestTranscript_ConsecutiveAssistantMessages_StaysWorking(t *testing.T) {
	// Real pattern from session 8b00212a: thinking → text → tool_use (same API call ID)
	path := writeTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "fix the bug",
		}},
		// Thinking block — intermediate (same ID, thinking content)
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "Let me analyze..."},
			},
		}},
		// Text response — intermediate (same ID as thinking)
		{"type": "assistant", "timestamp": ts(2), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "I'll check the file."},
			},
		}},
	})

	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	// Last event should be "assistant_streaming" (not "assistant")
	if m.LastEventType != "assistant_streaming" {
		t.Errorf("LastEventType = %q, want assistant_streaming (intermediate text)", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false (no tool_use yet)")
	}
}

// The tool_use message with stop_reason="tool_use" should keep "assistant".
func TestTranscript_ToolCallAfterStreaming_Working(t *testing.T) {
	path := writeTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "fix the bug",
		}},
		// Thinking (streaming, same API call msg_1)
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "analyzing..."},
			},
		}},
		// Text (streaming, same API call msg_1)
		{"type": "assistant", "timestamp": ts(2), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Let me read that."},
			},
		}},
		// Tool call with stop_reason — final message of API call msg_1
		{"type": "assistant", "timestamp": ts(3), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Read", "id": "tu_1"},
			},
		}},
	})

	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "assistant" {
		t.Errorf("LastEventType = %q, want assistant", m.LastEventType)
	}
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true")
	}
}

// Full multi-tool turn: tool calls resolve, mid-turn text stays working,
// final end_turn correctly signals done.
func TestTranscript_MultiToolTurn_FullCycle(t *testing.T) {
	path := writeTranscript(t, []map[string]interface{}{
		// User starts turn
		{"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "refactor the module",
		}},
		// Thinking (API call msg_1)
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "planning..."},
			},
		}},
		// First tool call (API call msg_1)
		{"type": "assistant", "timestamp": ts(2), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Let me read the file."},
				map[string]interface{}{"type": "tool_use", "name": "Read", "id": "tu_1"},
			},
		}},
		// Tool result
		{"type": "user", "timestamp": ts(3), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_1", "is_error": false},
			},
		}},
		// Mid-turn text (API call msg_2, different from msg_1)
		{"type": "assistant", "timestamp": ts(4), "message": map[string]interface{}{
			"id": "msg_2", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Now I'll edit it."},
			},
		}},
		// Second tool call (API call msg_2)
		{"type": "assistant", "timestamp": ts(5), "message": map[string]interface{}{
			"id": "msg_2", "role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Edit", "id": "tu_2"},
			},
		}},
		// Tool result
		{"type": "user", "timestamp": ts(6), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_2", "is_error": false},
			},
		}},
		// Final response (API call msg_3)
		{"type": "assistant", "timestamp": ts(7), "message": map[string]interface{}{
			"id": "msg_3", "role": "assistant", "stop_reason": "end_turn",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Done. The refactoring is complete."},
			},
		}},
	})

	tail := newCCTailer(path)

	// Simulate reading only up to the mid-turn text (line 4).
	// In reality the debounce might fire at this point.
	m, err := tail.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	// After the full transcript: last event is end_turn assistant.
	if m.LastEventType != "assistant" {
		t.Errorf("LastEventType = %q, want assistant (stop_reason=end_turn)", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false (all tools resolved)")
	}
	if m.LastAssistantText != "Done. The refactoring is complete." {
		t.Errorf("LastAssistantText = %q", m.LastAssistantText)
	}
}

// Incremental read: first read stops at mid-turn text, second read gets tool_use.
// Verifies that the mid-turn state doesn't trigger false ready.
func TestTranscript_IncrementalRead_MidTurnThenToolUse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	// Write first batch: user → thinking (streaming)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	enc.Encode(map[string]interface{}{
		"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "fix it",
		},
	})
	enc.Encode(map[string]interface{}{
		"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "I'll check..."},
			},
		},
	})
	f.Close()

	tail := newCCTailer(path)

	// First read: mid-turn text only
	m, err := tail.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "assistant_streaming" {
		t.Errorf("first read: LastEventType = %q, want assistant_streaming", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Error("first read: expected HasOpenToolCall=false")
	}

	// Append tool_use (same API call msg_1)
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	enc = json.NewEncoder(f)
	enc.Encode(map[string]interface{}{
		"type": "assistant", "timestamp": ts(2), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Bash", "id": "tu_1"},
			},
		},
	})
	f.Close()

	// Second read: picks up tool_use
	m, err = tail.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "assistant" {
		t.Errorf("second read: LastEventType = %q, want assistant", m.LastEventType)
	}
	if !m.HasOpenToolCall {
		t.Error("second read: expected HasOpenToolCall=true")
	}
}

// ESC during text generation: user message with "[Request interrupted by user]"
// should trigger cancellation detection via IsError.
func TestTranscript_ESCDuringGeneration_TransitionsToReady(t *testing.T) {
	path := writeTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "write a flight sim",
		}},
		// Tool call
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Bash", "id": "tu_1"},
			},
		}},
		// Tool result (success)
		{"type": "user", "timestamp": ts(2), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_1", "is_error": false},
			},
		}},
		// Second tool call
		{"type": "assistant", "timestamp": ts(3), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Bash", "id": "tu_2"},
			},
		}},
		// Tool result (success)
		{"type": "user", "timestamp": ts(4), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_2", "is_error": false},
			},
		}},
		// ESC during text generation
		{"type": "user", "timestamp": ts(5), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "[Request interrupted by user]"},
			},
		}},
	})

	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "user" {
		t.Errorf("LastEventType = %q, want user", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false (both tools resolved)")
	}
	if !m.LastToolResultWasError {
		t.Error("expected LastToolResultWasError=true (ESC interruption)")
	}
}

// Session without turn_done events (observed in session 14807218):
// end_turn stop_reason is the only done signal.
func TestTranscript_NoTurnDone_EndTurnStopReason(t *testing.T) {
	path := writeTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "ls on home",
		}},
		// Tool call
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Bash", "id": "tu_1"},
			},
		}},
		// Tool result
		{"type": "user", "timestamp": ts(2), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_1", "is_error": false},
			},
		}},
		// Final response — no system/turn_duration follows
		{"type": "assistant", "timestamp": ts(3), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "end_turn",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Here are the files in your home directory."},
			},
		}},
	})

	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	// end_turn message should emit "assistant" (IsAgentDone triggers)
	if m.LastEventType != "assistant" {
		t.Errorf("LastEventType = %q, want assistant (stop_reason=end_turn)", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false")
	}
}

// Simple text-only turn (no tools, no turn_done): "hi" → "Hi!"
// stop_reason=end_turn should correctly trigger done.
func TestTranscript_SimpleTextTurn_EndTurn(t *testing.T) {
	path := writeTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "hi",
		}},
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "end_turn",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Hi! How can I help?"},
			},
		}},
	})

	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "assistant" {
		t.Errorf("LastEventType = %q, want assistant", m.LastEventType)
	}
	if m.LastAssistantText != "Hi! How can I help?" {
		t.Errorf("LastAssistantText = %q", m.LastAssistantText)
	}
}

// Streaming text still extracts AssistantText for UI display.
func TestTranscript_StreamingText_StillExtractsText(t *testing.T) {
	path := writeTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "explain",
		}},
		// Thinking block first (sets msg_1 as current ID)
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "..."},
			},
		}},
		// Same ID text → streaming
		{"type": "assistant", "timestamp": ts(2), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Let me explain the architecture."},
			},
		}},
	})

	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	// Even though it's streaming, text should be extracted for display
	if m.LastAssistantText != "Let me explain the architecture." {
		t.Errorf("LastAssistantText = %q, want extracted text", m.LastAssistantText)
	}
	if m.LastEventType != "assistant_streaming" {
		t.Errorf("LastEventType = %q, want assistant_streaming", m.LastEventType)
	}
}
