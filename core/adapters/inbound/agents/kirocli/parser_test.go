package kirocli

import (
	"encoding/json"
	"testing"
	"time"
)

// line parses a raw JSONL string into the map shape ParseLine receives.
// Test inputs are verbatim lines from a live kiro-cli 2.5.1 session
// (.build/refresh/kiro-cli-smoke/), trimmed to the fields under test.
func line(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("bad test line: %v", err)
	}
	return m
}

func TestParser_Prompt_UserMessage(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"version":"v1","kind":"Prompt","data":{"message_id":"b68090cc","content":[{"kind":"text","data":"what is 2+2?"}],"meta":{"timestamp":1780612717}}}`))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "user_message" {
		t.Errorf("EventType = %q, want user_message", ev.EventType)
	}
	if !ev.ClearToolNames {
		t.Error("expected ClearToolNames on user prompt")
	}
	if want := time.Unix(1780612717, 0); !ev.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", ev.Timestamp, want)
	}
}

func TestParser_AssistantTextOnly_TurnDone(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"version":"v1","kind":"AssistantMessage","data":{"message_id":"aad4e312","content":[{"kind":"text","data":"2+2 is 4."}]}}`))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want turn_done", ev.EventType)
	}
	if ev.AssistantText != "2+2 is 4." {
		t.Errorf("AssistantText = %q", ev.AssistantText)
	}
	if len(ev.ToolUses) != 0 {
		t.Errorf("ToolUses = %v, want none", ev.ToolUses)
	}
}

func TestParser_AssistantWithToolUse_MidTurn(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"version":"v1","kind":"AssistantMessage","data":{"message_id":"f72c258e","content":[{"kind":"text","data":""},{"kind":"toolUse","data":{"toolUseId":"tooluse_hNI7POsrr87ovDvEEV0mlP","name":"write","input":{"command":"create","path":"hello.txt"}}}]}}`))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "assistant" {
		t.Errorf("EventType = %q, want assistant (toolUse keeps the turn open)", ev.EventType)
	}
	if len(ev.ToolUses) != 1 || ev.ToolUses[0].Name != "write" || ev.ToolUses[0].ID != "tooluse_hNI7POsrr87ovDvEEV0mlP" {
		t.Errorf("ToolUses = %+v", ev.ToolUses)
	}
}

func TestParser_ToolResults_ClosesTool(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"version":"v1","kind":"ToolResults","data":{"message_id":"65fe10cc","content":[{"kind":"toolResult","data":{"toolUseId":"tooluse_hNI7POsrr87ovDvEEV0mlP","content":[{"kind":"text","data":"Successfully created hello.txt"}],"status":"success"}}]}}`))
	if ev == nil || ev.Skip {
		t.Fatal("expected non-skipped event")
	}
	if ev.EventType != "tool_result" {
		t.Errorf("EventType = %q, want tool_result", ev.EventType)
	}
	if len(ev.ToolResultIDs) != 1 || ev.ToolResultIDs[0] != "tooluse_hNI7POsrr87ovDvEEV0mlP" {
		t.Errorf("ToolResultIDs = %v", ev.ToolResultIDs)
	}
	if ev.IsError {
		t.Error("IsError = true for status=success")
	}
}

// The three ToolResults status shapes below are verbatim lines from a live
// kiro-cli 2.6.0 probe (#592 finding 3), trimmed to the fields under test.
// kiro's status field is the tool HARNESS verdict: {success, error} is the
// complete vocabulary — there is no cancelled/denied status.

// A shell command exiting non-zero is still status:"success" (the exit code
// is payload data), so it must NOT raise IsError.
func TestParser_ToolResults_FailedCommandIsNotError(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"version":"v1","kind":"ToolResults","data":{"content":[{"kind":"toolResult","data":{"toolUseId":"tooluse_bn9siHin4aEo6wnCF1c9te","content":[{"kind":"json","data":{"exit_status":"exit status: 1","stdout":"","stderr":"cat: /nonexistent-file-probe-592: No such file or directory\n"}}],"status":"success"}}]}}`))
	if ev.IsError {
		t.Error("IsError = true for a non-zero-exit command recorded as status=success")
	}
}

// Tool-input validation failure → status:"error".
func TestParser_ToolResults_ErrorStatus(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"version":"v1","kind":"ToolResults","data":{"content":[{"kind":"toolResult","data":{"toolUseId":"tooluse_egAL467aQyT8eei5xhtBMa","content":[{"kind":"text","data":"Failed to parse the tool use: The tool arguments failed validation: '/nonexistent-probe-592-direct.txt' does not exist"}],"status":"error"}}]}}`))
	if !ev.IsError {
		t.Error("expected IsError for status=error")
	}
}

