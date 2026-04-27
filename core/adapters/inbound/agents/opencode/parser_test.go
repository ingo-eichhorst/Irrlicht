package opencode

import (
	"testing"
	"time"
)

func rawPart(fields map[string]interface{}) map[string]interface{} {
	base := map[string]interface{}{
		"_ts":   float64(time.Now().UnixMilli()),
		"_cwd":  "/some/project",
		"_role": "assistant",
	}
	for k, v := range fields {
		base[k] = v
	}
	return base
}

// --- step-start ---

func TestParser_StepStart_Skip(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type": "step-start",
	}))
	if ev == nil || !ev.Skip {
		t.Error("expected step-start to be skipped")
	}
}

// --- step-finish / turn_done ---

func TestParser_StepFinish_Stop_TurnDone(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":   "step-finish",
		"reason": "stop",
		"tokens": map[string]interface{}{
			"input":     float64(2892),
			"output":    float64(633),
			"reasoning": float64(0),
			"total":     float64(3525),
			"cache": map[string]interface{}{
				"read":  float64(46855),
				"write": float64(0),
			},
		},
		"cost":   float64(0.02172564),
		"_model": "claude-sonnet-4-5",
	}))
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.Skip {
		t.Error("step-finish stop should not be skipped")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want turn_done", ev.EventType)
	}
	if ev.Tokens == nil {
		t.Fatal("expected Tokens to be set")
	}
	if ev.Tokens.Input != 2892 {
		t.Errorf("Tokens.Input = %d, want 2892", ev.Tokens.Input)
	}
	if ev.Tokens.Output != 633 {
		t.Errorf("Tokens.Output = %d, want 633", ev.Tokens.Output)
	}
	if ev.Tokens.CacheRead != 46855 {
		t.Errorf("Tokens.CacheRead = %d, want 46855", ev.Tokens.CacheRead)
	}
	if ev.Contribution == nil {
		t.Fatal("expected Contribution to be set on reason=stop")
	}
	if ev.Contribution.Model != "claude-sonnet-4-5" {
		t.Errorf("Contribution.Model = %q, want claude-sonnet-4-5", ev.Contribution.Model)
	}
	if ev.Contribution.Usage.Input != 2892 {
		t.Errorf("Contribution.Usage.Input = %d, want 2892", ev.Contribution.Usage.Input)
	}
	if ev.Contribution.ProviderCostUSD == nil {
		t.Fatal("expected ProviderCostUSD to be set")
	}
	if *ev.Contribution.ProviderCostUSD != 0.02172564 {
		t.Errorf("ProviderCostUSD = %v, want 0.02172564", *ev.Contribution.ProviderCostUSD)
	}
}

func TestParser_StepFinish_ToolCalls_AssistantMessage(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":   "step-finish",
		"reason": "tool-calls",
		"tokens": map[string]interface{}{
			"input":  float64(1000),
			"output": float64(100),
			"total":  float64(1100),
			"cache":  map[string]interface{}{"read": float64(0), "write": float64(0)},
		},
		"cost": float64(0.001),
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "assistant_message" {
		t.Errorf("EventType = %q, want assistant_message", ev.EventType)
	}
	// No Contribution on tool-calls steps (turn not complete yet).
	if ev.Contribution != nil {
		t.Error("expected no Contribution on reason=tool-calls")
	}
}

// --- text part ---

func TestParser_TextPart_AssistantText(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type": "text",
		"text": "Here is the answer to your question.",
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "assistant_message" {
		t.Errorf("EventType = %q, want assistant_message", ev.EventType)
	}
	if ev.AssistantText != "Here is the answer to your question." {
		t.Errorf("AssistantText = %q", ev.AssistantText)
	}
}

func TestParser_TextPart_LongTruncated(t *testing.T) {
	p := &Parser{}
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type": "text",
		"text": string(long),
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if len(ev.AssistantText) != 200 {
		t.Errorf("AssistantText len = %d, want 200", len(ev.AssistantText))
	}
	if ev.ContentChars != 300 {
		t.Errorf("ContentChars = %d, want 300", ev.ContentChars)
	}
}

