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

// TestParser_AgentVersion_Captured pins that session_meta.payload.cli_version
// is surfaced on the parsed event for the cache-bloat detector (#374), even
// though the event is itself skipped.
func TestParser_AgentVersion_Captured(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "session_meta",
		"timestamp": ts(0),
		"payload":   map[string]interface{}{"cli_version": "0.137.0"},
	})
	if ev == nil || !ev.Skip {
		t.Fatalf("session_meta should be skipped, got %+v", ev)
	}
	if ev.AgentVersion != "0.137.0" {
		t.Errorf("expected AgentVersion=0.137.0, got %q", ev.AgentVersion)
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
	if ev.UserText != "ls" {
		t.Errorf("UserText = %q, want the prompt 'ls' (heuristic summary)", ev.UserText)
	}
}

func TestParser_UserMessage_InstructionsPreambleSkipped(t *testing.T) {
	// Codex injects its AGENTS.md / <INSTRUCTIONS> preamble as the FIRST
	// user-role message; it must not become the heuristic task summary (#738).
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"role":      "user",
		"timestamp": ts(0),
		"content": []interface{}{
			map[string]interface{}{"type": "input_text", "text": "# AGENTS.md instructions for /x\n\n<INSTRUCTIONS>\n@AGENTS.md\n</INSTRUCTIONS>"},
		},
	})
	if ev == nil || ev.EventType != "user_message" {
		t.Fatalf("ev = %+v, want a user_message", ev)
	}
	if ev.UserText != "" {
		t.Errorf("UserText = %q, want empty (injected preamble must not seed the summary)", ev.UserText)
	}
}

