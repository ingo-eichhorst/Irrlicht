package vibe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/pkg/tailer"
)

// line parses a raw JSONL string into the map shape ParseLine receives. Test
// inputs are trimmed verbatim lines from a live Mistral Vibe 2.19.0 session
// (~/.vibe/logs/session/.../messages.jsonl).
func line(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("bad test line: %v", err)
	}
	return m
}

func TestParser_User_UserMessage(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"role":"user","content":"see the PR and test it.","injected":false,"message_id":"75288117"}`))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "user_message" {
		t.Errorf("EventType = %q, want user_message", ev.EventType)
	}
	if !ev.ClearToolNames {
		t.Error("expected ClearToolNames on a user prompt")
	}
	if ev.UserText != "see the PR and test it." {
		t.Errorf("UserText = %q", ev.UserText)
	}
}

// An injected user message (Vibe's `!`-shell escape result fed back as
// context) is NOT a user turn — it must be skipped so it can't flip the
// session to working with no turn_done to close it (the session-sticks-working
// bug). See the parser's user branch.
func TestParser_InjectedUser_Skipped(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"role":"user","content":"Manual `+"`!`"+` command result from the user. Use this as context only.","injected":true,"message_id":"abc"}`))
	if ev == nil || !ev.Skip {
		t.Fatalf("injected `!` user context should be skipped, got %+v", ev)
	}
	if ev.EventType != "" {
		t.Errorf("skipped event should carry no EventType, got %q", ev.EventType)
	}
}

// A normal (injected:false) user message is still a real turn.
func TestParser_NonInjectedUser_UserMessage(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"role":"user","content":"fix the bug","injected":false,"message_id":"abc"}`))
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.Skip {
		t.Fatal("non-injected user message should not be skipped")
	}
	if ev.EventType != "user_message" {
		t.Errorf("EventType = %q, want user_message", ev.EventType)
	}
}

// Assistant messages carry tool calls in the OpenAI shape: a nested
// function.name, not a flat name. The turn continues (assistant_message).
func TestParser_AssistantWithToolCalls_MidTurn(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"role":"assistant","injected":false,"tool_calls":[{"id":"ezv2C47us","index":0,"function":{"name":"bash","arguments":"{\"command\":\"ls\"}"},"type":"function"}],"message_id":"054580ff"}`))
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "assistant_message" {
		t.Errorf("EventType = %q, want assistant_message", ev.EventType)
	}
	if len(ev.ToolUses) != 1 {
		t.Fatalf("ToolUses = %+v, want exactly one", ev.ToolUses)
	}
	assertToolUse(t, ev.ToolUses[0], "bash", "ezv2C47us")
}

// A flat `name` (no nested function object) is tolerated as a fallback for a
// hypothetical future Vibe shape.
func TestParser_AssistantFlatToolName_Fallback(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"role":"assistant","tool_calls":[{"id":"x1","name":"grep"}],"message_id":"m"}`))
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if len(ev.ToolUses) != 1 {
		t.Fatalf("flat tool name: ToolUses = %+v, want exactly one", ev.ToolUses)
	}
	if ev.ToolUses[0].Name != "grep" {
		t.Errorf("ToolUses[0].Name = %q, want grep", ev.ToolUses[0].Name)
	}
	if ev.EventType != "assistant_message" {
		t.Errorf("EventType = %q, want assistant_message", ev.EventType)
	}
}

// assertToolUse checks a single ToolUse's name and id as independent
// comparisons instead of one compound conditional.
func assertToolUse(t *testing.T, got tailer.ToolUse, wantName, wantID string) {
	t.Helper()
	if got.Name != wantName {
		t.Errorf("ToolUses[0].Name = %q, want %q", got.Name, wantName)
	}
	if got.ID != wantID {
		t.Errorf("ToolUses[0].ID = %q, want %q", got.ID, wantID)
	}
}

// A text-only assistant message (no tool_calls) is the terminal line of a turn.
func TestParser_AssistantTextOnly_TurnDone(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"role":"assistant","content":"The implementation is correct.","reasoning_content":"thinking...","message_id":"177428c4"}`))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want turn_done", ev.EventType)
	}
	if ev.AssistantText != "The implementation is correct." {
		t.Errorf("AssistantText = %q", ev.AssistantText)
	}
	if len(ev.ToolUses) != 0 {
		t.Errorf("ToolUses = %v, want none on turn_done", ev.ToolUses)
	}
}

