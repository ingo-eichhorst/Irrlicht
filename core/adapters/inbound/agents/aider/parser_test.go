package aider

import (
	"strings"
	"testing"
	"time"

	"irrlicht/core/pkg/tailer"
)

// drive feeds a slice of lines through ParseLineRaw and returns every
// non-nil event the parser emitted, in order.
func drive(p *Parser, lines []string) []*tailer.ParsedEvent {
	var out []*tailer.ParsedEvent
	for _, line := range lines {
		if ev := p.ParseLineRaw(line); ev != nil {
			out = append(out, ev)
		}
	}
	return out
}

func TestParser_SatisfiesRawLineParser(t *testing.T) {
	var _ tailer.RawLineParser = &Parser{}
	var _ tailer.TranscriptParser = &Parser{}
}

func TestParser_ParseLine_IsNoOp(t *testing.T) {
	p := &Parser{}
	if ev := p.ParseLine(map[string]any{"anything": 1}); ev != nil {
		t.Errorf("ParseLine should be a no-op for raw-line parsers, got %+v", ev)
	}
}

func TestParser_BaselineHello_FullTurn(t *testing.T) {
	// Mirrors the sample transcript in issue #212.
	lines := []string{
		"# aider chat started at 2026-04-25 17:20:04",
		"",
		"> You can skip this check with --no-gitignore",
		"> Add .aider* to .gitignore (recommended)? (Y)es/(N)o [Yes]: y",
		"> /Users/ingo/.local/bin/aider --no-auto-commits --yes-always …",
		"> Aider v0.86.2",
		"> Model: openai/gemma-4-e2b-it-uncensored with whole edit format",
		"",
		"#### Reply with exactly the word: ok",
		"",
		"ok",
		"",
		"> Tokens: 771 sent, 1 received.",
	}

	p := &Parser{}
	events := drive(p, lines)
	if len(events) < 3 {
		t.Fatalf("expected at least 3 events (model, user, assistant), got %d", len(events))
	}

	// First non-nil event: the model declaration (Skip with ModelName).
	if !events[0].Skip || events[0].ModelName == "" {
		t.Errorf("first event should be Skip+ModelName, got %+v", events[0])
	}

	// User message.
	var userEv *tailer.ParsedEvent
	for _, e := range events {
		if e.EventType == "user_message" {
			userEv = e
			break
		}
	}
	if userEv == nil {
		t.Fatal("no user_message emitted")
	}
	if userEv.AssistantText != "Reply with exactly the word: ok" {
		t.Errorf("user text mismatch: got %q", userEv.AssistantText)
	}
	if !userEv.ClearToolNames {
		t.Error("user_message should clear tool names")
	}

	// `> Tokens:` emits assistant_message — NOT turn_done. The turn stays
	// open until IdleFlush fires; see issue #263.
	var asstEv *tailer.ParsedEvent
	for _, e := range events {
		if e.EventType == "assistant_message" {
			asstEv = e
			break
		}
	}
	if asstEv == nil {
		t.Fatal("no assistant_message emitted on `> Tokens:` line")
	}
	if asstEv.AssistantText != "ok" {
		t.Errorf("assistant text mismatch: got %q", asstEv.AssistantText)
	}
	if asstEv.Contribution == nil {
		t.Fatal("expected Contribution on assistant_message")
	}
	if asstEv.Contribution.Usage.Input != 771 || asstEv.Contribution.Usage.Output != 1 {
		t.Errorf("token counts wrong: in=%d out=%d", asstEv.Contribution.Usage.Input, asstEv.Contribution.Usage.Output)
	}
	for _, e := range events {
		if e.EventType == "turn_done" {
			t.Fatalf("turn_done must NOT be emitted by `> Tokens:`; only IdleFlush synthesizes it. Got %+v", e)
		}
	}

	// IdleFlush after the threshold synthesizes turn_done so the state
	// classifier transitions working → ready.
	flushed := p.IdleFlush(2 * time.Second)
	if flushed == nil || flushed.EventType != "turn_done" {
		t.Fatalf("IdleFlush should emit turn_done after threshold, got %+v", flushed)
	}
	if p.turnOpen {
		t.Error("IdleFlush should clear turnOpen")
	}
}

