package event_test

import (
	"fmt"
	"testing"

	"irrlicht/core/domain/event"
)

func TestHookEvent_Validate_MissingFields(t *testing.T) {
	tests := []struct {
		name    string
		evt     event.HookEvent
		wantErr string
	}{
		{"no event name", event.HookEvent{SessionID: "s1"}, "hook_event_name"},
		{"no session id", event.HookEvent{HookEventName: "SessionStart"}, "session_id"},
		{"invalid event", event.HookEvent{HookEventName: "BadEvent", SessionID: "s1"}, "invalid event type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.evt.Validate(nil)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !containsStr(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestHookEvent_Validate_ValidEvents(t *testing.T) {
	validNames := []string{
		"SessionStart", "UserPromptSubmit", "Notification",
		"PreToolUse", "PostToolUse", "PreCompact",
		"Stop", "SubagentStop", "SessionEnd",
	}
	for _, name := range validNames {
		evt := event.HookEvent{HookEventName: name, SessionID: "sid"}
		if err := evt.Validate(nil); err != nil {
			t.Errorf("%q: unexpected error: %v", name, err)
		}
	}
}

func TestHookEvent_Validate_PathValidator(t *testing.T) {
	rejector := func(string) error { return fmt.Errorf("bad path") }
	evt := event.HookEvent{
		HookEventName:  "SessionStart",
		SessionID:      "s1",
		TranscriptPath: "/bad/path",
	}
	if err := evt.Validate(rejector); err == nil {
		t.Error("expected path validation error")
	}
}

func TestHookEvent_ResolveReason_DirectField(t *testing.T) {
	evt := event.HookEvent{Reason: "clear"}
	if got := evt.ResolveReason(); got != "clear" {
		t.Errorf("got %q, want clear", got)
	}
}

func TestHookEvent_ResolveReason_DataMap(t *testing.T) {
	evt := event.HookEvent{
		Data: map[string]interface{}{"reason": "logout"},
	}
	if got := evt.ResolveReason(); got != "logout" {
		t.Errorf("got %q, want logout", got)
	}
}

func TestHookEvent_ResolveReason_DirectTakesPrecedence(t *testing.T) {
	evt := event.HookEvent{
		Reason: "clear",
		Data:   map[string]interface{}{"reason": "logout"},
	}
	if got := evt.ResolveReason(); got != "clear" {
		t.Errorf("direct field should win, got %q", got)
	}
}

func TestHookEvent_Validate_PathValidatorDataMap(t *testing.T) {
	callCount := 0
	validator := func(p string) error {
		callCount++
		return nil
	}
	evt := event.HookEvent{
		HookEventName: "SessionStart",
		SessionID:     "s1",
		Data: map[string]interface{}{
			"transcript_path": "/home/user/transcript.json",
			"cwd":             "/home/user/project",
		},
	}
	if err := evt.Validate(validator); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 path validator calls, got %d", callCount)
	}
}

func TestHookEvent_Validate_BadCWD(t *testing.T) {
	rejector := func(p string) error {
		if p == "/bad/cwd" {
			return fmt.Errorf("bad path")
		}
		return nil
	}
	evt := event.HookEvent{
		HookEventName: "SessionStart",
		SessionID:     "s1",
		CWD:           "/bad/cwd",
	}
	if err := evt.Validate(rejector); err == nil {
		t.Error("expected error for bad CWD")
	}
}

func TestHookEvent_ResolveReason_Empty(t *testing.T) {
	evt := event.HookEvent{}
	if got := evt.ResolveReason(); got != "" {
		t.Errorf("empty event should return empty reason, got %q", got)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
