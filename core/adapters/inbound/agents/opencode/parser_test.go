package opencode

import (
	"strings"
	"testing"
	"time"

	"irrlicht/core/pkg/tailer"
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

// An errored/aborted turn records the failure on the parent message
// (message.data.error), exported as the synthetic "_error" key on the bare
// step-start part — opencode emits no step-finish reason="error" part. On the
// replay path that "_error" is the sole turn-ending signal, so a part carrying
// it must settle the turn (mirrors the live watcher's isErrorMessage; #493).
func TestParser_ErrorMessage_TurnDone(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type": "step-start",
		"_error": map[string]interface{}{
			"name":    "UnknownError",
			"message": "n_keep >= n_ctx",
		},
	}))
	if ev == nil || ev.EventType != "turn_done" {
		t.Errorf("expected an errored step-start to settle the turn (turn_done); got EventType=%q skip=%v", evType(ev), evSkip(ev))
	}
}

// A normal (non-errored) part exports "_error": null — the JSON-null the
// driver injects when message.data.error is absent — and must NOT settle the
// turn, or every part of every recording would spuriously close the turn.
func TestParser_NullError_DoesNotSettle(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":   "step-start",
		"_error": nil,
	}))
	if ev == nil || ev.EventType == "turn_done" {
		t.Errorf("a null _error must not emit turn_done; got EventType=%q", evType(ev))
	}
	if !ev.Skip {
		t.Error("a step-start with null _error should still be skipped")
	}
}

func evType(ev *tailer.ParsedEvent) string {
	if ev == nil {
		return "<nil>"
	}
	return ev.EventType
}

func evSkip(ev *tailer.ParsedEvent) bool {
	return ev != nil && ev.Skip
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
	if len([]rune(ev.AssistantText)) != 201 {
		t.Errorf("AssistantText rune count = %d, want 201 (… + 200 chars)", len([]rune(ev.AssistantText)))
	}
	if !strings.HasPrefix(ev.AssistantText, "…") {
		t.Errorf("AssistantText = %q, want leading …", ev.AssistantText)
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
		"type":   "step-finish",
		"reason": "stop",
		"_ts":    float64(now.UnixMilli()),
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

// --- step-finish / interrupted ---

func TestParser_StepFinish_Interrupted_TurnDone(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":   "step-finish",
		"reason": "interrupted",
		"tokens": map[string]interface{}{
			"input":  float64(500),
			"output": float64(100),
			"total":  float64(600),
		},
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want turn_done", ev.EventType)
	}
	if ev.Contribution == nil {
		t.Fatal("expected Contribution on reason=interrupted (tokens were consumed)")
	}
	if ev.Contribution.Usage.Input != 500 {
		t.Errorf("Contribution.Usage.Input = %d, want 500", ev.Contribution.Usage.Input)
	}
}

// --- step-finish / length ---

func TestParser_StepFinish_Length_TurnDone(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":   "step-finish",
		"reason": "length",
		"tokens": map[string]interface{}{
			"input":  float64(100000),
			"output": float64(2000),
			"total":  float64(102000),
		},
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want turn_done", ev.EventType)
	}
	if ev.Contribution == nil {
		t.Fatal("expected Contribution on reason=length (tokens were consumed)")
	}
}

// --- step-finish / error ---

func TestParser_StepFinish_Error_TurnDone(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":   "step-finish",
		"reason": "error",
		"tokens": map[string]interface{}{
			"input":  float64(200),
			"output": float64(0),
			"total":  float64(200),
		},
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want turn_done", ev.EventType)
	}
	if ev.Contribution == nil {
		t.Fatal("expected Contribution on reason=error (tokens were consumed)")
	}
}

// --- step-finish / unknown reason ---

func TestParser_StepFinish_UnknownReason_AssistantMessage(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":   "step-finish",
		"reason": "some-future-reason",
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "assistant_message" {
		t.Errorf("EventType = %q, want assistant_message", ev.EventType)
	}
}

// --- step-finish / stop without tokens ---

