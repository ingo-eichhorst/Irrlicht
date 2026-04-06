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

func TestParser_FunctionCall_WithoutNameStillCountsAsActivity(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "function_call",
		"arguments": `{"command":["zsh","-lc","ls"]}`,
		"timestamp": ts(2),
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "function_call" {
		t.Errorf("EventType = %q, want function_call", ev.EventType)
	}
	if len(ev.ToolUseNames) != 0 {
		t.Errorf("ToolUseNames = %v, want empty", ev.ToolUseNames)
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

func TestParser_WrappedAssistantMessage(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "response_item",
		"timestamp": ts(0),
		"payload": map[string]interface{}{
			"type": "message",
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{"type": "output_text", "text": "Should I run the tests?"},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "assistant_message" {
		t.Errorf("EventType = %q, want assistant_message", ev.EventType)
	}
	if ev.AssistantText != "Should I run the tests?" {
		t.Errorf("AssistantText = %q, want wrapped assistant text", ev.AssistantText)
	}
}

func TestParser_WrappedCustomAndWebSearchTools(t *testing.T) {
	p := &Parser{}

	custom := p.ParseLine(map[string]interface{}{
		"type":      "response_item",
		"timestamp": ts(0),
		"payload": map[string]interface{}{
			"type":    "custom_tool_call",
			"name":    "apply_patch",
			"call_id": "call_patch",
			"status":  "completed",
		},
	})
	if custom == nil {
		t.Fatal("expected non-nil custom tool event")
	}
	if custom.EventType != "function_call" {
		t.Errorf("custom EventType = %q, want function_call", custom.EventType)
	}
	if len(custom.ToolUseNames) != 1 || custom.ToolUseNames[0] != "apply_patch" {
		t.Errorf("custom ToolUseNames = %v, want [apply_patch]", custom.ToolUseNames)
	}

	web := p.ParseLine(map[string]interface{}{
		"type":      "response_item",
		"timestamp": ts(1),
		"payload": map[string]interface{}{
			"type":   "web_search_call",
			"status": "completed",
			"action": map[string]interface{}{"type": "search", "query": "irrlicht issue 90"},
		},
	})
	if web == nil {
		t.Fatal("expected non-nil web search event")
	}
	if web.EventType != "function_call_output" {
		t.Errorf("web EventType = %q, want function_call_output", web.EventType)
	}
	if len(web.ToolUseNames) != 1 || web.ToolUseNames[0] != "web_search" {
		t.Errorf("web ToolUseNames = %v, want [web_search]", web.ToolUseNames)
	}
	if web.ToolResultCount != 1 {
		t.Errorf("web ToolResultCount = %d, want 1", web.ToolResultCount)
	}
}

func TestParser_FullWrappedTranscript_MetadataAndWaitingState(t *testing.T) {
	path := writeLines(t, []map[string]interface{}{
		{
			"timestamp": ts(0),
			"type":      "session_meta",
			"payload": map[string]interface{}{
				"id":         "sess_wrapped",
				"cwd":        "/Users/test/project",
				"source":     "vscode",
				"originator": "codex_vscode",
			},
		},
		{
			"timestamp": ts(1),
			"type":      "turn_context",
			"payload": map[string]interface{}{
				"cwd":             "/Users/test/project",
				"model":           "gpt-5.2-codex",
				"approval_policy": "never",
				"sandbox_policy":  "danger-full-access",
			},
		},
		{
			"timestamp": ts(2),
			"type":      "event_msg",
			"payload": map[string]interface{}{
				"type": "token_count",
				"info": map[string]interface{}{
					"total_token_usage": map[string]interface{}{
						"input_tokens":  12,
						"output_tokens": 3,
						"total_tokens":  15,
					},
					"model_context_window": 258400,
				},
			},
		},
		{
			"timestamp": ts(3),
			"type":      "response_item",
			"payload": map[string]interface{}{
				"type": "message",
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "input_text", "text": "Please inspect the repo."},
				},
			},
		},
		{
			"timestamp": ts(4),
			"type":      "response_item",
			"payload": map[string]interface{}{
				"type":      "function_call",
				"name":      "shell_command",
				"call_id":   "call_shell",
				"arguments": `{"command":["pwd"],"workdir":"/Users/test/project"}`,
			},
		},
		{
			"timestamp": ts(5),
			"type":      "response_item",
			"payload": map[string]interface{}{
				"type":    "function_call_output",
				"call_id": "call_shell",
				"output":  "/Users/test/project\n",
			},
		},
		{
			"timestamp": ts(6),
			"type":      "response_item",
			"payload": map[string]interface{}{
				"type":    "custom_tool_call",
				"name":    "apply_patch",
				"call_id": "call_patch",
				"status":  "completed",
			},
		},
		{
			"timestamp": ts(7),
			"type":      "response_item",
			"payload": map[string]interface{}{
				"type":    "custom_tool_call_output",
				"call_id": "call_patch",
				"output":  "Success. Updated the following files:\nM foo.go",
			},
		},
		{
			"timestamp": ts(8),
			"type":      "response_item",
			"payload": map[string]interface{}{
				"type":   "web_search_call",
				"status": "completed",
				"action": map[string]interface{}{"type": "search", "query": "irrlicht wrapped codex schema"},
			},
		},
		{
			"timestamp": ts(9),
			"type":      "response_item",
			"payload": map[string]interface{}{
				"type": "message",
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{"type": "output_text", "text": "Should I run the tests?"},
				},
			},
		},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "codex")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastCWD != "/Users/test/project" {
		t.Errorf("LastCWD = %q, want /Users/test/project", m.LastCWD)
	}
	if m.ModelName != "gpt-5.2-codex" {
		t.Errorf("ModelName = %q, want gpt-5.2-codex", m.ModelName)
	}
	if m.ContextWindow != 258400 {
		t.Errorf("ContextWindow = %d, want 258400", m.ContextWindow)
	}
	if m.TotalTokens != 15 {
		t.Errorf("TotalTokens = %d, want 15", m.TotalTokens)
	}
	if m.LastEventType != "assistant_message" {
		t.Errorf("LastEventType = %q, want assistant_message", m.LastEventType)
	}
	if m.LastAssistantText != "Should I run the tests?" {
		t.Errorf("LastAssistantText = %q, want wrapped assistant text", m.LastAssistantText)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false after wrapped tool calls resolved")
	}

}
