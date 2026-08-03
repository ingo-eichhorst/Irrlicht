package copilot

import (
	"strings"
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// TestShellEscapeDoesNotStickWorking pins the defect found while assessing the
// shell-escape-command cell.
//
// Copilot's `!` escape runs a command LOCALLY with no model turn — it never
// reaches the provider, and the CLI's own credit counter stays at zero. But it
// still appends tool.user_requested + tool.execution_complete{isUserRequested:true}
// to events.jsonl, and crucially emits NO assistant.turn_end.
//
// Mapping that completion to function_call_output bounces a settled session
// ready → working with no turn-done to ever close it, so the session sticks in
// working for the rest of its life. mistral-vibe hit the identical shape with
// its own `!` escape and solved it by skipping the injected record.
func TestShellEscapeDoesNotStickWorking(t *testing.T) {
	lines := `{"type":"session.start","timestamp":"2026-08-03T09:00:00.000Z","data":{"context":{"cwd":"/tmp/p"}}}
{"type":"user.message","timestamp":"2026-08-03T09:00:01.000Z","data":{"content":"hello"}}
{"type":"assistant.message","timestamp":"2026-08-03T09:00:02.000Z","data":{"model":"gpt-5-mini","content":"hi","outputTokens":5}}
{"type":"assistant.turn_end","timestamp":"2026-08-03T09:00:03.000Z","data":{"turnId":"0"}}
{"type":"tool.user_requested","timestamp":"2026-08-03T09:00:10.000Z","data":{"toolCallId":"call_esc","toolName":"bash","isUserRequested":true}}
{"type":"tool.execution_complete","timestamp":"2026-08-03T09:00:11.000Z","data":{"toolCallId":"call_esc","success":true,"isUserRequested":true}}
`
	tl := tailer.NewTranscriptTailer(writeTranscript(t, lines), &Parser{}, AdapterName)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}

	// The turn ended before the escape ran, and a local shell escape is not
	// agent activity — the session must still read as finished.
	if m.LastEventType != "turn_done" {
		t.Errorf("LastEventType = %q, want %q — a `!` shell escape runs locally with no "+
			"model turn, so it must not register as agent activity and strand the "+
			"session in working", m.LastEventType, "turn_done")
	}
	if m.HasOpenToolCall {
		t.Error("HasOpenToolCall = true, want false — the escape's own completion must not " +
			"leave an open tool call behind")
	}
}

// TestPlanModeAndAskUserAreUserBlocking pins the two Copilot builtins that
// block on the user but were missing from the exact-match user-blocking sets.
//
// Copilot's plan-mode gate is `exit_plan_mode` (snake_case) — the same concept
// as Claude Code's PascalCase `ExitPlanMode`, which IS listed. `ask_user` is
// Copilot's clarifying-question tool. Both write a persisted
// tool.execution_start and hold it open while the user decides, so without an
// entry the session sits in `working` while it is really waiting.
//
// The two sets are deliberately duplicated (domain can't import tailer), and
// their comments say KEEP THE TWO IN SYNC — so this asserts both.
func TestPlanModeAndAskUserAreUserBlocking(t *testing.T) {
	for _, name := range []string{"exit_plan_mode", "ask_user"} {
		t.Run(name, func(t *testing.T) {
			m := &session.SessionMetrics{
				HasOpenToolCall:   true,
				LastOpenToolNames: []string{name},
			}
			if !m.NeedsUserAttention() {
				t.Errorf("NeedsUserAttention() = false for an open %q — the user is blocked "+
					"on it, so the session must route to waiting", name)
			}
		})
	}
}

// TestPendingWaitingCueIsComputed pins the #1150-shaped gap: claudecode and
// codex both compute session.ProseIndicatesWaiting over a window TWICE the
// size of the display truncation, so a question that falls off the displayed
// tail is still detected. The copilot parser did not compute it at all, so
// only a question inside the display tail routed to waiting.
//
// The question is placed deliberately between MaxAssistantTextRunes (the
// display tail) and MaxWaitingScanRunes (the scan window) — the exact band the
// fix exists for. A question beyond MaxWaitingScanRunes is out of scope by
// design, not a bug.
func TestPendingWaitingCueIsComputed(t *testing.T) {
	filler := strings.Repeat("x", tailer.MaxAssistantTextRunes+tailer.MaxAssistantTextRunes/2)
	content := "Which of the two approaches would you prefer? " + filler

	if len([]rune(content)) <= tailer.MaxAssistantTextRunes {
		t.Fatal("test setup: the question must fall OUTSIDE the display tail to be meaningful")
	}
	if strings.Contains(tailer.TruncateAssistantText(content), "prefer") {
		t.Fatal("test setup: the question must not survive display truncation")
	}

	p := &Parser{}
	ev := p.ParseLine(map[string]any{
		"type": "assistant.message",
		"data": map[string]any{"model": "gpt-5-mini", "content": content},
	})

	if !ev.PendingWaitingCue {
		t.Error("PendingWaitingCue = false — a question past the display tail but inside " +
			"the waiting-scan window must still be detected (issue #1150)")
	}
}

