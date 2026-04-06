package claudecode

import (
	"testing"
)

func TestParser_SystemEvent_TurnDone(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "system",
		"subtype":   "turn_duration",
		"timestamp": "2026-04-05T22:00:00Z",
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want turn_done", ev.EventType)
	}
	if ev.Skip {
		t.Error("turn_done should not be skipped — tailer needs it for LastEventType")
	}
}

func TestParser_SystemEvent_StopHookSummary(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "system",
		"subtype":   "stop_hook_summary",
		"timestamp": "2026-04-05T22:00:00Z",
	})
	if ev == nil || ev.EventType != "turn_done" {
		t.Errorf("expected turn_done for stop_hook_summary, got %+v", ev)
	}
}

func TestParser_IsMeta_Skip(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "user",
		"isMeta":    true,
		"timestamp": "2026-04-05T22:00:00Z",
	})
	if ev == nil || !ev.Skip {
		t.Error("expected isMeta user event to be skipped")
	}
}

func TestParser_LocalCommand_Skip(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "user",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"content": "<local-command-caveat>text</local-command-caveat>",
		},
	})
	if ev == nil || !ev.Skip {
		t.Error("expected local command to be skipped")
	}
}

func TestParser_PermissionMode(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":           "permission-mode",
		"permissionMode": "bypassPermissions",
		"timestamp":      "2026-04-05T22:00:00Z",
	})
	if ev == nil || !ev.Skip {
		t.Error("expected permission-mode to be skipped")
	}
	if ev.PermissionMode != "bypassPermissions" {
		t.Errorf("PermissionMode = %q, want bypassPermissions", ev.PermissionMode)
	}
}

func TestParser_ToolUseInContent(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role":        "assistant",
			"stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Let me check."},
				map[string]interface{}{"type": "tool_use", "name": "Bash", "id": "tu_1"},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if len(ev.ToolUseNames) != 1 || ev.ToolUseNames[0] != "Bash" {
		t.Errorf("ToolUseNames = %v, want [Bash]", ev.ToolUseNames)
	}
}

func TestParser_ToolResultInContent(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "user",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_1", "is_error": false},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.ToolResultCount != 1 {
		t.Errorf("ToolResultCount = %d, want 1", ev.ToolResultCount)
	}
}

func TestParser_AssistantText(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role":        "assistant",
			"stop_reason": "end_turn",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Should I proceed?"},
			},
		},
	})
	if ev.AssistantText != "Should I proceed?" {
		t.Errorf("AssistantText = %q, want question", ev.AssistantText)
	}
}

func TestParser_AssistantStreaming_NoStopReason(t *testing.T) {
	// Intermediate streaming messages (thinking, partial text) lack stop_reason.
	// They should emit "assistant_streaming" to prevent false IsAgentDone().
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "Let me think..."},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "assistant_streaming" {
		t.Errorf("EventType = %q, want assistant_streaming (no stop_reason)", ev.EventType)
	}
}

func TestParser_AssistantStreaming_TextNoStopReason(t *testing.T) {
	// Text-only assistant message without stop_reason = intermediate.
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Let me check the file..."},
			},
		},
	})
	if ev.EventType != "assistant_streaming" {
		t.Errorf("EventType = %q, want assistant_streaming", ev.EventType)
	}
	if ev.AssistantText != "Let me check the file..." {
		t.Errorf("AssistantText = %q, want text (should still extract)", ev.AssistantText)
	}
}

func TestParser_AssistantFinal_EndTurn(t *testing.T) {
	// Final response with stop_reason=end_turn keeps EventType="assistant".
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role":        "assistant",
			"stop_reason": "end_turn",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Done. All tests pass."},
			},
		},
	})
	if ev.EventType != "assistant" {
		t.Errorf("EventType = %q, want assistant (stop_reason=end_turn)", ev.EventType)
	}
}

func TestParser_AssistantFinal_ToolUse(t *testing.T) {
	// Tool call with stop_reason=tool_use keeps EventType="assistant".
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role":        "assistant",
			"stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Read", "id": "tu_1"},
			},
		},
	})
	if ev.EventType != "assistant" {
		t.Errorf("EventType = %q, want assistant (stop_reason=tool_use)", ev.EventType)
	}
	if len(ev.ToolUseNames) != 1 || ev.ToolUseNames[0] != "Read" {
		t.Errorf("ToolUseNames = %v, want [Read]", ev.ToolUseNames)
	}
}

func TestParser_AssistantFinal_NullStopReason(t *testing.T) {
	// stop_reason: null (field present, value nil) = potentially done.
	// Should keep "assistant" EventType (not "assistant_streaming").
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role":        "assistant",
			"stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Here are the files."},
			},
		},
	})
	if ev.EventType != "assistant" {
		t.Errorf("EventType = %q, want assistant (stop_reason=null → field present)", ev.EventType)
	}
}

func TestParser_UserInterrupted_SetsIsError(t *testing.T) {
	// ESC during text generation writes "[Request interrupted by user]"
	// as a user text message. Parser should set IsError=true.
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "user",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "[Request interrupted by user]"},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if !ev.IsError {
		t.Error("expected IsError=true for user interruption message")
	}
	if ev.EventType != "user" {
		t.Errorf("EventType = %q, want user", ev.EventType)
	}
}

func TestParser_UserMessage_ClearsToolNames(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "user",
		"timestamp": "2026-04-05T22:00:00Z",
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if !ev.ClearToolNames {
		t.Error("expected ClearToolNames=true for user event")
	}
}
