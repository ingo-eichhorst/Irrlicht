package hermes

import (
	"strings"
	"testing"
)

// realToolCallsJSON is a verbatim `messages.tool_calls` value captured from a
// live Hermes store (ids shortened only in that they were already numeric).
const realToolCallsJSON = `[{"id": "888148639", "call_id": "888148639", ` +
	`"response_item_id": "fc_888148639", "type": "function", ` +
	`"function": {"name": "terminal", "arguments": "{\"command\":\"ls /tmp\"}"}}]`

func TestParseLine_UserMessage(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		keyRole:      "user",
		keyContent:   "fix the build",
		keyTimestamp: 1785693388.42541,
	})
	if ev == nil || ev.Skip {
		t.Fatal("user row must produce an event")
	}
	if ev.EventType != "user_message" {
		t.Errorf("EventType = %q, want user_message", ev.EventType)
	}
	if !ev.ClearToolNames {
		t.Error("a user message must clear open tool state")
	}
	if ev.Timestamp.IsZero() {
		t.Error("Timestamp must be parsed from the REAL epoch-seconds column")
	}
	// The column is REAL seconds, not epoch-ms: a naive UnixMilli read would
	// land in 1970.
	if got := ev.Timestamp.Year(); got != 2026 {
		t.Errorf("Timestamp.Year() = %d, want 2026 (epoch SECONDS, not millis)", got)
	}
}

// A model_switch row is bookkeeping, not a prompt. Treating it as a user
// message would clear the open-tool set and restart the turn mid-flight.
func TestParseLine_ModelSwitchIsSkipped(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		keyRole:        "user",
		keyDisplayKind: "model_switch",
	})
	if ev == nil || !ev.Skip {
		t.Fatalf("model_switch row must be skipped, got %+v", ev)
	}
}

func TestParseLine_AssistantTurnDone(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		keyRole:    "assistant",
		keyContent: "all done",
		keyFinish:  "stop",
		keyModel:   "gpt-5.6-luna",
	})
	if ev == nil || ev.Skip {
		t.Fatal("assistant row must produce an event")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want turn_done — finish_reason=stop is Hermes' explicit turn boundary", ev.EventType)
	}
	if ev.AssistantText != "all done" {
		t.Errorf("AssistantText = %q", ev.AssistantText)
	}
	if ev.ModelName != "gpt-5.6-luna" {
		t.Errorf("ModelName = %q", ev.ModelName)
	}
}

func TestParseLine_AssistantToolCallsDoesNotEndTurn(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		keyRole:      "assistant",
		keyFinish:    "tool_calls",
		keyToolCalls: realToolCallsJSON,
	})
	if ev == nil || ev.Skip {
		t.Fatal("assistant row must produce an event")
	}
	if ev.EventType == "turn_done" {
		t.Error("finish_reason=tool_calls means work continues — must NOT end the turn")
	}
	if len(ev.ToolUses) != 1 {
		t.Fatalf("len(ToolUses) = %d, want 1", len(ev.ToolUses))
	}
	if ev.ToolUses[0].ID != "888148639" {
		t.Errorf("ToolUse.ID = %q, want 888148639", ev.ToolUses[0].ID)
	}
	if ev.ToolUses[0].Name != "terminal" {
		t.Errorf("ToolUse.Name = %q, want terminal", ev.ToolUses[0].Name)
	}
}

// The `tool` row closes the call by tool_call_id — the id the assistant row
// opened. If these two ever disagree, open tool calls leak forever and the
// session never settles.
func TestParseLine_ToolResultClosesByCallID(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		keyRole:       "tool",
		keyToolName:   "terminal",
		keyToolCallID: "888148639",
		keyContent:    `{"output":"..."}`,
	})
	if ev == nil || ev.Skip {
		t.Fatal("tool row must produce an event")
	}
	if ev.EventType != "tool_result" {
		t.Errorf("EventType = %q, want tool_result", ev.EventType)
	}
	if len(ev.ToolResultIDs) != 1 || ev.ToolResultIDs[0] != "888148639" {
		t.Errorf("ToolResultIDs = %v, want [888148639]", ev.ToolResultIDs)
	}
}

func TestParseLine_UnknownRoleIsSkipped(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{keyRole: "system"})
	if ev == nil || !ev.Skip {
		t.Fatalf("unknown role must be skipped, got %+v", ev)
	}
	if ev := p.ParseLine(map[string]interface{}{}); ev != nil {
		t.Errorf("a row with no role must return nil, got %+v", ev)
	}
}

func TestParseToolCalls_Malformed(t *testing.T) {
	for _, in := range []interface{}{nil, "", "not json", `{"not":"an array"}`, `[]`} {
		if got := parseToolCalls(in); got != nil {
			t.Errorf("parseToolCalls(%v) = %v, want nil", in, got)
		}
	}
	// call_id is the documented fallback when id is absent.
	got := parseToolCalls(`[{"call_id":"c1","function":{"name":"read"}}]`)
	if len(got) != 1 || got[0].ID != "c1" {
		t.Errorf("call_id fallback failed: %v", got)
	}
}

// AssistantText is tail-truncated because a waiting cue ("...shall I
// proceed?") sits at the END of the text.
func TestTruncateRunesKeepsTail(t *testing.T) {
	long := strings.Repeat("a", 250) + "QUESTION?"
	got := truncateRunes(long, maxAssistantText)
	if len([]rune(got)) != maxAssistantText {
		t.Errorf("len = %d, want %d", len([]rune(got)), maxAssistantText)
	}
	if !strings.HasSuffix(got, "QUESTION?") {
		t.Error("truncation must keep the tail, where waiting cues live")
	}
	if got := truncateRunes("short", maxAssistantText); got != "short" {
		t.Errorf("short strings must pass through, got %q", got)
	}
}