func TestParser_TextPart_UserMessage(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":  "text",
		"text":  "Please help me with this.",
		"_role": "user",
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "user_message" {
		t.Errorf("EventType = %q, want user_message", ev.EventType)
	}
	if !ev.ClearToolNames {
		t.Error("expected ClearToolNames=true on user message")
	}
}

// --- tool part ---

func TestParser_ToolPart_Pending_FunctionCall(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":   "tool",
		"tool":   "bash",
		"callID": "toolu_01ABC",
		"state": map[string]interface{}{
			"status": "pending",
			"input":  map[string]interface{}{"command": "ls"},
		},
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "function_call" {
		t.Errorf("EventType = %q, want function_call", ev.EventType)
	}
	if len(ev.ToolUses) != 1 {
		t.Fatalf("ToolUses len = %d, want 1", len(ev.ToolUses))
	}
	if ev.ToolUses[0].ID != "toolu_01ABC" {
		t.Errorf("ToolUse.ID = %q, want toolu_01ABC", ev.ToolUses[0].ID)
	}
	if ev.ToolUses[0].Name != "bash" {
		t.Errorf("ToolUse.Name = %q, want bash", ev.ToolUses[0].Name)
	}
}

func TestParser_ToolPart_Running_FunctionCall(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":   "tool",
		"tool":   "read",
		"callID": "toolu_02DEF",
		"state": map[string]interface{}{
			"status": "running",
			"input":  map[string]interface{}{"filePath": "/foo"},
		},
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "function_call" {
		t.Errorf("EventType = %q, want function_call", ev.EventType)
	}
}

func TestParser_ToolPart_Completed_FunctionCallOutput(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":   "tool",
		"tool":   "bash",
		"callID": "toolu_01ABC",
		"state": map[string]interface{}{
			"status": "completed",
			"output": "total 0",
		},
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "function_call_output" {
		t.Errorf("EventType = %q, want function_call_output", ev.EventType)
	}
	if len(ev.ToolResultIDs) != 1 || ev.ToolResultIDs[0] != "toolu_01ABC" {
		t.Errorf("ToolResultIDs = %v, want [toolu_01ABC]", ev.ToolResultIDs)
	}
	if ev.IsError {
		t.Error("expected IsError=false on completed tool")
	}
}

func TestParser_ToolPart_Error_IsError(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":   "tool",
		"tool":   "bash",
		"callID": "toolu_03GHI",
		"state": map[string]interface{}{
			"status": "error",
			"error":  "command not found",
		},
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "function_call_output" {
		t.Errorf("EventType = %q, want function_call_output", ev.EventType)
	}
	if !ev.IsError {
		t.Error("expected IsError=true on error tool")
	}
	if len(ev.ToolResultIDs) != 1 || ev.ToolResultIDs[0] != "toolu_03GHI" {
		t.Errorf("ToolResultIDs = %v, want [toolu_03GHI]", ev.ToolResultIDs)
	}
}

func TestParser_ToolPart_NoState_Skip(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":   "tool",
		"tool":   "bash",
		"callID": "toolu_04",
		// no "state" key
	}))
	if ev == nil || !ev.Skip {
		t.Error("expected tool part without state to be skipped")
	}
}

// --- unknown part type ---

func TestParser_UnknownType_Skip(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type": "snapshot",
		"data": "some blob",
	}))
	if ev == nil || !ev.Skip {
		t.Error("expected unknown part type to be skipped")
	}
}

// --- CWD extraction ---

func TestParser_CWDExtracted(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":  "text",
		"text":  "hello",
		"_cwd":  "/Users/marvin/project",
		"_role": "assistant",
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-nil, non-skipped event")
	}
	if ev.CWD != "/Users/marvin/project" {
		t.Errorf("CWD = %q, want /Users/marvin/project", ev.CWD)
	}
}

// --- timestamp extraction ---

func TestParser_TimestampExtracted(t *testing.T) {
	p := &Parser{}
	now := time.Now().Truncate(time.Millisecond)
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":  "step-finish",
		"reason": "stop",
		"_ts":   float64(now.UnixMilli()),
	}))
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	// Allow 1ms tolerance.
	diff := ev.Timestamp.Sub(now)
	if diff < 0 {
		diff = -diff
	}
	if diff > time.Millisecond {
		t.Errorf("Timestamp diff = %v, want < 1ms", diff)
	}
}