// The builtin `todo` tool (whole-list-replace) is decoded into task deltas +
// a snapshot so the checklist surfaces in the session tasks field.
func TestParser_TodoTool_TaskDeltas(t *testing.T) {
	p := &Parser{}
	// First write: two pending todos → two creates, snapshot of 2.
	ev1 := p.ParseLine(line(t, `{"role":"assistant","tool_calls":[{"id":"tc1","function":{"name":"todo","arguments":"{\"action\":\"write\",\"todos\":[{\"id\":\"1\",\"content\":\"bump java\",\"status\":\"pending\",\"priority\":\"high\"},{\"id\":\"2\",\"content\":\"upgrade dw\",\"status\":\"pending\",\"priority\":\"high\"}]}"}}],"message_id":"a1"}`))
	if got := countTaskDeltaOp(ev1.TaskDeltas, tailer.TaskOpCreate); got != 2 {
		t.Errorf("first write: got %d creates, want 2 (deltas=%+v)", got, ev1.TaskDeltas)
	}
	if ev1.TaskSnapshot == nil || len(*ev1.TaskSnapshot) != 2 {
		t.Fatalf("first write: snapshot = %v, want 2 entries", ev1.TaskSnapshot)
	}

	// Second write: same list, first now in_progress → an Update, no new creates.
	ev2 := p.ParseLine(line(t, `{"role":"assistant","tool_calls":[{"id":"tc2","function":{"name":"todo","arguments":"{\"action\":\"write\",\"todos\":[{\"id\":\"1\",\"content\":\"bump java\",\"status\":\"in_progress\",\"priority\":\"high\"},{\"id\":\"2\",\"content\":\"upgrade dw\",\"status\":\"pending\",\"priority\":\"high\"}]}"}}],"message_id":"a2"}`))
	if got := countTaskDeltaOp(ev2.TaskDeltas, tailer.TaskOpCreate); got != 0 {
		t.Errorf("second write should create nothing, got %d creates (deltas=%+v)", got, ev2.TaskDeltas)
	}
	if !hasTaskDeltaStatus(ev2.TaskDeltas, tailer.TaskOpUpdate, tailer.TaskStatusInProgress) {
		t.Errorf("second write: expected an in_progress Update, deltas=%+v", ev2.TaskDeltas)
	}
}

// countTaskDeltaOp counts deltas of the given op, keeping the assertions
// above to one check per line instead of an inline accumulation loop.
func countTaskDeltaOp(deltas []tailer.TaskDelta, op string) int {
	n := 0
	for _, d := range deltas {
		if d.Op == op {
			n++
		}
	}
	return n
}

// hasTaskDeltaStatus reports whether any delta matches the given op+status pair.
func hasTaskDeltaStatus(deltas []tailer.TaskDelta, op, status string) bool {
	for _, d := range deltas {
		if d.Op == op && d.Status == status {
			return true
		}
	}
	return false
}

// A cancelled todo is dropped from the tracked list (vibe excludes it from the plan).
func TestParser_TodoTool_DropsCancelled(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"role":"assistant","tool_calls":[{"id":"tc","function":{"name":"todo","arguments":"{\"action\":\"write\",\"todos\":[{\"id\":\"1\",\"content\":\"keep\",\"status\":\"pending\"},{\"id\":\"2\",\"content\":\"drop\",\"status\":\"cancelled\"}]}"}}],"message_id":"a"}`))
	if ev.TaskSnapshot == nil || len(*ev.TaskSnapshot) != 1 {
		t.Fatalf("snapshot = %v, want 1 entry (cancelled dropped)", ev.TaskSnapshot)
	}
	if (*ev.TaskSnapshot)[0].Subject != "keep" {
		t.Errorf("kept the wrong todo: %+v", (*ev.TaskSnapshot)[0])
	}
}

func TestParser_Tool_ToolResult(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"role":"tool","content":"command output","injected":false,"name":"bash","tool_call_id":"ezv2C47us"}`))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "tool_result" {
		t.Errorf("EventType = %q, want tool_result", ev.EventType)
	}
	if len(ev.ToolResultIDs) != 1 || ev.ToolResultIDs[0] != "ezv2C47us" {
		t.Errorf("ToolResultIDs = %v, want [ezv2C47us]", ev.ToolResultIDs)
	}
}

