package tailer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/capacity"
)

// testCapacityFixture is the synthetic LiteLLM-like model table used by all
// tailer tests. It is intentionally deterministic (no dependency on the
// on-disk LiteLLM cache) and contains every model referenced by tests.
var testCapacityFixture = map[string]capacity.ModelCapacity{
	"claude-sonnet-4-5": {
		ContextWindow: 1_000_000,
		MaxOutput:     64_000,
		Family:        "claude-4",
		DisplayName:   "Claude Sonnet 4.5",
		Pricing: &capacity.ModelPricing{
			InputPerMTok: 3.0, OutputPerMTok: 15.0,
			CacheReadPerMTok: 0.30, CacheCreationPerMTok: 3.75,
		},
	},
	"claude-opus-4-6": {
		ContextWindow: 1_000_000,
		MaxOutput:     64_000,
		Family:        "claude-4",
		DisplayName:   "Claude Opus 4.6",
		Pricing: &capacity.ModelPricing{
			InputPerMTok: 15.0, OutputPerMTok: 75.0,
			CacheReadPerMTok: 1.875, CacheCreationPerMTok: 18.75,
		},
	},
	"claude-sonnet-4-6": {
		ContextWindow: 1_000_000,
		MaxOutput:     64_000,
		Family:        "claude-4",
		DisplayName:   "Claude Sonnet 4.6",
		Pricing: &capacity.ModelPricing{
			InputPerMTok: 3.0, OutputPerMTok: 15.0,
			CacheReadPerMTok: 0.30, CacheCreationPerMTok: 3.75,
		},
	},
	"claude-haiku-4-5": {
		ContextWindow: 200_000,
		MaxOutput:     64_000,
		Family:        "claude-4",
		DisplayName:   "Claude Haiku 4.5",
		Pricing: &capacity.ModelPricing{
			InputPerMTok: 0.80, OutputPerMTok: 4.0,
			CacheReadPerMTok: 0.08, CacheCreationPerMTok: 1.0,
		},
	},
	"claude-opus-4-1": {
		ContextWindow: 200_000,
		MaxOutput:     64_000,
		Family:        "claude-4",
		DisplayName:   "Claude Opus 4.1",
		Pricing: &capacity.ModelPricing{
			InputPerMTok: 15.0, OutputPerMTok: 75.0,
			CacheReadPerMTok: 1.875, CacheCreationPerMTok: 18.75,
		},
	},
	"gpt-5.3-codex": {
		ContextWindow: 256_000,
		MaxOutput:     32_768,
		Family:        "gpt-5",
		DisplayName:   "GPT-5.3 Codex",
		Pricing: &capacity.ModelPricing{
			InputPerMTok: 2.0, OutputPerMTok: 8.0,
		},
	},
	"gpt-5.4": {
		ContextWindow: 258_400,
		MaxOutput:     32_768,
		Family:        "gpt-5",
		DisplayName:   "GPT-5.4",
		Pricing: &capacity.ModelPricing{
			InputPerMTok: 2.0, OutputPerMTok: 8.0,
		},
	},
	"gpt-5.9-codex-preview": {
		ContextWindow: 256_000,
		MaxOutput:     32_768,
		Family:        "gpt-5",
		DisplayName:   "GPT-5.9 Codex Preview",
		Pricing: &capacity.ModelPricing{
			InputPerMTok: 2.0, OutputPerMTok: 8.0,
		},
	},
}

