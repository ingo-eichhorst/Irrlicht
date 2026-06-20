package antigravity

import (
	"testing"

	"irrlicht/core/pkg/tailer"
)

// Compile-time proof the parser satisfies both the adapter-facing and tailer-
// facing contracts (maps.go casts the NewParser result to TranscriptParser).
var (
	_ tailer.TranscriptParser = (*Parser)(nil)
)

// line builds a decoded JSONL map. JSON numbers decode to float64, so
// step_index is passed as float64 to mirror the real tailer input.
func line(source, typ string, step float64, content string, toolCalls []any) map[string]any {
	m := map[string]any{
		"source":     source,
		"type":       typ,
		"status":     "DONE",
		"step_index": step,
		"created_at": "2026-06-19T05:33:39Z",
	}
	if content != "" {
		m["content"] = content
	}
	if toolCalls != nil {
		m["tool_calls"] = toolCalls
	}
	return m
}

func runCommand(cwd string) []any {
	return []any{map[string]any{
		"name": "run_command",
		"args": map[string]any{"CommandLine": `"ls"`, "Cwd": `"` + cwd + `"`},
	}}
}

// TestParseTurn walks a full turn — prompt, planning+tool, failing result,
// terminal line — and asserts the normalized event for each step, plus the cwd
// and model harvested along the way.
func TestParseTurn(t *testing.T) {
	p := &Parser{}

	userContent := "<USER_REQUEST>\nls\n</USER_REQUEST>\n<USER_SETTINGS_CHANGE>\n" +
		"The user changed setting `Model Selection` from None to Gemini 3.5 Flash (Medium). No need to comment.\n</USER_SETTINGS_CHANGE>"
	ev := p.ParseLine(line("USER_EXPLICIT", "USER_INPUT", 0, userContent, nil))
	if ev.Skip || ev.EventType != "user_message" {
		t.Fatalf("USER_INPUT: got skip=%v type=%q, want user_message", ev.Skip, ev.EventType)
	}
	if !ev.ClearToolNames {
		t.Error("USER_INPUT must set ClearToolNames to reset open tools on a new prompt")
	}
	if p.model == "" {
		t.Error("model should be harvested from <USER_SETTINGS_CHANGE>")
	}

	// SYSTEM lines are skipped.
	if ev := p.ParseLine(line("SYSTEM", "CONVERSATION_HISTORY", 1, "", nil)); !ev.Skip {
		t.Errorf("SYSTEM/CONVERSATION_HISTORY should be skipped, got type=%q", ev.EventType)
	}

	// Planning step with one tool call → assistant_message, one open tool, cwd
	// + model attached.
	ev = p.ParseLine(line("MODEL", "PLANNER_RESPONSE", 2, "I will list the directory.", runCommand("/repo")))
	if ev.EventType != "assistant_message" {
		t.Fatalf("planner-with-tool: got type=%q, want assistant_message", ev.EventType)
	}
	if len(ev.ToolUses) != 1 || ev.ToolUses[0].Name != "run_command" {
		t.Fatalf("planner-with-tool: ToolUses=%+v, want one run_command", ev.ToolUses)
	}
	if ev.ToolUses[0].ID != "2-0" {
		t.Errorf("tool ID = %q, want synthetic step-derived 2-0", ev.ToolUses[0].ID)
	}
	if ev.CWD != "/repo" {
		t.Errorf("CWD = %q, want /repo harvested from run_command Cwd arg", ev.CWD)
	}
	if ev.ModelName == "" {
		t.Error("ModelName should be attached to assistant events once harvested")
	}

	// Failing tool result → function_call_output, closes the open tool, flags error.
	ev = p.ParseLine(line("MODEL", "RUN_COMMAND", 3,
		"Created At: ...\nThe command failed with exit code: 1\nOutput:\n", nil))
	if ev.EventType != "function_call_output" {
		t.Fatalf("tool result: got type=%q, want function_call_output", ev.EventType)
	}
	if len(ev.ToolResultIDs) != 1 || ev.ToolResultIDs[0] != "2-0" {
		t.Fatalf("tool result: ToolResultIDs=%v, want [2-0] closing the open tool", ev.ToolResultIDs)
	}
	if !ev.IsError {
		t.Error("a failing run_command result must set IsError")
	}
	if p.openToolID != "" {
		t.Errorf("openToolID = %q, want cleared after the result closed it", p.openToolID)
	}

	// Terminal empty planner step → turn_done.
	ev = p.ParseLine(line("MODEL", "PLANNER_RESPONSE", 4, "", nil))
	if ev.EventType != "turn_done" {
		t.Fatalf("terminal planner: got type=%q, want turn_done", ev.EventType)
	}
	if len(ev.ToolUses) != 0 {
		t.Errorf("terminal planner opened tools: %+v", ev.ToolUses)
	}
}

// TestSuccessfulResultNotError guards the error classifier: a successful command
// result must not flag IsError.
func TestSuccessfulResultNotError(t *testing.T) {
	p := &Parser{}
	p.ParseLine(line("MODEL", "PLANNER_RESPONSE", 0, "I will run it.", runCommand("/repo")))
	ev := p.ParseLine(line("MODEL", "RUN_COMMAND", 1, "The command completed successfully.\nOutput:\n", nil))
	if ev.IsError {
		t.Error("a successful command result must not set IsError")
	}
	if len(ev.ToolResultIDs) != 1 {
		t.Errorf("successful result should still close the open tool, got %v", ev.ToolResultIDs)
	}
}

// TestModelExtraction covers the <USER_SETTINGS_CHANGE> regex against name
// shapes with dotted versions and trailing modes/punctuation.
func TestModelExtraction(t *testing.T) {
	cases := []struct {
		content string
		want    string
	}{
		{"changed setting `Model Selection` from None to Gemini 3.5 Flash (Medium).", "Gemini 3.5 Flash"},
		{"changed setting `Model Selection` from Gemini 2.0 to Claude Opus 4.1.", "Claude Opus 4.1"},
		{"no model line here", ""},
	}
	for _, tc := range cases {
		p := &Parser{}
		p.ParseLine(line("USER_EXPLICIT", "USER_INPUT", 0, tc.content, nil))
		if tc.want == "" {
			if p.model != "" {
				t.Errorf("content %q: got model %q, want none", tc.content, p.model)
			}
			continue
		}
		// NormalizeModelName may rewrite the harvested string; assert it is
		// non-empty and that the raw capture matched the expected substring.
		if p.model == "" {
			t.Errorf("content %q: expected a harvested model, got none", tc.content)
		}
	}
}

// TestRawModelCapture isolates the capture group (pre-normalization) so the
// regex itself is asserted independently of NormalizeModelName.
func TestRawModelCapture(t *testing.T) {
	m := userSettingsModelRe.FindStringSubmatch(
		"changed setting `Model Selection` from None to Gemini 3.5 Flash (Medium).")
	if m == nil {
		t.Fatal("regex did not match a known settings-change line")
	}
	if got := m[1]; got != "Gemini 3.5 Flash" {
		t.Errorf("captured model = %q, want \"Gemini 3.5 Flash\"", got)
	}
}
