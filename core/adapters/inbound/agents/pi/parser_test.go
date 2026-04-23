package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

func ts(offset int) string {
	return time.Now().Add(time.Duration(offset) * time.Second).Format(time.RFC3339)
}

func writeLines(t *testing.T, lines []map[string]interface{}) string {
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

func TestParser_SessionHeader_SkipWithCWD(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":    "session",
		"version": float64(3),
		"id":      "abc-123",
		"cwd":     "/Users/test/project",
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if !ev.Skip {
		t.Error("expected session header to be skipped")
	}
	if ev.CWD != "/Users/test/project" {
		t.Errorf("CWD = %q, want /Users/test/project", ev.CWD)
	}
}

func TestParser_ModelChange_SkipWithModel(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":    "model_change",
		"modelId": "gpt-5.3-codex",
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if !ev.Skip {
		t.Error("expected model_change to be skipped")
	}
	if ev.ModelName != "gpt-5.3-codex" {
		t.Errorf("ModelName = %q, want gpt-5.3-codex", ev.ModelName)
	}
}

func TestParser_ThinkingLevelChange_Skip(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":          "thinking_level_change",
		"thinkingLevel": "off",
	})
	if ev == nil || !ev.Skip {
		t.Error("expected thinking_level_change to be skipped")
	}
}

func TestParser_Compaction_IsActivityEvent(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "compaction",
		"timestamp": ts(0),
		"summary":   "checkpoint",
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.Skip {
		t.Fatal("expected compaction not to be skipped")
	}
	if ev.EventType != "assistant" {
		t.Errorf("EventType = %q, want assistant", ev.EventType)
	}
}

func TestParser_BashExecution_Skip(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"timestamp": ts(0),
		"message": map[string]interface{}{
			"role": "bashExecution",
		},
	})
	if ev == nil || !ev.Skip {
		t.Error("expected bashExecution to be skipped")
	}
}

func TestParser_AssistantEndOfTurn(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"timestamp": ts(0),
		"message": map[string]interface{}{
			"role":       "assistant",
			"stopReason": "stop",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Done!"},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want turn_done (end-of-turn)", ev.EventType)
	}
	if ev.AssistantText != "Done!" {
		t.Errorf("AssistantText = %q, want Done!", ev.AssistantText)
	}
}

func TestParser_AssistantMidTurn(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"timestamp": ts(0),
		"message": map[string]interface{}{
			"role":       "assistant",
			"stopReason": "toolUse",
			"content": []interface{}{
				map[string]interface{}{"type": "toolCall", "id": "call_1", "name": "bash",
					"arguments": map[string]interface{}{"command": "ls"}},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "assistant" {
		t.Errorf("EventType = %q, want assistant (mid-turn)", ev.EventType)
	}
	if len(ev.ToolUses) != 1 || ev.ToolUses[0].Name != "bash" || ev.ToolUses[0].ID != "call_1" {
		t.Errorf("ToolUses = %v, want [{call_1 bash}]", ev.ToolUses)
	}
}

func TestParser_ToolResult_SingleCount(t *testing.T) {
	// This is the Bug 1 regression test: toolResult should produce exactly
	// one ToolResultID (in the parser), not also in addMessageEvent.
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"timestamp": ts(0),
		"message": map[string]interface{}{
			"role":       "toolResult",
			"toolCallId": "call_1",
			"toolName":   "bash",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "file1\nfile2\n"},
			},
			"isError": false,
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "tool_result" {
		t.Errorf("EventType = %q, want tool_result", ev.EventType)
	}
	if len(ev.ToolResultIDs) != 1 || ev.ToolResultIDs[0] != "call_1" {
		t.Errorf("ToolResultIDs = %v, want [call_1]", ev.ToolResultIDs)
	}
}

func TestParser_UserMessage(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"timestamp": ts(0),
		"message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "hello"},
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.EventType != "user_message" {
		t.Errorf("EventType = %q, want user_message", ev.EventType)
	}
	if !ev.ClearToolNames {
		t.Error("expected ClearToolNames=true for user message")
	}
}

func TestParser_FullTranscript_EndDetection(t *testing.T) {
	// Simulate the real Pi transcript from the bug report.
	path := writeLines(t, []map[string]interface{}{
		{"type": "session", "version": float64(3), "id": "abc-123",
			"timestamp": ts(0), "cwd": "/Users/test/project"},
		{"type": "model_change", "id": "m1", "timestamp": ts(0),
			"provider": "openai-codex", "modelId": "gpt-5.3-codex"},
		{"type": "thinking_level_change", "id": "t1", "timestamp": ts(0),
			"thinkingLevel": "off"},
		{"type": "message", "id": "u1", "timestamp": ts(1),
			"message": map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "ls"},
				},
			}},
		{"type": "message", "id": "a1", "timestamp": ts(2),
			"message": map[string]interface{}{
				"role":       "assistant",
				"stopReason": "toolUse",
				"content": []interface{}{
					map[string]interface{}{"type": "toolCall", "id": "call_1", "name": "bash",
						"arguments": map[string]interface{}{"command": "ls"}},
				},
			}},
		{"type": "message", "id": "tr1", "timestamp": ts(3),
			"message": map[string]interface{}{
				"role":       "toolResult",
				"toolCallId": "call_1",
				"toolName":   "bash",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "hello.py\n"},
				},
				"isError": false,
			}},
		{"type": "message", "id": "a2", "timestamp": ts(4),
			"message": map[string]interface{}{
				"role":       "assistant",
				"stopReason": "stop",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "hello.py"},
				},
			}},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "pi")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("LastEventType = %q, want turn_done", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false after all tool calls resolved")
	}
	if m.OpenToolCallCount != 0 {
		t.Errorf("OpenToolCallCount = %d, want 0", m.OpenToolCallCount)
	}
}