// A user-cancelled tool (Esc mid-flight) is ALSO status:"error" — kiro's own
// classification; only data.results[id].result == "Cancelled" distinguishes it.
func TestParser_ToolResults_CancelledIsError(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"version":"v1","kind":"ToolResults","data":{"content":[{"kind":"toolResult","data":{"toolUseId":"tooluse_eL1H20njj3mlLOKK9Wml7w","content":[{"kind":"text","data":"Tool use was cancelled by the user"}],"status":"error"}}],"results":{"tooluse_eL1H20njj3mlLOKK9Wml7w":{"tool":null,"result":"Cancelled"}}}}`))
	if !ev.IsError {
		t.Error("expected IsError for a cancelled tool (kiro records it as status=error)")
	}
	if len(ev.ToolResultIDs) != 1 || ev.ToolResultIDs[0] != "tooluse_eL1H20njj3mlLOKK9Wml7w" {
		t.Errorf("ToolResultIDs = %v, want the cancelled tool's id (cancellation must still close the tool)", ev.ToolResultIDs)
	}
}

func TestParser_Clear_Skipped(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"version":"v1","kind":"Clear","data":{}}`))
	if ev == nil || !ev.Skip {
		t.Error("expected Clear to be skipped")
	}
}

func TestParser_UnknownKind_Skipped(t *testing.T) {
	p := &Parser{}
	for _, raw := range []string{
		`{"version":"v1","kind":"SomethingNew","data":{}}`,
		`{"version":"v1"}`,
		`{}`,
	} {
		if ev := p.ParseLine(line(t, raw)); ev == nil || !ev.Skip {
			t.Errorf("expected skip for %s", raw)
		}
	}
}

func TestParser_TaskEstimateMarker(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"version":"v1","kind":"AssistantMessage","data":{"content":[{"kind":"text","data":"working on it <!-- {\"marker\":\"irrlicht-eta\",\"total_rounds\":5,\"completed_rounds\":2} -->"}]}}`))
	if ev.TaskEstimate == nil {
		t.Fatal("expected TaskEstimate from marker")
	}
	if ev.TaskEstimate.TotalRounds != 5 || ev.TaskEstimate.CompletedRounds != 2 {
		t.Errorf("TaskEstimate = %+v", ev.TaskEstimate)
	}
}

func TestParser_AssistantTextTruncated(t *testing.T) {
	p := &Parser{}
	long := make([]rune, 300)
	for i := range long {
		long[i] = 'x'
	}
	raw := map[string]interface{}{
		"version": "v1",
		"kind":    "AssistantMessage",
		"data": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{"kind": "text", "data": string(long)},
			},
		},
	}
	ev := p.ParseLine(raw)
	if got := len([]rune(ev.AssistantText)); got != 200 {
		t.Errorf("AssistantText length = %d, want 200", got)
	}
}

// TestParser_FullTurnSequence walks the exact event sequence captured live
// (Prompt → AM(toolUse) → ToolResults → AM(toolUse) → ToolResults →
// AM(text-only)) and asserts the state-relevant EventType progression.
func TestParser_FullTurnSequence(t *testing.T) {
	p := &Parser{}
	seq := []struct {
		raw  string
		want string
	}{
		{`{"version":"v1","kind":"Prompt","data":{"content":[{"kind":"text","data":"create hello.txt"}],"meta":{"timestamp":1780612801}}}`, "user_message"},
		{`{"version":"v1","kind":"AssistantMessage","data":{"content":[{"kind":"text","data":""},{"kind":"toolUse","data":{"toolUseId":"t1","name":"write","input":{}}}]}}`, "assistant"},
		{`{"version":"v1","kind":"ToolResults","data":{"content":[{"kind":"toolResult","data":{"toolUseId":"t1","status":"success"}}]}}`, "tool_result"},
		{`{"version":"v1","kind":"AssistantMessage","data":{"content":[{"kind":"text","data":""},{"kind":"toolUse","data":{"toolUseId":"t2","name":"shell","input":{}}}]}}`, "assistant"},
		{`{"version":"v1","kind":"ToolResults","data":{"content":[{"kind":"toolResult","data":{"toolUseId":"t2","status":"success"}}]}}`, "tool_result"},
		{`{"version":"v1","kind":"AssistantMessage","data":{"content":[{"kind":"text","data":"done: hello.txt is 6 bytes"}]}}`, "turn_done"},
	}
	for i, step := range seq {
		ev := p.ParseLine(line(t, step.raw))
		if ev == nil || ev.Skip {
			t.Fatalf("step %d: unexpected skip", i)
		}
		if ev.EventType != step.want {
			t.Errorf("step %d: EventType = %q, want %q", i, ev.EventType, step.want)
		}
	}
}