func TestParser_FunctionCall(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "function_call",
		"name":      "shell",
		"call_id":   "call_abc",
		"arguments": `{"command":["zsh","-lc","ls"]}`,
		"timestamp": ts(2),
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "function_call" {
		t.Errorf("EventType = %q, want function_call", ev.EventType)
	}
	if len(ev.ToolUses) != 1 || ev.ToolUses[0].Name != "shell" || ev.ToolUses[0].ID != "call_abc" {
		t.Errorf("ToolUses = %v, want [{call_abc shell}]", ev.ToolUses)
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
	if len(ev.ToolUses) != 0 {
		t.Errorf("ToolUses = %v, want empty", ev.ToolUses)
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
	if len(ev.ToolResultIDs) != 1 || ev.ToolResultIDs[0] != "call_abc" {
		t.Errorf("ToolResultIDs = %v, want [call_abc]", ev.ToolResultIDs)
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
	if len(custom.ToolUses) != 1 || custom.ToolUses[0].Name != "apply_patch" || custom.ToolUses[0].ID != "call_patch" {
		t.Errorf("custom ToolUses = %v, want [{call_patch apply_patch}]", custom.ToolUses)
	}

	web := p.ParseLine(map[string]interface{}{
		"type":      "response_item",
		"timestamp": ts(1),
		"payload": map[string]interface{}{
			"type":   "web_search_call",
			"id":     "ws_1",
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
	if len(web.ToolUses) != 1 || web.ToolUses[0].Name != "web_search" {
		t.Errorf("web ToolUses = %v, want [{ws_1 web_search}]", web.ToolUses)
	}
	if len(web.ToolResultIDs) != 1 || web.ToolResultIDs[0] != "ws_1" {
		t.Errorf("web ToolResultIDs = %v, want [ws_1]", web.ToolResultIDs)
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
					"last_token_usage": map[string]interface{}{
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

// TestParser_MultiTurnTokenCount_UsesPerTurnSnapshot is a regression test for
// the bug where Codex's cumulative `total_token_usage` was used as the per-turn
// token snapshot, causing context utilization to grow past 100% over a long
// session. The parser must read `last_token_usage` (per-turn) instead.
//
// Reproduces the shape of a real codex transcript: every token_count event
// carries both blocks, where total_token_usage grows monotonically while
// last_token_usage reflects the size of just the most recent turn.
func TestParser_MultiTurnTokenCount_UsesPerTurnSnapshot(t *testing.T) {
	mkTokenCount := func(offset int, lastInput, lastOutput, totalCumulative int) map[string]interface{} {
		return map[string]interface{}{
			"timestamp": ts(offset),
			"type":      "event_msg",
			"payload": map[string]interface{}{
				"type": "token_count",
				"info": map[string]interface{}{
					"total_token_usage": map[string]interface{}{
						// Cumulative across all turns — should be IGNORED.
						"input_tokens":  totalCumulative - 100,
						"output_tokens": 100,
						"total_tokens":  totalCumulative,
					},
					"last_token_usage": map[string]interface{}{
						// Per-turn — what context utilization should use.
						"input_tokens":  lastInput,
						"output_tokens": lastOutput,
						"total_tokens":  lastInput + lastOutput,
					},
					"model_context_window": 258400,
				},
			},
		}
	}

	path := writeLines(t, []map[string]interface{}{
		{
			"timestamp": ts(0),
			"type":      "turn_context",
			"payload": map[string]interface{}{
				"model": "gpt-5.2-codex",
			},
		},
		mkTokenCount(1, 22672, 303, 22975),
		mkTokenCount(2, 30330, 365, 53670),
		mkTokenCount(3, 38511, 352, 92533),
		mkTokenCount(4, 48256, 385, 141174),
		mkTokenCount(5, 49030, 1431, 191635),
		mkTokenCount(6, 51241, 93, 242969),
		mkTokenCount(7, 52130, 104, 295203),
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "codex")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	// Last per-turn snapshot: 52130 + 104 = 52234. The cumulative 295203
	// MUST NOT leak through.
	if m.TotalTokens != 52234 {
		t.Errorf("TotalTokens = %d, want 52234 (per-turn). Cumulative leak?", m.TotalTokens)
	}
	if m.ContextWindow != 258400 {
		t.Errorf("ContextWindow = %d, want 258400", m.ContextWindow)
	}
	// 52234 / 258400 ≈ 20.21%
	if m.ContextUtilization < 19.0 || m.ContextUtilization > 21.5 {
		t.Errorf("ContextUtilization = %.2f%%, want ~20.2%%", m.ContextUtilization)
	}
	if m.PressureLevel != "safe" {
		t.Errorf("PressureLevel = %q, want safe", m.PressureLevel)
	}
}

// TestParser_TurnCompleteEmitsTurnDone is a regression test for flicker at the
// start of codex turns: the agent routinely emits a preliminary assistant
// message BEFORE calling a tool, and the old fallback (`assistant_message`
// as terminal) flipped the session ready prematurely. The canonical end-of-
// turn signal is the `event_msg` with payload.type == "task_complete", and
// the parser must map it to LastEventType == "turn_done" so IsAgentDone()
// fires via the primary path only at the real turn boundary.
func TestParser_TurnCompleteEmitsTurnDone(t *testing.T) {
	path := writeLines(t, []map[string]interface{}{
		{
			"timestamp": ts(0),
			"type":      "turn_context",
			"payload":   map[string]interface{}{"model": "gpt-5.2-codex"},
		},
		// Preliminary assistant message emitted mid-turn before a tool call.
		// This must NOT look like a terminal event.
		{
			"timestamp": ts(1),
			"type":      "response_item",
			"payload": map[string]interface{}{
				"type": "message",
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{"type": "output_text", "text": "let me check"},
				},
			},
		},
		{
			"timestamp": ts(2),
			"type":      "response_item",
			"payload": map[string]interface{}{
				"type": "function_call",
				"name": "shell",
			},
		},
		{
			"timestamp": ts(3),
			"type":      "response_item",
			"payload":   map[string]interface{}{"type": "function_call_output"},
		},
		// Real end of turn.
		{
			"timestamp": ts(4),
			"type":      "event_msg",
			"payload":   map[string]interface{}{"type": "task_complete"},
		},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "codex")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("LastEventType = %q, want turn_done", m.LastEventType)
	}
}

// TestParser_TurnAbortedEmitsTurnDone is a regression test for #453: codex
// emits `event_msg/turn_aborted` (no task_complete) when a turn is cancelled
// (user ESC) or errors mid-flight. The parser must treat it as a turn-end
// signal — map it to LastEventType == "turn_done" — so the interrupted turn
// settles instead of sticking in `working` until the process exits.
func TestParser_TurnAbortedEmitsTurnDone(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"timestamp": ts(0),
		"type":      "event_msg",
		"payload": map[string]interface{}{
			"type":         "turn_aborted",
			"turn_id":      "019e3291-4d64-7e80-b513-d0a57d8169c1",
			"reason":       "interrupted",
			"completed_at": float64(1778964847),
			"duration_ms":  float64(4011),
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.Skip {
		t.Fatal("turn_aborted must not be skipped; it is a turn-end signal")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want turn_done", ev.EventType)
	}
}

// TestParser_InterruptedTurnSettles drives the full interrupted-turn shape as
// recorded from a real codex session: a turn opens (assistant message + a
// function call with no matching output because the user hit ESC), then codex
// writes the synthetic <turn_aborted> user message and the
// event_msg/turn_aborted boundary. End-to-end through the tailer this must
// leave LastEventType == "turn_done" so the session settles to ready rather
// than freezing in working (#453).
func TestParser_InterruptedTurnSettles(t *testing.T) {
	path := writeLines(t, []map[string]interface{}{
		{
			"timestamp": ts(0),
			"type":      "turn_context",
			"payload":   map[string]interface{}{"model": "gpt-5.2-codex"},
		},
		{
			"timestamp": ts(1),
			"type":      "response_item",
			"payload": map[string]interface{}{
				"type": "message",
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{"type": "output_text", "text": "running a long command"},
				},
			},
		},
		// Tool call opens but is never answered — the user interrupted it.
		{
			"timestamp": ts(2),
			"type":      "response_item",
			"payload": map[string]interface{}{
				"type":    "function_call",
				"name":    "shell",
				"call_id": "call_interrupted",
			},
		},
		// Synthetic user message codex injects after an interrupt.
		{
			"timestamp": ts(3),
			"type":      "response_item",
			"payload": map[string]interface{}{
				"type": "message",
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "input_text", "text": "<turn_aborted>\nThe user interrupted the previous turn on purpose.\n</turn_aborted>"},
				},
			},
		},
		// The turn boundary: no task_complete, only turn_aborted.
		{
			"timestamp": ts(4),
			"type":      "event_msg",
			"payload": map[string]interface{}{
				"type":    "turn_aborted",
				"turn_id": "019e3291-4d64-7e80-b513-d0a57d8169c1",
				"reason":  "interrupted",
			},
		},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "codex")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("LastEventType = %q, want turn_done (interrupted turn must settle)", m.LastEventType)
	}
}

func TestParser_ProposedPlan_SynthesizesExitPlanMode(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"role":      "assistant",
		"timestamp": ts(0),
		"content": []interface{}{
			map[string]interface{}{"type": "output_text", "text": "<proposed_plan>\n# Plan\n\nDo the thing.\n</proposed_plan>"},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "assistant_message" {
		t.Errorf("EventType = %q, want assistant_message", ev.EventType)
	}
	if len(ev.ToolUses) != 1 {
		t.Fatalf("ToolUses len = %d, want 1; got %v", len(ev.ToolUses), ev.ToolUses)
	}
	if ev.ToolUses[0].Name != "ExitPlanMode" {
		t.Errorf("ToolUses[0].Name = %q, want ExitPlanMode", ev.ToolUses[0].Name)
	}
	if ev.ToolUses[0].ID == "" {
		t.Error("ToolUses[0].ID is empty; need a non-empty ID for the tailer to track the open call")
	}
}

func TestParser_AssistantMessageWithoutProposedPlan_NoSyntheticTool(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"role":      "assistant",
		"timestamp": ts(0),
		"content": []interface{}{
			map[string]interface{}{"type": "output_text", "text": "Here's what I'll do next."},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if len(ev.ToolUses) != 0 {
		t.Errorf("ToolUses = %v, want empty for plain assistant text", ev.ToolUses)
	}
}

// The Codex developer system prompt mentions `<proposed_plan>` as
// documentation — detection must be role-gated to avoid firing on it.
func TestParser_ProposedPlan_DeveloperMessageDoesNotTrigger(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"role":      "developer",
		"timestamp": ts(0),
		"content": []interface{}{
			map[string]interface{}{"type": "input_text", "text": "...eventually issuing a <proposed_plan> block..."},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "user_message" {
		t.Errorf("EventType = %q, want user_message", ev.EventType)
	}
	if len(ev.ToolUses) != 0 {
		t.Errorf("ToolUses = %v, want empty for developer message", ev.ToolUses)
	}
}

// The synthetic ExitPlanMode must survive `task_complete`, otherwise
// IsAgentDone() would route the session to ready before the user gets a
// chance to respond.
func TestParser_ProposedPlan_EndToEndWaitingState(t *testing.T) {
	path := writeLines(t, []map[string]interface{}{
		{
			"timestamp": ts(0),
			"type":      "turn_context",
			"payload":   map[string]interface{}{"model": "gpt-5.2-codex"},
		},
		{
			"timestamp": ts(1),
			"type":      "response_item",
			"payload": map[string]interface{}{
				"type": "message",
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{"type": "output_text", "text": "<proposed_plan>\n# Plan\n\n## Summary\n\nDo X then Y.\n</proposed_plan>"},
				},
				"phase": "final_answer",
			},
		},
		{
			"timestamp": ts(2),
			"type":      "event_msg",
			"payload": map[string]interface{}{
				"type": "token_count",
				"info": map[string]interface{}{
					"last_token_usage":     map[string]interface{}{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
					"model_context_window": 258400,
				},
			},
		},
		{
			"timestamp": ts(3),
			"type":      "event_msg",
			"payload":   map[string]interface{}{"type": "task_complete"},
		},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "codex")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true after proposed_plan + task_complete (waiting on user approval)")
	}
	foundExitPlanMode := false
	for _, name := range m.LastOpenToolNames {
		if name == "ExitPlanMode" {
			foundExitPlanMode = true
			break
		}
	}
	if !foundExitPlanMode {
		t.Errorf("LastOpenToolNames = %v, want to contain ExitPlanMode", m.LastOpenToolNames)
	}
}

func TestParser_ProposedPlan_PartialTagDoesNotTrigger(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"role":      "assistant",
		"timestamp": ts(0),
		"content": []interface{}{
			map[string]interface{}{"type": "output_text", "text": "<proposed_plan>\n# Half a plan, no close tag yet…"},
		},
	})
	if len(ev.ToolUses) != 0 {
		t.Errorf("ToolUses = %v, want empty when closing tag is missing", ev.ToolUses)
	}
}

// Two consecutive proposed_plan messages share the same synthetic ID so
// the tailer dedupes to a single open ExitPlanMode entry.
func TestParser_ProposedPlan_TwoConsecutivePlans_DedupesOpenTool(t *testing.T) {
	path := writeLines(t, []map[string]interface{}{
		{
			"timestamp": ts(0),
			"type":      "response_item",
			"payload": map[string]interface{}{
				"type": "message", "role": "assistant",
				"content": []interface{}{
					map[string]interface{}{"type": "output_text", "text": "<proposed_plan>v1</proposed_plan>"},
				},
			},
		},
		{
			"timestamp": ts(1),
			"type":      "response_item",
			"payload": map[string]interface{}{
				"type": "message", "role": "assistant",
				"content": []interface{}{
					map[string]interface{}{"type": "output_text", "text": "<proposed_plan>v2 (revised)</proposed_plan>"},
				},
			},
		},
		{
			"timestamp": ts(2),
			"type":      "event_msg",
			"payload":   map[string]interface{}{"type": "task_complete"},
		},
	})
	tl := tailer.NewTranscriptTailer(path, &Parser{}, "codex")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenToolCall {
		t.Fatal("expected HasOpenToolCall=true after two proposed_plan messages")
	}
	exitPlanModeCount := 0
	for _, name := range m.LastOpenToolNames {
		if name == "ExitPlanMode" {
			exitPlanModeCount++
		}
	}
	if exitPlanModeCount != 1 {
		t.Errorf("LastOpenToolNames had %d ExitPlanMode entries, want exactly 1 (fixed-ID dedup); got %v", exitPlanModeCount, m.LastOpenToolNames)
	}
}

func TestParser_ProposedPlan_ClosedByUserReply(t *testing.T) {
	path := writeLines(t, []map[string]interface{}{
		{
			"timestamp": ts(0),
			"type":      "response_item",
			"payload": map[string]interface{}{
				"type": "message",
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{"type": "output_text", "text": "<proposed_plan>plan</proposed_plan>"},
				},
			},
		},
		{
			"timestamp": ts(1),
			"type":      "event_msg",
			"payload":   map[string]interface{}{"type": "task_complete"},
		},
		{
			"timestamp": ts(2),
			"type":      "response_item",
			"payload": map[string]interface{}{
				"type": "message",
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "input_text", "text": "looks good, proceed"},
				},
			},
		},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "codex")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.HasOpenToolCall {
		t.Errorf("expected HasOpenToolCall=false after user reply, got open tools = %v", m.LastOpenToolNames)
	}
}

// TestParser_Contribution_CachedTokensDeductedFromInput verifies that
// input_tokens_details.cached_tokens is used for CacheRead and deducted from
// Input so cost isn't double-counted (OpenAI includes cached in input_tokens).
func TestParser_Contribution_CachedTokensDeductedFromInput(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"timestamp": ts(0),
		"type":      "event_msg",
		"payload": map[string]interface{}{
			"type": "token_count",
			"info": map[string]interface{}{
				"model_context_window": float64(258400),
				"last_token_usage": map[string]interface{}{
					"input_tokens":  float64(5000),
					"output_tokens": float64(200),
					"total_tokens":  float64(5200),
				},
				"total_token_usage": map[string]interface{}{
					"input_tokens":  float64(10000),
					"output_tokens": float64(500),
					"input_tokens_details": map[string]interface{}{
						"cached_tokens": float64(2000),
					},
				},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.Contribution == nil {
		t.Fatal("expected Contribution to be set from cumulative usage")
	}
	// Input = 10000 − 2000 (cached deducted) = 8000.
	if ev.Contribution.Usage.Input != 8000 {
		t.Errorf("Input = %d, want 8000 (gross 10000 minus 2000 cached)", ev.Contribution.Usage.Input)
	}
	if ev.Contribution.Usage.CacheRead != 2000 {
		t.Errorf("CacheRead = %d, want 2000", ev.Contribution.Usage.CacheRead)
	}
	if ev.Contribution.Usage.Output != 500 {
		t.Errorf("Output = %d, want 500", ev.Contribution.Usage.Output)
	}
}

// TestParser_Contribution_Monotonic confirms the cursor prevents a decrease in
// cumulative usage from lowering the accumulated cost.
func TestParser_Contribution_Monotonic(t *testing.T) {
	p := &Parser{}

	mkEvent := func(input, output float64) *tailer.ParsedEvent {
		return p.ParseLine(map[string]interface{}{
			"timestamp": ts(0),
			"type":      "event_msg",
			"payload": map[string]interface{}{
				"type": "token_count",
				"info": map[string]interface{}{
					"model_context_window": float64(258400),
					"last_token_usage": map[string]interface{}{
						"input_tokens": input, "output_tokens": output,
					},
					"total_token_usage": map[string]interface{}{
						"input_tokens": input, "output_tokens": output,
					},
				},
			},
		})
	}

	// First event: 1000 input, 100 output.
	ev1 := mkEvent(1000, 100)
	if ev1.Contribution == nil {
		t.Fatal("expected first Contribution")
	}
	if ev1.Contribution.Usage.Input != 1000 || ev1.Contribution.Usage.Output != 100 {
		t.Errorf("first delta = {%d,%d}, want {1000,100}",
			ev1.Contribution.Usage.Input, ev1.Contribution.Usage.Output)
	}

	// Second event: cumulative drops below first (would happen if parser restarts).
	// Delta must be zero → no Contribution emitted.
	ev2 := mkEvent(500, 50)
	if ev2.Contribution != nil {
		t.Errorf("expected no Contribution when cumulative decreases, got %+v", ev2.Contribution)
	}

	// Third event: advances again from first high-water mark.
	ev3 := mkEvent(1500, 150)
	if ev3.Contribution == nil {
		t.Fatal("expected Contribution after cumulative advances again")
	}
	// Delta should be 1500−1000=500 input, 150−100=50 output.
	if ev3.Contribution.Usage.Input != 500 || ev3.Contribution.Usage.Output != 50 {
		t.Errorf("third delta = {%d,%d}, want {500,50}",
			ev3.Contribution.Usage.Input, ev3.Contribution.Usage.Output)
	}
}

// TestParser_EventMsgNonTaskCompleteSkipped confirms the carve-out is narrow:
// token_count, task_started, exec_command_*, and friends must still be
// skipped, otherwise we'd leak spurious LastEventType values that the
// classifier isn't prepared for.
func TestParser_EventMsgNonTaskCompleteSkipped(t *testing.T) {
	p := &Parser{}
	for _, pt := range []string{"task_started", "token_count", "exec_command_begin", "exec_command_end", "user_message"} {
		ev := p.ParseLine(map[string]interface{}{
			"type":    "event_msg",
			"payload": map[string]interface{}{"type": pt},
		})
		if ev == nil || !ev.Skip {
			t.Errorf("event_msg payload %q: expected skip", pt)
		}
	}
}

// TestParser_RateLimits_V3Schema asserts the parser extracts a complete v3
// rate_limits block (limit_id + plan_type + reached_type, no credits) from
// a token_count event into the snapshot.
func TestParser_RateLimits_V3Schema(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type": "event_msg",
		"payload": map[string]interface{}{
			"type":       "token_count",
			"limit_id":   "codex",
			"limit_name": nil,
			"rate_limits": map[string]interface{}{
				"limit_id":   "codex",
				"limit_name": nil,
				"primary": map[string]interface{}{
					"used_percent":   1.0,
					"window_minutes": 300.0,
					"resets_at":      1778625131.0,
				},
				"secondary": map[string]interface{}{
					"used_percent":   0.0,
					"window_minutes": 10080.0,
					"resets_at":      1778949174.0,
				},
				"credits":                 nil,
				"plan_type":               "plus",
				"rate_limit_reached_type": nil,
			},
		},
	})
	if ev == nil || !ev.Skip {
		t.Fatal("expected token_count to be skipped (post-rate-limit extraction)")
	}
	if ev.RateLimit == nil {
		t.Fatal("expected RateLimit snapshot on token_count event")
	}
	if ev.RateLimit.PlanType != "plus" {
		t.Errorf("plan_type = %q, want plus", ev.RateLimit.PlanType)
	}
	if len(ev.RateLimit.Windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(ev.RateLimit.Windows))
	}
	primary := ev.RateLimit.Windows[0]
	if primary.WindowMinutes != 300 || primary.UsedPercent != 1.0 || primary.ResetsAt != 1778625131 {
		t.Errorf("primary = %+v", primary)
	}
	secondary := ev.RateLimit.Windows[1]
	if secondary.WindowMinutes != 10080 || secondary.ResetsAt != 1778949174 {
		t.Errorf("secondary = %+v", secondary)
	}
	if ev.RateLimit.Credits != nil {
		t.Errorf("expected nil Credits on subscription path, got %+v", ev.RateLimit.Credits)
	}
}

// TestParser_RateLimits_V2WithCredits exercises the API-key / usage-path
// shape where plan_type is null and credits carries a balance.
func TestParser_RateLimits_V2WithCredits(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type": "event_msg",
		"payload": map[string]interface{}{
			"type": "token_count",
			"rate_limits": map[string]interface{}{
				"primary": map[string]interface{}{
					"used_percent":   12.5,
					"window_minutes": 300.0,
					"resets_at":      1778625131.0,
				},
				"secondary": map[string]interface{}{
					"used_percent":   2.0,
					"window_minutes": 10080.0,
					"resets_at":      1778949174.0,
				},
				"credits": map[string]interface{}{
					"has_credits": true,
					"unlimited":   false,
					"balance":     42.5,
				},
				"plan_type": nil,
			},
		},
	})
	if ev.RateLimit == nil {
		t.Fatal("expected RateLimit snapshot")
	}
	if ev.RateLimit.PlanType != "" {
		t.Errorf("expected empty plan_type, got %q", ev.RateLimit.PlanType)
	}
	if ev.RateLimit.Credits == nil {
		t.Fatal("expected non-nil Credits on API-key path")
	}
	if !ev.RateLimit.Credits.HasCredits || ev.RateLimit.Credits.Balance != 42.5 {
		t.Errorf("credits = %+v", ev.RateLimit.Credits)
	}
}

// TestParser_RateLimits_V1RelativeResets converts v1's resets_in_seconds
// (relative) to absolute epoch using the event timestamp, and preserves
// the off-by-one (299, 10079) window-minute values verbatim.
func TestParser_RateLimits_V1RelativeResets(t *testing.T) {
	p := &Parser{}
	tsStr := "2025-10-15T12:00:00Z"
	parsed, _ := time.Parse(time.RFC3339, tsStr)
	ev := p.ParseLine(map[string]interface{}{
		"type":      "event_msg",
		"timestamp": tsStr,
		"payload": map[string]interface{}{
			"type": "token_count",
			"rate_limits": map[string]interface{}{
				"primary": map[string]interface{}{
					"used_percent":      5.0,
					"window_minutes":    299.0,
					"resets_in_seconds": 1800.0,
				},
				"secondary": map[string]interface{}{
					"used_percent":      1.0,
					"window_minutes":    10079.0,
					"resets_in_seconds": 86400.0,
				},
			},
		},
	})
	if ev.RateLimit == nil {
		t.Fatal("expected RateLimit snapshot")
	}
	if ev.RateLimit.Windows[0].WindowMinutes != 299 {
		t.Errorf("expected v1 quirk window_minutes=299 preserved, got %d", ev.RateLimit.Windows[0].WindowMinutes)
	}
	wantPrimaryResets := parsed.Add(1800 * time.Second).Unix()
	if ev.RateLimit.Windows[0].ResetsAt != wantPrimaryResets {
		t.Errorf("primary resets_at = %d, want %d (relative→absolute)", ev.RateLimit.Windows[0].ResetsAt, wantPrimaryResets)
	}
}

// TestParser_RateLimits_AbsentReturnsNil keeps the snapshot nil when the
// payload has no rate_limits block (older Codex transcripts, pre-first-API
// response events). Must not produce a phantom empty snapshot.
func TestParser_RateLimits_AbsentReturnsNil(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type": "event_msg",
		"payload": map[string]interface{}{
			"type": "token_count",
			"info": map[string]interface{}{},
		},
	})
	if ev.RateLimit != nil {
		t.Errorf("expected nil RateLimit on payload without rate_limits, got %+v", ev.RateLimit)
	}
}

// TestParser_RateLimits_OnlyOnTokenCount ensures we don't extract from
// other event_msg payload types (task_started, exec_command_*, etc.) even
// if a malformed transcript carries a rate_limits block on them.
func TestParser_RateLimits_OnlyOnTokenCount(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type": "event_msg",
		"payload": map[string]interface{}{
			"type": "task_started",
			"rate_limits": map[string]interface{}{
				"primary": map[string]interface{}{
					"used_percent":   99.0,
					"window_minutes": 300.0,
					"resets_at":      1778625131.0,
				},
			},
		},
	})
	if ev.RateLimit != nil {
		t.Errorf("expected nil RateLimit on non-token_count event_msg, got %+v", ev.RateLimit)
	}
}

