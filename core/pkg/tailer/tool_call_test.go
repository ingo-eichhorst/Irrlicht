package tailer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testParser is a minimal TranscriptParser for tests. It handles the basic
// event types used in test fixtures (Claude Code-like format).
type testParser struct{}

func (p *testParser) ParseLine(raw map[string]interface{}) *ParsedEvent {
	ev := &ParsedEvent{Timestamp: ParseTimestamp(raw)}

	eventType := "unknown"
	if et, ok := raw["type"].(string); ok {
		eventType = et
	} else if _, ok := raw["user_input"]; ok {
		eventType = "user_message"
	} else if _, ok := raw["assistant_output"]; ok {
		eventType = "assistant_message"
	} else if _, ok := raw["tool_call"]; ok {
		eventType = "tool_call"
	}

	// System events.
	if eventType == "system" {
		if subtype, _ := raw["subtype"].(string); subtype == "turn_duration" || subtype == "stop_hook_summary" {
			ev.EventType = "turn_done"
			return ev
		}
		ev.Skip = true
		return ev
	}

	// Local command filtering.
	if eventType == "user" {
		if isMeta, ok := raw["isMeta"].(bool); ok && isMeta {
			ev.Skip = true
			return ev
		}
		if msg, ok := raw["message"].(map[string]interface{}); ok {
			if content, ok := msg["content"].(string); ok {
				if len(content) > 0 && content[0] == '<' {
					ev.Skip = true
					return ev
				}
			}
		}
	}

	// Permission mode.
	if eventType == "permission-mode" {
		if mode, ok := raw["permissionMode"].(string); ok {
			ev.PermissionMode = mode
		}
		ev.Skip = true
		return ev
	}

	// Model/token extraction.
	if message, ok := raw["message"].(map[string]interface{}); ok {
		if model, ok := message["model"].(string); ok && model != "" {
			ev.ModelName = model
		}
		if usage, ok := message["usage"].(map[string]interface{}); ok {
			ev.Tokens = ExtractUsage(usage)
		}
	}
	// RequestID for cost deduplication.
	if reqID, ok := raw["requestId"].(string); ok {
		ev.RequestID = reqID
	}
	// CumulativeTokens (Codex-style).
	if cumUsage, ok := raw["cumulative_usage"].(map[string]interface{}); ok {
		ev.CumulativeTokens = ExtractUsage(cumUsage)
	}
	if cm, ok := raw["context_management"].(map[string]interface{}); ok {
		if cw, ok := cm["context_window"].(float64); ok && cw > 0 {
			ev.ContextWindow = int64(cw)
		}
	}
	ev.ContentChars = ExtractContentChars(raw)

	// Filter non-message events.
	switch eventType {
	case "user_message", "assistant_message", "tool_call", "tool_result",
		"user_input", "assistant_output", "user", "assistant", "tool_use", "message":
		// OK
	default:
		ev.Skip = true
		return ev
	}

	ev.EventType = eventType

	// Scan message.content[] for tool blocks.
	if msg, ok := raw["message"].(map[string]interface{}); ok {
		if contentArr, ok := msg["content"].([]interface{}); ok {
			for _, item := range contentArr {
				if block, ok := item.(map[string]interface{}); ok {
					switch block["type"] {
					case "tool_use":
						if name, ok := block["name"].(string); ok {
							ev.ToolUseNames = append(ev.ToolUseNames, name)
						}
					case "tool_result":
						ev.ToolResultCount++
						if isErr, ok := block["is_error"].(bool); ok && isErr {
							ev.IsError = true
						}
					}
				}
			}
		}
	}

	// Top-level tool events (not embedded in message.content[]).
	switch eventType {
	case "tool_use":
		name, _ := raw["name"].(string)
		ev.ToolUseNames = append(ev.ToolUseNames, name)
	case "tool_call":
		// Legacy format: {"tool_call": {"name": "Bash"}}
		name := ""
		if tc, ok := raw["tool_call"].(map[string]interface{}); ok {
			name, _ = tc["name"].(string)
		}
		ev.ToolUseNames = append(ev.ToolUseNames, name)
	case "tool_result":
		ev.ToolResultCount++
	}

	// Assistant text.
	switch eventType {
	case "assistant", "assistant_message", "assistant_output":
		ev.AssistantText = ExtractAssistantText(raw)
	case "user", "user_message", "user_input":
		ev.ClearToolNames = true
	}

	return ev
}

