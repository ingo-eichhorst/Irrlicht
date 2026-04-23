package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/pkg/tailer"
)

// --- Contribution / cost tests ---

func TestParser_Contribution_RequestIDDedup(t *testing.T) {
	p := &Parser{}

	usage := map[string]interface{}{
		"input_tokens": float64(1000), "output_tokens": float64(50),
	}
	line1 := map[string]interface{}{
		"type": "assistant", "requestId": "req-1",
		"message": map[string]interface{}{"model": "claude-sonnet-4-6", "usage": usage},
	}
	line2 := map[string]interface{}{
		"type": "assistant", "requestId": "req-1",
		"message": map[string]interface{}{"model": "claude-sonnet-4-6", "usage": map[string]interface{}{
			"input_tokens": float64(1000), "output_tokens": float64(300),
		}},
	}
	line3 := map[string]interface{}{
		"type": "assistant", "requestId": "req-2",
		"message": map[string]interface{}{"model": "claude-sonnet-4-6", "usage": map[string]interface{}{
			"input_tokens": float64(500), "output_tokens": float64(100),
		}},
	}

	_ = p.ParseLine(line1) // pending turn 1
	ev2 := p.ParseLine(line2) // same requestId — update pending, no Contribution yet
	if ev2.Contribution != nil {
		t.Error("same requestId should not emit Contribution yet")
	}
	ev3 := p.ParseLine(line3) // new requestId — flush turn 1
	if ev3.Contribution == nil {
		t.Fatal("new requestId should emit Contribution for the completed turn")
	}
	// Turn 1's final snapshot had output=300 (from line2).
	if ev3.Contribution.Usage.Output != 300 {
		t.Errorf("Output = %d, want 300 (latest snapshot for req-1)", ev3.Contribution.Usage.Output)
	}
	if ev3.Contribution.Usage.Input != 1000 {
		t.Errorf("Input = %d, want 1000", ev3.Contribution.Usage.Input)
	}
	// PendingContribution should reflect the in-progress turn 2.
	if p.PendingContribution() == nil {
		t.Fatal("PendingContribution should hold turn 2 after line3")
	}
	if p.PendingContribution().Usage.Input != 500 {
		t.Errorf("pending Input = %d, want 500", p.PendingContribution().Usage.Input)
	}
}

func TestParser_Contribution_AnthropicCacheSubRates(t *testing.T) {
	p := &Parser{}

	// Anthropic nested cache_creation sub-rates.
	line := map[string]interface{}{
		"type": "assistant", "requestId": "req-1",
		"message": map[string]interface{}{
			"model": "claude-sonnet-4-6",
			"usage": map[string]interface{}{
				"input_tokens":  float64(1000),
				"output_tokens": float64(200),
				"cache_creation": map[string]interface{}{
					"ephemeral_5m_input_tokens": float64(300),
					"ephemeral_1h_input_tokens": float64(100),
				},
			},
		},
	}
	line2 := map[string]interface{}{
		"type": "assistant", "requestId": "req-2",
		"message": map[string]interface{}{"model": "claude-sonnet-4-6", "usage": map[string]interface{}{
			"input_tokens": float64(10),
		}},
	}
	_ = p.ParseLine(line)
	ev := p.ParseLine(line2) // flush req-1

	if ev.Contribution == nil {
		t.Fatal("expected Contribution on turn change")
	}
	if ev.Contribution.Usage.CacheCreation5m != 300 {
		t.Errorf("CacheCreation5m = %d, want 300", ev.Contribution.Usage.CacheCreation5m)
	}
	if ev.Contribution.Usage.CacheCreation1h != 100 {
		t.Errorf("CacheCreation1h = %d, want 100", ev.Contribution.Usage.CacheCreation1h)
	}
}