func TestParser_StepFinish_Stop_NoTokens(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":   "step-finish",
		"reason": "stop",
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want turn_done", ev.EventType)
	}
	if ev.Contribution != nil {
		t.Error("expected no Contribution when tokens are missing")
	}
	if ev.Tokens != nil {
		t.Error("expected nil Tokens when tokens field missing")
	}
}

// --- step-finish / zero cost ---

func TestParser_StepFinish_Stop_ZeroCost(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":   "step-finish",
		"reason": "stop",
		"tokens": map[string]interface{}{
			"input":  float64(100),
			"output": float64(50),
			"total":  float64(150),
		},
		"cost":   float64(0),
		"_model": "free-model",
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.Contribution == nil {
		t.Fatal("expected Contribution even with zero cost")
	}
	if ev.Contribution.ProviderCostUSD != nil {
		t.Error("expected nil ProviderCostUSD when cost is zero")
	}
}

// --- step-finish / missing cost field ---

func TestParser_StepFinish_Stop_MissingCost(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type":   "step-finish",
		"reason": "stop",
		"tokens": map[string]interface{}{
			"input":  float64(100),
			"output": float64(50),
			"total":  float64(150),
		},
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.Contribution == nil {
		t.Fatal("expected Contribution even without cost field")
	}
	if ev.Contribution.ProviderCostUSD != nil {
		t.Error("expected nil ProviderCostUSD when cost field missing")
	}
}

// --- todowrite ---

// todowritePart builds a "tool" part row in the shape OpenCode writes for the
// todowrite tool: an `input.todos` snapshot nested under `state`.
func todowritePart(callID string, status string, todos []map[string]interface{}) map[string]interface{} {
	items := make([]interface{}, 0, len(todos))
	for _, td := range todos {
		items = append(items, td)
	}
	return rawPart(map[string]interface{}{
		"type":   "tool",
		"tool":   "todowrite",
		"callID": callID,
		"state": map[string]interface{}{
			"status": status,
			"input": map[string]interface{}{
				"todos": items,
			},
		},
	})
}