func TestParser_MissingRole_Skips(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"content":"orphan","message_id":"m"}`))
	if ev == nil || !ev.Skip {
		t.Fatalf("expected Skip for a line with no role, got %+v", ev)
	}
}

// The sidecar supplies cwd + model on every event, and context tokens only on
// turn_done — the transcript itself carries none of these.
func TestParser_SidecarEnrichment(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, transcriptFilename)
	if err := os.WriteFile(transcript, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := `{"environment":{"working_directory":"/Users/x/proj"},"config":{"active_model":"mistral-medium-3.5"},"stats":{"context_tokens":136662,"session_prompt_tokens":10000,"session_completion_tokens":400}}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &Parser{}
	p.SetTranscriptPath(transcript)

	// A mid-turn assistant tool call: cwd + model attached, no tokens/contribution.
	mid := p.ParseLine(line(t, `{"role":"assistant","tool_calls":[{"id":"a","function":{"name":"bash"}}],"message_id":"m1"}`))
	assertSidecarMidTurn(t, mid)

	// The terminal assistant message: context tokens + the usage contribution
	// (the full session cumulative on this first turn_done) land here.
	done := p.ParseLine(line(t, `{"role":"assistant","content":"done","message_id":"m2"}`))
	assertSidecarFirstTurnDone(t, done)

	// A SECOND turn_done reading the SAME (unchanged) sidecar must NOT re-emit
	// the cumulative usage — the dedup that keeps backfill from double-counting.
	done2 := p.ParseLine(line(t, `{"role":"assistant","content":"again","message_id":"m3"}`))
	if done2.Contribution != nil {
		t.Errorf("second turn_done on unchanged sidecar re-emitted usage: %+v", done2.Contribution)
	}
}

func assertSidecarMidTurn(t *testing.T, mid *tailer.ParsedEvent) {
	t.Helper()
	if mid.CWD != "/Users/x/proj" {
		t.Errorf("mid CWD = %q, want /Users/x/proj", mid.CWD)
	}
	if mid.ModelName == "" {
		t.Errorf("mid ModelName empty, want the sidecar model")
	}
	if mid.Tokens != nil {
		t.Errorf("mid should carry no tokens off turn_done, got %+v", mid.Tokens)
	}
	if mid.Contribution != nil {
		t.Errorf("mid should carry no contribution off turn_done, got %+v", mid.Contribution)
	}
}

func assertSidecarFirstTurnDone(t *testing.T, done *tailer.ParsedEvent) {
	t.Helper()
	if done.EventType != "turn_done" {
		t.Fatalf("EventType = %q, want turn_done", done.EventType)
	}
	if done.CWD != "/Users/x/proj" {
		t.Errorf("done CWD = %q", done.CWD)
	}
	assertSidecarTokens(t, done.Tokens)
	assertSidecarContribution(t, done.Contribution)
}

func assertSidecarTokens(t *testing.T, tokens *tailer.TokenSnapshot) {
	t.Helper()
	if tokens == nil {
		t.Fatal("done Tokens is nil, want Total 136662")
	}
	if tokens.Total != 136662 {
		t.Errorf("done Tokens.Total = %d, want 136662", tokens.Total)
	}
}

func assertSidecarContribution(t *testing.T, contribution *tailer.PerTurnContribution) {
	t.Helper()
	if contribution == nil {
		t.Fatal("done should carry a usage Contribution")
	}
	if contribution.Usage.Input != 10000 {
		t.Errorf("Contribution usage Input = %d, want 10000", contribution.Usage.Input)
	}
	if contribution.Usage.Output != 400 {
		t.Errorf("Contribution usage Output = %d, want 400", contribution.Usage.Output)
	}
	if contribution.Model == "" {
		t.Errorf("Contribution.Model empty, want the model so cost buckets correctly")
	}
}

// With no transcript path (path-less tests) enrichment is skipped, not fatal.
func TestParser_NoPath_NoEnrichment(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"role":"assistant","content":"done","message_id":"m"}`))
	if ev.CWD != "" {
		t.Errorf("CWD = %q, want empty without a path", ev.CWD)
	}
	if ev.ModelName != "" {
		t.Errorf("ModelName = %q, want empty without a path", ev.ModelName)
	}
	if ev.Tokens != nil {
		t.Errorf("Tokens = %+v, want nil without a path", ev.Tokens)
	}
}
