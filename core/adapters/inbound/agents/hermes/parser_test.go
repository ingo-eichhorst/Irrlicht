package hermes

import (
	"strings"
	"testing"

	"irrlicht/core/pkg/tailer"
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

// realTodoToolCallsJSON is a `messages.tool_calls` value in the shape hermes'
// own tools/todo_tool.py defines: `todos` is a list of {id, content, status}
// with status ∈ pending|in_progress|completed|cancelled, and a `merge` flag.
// The arguments column is a JSON *string*, exactly like the terminal call
// above — that double encoding is the part the adapter has to get right.
const realTodoToolCallsJSON = `[{"id": "call_todo_1", "call_id": "call_todo_1", ` +
	`"type": "function", "function": {"name": "todo", "arguments": ` +
	`"{\"todos\":[{\"id\":\"t1\",\"content\":\"Read the failing test\",\"status\":\"completed\"},` +
	`{\"id\":\"t2\",\"content\":\"Fix the parser\",\"status\":\"in_progress\"},` +
	`{\"id\":\"t3\",\"content\":\"Run the suite\",\"status\":\"pending\"}],\"merge\":false}"}}]`

// The todo tool is in hermes' default CLI toolset, so a task list is ordinary
// traffic — but the adapter unmarshalled only {id, call_id, function.name} and
// dropped function.arguments, so Tasks stayed empty and zero task_delta events
// were emitted for a session that plainly had a plan.
func TestParseLine_TodoToolCallProducesTaskDeltas(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		keyRole:      "assistant",
		keyFinish:    "tool_calls",
		keyToolCalls: realTodoToolCallsJSON,
	})
	if ev == nil || ev.Skip {
		t.Fatal("assistant row must produce an event")
	}
	if len(ev.TaskDeltas) == 0 {
		t.Fatal("a todo tool call must produce task deltas — the plan is otherwise invisible")
	}
	if ev.TaskSnapshot == nil || len(*ev.TaskSnapshot) != 3 {
		t.Fatalf("TaskSnapshot must list all three todos, got %v", ev.TaskSnapshot)
	}
	// Subject is the user-visible label, matching every other todo adapter —
	// hermes' stable per-todo id would render as an opaque token.
	var subjects []string
	for _, d := range ev.TaskDeltas {
		if d.Op == tailer.TaskOpCreate {
			subjects = append(subjects, d.Subject)
		}
	}
	if len(subjects) != 3 || subjects[0] != "Read the failing test" {
		t.Errorf("Create subjects = %v, want the three todo contents", subjects)
	}
	// The tool call itself must still be reported: dropping it would trade one
	// gap for another (an unclosed tool would never settle).
	if len(ev.ToolUses) != 1 || ev.ToolUses[0].Name != "todo" {
		t.Errorf("ToolUses = %+v, want the todo call preserved", ev.ToolUses)
	}
}

// A second snapshot must emit only the delta, not re-create the whole list —
// that is what makes successive todo calls readable as progress.
func TestParseLine_TodoToolCallSecondSnapshotIsDeltaOnly(t *testing.T) {
	p := &Parser{}
	p.ParseLine(map[string]interface{}{
		keyRole: "assistant", keyFinish: "tool_calls", keyToolCalls: realTodoToolCallsJSON,
	})
	advanced := strings.Replace(realTodoToolCallsJSON,
		`\"content\":\"Fix the parser\",\"status\":\"in_progress\"`,
		`\"content\":\"Fix the parser\",\"status\":\"completed\"`, 1)
	ev := p.ParseLine(map[string]interface{}{
		keyRole: "assistant", keyFinish: "tool_calls", keyToolCalls: advanced,
	})
	for _, d := range ev.TaskDeltas {
		if d.Op == tailer.TaskOpCreate {
			t.Errorf("second snapshot must not re-Create %q", d.Subject)
		}
	}
	if len(ev.TaskDeltas) == 0 {
		t.Error("advancing a todo to completed must emit an Update")
	}
}

// A non-todo tool call must not be mined for todos, and a todo call with
// unparseable arguments must degrade to "no deltas" rather than panic.
func TestParseLine_TodoExtractionIsNarrowAndSafe(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		keyRole: "assistant", keyFinish: "tool_calls", keyToolCalls: realToolCallsJSON,
	})
	if len(ev.TaskDeltas) != 0 || ev.TaskSnapshot != nil {
		t.Errorf("a terminal call must not produce task deltas: %+v", ev.TaskDeltas)
	}

	for _, bad := range []string{`"not json"`, `"{}"`, `"{\"todos\":\"nope\"}"`, `""`} {
		raw := `[{"id":"c1","type":"function","function":{"name":"todo","arguments":` + bad + `}}]`
		ev := p.ParseLine(map[string]interface{}{
			keyRole: "assistant", keyFinish: "tool_calls", keyToolCalls: raw,
		})
		if ev == nil {
			t.Fatalf("malformed todo arguments must still yield an event: %s", bad)
		}
		if len(ev.TaskDeltas) != 0 {
			t.Errorf("malformed todo arguments %s must yield no deltas, got %+v", bad, ev.TaskDeltas)
		}
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

// AssistantText goes through the tailer's shared display-truncation rule —
// tail-keeping, because a waiting cue ("...shall I proceed?") sits at the END
// of the text. Asserted through ParseLine rather than against a local helper,
// so the adapter cannot drift back onto its own copy of the rule.
func TestParseLine_AssistantTextUsesSharedTruncation(t *testing.T) {
	p := &Parser{}
	long := strings.Repeat("a", 500) + " QUESTION?"
	ev := p.ParseLine(map[string]interface{}{
		keyRole:    "assistant",
		keyContent: long,
		keyFinish:  "stop",
	})
	if ev == nil {
		t.Fatal("expected an event")
	}
	if ev.AssistantText == long {
		t.Error("long assistant text must be truncated")
	}
	if !strings.HasSuffix(ev.AssistantText, "QUESTION?") {
		t.Errorf("truncation must keep the tail, where waiting cues live: %q", ev.AssistantText)
	}
	if ev.AssistantText != tailer.TruncateAssistantText(long) {
		t.Error("must use tailer.TruncateAssistantText, not a local copy of the rule")
	}
}

// A trailing question is what puts a finished turn into `waiting` rather than
// `ready`. Computed from the full text before truncation.
func TestParseLine_PendingWaitingCue(t *testing.T) {
	p := &Parser{}
	cue := p.ParseLine(map[string]interface{}{
		keyRole: "assistant", keyFinish: "stop",
		keyContent: "I can take either approach. Which would you prefer?",
	})
	if !cue.PendingWaitingCue {
		t.Error("a trailing question must set PendingWaitingCue")
	}
	plain := p.ParseLine(map[string]interface{}{
		keyRole: "assistant", keyFinish: "stop",
		keyContent: "Done. I fixed the build and all tests pass.",
	})
	if plain.PendingWaitingCue {
		t.Error("a plain completion report must NOT set PendingWaitingCue")
	}
}