// newTestTailer creates a TranscriptTailer with the testParser for unit tests.
func newTestTailer(path string) *TranscriptTailer {
	return NewTranscriptTailer(path, &testParser{}, "claude-code")
}

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

func appendTranscriptLine(t *testing.T, path string, line map[string]interface{}) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(line); err != nil {
		t.Fatal(err)
	}
}

func ts(offset int) string {
	return time.Now().Add(time.Duration(offset) * time.Second).Format(time.RFC3339)
}

func TestHasOpenToolCall_NoToolEvents(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1)},
	})

	tailer := newTestTailer(path)
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

	tailer := newTestTailer(path)
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

	tailer := newTestTailer(path)
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

func TestTailAndProcess_LargeAppendedToolResult_NotSkipped(t *testing.T) {
	// Regression: if >64KB is appended between polls, we must continue from
	// lastOffset and parse the full new JSON line instead of skipping into it.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "tool_use", "timestamp": ts(1), "name": "Read"},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenToolCall || m.OpenToolCallCount != 1 {
		t.Fatalf("setup failed: expected one open call, got open=%v count=%d", m.HasOpenToolCall, m.OpenToolCallCount)
	}

	appendTranscriptLine(t, path, map[string]interface{}{
		"type":      "tool_result",
		"timestamp": ts(2),
		"output":    strings.Repeat("x", 120*1024),
	})

	m, err = tailer.TailAndProcess()
	if err != nil {
		t.Fatalf("unexpected tail error on large appended line: %v", err)
	}
	if m.HasOpenToolCall || m.OpenToolCallCount != 0 {
		t.Fatalf("expected large tool_result to close call, got open=%v count=%d", m.HasOpenToolCall, m.OpenToolCallCount)
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

	tailer := newTestTailer(path)
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

func TestHasOpenToolCall_TurnDoneReconciles(t *testing.T) {
	// Regression for #114: if the FIFO has stale entries (e.g. from an
	// orphan tool_result or a multi-line assistant split), turn_done must
	// reconcile them so the classifier can transition working → ready.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "tool_use", "timestamp": ts(1), "name": "Bash"},
		{"type": "tool_use", "timestamp": ts(2), "name": "Bash"},
		// No matching tool_results — simulates the phantom-leak state.
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(3)},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false after turn_done reconciliation")
	}
	if m.OpenToolCallCount != 0 {
		t.Errorf("expected OpenToolCallCount=0, got %d", m.OpenToolCallCount)
	}
	if len(m.LastOpenToolNames) != 0 {
		t.Errorf("expected LastOpenToolNames empty, got %v", m.LastOpenToolNames)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("expected LastEventType=turn_done, got %q", m.LastEventType)
	}
}

func TestHasOpenToolCall_TurnDonePreservesAgent(t *testing.T) {
	// Defensive: if turn_done ever arrives while an Agent tool_use is still
	// open (a sub-agent running in the background — see the IsAgentDone
	// override in session.go), the reconciliation from #114 must preserve
	// the Agent entry so InferSubagents can still count in-process
	// sub-agents. Only non-Agent leaks get swept.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "tool_use", "timestamp": ts(1), "name": "Bash"},  // leak
		{"type": "tool_use", "timestamp": ts(2), "name": "Agent"}, // legit subagent
		{"type": "tool_use", "timestamp": ts(3), "name": "Read"},  // leak
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(4)},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true with Agent still open after turn_done")
	}
	if m.OpenToolCallCount != 1 {
		t.Errorf("expected OpenToolCallCount=1, got %d", m.OpenToolCallCount)
	}
	if len(m.LastOpenToolNames) != 1 || m.LastOpenToolNames[0] != "Agent" {
		t.Errorf("expected LastOpenToolNames=[Agent], got %v", m.LastOpenToolNames)
	}
}