// testParser is a minimal TranscriptParser for tests. It handles the basic
// event types used in test fixtures (Claude Code-like format) and emits
// PerTurnContribution to exercise the new cumByModel cost accumulation path.
type testParser struct {
	lastRequestID  string
	pendingContrib *PerTurnContribution
	cumCursor      UsageBreakdown // for Codex-style cumulative_usage dedup
}

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
	var modelName string
	if message, ok := raw["message"].(map[string]interface{}); ok {
		if model, ok := message["model"].(string); ok && model != "" {
			modelName = model
			ev.ModelName = model
		}
		if usage, ok := message["usage"].(map[string]interface{}); ok {
			ev.Tokens = ExtractUsage(usage)
		}
	}

	// Claude Code-style requestId dedup — emit Contribution when turn changes.
	if reqID, ok := raw["requestId"].(string); ok && reqID != "" {
		ev.RequestID = reqID // kept for context-util snapshot
		if reqID != p.lastRequestID {
			if p.lastRequestID != "" && p.pendingContrib != nil {
				ev.Contribution = p.pendingContrib
			}
			p.lastRequestID = reqID
			p.pendingContrib = nil
		}
		if ev.Tokens != nil {
			p.pendingContrib = &PerTurnContribution{
				Model: modelName,
				Usage: UsageBreakdown{
					Input:     ev.Tokens.Input,
					Output:    ev.Tokens.Output,
					CacheRead: ev.Tokens.CacheRead,
					// Treat single bucket as 5m cache writes.
					CacheCreation5m: ev.Tokens.CacheCreation,
				},
			}
		}
	}

	// Codex-style cumulative_usage — emit delta as Contribution.
	if cumUsage, ok := raw["cumulative_usage"].(map[string]interface{}); ok {
		cum := ExtractUsage(cumUsage)
		ev.CumulativeTokens = cum // keep for legacy compat during transition
		if cum != nil {
			cur := UsageBreakdown{
				Input:     cum.Input,
				Output:    cum.Output,
				CacheRead: cum.CacheRead,
			}
			delta := UsageBreakdown{
				Input:     max(0, cur.Input-p.cumCursor.Input),
				Output:    max(0, cur.Output-p.cumCursor.Output),
				CacheRead: max(0, cur.CacheRead-p.cumCursor.CacheRead),
			}
			if delta.Input > 0 || delta.Output > 0 || delta.CacheRead > 0 {
				ev.Contribution = &PerTurnContribution{
					Model: modelName,
					Usage: delta,
				}
				p.cumCursor = cur
			}
		}
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
						id, _ := block["id"].(string)
						name, _ := block["name"].(string)
						if name != "" {
							ev.ToolUses = append(ev.ToolUses, ToolUse{ID: id, Name: name})
						}
					case "tool_result":
						if toolUseID, ok := block["tool_use_id"].(string); ok && toolUseID != "" {
							ev.ToolResultIDs = append(ev.ToolResultIDs, toolUseID)
						}
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
		id, _ := raw["id"].(string)
		name, _ := raw["name"].(string)
		if name != "" {
			ev.ToolUses = append(ev.ToolUses, ToolUse{ID: id, Name: name})
		}
	case "tool_call":
		// Legacy format: {"tool_call": {"name": "Bash"}}
		name := ""
		id := ""
		if tc, ok := raw["tool_call"].(map[string]interface{}); ok {
			name, _ = tc["name"].(string)
			id, _ = tc["id"].(string)
		}
		if name != "" {
			ev.ToolUses = append(ev.ToolUses, ToolUse{ID: id, Name: name})
		}
	case "tool_result":
		if id, ok := raw["tool_use_id"].(string); ok && id != "" {
			ev.ToolResultIDs = append(ev.ToolResultIDs, id)
		}
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

// PendingContribution exposes the in-progress turn to the tailer (implements pendingContributor).
func (p *testParser) PendingContribution() *PerTurnContribution {
	return p.pendingContrib
}

// newTestTailer creates a TranscriptTailer with the testParser and the
// deterministic testCapacityFixture capacity manager. Tests must not depend
// on the real on-disk LiteLLM cache.
func newTestTailer(path string) *TranscriptTailer {
	t := NewTranscriptTailer(path, &testParser{}, "claude-code")
	t.capacityMgr = capacity.NewForTest(testCapacityFixture)
	return t
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
		{"type": "tool_use", "id": "tu_1", "name": "Bash", "timestamp": ts(1)},
		{"type": "tool_result", "tool_use_id": "tu_1", "timestamp": ts(2)},
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
		{"type": "tool_use", "id": "tu_1", "name": "Bash", "timestamp": ts(1)},
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
		{"type": "tool_use", "id": "tu_read", "name": "Read", "timestamp": ts(1)},
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
		"type":        "tool_result",
		"tool_use_id": "tu_read",
		"timestamp":   ts(2),
		"output":      strings.Repeat("x", 120*1024),
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
		{"type": "tool_use", "id": "tu_1", "name": "Bash", "timestamp": ts(0)},
		{"type": "tool_use", "id": "tu_2", "name": "Read", "timestamp": ts(1)},
		{"type": "tool_use", "id": "tu_3", "name": "Grep", "timestamp": ts(2)},
		{"type": "tool_result", "tool_use_id": "tu_1", "timestamp": ts(3)},
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
		{"type": "tool_use", "id": "tu_1", "name": "Bash", "timestamp": ts(1)},
		{"type": "tool_use", "id": "tu_2", "name": "Bash", "timestamp": ts(2)},
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
	// the Agent entry so the claudecode adapter's CountOpenSubagents can still count in-process
	// sub-agents. Only non-Agent leaks get swept.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "tool_use", "id": "tu_1", "name": "Bash", "timestamp": ts(1)},  // leak
		{"type": "tool_use", "id": "tu_2", "name": "Agent", "timestamp": ts(2)}, // legit subagent
		{"type": "tool_use", "id": "tu_3", "name": "Read", "timestamp": ts(3)},  // leak
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
		{"tool_call": map[string]interface{}{"name": "Bash", "id": "tu_1"}, "timestamp": ts(1)},
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
	// tool_use events. Orphan result IDs are harmless no-ops on the map.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "tool_result", "tool_use_id": "tu_orphan1", "timestamp": ts(0)},
		{"type": "tool_result", "tool_use_id": "tu_orphan2", "timestamp": ts(1)},
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
		{"type": "tool_use", "id": "tu_1", "name": "Bash", "timestamp": ts(0)},
		{"type": "tool_result", "tool_use_id": "tu_1", "timestamp": ts(1)},
		{"type": "tool_use", "id": "tu_2", "name": "Read", "timestamp": ts(2)},
		{"type": "tool_result", "tool_use_id": "tu_2", "timestamp": ts(3)},
		{"type": "tool_use", "id": "tu_3", "name": "Grep", "timestamp": ts(4)},
		{"type": "tool_result", "tool_use_id": "tu_3", "timestamp": ts(5)},
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

// --- ExtractAssistantText tail-storage tests ---

func TestExtractAssistantText_LongText_StoredAsTail(t *testing.T) {
	// 300-rune text: first 100 are 'A', last 200 are 'B…B?'
	// After the change, only the last 200 runes are kept (prefixed with "…").
	prefix := strings.Repeat("A", 100)
	suffix := strings.Repeat("B", 199) + "?"
	longText := prefix + suffix // 300 runes total
	raw := map[string]interface{}{
		"message": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": longText},
			},
		},
	}
	got := ExtractAssistantText(raw)
	if !strings.HasPrefix(got, "…") {
		t.Errorf("expected leading '…', got %q", got[:min(10, len(got))])
	}
	if !strings.HasSuffix(got, "?") {
		t.Errorf("expected trailing '?', got suffix %q", got[max(0, len(got)-5):])
	}
	if strings.ContainsRune(got, 'A') {
		t.Errorf("expected head (all-A prefix) to be cut, but 'A' found in %q", got[:min(30, len(got))])
	}
}

