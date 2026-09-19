package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// extractPiAssistantText keeps the trailing 200 runes with a leading ellipsis
// (the shared tailer.TruncateAssistantText rule). Tail, not head: the latest
// words — including a trailing "?" that waiting-state detection reads — survive.
func TestExtractPiAssistantText_TailTruncated(t *testing.T) {
	piMsg := map[string]interface{}{
		"content": []interface{}{
			map[string]interface{}{"type": "text", "text": strings.Repeat("x", 300) + "?"},
		},
	}
	got := extractPiAssistantText(piMsg)
	if n := len([]rune(got)); n != 201 {
		t.Errorf("rune count = %d, want 201 (… + 200 runes)", n)
	}
	if !strings.HasPrefix(got, "…") {
		t.Errorf("got %q, want leading … (tail truncation)", got)
	}
	if !strings.HasSuffix(got, "?") {
		t.Error("trailing question mark dropped — head truncated instead of tail")
	}
}

// --- Task-estimate marker (issue #558) ---

func TestParser_TaskEstimate_FromAssistantText(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"timestamp": ts(1),
		"message": map[string]interface{}{
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": `On it. <!-- {"marker":"irrlicht-eta","total_rounds":8,"completed_rounds":3} -->`},
			},
		},
	})
	if ev.TaskEstimate == nil {
		t.Fatal("expected TaskEstimate on assistant message")
	}
	if ev.TaskEstimate.TotalRounds != 8 || ev.TaskEstimate.CompletedRounds != 3 {
		t.Errorf("rounds = %d/%d, want 3/8", ev.TaskEstimate.CompletedRounds, ev.TaskEstimate.TotalRounds)
	}
}

// Marker early in a long message must survive — extractPiAssistantText keeps
// only the last text block, tail-truncated to 200 runes.
func TestParser_TaskEstimate_SurvivesLongMessage(t *testing.T) {
	p := &Parser{}
	long := `<!-- {"marker":"irrlicht-eta","total_rounds":5,"completed_rounds":2} --> `
	for i := 0; i < 50; i++ {
		long += "filler prose "
	}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"timestamp": ts(1),
		"message": map[string]interface{}{
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": long},
			},
		},
	})
	if ev.TaskEstimate == nil || ev.TaskEstimate.CompletedRounds != 2 {
		t.Fatalf("TaskEstimate = %+v, want 2/5 despite display truncation", ev.TaskEstimate)
	}
}

func TestParser_TaskEstimate_UserMessageIgnored(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":      "message",
		"timestamp": ts(0),
		"message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": `<!-- {"marker":"irrlicht-eta","total_rounds":5,"completed_rounds":1} -->`},
			},
		},
	})
	if ev.TaskEstimate != nil {
		t.Fatalf("user-pasted marker must not feed the estimate, got %+v", ev.TaskEstimate)
	}
}

// A healthy end-of-turn must carry no session error. LOCK, in this file
// specifically because CodeScene notes parser.go is usually changed together
// with it — the hint that surfaced a real defect in #1809. The errored
// counterpart lives in session_error_test.go.
func TestParser_NormalStopHasNoSessionError(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type": "message",
		"message": map[string]interface{}{
			"role": "assistant", "stopReason": "stop",
			"content": []interface{}{map[string]interface{}{"type": "text", "text": "done"}},
		},
	})
	if ev == nil || ev.EventType != "turn_done" {
		t.Fatalf("precondition: stopReason \"stop\" must be turn_done, got %+v", ev)
	}
	if ev.SessionError != nil {
		t.Errorf("a clean turn must carry no session error, got %+v", ev.SessionError)
	}
}

