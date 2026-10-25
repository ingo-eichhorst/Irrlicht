package opencode

import (
	"testing"

	"irrlicht/core/pkg/tailer"
)

// v2Row decodes one session_message row into the map ParseLine takes, with
// the 2.x metrics path's own builder.
func v2Row(t *testing.T, typ, data string) map[string]interface{} {
	t.Helper()
	raw, ok := buildV2MessageRaw(typ, data, fixtureDir, 1)
	if !ok {
		t.Fatalf("fixture row %s does not parse", data)
	}
	return raw
}

// TestParser_V2IdleOutcomes is #2106's red-first parser test for turn end.
// The idle row is the turn's end in opencode 2.x. Its outcome values are the
// schema's enum (succeeded, failed, interrupted — openapi
// Session.Message.Idle); succeeded and interrupted were recorded in the
// sandbox store, and failed was produced there by a session created with a
// model id its provider does not serve, which wrote NO assistant row and put
// the error on the idle row itself (testdata ses_e8bc9b17affe…).
func TestParser_V2IdleOutcomes(t *testing.T) {
	for _, c := range []struct {
		name      string
		data      string
		wantClass string
		wantMsg   string
	}{
		{"succeeded", `{"time":{"created":1},"outcome":"succeeded"}`, "", ""},
		{"interrupted", `{"time":{"created":1},"outcome":"interrupted"}`, "", ""},
		{"failed_with_its_own_error",
			`{"time":{"created":1},"outcome":"failed","error":{"type":"provider.no-route","message":"Model unavailable: local-splash/x"}}`,
			"provider.no-route", "Model unavailable: local-splash/x"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ev := (&Parser{}).ParseLine(v2Row(t, "idle", c.data))
			if ev == nil || ev.Skip || ev.EventType != "turn_done" {
				t.Fatalf("idle %s parsed to %+v, want turn_done", c.name, ev)
			}
			if c.wantClass == "" {
				if ev.SessionError != nil {
					t.Errorf("SessionError = %+v, want nil", ev.SessionError)
				}
				return
			}
			if se := ev.SessionError; se == nil || se.Class != c.wantClass || se.Message != c.wantMsg ||
				se.Phase != tailer.ErrorPhaseTerminal {
				t.Errorf("SessionError = %+v, want class %q, message %q, terminal", se, c.wantClass, c.wantMsg)
			}
		})
	}
}

// TestParser_V2FailedOutcomeFallsBackToTheStepsError: a failed idle row that
// carries no error of its own reports the error on the last assistant row
// before it. NOT RECORDED: the one failed outcome measured (testdata
// ses_e8bc9b17affe…) put its error on the idle row; this is the schema's
// other error site (openapi Session.Message.Assistant.error), read so a
// failure is never reported with no text when one exists.
func TestParser_V2FailedOutcomeFallsBackToTheStepsError(t *testing.T) {
	p := &Parser{}
	p.ParseLine(v2Row(t, "assistant",
		`{"model":{"id":"m"},"content":[],"finish":"error","error":{"type":"provider.http","message":"upstream 502","status":502}}`))
	ev := p.ParseLine(v2Row(t, "idle", `{"time":{"created":1},"outcome":"failed"}`))
	se := ev.SessionError
	if se == nil || se.Class != "provider.http" || se.Message != "upstream 502" || se.HTTPStatus == nil || *se.HTTPStatus != 502 {
		t.Fatalf("SessionError = %+v, want the step's provider.http error with status 502", se)
	}

	bare := (&Parser{}).ParseLine(v2Row(t, "idle", `{"time":{"created":1},"outcome":"failed"}`))
	if bare.SessionError == nil || bare.SessionError.Message == "" {
		t.Errorf("a failed outcome with no error anywhere reported SessionError %+v, want one saying the turn failed", bare.SessionError)
	}
}

// TestParser_V2InterruptedStepIsNotAFailure: an interrupt writes its step with
// finish "error" and error {type: "aborted"} (recorded, testdata
// ses_e8cee7cdaffe…). That error must not resurface as the failure of a later
// failed outcome in the same pass — an idle row consumes it.
func TestParser_V2InterruptedStepIsNotAFailure(t *testing.T) {
	p := &Parser{}
	step := p.ParseLine(v2Row(t, "assistant",
		`{"model":{"id":"m"},"content":[{"type":"reasoning","text":"…"}],"finish":"error","error":{"type":"aborted","message":"Step interrupted"}}`))
	if step.SessionError != nil || step.EventType != "assistant_message" {
		t.Errorf("the interrupted step parsed to %q with SessionError %+v, want assistant_message and none",
			step.EventType, step.SessionError)
	}
	p.ParseLine(v2Row(t, "idle", `{"time":{"created":1},"outcome":"interrupted"}`))
	next := p.ParseLine(v2Row(t, "idle", `{"time":{"created":2},"outcome":"failed"}`))
	if next.SessionError != nil && next.SessionError.Class == "aborted" {
		t.Errorf("a later failure reported the earlier interrupt's error: %+v", next.SessionError)
	}
}

