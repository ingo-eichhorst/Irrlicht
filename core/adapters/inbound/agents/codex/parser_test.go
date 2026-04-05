package codex

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

func TestParser_SessionHeader_Skip(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"id":        "abc-123",
		"timestamp": ts(0),
	})
	if ev == nil || !ev.Skip {
		t.Error("expected session header to be skipped")
	}
}

func TestParser_RecordTypeState_Skip(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"record_type": "state",
	})
	if ev == nil || !ev.Skip {
		t.Error("expected record_type:state to be skipped")
	}
}

func TestParser_Reasoning_Skip(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type": "reasoning",
		"id":   "rs_abc",
	})
	if ev == nil || !ev.Skip {
		t.Error("expected reasoning event to be skipped")
	}
}

func TestParser_AssistantMessage(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"role":      "assistant",
		"timestamp": ts(1),
		"content": []interface{}{
			map[string]interface{}{"type": "output_text", "text": "Hello!"},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "assistant_message" {
		t.Errorf("EventType = %q, want assistant_message", ev.EventType)
	}
	if ev.AssistantText != "Hello!" {
		t.Errorf("AssistantText = %q, want Hello!", ev.AssistantText)
	}
}

func TestParser_UserMessage(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"role":      "user",
		"timestamp": ts(0),
		"content": []interface{}{
			map[string]interface{}{"type": "input_text", "text": "ls"},
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

func TestParser_FunctionCall(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "function_call",
		"name":      "shell",
		"arguments": `{"command":["zsh","-lc","ls"]}`,
		"timestamp": ts(2),
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "function_call" {
		t.Errorf("EventType = %q, want function_call", ev.EventType)
	}
	if len(ev.ToolUseNames) != 1 || ev.ToolUseNames[0] != "shell" {
		t.Errorf("ToolUseNames = %v, want [shell]", ev.ToolUseNames)
	}
}

func TestParser_FunctionCallOutput(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "function_call_output",
		"call_id":   "call_abc",
		"output":    "file1\nfile2\n",
		"timestamp": ts(3),
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "function_call_output" {
		t.Errorf("EventType = %q, want function_call_output", ev.EventType)
	}
	if ev.ToolResultCount != 1 {
		t.Errorf("ToolResultCount = %d, want 1", ev.ToolResultCount)
	}
}

func TestParser_FullTranscript_EndDetection(t *testing.T) {
	// Simulate the real Codex transcript from the bug report.
	path := writeLines(t, []map[string]interface{}{
		{"id": "abc-123", "timestamp": ts(0)},
		{"record_type": "state"},
		{"type": "message", "role": "user", "timestamp": ts(1),
			"content": []interface{}{
				map[string]interface{}{"type": "input_text", "text": "ls"},
			}},
		{"record_type": "state"},
		{"type": "reasoning", "id": "rs_abc", "timestamp": ts(2)},
		{"type": "message", "role": "assistant", "timestamp": ts(3),
			"content": []interface{}{
				map[string]interface{}{"type": "output_text", "text": "Listing files."},
			}},
		{"type": "function_call", "name": "shell", "timestamp": ts(4),
			"arguments": `{"command":["ls"]}`},
		{"type": "function_call_output", "call_id": "call_1", "timestamp": ts(5),
			"output": "hello.py\n"},
		{"record_type": "state"},
		{"type": "message", "role": "assistant", "timestamp": ts(6),
			"content": []interface{}{
				map[string]interface{}{"type": "output_text", "text": "hello.py"},
			}},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "codex")
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
}

func TestParser_AssistantText_OutputText(t *testing.T) {
	// Codex uses output_text blocks, not text blocks.
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"role":      "assistant",
		"timestamp": ts(0),
		"content": []interface{}{
			map[string]interface{}{"type": "output_text", "text": "What would you like to do?"},
		},
	})
	if ev.AssistantText != "What would you like to do?" {
		t.Errorf("AssistantText = %q, want question text", ev.AssistantText)
	}
}

func TestParser_CWDExtraction(t *testing.T) {
	p := &Parser{}
	// CWD from <cwd> XML tag in content.
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"role":      "user",
		"timestamp": ts(0),
		"content": []interface{}{
			map[string]interface{}{"type": "input_text", "text": "<environment_context>\n  <cwd>/Users/test/project</cwd>\n</environment_context>"},
		},
	})
	if ev.CWD != "/Users/test/project" {
		t.Errorf("CWD = %q, want /Users/test/project", ev.CWD)
	}
}