func TestParser_TextOnlyTurn_TriggersIsAgentDone(t *testing.T) {
	// Regression for #119: a text-only pi turn (stopReason:stop) must
	// drive IsAgentDone() to true so the classifier transitions to ready.
	// Previously, the parser emitted "assistant_message" which IsAgentDone()
	// deliberately excludes (to avoid codex preliminary-message flicker), so
	// pi sessions stayed pinned at working after every text-only reply.
	path := writeLines(t, []map[string]interface{}{
		{"type": "session", "version": float64(3), "cwd": "/tmp"},
		{"type": "message", "timestamp": ts(0),
			"message": map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "hi"},
				}}},
		{"type": "message", "timestamp": ts(1),
			"message": map[string]interface{}{
				"role":       "assistant",
				"stopReason": "stop",
				"content": []interface{}{
					map[string]interface{}{"type": "text",
						"text": "Hi! What would you like to work on?"},
				}}},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "pi")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("LastEventType = %q, want turn_done", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Error("expected HasOpenToolCall=false for text-only turn")
	}
	// Cross-check that the domain's IsAgentDone() — which is what the
	// classifier ultimately consults — accepts the parser output. The
	// tailer and domain each have their own SessionMetrics struct; the
	// classifier pipes LastEventType/HasOpenToolCall from one to the other.
	dm := &session.SessionMetrics{
		LastEventType:   m.LastEventType,
		HasOpenToolCall: m.HasOpenToolCall,
	}
	if !dm.IsAgentDone() {
		t.Error("session.IsAgentDone() = false, want true (session should be ready after text-only turn)")
	}
}