func TestParser_Contribution_FlatCacheCreationFallback(t *testing.T) {
	p := &Parser{}

	// No nested cache_creation — flat field should map to 5m.
	line := map[string]interface{}{
		"type": "assistant", "requestId": "req-1",
		"message": map[string]interface{}{
			"model": "claude-sonnet-4-6",
			"usage": map[string]interface{}{
				"input_tokens":                     float64(500),
				"cache_creation_input_tokens":       float64(200),
			},
		},
	}
	line2 := map[string]interface{}{
		"type": "assistant", "requestId": "req-2",
		"message": map[string]interface{}{"model": "claude-sonnet-4-6", "usage": map[string]interface{}{
			"input_tokens": float64(1),
		}},
	}
	_ = p.ParseLine(line)
	ev := p.ParseLine(line2)

	if ev.Contribution == nil {
		t.Fatal("expected Contribution")
	}
	if ev.Contribution.Usage.CacheCreation5m != 200 {
		t.Errorf("CacheCreation5m = %d, want 200 (flat fallback)", ev.Contribution.Usage.CacheCreation5m)
	}
	if ev.Contribution.Usage.CacheCreation1h != 0 {
		t.Errorf("CacheCreation1h = %d, want 0", ev.Contribution.Usage.CacheCreation1h)
	}
}

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
	if len(ev.ToolUses) != 1 || ev.ToolUses[0].Name != "Bash" || ev.ToolUses[0].ID != "tu_1" {
		t.Errorf("ToolUses = %v, want [{tu_1 Bash}]", ev.ToolUses)
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
	if len(ev.ToolResultIDs) != 1 || ev.ToolResultIDs[0] != "tu_1" {
		t.Errorf("ToolResultIDs = %v, want [tu_1]", ev.ToolResultIDs)
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

func TestParser_AskUserQuestion_FallsBackWhenNoText(t *testing.T) {
	// Assistant message with only a tool_use block for AskUserQuestion and no
	// text block: parser should fall back to questions[0].question so the
	// waiting-state UI has something to display.
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role":        "assistant",
			"stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{
					"type": "tool_use",
					"id":   "tu_1",
					"name": "AskUserQuestion",
					"input": map[string]interface{}{
						"questions": []interface{}{
							map[string]interface{}{"question": "Proceed?"},
						},
					},
				},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.AssistantText != "Proceed?" {
		t.Errorf("AssistantText = %q, want %q", ev.AssistantText, "Proceed?")
	}
}

func TestParser_AskUserQuestion_LongQuestionStoresTail(t *testing.T) {
	// 250-rune question exceeds the 200-rune cap. Fallback must keep the tail
	// (so the '?' at the end survives) with a leading ellipsis.
	longQ := ""
	for i := 0; i < 249; i++ {
		longQ += "x"
	}
	longQ += "?"
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role":        "assistant",
			"stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{
					"type": "tool_use",
					"name": "AskUserQuestion",
					"input": map[string]interface{}{
						"questions": []interface{}{
							map[string]interface{}{"question": longQ},
						},
					},
				},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	got := ev.AssistantText
	runes := []rune(got)
	if len(runes) == 0 || runes[0] != '…' {
		t.Errorf("AssistantText should start with '…', got %q", got)
	}
	if len(runes) > 201 {
		t.Errorf("AssistantText too long: %d runes (want ≤201)", len(runes))
	}
	if runes[len(runes)-1] != '?' {
		t.Errorf("AssistantText should end with '?' (tail-preserving), got last rune %q", string(runes[len(runes)-1]))
	}
}

func TestParser_AssistantStreaming_ThinkingBlock(t *testing.T) {
	// Thinking blocks are always intermediate regardless of ID.
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"id": "msg_new", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "Let me think..."},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "assistant_streaming" {
		t.Errorf("EventType = %q, want assistant_streaming (thinking block)", ev.EventType)
	}
}

func TestParser_AssistantStreaming_SameID(t *testing.T) {
	// Consecutive messages with the same ID are part of one streaming sequence.
	p := &Parser{}

	// First message sets the ID.
	p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"id": "msg_abc", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "analyzing..."},
			},
		},
	})

	// Second message with same ID + null stop_reason → streaming.
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:01Z",
		"message": map[string]interface{}{
			"id": "msg_abc", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Let me check the file..."},
			},
		},
	})
	if ev.EventType != "assistant_streaming" {
		t.Errorf("EventType = %q, want assistant_streaming (same ID)", ev.EventType)
	}
	if ev.AssistantText != "Let me check the file..." {
		t.Errorf("AssistantText = %q, want text (should still extract)", ev.AssistantText)
	}
}