func TestExtractAssistantText_ShortText_NoEllipsis(t *testing.T) {
	raw := map[string]interface{}{
		"message": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Should I proceed?"},
			},
		},
	}
	got := ExtractAssistantText(raw)
	if got != "Should I proceed?" {
		t.Errorf("got %q, want exact text unchanged", got)
	}
}

func TestLastAssistantText_LongQuestion_TailDetectedAsWaiting(t *testing.T) {
	// 251-rune question: exceeds 200-rune limit, but ends with '?'.
	// The tail-storage fix ensures IsWaitingForUserInput() sees the '?'.
	longQuestion := strings.Repeat("x", 250) + "?"
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "end_turn",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": longQuestion},
			},
		}},
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(2)},
	})
	m, err := newTestTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(m.LastAssistantText, "?") {
		t.Errorf("LastAssistantText %q does not end with '?'", m.LastAssistantText)
	}
	// Downstream state classification reads LastAssistantText via the domain
	// helper; verify the tail-storage fix still trips waiting detection.
	dm := &session.SessionMetrics{LastAssistantText: m.LastAssistantText}
	if !dm.IsWaitingForUserInput() {
		t.Error("IsWaitingForUserInput() = false, want true for long question ending with '?'")
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
	// must still contain them so the claudecode adapter's CountOpenSubagents can count them.
	path := writeTranscriptLines(t, []map[string]interface{}{
		// 3 streaming assistant chunks, each with one Agent tool_use
		{"type": "assistant", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "id": "tu_agent1", "name": "Agent"},
			},
		}},
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "id": "tu_agent2", "name": "Agent"},
			},
		}},
		{"type": "assistant", "timestamp": ts(2), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "id": "tu_agent3", "name": "Agent"},
			},
		}},
		// First tool_result arrives (user event with embedded tool_result)
		{"type": "user", "timestamp": ts(3), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_agent1", "content": "done"},
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
	// should be preserved so the claudecode adapter's CountOpenSubagents can detect them.
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
				map[string]interface{}{"type": "tool_use", "id": "tu_agent1", "name": "Agent"},
			},
		}},
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "id": "tu_agent2", "name": "Agent"},
			},
		}},
		{"type": "assistant", "timestamp": ts(2), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "id": "tu_agent3", "name": "Agent"},
			},
		}},
		// All 3 results
		{"type": "user", "timestamp": ts(3), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_agent1", "content": "done"},
			},
		}},
		{"type": "user", "timestamp": ts(4), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_agent2", "content": "done"},
			},
		}},
		{"type": "user", "timestamp": ts(5), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_agent3", "content": "done"},
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

// --- Issue #117: id-based tool tracking tests ---

func TestIDTracking_DuplicateToolUseID(t *testing.T) {
	// Duplicate tool_use with same ID (e.g. multi-line streaming split) should
	// be idempotent — one delete removes it.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "tool_use", "id": "tu_1", "name": "Bash", "timestamp": ts(0)},
		{"type": "tool_use", "id": "tu_1", "name": "Bash", "timestamp": ts(1)}, // duplicate
		{"type": "tool_result", "tool_use_id": "tu_1", "timestamp": ts(2)},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false: duplicate ID should be idempotent")
	}
	if m.OpenToolCallCount != 0 {
		t.Errorf("expected OpenToolCallCount=0, got %d", m.OpenToolCallCount)
	}
}