func TestParser_Todowrite_FirstCallCreates(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(todowritePart("call_1", "completed", []map[string]interface{}{
		{"content": "Task A", "status": "pending", "priority": "low"},
		{"content": "Task B", "status": "pending", "priority": "low"},
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "function_call_output" {
		t.Errorf("EventType = %q, want function_call_output", ev.EventType)
	}
	if len(ev.TaskDeltas) != 2 {
		t.Fatalf("TaskDeltas len = %d, want 2", len(ev.TaskDeltas))
	}
	for i, d := range ev.TaskDeltas {
		if d.Op != tailer.TaskOpCreate {
			t.Errorf("TaskDeltas[%d].Op = %q, want create", i, d.Op)
		}
	}
	if ev.TaskDeltas[0].Subject != "Task A" || ev.TaskDeltas[1].Subject != "Task B" {
		t.Errorf("subjects = [%q, %q], want [Task A, Task B]",
			ev.TaskDeltas[0].Subject, ev.TaskDeltas[1].Subject)
	}
}

func TestParser_Todowrite_SecondCallUpdates(t *testing.T) {
	p := &Parser{}
	p.ParseLine(todowritePart("call_1", "completed", []map[string]interface{}{
		{"content": "Task A", "status": "pending"},
		{"content": "Task B", "status": "pending"},
	}))
	ev := p.ParseLine(todowritePart("call_2", "completed", []map[string]interface{}{
		{"content": "Task A", "status": "in_progress"},
		{"content": "Task B", "status": "pending"},
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if len(ev.TaskDeltas) != 1 {
		t.Fatalf("TaskDeltas len = %d, want 1 (only Task A status change)", len(ev.TaskDeltas))
	}
	d := ev.TaskDeltas[0]
	if d.Op != tailer.TaskOpUpdate {
		t.Errorf("Op = %q, want update", d.Op)
	}
	if d.ID != "1" {
		t.Errorf("ID = %q, want \"1\"", d.ID)
	}
	if d.Status != "in_progress" {
		t.Errorf("Status = %q, want in_progress", d.Status)
	}
}

func TestParser_Todowrite_ThirdCallAppends(t *testing.T) {
	p := &Parser{}
	p.ParseLine(todowritePart("call_1", "completed", []map[string]interface{}{
		{"content": "Task A", "status": "pending"},
		{"content": "Task B", "status": "pending"},
	}))
	p.ParseLine(todowritePart("call_2", "completed", []map[string]interface{}{
		{"content": "Task A", "status": "in_progress"},
		{"content": "Task B", "status": "pending"},
	}))
	ev := p.ParseLine(todowritePart("call_3", "completed", []map[string]interface{}{
		{"content": "Task A", "status": "completed"},
		{"content": "Task B", "status": "in_progress"},
		{"content": "Task C", "status": "pending"},
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if len(ev.TaskDeltas) != 3 {
		t.Fatalf("TaskDeltas len = %d, want 3 (Task A completed, Task B in_progress, Task C create)", len(ev.TaskDeltas))
	}
	if ev.TaskDeltas[0] != (tailer.TaskDelta{Op: tailer.TaskOpUpdate, ID: "1", Status: "completed"}) {
		t.Errorf("deltas[0] = %+v, want update id=1 status=completed", ev.TaskDeltas[0])
	}
	if ev.TaskDeltas[1] != (tailer.TaskDelta{Op: tailer.TaskOpUpdate, ID: "2", Status: "in_progress"}) {
		t.Errorf("deltas[1] = %+v, want update id=2 status=in_progress", ev.TaskDeltas[1])
	}
	if ev.TaskDeltas[2].Op != tailer.TaskOpCreate || ev.TaskDeltas[2].Subject != "Task C" {
		t.Errorf("deltas[2] = %+v, want create subject=Task C", ev.TaskDeltas[2])
	}
}

func TestParser_Todowrite_PendingOnCreateSkipsUpdate(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(todowritePart("call_1", "completed", []map[string]interface{}{
		{"content": "Already in progress", "status": "in_progress"},
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	// First sighting of "Already in progress" with status in_progress should
	// emit BOTH a Create (default pending) AND an Update to reach in_progress —
	// otherwise the tailer's seq matches the parser's but the status is wrong.
	if len(ev.TaskDeltas) != 2 {
		t.Fatalf("TaskDeltas len = %d, want 2 (create + update)", len(ev.TaskDeltas))
	}
	if ev.TaskDeltas[0].Op != tailer.TaskOpCreate {
		t.Errorf("deltas[0].Op = %q, want create", ev.TaskDeltas[0].Op)
	}
	if ev.TaskDeltas[1] != (tailer.TaskDelta{Op: tailer.TaskOpUpdate, ID: "1", Status: "in_progress"}) {
		t.Errorf("deltas[1] = %+v, want update id=1 status=in_progress", ev.TaskDeltas[1])
	}
}

func TestParser_Todowrite_SnapshotPrunesDeletedTodos(t *testing.T) {
	// OpenCode's `todowrite` is a full-list replace — if a todo is
	// missing from a subsequent snapshot, it has been removed. Parser
	// must emit TaskSnapshot so the tailer / metrics accumulator can
	// prune the stale entry.
	p := &Parser{}
	p.ParseLine(todowritePart("call_1", "completed", []map[string]interface{}{
		{"content": "Task A", "status": "pending"},
		{"content": "Task B", "status": "pending"},
		{"content": "Task C", "status": "pending"},
	}))
	ev := p.ParseLine(todowritePart("call_2", "completed", []map[string]interface{}{
		{"content": "Task A", "status": "pending"},
		{"content": "Task B", "status": "pending"},
		// Task C dropped.
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.TaskSnapshot == nil {
		t.Fatal("expected TaskSnapshot to be set")
	}
	if got, want := len(*ev.TaskSnapshot), 2; got != want {
		t.Fatalf("snapshot len = %d, want %d", got, want)
	}
	ids := map[string]bool{}
	for _, e := range *ev.TaskSnapshot {
		ids[e.ID] = true
	}
	if !ids["1"] || !ids["2"] {
		t.Errorf("snapshot IDs = %v, want {1, 2}", ids)
	}
	if ids["3"] {
		t.Errorf("snapshot still contains pruned id=3: %v", ids)
	}
}

func TestParser_Todowrite_SnapshotCarriesStatusReversion(t *testing.T) {
	// A todo that flips from in_progress back to pending in a later
	// snapshot must surface in TaskSnapshot with the new (pending)
	// status — the Update path suppresses pending writes, so without
	// the snapshot the tailer would freeze the stale in_progress.
	p := &Parser{}
	p.ParseLine(todowritePart("call_1", "completed", []map[string]interface{}{
		{"content": "Task A", "status": "in_progress"},
	}))
	ev := p.ParseLine(todowritePart("call_2", "completed", []map[string]interface{}{
		{"content": "Task A", "status": "pending"},
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	// No Update delta because status is pending.
	if len(ev.TaskDeltas) != 0 {
		t.Errorf("TaskDeltas len = %d, want 0 (reversion via snapshot only)", len(ev.TaskDeltas))
	}
	if ev.TaskSnapshot == nil || len(*ev.TaskSnapshot) != 1 {
		t.Fatalf("TaskSnapshot = %v, want one entry", ev.TaskSnapshot)
	}
	got := (*ev.TaskSnapshot)[0]
	if got.ID != "1" || got.Status != "pending" {
		t.Errorf("snapshot entry = %+v, want {ID:1 Status:pending}", got)
	}
}

func TestParser_Todowrite_LifecycleStillFires(t *testing.T) {
	// Regression: todowrite must still participate in the tool-call lifecycle
	// (open on pending/running, close on completed/error). Without this the
	// dashboard would never see the tool's open/close pair.
	p := &Parser{}
	ev := p.ParseLine(todowritePart("call_42", "completed", []map[string]interface{}{
		{"content": "Single task", "status": "pending"},
	}))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "function_call_output" {
		t.Errorf("EventType = %q, want function_call_output", ev.EventType)
	}
	if len(ev.ToolResultIDs) != 1 || ev.ToolResultIDs[0] != "call_42" {
		t.Errorf("ToolResultIDs = %v, want [call_42]", ev.ToolResultIDs)
	}
}

// --- text part / missing role (defaults to assistant) ---

func TestParser_TextPart_NoRole_DefaultsAssistant(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type": "text",
		"text": "some output",
		"_ts":  float64(time.Now().UnixMilli()),
		"_cwd": "/tmp",
	})
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "assistant_message" {
		t.Errorf("EventType = %q, want assistant_message", ev.EventType)
	}
}

// --- Task-estimate marker (issue #558) ---

func TestParser_TaskEstimate_FromTextPart(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type": "text",
		"text": `Working. <!-- {"marker":"irrlicht-eta","total_rounds":9,"completed_rounds":4} -->`,
	}))
	if ev.TaskEstimate == nil {
		t.Fatal("expected TaskEstimate from assistant text part")
	}
	if ev.TaskEstimate.TotalRounds != 9 || ev.TaskEstimate.CompletedRounds != 4 {
		t.Errorf("rounds = %d/%d, want 4/9", ev.TaskEstimate.CompletedRounds, ev.TaskEstimate.TotalRounds)
	}
}

// Marker early in a long part must survive the 200-rune display truncation.
func TestParser_TaskEstimate_SurvivesLongPart(t *testing.T) {
	p := &Parser{}
	long := `<!-- {"marker":"irrlicht-eta","total_rounds":5,"completed_rounds":1} --> `
	for i := 0; i < 50; i++ {
		long += "filler prose "
	}
	ev := p.ParseLine(rawPart(map[string]interface{}{
		"type": "text",
		"text": long,
	}))
	if ev.TaskEstimate == nil || ev.TaskEstimate.CompletedRounds != 1 {
		t.Fatalf("TaskEstimate = %+v, want 1/5 despite display truncation", ev.TaskEstimate)
	}
}