func TestParser_AssistantStreaming_NullStopReason_DifferentID(t *testing.T) {
	// Any assistant message with stop_reason=null is intermediate streaming,
	// even with a new message ID. Claude Code final messages always have
	// stop_reason="end_turn" or "tool_use". Classifying these as "assistant"
	// caused working→ready flicker between tool calls.
	p := &Parser{}

	// Set a previous ID.
	p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"id": "msg_old", "role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Bash", "id": "tu_1"},
			},
		},
	})

	// New ID + null stop_reason → streaming (not final).
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:01Z",
		"message": map[string]interface{}{
			"id": "msg_new", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Here are the files."},
			},
		},
	})
	if ev.EventType != "assistant_streaming" {
		t.Errorf("EventType = %q, want assistant_streaming (null stop_reason is always intermediate)", ev.EventType)
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
	if len(ev.ToolUses) != 1 || ev.ToolUses[0].Name != "Read" || ev.ToolUses[0].ID != "tu_1" {
		t.Errorf("ToolUses = %v, want [{tu_1 Read}]", ev.ToolUses)
	}
}

func TestParser_AssistantStreaming_NullStopReason_SameID(t *testing.T) {
	// stop_reason: null + same message ID as previous = streaming sequence.
	p := &Parser{}

	// First message in sequence
	p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"id": "msg_x", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "hmm"},
			},
		},
	})

	// Same ID, text content → streaming
	ev := p.ParseLine(map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:01Z",
		"message": map[string]interface{}{
			"id": "msg_x", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Here are the files."},
			},
		},
	})
	if ev.EventType != "assistant_streaming" {
		t.Errorf("EventType = %q, want assistant_streaming (null + same ID)", ev.EventType)
	}
}

func TestParser_MaxTokensStopReason_ClassifiedAsStreaming(t *testing.T) {
	// Regression for issue #102 Bug D: an assistant message with
	// stop_reason="max_tokens" is the agent hitting its thinking budget
	// mid-turn, not end-of-turn. It must be classified as streaming so
	// IsAgentDone() doesn't fire. Same for pause_turn and unknown reasons.
	p := &Parser{}
	for _, stop := range []string{"max_tokens", "pause_turn", "something_new"} {
		ev := p.ParseLine(map[string]interface{}{
			"type":      "assistant",
			"timestamp": "2026-04-07T20:02:08Z",
			"message": map[string]interface{}{
				"role":        "assistant",
				"stop_reason": stop,
				"content": []interface{}{
					map[string]interface{}{"type": "thinking", "thinking": "…"},
				},
			},
		})
		if ev.EventType != "assistant_streaming" {
			t.Errorf("stop_reason=%q: EventType = %q, want assistant_streaming", stop, ev.EventType)
		}
	}
}

func TestParser_UserInterrupted_SetsIsUserInterrupt(t *testing.T) {
	// ESC during text generation writes "[Request interrupted by user]"
	// as a user text message. Parser should set IsUserInterrupt=true, NOT
	// IsError=true — see issue #102 Bug B: we must distinguish a real ESC
	// from a benign tool_result.is_error=true (grep miss, build fail).
	// And NOT IsToolDenial — that's the "for tool use" suffix variant.
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
	if !ev.IsUserInterrupt {
		t.Error("expected IsUserInterrupt=true for user ESC interruption")
	}
	if ev.IsToolDenial {
		t.Error("IsToolDenial must be false for plain ESC — that flag is reserved for the 'for tool use' marker")
	}
	if ev.IsError {
		t.Error("IsError must remain false for ESC interrupts — it's reserved for tool_result.is_error")
	}
	if ev.EventType != "user" {
		t.Errorf("EventType = %q, want user", ev.EventType)
	}
}