// TestParser_ModelChange_RetainsProvider is issue #2005's red-first defect
// test: parses the committed regression fixture's first model_change line
// (replaydata/agents/pi/regressions/model-switch), which carries
// "provider":"openai-codex" alongside modelId, and asserts the parsed event
// retains it as route evidence. On main (before this ticket's fix), the
// parser never reads raw["provider"] at all, so ev.RateLimit stays nil and
// this assertion fails.
func TestParser_ModelChange_RetainsProvider(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..",
		"replaydata", "agents", "pi", "regressions", "model-switch",
		"recordings", "2026-05-25-04-09-10_irrlichd-0.4.7+2a46388", "transcript.jsonl"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var raw map[string]interface{}
	var found bool
	for _, line := range lines {
		var candidate map[string]interface{}
		if err := json.Unmarshal([]byte(line), &candidate); err != nil {
			t.Fatalf("unmarshal fixture line: %v", err)
		}
		if candidate["type"] == "model_change" {
			raw = candidate
			found = true
			break
		}
	}
	if !found {
		t.Fatal("fixture has no model_change line — fixture assumption broken")
	}
	if raw["provider"] != "openai-codex" {
		t.Fatalf("fixture's model_change provider = %v, want openai-codex — fixture assumption broken", raw["provider"])
	}

	p := &Parser{}
	ev := p.ParseLine(raw)

	if ev.RateLimit == nil {
		t.Fatal("expected ev.RateLimit to carry the transcript's provider evidence, got nil")
	}
	if ev.RateLimit.Provider != tailer.ProviderOpenAI {
		t.Errorf("RateLimit.Provider = %q, want %q", ev.RateLimit.Provider, tailer.ProviderOpenAI)
	}
	if ev.RateLimit.AttributionQuality != tailer.AttributionQualityConfirmed {
		t.Errorf("RateLimit.AttributionQuality = %q, want %q", ev.RateLimit.AttributionQuality, tailer.AttributionQualityConfirmed)
	}
}

// TestParser_ModelChange_NoProviderKeepsModel is issue #2005 §6 row 2: a
// model_change with modelId and no provider retains the model but leaves
// the provider unknown (ev.RateLimit stays nil, not a guessed value).
func TestParser_ModelChange_NoProviderKeepsModel(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":    "model_change",
		"modelId": "gpt-5.3-codex",
	})
	if ev.ModelName != "gpt-5.3-codex" {
		t.Errorf("ModelName = %q, want gpt-5.3-codex", ev.ModelName)
	}
	if ev.RateLimit != nil {
		t.Errorf("expected no RateLimit when provider is absent, got %+v", ev.RateLimit)
	}
	if state, _ := classifyPiProviderRoute(map[string]interface{}{"modelId": "x"}); state != piProviderRouteAbsent {
		t.Errorf("classifyPiProviderRoute state = %q, want absent", state)
	}
}

// TestParser_ModelChange_EmptyProviderIsUnknownNotConfirmed is issue #2005
// §6 row 3: an empty "provider" string must resolve to unknown, never to an
// empty-but-confirmed value.
func TestParser_ModelChange_EmptyProviderIsUnknownNotConfirmed(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":     "model_change",
		"modelId":  "gpt-5.3-codex",
		"provider": "",
	})
	if ev.RateLimit != nil {
		t.Errorf("expected no RateLimit for an empty provider string, got %+v", ev.RateLimit)
	}
	if state, _ := classifyPiProviderRoute(map[string]interface{}{"provider": ""}); state != piProviderRouteEmpty {
		t.Errorf("classifyPiProviderRoute state = %q, want empty", state)
	}
}

// TestParser_ModelChange_MalformedProviderDistinctFromAbsent is issue #2005
// §6 row 4: a non-string "provider" value is malformed, and the classifier
// reports a state distinct from "absent" even though both leave ev.RateLimit
// nil — the witness the design's "distinct from absent" wording requires.
func TestParser_ModelChange_MalformedProviderDistinctFromAbsent(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":     "model_change",
		"modelId":  "gpt-5.3-codex",
		"provider": float64(42),
	})
	if ev.RateLimit != nil {
		t.Errorf("expected no RateLimit for a non-string provider, got %+v", ev.RateLimit)
	}
	malformedState, _ := classifyPiProviderRoute(map[string]interface{}{"provider": float64(42)})
	absentState, _ := classifyPiProviderRoute(map[string]interface{}{})
	if malformedState != piProviderRouteMalformed {
		t.Errorf("classifyPiProviderRoute state = %q, want malformed", malformedState)
	}
	if malformedState == absentState {
		t.Error("malformed and absent must classify distinctly")
	}
}

