package opencode

import (
	"encoding/json"
	"testing"

	"irrlicht/core/pkg/tailer"
)

// recordedOpencodeError is the `_error` object verbatim from
// replaydata/agents/opencode/scenarios/1-8_model-context-display/recordings/
// 2026-06-05-11-06-00_irrlichd-0.4.8+44399ba/transcript.jsonl. Note the shape:
// {name, data:{message}} — NOT {name, message} as watcher.go's comment claims.
const recordedOpencodeErrorJSON = `{"name":"UnknownError","data":{"message":"\"The number of tokens to keep from the initial prompt is greater than the context length (n_keep: 19497>= n_ctx: 4096). Try to load the model with a larger context length, or provide a shorter input.\""}}`

func recordedOpencodeError(t *testing.T) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(recordedOpencodeErrorJSON), &m); err != nil {
		t.Fatalf("unmarshal recorded _error: %v", err)
	}
	return m
}

func TestParser_MessageError_IsASessionError(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"_role":  "assistant",
		"type":   "text",
		"text":   "partial output",
		"_error": recordedOpencodeError(t),
	})
	if ev == nil {
		t.Fatal("ParseLine returned nil")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want \"turn_done\" (unchanged, load-bearing for #493)", ev.EventType)
	}
	if ev.SessionError == nil {
		t.Fatal("SessionError is nil — the error object is read only as a boolean today")
	}
	if ev.SessionError.Phase != tailer.ErrorPhaseTerminal {
		t.Errorf("Phase = %q, want terminal", ev.SessionError.Phase)
	}
	if ev.SessionError.Message == "" {
		t.Error("Message is empty — it lives at error.data.message, not error.message")
	}
}

func TestParser_StepFinishError_IsASessionError(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"_role":  "assistant",
		"type":   "step-finish",
		"reason": "error",
	})
	if ev == nil {
		t.Fatal("ParseLine returned nil")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want \"turn_done\" (unchanged)", ev.EventType)
	}
	if ev.SessionError == nil {
		t.Fatal("SessionError is nil — step-finish reason:\"error\" settles the turn but " +
			"reports nothing about why")
	}
}

// LOCK — a clean turn must stay clean.
func TestParser_StepFinishStop_HasNoSessionError(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"_role": "assistant", "type": "step-finish", "reason": "stop",
	})
	if ev == nil || ev.EventType != "turn_done" {
		t.Fatalf("precondition: a clean step-finish must be turn_done, got %+v", ev)
	}
	if ev.SessionError != nil {
		t.Errorf("SessionError must be nil for reason:\"stop\", got %+v", ev.SessionError)
	}
}

// The live/replay divergence: the parser accepts ANY non-nil _error while
// watcher.go's isErrorMessage rejects empty and non-error-shaped values. A
// `{}` therefore settles the turn under replay and not live, so the golden and
// the daemon disagree about the same session.
func TestParser_EmptyErrorObject_IsNotATurnEnd(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"_role": "assistant", "type": "text", "text": "still going",
		"_error": map[string]interface{}{},
	})
	if ev == nil {
		t.Fatal("ParseLine returned nil")
	}
	if ev.EventType == "turn_done" {
		t.Error("an empty _error object ended the turn — watcher.go's isErrorMessage " +
			"rejects this shape, so live and replay disagree")
	}
	if ev.SessionError != nil {
		t.Errorf("an empty _error object must not produce a session error, got %+v", ev.SessionError)
	}
}

// The live path builds the parser's input itself and never injected _error,
// so a message-level error reached the parser only under replay.
//
// ADDED CHECK, not a defect test: it could not compile before the signature
// change, so its red-first evidence is the mutation in the PR body (drop the
// `_error` injection from buildPartRaw → this fails).
func TestBuildPartRaw_CarriesTheMessageError(t *testing.T) {
	raw, ok := buildPartRaw(`{"type":"text","text":"partial"}`, "assistant", "/tmp", 1, "m",
		`{"role":"assistant","error":`+recordedOpencodeErrorJSON+`}`)
	if !ok {
		t.Fatal("buildPartRaw returned ok=false")
	}
	if raw["_error"] == nil {
		t.Fatal("_error absent — the live path drops the message-level error before the " +
			"parser ever sees it")
	}
}

// A message with no error must not gain a phantom `_error` key: the parser's
// branch fires on presence, so injecting a nil would end every turn.
func TestBuildPartRaw_CleanMessageHasNoErrorKey(t *testing.T) {
	raw, ok := buildPartRaw(`{"type":"text","text":"fine"}`, "assistant", "/tmp", 1, "m",
		`{"role":"assistant","error":null}`)
	if !ok {
		t.Fatal("buildPartRaw returned ok=false")
	}
	if _, present := raw["_error"]; present {
		t.Error("a clean message must not carry an _error key at all")
	}
}