func TestIDTracking_OrphanToolResultID(t *testing.T) {
	// Orphan tool_result with unknown ID (from --continue/compact replay) should
	// be a no-op — the real entry must survive.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "tool_use", "id": "tu_1", "name": "Bash", "timestamp": ts(0)},
		{"type": "tool_result", "tool_use_id": "tu_unknown", "timestamp": ts(1)}, // orphan
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true: orphan result should not remove the real entry")
	}
	if m.OpenToolCallCount != 1 {
		t.Errorf("expected OpenToolCallCount=1, got %d", m.OpenToolCallCount)
	}

	// Now close the real one.
	appendTranscriptLine(t, path, map[string]interface{}{
		"type": "tool_result", "tool_use_id": "tu_1", "timestamp": ts(2),
	})
	m, err = tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false after real result arrives")
	}
}

func TestIDTracking_ParallelOutOfOrder(t *testing.T) {
	// 3 parallel tool_use events, results arrive in reverse order.
	// With the old FIFO this would corrupt state; with id-keyed map each
	// delete targets the correct entry.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "tool_use", "id": "tu_1", "name": "Bash", "timestamp": ts(0)},
		{"type": "tool_use", "id": "tu_2", "name": "Read", "timestamp": ts(1)},
		{"type": "tool_use", "id": "tu_3", "name": "Grep", "timestamp": ts(2)},
		{"type": "tool_result", "tool_use_id": "tu_3", "timestamp": ts(3)}, // last finishes first
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.OpenToolCallCount != 2 {
		t.Errorf("expected 2 open after tu_3 result, got %d", m.OpenToolCallCount)
	}

	appendTranscriptLine(t, path, map[string]interface{}{
		"type": "tool_result", "tool_use_id": "tu_1", "timestamp": ts(4),
	})
	m, err = tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.OpenToolCallCount != 1 {
		t.Errorf("expected 1 open after tu_1 result, got %d", m.OpenToolCallCount)
	}
	if len(m.LastOpenToolNames) != 1 || m.LastOpenToolNames[0] != "Read" {
		t.Errorf("expected [Read] remaining, got %v", m.LastOpenToolNames)
	}

	appendTranscriptLine(t, path, map[string]interface{}{
		"type": "tool_result", "tool_use_id": "tu_2", "timestamp": ts(5),
	})
	m, err = tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false after all results")
	}
}