func TestParser_TokensWithCost(t *testing.T) {
	p := &Parser{}
	drive(p, []string{
		"> Model: openai/gpt-5 with diff edit format",
		"#### hi",
		"reply text",
	})
	ev := p.ParseLineRaw("> Tokens: 1.2k sent, 543 received, $0.0123 message, $0.0456 session.")
	if ev == nil || ev.Contribution == nil {
		t.Fatal("expected Contribution")
	}
	if ev.EventType != "assistant_message" {
		t.Errorf("expected assistant_message, got %q", ev.EventType)
	}
	if ev.Contribution.Usage.Input != 1200 {
		t.Errorf("expected 1.2k → 1200, got %d", ev.Contribution.Usage.Input)
	}
	if ev.Contribution.Usage.Output != 543 {
		t.Errorf("expected 543, got %d", ev.Contribution.Usage.Output)
	}
	if ev.Contribution.ProviderCostUSD == nil || *ev.Contribution.ProviderCostUSD != 0.0123 {
		t.Errorf("expected $0.0123 cost, got %v", ev.Contribution.ProviderCostUSD)
	}
}

func TestParser_ToolCall_AppliedEdit(t *testing.T) {
	p := &Parser{}
	drive(p, []string{
		"#### edit foo.go",
	})
	ev := p.ParseLineRaw("> Applied edit to foo.go")
	if ev == nil {
		t.Fatal("expected event for Applied edit")
	}
	if len(ev.ToolUses) != 1 || ev.ToolUses[0].Name != "Edit" {
		t.Errorf("expected one Edit ToolUse, got %+v", ev.ToolUses)
	}
	if len(ev.ToolResultIDs) != 1 || ev.ToolResultIDs[0] != ev.ToolUses[0].ID {
		t.Errorf("ToolResultID should match ToolUse.ID, got %+v / %+v", ev.ToolUses, ev.ToolResultIDs)
	}
}

func TestParser_ToolCall_RunningShell(t *testing.T) {
	p := &Parser{}
	drive(p, []string{"#### run something"})
	ev := p.ParseLineRaw("> Running echo hello")
	if ev == nil {
		t.Fatal("expected event for Running")
	}
	if len(ev.ToolUses) != 1 || ev.ToolUses[0].Name != "Bash" {
		t.Errorf("expected one Bash ToolUse, got %+v", ev.ToolUses)
	}
}

func TestParser_EmptyTurn_NoCrash(t *testing.T) {
	p := &Parser{}
	events := drive(p, []string{
		"#### ask",
		"> Tokens: 10 sent, 0 received.",
	})
	var asst *tailer.ParsedEvent
	for _, e := range events {
		if e.EventType == "assistant_message" {
			asst = e
		}
	}
	if asst == nil {
		t.Fatal("expected assistant_message even with empty body")
	}
	if asst.AssistantText != "" {
		t.Errorf("expected empty assistant text, got %q", asst.AssistantText)
	}
	if asst.ContentChars != 0 {
		t.Errorf("expected 0 ContentChars, got %d", asst.ContentChars)
	}
}

func TestParser_BlockquoteNoise_Skipped(t *testing.T) {
	p := &Parser{}
	for _, line := range []string{
		"> Aider v0.86.2",
		"> Use /help for commands",
		"> Repo-map can't include /tmp/foo (permission)",
	} {
		if ev := p.ParseLineRaw(line); ev != nil {
			t.Errorf("expected nil for noise line %q, got %+v", line, ev)
		}
	}
}

// TestParser_MainModel_AfterSlashCommand pins that `> Main model: …` (the
// line aider emits after a `/model` switch) is treated identically to
// `> Model: …`. Without this, model_changed scenarios silently fail because
// post-switch turns keep the original model name on Contribution.Model.
func TestParser_MainModel_AfterSlashCommand(t *testing.T) {
	p := &Parser{}
	drive(p, []string{
		"> Model: openai/gemma-4-26b-a4b with whole edit format",
		"#### first",
		"first reply",
		"> Tokens: 100 sent, 5 received.",
	})
	if p.model != "gemma-4-26b" && p.model != "openai/gemma-4-26b-a4b" {
		t.Errorf("initial model should be set, got %q", p.model)
	}

	// Simulate the `/model` switch.
	ev := p.ParseLineRaw("> Main model: openai/gemma-4-e2b-it-uncensored with whole edit format")
	if ev == nil || ev.ModelName == "" {
		t.Fatalf("expected Skip+ModelName for `> Main model: …`, got %+v", ev)
	}
	if !ev.Skip {
		t.Errorf("model line should set Skip=true, got %+v", ev)
	}
	if p.model == "openai/gemma-4-26b-a4b" || p.model == "gemma-4-26b" {
		t.Errorf("model should have switched away from gemma-4-26b, got %q", p.model)
	}
}