// --- Task-estimate marker (issue #558) ---

func TestParser_TaskEstimate_TopLevelContent(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type": "message", "role": "assistant", "timestamp": ts(1),
		"content": []interface{}{
			map[string]interface{}{"type": "output_text", "text": `Step done. <!-- {"marker":"irrlicht-eta","total_rounds":6,"completed_rounds":2} -->`},
		},
	})
	if ev.TaskEstimate == nil {
		t.Fatal("expected TaskEstimate from top-level content")
	}
	if ev.TaskEstimate.TotalRounds != 6 || ev.TaskEstimate.CompletedRounds != 2 {
		t.Errorf("rounds = %d/%d, want 2/6", ev.TaskEstimate.CompletedRounds, ev.TaskEstimate.TotalRounds)
	}
}

func TestParser_TaskEstimate_NestedMessageContent(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type": "message", "role": "assistant", "timestamp": ts(1),
		"message": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": `<!-- {"marker":"irrlicht-eta","total_rounds":4,"completed_rounds":1} -->`},
			},
		},
	})
	if ev.TaskEstimate == nil || ev.TaskEstimate.TotalRounds != 4 {
		t.Fatalf("TaskEstimate = %+v, want 1/4 from nested message.content", ev.TaskEstimate)
	}
}

// Marker early in a long message must survive — AssistantText keeps only the
// last 200 runes.
func TestParser_TaskEstimate_SurvivesLongMessage(t *testing.T) {
	p := &Parser{}
	long := `<!-- {"marker":"irrlicht-eta","total_rounds":5,"completed_rounds":3} --> `
	for i := 0; i < 50; i++ {
		long += "filler prose "
	}
	ev := p.ParseLine(map[string]interface{}{
		"type": "message", "role": "assistant", "timestamp": ts(1),
		"content": []interface{}{
			map[string]interface{}{"type": "output_text", "text": long},
		},
	})
	if ev.TaskEstimate == nil || ev.TaskEstimate.CompletedRounds != 3 {
		t.Fatalf("TaskEstimate = %+v, want 3/5 despite truncated AssistantText", ev.TaskEstimate)
	}
}

func TestParser_TaskEstimate_UserMessageIgnored(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type": "message", "role": "user", "timestamp": ts(0),
		"content": []interface{}{
			map[string]interface{}{"type": "input_text", "text": `<!-- {"marker":"irrlicht-eta","total_rounds":5,"completed_rounds":1} -->`},
		},
	})
	if ev.TaskEstimate != nil {
		t.Fatalf("user-pasted marker must not feed the estimate, got %+v", ev.TaskEstimate)
	}
}
