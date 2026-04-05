package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/pkg/tailer"
)

func ts(offset int) string {
	return time.Now().Add(time.Duration(offset) * time.Second).Format(time.RFC3339)
}

func writeLines(t *testing.T, lines []map[string]interface{}) string {
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

func TestParser_SessionHeader_SkipWithCWD(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":    "session",
		"version": float64(3),
		"id":      "abc-123",
		"cwd":     "/Users/test/project",
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if !ev.Skip {
		t.Error("expected session header to be skipped")
	}
	if ev.CWD != "/Users/test/project" {
		t.Errorf("CWD = %q, want /Users/test/project", ev.CWD)
	}
}

func TestParser_ModelChange_SkipWithModel(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":    "model_change",
		"modelId": "gpt-5.3-codex",
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if !ev.Skip {
		t.Error("expected model_change to be skipped")
	}
	if ev.ModelName != "gpt-5.3-codex" {
		t.Errorf("ModelName = %q, want gpt-5.3-codex", ev.ModelName)
	}
}

func TestParser_ThinkingLevelChange_Skip(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":          "thinking_level_change",
		"thinkingLevel": "off",
	})
	if ev == nil || !ev.Skip {
		t.Error("expected thinking_level_change to be skipped")
	}
}

func TestParser_BashExecution_Skip(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"timestamp": ts(0),
		"message": map[string]interface{}{
			"role": "bashExecution",
		},
	})
	if ev == nil || !ev.Skip {
		t.Error("expected bashExecution to be skipped")
	}
}

func TestParser_AssistantEndOfTurn(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"timestamp": ts(0),
		"message": map[string]interface{}{
			"role":       "assistant",
			"stopReason": "stop",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Done!"},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "assistant_message" {
		t.Errorf("EventType = %q, want assistant_message (end-of-turn)", ev.EventType)
	}
	if ev.AssistantText != "Done!" {
		t.Errorf("AssistantText = %q, want Done!", ev.AssistantText)
	}
}

func TestParser_AssistantMidTurn(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"timestamp": ts(0),
		"message": map[string]interface{}{
			"role":       "assistant",
			"stopReason": "toolUse",
			"content": []interface{}{
				map[string]interface{}{"type": "toolCall", "id": "call_1", "name": "bash",
					"arguments": map[string]interface{}{"command": "ls"}},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "assistant" {
		t.Errorf("EventType = %q, want assistant (mid-turn)", ev.EventType)
	}
	if len(ev.ToolUseNames) != 1 || ev.ToolUseNames[0] != "bash" {
		t.Errorf("ToolUseNames = %v, want [bash]", ev.ToolUseNames)
	}
}

func TestParser_ToolResult_SingleCount(t *testing.T) {
	// This is the Bug 1 regression test: toolResult should be counted
	// exactly once (in the parser), not also in addMessageEvent.
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"timestamp": ts(0),
		"message": map[string]interface{}{
			"role":       "toolResult",
			"toolCallId": "call_1",
			"toolName":   "bash",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "file1\nfile2\n"},
			},
			"isError": false,
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "tool_result" {
		t.Errorf("EventType = %q, want tool_result", ev.EventType)
	}
	if ev.ToolResultCount != 1 {
		t.Errorf("ToolResultCount = %d, want exactly 1 (no double-counting)", ev.ToolResultCount)
	}
}

func TestParser_UserMessage(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"timestamp": ts(0),
		"message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "hello"},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "user_message" {
		t.Errorf("EventType = %q, want user_message", ev.EventType)
	}
	if !ev.ClearToolNames {
		t.Error("expected ClearToolNames=true for user message")
	}
}

func TestParser_FullTranscript_EndDetection(t *testing.T) {
	// Simulate the real Pi transcript from the bug report.
	path := writeLines(t, []map[string]interface{}{
		{"type": "session", "version": float64(3), "id": "abc-123",
			"timestamp": ts(0), "cwd": "/Users/test/project"},
		{"type": "model_change", "id": "m1", "timestamp": ts(0),
			"provider": "openai-codex", "modelId": "gpt-5.3-codex"},
		{"type": "thinking_level_change", "id": "t1", "timestamp": ts(0),
			"thinkingLevel": "off"},
		{"type": "message", "id": "u1", "timestamp": ts(1),
			"message": map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "ls"},
				},
			}},
		{"type": "message", "id": "a1", "timestamp": ts(2),
			"message": map[string]interface{}{
				"role":       "assistant",
				"stopReason": "toolUse",
				"content": []interface{}{
					map[string]interface{}{"type": "toolCall", "id": "call_1", "name": "bash",
						"arguments": map[string]interface{}{"command": "ls"}},
				},
			}},
		{"type": "message", "id": "tr1", "timestamp": ts(3),
			"message": map[string]interface{}{
				"role":       "toolResult",
				"toolCallId": "call_1",
				"toolName":   "bash",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "hello.py\n"},
				},
				"isError": false,
			}},
		{"type": "message", "id": "a2", "timestamp": ts(4),
			"message": map[string]interface{}{
				"role":       "assistant",
				"stopReason": "stop",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "hello.py"},
				},
			}},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "pi")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "assistant_message" {
		t.Errorf("LastEventType = %q, want assistant_message", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false after all tool calls resolved")
	}
	if m.OpenToolCallCount != 0 {
		t.Errorf("OpenToolCallCount = %d, want 0", m.OpenToolCallCount)
	}
}

func TestParser_BashExecutionSkipped_PreservesLastEvent(t *testing.T) {
	// After an assistant end-of-turn, a bashExecution event should be skipped
	// and not change LastEventType.
	path := writeLines(t, []map[string]interface{}{
		{"type": "session", "version": float64(3), "cwd": "/tmp"},
		{"type": "message", "timestamp": ts(0), "message": map[string]interface{}{
			"role":       "assistant",
			"stopReason": "stop",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Hi!"},
			},
		}},
		{"type": "message", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "bashExecution",
		}},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "pi")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "assistant_message" {
		t.Errorf("LastEventType = %q, want assistant_message (bashExecution should be skipped)", m.LastEventType)
	}
}