// TestCompactionHoldsSessionWorking pins the context-compaction unlock.
//
// Copilot's compactHistory emits session.compaction_start /
// session.compaction_complete as ordinary (non-ephemeral) events into the same
// events.jsonl irrlicht tails. A manual /compact emits NO user.message and no
// assistant.turn_start/turn_end at all — so with no arm for either type both
// fell to the default Skip, and the session sat in `ready` for the whole
// summarization call while Copilot's own TUI showed a compacting spinner.
//
// Unlike Claude Code this needs neither IsManualCompactBoundary nor the
// hook-driven SignalCompactInProgress overlay: the boundary is on disk.
func TestCompactionHoldsSessionWorking(t *testing.T) {
	base := `{"type":"session.start","timestamp":"2026-08-03T09:00:00.000Z","data":{"context":{"cwd":"/tmp/p"}}}
{"type":"user.message","timestamp":"2026-08-03T09:00:01.000Z","data":{"content":"hello"}}
{"type":"assistant.message","timestamp":"2026-08-03T09:00:02.000Z","data":{"model":"gpt-5-mini","content":"hi","outputTokens":5}}
{"type":"assistant.turn_end","timestamp":"2026-08-03T09:00:03.000Z","data":{"turnId":"0"}}
`
	compacting := base + `{"type":"session.compaction_start","timestamp":"2026-08-03T09:00:20.000Z","data":{"trigger":"manual"}}` + "\n"
	done := compacting + `{"type":"session.compaction_complete","timestamp":"2026-08-03T09:00:40.000Z","data":{"trigger":"manual"}}` + "\n"

	for _, tc := range []struct {
		name          string
		lines         string
		wantEventType string
	}{
		{"compaction in progress is activity", compacting, "compaction_start"},
		{"compaction complete settles the session", done, "turn_done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tl := tailer.NewTranscriptTailer(writeTranscript(t, tc.lines), &Parser{}, AdapterName)
			m, err := tl.TailAndProcess()
			if err != nil {
				t.Fatalf("TailAndProcess: %v", err)
			}
			if m.LastEventType != tc.wantEventType {
				t.Errorf("LastEventType = %q, want %q", m.LastEventType, tc.wantEventType)
			}
		})
	}
}

// TestAutoPlaceholderIsNotAModel pins the "auto" placeholder fix. Copilot's
// session.model_change reports the literal "auto" before
// session.auto_mode_resolved names the concrete model. Accepting it surfaces
// "auto" as the session's model in the UI for the gap between the two events,
// and drives a pricing lookup that can only miss — a real recording logged
// `no pricing for model "auto"`.
func TestAutoPlaceholderIsNotAModel(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(map[string]any{
		"type": "session.model_change",
		"data": map[string]any{"newModel": "auto"},
	})
	if ev.ModelName != "" {
		t.Errorf("ModelName = %q after model_change:auto, want empty — "+
			"\"auto\" is the router's placeholder, not a model", ev.ModelName)
	}

	ev = p.ParseLine(map[string]any{
		"type": "session.auto_mode_resolved",
		"data": map[string]any{"chosenModel": "gpt-5-mini"},
	})
	if ev.ModelName != "gpt-5-mini" {
		t.Errorf("ModelName = %q after auto_mode_resolved, want gpt-5-mini", ev.ModelName)
	}
}

// TestContinuationTurnDoesNotFlashReady pins the mid-turn `ready` flash found
// by recording the auto-executed-tool-call and self-correction-iteration cells.
//
// Copilot closes a turn after every tool call and opens the next one ~50-80ms
// later, with NO user.message in between. assistant.turn_end settles the
// session to ready, and assistant.turn_start was skipped — justified in a
// comment as "the open bracket of a turn the user message already put into
// working", which holds for the FIRST turn and for no other. The session
// therefore read idle for the whole continuation: 122ms in one recording, and
// 5.24 SECONDS in another.
func TestContinuationTurnDoesNotFlashReady(t *testing.T) {
	lines := `{"type":"session.start","timestamp":"2026-08-03T10:44:00.000Z","data":{"context":{"cwd":"/tmp/p"}}}
{"type":"user.message","timestamp":"2026-08-03T10:44:01.000Z","data":{"content":"run it"}}
{"type":"assistant.turn_start","timestamp":"2026-08-03T10:44:02.000Z","data":{"turnId":"0"}}
{"type":"tool.execution_start","timestamp":"2026-08-03T10:44:03.000Z","data":{"toolCallId":"call_a","toolName":"bash"}}
{"type":"tool.execution_complete","timestamp":"2026-08-03T10:44:13.000Z","data":{"toolCallId":"call_a","success":true}}
{"type":"assistant.turn_end","timestamp":"2026-08-03T10:44:14.149Z","data":{"turnId":"0"}}
{"type":"assistant.turn_start","timestamp":"2026-08-03T10:44:14.228Z","data":{"turnId":"1"}}
`
	tl := tailer.NewTranscriptTailer(writeTranscript(t, lines), &Parser{}, AdapterName)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}

	if m.LastEventType == "turn_done" {
		t.Error("LastEventType = \"turn_done\" after a continuation turn_start — the agent " +
			"opened turn 1 immediately after closing turn 0 with no user message, so the " +
			"session must not read as finished")
	}
}