func TestIDTracking_EmptyID(t *testing.T) {
	// Empty-ID tool_use is skipped by the tailer's insert guard (tu.ID != "").
	// Not tracked, but harmless — tests graceful degradation when a parser
	// fails to extract an ID from a transcript format.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "tool_use", "id": "", "name": "Bash", "timestamp": ts(0)},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false: empty ID should be skipped")
	}
}

func TestIDTracking_AgentSurvivesTurnDone(t *testing.T) {
	// Agent + non-Agent open, turn_done sweeps only non-Agent.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "tool_use", "id": "tu_agent", "name": "Agent", "timestamp": ts(0)},
		{"type": "tool_use", "id": "tu_bash", "name": "Bash", "timestamp": ts(1)}, // leaked
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(2)},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true with Agent still open")
	}
	if m.OpenToolCallCount != 1 {
		t.Errorf("expected OpenToolCallCount=1 (Agent only), got %d", m.OpenToolCallCount)
	}
	if len(m.LastOpenToolNames) != 1 || m.LastOpenToolNames[0] != "Agent" {
		t.Errorf("expected [Agent], got %v", m.LastOpenToolNames)
	}
}

func TestIDTracking_SendMessageSurvivesTurnDone(t *testing.T) {
	// Claude Code 2.1.77 replaced Agent({resume}) with SendMessage({to}) for
	// resuming background sub-agents. SendMessage's tool_result arrives after
	// turn_done just like Agent's, so it must survive the sweep.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "tool_use", "id": "tu_send", "name": "SendMessage", "timestamp": ts(0)},
		{"type": "tool_use", "id": "tu_bash", "name": "Bash", "timestamp": ts(1)}, // leaked
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(2)},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true with SendMessage still open")
	}
	if m.OpenToolCallCount != 1 {
		t.Errorf("expected OpenToolCallCount=1 (SendMessage only), got %d", m.OpenToolCallCount)
	}
	if len(m.LastOpenToolNames) != 1 || m.LastOpenToolNames[0] != "SendMessage" {
		t.Errorf("expected [SendMessage], got %v", m.LastOpenToolNames)
	}
}