func TestParser_ToolDenial_SetsIsToolDenialNotUserInterrupt(t *testing.T) {
	// When the user clicks "no" on a permission prompt, Claude Code writes
	// "[Request interrupted by user for tool use]" as a user text message.
	// This is a *different* signal from ESC: the agent's turn is NOT over,
	// so the cancellation rule must NOT fire. Parser sets IsToolDenial=true
	// and leaves IsUserInterrupt=false so the classifier keeps the session
	// in working instead of bouncing through ready.
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "user",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "[Request interrupted by user for tool use]"},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if !ev.IsToolDenial {
		t.Error("expected IsToolDenial=true for tool permission denial")
	}
	if ev.IsUserInterrupt {
		t.Error("IsUserInterrupt must be false for tool denials — that flag is reserved for plain ESC")
	}
	if ev.IsError {
		t.Error("IsError must remain false for tool denials — it's reserved for tool_result.is_error")
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

// --- Integration tests: full transcript sequences through tailer ---
//
// These tests simulate real Claude Code transcript patterns observed in
// sessions 8b00212a and a27640c8 that exposed bugs #84.

func ts(offset int) string {
	return time.Now().Add(time.Duration(offset) * time.Second).Format(time.RFC3339)
}

func writeTranscript(t *testing.T, lines []map[string]interface{}) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, line := range lines {
		if err := enc.Encode(line); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func newCCTailer(path string) *tailer.TranscriptTailer {
	return tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
}

// Regression: consecutive assistant messages (thinking + text + tool_use)
// caused false ready because intermediate messages set LastEventType="assistant".
// With stop_reason + message ID check, intermediates use "assistant_streaming".
func TestTranscript_ConsecutiveAssistantMessages_StaysWorking(t *testing.T) {
	// Real pattern from session 8b00212a: thinking → text → tool_use (same API call ID)
	path := writeTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "fix the bug",
		}},
		// Thinking block — intermediate (same ID, thinking content)
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "Let me analyze..."},
			},
		}},
		// Text response — intermediate (same ID as thinking)
		{"type": "assistant", "timestamp": ts(2), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "I'll check the file."},
			},
		}},
	})

	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	// Last event should be "assistant_streaming" (not "assistant")
	if m.LastEventType != "assistant_streaming" {
		t.Errorf("LastEventType = %q, want assistant_streaming (intermediate text)", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false (no tool_use yet)")
	}
}

// The tool_use message with stop_reason="tool_use" should keep "assistant".
func TestTranscript_ToolCallAfterStreaming_Working(t *testing.T) {
	path := writeTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "fix the bug",
		}},
		// Thinking (streaming, same API call msg_1)
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "analyzing..."},
			},
		}},
		// Text (streaming, same API call msg_1)
		{"type": "assistant", "timestamp": ts(2), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Let me read that."},
			},
		}},
		// Tool call with stop_reason — final message of API call msg_1
		{"type": "assistant", "timestamp": ts(3), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Read", "id": "tu_1"},
			},
		}},
	})

	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "assistant" {
		t.Errorf("LastEventType = %q, want assistant", m.LastEventType)
	}
	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true")
	}
}

// Full multi-tool turn: tool calls resolve, mid-turn text stays working,
// final end_turn correctly signals done.
func TestTranscript_MultiToolTurn_FullCycle(t *testing.T) {
	path := writeTranscript(t, []map[string]interface{}{
		// User starts turn
		{"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "refactor the module",
		}},
		// Thinking (API call msg_1)
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "planning..."},
			},
		}},
		// First tool call (API call msg_1)
		{"type": "assistant", "timestamp": ts(2), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Let me read the file."},
				map[string]interface{}{"type": "tool_use", "name": "Read", "id": "tu_1"},
			},
		}},
		// Tool result
		{"type": "user", "timestamp": ts(3), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_1", "is_error": false},
			},
		}},
		// Mid-turn text (API call msg_2, different from msg_1)
		{"type": "assistant", "timestamp": ts(4), "message": map[string]interface{}{
			"id": "msg_2", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Now I'll edit it."},
			},
		}},
		// Second tool call (API call msg_2)
		{"type": "assistant", "timestamp": ts(5), "message": map[string]interface{}{
			"id": "msg_2", "role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Edit", "id": "tu_2"},
			},
		}},
		// Tool result
		{"type": "user", "timestamp": ts(6), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_2", "is_error": false},
			},
		}},
		// Final response (API call msg_3)
		{"type": "assistant", "timestamp": ts(7), "message": map[string]interface{}{
			"id": "msg_3", "role": "assistant", "stop_reason": "end_turn",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Done. The refactoring is complete."},
			},
		}},
	})

	tail := newCCTailer(path)

	// Simulate reading only up to the mid-turn text (line 4).
	// In reality the debounce might fire at this point.
	m, err := tail.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	// After the full transcript: last event is end_turn assistant.
	if m.LastEventType != "assistant" {
		t.Errorf("LastEventType = %q, want assistant (stop_reason=end_turn)", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false (all tools resolved)")
	}
	if m.LastAssistantText != "Done. The refactoring is complete." {
		t.Errorf("LastAssistantText = %q", m.LastAssistantText)
	}
}