func TestHasOpenToolCall_ToolCallEventType(t *testing.T) {
	// The "tool_call" event type (legacy format) should also be counted
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"tool_call": map[string]interface{}{"name": "Bash"}, "timestamp": ts(1)},
		// No matching tool_result
	})

	tailer := newTestTailer(path)
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

	tailer := newTestTailer(path)
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

	tailer := newTestTailer(path)
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
	m, err := newTestTailer(path).TailAndProcess()
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
	m, err := newTestTailer(path).TailAndProcess()
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

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("expected LastEventType=%q after local commands, got %q", "turn_done", m.LastEventType)
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

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "user" {
		t.Errorf("expected LastEventType=%q for normal user message, got %q", "user", m.LastEventType)
	}
}

// --- Agent subagent tool name tracking tests (issue #88) ---

func TestLastOpenToolNames_AgentToolsPreservedAfterPartialResults(t *testing.T) {
	// Simulate Claude Code format: 3 streaming assistant events each with one
	// Agent tool_use, followed by 1 user event carrying a tool_result.
	// After the first result, 2 Agent calls remain open — LastOpenToolNames
	// must still contain them so InferSubagents can count them.
	path := writeTranscriptLines(t, []map[string]interface{}{
		// 3 streaming assistant chunks, each with one Agent tool_use
		{"type": "assistant", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Agent"},
			},
		}},
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Agent"},
			},
		}},
		{"type": "assistant", "timestamp": ts(2), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Agent"},
			},
		}},
		// First tool_result arrives (user event with embedded tool_result)
		{"type": "user", "timestamp": ts(3), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "content": "done"},
			},
		}},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	// 3 uses - 1 result = 2 open
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true with 2 unmatched Agent calls")
	}
	if m.OpenToolCallCount != 2 {
		t.Errorf("expected OpenToolCallCount=2, got %d", m.OpenToolCallCount)
	}

	// BUG (issue #88): ClearToolNames on the user event wipes LastOpenToolNames
	// even though tool_result blocks are present. The remaining 2 Agent names
	// should be preserved so InferSubagents can detect them.
	if len(m.LastOpenToolNames) != 2 {
		t.Errorf("expected LastOpenToolNames to have 2 entries, got %d: %v",
			len(m.LastOpenToolNames), m.LastOpenToolNames)
	}
	for i, name := range m.LastOpenToolNames {
		if name != "Agent" {
			t.Errorf("LastOpenToolNames[%d] = %q, want \"Agent\"", i, name)
		}
	}
}

func TestLastOpenToolNames_AllAgentResultsCleared(t *testing.T) {
	// All 3 Agent tool_results arrive — verify everything is properly zeroed out.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "assistant", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Agent"},
			},
		}},
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Agent"},
			},
		}},
		{"type": "assistant", "timestamp": ts(2), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Agent"},
			},
		}},
		// All 3 results
		{"type": "user", "timestamp": ts(3), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "content": "done"},
			},
		}},
		{"type": "user", "timestamp": ts(4), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "content": "done"},
			},
		}},
		{"type": "user", "timestamp": ts(5), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "content": "done"},
			},
		}},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false when all Agent calls are paired")
	}
	if m.OpenToolCallCount != 0 {
		t.Errorf("expected OpenToolCallCount=0, got %d", m.OpenToolCallCount)
	}
	if len(m.LastOpenToolNames) != 0 {
		t.Errorf("expected empty LastOpenToolNames, got %v", m.LastOpenToolNames)
	}
}