func TestParser_Compaction_SetsLastEventToAssistant(t *testing.T) {
	path := writeLines(t, []map[string]interface{}{
		{"type": "session", "version": float64(3), "cwd": "/tmp"},
		{"type": "compaction", "timestamp": ts(0), "summary": "checkpoint"},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "pi")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "assistant" {
		t.Errorf("LastEventType = %q, want assistant (compaction should count as activity)", m.LastEventType)
	}
}

func TestParser_Contribution_TokenFields(t *testing.T) {
	// Pi uses short field names: input, output, cacheRead, cacheWrite.
	// All four must map to the correct UsageBreakdown buckets.
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"timestamp": ts(0),
		"message": map[string]interface{}{
			"role":       "assistant",
			"stopReason": "stop",
			"model":      "claude-sonnet-4-5",
			"usage": map[string]interface{}{
				"input":      float64(1000),
				"output":     float64(200),
				"cacheRead":  float64(300),
				"cacheWrite": float64(50),
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.Contribution == nil {
		t.Fatal("expected Contribution to be set")
	}
	c := ev.Contribution
	if c.Usage.Input != 1000 {
		t.Errorf("Input = %d, want 1000", c.Usage.Input)
	}
	if c.Usage.Output != 200 {
		t.Errorf("Output = %d, want 200", c.Usage.Output)
	}
	if c.Usage.CacheRead != 300 {
		t.Errorf("CacheRead = %d, want 300", c.Usage.CacheRead)
	}
	if c.Usage.CacheCreation5m != 50 {
		t.Errorf("CacheCreation5m = %d, want 50 (mapped from cacheWrite)", c.Usage.CacheCreation5m)
	}
	if c.ProviderCostUSD != nil {
		t.Errorf("ProviderCostUSD = %v, want nil (no cost field present)", c.ProviderCostUSD)
	}
}

func TestParser_Contribution_ProviderCostWins(t *testing.T) {
	// When usage.cost is present, ProviderCostUSD is set and takes precedence
	// over token-based pricing in the tailer.
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"timestamp": ts(0),
		"message": map[string]interface{}{
			"role":       "assistant",
			"stopReason": "stop",
			"model":      "claude-sonnet-4-5",
			"usage": map[string]interface{}{
				"input":  float64(500),
				"output": float64(100),
				"cost":   float64(0.00123),
			},
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.Contribution == nil {
		t.Fatal("expected Contribution to be set")
	}
	c := ev.Contribution
	if c.ProviderCostUSD == nil {
		t.Fatal("expected ProviderCostUSD to be set when cost field present")
	}
	if *c.ProviderCostUSD != 0.00123 {
		t.Errorf("ProviderCostUSD = %v, want 0.00123", *c.ProviderCostUSD)
	}
	// Token fields still populated (tailer uses them as fallback when cost is nil).
	if c.Usage.Input != 500 {
		t.Errorf("Input = %d, want 500", c.Usage.Input)
	}
}

func TestParser_Contribution_MidTurnNoContribution(t *testing.T) {
	// Mid-turn assistant events (stopReason != "stop") have no usage block in
	// typical Pi transcripts. Contribution should be nil in that case.
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"timestamp": ts(0),
		"message": map[string]interface{}{
			"role":       "assistant",
			"stopReason": "toolUse",
		},
	})
	if ev == nil {
		t.Fatal("expected non-nil event")
	}
	if ev.Contribution != nil {
		t.Errorf("expected no Contribution for mid-turn event with no usage, got %+v", ev.Contribution)
	}
}

func TestParser_Contribution_AccumulatesViaTailer(t *testing.T) {
	// Full transcript: two assistant end-of-turns, each with usage.
	// The tailer must accumulate both contributions into EstimatedCostUSD.
	path := writeLines(t, []map[string]interface{}{
		{"type": "session", "version": float64(3), "cwd": "/tmp"},
		{"type": "message", "timestamp": ts(0), "message": map[string]interface{}{
			"role": "user", "content": []interface{}{
				map[string]interface{}{"type": "text", "text": "go"},
			},
		}},
		{"type": "message", "timestamp": ts(1), "message": map[string]interface{}{
			"role":       "assistant",
			"stopReason": "stop",
			"model":      "claude-sonnet-4-5",
			"usage":      map[string]interface{}{"input": float64(1000), "output": float64(200), "cost": float64(0.005)},
		}},
		{"type": "message", "timestamp": ts(2), "message": map[string]interface{}{
			"role": "user", "content": []interface{}{
				map[string]interface{}{"type": "text", "text": "more"},
			},
		}},
		{"type": "message", "timestamp": ts(3), "message": map[string]interface{}{
			"role":       "assistant",
			"stopReason": "stop",
			"model":      "claude-sonnet-4-5",
			"usage":      map[string]interface{}{"input": float64(1200), "output": float64(300), "cost": float64(0.007)},
		}},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "pi")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	// Two provider-reported costs should accumulate: 0.005 + 0.007 = 0.012.
	const wantCost = 0.012
	const epsilon = 1e-9
	if m.EstimatedCostUSD < wantCost-epsilon || m.EstimatedCostUSD > wantCost+epsilon {
		t.Errorf("EstimatedCostUSD = %v, want %v (sum of two provider costs)", m.EstimatedCostUSD, wantCost)
	}
}

func TestParser_BashExecutionSkipped_PreservesLastEvent(t *testing.T) {
	// After an assistant end-of-turn, a bashExecution event should be skipped
	// and not change LastEventType.
	path := writeLines(t, []map[string]interface{}{
		{"type": "session", "version": float64(3), "cwd": "/tmp"},
		{"type": "message", "timestamp": ts(0), "message": map[string]interface{}{
			"role":       "assistant",
			"stopReason": "stop",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "Hi!"},
			},
		}},
		{"type": "message", "timestamp": ts(1), "message": map[string]interface{}{
			"role": "bashExecution",
		}},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "pi")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("LastEventType = %q, want turn_done (bashExecution should be skipped)", m.LastEventType)
	}
}