// TestParser_V2UserAndAssistantRows pins the shapes the sandbox store
// recorded: a user row is a new user turn; an assistant row is one step whose
// content[] holds reasoning, text and tool items, with the tool's progress in
// state.status (running while a permission is held — sampled — then completed
// or error).
func TestParser_V2UserAndAssistantRows(t *testing.T) {
	p := &Parser{}
	user := p.ParseLine(v2Row(t, "user", `{"time":{"created":1},"text":"Run date.","files":[]}`))
	if user.EventType != "user_message" || !user.ClearToolNames || user.UserText != "Run date." {
		t.Errorf("user row parsed to %+v, want user_message, ClearToolNames, UserText", user)
	}

	running := p.ParseLine(v2Row(t, "assistant", `{"model":{"id":"qwen3.8-27b-splash","providerID":"local-splash"},
		"content":[{"type":"reasoning","text":"thinking"},
		           {"type":"tool","id":"call_1","name":"shell","state":{"status":"running","input":{"command":"date"},"metadata":{}}}]}`))
	if running.EventType != "assistant_message" || len(running.ToolUses) != 1 ||
		running.ToolUses[0] != (tailer.ToolUse{ID: "call_1", Name: "shell"}) {
		t.Errorf("a step with a running tool parsed to %q with ToolUses %+v, want assistant_message with call_1/shell",
			running.EventType, running.ToolUses)
	}
	if running.ModelName != "qwen3.8-27b-splash" {
		t.Errorf("ModelName = %q, want the row's data.model.id", running.ModelName)
	}

	done := p.ParseLine(v2Row(t, "assistant", `{"model":{"id":"m"},
		"content":[{"type":"tool","id":"call_1","name":"shell","state":{"status":"completed","input":{},"content":[{"type":"text","text":"Sun"}]}},
		           {"type":"tool","id":"call_2","name":"shell","state":{"status":"error","input":{},"error":{"type":"permission.rejected","message":"no"}}},
		           {"type":"text","text":"It is Sunday."}],
		"finish":"stop","tokens":{"input":10,"output":20,"reasoning":30,"cache":{"read":40,"write":50}}}`))
	if len(done.ToolResultIDs) != 2 || done.ToolResultIDs[0] != "call_1" || done.ToolResultIDs[1] != "call_2" || !done.IsError {
		t.Errorf("completed/error tools parsed to ToolResultIDs %v, IsError %t; want [call_1 call_2], true",
			done.ToolResultIDs, done.IsError)
	}
	if done.AssistantText != "It is Sunday." {
		t.Errorf("AssistantText = %q, want the step's text", done.AssistantText)
	}
	if done.Tokens == nil || done.Tokens.Total != 150 || done.Tokens.Input != 10 || done.Tokens.CacheRead != 40 {
		t.Errorf("Tokens = %+v, want input 10, cache read 40, total 150 (all five counts)", done.Tokens)
	}
}

// TestParser_V2IdleClosesToolsLeftOpen: an idle row is the end of the
// session's run, so a tool its last stored step still shows as running is
// reported closed rather than holding the session in working forever.
func TestParser_V2IdleClosesToolsLeftOpen(t *testing.T) {
	p := &Parser{}
	p.ParseLine(v2Row(t, "assistant",
		`{"model":{"id":"m"},"content":[{"type":"tool","id":"call_9","name":"shell","state":{"status":"running","input":{},"metadata":{}}}]}`))
	ev := p.ParseLine(v2Row(t, "idle", `{"time":{"created":1},"outcome":"interrupted"}`))
	if len(ev.ToolResultIDs) != 1 || ev.ToolResultIDs[0] != "call_9" {
		t.Errorf("idle after a running tool parsed to ToolResultIDs %v, want [call_9]", ev.ToolResultIDs)
	}
}

// TestParser_V1PartsStillTakeTheV1Branch is a lock: the 2.x branch is chosen
// by the row's type, and no 1.x part type is user, assistant or idle.
func TestParser_V1PartsStillTakeTheV1Branch(t *testing.T) {
	ev := (&Parser{}).ParseLine(map[string]interface{}{"type": "step-finish", "reason": "stop"})
	if ev.EventType != "turn_done" {
		t.Errorf("a 1.x step-finish parsed to %q, want turn_done", ev.EventType)
	}
	text := (&Parser{}).ParseLine(map[string]interface{}{"type": "text", "_role": "user", "text": "hi"})
	if text.EventType != "user_message" {
		t.Errorf("a 1.x user text part parsed to %q, want user_message", text.EventType)
	}
}