// Incremental read: first read stops at mid-turn text, second read gets tool_use.
// Verifies that the mid-turn state doesn't trigger false ready.
func TestTranscript_IncrementalRead_MidTurnThenToolUse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	// Write first batch: user → thinking (streaming)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	enc.Encode(map[string]interface{}{
		"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "fix it",
		},
	})
	enc.Encode(map[string]interface{}{
		"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "I'll check..."},
			},
		},
	})
	f.Close()

	tail := newCCTailer(path)

	// First read: mid-turn text only
	m, err := tail.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "assistant_streaming" {
		t.Errorf("first read: LastEventType = %q, want assistant_streaming", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Error("first read: expected HasOpenToolCall=false")
	}

	// Append tool_use (same API call msg_1)
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	enc = json.NewEncoder(f)
	enc.Encode(map[string]interface{}{
		"type": "assistant", "timestamp": ts(2), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Bash", "id": "tu_1"},
			},
		},
	})
	f.Close()

	// Second read: picks up tool_use
	m, err = tail.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "assistant" {
		t.Errorf("second read: LastEventType = %q, want assistant", m.LastEventType)
	}
	if !m.HasOpenToolCall {
		t.Error("second read: expected HasOpenToolCall=true")
	}
}

// ESC during text generation: user message with "[Request interrupted by user]"
// should trigger cancellation detection via LastWasUserInterrupt (not IsError —
// see issue #102 Bug B).
func TestTranscript_ESCDuringGeneration_TransitionsToReady(t *testing.T) {
	path := writeTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "write a flight sim",
		}},
		// Tool call
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Bash", "id": "tu_1"},
			},
		}},
		// Tool result (success)
		{"type": "user", "timestamp": ts(2), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_1", "is_error": false},
			},
		}},
		// Second tool call
		{"type": "assistant", "timestamp": ts(3), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Bash", "id": "tu_2"},
			},
		}},
		// Tool result (success)
		{"type": "user", "timestamp": ts(4), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_2", "is_error": false},
			},
		}},
		// ESC during text generation
		{"type": "user", "timestamp": ts(5), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "[Request interrupted by user]"},
			},
		}},
	})

	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "user" {
		t.Errorf("LastEventType = %q, want user", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false (both tools resolved)")
	}
	if !m.LastWasUserInterrupt {
		t.Error("expected LastWasUserInterrupt=true (ESC text marker)")
	}
	if m.LastWasToolDenial {
		t.Error("expected LastWasToolDenial=false (this is plain ESC, not the 'for tool use' marker)")
	}
}

// Tool denial: user clicks "no" on a permission prompt → Claude Code writes
// "[Request interrupted by user for tool use]" as a user text message. The
// parser must set LastWasToolDenial (not LastWasUserInterrupt) so the
// classifier's cancellation rule does NOT fire — denials don't end the turn.
func TestTranscript_ToolDenial_DoesNotSetUserInterrupt(t *testing.T) {
	path := writeTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "delete /tmp/foo for me",
		}},
		// Tool call requesting Bash permission
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Bash", "id": "tu_1"},
			},
		}},
		// User denies the permission prompt
		{"type": "user", "timestamp": ts(2), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "[Request interrupted by user for tool use]"},
			},
		}},
	})

	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "user" {
		t.Errorf("LastEventType = %q, want user", m.LastEventType)
	}
	if !m.LastWasToolDenial {
		t.Error("expected LastWasToolDenial=true (the 'for tool use' marker)")
	}
	if m.LastWasUserInterrupt {
		t.Error("expected LastWasUserInterrupt=false (denial is not ESC)")
	}
}

// Session without turn_done events (observed in session 14807218):
// end_turn stop_reason is the only done signal.
func TestTranscript_NoTurnDone_EndTurnStopReason(t *testing.T) {
	path := writeTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "ls on home",
		}},
		// Tool call
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Bash", "id": "tu_1"},
			},
		}},
		// Tool result
		{"type": "user", "timestamp": ts(2), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_1", "is_error": false},
			},
		}},
		// Final response — no system/turn_duration follows
		{"type": "assistant", "timestamp": ts(3), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "end_turn",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Here are the files in your home directory."},
			},
		}},
	})

	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	// end_turn message should emit "assistant" (IsAgentDone triggers)
	if m.LastEventType != "assistant" {
		t.Errorf("LastEventType = %q, want assistant (stop_reason=end_turn)", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false")
	}
}

