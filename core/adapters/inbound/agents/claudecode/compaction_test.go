package claudecode

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"irrlicht/core/application/replayengine"
	"irrlicht/core/pkg/tailer"
)

// --- Parser + tailer end-to-end: manual /compact tail (issue #641) ---

func compactionTS(offset int) string {
	return time.Now().Add(time.Duration(offset) * time.Second).Format(time.RFC3339)
}

func appendTranscriptLine(t *testing.T, path string, line map[string]interface{}) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(line); err != nil {
		t.Fatalf("append line: %v", err)
	}
}

// manualCompactTail is the synthetic block Claude Code burst-writes when a
// manual /compact finishes: the compact_boundary (carrying
// compactMetadata.trigger="manual"), the isCompactSummary continuation, the
// isMeta caveat, and the command wrappers. Only the boundary is substantive
// (#656); the rest stay skipped (#641).
func manualCompactTail(trigger string) []map[string]interface{} {
	return []map[string]interface{}{
		{"type": "system", "subtype": "compact_boundary", "content": "Conversation compacted",
			"compactMetadata": map[string]interface{}{"trigger": trigger, "preTokens": 655773},
			"timestamp":       compactionTS(120)},
		{"type": "user", "isCompactSummary": true, "isVisibleInTranscriptOnly": true,
			"timestamp": compactionTS(120), "message": map[string]interface{}{
				"role":    "user",
				"content": "This session is being continued from a previous conversation that ran out of context.",
			}},
		{"type": "user", "isMeta": true, "timestamp": compactionTS(120), "message": map[string]interface{}{
			"role": "user", "content": "<local-command-caveat>Caveat: …</local-command-caveat>",
		}},
		{"type": "user", "timestamp": compactionTS(120), "message": map[string]interface{}{
			"role": "user", "content": "<command-name>/compact</command-name>",
		}},
		{"type": "user", "timestamp": compactionTS(121), "message": map[string]interface{}{
			"role": "user", "content": "<local-command-stdout>Compacted (ctrl+o to see full summary)</local-command-stdout>",
		}},
	}
}

// TestTailer_ManualCompactTail_AfterReadyTurn pins the #641 ready→ready half
// under the new #656 semantics. The prior turn closed cleanly with turn_done.
// A manual /compact's burst-written boundary is now substantive (its
// compactMetadata.trigger=="manual" promotes it to turn_done), so the pass is
// NOT NoSubstantiveActivity — but the observable outcome is unchanged: the
// session still ends at turn_done (IsAgentDone), so ready→ready holds. The
// boundary surfaces as SawManualCompactBoundary so the detector can clear any
// PreCompact hold (#657).
func TestTailer_ManualCompactTail_AfterReadyTurn(t *testing.T) {
	path := writeBgTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": compactionTS(0), "message": map[string]interface{}{
			"role": "user", "content": "do the thing",
		}},
		{"type": "assistant", "timestamp": compactionTS(1), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "end_turn",
			"content": []interface{}{map[string]interface{}{"type": "text", "text": "done."}},
		}},
		{"type": "system", "subtype": "turn_duration", "timestamp": compactionTS(2)},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	if m.LastEventType != "turn_done" {
		t.Fatalf("pass 1 LastEventType = %q, want turn_done", m.LastEventType)
	}

	for _, ln := range manualCompactTail("manual") {
		appendTranscriptLine(t, path, ln)
	}

	m, err = tl.TailAndProcess()
	if err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if m.NoSubstantiveActivity {
		t.Errorf("pass 2 NoSubstantiveActivity = true, want false (the manual compact_boundary is substantive)")
	}
	if !m.SawManualCompactBoundary {
		t.Errorf("pass 2 SawManualCompactBoundary = false, want true")
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("pass 2 LastEventType = %q, want turn_done", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Errorf("pass 2 HasOpenToolCall = true, want false (ready→ready preserved)")
	}
}

// TestTailer_ManualCompactTail_AfterMidToolUse is the #656 primary case. The
// prior turn was stranded mid tool-use with a still-open tool call (assistant
// tool_use, no tool_result, no turn_done) — pre-fix HasOpenToolCall pinned the
// session in working forever. The manual compact_boundary now promotes to
// turn_done, sweeping the lingering open call. LastEventType=="turn_done" with
// HasOpenToolCall==false is exactly what session.IsAgentDone() reads as done,
// so the session releases working→ready.
func TestTailer_ManualCompactTail_AfterMidToolUse(t *testing.T) {
	path := writeBgTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": compactionTS(0), "message": map[string]interface{}{
			"role": "user", "content": "do the thing",
		}},
		bashToolUse("tu_mid", "Bash", map[string]interface{}{"command": "echo hi"}),
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	if !m.HasOpenToolCall {
		t.Fatalf("pass 1 HasOpenToolCall = false, want true (open tool call, the stuck-working condition)")
	}
	if m.LastEventType == "turn_done" {
		t.Fatalf("pass 1 LastEventType = turn_done, want a mid-turn type (no turn_done yet)")
	}

	for _, ln := range manualCompactTail("manual") {
		appendTranscriptLine(t, path, ln)
	}

	m, err = tl.TailAndProcess()
	if err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if m.NoSubstantiveActivity {
		t.Errorf("pass 2 NoSubstantiveActivity = true, want false")
	}
	if !m.SawManualCompactBoundary {
		t.Errorf("pass 2 SawManualCompactBoundary = false, want true")
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("pass 2 LastEventType = %q, want turn_done", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Errorf("pass 2 HasOpenToolCall = true, want false (turn_done swept the open call → working→ready, #656)")
	}
}