// TestParser_TrailingQuestionMark_PreservedForWaitingClassification pins the
// contract with the state classifier: when the assistant's last buffered line
// ends in `?`, the emitted assistant_message event's AssistantText must also
// end in `?`. The tailer feeds assistant_message.AssistantText into
// LastAssistantText, which session.IsWaitingForUserInput inspects on the
// subsequent (synthesized) turn_done. Don't relax this without updating the
// classifier.
func TestParser_TrailingQuestionMark_PreservedForWaitingClassification(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
	}{
		{
			name: "single-line question",
			lines: []string{
				"#### what next",
				"What would you like to do?",
				"> Tokens: 50 sent, 7 received.",
			},
		},
		{
			name: "multi-line question with trailing whitespace",
			lines: []string{
				"#### ask me",
				"I have two options for you.",
				"Which one would you prefer?   ",
				"> Tokens: 60 sent, 14 received.",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := drive(&Parser{}, tc.lines)
			var asst *tailer.ParsedEvent
			for _, e := range events {
				if e.EventType == "assistant_message" {
					asst = e
				}
			}
			if asst == nil {
				t.Fatal("no assistant_message emitted")
			}
			if asst.AssistantText == "" {
				t.Fatal("AssistantText must be non-empty for the classifier to inspect")
			}
			if last := asst.AssistantText[len(asst.AssistantText)-1]; last != '?' {
				t.Errorf("AssistantText must end in '?', got %q (full=%q)", last, asst.AssistantText)
			}
		})
	}
}

func TestParser_MultiTurn_StateResets(t *testing.T) {
	p := &Parser{}
	events := drive(p, []string{
		"> Model: openai/gpt-5 with whole edit format",
		"#### turn one",
		"reply one",
		"> Tokens: 100 sent, 50 received.",
		"#### turn two",
		"reply two has more text",
		"> Tokens: 200 sent, 100 received.",
	})

	asstCount := 0
	for _, e := range events {
		if e.EventType == "assistant_message" {
			asstCount++
		}
	}
	if asstCount != 2 {
		t.Errorf("expected 2 assistant_message events across two turns, got %d", asstCount)
	}
	// No turn_done in-band — the synthesized one comes from IdleFlush.
	for _, e := range events {
		if e.EventType == "turn_done" {
			t.Errorf("unexpected in-band turn_done: %+v", e)
		}
	}
}

// TestParser_MultiModelCall_KeepsTurnOpen reproduces the issue #263 flow:
// aider with `--yes-always` emits multiple `> Tokens:` lines within one
// `####` user turn (one per model call). The parser must keep the turn
// open across them — emitting assistant_message per call, never an in-band
// turn_done — so the session stays `working` and prose between token
// lines is captured. Pre-fix, the first `> Tokens:` flipped the session
// to `ready` and dropped all subsequent assistant prose.
func TestParser_MultiModelCall_KeepsTurnOpen(t *testing.T) {
	p := &Parser{}
	events := drive(p, []string{
		"> Model: openai/gpt-5 with whole edit format",
		"#### can aider work in an agentic loop?",
		"I need to see the actual files. Please add them to the chat.",
		"> Tokens: 2.0k sent, 227 received.",
		"> core/pkg/tailer/parser.go",
		"> Add file to the chat? (Y)es/(N)o/(A)ll/(S)kip all/(D)on't ask again [Yes]: y",
		"Now examining the parser. The current implementation buffers prose between markers.",
		"> Tokens: 5.5k sent, 412 received.",
	})

	if !p.turnOpen {
		t.Error("turn must remain open across multiple `> Tokens:` lines within one ####")
	}

	asstCount := 0
	var lastAsst *tailer.ParsedEvent
	for _, e := range events {
		if e.EventType == "assistant_message" {
			asstCount++
			lastAsst = e
		}
		if e.EventType == "turn_done" {
			t.Errorf("turn_done must NOT be emitted in-band on `> Tokens:`; got %+v", e)
		}
	}
	if asstCount != 2 {
		t.Errorf("expected 2 assistant_message events (one per model call), got %d", asstCount)
	}

	// The second model call's prose must reach LastAssistantText. Pre-fix,
	// turnOpen was false after the first `> Tokens:`, so this prose was
	// silently dropped.
	if lastAsst == nil {
		t.Fatal("no assistant_message captured")
	}
	if !strings.Contains(lastAsst.AssistantText, "Now examining the parser") {
		t.Errorf("second-call prose missing from assistant_message; got %q", lastAsst.AssistantText)
	}
}