// Simple text-only turn (no tools, no turn_done): "hi" → "Hi!"
// stop_reason=end_turn should correctly trigger done.
func TestTranscript_SimpleTextTurn_EndTurn(t *testing.T) {
	path := writeTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "hi",
		}},
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "end_turn",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Hi! How can I help?"},
			},
		}},
	})

	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "assistant" {
		t.Errorf("LastEventType = %q, want assistant", m.LastEventType)
	}
	if m.LastAssistantText != "Hi! How can I help?" {
		t.Errorf("LastAssistantText = %q", m.LastAssistantText)
	}
}

// Regression: first chunk of a new assistant message between tool calls was
// classified as "assistant" (not streaming) because it had a new message ID.
// With no open tools at that moment, IsAgentDone() returned true → working→ready flicker.
func TestTranscript_FirstChunkNewMessage_NoFlicker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")

	// Write: user → tool_use → tool_result → first chunk of next message (new ID, no tools)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	enc.Encode(map[string]interface{}{
		"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "build and test",
		},
	})
	enc.Encode(map[string]interface{}{
		"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "name": "Bash", "id": "tu_1"},
			},
		},
	})
	enc.Encode(map[string]interface{}{
		"type": "user", "timestamp": ts(5), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_1", "is_error": false},
			},
		},
	})
	// First chunk of NEW message — stop_reason=nil, new ID, no tool_use yet.
	// This is the critical moment: HasOpenToolCall=false, LastEventType must NOT
	// be "assistant" or IsAgentDone() fires and state flickers to ready.
	enc.Encode(map[string]interface{}{
		"type": "assistant", "timestamp": ts(6), "message": map[string]interface{}{
			"id": "msg_2", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Now let me run the tests."},
			},
		},
	})
	f.Close()

	tail := newCCTailer(path)
	m, err := tail.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	if m.LastEventType != "assistant_streaming" {
		t.Errorf("LastEventType = %q, want assistant_streaming (first chunk of new message must not trigger IsAgentDone)", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false (tool_result resolved tu_1)")
	}
	// Critical: "assistant_streaming" is not matched by IsAgentDone's fallback,
	// so even with HasOpenToolCall=false the session stays working.
}

// Streaming text still extracts AssistantText for UI display.
func TestTranscript_StreamingText_StillExtractsText(t *testing.T) {
	path := writeTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "explain",
		}},
		// Thinking block first (sets msg_1 as current ID)
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "thinking", "thinking": "..."},
			},
		}},
		// Same ID text → streaming
		{"type": "assistant", "timestamp": ts(2), "message": map[string]interface{}{
			"id": "msg_1", "role": "assistant", "stop_reason": nil,
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Let me explain the architecture."},
			},
		}},
	})

	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	// Even though it's streaming, text should be extracted for display
	if m.LastAssistantText != "Let me explain the architecture." {
		t.Errorf("LastAssistantText = %q, want extracted text", m.LastAssistantText)
	}
	if m.LastEventType != "assistant_streaming" {
		t.Errorf("LastEventType = %q, want assistant_streaming", m.LastEventType)
	}
}