// TestTailer_AutoCompactTail_StaysSkipped guards the gate: an auto-compaction
// boundary (compactMetadata.trigger="auto") must stay skipped — it fires
// mid-turn and the agent continues, so promoting it would emit a spurious
// ready-blip. The prior mid-tool-use turn must remain not-done.
func TestTailer_AutoCompactTail_StaysSkipped(t *testing.T) {
	path := writeBgTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": compactionTS(0), "message": map[string]interface{}{
			"role": "user", "content": "do the thing",
		}},
		bashToolUse("tu_auto", "Bash", map[string]interface{}{"command": "echo hi"}),
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("pass 1: %v", err)
	}

	for _, ln := range manualCompactTail("auto") {
		appendTranscriptLine(t, path, ln)
	}

	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if !m.NoSubstantiveActivity {
		t.Errorf("pass 2 NoSubstantiveActivity = false, want true (auto boundary must stay skipped)")
	}
	if m.SawManualCompactBoundary {
		t.Errorf("pass 2 SawManualCompactBoundary = true, want false (auto, not manual)")
	}
	if m.LastEventType == "turn_done" {
		t.Errorf("pass 2 LastEventType = turn_done, want unchanged (auto boundary not promoted)")
	}
	if !m.HasOpenToolCall {
		t.Errorf("pass 2 HasOpenToolCall = false, want true (auto compaction must not sweep the open call)")
	}
}

// TestTailer_ManualCompact_Issue656Regression replays the exact tail of the
// live session that surfaced #656 (fa5751bf, Claude Code 2.1.168, manual
// /compact). The last real turn ended mid tool-use — assistant tool_use →
// tool_result, NO turn_done — so LastEventType was pinned at the tool_result
// user event and the session was stranded in "working" forever. Faithful to the
// report, the compaction block is burst-written with NON-monotonic, back-dated
// timestamps (the /compact invocation predates the tool turn), and a trailing
// system/away_summary recap proves the session went idle and still never
// released. The regression bar: after the manual compact_boundary is parsed,
// the session classifies as agent-done (→ ready), not stuck working.
func TestTailer_ManualCompact_Issue656Regression(t *testing.T) {
	// ts builds an absolute RFC3339 timestamp at a fixed wall-clock so the
	// back-dating is literal, not relative to time.Now().
	ts := func(h, m, s int) string {
		return time.Date(2026, 6, 10, h, m, s, 0, time.UTC).Format(time.RFC3339)
	}

	path := writeBgTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": ts(19, 57, 30), "message": map[string]interface{}{
			"role": "user", "content": "run the build",
		}},
		// Last real assistant turn: a tool_use (ts 19:57:42) ...
		{"type": "assistant", "timestamp": ts(19, 57, 42), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "id": "tu_656", "name": "Bash",
					"input": map[string]interface{}{"command": "go build ./..."}},
			},
		}},
		// ... whose tool_result (ts 19:57:48) is the last NON-skipped event
		// before compaction — a "user" event, NO turn_done. This is the pin
		// that stranded the session in working.
		{"type": "user", "timestamp": ts(19, 57, 48), "message": map[string]interface{}{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": "tu_656", "content": "ok"},
			},
		}},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	// Pre-compaction: the bug condition — not done, pinned at the tool_result.
	if replayengine.TailerToDomain(m).IsAgentDone() {
		t.Fatalf("pass 1 IsAgentDone = true, want false (mid tool-use, no turn_done — the stuck-working condition)")
	}

	// The manual /compact burst, back-dated: the compact_boundary and the
	// /compact invocation carry timestamps BEFORE the 19:57 tool turn (file
	// order is what the tailer honours, not these timestamps).
	for _, ln := range []map[string]interface{}{
		{"type": "system", "subtype": "compact_boundary",
			"compactMetadata": map[string]interface{}{"trigger": "manual", "preTokens": 655773},
			"timestamp":       ts(17, 21, 23)},
		{"type": "user", "isCompactSummary": true, "isVisibleInTranscriptOnly": true,
			"timestamp": ts(17, 21, 23), "message": map[string]interface{}{
				"role": "user", "content": "This session is being continued from a previous conversation …",
			}},
		{"type": "user", "isMeta": true, "timestamp": ts(17, 18, 42), "message": map[string]interface{}{
			"role": "user", "content": "<local-command-caveat>Caveat: …</local-command-caveat>",
		}},
		{"type": "user", "timestamp": ts(17, 18, 42), "message": map[string]interface{}{
			"role": "user", "content": "<command-name>/compact</command-name>",
		}},
		{"type": "user", "timestamp": ts(17, 18, 43), "message": map[string]interface{}{
			"role": "user", "content": "<local-command-stdout>Compacted (ctrl+o to see full summary)</local-command-stdout>",
		}},
		// away_summary recap proves the session went idle after compaction; it
		// must not bounce the classification back to working.
		{"type": "system", "subtype": "away_summary", "timestamp": ts(17, 39, 44),
			"content": "Session idle recap …"},
	} {
		appendTranscriptLine(t, path, ln)
	}

	m, err = tl.TailAndProcess()
	if err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if !m.SawManualCompactBoundary {
		t.Errorf("pass 2 SawManualCompactBoundary = false, want true")
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("pass 2 LastEventType = %q, want turn_done (manual compact_boundary ends the turn)", m.LastEventType)
	}
	if m.HasOpenToolCall {
		t.Errorf("pass 2 HasOpenToolCall = true, want false")
	}
	// The regression assertion: the session is no longer stranded in working.
	if !replayengine.TailerToDomain(m).IsAgentDone() {
		t.Errorf("pass 2 IsAgentDone = false, want true — session must release working→ready after manual /compact (#656)")
	}
}

