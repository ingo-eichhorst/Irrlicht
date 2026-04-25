package aider

import (
	"testing"

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

	events := drive(&Parser{}, lines)
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

	// Assistant turn close.
	var asstEv *tailer.ParsedEvent
	for _, e := range events {
		if e.EventType == "turn_done" {
			asstEv = e
			break
		}
	}
	if asstEv == nil {
		t.Fatal("no turn_done emitted")
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
	// One user_message, one assistant_message (with empty text).
	var asst *tailer.ParsedEvent
	for _, e := range events {
		if e.EventType == "turn_done" {
			asst = e
		}
	}
	if asst == nil {
		t.Fatal("expected turn_done even with empty body")
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
		if e.EventType == "turn_done" {
			asstCount++
		}
	}
	if asstCount != 2 {
		t.Errorf("expected 2 turn_done events across two turns, got %d", asstCount)
	}
}