// Regression: ExitPlanMode delivered as a split message (text in one JSONL line,
// tool_use in a separate line with the same message ID) must be detected as
// NeedsUserAttention. In Claude Code ≥2.1, deferred tools (ToolSearch) cause
// the model to emit text and tool_use as separate JSONL entries. The text line
// carries stop_reason:"tool_use" but no tool_use block, so it looks like a
// terminal assistant message with IsAgentDone=true. The parser must still
// correctly track the subsequent tool_use line.
//
// Reproduces the pattern from session 8d89e177 (issue #151-worktree).
func TestTranscript_ExitPlanMode_SplitMessage_DetectedAsWaiting(t *testing.T) {
	path := writeTranscript(t, []map[string]interface{}{
		// User prompt.
		{"type": "user", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": "implement the plan",
		}},
		// ToolSearch for ExitPlanMode (deferred tool loading).
		{"type": "assistant", "timestamp": ts(1), "message": map[string]interface{}{
			"id": "msg_ts", "role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "id": "tu_search", "name": "ToolSearch",
					"input": map[string]interface{}{"query": "select:ExitPlanMode"}},
			},
		}},
		// ToolSearch result (user event with tool_result).
		{"type": "user", "timestamp": ts(2), "message": map[string]interface{}{
			"role": "user", "content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_search",
					"content": []interface{}{
						map[string]interface{}{"type": "tool_reference", "tool_name": "ExitPlanMode"},
					}},
			},
		}},
		// Split message part 1: text-only assistant (same msg ID, stop_reason:"tool_use").
		{"type": "assistant", "timestamp": ts(3), "message": map[string]interface{}{
			"id": "msg_plan", "role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Here is my implementation plan."},
			},
		}},
		// Split message part 2: tool_use-only assistant (same msg ID).
		{"type": "assistant", "timestamp": ts(3), "message": map[string]interface{}{
			"id": "msg_plan", "role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "id": "tu_exit", "name": "ExitPlanMode",
					"input": map[string]interface{}{"plan": "# My plan"}},
			},
		}},
		// No tool_result yet — user is reviewing the plan.
	})

	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}

	if !m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=true — ExitPlanMode is open")
	}
	if m.OpenToolCallCount != 1 {
		t.Errorf("expected OpenToolCallCount=1, got %d", m.OpenToolCallCount)
	}
	if len(m.LastOpenToolNames) != 1 || m.LastOpenToolNames[0] != "ExitPlanMode" {
		t.Errorf("expected LastOpenToolNames=[ExitPlanMode], got %v", m.LastOpenToolNames)
	}
	// The event type should be "assistant" (terminal stop_reason).
	if m.LastEventType != "assistant" {
		t.Errorf("LastEventType = %q, want assistant", m.LastEventType)
	}
}

// TestParser_TaskNotification_EmitsSubagentCompletion is the issue #134
// regression: parent user line with origin.kind="task-notification" must
// emit a single SubagentCompletion (parsed task-id, tool-use-id, status),
// be marked Skip=true so it doesn't pollute LastEventType, and must NOT
// set IsUserInterrupt or IsToolDenial.
func TestParser_TaskNotification_EmitsSubagentCompletion(t *testing.T) {
	p := &Parser{}
	xmlPayload := "<task-notification>\n" +
		"<task-id>af7bf8be5a1b511e4</task-id>\n" +
		"<tool-use-id>toolu_01WfKzuNdE9j8zVUsTE7twbF</tool-use-id>\n" +
		"<status>completed</status>\n" +
		"<summary>Agent \"Find TODO comments\" completed</summary>\n" +
		"</task-notification>"

	ev := p.ParseLine(map[string]interface{}{
		"type":      "user",
		"timestamp": "2026-04-11T18:53:50.828Z",
		"origin":    map[string]interface{}{"kind": "task-notification"},
		"message":   map[string]interface{}{"role": "user", "content": xmlPayload},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if !ev.Skip {
		t.Error("task-notification line must be Skip=true so it doesn't feed LastEventType")
	}
	if ev.IsUserInterrupt {
		t.Error("task-notification must not be classified as a user interrupt")
	}
	if ev.IsToolDenial {
		t.Error("task-notification must not be classified as a tool denial")
	}
	if len(ev.SubagentCompletions) != 1 {
		t.Fatalf("expected 1 SubagentCompletion, got %d", len(ev.SubagentCompletions))
	}
	got := ev.SubagentCompletions[0]
	want := tailer.SubagentCompletion{
		AgentID:   "af7bf8be5a1b511e4",
		ToolUseID: "toolu_01WfKzuNdE9j8zVUsTE7twbF",
		Status:    "completed",
	}
	if got != want {
		t.Errorf("SubagentCompletion = %+v, want %+v", got, want)
	}
}

// TestTailer_TaskNotification_SurfacedThroughMetrics is the per-pass surfacing
// test: the fixture from issue #134 must produce a SubagentCompletion in
// SessionMetrics.SubagentCompletions after a single TailAndProcess pass.
func TestTailer_TaskNotification_SurfacedThroughMetrics(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "..", "testdata", "replay", "claudecode",
		"13-full-lifecycle-continue-8a525d27.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("fixture not present at %s: %v", path, err)
	}
	m, err := newCCTailer(path).TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	found := false
	for _, c := range m.SubagentCompletions {
		if c.AgentID == "af7bf8be5a1b511e4" && c.Status == "completed" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected SubagentCompletion for af7bf8be5a1b511e4 in metrics; got %+v", m.SubagentCompletions)
	}
}

// --- Task list tests ---

func makeToolUseEvent(id, name string, input map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"type":      "assistant",
		"timestamp": "2026-04-05T22:00:00Z",
		"message": map[string]interface{}{
			"role":        "assistant",
			"stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{
					"type":  "tool_use",
					"id":    id,
					"name":  name,
					"input": input,
				},
			},
		},
	}
}