// --- away_summary content extraction (issue #979) ---

// TestTailer_AwaySummary_ExtractedAsPassiveUpgrade pins that the recap's
// content is read onto SessionMetrics.AwaySummary even though the event
// itself still stays skipped (NoSubstantiveActivity unchanged, #329).
func TestTailer_AwaySummary_ExtractedAsPassiveUpgrade(t *testing.T) {
	path := writeBgTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": compactionTS(0), "message": map[string]interface{}{
			"role": "user", "content": "do the thing",
		}},
		{"type": "assistant", "timestamp": compactionTS(1), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "end_turn",
			"content": []interface{}{map[string]interface{}{"type": "text", "text": "done."}},
		}},
		{"type": "system", "subtype": "turn_duration", "timestamp": compactionTS(2)},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("pass 1: %v", err)
	}

	appendTranscriptLine(t, path, map[string]interface{}{
		"type": "system", "subtype": "away_summary", "timestamp": compactionTS(180),
		"content": "Goal was X. Done: Y. Next: decide whether to merge now or wait for review.",
	})

	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if !m.NoSubstantiveActivity {
		t.Errorf("pass 2 NoSubstantiveActivity = false, want true (away_summary must stay skipped)")
	}
	if m.AwaySummary == nil {
		t.Fatalf("pass 2 AwaySummary = nil, want the recap content extracted")
	}
	want := "Goal was X. Done: Y. Next: decide whether to merge now or wait for review."
	if m.AwaySummary.Text != want {
		t.Errorf("pass 2 AwaySummary.Text = %q, want %q", m.AwaySummary.Text, want)
	}
}

// TestTailer_AwaySummary_ClearedOnNewUserMessage pins that a real new user
// message resets AwaySummary, mirroring TaskQuestion's answered-question
// reset — a stale recap must not leak into the next turn.
func TestTailer_AwaySummary_ClearedOnNewUserMessage(t *testing.T) {
	path := writeBgTranscript(t, []map[string]interface{}{
		{"type": "user", "timestamp": compactionTS(0), "message": map[string]interface{}{
			"role": "user", "content": "do the thing",
		}},
		{"type": "assistant", "timestamp": compactionTS(1), "message": map[string]interface{}{
			"role": "assistant", "stop_reason": "end_turn",
			"content": []interface{}{map[string]interface{}{"type": "text", "text": "done."}},
		}},
		{"type": "system", "subtype": "turn_duration", "timestamp": compactionTS(2)},
		{"type": "system", "subtype": "away_summary", "timestamp": compactionTS(180),
			"content": "Goal was X. Done: Y. Next: Z."},
	})

	tl := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	if m.AwaySummary == nil {
		t.Fatalf("pass 1 AwaySummary = nil, want the recap content extracted")
	}

	appendTranscriptLine(t, path, map[string]interface{}{
		"type": "user", "timestamp": compactionTS(181), "message": map[string]interface{}{
			"role": "user", "content": "let's merge it",
		},
	})

	m, err = tl.TailAndProcess()
	if err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if m.AwaySummary != nil {
		t.Errorf("pass 2 AwaySummary = %+v, want nil (cleared by the new user message)", m.AwaySummary)
	}
}