// TestParser_ModelChange_UnmappedRouteStaysUnknown covers an unrecognized Pi
// route name: no literal table entry means no guess by string match (issue
// #2005 §9 question 2).
func TestParser_ModelChange_UnmappedRouteStaysUnknown(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]interface{}{
		"type":     "model_change",
		"modelId":  "some-future-model",
		"provider": "some-future-route",
	})
	if ev.RateLimit != nil {
		t.Errorf("expected no RateLimit for an unmapped route, got %+v", ev.RateLimit)
	}
	if state, _ := classifyPiProviderRoute(map[string]interface{}{"provider": "some-future-route"}); state != piProviderRouteUnmapped {
		t.Errorf("classifyPiProviderRoute state = %q, want unmapped", state)
	}
}

// TestParser_ModelChange_LaterProviderDoesNotRelabelEarlierUsage is issue
// #2005 §6 row 5, exercised through the real tailer: two model_change events
// naming different providers within the same transcript. The tailer's
// metrics reflect the LATER provider for subsequent attribution
// (ingestRateLimit in tailer_metrics.go replaces the whole snapshot on every
// model_change, exactly as it already does for codex's per-event
// rate_limits). Earlier per-turn cost rows are never rewritten —
// cost_tracker.go's RecordSnapshot only ever appends (os.O_APPEND, see
// TestRecordSnapshot_AppendsOnChange in
// core/adapters/outbound/filesystem/cost_tracker_test.go) — so a row already
// written under the earlier provider is untouched by this replacement; that
// half is a lock on existing behavior, not new code from this ticket.
func TestParser_ModelChange_LaterProviderDoesNotRelabelEarlierUsage(t *testing.T) {
	t0 := ts(0)
	t1 := ts(1)
	t2 := ts(2)
	t3 := ts(3)
	path := writeLines(t, []map[string]interface{}{
		{"type": "session", "version": float64(3), "cwd": "/tmp"},
		{"type": "model_change", "timestamp": t0, "provider": "openai-codex", "modelId": "gpt-5.4-mini"},
		{"type": "message", "timestamp": t1, "message": map[string]interface{}{
			"role": "user", "content": []interface{}{
				map[string]interface{}{"type": "text", "text": "go"},
			},
		}},
		{"type": "message", "timestamp": t2, "message": map[string]interface{}{
			"role": "assistant", "stopReason": "stop", "model": "gpt-5.4-mini",
			"usage": map[string]interface{}{"input": float64(100), "output": float64(20), "cost": float64(0.001)},
		}},
		// A second model_change on the same session — same route today
		// (only one table entry exists), but exercises the "later wins"
		// replacement semantics of ingestRateLimit regardless.
		{"type": "model_change", "timestamp": t3, "provider": "openai-codex", "modelId": "gpt-5.5"},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "pi")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.RateLimit == nil {
		t.Fatal("expected a RateLimit snapshot after a mapped model_change")
	}
	if m.RateLimit.Provider != tailer.ProviderOpenAI {
		t.Errorf("RateLimit.Provider = %q, want %q", m.RateLimit.Provider, tailer.ProviderOpenAI)
	}
	// SampledAt must track the LATEST model_change's own timestamp, not the
	// first — the concrete, assertable form of "later wins for subsequent
	// attribution".
	wantSampledAt := mustParseRFC3339(t, t3).Unix()
	if m.RateLimit.SampledAt != wantSampledAt {
		t.Errorf("RateLimit.SampledAt = %d, want %d (the later model_change's timestamp)", m.RateLimit.SampledAt, wantSampledAt)
	}
}

func mustParseRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse timestamp %q: %v", s, err)
	}
	return parsed
}