// TestParser_IdleFlush_AfterMultipleModelCalls_EmitsTurnDone is the
// counterpart to MultiModelCall_KeepsTurnOpen: once aider has truly gone
// idle (no transcript activity for ≥ aiderIdleTurnDoneAfter), IdleFlush
// must synthesize the turn_done so the state classifier transitions
// working → ready. Per-call tokens are already accumulated via the
// assistant_message events, so this synthesized event carries no payload.
func TestParser_IdleFlush_AfterMultipleModelCalls_EmitsTurnDone(t *testing.T) {
	p := &Parser{}
	drive(p, []string{
		"#### multi-step",
		"first step",
		"> Tokens: 100 sent, 10 received.",
		"second step",
		"> Tokens: 200 sent, 20 received.",
	})

	// Below threshold: no flush.
	if ev := p.IdleFlush(500 * time.Millisecond); ev != nil {
		t.Errorf("IdleFlush below threshold should return nil, got %+v", ev)
	}
	if !p.turnOpen {
		t.Fatal("below-threshold IdleFlush must not close the turn")
	}

	// At/above threshold: synthesize turn_done.
	ev := p.IdleFlush(aiderIdleTurnDoneAfter)
	if ev == nil || ev.EventType != "turn_done" {
		t.Fatalf("IdleFlush at threshold should emit turn_done, got %+v", ev)
	}
	if p.turnOpen {
		t.Error("IdleFlush must clear turnOpen after synthesizing turn_done")
	}

	// Subsequent calls return nil — turn is closed.
	if ev := p.IdleFlush(10 * time.Second); ev != nil {
		t.Errorf("IdleFlush after close should return nil, got %+v", ev)
	}
}

func TestParser_IdleFlush_NoTurnOpen(t *testing.T) {
	p := &Parser{}
	if ev := p.IdleFlush(10 * time.Second); ev != nil {
		t.Errorf("IdleFlush on fresh parser should return nil, got %+v", ev)
	}
}

// TestParser_NewUserPromptAfterMultiCall_StaysWorking pins that a `####`
// arriving while a turn is still open (e.g. user typed before IdleFlush
// fired) opens the new turn cleanly. The previous turn's per-call
// contributions were already emitted at each `> Tokens:`, so no
// synthesized turn_done is needed — the session stays `working` across
// the boundary. Tested separately because the parser does NOT emit a
// terminal event for the previous turn here.
func TestParser_NewUserPromptAfterMultiCall_StaysWorking(t *testing.T) {
	p := &Parser{}
	events := drive(p, []string{
		"#### turn one",
		"reply",
		"> Tokens: 100 sent, 10 received.",
		"#### turn two",
	})

	// Order: user_message, assistant_message, user_message.
	var types []string
	for _, e := range events {
		types = append(types, e.EventType)
	}
	want := []string{"user_message", "assistant_message", "user_message"}
	if len(types) != len(want) {
		t.Fatalf("event sequence: got %v, want %v", types, want)
	}
	for i, wt := range want {
		if types[i] != wt {
			t.Errorf("event[%d] = %q, want %q (full %v)", i, types[i], wt, types)
		}
	}
	if !p.turnOpen {
		t.Error("new #### must open a new turn")
	}
}