func TestSurviveTurnDone(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"Agent", true},
		{"SendMessage", true},
		{"AskUserQuestion", true},
		{"ExitPlanMode", true},
		{"Bash", false},
		{"Read", false},
		{"Write", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := surviveTurnDone(tt.name); got != tt.want {
				t.Errorf("surviveTurnDone(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestHasOpenToolCall_TurnDonePreservesAskUserQuestion(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "tool_use", "id": "tu_ask", "name": "AskUserQuestion", "timestamp": ts(1)},
		{"type": "system", "subtype": "stop_hook_summary", "timestamp": ts(2)},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true with AskUserQuestion still open after turn_done")
	}
	if m.OpenToolCallCount != 1 {
		t.Errorf("expected OpenToolCallCount=1, got %d", m.OpenToolCallCount)
	}
	if len(m.LastOpenToolNames) != 1 || m.LastOpenToolNames[0] != "AskUserQuestion" {
		t.Errorf("expected [AskUserQuestion], got %v", m.LastOpenToolNames)
	}
}

func TestHasOpenToolCall_TurnDonePreservesExitPlanMode(t *testing.T) {
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0)},
		{"type": "tool_use", "id": "tu_plan", "name": "ExitPlanMode", "timestamp": ts(1)},
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(2)},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true with ExitPlanMode still open after turn_done")
	}
	if m.OpenToolCallCount != 1 {
		t.Errorf("expected OpenToolCallCount=1, got %d", m.OpenToolCallCount)
	}
	if len(m.LastOpenToolNames) != 1 || m.LastOpenToolNames[0] != "ExitPlanMode" {
		t.Errorf("expected [ExitPlanMode], got %v", m.LastOpenToolNames)
	}
}

func TestIDTracking_UserBlockingToolsSurviveTurnDone(t *testing.T) {
	// AskUserQuestion survives turn_done while Bash is swept.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "tool_use", "id": "tu_ask", "name": "AskUserQuestion", "timestamp": ts(0)},
		{"type": "tool_use", "id": "tu_bash", "name": "Bash", "timestamp": ts(1)}, // leaked
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(2)},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true with AskUserQuestion still open")
	}
	if m.OpenToolCallCount != 1 {
		t.Errorf("expected OpenToolCallCount=1 (AskUserQuestion only), got %d", m.OpenToolCallCount)
	}
	if len(m.LastOpenToolNames) != 1 || m.LastOpenToolNames[0] != "AskUserQuestion" {
		t.Errorf("expected [AskUserQuestion], got %v", m.LastOpenToolNames)
	}
}

func TestHasOpenToolCall_TurnDonePreservesMultipleSurvivors(t *testing.T) {
	// Agent + AskUserQuestion both survive, Read is swept.
	path := writeTranscriptLines(t, []map[string]interface{}{
		{"type": "tool_use", "id": "tu_agent", "name": "Agent", "timestamp": ts(0)},
		{"type": "tool_use", "id": "tu_ask", "name": "AskUserQuestion", "timestamp": ts(1)},
		{"type": "tool_use", "id": "tu_read", "name": "Read", "timestamp": ts(2)}, // leaked
		{"type": "system", "subtype": "turn_duration", "timestamp": ts(3)},
	})

	tailer := newTestTailer(path)
	m, err := tailer.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true with Agent and AskUserQuestion still open")
	}
	if m.OpenToolCallCount != 2 {
		t.Errorf("expected OpenToolCallCount=2, got %d", m.OpenToolCallCount)
	}
	nameSet := map[string]bool{}
	for _, n := range m.LastOpenToolNames {
		nameSet[n] = true
	}
	if !nameSet["Agent"] || !nameSet["AskUserQuestion"] {
		t.Errorf("expected Agent and AskUserQuestion in LastOpenToolNames, got %v", m.LastOpenToolNames)
	}
	if nameSet["Read"] {
		t.Error("Read should have been swept by turn_done")
	}
}