func TestParser_TaskCreate_EmitsTaskDelta(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(makeToolUseEvent("tu_1", "TaskCreate", map[string]interface{}{
		"subject":     "Write tests",
		"description": "Add unit tests for the parser",
		"activeForm":  "Writing tests",
	}))
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if len(ev.TaskDeltas) != 1 {
		t.Fatalf("TaskDeltas len = %d, want 1", len(ev.TaskDeltas))
	}
	d := ev.TaskDeltas[0]
	if d.Op != "create" {
		t.Errorf("Op = %q, want create", d.Op)
	}
	if d.Subject != "Write tests" {
		t.Errorf("Subject = %q, want Write tests", d.Subject)
	}
	if d.Description != "Add unit tests for the parser" {
		t.Errorf("Description = %q", d.Description)
	}
	if d.ActiveForm != "Writing tests" {
		t.Errorf("ActiveForm = %q", d.ActiveForm)
	}
	// ID is empty on create — the tailer assigns it sequentially.
	if d.ID != "" {
		t.Errorf("ID should be empty on create, got %q", d.ID)
	}
}

func TestParser_TaskUpdate_EmitsTaskDelta(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(makeToolUseEvent("tu_2", "TaskUpdate", map[string]interface{}{
		"taskId": "1",
		"status": "in_progress",
	}))
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if len(ev.TaskDeltas) != 1 {
		t.Fatalf("TaskDeltas len = %d, want 1", len(ev.TaskDeltas))
	}
	d := ev.TaskDeltas[0]
	if d.Op != "update" {
		t.Errorf("Op = %q, want update", d.Op)
	}
	if d.ID != "1" {
		t.Errorf("ID = %q, want 1", d.ID)
	}
	if d.Status != "in_progress" {
		t.Errorf("Status = %q, want in_progress", d.Status)
	}
}

func TestParser_TaskList_NoTaskDelta(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(makeToolUseEvent("tu_3", "TaskList", map[string]interface{}{}))
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if len(ev.TaskDeltas) != 0 {
		t.Errorf("TaskDeltas len = %d, want 0 for TaskList", len(ev.TaskDeltas))
	}
}

func TestTailer_TaskAccumulation(t *testing.T) {
	// Build a tiny transcript with TaskCreate × 3 then TaskUpdate × 2.
	events := []map[string]interface{}{
		makeToolUseEvent("tu_1", "TaskCreate", map[string]interface{}{
			"subject": "Step one", "activeForm": "Doing step one",
		}),
		makeToolUseEvent("tu_2", "TaskCreate", map[string]interface{}{
			"subject": "Step two", "activeForm": "Doing step two",
		}),
		makeToolUseEvent("tu_3", "TaskCreate", map[string]interface{}{
			"subject": "Step three", "activeForm": "Doing step three",
		}),
		makeToolUseEvent("tu_4", "TaskUpdate", map[string]interface{}{
			"taskId": "1", "status": "completed",
		}),
		makeToolUseEvent("tu_5", "TaskUpdate", map[string]interface{}{
			"taskId": "2", "status": "in_progress",
		}),
	}

	// Write to a temp file.
	dir := t.TempDir()
	path := dir + "/tasks.jsonl"
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()

	tr := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
	m, err := tr.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if len(m.Tasks) != 3 {
		t.Fatalf("Tasks len = %d, want 3", len(m.Tasks))
	}
	if m.Tasks[0].ID != "1" || m.Tasks[0].Subject != "Step one" || m.Tasks[0].Status != "completed" {
		t.Errorf("task 0 = %+v", m.Tasks[0])
	}
	if m.Tasks[1].ID != "2" || m.Tasks[1].Status != "in_progress" {
		t.Errorf("task 1 = %+v", m.Tasks[1])
	}
	if m.Tasks[2].ID != "3" || m.Tasks[2].Status != "pending" {
		t.Errorf("task 2 = %+v", m.Tasks[2])
	}
}
