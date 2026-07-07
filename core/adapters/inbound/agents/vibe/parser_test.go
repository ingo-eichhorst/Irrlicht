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
	if ev == nil || ev.Skip || ev.EventType != "user_message" {
		t.Fatalf("non-injected user: EventType = %q, want user_message", eventTypeOf(ev))
	}
}

// Assistant messages carry tool calls in the OpenAI shape: a nested
// function.name, not a flat name. The turn continues (assistant_message).
func TestParser_AssistantWithToolCalls_MidTurn(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"role":"assistant","injected":false,"tool_calls":[{"id":"ezv2C47us","index":0,"function":{"name":"bash","arguments":"{\"command\":\"ls\"}"},"type":"function"}],"message_id":"054580ff"}`))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "assistant_message" {
		t.Errorf("EventType = %q, want assistant_message", ev.EventType)
	}
	if len(ev.ToolUses) != 1 || ev.ToolUses[0].Name != "bash" || ev.ToolUses[0].ID != "ezv2C47us" {
		t.Errorf("ToolUses = %+v, want one bash/ezv2C47us", ev.ToolUses)
	}
}

// A flat `name` (no nested function object) is tolerated as a fallback for a
// hypothetical future Vibe shape.
func TestParser_AssistantFlatToolName_Fallback(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"role":"assistant","tool_calls":[{"id":"x1","name":"grep"}],"message_id":"m"}`))
	if ev == nil || len(ev.ToolUses) != 1 || ev.ToolUses[0].Name != "grep" {
		t.Fatalf("flat tool name: ToolUses = %+v, want one grep", toolUsesOf(ev))
	}
	if ev.EventType != "assistant_message" {
		t.Errorf("EventType = %q, want assistant_message", ev.EventType)
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
	meta := `{"environment":{"working_directory":"/Users/x/proj"},"config":{"active_model":"mistral-medium-3.5"},"stats":{"context_tokens":136662}}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &Parser{}
	p.SetTranscriptPath(transcript)

	// A mid-turn assistant tool call: cwd + model attached, no tokens yet.
	mid := p.ParseLine(line(t, `{"role":"assistant","tool_calls":[{"id":"a","function":{"name":"bash"}}],"message_id":"m1"}`))
	if mid.CWD != "/Users/x/proj" {
		t.Errorf("mid CWD = %q, want /Users/x/proj", mid.CWD)
	}
	if mid.ModelName == "" {
		t.Errorf("mid ModelName empty, want the sidecar model")
	}
	if mid.Tokens != nil {
		t.Errorf("mid Tokens = %+v, want nil off turn_done", mid.Tokens)
	}

	// The terminal assistant message: context tokens land here.
	done := p.ParseLine(line(t, `{"role":"assistant","content":"done","message_id":"m2"}`))
	if done.EventType != "turn_done" {
		t.Fatalf("EventType = %q, want turn_done", done.EventType)
	}
	if done.Tokens == nil || done.Tokens.Total != 136662 {
		t.Errorf("done Tokens = %+v, want Total 136662", done.Tokens)
	}
	if done.CWD != "/Users/x/proj" {
		t.Errorf("done CWD = %q", done.CWD)
	}
}

// With no transcript path (path-less tests) enrichment is skipped, not fatal.
func TestParser_NoPath_NoEnrichment(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"role":"assistant","content":"done","message_id":"m"}`))
	if ev.CWD != "" || ev.ModelName != "" || ev.Tokens != nil {
		t.Errorf("expected no enrichment without a path, got CWD=%q model=%q tokens=%+v", ev.CWD, ev.ModelName, ev.Tokens)
	}
}

func eventTypeOf(ev *tailer.ParsedEvent) string {
	if ev == nil {
		return "<nil>"
	}
	return ev.EventType
}

func toolUsesOf(ev *tailer.ParsedEvent) []tailer.ToolUse {
	if ev == nil {
		return nil
	}
	return ev.ToolUses
}
